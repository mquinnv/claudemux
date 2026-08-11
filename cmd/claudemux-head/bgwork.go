package main

import (
	"regexp"
	"strings"
	"time"
)

// Tracking work a session launched and then stopped waiting on: async agents
// and run_in_background shells. Both resolve their tool_use at LAUNCH, so the
// main thread's turn ends and classifyState would otherwise call the session
// Idle — which isWaiting reads as "waiting on the human", sending the
// switchboard's conductor into a session that is busy.
//
// Launches are recognized by the text of the tool_result that started them;
// completions by the notification that carries the same id back. See
// docs/superpowers/specs/2026-08-11-background-work-state-design.md for the
// verified event shapes.

var (
	// Anchored to the ABSOLUTE start of the tool_result text — no `(?m)` —
	// not merely the start of a line. Per the design spec, a background
	// shell's tool_result.content IS that sentence: nothing precedes it in a
	// real payload. `(?m)^` (start of any line) was tried first and still
	// false-positived on this repo's own design spec, which quotes the exact
	// sentence as a standalone paragraph in a fenced code block — a real
	// physical line in the raw markdown, so a per-line anchor still matched
	// it. Anchoring to the whole string's start instead requires the sentence
	// to be the FIRST thing in the tool_result, which a doc being read never
	// is (whatever text precedes the quoted line in the file is included in
	// the same tool_result content ahead of it).
	bgShellRe = regexp.MustCompile(`^Command running in background with ID: ([A-Za-z0-9]+)`)
	// agentId is ALSO anchored to the start of a line, not just gated on the
	// launch-sentence substring: the design spec (and this repo's plan doc,
	// and our own test fixtures) quote the real async-agent payload as raw,
	// unparsed JSON text, where the payload's "\n" is two literal characters
	// on ONE physical line — so an unanchored-but-gated search still matched
	// a doc that merely quotes both strings anywhere in the same tool_result.
	// A REAL tool_result has already been through flattenText's json.Unmarshal,
	// which decodes that escape into an actual newline, so "agentId:" only
	// begins a true physical line in real data. bgLaunches still requires the
	// launch sentence in the same tool_result too, matching bgShellRe's
	// "Command " lead-in requirement: this trades a possible false negative if
	// the harness rephrases either string (degrades to pre-branch behavior)
	// for immunity to a session merely reading text that quotes one — a false
	// positive hides a session from the conductor for up to bgMaxAge, which is
	// the worse failure.
	bgAgentRe           = regexp.MustCompile(`(?m)^agentId: ([A-Za-z0-9]+)`)
	bgAgentLaunchMarker = "Async agent launched"
	bgTaskIDRe          = regexp.MustCompile(`<task-id>([A-Za-z0-9]+)</task-id>`)
)

// bgNotificationPrefix opens a task-notification payload. Recognition is a
// PREFIX check, never a substring search: the literal tag appears in ordinary
// prose — skill documentation quotes it — and a substring match would retire
// tasks that are still running.
const bgNotificationPrefix = "<task-notification>"

// bgLaunches returns the ids of background tasks this event launched.
func bgLaunches(e Event) []string {
	var ids []string
	for _, tr := range e.ToolResults {
		if m := bgShellRe.FindStringSubmatch(tr.Content); m != nil {
			ids = append(ids, m[1])
		}
		// Gated on the launch sentence, not the id pattern alone — see
		// bgAgentRe's comment.
		if strings.Contains(tr.Content, bgAgentLaunchMarker) {
			if m := bgAgentRe.FindStringSubmatch(tr.Content); m != nil {
				ids = append(ids, m[1])
			}
		}
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
		for _, id := range bgCompletions(e) {
			delete(b.tasks, id)
		}
		for _, id := range bgLaunches(e) {
			b.tasks[id] = parseTimestampOr(e.Timestamp, now)
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
