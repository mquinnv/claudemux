package main

import (
	"os"
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
	// jsonlPath's directory is the already-encoded project dir for
	// "/p/x/.claude/worktrees/w" (dots and slashes both become '-').
	m := model{
		sessionID: "abc", sessionCwd: "/p/x/.claude/worktrees/w", workDir: "/p/x",
		jsonlPath: "/home/u/.claude/projects/-p-x--claude-worktrees-w/abc.jsonl",
		state:     State{Kind: StateTool, ToolName: "Bash"}, summary: Summary{Topic: "Fix it"},
	}
	r := m.sessionRecordFor(now)
	want := sessionRecord{SessionID: "abc", ClaudeCwd: "/p/x/.claude/worktrees/w",
		State: "Tool:Bash", Topic: "Fix it", LastSeen: now.Unix()}
	if r != want {
		t.Errorf("got %+v, want %+v", r, want)
	}
	m.sessionCwd = ""
	if r := m.sessionRecordFor(now); r.ClaudeCwd != "/p/x" {
		t.Errorf("no sessionCwd: cwd = %q, want workDir", r.ClaudeCwd)
	}
}

// TestClaudeCwdFor covers the three cases sessionRecordFor's ClaudeCwd
// choice can land on: a tool `cd` left sessionCwd inside a subdirectory of
// where claude actually resumes, sessionCwd is already that directory, and
// no candidate matches at all.
func TestClaudeCwdFor(t *testing.T) {
	worktreeJSONL := "/home/u/.claude/projects/-p-x--claude-worktrees-w/abc.jsonl"

	t.Run("subdir cwd resolves to the ancestor that matches", func(t *testing.T) {
		got := claudeCwdFor("/p/x/.claude/worktrees/w/cmd/sub", "/p/x", worktreeJSONL)
		if want := "/p/x/.claude/worktrees/w"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("worktree cwd matching its own project dir stays", func(t *testing.T) {
		got := claudeCwdFor("/p/x/.claude/worktrees/w", "/p/x", worktreeJSONL)
		if want := "/p/x/.claude/worktrees/w"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("no match falls back to workDir", func(t *testing.T) {
		got := claudeCwdFor("/elsewhere/entirely", "/p/x", worktreeJSONL)
		if want := "/p/x"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("empty jsonlPath falls back to workDir", func(t *testing.T) {
		got := claudeCwdFor("/p/x/.claude/worktrees/w", "/p/x", "")
		if want := "/p/x"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("empty sessionCwd falls back to workDir", func(t *testing.T) {
		got := claudeCwdFor("", "/p/x", worktreeJSONL)
		if want := "/p/x"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestClaudeProjectDirName(t *testing.T) {
	got := claudeProjectDirName("/p/x/.claude/worktrees/w")
	if want := "-p-x--claude-worktrees-w"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestRecordWrittenMsgDeletesRenamedRecord covers finding 2(b): a tmux
// session renamed within lostClusterWindow of a reboot leaves a record
// under its old name too (the head hasn't heartbeat since the rename), and
// both would point at the same session_id — selectLost's dedupe (2a) covers
// that, but deleting the stale file here means it never gets that far.
func TestRecordWrittenMsgDeletesRenamedRecord(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := sessionRecordDir()
	if err := writeSessionRecord(dir, sessionRecord{SessionName: "old", SessionID: "x", LastSeen: 1}); err != nil {
		t.Fatal(err)
	}
	oldPath := sessionRecordFile(dir, "old")

	m := model{recordName: "old"}
	next, _ := m.Update(recordWrittenMsg{name: "new"})
	m = next.(model)

	if m.recordName != "new" {
		t.Errorf("recordName = %q, want \"new\"", m.recordName)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("old record still present: err = %v, want NotExist", err)
	}
}

// The very first heartbeat (recordName "") has nothing to delete, and a
// repeat heartbeat under the same name must not delete the file it just
// wrote.
func TestRecordWrittenMsgNoDeleteWhenNameUnchanged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := sessionRecordDir()
	if err := writeSessionRecord(dir, sessionRecord{SessionName: "same", SessionID: "x", LastSeen: 1}); err != nil {
		t.Fatal(err)
	}
	path := sessionRecordFile(dir, "same")

	m := model{recordName: "same"}
	next, _ := m.Update(recordWrittenMsg{name: "same"})
	m = next.(model)

	if m.recordName != "same" {
		t.Errorf("recordName = %q, want \"same\"", m.recordName)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("record deleted on unchanged name: %v", err)
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
