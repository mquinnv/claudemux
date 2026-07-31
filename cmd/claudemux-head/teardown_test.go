package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWorktreeListed(t *testing.T) {
	const porcelain = `worktree /Users/me/Projects/app
HEAD abc123
branch refs/heads/main

worktree /Users/me/Projects/app/.claude/worktrees/floating-harp
HEAD def456
branch refs/heads/feature
`
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"main checkout", "/Users/me/Projects/app", true},
		{"linked worktree", "/Users/me/Projects/app/.claude/worktrees/floating-harp", true},
		{"trailing slash still matches", "/Users/me/Projects/app/.claude/worktrees/floating-harp/", true},
		{"removed worktree", "/Users/me/Projects/app/.claude/worktrees/gone", false},
		{"prefix is not a match", "/Users/me/Projects/app/.claude/worktrees/floating", false},
		{"empty path", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := worktreeListed(porcelain, tt.path); got != tt.want {
				t.Errorf("worktreeListed(_, %q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// Empty output means git failed or printed nothing; nothing is listed, and the
// caller must not read that as "the worktree is gone".
func TestWorktreeListedEmptyListing(t *testing.T) {
	if worktreeListed("", "/Users/me/Projects/app") {
		t.Error("empty listing matched a path")
	}
}

func TestMainCheckoutFromCommonDir(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{"trailing newline", "/Users/me/Projects/app/.git\n", "/Users/me/Projects/app"},
		{"no newline", "/Users/me/Projects/app/.git", "/Users/me/Projects/app"},
		{"empty", "", ""},
		{"whitespace only", "  \n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mainCheckoutFromCommonDir(tt.out); got != tt.want {
				t.Errorf("mainCheckoutFromCommonDir(%q) = %q, want %q", tt.out, got, tt.want)
			}
		})
	}
}

// A deleted work dir is gone regardless of what git says — the common case,
// and the one where git can't be run from the work dir at all.
func TestWorktreeIsGoneMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "deleted")
	if !worktreeIsGone(context.Background(), missing, "") {
		t.Error("missing work dir reported as present")
	}
}

// A directory that still exists and is still a registered worktree is present.
// This exercises the real git path end to end.
func TestWorktreeIsGoneLiveWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	main := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", main}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("commit", "--allow-empty", "-m", "init")

	wt := filepath.Join(main, "wt")
	run("worktree", "add", "-b", "side", wt)

	// EvalSymlinks because t.TempDir() hands back /var/... on macOS while git
	// reports the resolved /private/var/... — the same normalization the model
	// does at startup.
	resolvedWT, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}
	resolvedMain, err := filepath.EvalSymlinks(main)
	if err != nil {
		t.Fatal(err)
	}

	if got := mainCheckoutFor(resolvedWT); got != resolvedMain {
		t.Errorf("mainCheckoutFor = %q, want %q", got, resolvedMain)
	}
	if worktreeIsGone(context.Background(), resolvedWT, resolvedMain) {
		t.Error("live worktree reported as gone")
	}

	run("worktree", "remove", "--force", wt)
	if !worktreeIsGone(context.Background(), resolvedWT, resolvedMain) {
		t.Error("removed worktree reported as present")
	}
}
