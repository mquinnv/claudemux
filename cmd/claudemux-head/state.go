package main

import (
	"strconv"
	"time"
)

type StateKind int

const (
	StateIdle StateKind = iota
	StateThinking
	StateTool
	StateAwaiting
	StateError
	StateCompacting
	// StateBackground: the main thread's turn ended, but work it launched —
	// an async agent, a run_in_background shell — is still running. Distinct
	// from Idle because Idle means "waiting on the human", and this session
	// is not.
	StateBackground
	// StateAsking: an AskUserQuestion is on screen, blocked on the human.
	// This can NEVER be derived from the transcript: Claude Code flushes the
	// assistant tool_use event only when the question is answered, so while
	// one is pending the transcript just ends at the preceding thinking/text
	// event and classifyState honestly reads Idle or Thinking. The signal
	// comes from the marker hooks/claudemux-ask.sh writes at PreToolUse —
	// see askOverride.
	StateAsking
	// StateWaiting: the head is bound to a project with no transcript yet — a
	// brand-new project whose claude pane is still booting. Not derivable
	// from events (there are none); set by recomputeFromEvents while the
	// model has no session (see waitingTranscript). Distinct from Idle so the
	// conductor doesn't escort a human into a session that can't take input
	// yet, and so the pane says "Starting" instead of a misleading "Idle".
	StateWaiting
	// StateUnsure: the main thread's turn ended and the head stopped
	// counting background work because a liveness cap gave up on it — not
	// because the work was seen to finish. It is Idle with an asterisk: the
	// transcript says the turn ended, but the pane may well still read
	// "2 shells still running". Distinct from Idle so isWaiting does not
	// match it and the conductor does not escort a human into it; distinct
	// from Background because nothing is known to be running. Clears on the
	// next conversation event (see bgTracker.expired).
	StateUnsure
)

type State struct {
	Kind     StateKind
	ToolName string // populated for StateTool / StateAwaiting
	Since    time.Time
	BgCount  int // outstanding background tasks (StateBackground) or tasks given up on (StateUnsure)

	// Anchored: Since came from an event timestamp (or a launch time), not a
	// now-fallback. Only an anchored Since is a real episode boundary the
	// publisher may key change-detection on; an unanchored one differs every
	// poll by construction.
	Anchored bool
}

func classifyState(events []Event, bgCount int, bgOldest time.Time, bgUnsure int, now time.Time) State {
	if len(events) == 0 {
		return State{Kind: StateIdle, Since: now}
	}

	// Build set of tool_use IDs that have been resolved.
	resolved := map[string]bool{}
	for _, e := range events {
		for _, tr := range e.ToolResults {
			resolved[tr.ToolUseID] = true
		}
	}

	// Find the most recent unresolved tool_use, if any. We used to flip
	// to "Awaiting" after 15s, on the theory that a long-stuck tool_use
	// meant Claude was blocked on user permission. In practice many
	// legitimate tools (Agent, long Bash, WebFetch) run multi-minutes,
	// and the false-positive rate made the signal useless. Just report
	// Tool with the elapsed duration and let the human read the clock.
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		for _, tu := range e.ToolUses {
			if resolved[tu.ID] {
				continue
			}
			since := parseTimestamp(e.Timestamp)
			anchored := !since.IsZero()
			if !anchored {
				since = now
			}
			return State{Kind: StateTool, ToolName: tu.Name, Since: since, Anchored: anchored}
		}
	}

	// No unresolved tool_use. Last *conversation* event determines state.
	// Claude Code interleaves bookkeeping events (attachment, last-prompt,
	// file-history-snapshot, queue-operation, system) with real
	// user/assistant turns; only the latter describe conversational state.
	last, ok := lastConversationEvent(events)
	if !ok {
		// No user/assistant event at all — e.g. a fork stub holding only
		// bookkeeping records ("ai-title", "agent-name"). Still route
		// through bgOverride: the tracker may know about work this session
		// launched even though its transcript has no conversation turn yet.
		return bgOverride(State{Kind: StateIdle, Since: now}, bgCount, bgOldest, bgUnsure)
	}
	switch last.Type {
	case "assistant":
		since := parseTimestamp(last.Timestamp)
		anchored := !since.IsZero()
		if !anchored {
			since = now
		}
		if last.UserText != "" {
			idle := State{Kind: StateIdle, Since: since, Anchored: anchored}
			return bgOverride(idle, bgCount, bgOldest, bgUnsure)
		}
		return State{Kind: StateThinking, Since: since, Anchored: anchored}
	case "user":
		// User turn (real prompt or tool_result with no new assistant yet) →
		// Claude is about to think.
		since := parseTimestamp(last.Timestamp)
		anchored := !since.IsZero()
		if !anchored {
			since = now
		}
		return State{Kind: StateThinking, Since: since, Anchored: anchored}
	}
	return bgOverride(State{Kind: StateIdle, Since: now}, bgCount, bgOldest, bgUnsure)
}

// bgOverride upgrades an Idle verdict when the session has work outstanding
// (Background) or when the tracker gave up on work by expiry rather than
// seeing it finish (Unsure). Only Idle is overridden: an unresolved
// foreground tool_use is the more specific truth and already classifies
// correctly, and Thinking is not-waiting either way. Live work wins over
// doubt — while anything is still counting, Background is the fact.
func bgOverride(s State, count int, oldest time.Time, unsure int) State {
	if s.Kind != StateIdle {
		return s
	}
	if count > 0 {
		since, anchored := oldest, true
		if since.IsZero() {
			since, anchored = s.Since, s.Anchored
		}
		return State{Kind: StateBackground, Since: since, BgCount: count, Anchored: anchored}
	}
	if unsure > 0 {
		// Since stays the Stop's timestamp: the doubt began when the turn
		// ended, and the lobby's age column should say how long the human
		// has been unsure-of, not when the cap fired.
		return State{Kind: StateUnsure, Since: s.Since, BgCount: unsure, Anchored: s.Anchored}
	}
	return s
}

// askOverride upgrades s to Asking when the ask-marker says an
// AskUserQuestion is pending and the transcript has produced nothing newer.
// markerTime is the marker file's mtime (zero when there is no marker).
//
// The freshness comparison against the newest conversation event is what
// makes a leftover marker harmless: an answered question flushes its
// tool_result with a newer timestamp, and an Esc'd one flushes the
// interrupted turn — either way the transcript overtakes the marker and the
// override stops applying, even if the hook's cleanup never ran.
//
// Only the not-actively-working verdicts are overridden. An unresolved
// foreground tool_use (StateTool) means the marker is stale by construction —
// a question can't be pending while another tool runs — and Error/Compacting
// are more specific truths.
func askOverride(s State, events []Event, markerTime time.Time) State {
	if markerTime.IsZero() {
		return s
	}
	switch s.Kind {
	case StateIdle, StateThinking, StateBackground, StateUnsure:
	default:
		return s
	}
	if last, ok := lastConversationEvent(events); ok {
		if t := parseTimestamp(last.Timestamp); !t.IsZero() && t.After(markerTime) {
			return s
		}
	}
	return State{Kind: StateAsking, Since: markerTime}
}

// lastConversationEvent returns the newest user or assistant event,
// skipping bookkeeping types.
func lastConversationEvent(events []Event) (Event, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		t := events[i].Type
		if t == "user" || t == "assistant" {
			return events[i], true
		}
	}
	return Event{}, false
}

func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func parseTimestampOr(s string, fallback time.Time) time.Time {
	t := parseTimestamp(s)
	if t.IsZero() {
		return fallback
	}
	return t
}

func (s State) Label() string {
	switch s.Kind {
	case StateIdle:
		return "Idle"
	case StateThinking:
		return "Thinking"
	case StateTool:
		return "Tool: " + s.ToolName
	case StateAwaiting:
		return "Awaiting"
	case StateError:
		return "Error"
	case StateCompacting:
		return "Compacting"
	case StateBackground:
		return "Working " + strconv.Itoa(s.BgCount)
	case StateUnsure:
		return "Unsure " + strconv.Itoa(s.BgCount)
	case StateAsking:
		return "Asking"
	case StateWaiting:
		return "Starting"
	}
	return "Unknown"
}
