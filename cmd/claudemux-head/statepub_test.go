package main

import (
	"reflect"
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
		{"unknown", State{Kind: StateKind(99)}, ""},
	}
	for _, c := range cases {
		if got := statePublishValue(c.s); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
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
