package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runHookWithEnv runs one `claudemux-head hook <name>` in-process with stdin
// payload and ONLY the variables in env ("K=V") visible to it, and returns its
// stdout. HOME, when given, is also where the hook resolves its state dir. A
// non-zero exit fails the test: on every path these tests take, the hooks end
// normally.
func runHookWithEnv(t *testing.T, name, payload string, env []string) string {
	t.Helper()
	vars := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	// Always, even when env has no HOME: an empty one makes os.UserHomeDir
	// fail, which is what keeps a test without a HOME of its own from writing
	// into the developer's real ~/.claude.
	t.Setenv("HOME", vars["HOME"])
	var stdout, stderr bytes.Buffer
	if rc := hookRunners[name](strings.NewReader(payload), &stdout, &stderr, func(k string) string { return vars[k] }); rc != 0 {
		t.Fatalf("hook %s exited %d (stdout %q, stderr %q)", name, rc, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// runAskHookWithEnv feeds payload to the ask hook with the exact env given and
// returns its stdout. The hook must stay silent — UserPromptSubmit stdout is
// injected into the model's context.
func runAskHookWithEnv(t *testing.T, payload string, env []string) string {
	t.Helper()
	return runHookWithEnv(t, "ask", payload, env)
}

func runAskHook(t *testing.T, home, payload string) string {
	t.Helper()
	return runAskHookWithEnv(t, payload, []string{"HOME=" + home, "TMUX_PANE=%0"})
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

// Outside tmux there is no sibling pane running claudemux-head to read this
// session's marker, so the hook must not even get as far as parsing the
// payload — exactly the same reasoning, and the same early exit, as the map
// hook.
func TestAskHookSilentWithoutTmuxPane(t *testing.T) {
	home := t.TempDir()
	out := runAskHookWithEnv(t,
		`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"sess-1"}`,
		[]string{"HOME=" + home})
	if out != "" {
		t.Errorf("hook spoke without TMUX_PANE: %q", out)
	}
	if _, err := os.Stat(askMarkerPath(home, "sess-1")); err == nil {
		t.Error("marker written despite no TMUX_PANE set")
	}
}

func permitMarkerPath(home, session string) string {
	return filepath.Join(home, ".claude", "claudemux", "asking", session+".permit.json")
}

// PermissionRequest fires when a permission dialog is about to open. It is the
// only hook that does: the tool_use is already in the transcript by then and
// reads as a tool that is running, so without this marker a session blocked on
// "Do you want to proceed?" is indistinguishable from one that is working.
func TestAskHookWritesPermitMarkerOnPermissionRequest(t *testing.T) {
	home := t.TempDir()
	out := runAskHook(t, home,
		`{"hook_event_name":"PermissionRequest","tool_name":"mcp__betterstack__chart_alert_help","session_id":"sess-1"}`)
	// Stdout here is a contract with Claude Code: JSON on it can ALLOW the
	// call. This hook only observes.
	if out != "" {
		t.Errorf("hook spoke on stdout: %q", out)
	}
	data, err := os.ReadFile(permitMarkerPath(home, "sess-1"))
	if err != nil {
		t.Fatalf("permit marker not written: %v", err)
	}
	if !strings.Contains(string(data), `"mcp__betterstack__chart_alert_help"`) {
		t.Errorf("permit marker %s does not name the tool", data)
	}
	if _, err := os.Stat(askMarkerPath(home, "sess-1")); err == nil {
		t.Error("a permission dialog wrote the AskUserQuestion marker")
	}
}

// AskUserQuestion already has its own marker from PreToolUse; a second one
// would have the head choose between two names for one dialog.
func TestAskHookPermissionRequestForQuestionIsIgnored(t *testing.T) {
	home := t.TempDir()
	runAskHook(t, home,
		`{"hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion","session_id":"sess-1"}`)
	if _, err := os.Stat(permitMarkerPath(home, "sess-1")); err == nil {
		t.Error("permit marker written for AskUserQuestion")
	}
}

func TestAskHookUserPromptSubmitRemovesPermitMarker(t *testing.T) {
	home := t.TempDir()
	runAskHook(t, home,
		`{"hook_event_name":"PermissionRequest","tool_name":"Bash","session_id":"sess-1"}`)
	runAskHook(t, home,
		`{"hook_event_name":"UserPromptSubmit","session_id":"sess-1"}`)
	if _, err := os.Stat(permitMarkerPath(home, "sess-1")); err == nil {
		t.Error("permit marker survived a new prompt (UserPromptSubmit)")
	}
}

// Each dialog must restamp the marker: the head tells one dialog from the next
// by the marker's mtime.
func TestAskHookPermissionRequestRestampsMarker(t *testing.T) {
	home := t.TempDir()
	runAskHook(t, home,
		`{"hook_event_name":"PermissionRequest","tool_name":"Bash","session_id":"sess-1"}`)
	path := permitMarkerPath(home, "sess-1")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	runAskHook(t, home,
		`{"hook_event_name":"PermissionRequest","tool_name":"Edit","session_id":"sess-1"}`)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().After(old) {
		t.Error("a second dialog did not restamp the marker")
	}
}
