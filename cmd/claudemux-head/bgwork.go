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
// Whether a launch happened is decided by the TOOL that produced the
// tool_result, never by the result's text; the text only supplies the id.
// Completions are recognized by the notification that carries the same id
// back. See docs/superpowers/specs/2026-08-11-background-work-state-design.md
// for the verified event shapes.
//
// Text-only launch detection was tried twice and cannot work: no pattern
// distinguishes "a launch happened" from "text about a launch", because a
// session that reads or greps a document quoting a launch payload produces a
// tool_result containing exactly those bytes. Two rounds of anchoring were
// each defeated by a different quoting shape. A tool_result, by contrast,
// cannot fabricate the `tool_use` block that produced it — so gating on tool
// identity closes the whole class rather than the instances found so far. A
// false positive publishes Background for an idle session, hiding it from the
// conductor for up to bgMaxAge; that is worse than not having the feature,
// which is why the gate errs toward missing launches.
var (
	// Unanchored, deliberately: with the tool gate doing the guarding, the
	// anchors of the previous rounds bought nothing and cost real launches —
	// a sweep of this machine's transcripts found 40 of 803 genuine shell
	// launch payloads that do not begin at byte 0.
	bgShellRe = regexp.MustCompile(`running in background with ID: ([A-Za-z0-9]+)`)
	bgAgentRe = regexp.MustCompile(`agentId: ([A-Za-z0-9]+)`)
	// The agent tool dispatches foreground agents from the same tool as async
	// ones, and a foreground result is the agent's final report rather than a
	// launch acknowledgement. Tool identity says "this is an agent dispatch";
	// this sentence says "and it was an async one". That is a semantic
	// distinction the structure genuinely does not carry, not a text
	// workaround — and it is safe here because the gate has already
	// established that an agent really was dispatched.
	bgAgentLaunchMarker = "Async agent launched"
	bgTaskIDRe          = regexp.MustCompile(`<task-id>([A-Za-z0-9]+)</task-id>`)
)

// bgAgentToolName is the tool Claude Code dispatches subagents with. Verified
// against every transcript under ~/.claude/projects on this machine: all 1648
// results carrying the async-launch sentence came from a tool_use named
// exactly `Agent` (1696 total dispatches; the remainder are foreground agents).
// No other name produces one — the handful of other tools whose results
// contain the sentence (Read, Bash, AskUserQuestion) are the false positives
// this gate exists to reject.
const bgAgentToolName = "Agent"

// bgNotificationPrefix opens a task-notification payload. Recognition is a
// PREFIX check, never a substring search: the literal tag appears in ordinary
// prose — skill documentation quotes it — and a substring match would retire
// tasks that are still running.
const bgNotificationPrefix = "<task-notification>"

// bgLaunchKind is what a pending tool_use would launch if it succeeds. The two
// kinds read their result differently, so the kind has to be remembered
// alongside the tool_use id.
type bgLaunchKind int

const (
	bgKindShell bgLaunchKind = iota
	bgKindAgent
)

// bgLaunchKindOf reports whether this tool_use is one that can start
// background work. Only these are worth remembering; recording every tool_use
// a session makes would grow without bound for no gain.
func bgLaunchKindOf(tu ToolUse) (bgLaunchKind, bool) {
	switch tu.Name {
	case "Bash":
		// Input is map[string]interface{}, so the flag arrives as a JSON
		// bool; missing or any other type means a foreground shell.
		if bg, ok := tu.Input["run_in_background"].(bool); ok && bg {
			return bgKindShell, true
		}
	case bgAgentToolName:
		// An async agent carries no input flag — foreground and async
		// dispatches are the same call — so every agent dispatch is pending
		// until its result says which it was.
		return bgKindAgent, true
	}
	return 0, false
}

// bgPending is a launch-capable tool_use whose result has not arrived yet.
type bgPending struct {
	kind bgLaunchKind
	at   time.Time
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
// finish, keyed by task id and stamped with the launch time, plus the
// launch-capable tool_uses whose results have not arrived yet.
//
// Accumulated from each poll's NEW events rather than recomputed from the event
// ring: allEvents is capped at 1000, so a busy session scrolls a long-running
// task's launch out while it is still running, and a recompute would silently
// call the session Idle again.
type bgTracker struct {
	tasks map[string]time.Time
	// pending holds tool_use id → what it would launch. It must survive
	// between observe calls: the assistant turn calling the tool and the user
	// turn carrying its result routinely land in different polls.
	pending map[string]bgPending
}

func newBgTracker() bgTracker {
	return bgTracker{tasks: map[string]time.Time{}, pending: map[string]bgPending{}}
}

// observe folds one batch of new events into the tracker.
func (b *bgTracker) observe(events []Event, now time.Time) {
	if b.tasks == nil {
		b.tasks = map[string]time.Time{}
	}
	if b.pending == nil {
		b.pending = map[string]bgPending{}
	}
	for _, e := range events {
		// A prompt the human actually typed retires everything: they are
		// looking at the session, so what it was waiting on no longer decides
		// whether it needs them. genuinePrompt already rejects the delivered
		// notification turns (their text opens with "<"). pending survives:
		// it is not tracked work but an unresolved observation, and its
		// result still reports truthfully whether a launch happened.
		if genuinePrompt(e) {
			b.tasks = map[string]time.Time{}
			continue
		}
		at := parseTimestampOr(e.Timestamp, now)
		for _, tu := range e.ToolUses {
			if kind, ok := bgLaunchKindOf(tu); ok {
				b.pending[tu.ID] = bgPending{kind: kind, at: at}
			}
		}
		for _, id := range bgCompletions(e) {
			delete(b.tasks, id)
		}
		for _, id := range b.launches(e) {
			b.tasks[id] = at
		}
	}
}

// launches returns the ids of background tasks this event's results started,
// consuming the pending tool_uses they resolve.
func (b *bgTracker) launches(e Event) []string {
	var ids []string
	for _, tr := range e.ToolResults {
		p, ok := b.pending[tr.ToolUseID]
		if !ok {
			// The result did not come from a launch-capable tool. Whatever
			// its text says — including a verbatim copy of a launch payload
			// out of a document — nothing was launched.
			continue
		}
		// A tool_use produces exactly one result, launch or not.
		delete(b.pending, tr.ToolUseID)
		var re *regexp.Regexp
		switch p.kind {
		case bgKindShell:
			re = bgShellRe
		case bgKindAgent:
			if !strings.Contains(tr.Content, bgAgentLaunchMarker) {
				continue // a foreground agent's final report
			}
			re = bgAgentRe
		}
		if m := re.FindStringSubmatch(tr.Content); m != nil {
			ids = append(ids, m[1])
		}
	}
	return ids
}

// outstanding reports how many launches are still counting and when the oldest
// of them started. Entries past bgMaxAge are dropped as they are found, so a
// stale tracker heals itself without a separate sweep — pending tool_uses
// included, so a session killed mid-tool cannot grow that map without limit.
func (b *bgTracker) outstanding(now time.Time) (int, time.Time) {
	for id, p := range b.pending {
		if now.Sub(p.at) > bgMaxAge {
			delete(b.pending, id)
		}
	}
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
