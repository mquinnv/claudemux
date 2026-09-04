package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Tracking work a session launched and then stopped waiting on: async agents
// and background shells. Both resolve their tool_use at LAUNCH, so the main
// thread's turn ends and classifyState would otherwise call the session Idle —
// which isWaiting reads as "waiting on the human", sending the switchboard's
// conductor into a session that is busy.
//
// Whether a launch happened is decided by the harness's own record on the
// tool_result entry — Event.BgTaskID, Event.BgAgentID and
// Event.BgQueuedAgentID, read from the top-level `toolUseResult` — never by
// the result's text. That covers all five ways background work starts: a
// backgrounded shell, an async agent, a forked background skill, a SendMessage
// that RESUMES a stopped agent, and a SendMessage the harness only QUEUED,
// which sets an agent running again after its previous run has already
// notified (see extractLaunch for each record's shape). Completions are
// recognized by the notification that carries the same id back. See
// docs/superpowers/specs/2026-08-11-background-work-state-design.md for the
// verified event shapes.
//
// Text detection was tried twice and cannot work: no pattern distinguishes "a
// launch happened" from "text about a launch", because a session that reads or
// greps a document quoting a launch payload produces a tool_result containing
// exactly those bytes — this repo's own spec is such a document. It also missed
// ~100 real launches, since Claude Code backgrounds a Bash on its own when it
// overruns its timeout and announces that in wordings no pattern knew. The
// harness field is immune to both: a command's stdout lands in
// `toolUseResult.stdout` and cannot fabricate a sibling key, and the field is
// written for every backgrounding however it was triggered.

// bgTaskIDRe pulls the id out of a completion notification. That id is the same
// string the launch recorded — verified across this machine's transcripts,
// where every notified id matches either a backgroundTaskId or an agentId.
//
// The id charset is deliberately "anything that isn't markup or whitespace",
// not [A-Za-z0-9]: a NAMED fork is notified under an id the harness builds from
// its prompt, e.g. `awhat-is-apiwebhookscallr-53690e0dfb7cf9f8`. An id the
// pattern cannot express is worse than a missed launch — the completion goes
// unrecognized, so the task is only ever retired by an expiry timer and the
// session reads Background for minutes after its agent finished.
var bgTaskIDRe = regexp.MustCompile(`<task-id>([^<>\s]+)</task-id>`)

// bgNotificationPrefix opens a task-notification payload. Recognition is a
// PREFIX check, never a substring search: the literal tag appears in ordinary
// prose — skill documentation quotes it — and a substring match would retire
// tasks that are still running.
const bgNotificationPrefix = "<task-notification>"

// bgLaunch is one background launch as the harness recorded it: the id the
// completion notification will carry back, and which kind of work it is —
// the kinds expire differently (see the constants below).
type bgLaunch struct {
	ID    string
	Agent bool
	// Queued marks the weak record: a SendMessage the harness only queued,
	// which starts an agent running again when its previous run had already
	// notified, but which is also what a message to a non-agent recipient
	// looks like. Such a launch counts only while the agent's own transcript
	// proves it — no spawn grace, since the file is already there.
	Queued bool
}

// bgLaunches returns the background work this event started. An entry
// carries at most one.
func bgLaunches(e Event) []bgLaunch {
	var ids []bgLaunch
	if e.BgTaskID != "" {
		ids = append(ids, bgLaunch{ID: e.BgTaskID})
	}
	if e.BgAgentID != "" {
		ids = append(ids, bgLaunch{ID: e.BgAgentID, Agent: true})
	}
	if e.BgQueuedAgentID != "" {
		ids = append(ids, bgLaunch{ID: e.BgQueuedAgentID, Agent: true, Queued: true})
	}
	return ids
}

// bgCompletions returns the ids this event reports as finished. Both delivery
// forms count: the queue-operation that lands the moment the task ends, and the
// user turn carrying the same payload once the session wakes to consume it.
// Retiring an id twice is harmless, and handling both means the feature
// survives either form going away.
func bgCompletions(e Event) []string {
	var ids []string
	for _, text := range []string{e.QueueText, e.UserText} {
		if !strings.HasPrefix(strings.TrimSpace(text), bgNotificationPrefix) {
			continue
		}
		if m := bgTaskIDRe.FindStringSubmatch(text); m != nil {
			ids = append(ids, m[1])
		}
	}
	return ids
}

// Expiry, per kind. A task that never notifies — killed, crashed, launched
// before a head restart — must not mark the session busy forever: the
// conductor would never visit it again, a worse bug than a stale count.
//
// Async agents have ground truth on disk: the harness writes each agent's own
// transcript at <transcript dir>/<session id>/subagents/agent-<id>.jsonl and
// its mtime advances while the agent runs (longest observed gap between
// writes is a single long tool call, bounded by Bash's 10-minute cap).
// bgAgentStallAge past the last write means the agent is gone however it
// died; bgAgentMaxAge backstops a wedged agent that keeps writing forever;
// bgAgentSpawnGrace covers the moment between the launch record and the
// file's creation. Fleet measurement 2026-08-13: real agents run hours (max
// observed 11h), which is why a flat 30-minute cap cannot work for them.
//
// Background shells leave no per-task file, so they keep the flat cap. It
// stays at 30 minutes deliberately: a backgrounded dev server runs (and
// stays silent) indefinitely, and a longer cap would hide its session from
// the conductor for the whole overrun.
const (
	bgShellMaxAge     = 30 * time.Minute
	bgAgentMaxAge     = 24 * time.Hour
	bgAgentStallAge   = 15 * time.Minute
	bgAgentSpawnGrace = 2 * time.Minute
)

// bgTask is one tracked launch: when it started and which expiry regime
// applies to it.
type bgTask struct {
	at    time.Time
	agent bool
	// queued is bgLaunch.Queued carried forward: this entry rests on a
	// queued SendMessage rather than a launch record, so it never gets the
	// spawn grace.
	queued bool
}

// bgTracker holds the background tasks a session has launched and not yet
// seen finish, keyed by task id.
//
// Accumulated from observed events rather than recomputed from the event
// ring: allEvents is capped, so a busy session scrolls a long-running task's
// launch out while it is still running, and a recompute would silently call
// the session Idle again.
type bgTracker struct {
	tasks map[string]bgTask
	// subagentsDir is where this session's async agents write their own
	// transcripts (see subagentsDirFor). Empty means no liveness source at
	// all (tests only — production always sets one from the transcript
	// path): agents then fall back to the shell cap rather than counting
	// forever.
	//
	// An unexpected production layout is a DIFFERENT failure and does not
	// land here: subagentsDirFor always returns a non-empty path, so if
	// Claude Code ever moves where it writes agent transcripts,
	// subagentsDir stays non-empty but points at a directory whose agent
	// files never appear. That never reaches the empty-dir fallback above —
	// alive's os.Stat keeps erroring, so every agent expires via
	// bgAgentSpawnGrace (~2 minutes after launch) instead. If agent
	// detection ever seems to be quietly capping out fast, this is where to
	// look first.
	subagentsDir string

	// expired counts launches that outstanding dropped because their
	// liveness regime gave up on them — a cap or a stalled transcript —
	// never because a completion arrived. expiredAt is when the latest such
	// drop happened.
	//
	// An expiry is a guess about work the head can no longer see. Publishing
	// a confident Idle on it is what sent the conductor into a session
	// whose pane still read "2 shells still running" (2026-09-04, a hung
	// ssh past bgShellMaxAge). classifyState turns a non-zero count into
	// StateUnsure, which isWaiting does not match. The doubt clears on the
	// next conversation event newer than expiredAt (see observe): a human
	// prompt or an assistant turn means someone engaged, and the Stop that
	// follows is a real one.
	expired   int
	expiredAt time.Time
}

// subagentsDirFor maps a session transcript path to the directory holding
// that session's per-agent transcripts, as Claude Code lays them out:
// <dir>/<session id>/subagents/.
func subagentsDirFor(jsonlPath string) string {
	base := strings.TrimSuffix(filepath.Base(jsonlPath), ".jsonl")
	return filepath.Join(filepath.Dir(jsonlPath), base, "subagents")
}

func newBgTracker() bgTracker {
	return bgTracker{tasks: map[string]bgTask{}}
}

// observe folds one batch of new events into the tracker.
func (b *bgTracker) observe(events []Event, now time.Time) {
	if b.tasks == nil {
		b.tasks = map[string]bgTask{}
	}
	for _, e := range events {
		at := parseTimestampOr(e.Timestamp, now)

		// A newer conversation event resolves any doubt an expiry left —
		// bookkeeping types (attachment, system, snapshots) are written with
		// nobody present and do not count.
		if b.expired > 0 && (e.Type == "user" || e.Type == "assistant") && at.After(b.expiredAt) {
			b.expired = 0
			b.expiredAt = time.Time{}
		}

		for _, id := range bgCompletions(e) {
			delete(b.tasks, id)
		}
		for _, l := range bgLaunches(e) {
			// A queued message to work already being counted changes
			// nothing — least of all when that work started, which is what
			// the switchboard shows as the session's Background age.
			if _, tracked := b.tasks[l.ID]; tracked && l.Queued {
				continue
			}
			b.tasks[l.ID] = bgTask{at: at, agent: l.Agent, queued: l.Queued}
		}
	}
}

// outstanding reports how many launches are still counting and when the
// oldest of them started. Dead entries are dropped as they are found, so a
// stale tracker heals itself without a separate sweep — but each drop is
// remembered in expired until the next conversation turn (see unsure).
func (b *bgTracker) outstanding(now time.Time) (int, time.Time) {
	var oldest time.Time
	for id, task := range b.tasks {
		if !b.alive(id, task, now) {
			delete(b.tasks, id)
			b.expired++
			b.expiredAt = now
			continue
		}
		if oldest.IsZero() || task.at.Before(oldest) {
			oldest = task.at
		}
	}
	return len(b.tasks), oldest
}

// unsure reports how many launches the tracker gave up on since the last
// conversation turn — dropped by expiry, not retired by a completion. Zero
// means every retirement so far was a fact.
func (b *bgTracker) unsure() int {
	return b.expired
}

// alive decides whether an unfinished launch still counts — see the expiry
// constants for the regime rationale. The os.Stat here runs per outstanding
// agent per poll (~1/s), a handful of stats at most.
func (b *bgTracker) alive(id string, task bgTask, now time.Time) bool {
	if !task.agent {
		return now.Sub(task.at) <= bgShellMaxAge
	}
	if now.Sub(task.at) > bgAgentMaxAge {
		return false
	}
	if b.subagentsDir == "" {
		return now.Sub(task.at) <= bgShellMaxAge
	}
	fi, err := os.Stat(filepath.Join(b.subagentsDir, "agent-"+id+".jsonl"))
	if err != nil {
		// No file yet. A real launch is allowed the moment it takes the
		// harness to create one; a queued SendMessage is not, because an
		// agent already running has written its transcript long since, so a
		// missing file means the recipient was never an agent of this
		// session and nothing here is running.
		return !task.queued && now.Sub(task.at) <= bgAgentSpawnGrace
	}
	return now.Sub(fi.ModTime()) <= bgAgentStallAge
}
