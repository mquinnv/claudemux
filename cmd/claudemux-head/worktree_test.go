package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// initRepoWithWorktree builds a throwaway repo with one linked worktree and
// returns (main repo root, linked worktree root). Both are real git dirs, so
// the resolver under test exercises the same rev-parse it runs in production.
func initRepoWithWorktree(t *testing.T, worktreeName string) (string, string) {
	t.Helper()
	// Resolve symlinks so paths match what git reports: on macOS t.TempDir()
	// hands back /var/folders/… while `git` canonicalizes to /private/var/…,
	// and the substring matching in dominantWorktree needs the two to agree.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "webapp")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// Keep the caller's git identity/hooks out of it.
		cmd.Env = append(cmd.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := exec.Command("mkdir", "-p", repo).Run(); err != nil {
		t.Fatal(err)
	}
	run(repo, "init", "-b", "main")
	run(repo, "commit", "--allow-empty", "-m", "init")
	worktree := filepath.Join(root, worktreeName)
	run(repo, "worktree", "add", "-b", "feature/x", worktree)
	return repo, worktree
}

// A linked worktree created by plain `git worktree add` — the sibling-directory
// layout, which carries no ".claude/worktrees" path marker — must still be
// recognized, and must report the worktree's own directory name.
func TestGitWorktreeName(t *testing.T) {
	repo, worktree := initRepoWithWorktree(t, "webapp-crm-567")

	if got := gitWorktreeName(worktree); got != "webapp-crm-567" {
		t.Errorf("gitWorktreeName(linked worktree) = %q, want %q", got, "webapp-crm-567")
	}
	// A deeper cwd inside the worktree still resolves to the worktree root's name.
	sub := filepath.Join(worktree, "sub")
	if err := exec.Command("mkdir", "-p", sub).Run(); err != nil {
		t.Fatal(err)
	}
	if got := gitWorktreeName(sub); got != "webapp-crm-567" {
		t.Errorf("gitWorktreeName(subdir of worktree) = %q, want %q", got, "webapp-crm-567")
	}
	// The main worktree is not a worktree for chip purposes.
	if got := gitWorktreeName(repo); got != "" {
		t.Errorf("gitWorktreeName(main repo) = %q, want \"\"", got)
	}
	// Outside any repo, and with no cwd at all.
	if got := gitWorktreeName(t.TempDir()); got != "" {
		t.Errorf("gitWorktreeName(non-repo) = %q, want \"\"", got)
	}
	if got := gitWorktreeName(""); got != "" {
		t.Errorf("gitWorktreeName(\"\") = %q, want \"\"", got)
	}
}

func TestWorktreeNameForCwd(t *testing.T) {
	// Claude's native layout is recognized from the path alone, with no git
	// call — the dir needn't even exist.
	native := "/Users/alice/Projects/webapp/.claude/worktrees/feature-branch/sub"
	if got := worktreeNameForCwd(native); got != "feature-branch" {
		t.Errorf("worktreeNameForCwd(native layout) = %q, want %q", got, "feature-branch")
	}

	repo, worktree := initRepoWithWorktree(t, "webapp-crm-569")
	if got := worktreeNameForCwd(worktree); got != "webapp-crm-569" {
		t.Errorf("worktreeNameForCwd(sibling worktree) = %q, want %q", got, "webapp-crm-569")
	}
	if got := worktreeNameForCwd(repo); got != "" {
		t.Errorf("worktreeNameForCwd(main repo) = %q, want \"\"", got)
	}
	if got := worktreeNameForCwd(""); got != "" {
		t.Errorf("worktreeNameForCwd(\"\") = %q, want \"\"", got)
	}
}

// The resolver is called once per poll per head; repeated lookups for the same
// cwd must not fork git again.
func TestWorktreeNameForCwdCaches(t *testing.T) {
	_, worktree := initRepoWithWorktree(t, "webapp-cached")
	before := gitRevParseCalls.Load()
	for i := 0; i < 3; i++ {
		if got := worktreeNameForCwd(worktree); got != "webapp-cached" {
			t.Fatalf("worktreeNameForCwd = %q, want %q", got, "webapp-cached")
		}
	}
	if n := gitRevParseCalls.Load() - before; n != 1 {
		t.Errorf("git rev-parse ran %d times for one cwd, want 1", n)
	}
}
