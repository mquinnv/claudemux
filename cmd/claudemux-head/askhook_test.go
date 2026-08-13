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

// runAskHookWithEnv feeds payload to the hook with the exact env given and
// returns its stdout. The hook must stay silent — UserPromptSubmit stdout is
// injected into the model's context.
func runAskHookWithEnv(t *testing.T, payload string, env []string) string {
	t.Helper()
	cmd := exec.Command("bash", askHookPath(t))
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook exited non-zero: %v (stdout %q)", err, out)
	}
	return string(out)
}

// runAskHook feeds payload to the hook with HOME pointed at home and
// TMUX_PANE set — every test below except the ones specifically about the
// TMUX_PANE gate wants the hook to actually run, not exit at that gate. See
// runAskHookWithEnv for tests that need to control env more precisely.
func runAskHook(t *testing.T, home, payload string) string {
	t.Helper()
	return runAskHookWithEnv(t, payload, []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TMUX_PANE=%0"})
}

func askMarkerPath(home, session string) string {
	return filepath.Join(home, ".claude", "claudemux", "asking", session+".json")
}

// requireJQ skips a test when jq is not on the host PATH. Every test below
// except TestAskHookSilentWithoutTmuxPane and TestAskHookNoJQOnPathExitsSilently
// exercises jq-dependent behavior; without this guard, a machine missing jq
// would fail them for an environment reason that has nothing to do with the
// hook's own logic.
func requireJQ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not found on PATH")
	}
}

func TestAskHookWritesMarkerOnPreToolUse(t *testing.T) {
	requireJQ(t)
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
	requireJQ(t)
	home := t.TempDir()
	runAskHook(t, home,
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","session_id":"sess-1"}`)
	if _, err := os.Stat(askMarkerPath(home, "sess-1")); err == nil {
		t.Error("marker written for a tool that is not AskUserQuestion")
	}
}

func TestAskHookRemovesMarkerOnPostToolUse(t *testing.T) {
	requireJQ(t)
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
	requireJQ(t)
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
	requireJQ(t)
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
	requireJQ(t)
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
	requireJQ(t)
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
// session's marker, so the hook must not even get as far as spawning jq —
// exactly the same reasoning, and the same early line, as claudemux-map.sh.
func TestAskHookSilentWithoutTmuxPane(t *testing.T) {
	home := t.TempDir()
	out := runAskHookWithEnv(t,
		`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"sess-1"}`,
		[]string{"HOME=" + home, "PATH=" + os.Getenv("PATH")})
	if out != "" {
		t.Errorf("hook spoke without TMUX_PANE: %q", out)
	}
	if _, err := os.Stat(askMarkerPath(home, "sess-1")); err == nil {
		t.Error("marker written despite no TMUX_PANE set")
	}
}

// jq ships as a documented dependency (README), but a session's PATH could
// still lack it — e.g. a launcher that trims PATH before exec'ing claude.
// The hook must degrade to a silent no-op, never a hang or a stray write, and
// never propagate jq's exit status: the every-tool-call cost this hook must
// avoid (see hook.go) means this failure mode is not rare.
func TestAskHookNoJQOnPathExitsSilently(t *testing.T) {
	home := t.TempDir()

	// Build a PATH with everything the script needs EXCEPT jq, so the
	// missing-jq behavior is isolated rather than incidentally caused by a
	// missing cat/mkdir/mv/rm/find.
	pathDir := t.TempDir()
	for _, tool := range []string{"cat", "mkdir", "mv", "rm", "find"} {
		src, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not found on host PATH, cannot build a jq-less PATH for it", tool)
		}
		if err := os.Symlink(src, filepath.Join(pathDir, tool)); err != nil {
			t.Fatal(err)
		}
	}

	out := runAskHookWithEnv(t,
		`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"sess-1"}`,
		[]string{"HOME=" + home, "PATH=" + pathDir, "TMUX_PANE=%0"})
	if out != "" {
		t.Errorf("hook spoke with no jq on PATH: %q", out)
	}
	if _, err := os.Stat(askMarkerPath(home, "sess-1")); err == nil {
		t.Error("marker written despite jq missing from PATH")
	}
}
