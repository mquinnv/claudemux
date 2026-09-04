package main

import (
	"testing"
	"time"
)

func TestClassifyEmptyStream(t *testing.T) {
	got := classifyState(nil, 0, time.Time{}, 0, time.Now())
	if got.Kind != StateIdle {
		t.Errorf("empty stream: got %v, want StateIdle", got.Kind)
	}
}

func TestClassifyLastIsAssistantText(t *testing.T) {
	events := []Event{{Type: "assistant", UserText: "all done"}}
	got := classifyState(events, 0, time.Time{}, 0, time.Now())
	if got.Kind != StateIdle {
		t.Errorf("got %v, want StateIdle", got.Kind)
	}
}

func TestClassifyToolInFlight(t *testing.T) {
	events := []Event{
		{Type: "assistant", ToolUses: []ToolUse{{ID: "t1", Name: "Bash"}}},
	}
	got := classifyState(events, 0, time.Time{}, 0, time.Now())
	if got.Kind != StateTool || got.ToolName != "Bash" {
		t.Errorf("got %v/%q, want StateTool/Bash", got.Kind, got.ToolName)
	}
}

func TestClassifyToolCompletedNotInFlight(t *testing.T) {
	events := []Event{
		{Type: "assistant", ToolUses: []ToolUse{{ID: "t1", Name: "Bash"}}},
		{Type: "user", ToolResults: []ToolResult{{ToolUseID: "t1"}}},
	}
	got := classifyState(events, 0, time.Time{}, 0, time.Now())
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
	got := classifyState(events, 0, time.Time{}, 0, now)
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
	got := classifyState(events, 0, time.Time{}, 0, time.Now())
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

	if got := classifyState(events, 0, time.Time{}, 0, now); got.Kind != StateIdle {
		t.Errorf("Kind = %v, want StateIdle with no background work", got.Kind)
	}
	got := classifyState(events, 2, oldest, 0, now)
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
	got := classifyState(events, 3, now.Add(-time.Minute), 0, now)
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
	if got := classifyState(events, 0, time.Time{}, 0, now); !got.Anchored {
		t.Errorf("event-stamped Idle must be Anchored")
	}
	// Empty stream: Since is a now-fallback, not anchored — publishing it as
	// a change every poll is exactly the flap the publish guard must avoid.
	if got := classifyState(nil, 0, time.Time{}, 0, now); got.Anchored {
		t.Errorf("empty-stream Idle must not be Anchored")
	}
	// Background inherits anchoring from the oldest launch time.
	events = []Event{{Type: "assistant", UserText: "launched", Timestamp: "2026-08-13T10:00:00Z"}}
	got := classifyState(events, 2, time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC), 0, now)
	if got.Kind != StateBackground || !got.Anchored {
		t.Errorf("got %v anchored=%v, want StateBackground anchored", got.Kind, got.Anchored)
	}
}

// Idle reached because the tracker gave up on work (an expiry) is doubt,
// not Idle: the conductor must not treat it as waiting on the human.
func TestClassifyStateUnsureAfterExpiry(t *testing.T) {
	now := time.Date(2026, 9, 4, 17, 20, 0, 0, time.UTC)
	events := []Event{{Type: "assistant", UserText: "I'll pick this up when the run finishes.", Timestamp: "2026-09-04T16:45:53Z"}}
	got := classifyState(events, 0, time.Time{}, 2, now)
	if got.Kind != StateUnsure {
		t.Fatalf("kind = %v, want StateUnsure", got.Kind)
	}
	if got.BgCount != 2 {
		t.Errorf("BgCount = %d, want 2 (the expired count)", got.BgCount)
	}
	want, _ := time.Parse(time.RFC3339, "2026-09-04T16:45:53Z")
	if !got.Since.Equal(want) || !got.Anchored {
		t.Errorf("Since = %v anchored=%v, want the Stop's timestamp, anchored", got.Since, got.Anchored)
	}
}

// Live work beats doubt: while anything is still counting, Background is
// the truth and the expired count is irrelevant.
func TestClassifyStateBackgroundBeatsUnsure(t *testing.T) {
	now := time.Date(2026, 9, 4, 17, 20, 0, 0, time.UTC)
	events := []Event{{Type: "assistant", UserText: "done", Timestamp: "2026-09-04T16:45:53Z"}}
	got := classifyState(events, 1, now.Add(-time.Minute), 3, now)
	if got.Kind != StateBackground || got.BgCount != 1 {
		t.Errorf("got %+v, want Background with 1 outstanding", got)
	}
}

// Only Idle is overridden — a session mid-turn is Thinking whatever the
// tracker gave up on earlier.
func TestClassifyStateUnsureDoesNotOverrideThinking(t *testing.T) {
	now := time.Date(2026, 9, 4, 17, 20, 0, 0, time.UTC)
	events := []Event{{Type: "user", UserText: "how's it going?", Timestamp: "2026-09-04T17:19:00Z"}}
	got := classifyState(events, 0, time.Time{}, 2, now)
	if got.Kind != StateThinking {
		t.Errorf("kind = %v, want StateThinking", got.Kind)
	}
}

// A pending AskUserQuestion is a more specific truth than doubt.
func TestAskOverrideUpgradesUnsure(t *testing.T) {
	marker := time.Date(2026, 9, 4, 17, 0, 0, 0, time.UTC)
	events := []Event{{Type: "assistant", UserText: "done", Timestamp: "2026-09-04T16:45:53Z"}}
	s := State{Kind: StateUnsure, BgCount: 1, Since: marker.Add(-time.Hour), Anchored: true}
	if got := askOverride(s, events, marker); got.Kind != StateAsking {
		t.Errorf("kind = %v, want StateAsking", got.Kind)
	}
}

func TestUnsureLabel(t *testing.T) {
	if got := (State{Kind: StateUnsure, BgCount: 2}).Label(); got != "Unsure 2" {
		t.Errorf("Label = %q, want %q", got, "Unsure 2")
	}
}

// A transcript that carries no user/assistant event at all — e.g. a fork
// stub holding only bookkeeping records like "ai-title" or "agent-name" —
// must still route through bgOverride. Before the fix, this branch returned
// a bare Idle directly, so a session whose only visible activity was such a
// bookkeeping record could never publish Background (or Unsure) no matter
// what the tracker knew.
func TestClassifyNoConversationEventRoutesThroughBgOverride(t *testing.T) {
	events := []Event{{Type: "ai-title", Timestamp: "2026-08-11T10:00:00Z"}}
	oldest := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 8, 11, 10, 5, 0, 0, time.UTC)
	got := classifyState(events, 1, oldest, 0, now)
	if got.Kind != StateBackground {
		t.Errorf("kind = %v, want StateBackground: bookkeeping-only events must not bypass bgOverride", got.Kind)
	}
}
