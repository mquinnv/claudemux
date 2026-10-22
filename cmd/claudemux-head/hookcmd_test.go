package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These cover `claudemux-head hook <name>` end to end through runHookCommand,
// and the hooks' failure paths. Claude Code reads exit 2 as "block this prompt
// / tool call", so the one thing every case here asserts, whatever else it
// checks, is that nothing exits 2.

// runHookCommandWithEnv runs `claudemux-head hook <args...>` in-process with
// stdin payload and ONLY the variables in env ("K=V") visible, and returns
// its exit code, stdout and stderr. Unlike runHookWithEnv it does not fail on
// a non-zero exit: the failure paths are what these tests are for.
func runHookCommandWithEnv(t *testing.T, args []string, payload string, env []string) (int, string, string) {
	t.Helper()
	vars := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	// Always set, as runHookWithEnv does: an empty HOME keeps a test without
	// one of its own out of the developer's real ~/.claude.
	t.Setenv("HOME", vars["HOME"])
	var stdout, stderr bytes.Buffer
	rc := runHookCommand(args, strings.NewReader(payload), &stdout, &stderr, func(k string) string { return vars[k] })
	if rc == 2 {
		t.Errorf("hook %v exited 2, which blocks the prompt (stderr %q)", args, stderr.String())
	}
	return rc, stdout.String(), stderr.String()
}

// blockWithFile puts a regular file where a hook expects a directory under
// ~/.claude/claudemux, so creating or writing into it fails.
func blockWithFile(t *testing.T, home, name string) {
	t.Helper()
	state := filepath.Join(home, ".claude", "claudemux")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHookCommandRoutesAsk(t *testing.T) {
	home := t.TempDir()
	rc, out, _ := runHookCommandWithEnv(t, []string{"ask"},
		`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"sess-1"}`,
		[]string{"HOME=" + home, "TMUX_PANE=%0"})
	if rc != 0 || out != "" {
		t.Errorf("rc %d, stdout %q; want 0 and silence", rc, out)
	}
	if _, err := os.Stat(askMarkerPath(home, "sess-1")); err != nil {
		t.Errorf("marker not written: %v", err)
	}
}

func TestHookCommandRoutesWorktree(t *testing.T) {
	rc, out, _ := runHookCommandWithEnv(t, []string{"worktree"},
		`{"cwd":"/tmp/repo","session_id":"s1"}`,
		[]string{"HOME=" + t.TempDir(), "CLAUDEMUX_WORKTREE_PENDING=1"})
	if rc != 0 || out != worktreeInstruction {
		t.Errorf("rc %d, stdout %q; want 0 and the instruction", rc, out)
	}
}

func TestHookCommandRoutesMap(t *testing.T) {
	home := t.TempDir()
	rc, out, _ := runHookCommandWithEnv(t, []string{"map"},
		`{"session_id":"s1","transcript_path":"/t/s1.jsonl","cwd":"/r"}`,
		[]string{"HOME=" + home, "TMUX_PANE=%4"})
	if rc != 0 || out != "" {
		t.Errorf("rc %d, stdout %q; want 0 and silence", rc, out)
	}
	if _, err := os.Stat(paneMapPath(home, "4")); err != nil {
		t.Errorf("pane map not written: %v", err)
	}
}

// settings.json can name a hook this binary does not have — written by a newer
// claudemux, run by an older copy. That must fail quietly, not block every
// prompt with exit 2.
func TestHookCommandUnknownNameIsNotBlocking(t *testing.T) {
	rc, out, errOut := runHookCommandWithEnv(t, []string{"from-the-future"}, `{}`, nil)
	if rc != 1 {
		t.Errorf("rc %d, want 1", rc)
	}
	if out != "" {
		t.Errorf("stdout %q, want silence", out)
	}
	if !strings.Contains(errOut, "from-the-future") || strings.Count(errOut, "\n") != 1 {
		t.Errorf("stderr %q, want one line naming the hook", errOut)
	}
}

func TestHookCommandBareIsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if rc := runHookCommand(nil, strings.NewReader(""), &stdout, &stderr, func(string) string { return "" }); rc != 2 {
		t.Errorf("rc %d, want 2 (usage)", rc)
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Errorf("stderr %q, want usage", stderr.String())
	}
}

func TestHookCommandEmptyStdinNeverBlocks(t *testing.T) {
	for _, h := range claudemuxHooks {
		env := []string{"HOME=" + t.TempDir(), "TMUX_PANE=%1", "CLAUDEMUX_WORKTREE_PENDING=1"}
		runHookCommandWithEnv(t, []string{h.name}, "", env)
	}
}

func TestHookCommandRecoversPanic(t *testing.T) {
	hookRunners["boom"] = func(io.Reader, io.Writer, io.Writer, func(string) string) int {
		panic("kaboom")
	}
	t.Cleanup(func() { delete(hookRunners, "boom") })

	rc, out, errOut := runHookCommandWithEnv(t, []string{"boom"}, `{}`, nil)
	if rc != 1 {
		t.Errorf("rc %d, want 1", rc)
	}
	if out != "" {
		t.Errorf("stdout %q, want silence", out)
	}
	if !strings.Contains(errOut, "claudemux hook boom: internal error: kaboom") {
		t.Errorf("stderr %q, want the recovered panic", errOut)
	}
}

func TestMapHookFailsWhenPanesIsAFile(t *testing.T) {
	home := t.TempDir()
	blockWithFile(t, home, "panes")
	rc, out, errOut := runHookCommandWithEnv(t, []string{"map"},
		`{"session_id":"s1","transcript_path":"/t/s1.jsonl","cwd":"/r"}`,
		[]string{"HOME=" + home, "TMUX_PANE=%4"})
	if rc != 1 || out != "" || errOut == "" {
		t.Errorf("rc %d, stdout %q, stderr %q; want 1, silence, an error", rc, out, errOut)
	}
}

func TestAskHookFailsWhenAskingIsAFile(t *testing.T) {
	for _, payload := range []string{
		`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"s1"}`,
		`{"hook_event_name":"PermissionRequest","tool_name":"Bash","session_id":"s1"}`,
	} {
		home := t.TempDir()
		blockWithFile(t, home, "asking")
		rc, out, errOut := runHookCommandWithEnv(t, []string{"ask"}, payload,
			[]string{"HOME=" + home, "TMUX_PANE=%0"})
		if rc != 1 || out != "" || errOut == "" {
			t.Errorf("%s: rc %d, stdout %q, stderr %q; want 1, silence, an error", payload, rc, out, errOut)
		}
	}
}

// The ask counter is bookkeeping: failing to keep it must never cost the
// instruction itself.
func TestWorktreeHookAsksWhenCounterDirIsAFile(t *testing.T) {
	home := t.TempDir()
	blockWithFile(t, home, "worktree-asks")
	rc, out, _ := runHookCommandWithEnv(t, []string{"worktree"},
		`{"cwd":"/tmp/repo","session_id":"s1"}`,
		[]string{"HOME=" + home, "CLAUDEMUX_WORKTREE_PENDING=1"})
	if rc != 0 || out != worktreeInstruction {
		t.Errorf("rc %d, stdout %q; want 0 and the instruction", rc, out)
	}
}

func TestWorktreeHookAsksWithoutHome(t *testing.T) {
	rc, out, _ := runHookCommandWithEnv(t, []string{"worktree"},
		`{"cwd":"/tmp/repo","session_id":"s1"}`,
		[]string{"CLAUDEMUX_WORKTREE_PENDING=1"})
	if rc != 0 || out != worktreeInstruction {
		t.Errorf("rc %d, stdout %q; want 0 and the instruction", rc, out)
	}
}
