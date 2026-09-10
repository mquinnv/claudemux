package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestDirtiedSince(t *testing.T) {
	tests := []struct {
		name              string
		baseline, current string
		want              bool
	}{
		{"clean stays clean", "", "", false},
		{"pre-existing junk is not the session's doing",
			"?? .claude/\n?? scratch.txt", "?? .claude/\n?? scratch.txt", false},
		{"a new untracked file is dirt",
			"?? .claude/", "?? .claude/\n?? docs/plan.md", true},
		{"a tracked file going modified is dirt",
			"", " M README.md", true},
		{"a cleanup is not dirt",
			"?? scratch.txt", "", false},
		{"the same path changing state is dirt",
			"?? notes.md", "A  notes.md", true},
		{"trailing newlines do not matter",
			"?? a\n", "?? a", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dirtiedSince(tt.baseline, tt.current); got != tt.want {
				t.Errorf("dirtiedSince(%q, %q) = %v, want %v", tt.baseline, tt.current, got, tt.want)
			}
		})
	}
}

// gitRepoWithJunk makes a repo with one commit and one pre-existing untracked
// file — the ordinary state of a main checkout, which the baseline must absorb.
func gitRepoWithJunk(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "init")
	if err := os.WriteFile(filepath.Join(dir, "junk.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestMainDirtyProbeAgainstRealGit(t *testing.T) {
	dir := gitRepoWithJunk(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	baseline, ok := mainStatusSnapshot(ctx, dir)
	if !ok {
		t.Fatal("snapshot of a real repo failed")
	}
	if baseline == "" {
		t.Fatal("baseline is empty, want the pre-existing junk.txt in it")
	}

	// Nothing has changed: the pre-existing junk must not be reported.
	if msg := mainDirtyProbeCmd(dir, baseline)().(mainDirtyMsg); msg.dirtied {
		t.Error("probe reported dirt with nothing changed since the baseline")
	}

	// The session writes a plan document into the main checkout.
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte("steps\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := mainDirtyProbeCmd(dir, baseline)().(mainDirtyMsg); !msg.dirtied {
		t.Error("probe missed a new untracked file in the main checkout")
	}

	// Editing a tracked file counts too.
	dir2 := gitRepoWithJunk(t)
	baseline2, _ := mainStatusSnapshot(ctx, dir2)
	if err := os.WriteFile(filepath.Join(dir2, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := mainDirtyProbeCmd(dir2, baseline2)().(mainDirtyMsg); !msg.dirtied {
		t.Error("probe missed a modified tracked file in the main checkout")
	}
}

// A snapshot that cannot be taken must say so, and a probe that cannot read
// must not accuse. Both failure modes feed a warning, so "unknown" has to
// collapse to silence, not to ⚠.
func TestMainDirtyProbeUnreadableIsSilent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, ok := mainStatusSnapshot(ctx, ""); ok {
		t.Error("snapshot of an empty dir reported ok")
	}
	if _, ok := mainStatusSnapshot(ctx, t.TempDir()); ok {
		t.Error("snapshot of a non-repo reported ok")
	}
	if msg := mainDirtyProbeCmd(t.TempDir(), "")().(mainDirtyMsg); msg.dirtied {
		t.Error("probe of a non-repo reported dirt")
	}
}

func TestMainDirtyProbeDue(t *testing.T) {
	now := time.Now()
	ready := model{
		worktreePending:      true,
		mainStatusBaselineOK: true,
		firstPrompt:          "fix the thing",
		jsonlPath:            "/proj/abc.jsonl", // not a worktree path
	}
	if !ready.mainDirtyProbeDue(now) {
		t.Fatal("a marked, prompted, baselined session outside a worktree is not probing")
	}

	cases := map[string]func(m model) model{
		"unmarked session":       func(m model) model { m.worktreePending = false; return m },
		"no baseline":            func(m model) model { m.mainStatusBaselineOK = false; return m },
		"already warned":         func(m model) model { m.mainDirtied = true; return m },
		"probe in flight":        func(m model) model { m.mainDirtyProbing = true; return m },
		"no prompt yet":          func(m model) model { m.firstPrompt = ""; return m },
		"inside a worktree":      func(m model) model { m.sessionCwd = "/repo/.claude/worktrees/x"; return m },
		"probed a moment ago":    func(m model) model { m.mainDirtyProbeAt = now.Add(-time.Second); return m },
		"probed a long time ago": func(m model) model { m.mainDirtyProbeAt = now.Add(-2 * mainDirtyProbeInterval); return m },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			got := mutate(ready).mainDirtyProbeDue(now)
			want := name == "probed a long time ago"
			if got != want {
				t.Errorf("mainDirtyProbeDue = %v, want %v", got, want)
			}
		})
	}
}

// The latch is one-way and the probe flag clears on every answer.
func TestMainDirtyMsgLatches(t *testing.T) {
	m := model{mainDirtyProbing: true}
	next, _ := m.Update(mainDirtyMsg{dirtied: false})
	m = next.(model)
	if m.mainDirtyProbing || m.mainDirtied {
		t.Fatalf("after a clean probe: probing=%v dirtied=%v, want false/false", m.mainDirtyProbing, m.mainDirtied)
	}
	next, _ = m.Update(mainDirtyMsg{dirtied: true})
	m = next.(model)
	if !m.mainDirtied {
		t.Fatal("a dirty probe did not latch the warning")
	}
	next, _ = m.Update(mainDirtyMsg{dirtied: false})
	if !next.(model).mainDirtied {
		t.Fatal("a later clean probe un-latched the warning")
	}
}
