package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// askOverride is the transcript-blind spot's patch: a pending AskUserQuestion
// never reaches the transcript (Claude Code flushes the tool_use only when it
// is answered), so the marker the PreToolUse hook wrote is the one signal
// that the session is blocked on the human rather than Idle/Thinking.

func TestAskOverrideUpgradesIdleAndThinking(t *testing.T) {
	now := time.Now()
	marker := now.Add(-time.Minute)
	events := []Event{
		{Type: "assistant", UserText: "text", Timestamp: now.Add(-2 * time.Minute).Format(time.RFC3339)},
	}
	for _, kind := range []StateKind{StateIdle, StateThinking, StateBackground} {
		got := askOverride(State{Kind: kind, Since: now}, events, marker)
		if got.Kind != StateAsking {
			t.Errorf("kind %v: got %v, want StateAsking", kind, got.Kind)
		}
		if !got.Since.Equal(marker) {
			t.Errorf("kind %v: Since = %v, want marker time %v — the age column must show how long the question has been waiting", kind, got.Since, marker)
		}
	}
}

func TestAskOverrideNoMarkerIsNoop(t *testing.T) {
	s := State{Kind: StateIdle, Since: time.Now()}
	if got := askOverride(s, nil, time.Time{}); got != s {
		t.Errorf("zero marker time: got %+v, want unchanged %+v", got, s)
	}
}

// A transcript event NEWER than the marker means the question was resolved —
// answered (the tool_use+tool_result pair flushed) or Esc'd (the interrupted
// turn flushed). A marker whose cleanup hook never ran must not wedge the
// session in Asking.
func TestAskOverrideStaleMarkerIgnored(t *testing.T) {
	now := time.Now()
	marker := now.Add(-10 * time.Minute)
	events := []Event{
		{Type: "assistant", UserText: "answered and moved on", Timestamp: now.Add(-time.Minute).Format(time.RFC3339)},
	}
	got := askOverride(State{Kind: StateIdle, Since: now}, events, marker)
	if got.Kind != StateIdle {
		t.Errorf("stale marker: got %v, want StateIdle", got.Kind)
	}
}

// Active-work verdicts are more specific truths than the marker: a question
// cannot be pending while a foreground tool runs, and Error/Compacting must
// stay visible.
func TestAskOverrideLeavesActiveStatesAlone(t *testing.T) {
	marker := time.Now()
	for _, kind := range []StateKind{StateTool, StateError, StateCompacting, StateAwaiting} {
		s := State{Kind: kind, ToolName: "Bash", Since: marker.Add(-time.Minute)}
		if got := askOverride(s, nil, marker); got != s {
			t.Errorf("kind %v: got %+v, want unchanged", kind, got)
		}
	}
}

// An empty transcript (fresh session, ring not yet seeded) has no newer event
// to out-vote the marker — the marker wins.
func TestAskOverrideAppliesWithNoEvents(t *testing.T) {
	marker := time.Now().Add(-time.Second)
	got := askOverride(State{Kind: StateIdle, Since: time.Now()}, nil, marker)
	if got.Kind != StateAsking {
		t.Errorf("got %v, want StateAsking", got.Kind)
	}
}

func TestAskingLabelAndPublishValue(t *testing.T) {
	s := State{Kind: StateAsking, Since: time.Now()}
	if got := s.Label(); got != "Asking" {
		t.Errorf("Label() = %q, want Asking", got)
	}
	if got := statePublishValue(s); got != "Asking" {
		t.Errorf("statePublishValue() = %q, want Asking", got)
	}
}

func TestIsWaitingIncludesAsking(t *testing.T) {
	for state, want := range map[string]bool{
		"Asking":               true,
		"Idle":                 true,
		"Tool:AskUserQuestion": true, // pre-Asking heads publish this
		"Thinking":             false,
		"Tool:Bash":            false,
	} {
		if got := isWaiting(state); got != want {
			t.Errorf("isWaiting(%q) = %v, want %v", state, got, want)
		}
	}
}

func TestAskMarkerTime(t *testing.T) {
	dir := t.TempDir()
	if got := askMarkerTime(dir, "sess-a"); !got.IsZero() {
		t.Errorf("no marker: got %v, want zero", got)
	}
	if got := askMarkerTime("", "sess-a"); !got.IsZero() {
		t.Errorf("empty dir: got %v, want zero", got)
	}
	if got := askMarkerTime(dir, ""); !got.IsZero() {
		t.Errorf("empty session: got %v, want zero", got)
	}
	p := filepath.Join(dir, "sess-a.json")
	if err := os.WriteFile(p, []byte(`{"session_id":"sess-a"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := askMarkerTime(dir, "sess-a")
	if got.IsZero() {
		t.Fatal("marker present: got zero time")
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(fi.ModTime()) {
		t.Errorf("got %v, want the marker's mtime %v", got, fi.ModTime())
	}
}
