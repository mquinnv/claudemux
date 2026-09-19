package main

import (
	"testing"
	"time"
)

func TestRecordDue(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	base := model{selfPane: "%1", sessionID: "abc", state: State{Kind: StateIdle}}

	if !base.recordDue(now) {
		t.Error("never written: want due")
	}
	m := base
	m.lastRecordAt, m.lastRecordState = now.Add(-10*time.Second), "Idle"
	if m.recordDue(now) {
		t.Error("10s since write, same state: want not due")
	}
	m.lastRecordAt = now.Add(-30 * time.Second)
	if !m.recordDue(now) {
		t.Error("30s since write: want due")
	}
	m.lastRecordAt = now.Add(-5 * time.Second)
	m.state = State{Kind: StateThinking}
	if !m.recordDue(now) {
		t.Error("state changed: want due")
	}

	for name, mm := range map[string]model{
		"no pane":    {sessionID: "abc"},
		"no session": {selfPane: "%1"},
		"teardown":   {selfPane: "%1", sessionID: "abc", teardown: teardownExiting},
	} {
		if mm.recordDue(now) {
			t.Errorf("%s: want not due", name)
		}
	}
}

func TestSessionRecordFor(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	m := model{
		sessionID: "abc", sessionCwd: "/p/x/.claude/worktrees/w", workDir: "/p/x",
		state: State{Kind: StateTool, ToolName: "Bash"}, summary: Summary{Topic: "Fix it"},
	}
	r := m.sessionRecordFor(now)
	want := sessionRecord{SessionID: "abc", ClaudeCwd: "/p/x/.claude/worktrees/w",
		State: "Tool:Bash", Topic: "Fix it", LastSeen: now.Unix()}
	if r != want {
		t.Errorf("got %+v, want %+v", r, want)
	}
	m.sessionCwd = ""
	if r := m.sessionRecordFor(now); r.ClaudeCwd != "/p/x" {
		t.Errorf("fallback cwd = %q, want workDir", r.ClaudeCwd)
	}
}

func TestParseSessionNamePath(t *testing.T) {
	name, path, ok := parseSessionNamePath("remix-2\t/Users/m/Projects/remix\n")
	if !ok || name != "remix-2" || path != "/Users/m/Projects/remix" {
		t.Errorf("got %q %q %v", name, path, ok)
	}
	for _, bad := range []string{"", "\t/p", "name-only"} {
		if _, _, ok := parseSessionNamePath(bad); ok {
			t.Errorf("parseSessionNamePath(%q) ok, want failure", bad)
		}
	}
}
