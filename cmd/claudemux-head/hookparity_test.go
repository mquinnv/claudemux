package main

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Differential test: every scenario runs through the bash script and through
// the Go port, each in its own fresh HOME, and must produce the same stdout,
// exit code, and files. Temporary — it goes away with the scripts.

type parityStep struct {
	hook    string // map | ask | worktree
	env     []string
	payload string
}

type parityCase struct {
	name  string
	seed  func(home string) // optional pre-existing state
	steps []parityStep
}

func scriptFor(t *testing.T, hook string) string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "hooks", "claudemux-"+hook+".sh")
}

func runParityBash(t *testing.T, home string, s parityStep) (string, int) {
	cmd := exec.Command("bash", scriptFor(t, s.hook))
	cmd.Stdin = strings.NewReader(s.payload)
	cmd.Env = append([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}, s.env...)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	rc := 0
	if ee, ok := err.(*exec.ExitError); ok {
		rc = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), rc
}

func runParityGo(t *testing.T, home string, s parityStep) (string, int) {
	t.Setenv("HOME", home)
	env := map[string]string{}
	for _, kv := range s.env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	var out, errb bytes.Buffer
	rc := hookRunners[s.hook](strings.NewReader(s.payload), &out, &errb, func(k string) string { return env[k] })
	return out.String(), rc
}

// tree maps every file under home to its contents (tmp files normalised,
// since their names carry a pid).
func tree(t *testing.T, home string) map[string]string {
	got := map[string]string{}
	filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(home, p)
		if d.IsDir() {
			got[rel+"/"] = ""
			return nil
		}
		b, _ := os.ReadFile(p)
		got[rel] = string(b)
		return nil
	})
	return got
}

func TestHookParity(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	pane := "TMUX_PANE=%7"
	wt := "CLAUDEMUX_WORKTREE_PENDING=1"
	old := time.Now().Add(-8 * 24 * time.Hour)
	young := time.Now().Add(-6 * 24 * time.Hour)
	seedOld := func(rel ...string) func(string) {
		return func(home string) {
			for _, r := range rel {
				p := filepath.Join(home, r)
				os.MkdirAll(filepath.Dir(p), 0o755)
				os.WriteFile(p, []byte("x"), 0o644)
				mt := old
				if strings.Contains(r, "young") {
					mt = young
				}
				os.Chtimes(p, mt, mt)
			}
		}
	}
	m := func(payload string, env ...string) parityStep { return parityStep{"map", env, payload} }
	a := func(payload string, env ...string) parityStep { return parityStep{"ask", env, payload} }
	w := func(payload string, env ...string) parityStep { return parityStep{"worktree", env, payload} }

	cases := []parityCase{
		// map
		{name: "map/full", steps: []parityStep{m(`{"session_id":"s1","transcript_path":"/t/x.jsonl","cwd":"/repo","hook_event_name":"SessionStart"}`, pane)}},
		{name: "map/no-pane", steps: []parityStep{m(`{"session_id":"s1","transcript_path":"/t/x.jsonl","cwd":"/r"}`)}},
		{name: "map/missing-cwd", steps: []parityStep{m(`{"session_id":"s1","transcript_path":"/t/x.jsonl"}`, pane)}},
		{name: "map/empty-transcript", steps: []parityStep{m(`{"session_id":"s1","transcript_path":"","cwd":"/r"}`, pane)}},
		{name: "map/numeric-transcript", steps: []parityStep{m(`{"session_id":"s1","transcript_path":5,"cwd":"/r"}`, pane)}},
		{name: "map/garbage", steps: []parityStep{m(`not json`, pane)}},
		{name: "map/empty", steps: []parityStep{m(``, pane)}},
		{name: "map/array", steps: []parityStep{m(`[1,2]`, pane)}},
		{name: "map/escapes", steps: []parityStep{m(`{"session_id":"a\"b<&>","transcript_path":"/t/é x\\y.jsonl","cwd":"/r/\t"}`, pane)}},
		{name: "map/numeric-session", steps: []parityStep{m(`{"session_id":42,"transcript_path":"/t","cwd":{"a": [1, 2]}}`, pane)}},
		{name: "map/pretty-input", steps: []parityStep{m("{\n  \"session_id\": \"s\",\n  \"transcript_path\": \"/t\",\n  \"cwd\": \"/c\"\n}\n", pane)}},
		{name: "map/overwrite", steps: []parityStep{
			m(`{"session_id":"s1","transcript_path":"/t/1","cwd":"/r"}`, pane),
			m(`{"session_id":"s2","transcript_path":"/t/2","cwd":"/r"}`, pane),
		}},
		{name: "map/prunes", seed: seedOld(".claude/claudemux/panes/3.json", ".claude/claudemux/panes/4.json.tmp.99", ".claude/claudemux/panes/young5.json", ".claude/claudemux/panes/other.txt"),
			steps: []parityStep{m(`{"session_id":"s","transcript_path":"/t","cwd":"/c"}`, pane)}},

		// ask
		{name: "ask/pre", steps: []parityStep{a(`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"sess-1"}`, pane)}},
		{name: "ask/pre-other-tool", steps: []parityStep{a(`{"hook_event_name":"PreToolUse","tool_name":"Bash","session_id":"sess-1"}`, pane)}},
		{name: "ask/no-pane", steps: []parityStep{a(`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"sess-1"}`)}},
		{name: "ask/pre-post", steps: []parityStep{
			a(`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"s"}`, pane),
			a(`{"hook_event_name":"PostToolUse","tool_name":"AskUserQuestion","session_id":"s"}`, pane),
		}},
		{name: "ask/pre-post-other", steps: []parityStep{
			a(`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"s"}`, pane),
			a(`{"hook_event_name":"PostToolUse","tool_name":"Bash","session_id":"s"}`, pane),
		}},
		{name: "ask/permit-then-prompt", steps: []parityStep{
			a(`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"s"}`, pane),
			a(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","session_id":"s"}`, pane),
		}},
		{name: "ask/permit-ask-ignored", steps: []parityStep{a(`{"hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion","session_id":"s"}`, pane)}},
		{name: "ask/prompt-clears", steps: []parityStep{
			a(`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"s"}`, pane),
			a(`{"hook_event_name":"PermissionRequest","tool_name":"Edit","session_id":"s"}`, pane),
			a(`{"hook_event_name":"UserPromptSubmit","session_id":"s","prompt":"hi"}`, pane),
		}},
		{name: "ask/pathy", steps: []parityStep{
			a(`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"../x"}`, pane),
			a(`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":".hidden"}`, pane),
			a(`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":"a/b"}`, pane),
		}},
		{name: "ask/empty-session", steps: []parityStep{a(`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion"}`, pane)}},
		{name: "ask/garbage", steps: []parityStep{a(`{{{`, pane), a(``, pane), a(`[]`, pane), a(`"str"`, pane), a(`null`, pane)}},
		{name: "ask/object-field", steps: []parityStep{a(`{"hook_event_name":"PreToolUse","tool_name":{"x":1},"session_id":"s"}`, pane)}},
		{name: "ask/numeric-session", steps: []parityStep{a(`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","session_id":123}`, pane)}},
		{name: "ask/quote-session", steps: []parityStep{a(`{"hook_event_name":"PermissionRequest","tool_name":"Bash<&>","session_id":"q\"uo\\te"}`, pane)}},
		{name: "ask/unknown-event-prunes", seed: seedOld(".claude/claudemux/asking/old.json", ".claude/claudemux/asking/young.json"),
			steps: []parityStep{a(`{"hook_event_name":"Stop","session_id":"s"}`, pane)}},
		{name: "ask/other-tool-no-prune", seed: seedOld(".claude/claudemux/asking/old.json"),
			steps: []parityStep{a(`{"hook_event_name":"PreToolUse","tool_name":"Bash","session_id":"s"}`, pane)}},

		// worktree
		{name: "wt/no-marker", steps: []parityStep{w(`{"cwd":"/tmp/repo","session_id":"s"}`)}},
		{name: "wt/inside", steps: []parityStep{w(`{"cwd":"/tmp/repo/.claude/worktrees/x","session_id":"s"}`, wt)}},
		{name: "wt/garbage", steps: []parityStep{w(`nope`, wt), w(``, wt), w(`[1]`, wt)}},
		{name: "wt/no-cwd", steps: []parityStep{w(`{"session_id":"s"}`, wt)}},
		{name: "wt/cap", steps: []parityStep{
			w(`{"cwd":"/r","session_id":"s"}`, wt), w(`{"cwd":"/r","session_id":"s"}`, wt),
			w(`{"cwd":"/r","session_id":"s"}`, wt), w(`{"cwd":"/r","session_id":"t"}`, wt),
		}},
		{name: "wt/no-session-always-asks", steps: []parityStep{w(`{"cwd":"/r"}`, wt), w(`{"cwd":"/r"}`, wt), w(`{"cwd":"/r"}`, wt)}},
		{name: "wt/corrupt-counter", seed: func(h string) {
			os.MkdirAll(filepath.Join(h, ".claude/claudemux/worktree-asks"), 0o755)
			os.WriteFile(filepath.Join(h, ".claude/claudemux/worktree-asks/s"), []byte("garbage"), 0o644)
			os.WriteFile(filepath.Join(h, ".claude/claudemux/worktree-asks/u"), []byte("1\n"), 0o644)
			os.WriteFile(filepath.Join(h, ".claude/claudemux/worktree-asks/v"), []byte("-1"), 0o644)
		}, steps: []parityStep{w(`{"cwd":"/r","session_id":"s"}`, wt), w(`{"cwd":"/r","session_id":"u"}`, wt), w(`{"cwd":"/r","session_id":"u"}`, wt), w(`{"cwd":"/r","session_id":"v"}`, wt)}},
		{name: "wt/prunes", seed: seedOld(".claude/claudemux/worktree-asks/gone", ".claude/claudemux/worktree-asks/young"),
			steps: []parityStep{w(`{"cwd":"/r","session_id":"s"}`, wt)}},
		{name: "wt/numeric-cwd", steps: []parityStep{w(`{"cwd":7,"session_id":"s"}`, wt)}},
		{name: "wt/false-cwd", steps: []parityStep{w(`{"cwd":false,"session_id":"s"}`, wt)}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bh, gh := t.TempDir(), t.TempDir()
			if c.seed != nil {
				c.seed(bh)
				c.seed(gh)
			}
			for i, s := range c.steps {
				bo, brc := runParityBash(t, bh, s)
				gout, grc := runParityGo(t, gh, s)
				if bo != gout || brc != grc {
					t.Errorf("step %d: bash (rc=%d) %q\n           go   (rc=%d) %q", i, brc, bo, grc, gout)
				}
			}
			bt, gt := tree(t, bh), tree(t, gh)
			for k, v := range bt {
				if gv, ok := gt[k]; !ok {
					t.Errorf("bash has %s (%q), go does not", k, v)
				} else if gv != v {
					t.Errorf("%s: bash %q, go %q", k, v, gv)
				}
			}
			for k, v := range gt {
				if _, ok := bt[k]; !ok {
					t.Errorf("go has %s (%q), bash does not", k, v)
				}
			}
		})
	}
}
