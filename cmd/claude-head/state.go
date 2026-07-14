package main

import "time"

type StateKind int

const (
	StateIdle StateKind = iota
	StateThinking
	StateTool
	StateAwaiting
	StateError
	StateCompacting
)

type State struct {
	Kind     StateKind
	ToolName string // populated for StateTool / StateAwaiting
	Since    time.Time
}

func classifyState(events []Event, now time.Time) State {
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
			if since.IsZero() {
				since = now
			}
			return State{Kind: StateTool, ToolName: tu.Name, Since: since}
		}
	}

	// No unresolved tool_use. Last *conversation* event determines state.
	// Claude Code interleaves bookkeeping events (attachment, last-prompt,
	// file-history-snapshot, queue-operation, system) with real
	// user/assistant turns; only the latter describe conversational state.
	last, ok := lastConversationEvent(events)
	if !ok {
		return State{Kind: StateIdle, Since: now}
	}
	switch last.Type {
	case "assistant":
		if last.UserText != "" {
			return State{Kind: StateIdle, Since: parseTimestampOr(last.Timestamp, now)}
		}
		return State{Kind: StateThinking, Since: parseTimestampOr(last.Timestamp, now)}
	case "user":
		// User turn (real prompt or tool_result with no new assistant yet) →
		// Claude is about to think.
		return State{Kind: StateThinking, Since: parseTimestampOr(last.Timestamp, now)}
	}
	return State{Kind: StateIdle, Since: now}
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
	}
	return "Unknown"
}
