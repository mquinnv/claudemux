package main

import (
	"os"
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
	// append to an empty slice, not `cmd.Env = env`: a variadic call with no
	// entries passes a NIL slice, and os/exec reads nil as "inherit the
	// parent's environment" — which handed the no-marker case the marker from
	// a claudemux-launched shell and failed the test it was meant to isolate.
	cmd.Env = append([]string{}, env...)
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

// The cwd check only ends the nagging when EnterWorktree SUCCEEDS. On every
// failure path — the user says "no, just work here", the tool refuses on a
// dirty tree, the model declines — the cwd never moves, so without a cap the
// hook would re-inject on every prompt for the rest of the session.
func TestWorktreeHookStopsAskingAfterCap(t *testing.T) {
	home := t.TempDir()
	payload := `{"cwd":"/tmp/repo","session_id":"sess-cap"}`
	env := []string{"CLAUDEMUX_WORKTREE_PENDING=1", "HOME=" + home, "PATH=" + os.Getenv("PATH")}

	for i := 1; i <= 2; i++ {
		if got := runWorktreeHook(t, payload, env...); !strings.Contains(got, "EnterWorktree") {
			t.Fatalf("ask %d: hook stayed silent, want the instruction: %q", i, got)
		}
	}
	if got := runWorktreeHook(t, payload, env...); got != "" {
		t.Errorf("third ask spoke: %q — the cap must stop the nagging once the human has effectively answered", got)
	}
}

// A different session gets its own budget: a resume or /clear is a new task,
// and it deserves a worktree of its own.
func TestWorktreeHookCapIsPerSession(t *testing.T) {
	home := t.TempDir()
	env := []string{"CLAUDEMUX_WORKTREE_PENDING=1", "HOME=" + home, "PATH=" + os.Getenv("PATH")}

	for i := 0; i < 3; i++ {
		runWorktreeHook(t, `{"cwd":"/tmp/repo","session_id":"sess-a"}`, env...)
	}
	got := runWorktreeHook(t, `{"cwd":"/tmp/repo","session_id":"sess-b"}`, env...)
	if !strings.Contains(got, "EnterWorktree") {
		t.Errorf("a fresh session was silenced by another session's budget: %q", got)
	}
}

// A corrupt counter must degrade to asking, never to silently disabling the
// feature — the failure that would be invisible.
func TestWorktreeHookCorruptCounterStillAsks(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "claudemux", "worktree-asks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sess-bad"), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"CLAUDEMUX_WORKTREE_PENDING=1", "HOME=" + home, "PATH=" + os.Getenv("PATH")}

	got := runWorktreeHook(t, `{"cwd":"/tmp/repo","session_id":"sess-bad"}`, env...)
	if !strings.Contains(got, "EnterWorktree") {
		t.Errorf("a corrupt counter silenced the hook: %q", got)
	}
}
