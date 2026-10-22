package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These pin the hooks' output byte for byte against what the bash scripts
// they replaced produced for the same payloads (recorded from
// hooks/claudemux-*.sh before their removal): the files the head reads back,
// and the one stdout that reaches the model.

func paneMapPath(home, pane string) string {
	return filepath.Join(home, ".claude", "claudemux", "panes", pane+".json")
}

func runMapHook(t *testing.T, home, payload string, env ...string) string {
	t.Helper()
	return runHookWithEnv(t, "map", payload, append([]string{"HOME=" + home}, env...))
}

func TestMapHookWritesPaneMap(t *testing.T) {
	home := t.TempDir()
	out := runMapHook(t, home,
		`{"session_id":"s1","transcript_path":"/t/x.jsonl","cwd":"/repo","hook_event_name":"SessionStart","source":"startup"}`,
		"TMUX_PANE=%7")
	// UserPromptSubmit stdout is injected into the model's context.
	if out != "" {
		t.Errorf("map hook spoke on stdout: %q", out)
	}
	got, err := os.ReadFile(paneMapPath(home, "7"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"session_id":"s1","transcript_path":"/t/x.jsonl","cwd":"/repo"}` + "\n"; string(got) != want {
		t.Errorf("pane map = %q, want %q", got, want)
	}
}

// jq -c re-encoded every value: strings without HTML escaping, a missing
// field as null, a non-string value compacted.
func TestMapHookEncodesLikeJQ(t *testing.T) {
	home := t.TempDir()
	runMapHook(t, home,
		`{"session_id":42,"transcript_path":"/t/é <&> \"q\"\\b.jsonl","other":1}`,
		"TMUX_PANE=%3")
	got, err := os.ReadFile(paneMapPath(home, "3"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"session_id":42,"transcript_path":"/t/é <&> \"q\"\\b.jsonl","cwd":null}` + "\n"; string(got) != want {
		t.Errorf("pane map = %q, want %q", got, want)
	}
}

func TestMapHookSkipsWithoutAUsablePayload(t *testing.T) {
	for name, payload := range map[string]string{
		"garbage":               `not json`,
		"empty":                 ``,
		"array":                 `[1]`,
		"no transcript":         `{"session_id":"s","cwd":"/r"}`,
		"empty transcript":      `{"session_id":"s","transcript_path":"","cwd":"/r"}`,
		"non-string transcript": `{"session_id":"s","transcript_path":5,"cwd":"/r"}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			if out := runMapHook(t, home, payload, "TMUX_PANE=%1"); out != "" {
				t.Errorf("spoke: %q", out)
			}
			if _, err := os.Stat(paneMapPath(home, "1")); !os.IsNotExist(err) {
				t.Errorf("pane map written (err=%v)", err)
			}
		})
	}
}

// Outside tmux there is no pane to key on.
func TestMapHookSilentWithoutTmuxPane(t *testing.T) {
	home := t.TempDir()
	runMapHook(t, home, `{"session_id":"s","transcript_path":"/t","cwd":"/c"}`)
	if _, err := os.Stat(filepath.Join(home, ".claude", "claudemux", "panes")); !os.IsNotExist(err) {
		t.Errorf("panes dir created without TMUX_PANE (err=%v)", err)
	}
}

// Pane ids restart with the tmux server, so a dead pane's map can shadow a
// new one: anything unwritten for over a week goes, crashed runs' tmp files
// included, and nothing that is not a map file is touched.
func TestMapHookPrunesStaleMaps(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "claudemux", "panes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-8 * 24 * time.Hour)
	recent := time.Now().Add(-6 * 24 * time.Hour)
	files := map[string]time.Time{"3.json": stale, "4.json.tmp.99": stale, "5.json": recent, "notes.txt": stale}
	for name, mt := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	runMapHook(t, home, `{"session_id":"s","transcript_path":"/t","cwd":"/c"}`, "TMUX_PANE=%7")
	for name, wantGone := range map[string]bool{"3.json": true, "4.json.tmp.99": true, "5.json": false, "notes.txt": false, "7.json": false} {
		_, err := os.Stat(filepath.Join(dir, name))
		if gone := os.IsNotExist(err); gone != wantGone {
			t.Errorf("%s gone=%v, want %v", name, gone, wantGone)
		}
	}
}

// jq -n wrote the markers indented by two spaces; the head only parses them,
// but byte-identical files keep an old and a new hook interchangeable.
func TestAskHookMarkerBytes(t *testing.T) {
	home := t.TempDir()
	runAskHook(t, home, `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"q\"uo<te"}`)
	got, err := os.ReadFile(askMarkerPath(home, `q"uo<te`))
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"session_id\": \"q\\\"uo<te\"\n}\n"; string(got) != want {
		t.Errorf("marker = %q, want %q", got, want)
	}

	runAskHook(t, home, `{"hook_event_name":"PermissionRequest","tool_name":"Bash","session_id":"s"}`)
	got, err = os.ReadFile(permitMarkerPath(home, "s"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"session_id\": \"s\",\n  \"tool_name\": \"Bash\"\n}\n"; string(got) != want {
		t.Errorf("permit marker = %q, want %q", got, want)
	}
}

// The instruction, byte for byte. Its wording is the product of two rounds of
// watching what the model does with it (see worktreeInstruction); an edit to
// it should be a deliberate edit to this test too.
func TestWorktreeHookInstructionBytes(t *testing.T) {
	got := runWorktreeHook(t, `{"cwd":"/r","session_id":"s"}`, "CLAUDEMUX_WORKTREE_PENDING=1", "HOME="+t.TempDir())
	want := "<claudemux-worktree>\n" +
		"This session is configured to work in a git worktree, and is not in one yet.\n" +
		"Do not enter one now. Investigate, read, search, review, and answer in the\n" +
		"current checkout. Call EnterWorktree only immediately before your first action\n" +
		"that changes this repository's git state (editing a tracked file, adding a new\n" +
		"file such as a plan or design document, committing) -- and if the task never\n" +
		"changes anything, never call it. When you do, derive `name` from the user's\n" +
		"request: 2-5 words, lowercase, dash-separated, naming the durable goal rather\n" +
		"than the first step (e.g. \"rename-worktrees-on-topic\"). Use only letters,\n" +
		"digits, dots, underscores and dashes, up to 64 characters total.\n" +
		"</claudemux-worktree>\n"
	if got != want {
		t.Errorf("instruction = %q\nwant          %q", got, want)
	}
}

// An unknown hook name is not silently a no-op: runHookCommand fails with exit
// 1 for anything hookRunners lacks, so the table must hold exactly the names
// claudemuxHooks registers.
func TestHookRunnersCoverRegisteredHooks(t *testing.T) {
	if len(hookRunners) != len(claudemuxHooks) {
		t.Errorf("hookRunners has %d entries, claudemuxHooks %d", len(hookRunners), len(claudemuxHooks))
	}
	for _, h := range claudemuxHooks {
		if hookRunners[h.name] == nil {
			t.Errorf("hook %q is registered but has no runner", h.name)
		}
	}
}
