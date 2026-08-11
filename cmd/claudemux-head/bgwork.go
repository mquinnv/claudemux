package main

import (
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

// bgLaunches returns the ids of background work this event started, as the
// harness itself recorded them. An entry carries at most one.
func bgLaunches(e Event) []string {
	var ids []string
	if e.BgTaskID != "" {
		ids = append(ids, e.BgTaskID)
	}
	if e.BgAgentID != "" {
		ids = append(ids, e.BgAgentID)
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

// bgMaxAge is how long an unfinished launch keeps counting. A task that never
// notifies — killed, crashed, or launched before a head restart — would
// otherwise mark the session busy forever, and the conductor would never visit
// it again: a worse bug than the one this fixes. Thirty minutes is longer than
// any agent this has been observed to run and short enough to self-heal within
// one sitting.
const bgMaxAge = 30 * time.Minute

// bgTracker holds the background tasks a session has launched and not yet seen
// finish, keyed by task id and stamped with the launch time.
//
// Accumulated from each poll's NEW events rather than recomputed from the event
// ring: allEvents is capped at 1000, so a busy session scrolls a long-running
// task's launch out while it is still running, and a recompute would silently
// call the session Idle again.
type bgTracker struct {
	tasks map[string]time.Time
}

func newBgTracker() bgTracker {
	return bgTracker{tasks: map[string]time.Time{}}
}

// observe folds one batch of new events into the tracker.
func (b *bgTracker) observe(events []Event, now time.Time) {
	if b.tasks == nil {
		b.tasks = map[string]time.Time{}
	}
	for _, e := range events {
		// A prompt the human actually typed retires everything: they are
		// looking at the session, so what it was waiting on no longer decides
		// whether it needs them. genuinePrompt already rejects the delivered
		// notification turns (their text opens with "<").
		if genuinePrompt(e) {
			b.tasks = map[string]time.Time{}
			continue
		}
		at := parseTimestampOr(e.Timestamp, now)
		for _, id := range bgCompletions(e) {
			delete(b.tasks, id)
		}
		for _, id := range bgLaunches(e) {
			b.tasks[id] = at
		}
	}
}

// outstanding reports how many launches are still counting and when the oldest
// of them started. Entries past bgMaxAge are dropped as they are found, so a
// stale tracker heals itself without a separate sweep.
func (b *bgTracker) outstanding(now time.Time) (int, time.Time) {
	var oldest time.Time
	for id, at := range b.tasks {
		if now.Sub(at) > bgMaxAge {
			delete(b.tasks, id)
			continue
		}
		if oldest.IsZero() || at.Before(oldest) {
			oldest = at
		}
	}
	return len(b.tasks), oldest
}
