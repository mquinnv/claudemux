package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// worktreeHookPath locates hooks/claudemux-worktree.sh relative to this test
// file, so the test does not depend on the working directory or on the script
// having been installed.
func worktreeHookPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "hooks", "claudemux-worktree.sh")
}

// runWorktreeHook feeds payload to the hook with the given environment and
// returns its stdout. env entries are "K=V"; the child gets ONLY these, so a
// marker leaking in from the developer's shell cannot mask a failure.
func runWorktreeHook(t *testing.T, payload string, env ...string) string {
	t.Helper()
	cmd := exec.Command("bash", worktreeHookPath(t))
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook exited non-zero: %v (stdout %q)", err, out)
	}
	return string(out)
}

func TestWorktreeHookSilentWithoutMarker(t *testing.T) {
	got := runWorktreeHook(t, `{"cwd":"/tmp/repo"}`)
	if got != "" {
		t.Errorf("hook spoke with no marker set: %q", got)
	}
}

func TestWorktreeHookSilentInsideWorktree(t *testing.T) {
	got := runWorktreeHook(t,
		`{"cwd":"/tmp/repo/.claude/worktrees/some-name"}`,
		"CLAUDEMUX_WORKTREE_PENDING=1")
	if got != "" {
		t.Errorf("hook spoke for a cwd already in a worktree: %q", got)
	}
}

func TestWorktreeHookSilentOnGarbagePayload(t *testing.T) {
	got := runWorktreeHook(t, `not json at all`, "CLAUDEMUX_WORKTREE_PENDING=1")
	if got != "" {
		t.Errorf("hook spoke on an unparseable payload: %q", got)
	}
}

func TestWorktreeHookAsksForWorktree(t *testing.T) {
	got := runWorktreeHook(t,
		`{"cwd":"/tmp/repo"}`,
		"CLAUDEMUX_WORKTREE_PENDING=1")
	if !strings.Contains(got, "EnterWorktree") {
		t.Errorf("instruction does not name the tool: %q", got)
	}
	// The name convention must reach the model, or it will invent its own shape.
	if !strings.Contains(got, "dash-separated") {
		t.Errorf("instruction does not state the naming convention: %q", got)
	}
}
