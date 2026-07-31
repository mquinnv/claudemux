package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
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

func TestTeardownTurnEnded(t *testing.T) {
	tests := []struct {
		kind StateKind
		want bool
	}{
		{StateIdle, true},
		{StateAwaiting, true}, // a wrap-up asking its confirmation is a real pause
		{StateError, true},
		{StateThinking, false},
		{StateTool, false},
		{StateCompacting, false},
	}
	for _, tt := range tests {
		if got := teardownTurnEnded(tt.kind); got != tt.want {
			t.Errorf("teardownTurnEnded(%v) = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestTeardownGateOpen(t *testing.T) {
	tests := []struct {
		name         string
		kind         StateKind
		inWorktree   bool
		worktreeGone bool
		want         bool
	}{
		{"worktree gone, turn ended", StateIdle, true, true, true},
		{"worktree gone but still working", StateTool, true, true, false},
		{"turn ended, worktree survives", StateIdle, true, false, false},
		{"awaiting confirmation, worktree survives", StateAwaiting, true, false, false},
		{"not a worktree, turn ended", StateIdle, false, false, true},
		{"not a worktree, still working", StateThinking, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := teardownGateOpen(tt.kind, tt.inWorktree, tt.worktreeGone)
			if got != tt.want {
				t.Errorf("teardownGateOpen(%v, %v, %v) = %v, want %v",
					tt.kind, tt.inWorktree, tt.worktreeGone, got, tt.want)
			}
		})
	}
}

func TestTeardownChip(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		phase   teardownPhase
		blocked bool
		note    string
		noteAt  time.Time
		want    string
	}{
		{"idle, nothing to say", teardownIdle, false, "", time.Time{}, ""},
		{"sent", teardownSent, false, "", time.Time{}, "⏻ wrapping up…"},
		{"sent but blocked", teardownSent, true, "", time.Time{}, "⏻ worktree still present"},
		{"ready", teardownReady, false, "", time.Time{}, "⏻ press x to tear down"},
		{"exiting", teardownExiting, false, "", time.Time{}, "⏻ exiting claude…"},
		{"fresh abort note", teardownIdle, false, "claude didn't exit", now.Add(-2 * time.Second), "⏻ claude didn't exit"},
		{"expired abort note", teardownIdle, false, "claude didn't exit", now.Add(-6 * time.Second), ""},
		{"note is ignored mid-teardown", teardownSent, false, "stale", now, "⏻ wrapping up…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := teardownChip(tt.phase, tt.blocked, tt.note, tt.noteAt, now)
			if got != tt.want {
				t.Errorf("teardownChip() = %q, want %q", got, tt.want)
			}
		})
	}
}
