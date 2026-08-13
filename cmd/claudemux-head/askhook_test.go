package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// askHookPath locates hooks/claudemux-ask.sh relative to this test file, so
// the test does not depend on the working directory or on the script having
// been installed.
func askHookPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "hooks", "claudemux-ask.sh")
}

// runAskHook feeds payload to the hook with HOME pointed at home and returns
// its stdout. The hook must stay silent — UserPromptSubmit stdout is injected
// into the model's context.
func runAskHook(t *testing.T, home, payload string) string {
	t.Helper()
	cmd := exec.Command("bash", askHookPath(t))
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook exited non-zero: %v (stdout %q)", err, out)
	}
	return string(out)
}

func askMarkerPath(home, session string) string {
	return filepath.Join(home, ".claude", "claudemux", "asking", session+".json")
}

func TestAskHookWritesMarkerOnPreToolUse(t *testing.T) {
	home := t.TempDir()
	out := runAskHook(t, home,
		`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"sess-1"}`)
	if out != "" {
		t.Errorf("hook spoke on stdout: %q", out)
	}
	if _, err := os.Stat(askMarkerPath(home, "sess-1")); err != nil {
		t.Errorf("marker not written: %v", err)
	}
}

func TestAskHookIgnoresOtherTools(t *testing.T) {
	home := t.TempDir()
	runAskHook(t, home,
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","session_id":"sess-1"}`)
	if _, err := os.Stat(askMarkerPath(home, "sess-1")); err == nil {
		t.Error("marker written for a tool that is not AskUserQuestion")
	}
}

func TestAskHookRemovesMarkerOnPostToolUse(t *testing.T) {
	home := t.TempDir()
	runAskHook(t, home,
		`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"sess-1"}`)
	runAskHook(t, home,
		`{"hook_event_name":"PostToolUse","tool_name":"AskUserQuestion","session_id":"sess-1"}`)
	if _, err := os.Stat(askMarkerPath(home, "sess-1")); err == nil {
		t.Error("marker survived the answer (PostToolUse)")
	}
}

// A PostToolUse for some OTHER tool must not clear the marker: with parallel
// background tool results arriving, only the question's own completion, or a
// new prompt, means the question is gone.
func TestAskHookPostToolUseOtherToolKeepsMarker(t *testing.T) {
	home := t.TempDir()
	runAskHook(t, home,
		`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"sess-1"}`)
	runAskHook(t, home,
		`{"hook_event_name":"PostToolUse","tool_name":"Bash","session_id":"sess-1"}`)
	if _, err := os.Stat(askMarkerPath(home, "sess-1")); err != nil {
		t.Errorf("another tool's PostToolUse cleared the marker: %v", err)
	}
}

// UserPromptSubmit clears the marker: it is the only signal that fires after
// a question was Esc'd (no PostToolUse ever comes for those).
func TestAskHookRemovesMarkerOnUserPromptSubmit(t *testing.T) {
	home := t.TempDir()
	runAskHook(t, home,
		`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"sess-1"}`)
	runAskHook(t, home,
		`{"hook_event_name":"UserPromptSubmit","session_id":"sess-1"}`)
	if _, err := os.Stat(askMarkerPath(home, "sess-1")); err == nil {
		t.Error("marker survived a new prompt (UserPromptSubmit)")
	}
}

// Markers are per session: session B answering must not clear session A's
// pending question.
func TestAskHookMarkersArePerSession(t *testing.T) {
	home := t.TempDir()
	runAskHook(t, home,
		`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"sess-a"}`)
	runAskHook(t, home,
		`{"hook_event_name":"UserPromptSubmit","session_id":"sess-b"}`)
	if _, err := os.Stat(askMarkerPath(home, "sess-a")); err != nil {
		t.Errorf("another session's prompt cleared the marker: %v", err)
	}
}

func TestAskHookSilentOnGarbage(t *testing.T) {
	home := t.TempDir()
	if out := runAskHook(t, home, `not json at all`); out != "" {
		t.Errorf("hook spoke on garbage: %q", out)
	}
	if out := runAskHook(t, home, ``); out != "" {
		t.Errorf("hook spoke on empty stdin: %q", out)
	}
}

// A session id is used as a filename; anything that could traverse out of the
// marker directory must be dropped, not sanitized into a collision.
func TestAskHookRejectsPathySessionIDs(t *testing.T) {
	home := t.TempDir()
	runAskHook(t, home,
		`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"../evil"}`)
	if _, err := os.Stat(filepath.Join(home, ".claude", "claudemux", "evil.json")); err == nil {
		t.Error("a pathy session id escaped the asking dir")
	}
	entries, _ := os.ReadDir(filepath.Join(home, ".claude", "claudemux", "asking"))
	if len(entries) != 0 {
		t.Errorf("pathy session id left files behind: %v", entries)
	}
}
