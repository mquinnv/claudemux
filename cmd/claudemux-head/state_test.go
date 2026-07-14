package main

import (
	"testing"
	"time"
)

func TestClassifyEmptyStream(t *testing.T) {
	got := classifyState(nil, time.Now())
	if got.Kind != StateIdle {
		t.Errorf("empty stream: got %v, want StateIdle", got.Kind)
	}
}

func TestClassifyLastIsAssistantText(t *testing.T) {
	events := []Event{{Type: "assistant", UserText: "all done"}}
	got := classifyState(events, time.Now())
	if got.Kind != StateIdle {
		t.Errorf("got %v, want StateIdle", got.Kind)
	}
}

func TestClassifyToolInFlight(t *testing.T) {
	events := []Event{
		{Type: "assistant", ToolUses: []ToolUse{{ID: "t1", Name: "Bash"}}},
	}
	got := classifyState(events, time.Now())
	if got.Kind != StateTool || got.ToolName != "Bash" {
		t.Errorf("got %v/%q, want StateTool/Bash", got.Kind, got.ToolName)
	}
}

func TestClassifyToolCompletedNotInFlight(t *testing.T) {
	events := []Event{
		{Type: "assistant", ToolUses: []ToolUse{{ID: "t1", Name: "Bash"}}},
		{Type: "user", ToolResults: []ToolResult{{ToolUseID: "t1"}}},
	}
	got := classifyState(events, time.Now())
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
	got := classifyState(events, now)
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
	got := classifyState(events, time.Now())
	// Last event is assistant text — that's idle, not error. Errors should not
	// surface here as StateError. This guards against false positives.
	if got.Kind == StateError {
		t.Errorf("assistant text after error tool_result should not be StateError")
	}
}
