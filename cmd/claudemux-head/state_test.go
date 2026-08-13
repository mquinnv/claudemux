package main

import (
	"testing"
	"time"
)

func TestClassifyEmptyStream(t *testing.T) {
	got := classifyState(nil, 0, time.Time{}, time.Now())
	if got.Kind != StateIdle {
		t.Errorf("empty stream: got %v, want StateIdle", got.Kind)
	}
}

func TestClassifyLastIsAssistantText(t *testing.T) {
	events := []Event{{Type: "assistant", UserText: "all done"}}
	got := classifyState(events, 0, time.Time{}, time.Now())
	if got.Kind != StateIdle {
		t.Errorf("got %v, want StateIdle", got.Kind)
	}
}

func TestClassifyToolInFlight(t *testing.T) {
	events := []Event{
		{Type: "assistant", ToolUses: []ToolUse{{ID: "t1", Name: "Bash"}}},
	}
	got := classifyState(events, 0, time.Time{}, time.Now())
	if got.Kind != StateTool || got.ToolName != "Bash" {
		t.Errorf("got %v/%q, want StateTool/Bash", got.Kind, got.ToolName)
	}
}

func TestClassifyToolCompletedNotInFlight(t *testing.T) {
	events := []Event{
		{Type: "assistant", ToolUses: []ToolUse{{ID: "t1", Name: "Bash"}}},
		{Type: "user", ToolResults: []ToolResult{{ToolUseID: "t1"}}},
	}
	got := classifyState(events, 0, time.Time{}, time.Now())
	if got.Kind != StateThinking {
		t.Errorf("after tool result with no new assistant turn: got %v, want StateThinking", got.Kind)
	}
}

func TestClassifySkipsBookkeepingEvents(t *testing.T) {
	// Claude Code interleaves "attachment", "last-prompt", "system", etc.
	// between user/assistant turns. Those bookkeeping events must NOT
	// be treated as the "last event" — Idle would be wrong while Claude
	// is still mid-loop.
	now := time.Now()
	events := []Event{
		{Type: "assistant", ToolUses: []ToolUse{{ID: "t1", Name: "Bash"}}, Timestamp: now.Add(-2 * time.Second).Format(time.RFC3339)},
		{Type: "user", ToolResults: []ToolResult{{ToolUseID: "t1"}}, Timestamp: now.Add(-1 * time.Second).Format(time.RFC3339)},
		{Type: "attachment"},
		{Type: "last-prompt", UserText: "tail of user input"},
	}
	got := classifyState(events, 0, time.Time{}, now)
	if got.Kind != StateThinking {
		t.Errorf("got %v, want StateThinking (last conversation event was user/tool_result)", got.Kind)
	}
}

func TestClassifyError(t *testing.T) {
	events := []Event{
		{Type: "assistant", ToolUses: []ToolUse{{ID: "t1", Name: "Bash"}}},
		{Type: "user", ToolResults: []ToolResult{{ToolUseID: "t1", IsError: true}}},
		{Type: "assistant", UserText: "I hit an error"},
	}
	got := classifyState(events, 0, time.Time{}, time.Now())
	// Last event is assistant text — that's idle, not error. Errors should not
	// surface here as StateError. This guards against false positives.
	if got.Kind == StateError {
		t.Errorf("assistant text after error tool_result should not be StateError")
	}
}

// The bug this fixes: the main thread ended its turn while work it launched is
// still running, and the session reported Idle — which isWaiting reads as
// "waiting on the human".
func TestClassifyStateBackgroundOverridesIdle(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 10, 0, 0, time.UTC)
	oldest := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	events := []Event{{Type: "assistant", Timestamp: "2026-08-11T10:09:00Z", UserText: "Kicked that off in the background."}}

	if got := classifyState(events, 0, time.Time{}, now); got.Kind != StateIdle {
		t.Errorf("Kind = %v, want StateIdle with no background work", got.Kind)
	}
	got := classifyState(events, 2, oldest, now)
	if got.Kind != StateBackground {
		t.Errorf("Kind = %v, want StateBackground", got.Kind)
	}
	if got.BgCount != 2 {
		t.Errorf("BgCount = %d, want 2", got.BgCount)
	}
	if !got.Since.Equal(oldest) {
		t.Errorf("Since = %v, want the oldest launch %v", got.Since, oldest)
	}
}

// A foreground tool is the more specific truth and already classifies
// correctly; background work must not mask it.
func TestClassifyStateToolBeatsBackground(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 10, 0, 0, time.UTC)
	events := []Event{{
		Type:      "assistant",
		Timestamp: "2026-08-11T10:09:00Z",
		ToolUses:  []ToolUse{{ID: "toolu_1", Name: "Bash"}},
	}}
	got := classifyState(events, 3, now.Add(-time.Minute), now)
	if got.Kind != StateTool || got.ToolName != "Bash" {
		t.Errorf("got %v/%q, want StateTool/Bash", got.Kind, got.ToolName)
	}
}

func TestBackgroundLabel(t *testing.T) {
	if got := (State{Kind: StateBackground, BgCount: 2}).Label(); got != "Working 2" {
		t.Errorf("Label = %q, want %q", got, "Working 2")
	}
}

func TestClassifyAnchoredSince(t *testing.T) {
	now := time.Now()
	// Event-stamped Idle: anchored.
	events := []Event{{Type: "assistant", UserText: "done", Timestamp: "2026-08-13T10:00:00Z"}}
	if got := classifyState(events, 0, time.Time{}, now); !got.Anchored {
		t.Errorf("event-stamped Idle must be Anchored")
	}
	// Empty stream: Since is a now-fallback, not anchored — publishing it as
	// a change every poll is exactly the flap the publish guard must avoid.
	if got := classifyState(nil, 0, time.Time{}, now); got.Anchored {
		t.Errorf("empty-stream Idle must not be Anchored")
	}
	// Background inherits anchoring from the oldest launch time.
	events = []Event{{Type: "assistant", UserText: "launched", Timestamp: "2026-08-13T10:00:00Z"}}
	got := classifyState(events, 2, time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC), now)
	if got.Kind != StateBackground || !got.Anchored {
		t.Errorf("got %v anchored=%v, want StateBackground anchored", got.Kind, got.Anchored)
	}
}
