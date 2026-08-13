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
// tool_result entry — Event.BgTaskID and Event.BgAgentID, read from the
// top-level `toolUseResult` — never by the result's text. Completions are
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
var bgTaskIDRe = regexp.MustCompile(`<task-id>([A-Za-z0-9]+)</task-id>`)

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
		for _, id := range bgCompletions(e) {
			delete(b.tasks, id)
		}
		for _, l := range bgLaunches(e) {
			b.tasks[l.ID] = bgTask{at: at, agent: l.Agent}
		}
	}
}

// outstanding reports how many launches are still counting and when the
// oldest of them started. Dead entries are dropped as they are found, so a
// stale tracker heals itself without a separate sweep.
func (b *bgTracker) outstanding(now time.Time) (int, time.Time) {
	var oldest time.Time
	for id, task := range b.tasks {
		if !b.alive(id, task, now) {
			delete(b.tasks, id)
			continue
		}
		if oldest.IsZero() || task.at.Before(oldest) {
			oldest = task.at
		}
	}
	return len(b.tasks), oldest
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
		return now.Sub(task.at) <= bgAgentSpawnGrace
	}
	return now.Sub(fi.ModTime()) <= bgAgentStallAge
}
