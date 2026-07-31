package main

import (
	"context"
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
	repo, wt := initRepoWithWorktree(t, "wt")

	if got := mainCheckoutFor(wt); got != repo {
		t.Errorf("mainCheckoutFor = %q, want %q", got, repo)
	}
	if worktreeIsGone(context.Background(), wt, repo) {
		t.Error("live worktree reported as gone")
	}

	cmd := exec.Command("git", "-C", repo, "worktree", "remove", "--force", wt)
	cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree remove: %v\n%s", err, out)
	}
	if !worktreeIsGone(context.Background(), wt, repo) {
		t.Error("removed worktree reported as present")
	}
}
