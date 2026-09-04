package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStatePublishValue(t *testing.T) {
	cases := []struct {
		name string
		s    State
		want string
	}{
		{"idle", State{Kind: StateIdle}, "Idle"},
		{"thinking", State{Kind: StateThinking}, "Thinking"},
		{"tool", State{Kind: StateTool, ToolName: "Bash"}, "Tool:Bash"},
		{"ask", State{Kind: StateTool, ToolName: "AskUserQuestion"}, "Tool:AskUserQuestion"},
		{"awaiting", State{Kind: StateAwaiting}, "Awaiting"},
		{"error", State{Kind: StateError}, "Error"},
		{"compacting", State{Kind: StateCompacting}, "Compacting"},
		{"waiting", State{Kind: StateWaiting}, "Starting"},
		{"unknown", State{Kind: StateKind(99)}, ""},
	}
	for _, c := range cases {
		if got := statePublishValue(c.s); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStatePublishValueBackground(t *testing.T) {
	got := statePublishValue(State{Kind: StateBackground, BgCount: 2})
	if got != "Background:2" {
		t.Errorf("statePublishValue = %q, want Background:2", got)
	}
	// The conductor's rule is exact-match on the waiting values, so an
	// unrecognized value is already not-waiting. Assert it, because that is
	// what keeps switchboard.go unchanged.
	if isWaiting(got) {
		t.Error("a session with background work must not count as waiting")
	}
}

// A head waiting for its project's first transcript publishes "Starting". The
// conductor must never escort a human into it — the claude pane is still
// booting and can't take input — so the value must stay out of isWaiting's set.
func TestStatePublishValueWaitingIsNotEscortable(t *testing.T) {
	if isWaiting(statePublishValue(State{Kind: StateWaiting})) {
		t.Error("a booting session must not count as waiting on the human")
	}
}

func TestStatePublishArgs(t *testing.T) {
	since := time.Unix(1754700000, 0)
	args, ok := statePublishArgs("%3", "Idle", since)
	if !ok {
		t.Fatal("expected ok")
	}
	want := []string{
		"set-option", "-t", "%3", "@claudemux_state", "Idle",
		";",
		"set-option", "-t", "%3", "@claudemux_state_since", "1754700000",
	}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("got %v, want %v", args, want)
	}
}

func TestStatePublishArgsSkips(t *testing.T) {
	if _, ok := statePublishArgs("", "Idle", time.Now()); ok {
		t.Error("outside tmux (empty selfPane) must not publish")
	}
	if _, ok := statePublishArgs("%3", "", time.Now()); ok {
		t.Error("empty value must not publish")
	}
}

func TestPublishStateCmdNilWhenUnpublishable(t *testing.T) {
	if cmd := publishStateCmd("", State{Kind: StateIdle}, time.Now()); cmd != nil {
		t.Error("expected nil cmd outside tmux")
	}
}

func TestMaybePublishState(t *testing.T) {
	m := &model{selfPane: "%3", state: State{Kind: StateIdle, Since: time.Unix(1754700000, 0)}}
	now := time.Now()
	if cmd := m.maybePublishState(now); cmd == nil {
		t.Fatal("first call after a state change must publish")
	}
	if m.publishedState != "Idle" {
		t.Errorf("publishedState = %q, want %q", m.publishedState, "Idle")
	}
	if cmd := m.maybePublishState(now); cmd != nil {
		t.Error("unchanged state must not republish")
	}
	m.state = State{Kind: StateThinking, Since: time.Unix(1754700100, 0)}
	if cmd := m.maybePublishState(now); cmd == nil {
		t.Error("changed state must publish again")
	}
}

func TestMaybePublishStateOutsideTmux(t *testing.T) {
	m := &model{state: State{Kind: StateIdle}}
	if cmd := m.maybePublishState(time.Now()); cmd != nil {
		t.Error("empty selfPane must yield nil cmd")
	}
}

// A new waiting episode with the same value ("Idle" again after a busy blip
// the poll never saw) must still republish _since: the conductor's snoozes
// and queue order key on it, and a pinned stale Since starves the session.
func TestMaybePublishStateRepublishesAnchoredSinceChange(t *testing.T) {
	now := time.Now()
	m := &model{selfPane: "%1"}
	m.state = State{Kind: StateIdle, Since: time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC), Anchored: true}
	if cmd := m.maybePublishState(now); cmd == nil {
		t.Fatal("first publish must fire")
	}
	m.state.Since = m.state.Since.Add(5 * time.Minute) // same value, new episode
	if cmd := m.maybePublishState(now); cmd == nil {
		t.Error("anchored Since change with an unchanged value must republish")
	}
}

// The no-flap invariant the old comment guarded: an unanchored Since is a
// now-fallback that differs every tick, and must NOT trigger republishes.
func TestMaybePublishStateUnanchoredSinceDoesNotFlap(t *testing.T) {
	now := time.Now()
	m := &model{selfPane: "%1"}
	m.state = State{Kind: StateIdle, Since: now, Anchored: false}
	if cmd := m.maybePublishState(now); cmd == nil {
		t.Fatal("first publish must fire")
	}
	m.state.Since = now.Add(time.Second) // next tick's fallback
	if cmd := m.maybePublishState(now.Add(time.Second)); cmd != nil {
		t.Error("unanchored Since drift must not republish every tick")
	}
}

func TestSanitizeOptionValue(t *testing.T) {
	if got := sanitizeOptionValue("a\tb\nc"); got != "a b c" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("word ", 60)
	if got := sanitizeOptionValue(long); len([]rune(got)) > 120 {
		t.Errorf("not truncated: %d runes", len([]rune(got)))
	}
}

func TestPublishOptionCmd(t *testing.T) {
	if cmd := publishOptionCmd("", infoSummaryOption, "x"); cmd != nil {
		t.Error("outside tmux must be nil")
	}
	if cmd := publishOptionCmd("%3", infoSummaryOption, ""); cmd == nil {
		t.Error("empty value must still publish (columns stay aligned)")
	}
}

func TestMaybePublishInfo(t *testing.T) {
	m := &model{selfPane: "%3", publishedContext: -1}
	m.contextPct = 41.7
	m.summary = Summary{Now: "doing things"}
	m.lastTyped = "/done"
	m.modelName = "claude-opus-4-7"
	if got := len(m.maybePublishInfo()); got != 4 {
		t.Fatalf("first publish: %d cmds, want 4", got)
	}
	if m.publishedContext != 41 || m.publishedSummary != "doing things" || m.publishedPrompt != "/done" {
		t.Errorf("guards not updated: %d %q %q", m.publishedContext, m.publishedSummary, m.publishedPrompt)
	}
	if m.publishedModel != "claude-opus-4-7" {
		t.Errorf("publishedModel = %q, want claude-opus-4-7", m.publishedModel)
	}
	if got := len(m.maybePublishInfo()); got != 0 {
		t.Errorf("unchanged: %d cmds, want 0", got)
	}
	m.contextPct = 41.9 // same integer percent: no republish
	if got := len(m.maybePublishInfo()); got != 0 {
		t.Errorf("same integer percent republished: %d cmds", got)
	}
	m.contextPct = 42.0
	if got := len(m.maybePublishInfo()); got != 1 {
		t.Errorf("context change: %d cmds, want 1", got)
	}
}

func TestMaybePublishInfoOutsideTmux(t *testing.T) {
	m := &model{publishedContext: -1}
	m.summary = Summary{Now: "x"}
	if got := len(m.maybePublishInfo()); got != 0 {
		t.Errorf("outside tmux: %d cmds, want 0", got)
	}
}

// Unsure publishes with its count and is NOT escortable: doubt is the whole
// point — the conductor skips it exactly as it skips Background.
func TestStatePublishValueUnsureIsNotEscortable(t *testing.T) {
	v := statePublishValue(State{Kind: StateUnsure, BgCount: 2})
	if v != "Unsure:2" {
		t.Errorf("value = %q, want %q", v, "Unsure:2")
	}
	if isWaiting(v) {
		t.Errorf("isWaiting(%q) = true, want false", v)
	}
}
