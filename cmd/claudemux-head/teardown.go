package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// worktreeListed reports whether `git worktree list --porcelain` output names
// path as a worktree of the repo. Each stanza opens with a "worktree <abs
// path>" line; every other line is ignored.
//
// Paths are compared after filepath.Clean so a trailing slash cannot cause a
// false negative. Callers pass a symlink-resolved path: git prints resolved
// paths, and on macOS a /var/... work dir is really /private/var/..., which
// would otherwise never match.
func worktreeListed(porcelain, path string) bool {
	if path == "" {
		return false
	}
	want := filepath.Clean(path)
	for _, line := range strings.Split(porcelain, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree ")
		if !ok {
			continue
		}
		if filepath.Clean(strings.TrimSpace(rest)) == want {
			return true
		}
	}
	return false
}

// mainCheckoutFromCommonDir turns `git rev-parse --git-common-dir` output into
// the main checkout's path: the common dir is <main>/.git, so its parent is the
// checkout. Empty output yields empty, never ".".
func mainCheckoutFromCommonDir(out string) string {
	common := strings.TrimSpace(out)
	if common == "" {
		return ""
	}
	return filepath.Dir(common)
}

// mainCheckoutFor resolves the main checkout of the repo containing dir, or ""
// when dir is not in a repo, git is missing, or git is too old for
// --path-format (added in 2.31; without it the output could be relative and
// unusable once dir is deleted).
//
// Called once at startup, while dir still exists: by the time a teardown needs
// it, the wrap-up command may have deleted the working directory out from under
// the process, leaving nowhere to run git from.
func mainCheckoutFor(dir string) string {
	if dir == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse",
		"--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	return mainCheckoutFromCommonDir(string(out))
}

// worktreeIsGone reports whether the session's worktree has been torn down.
//
// Two ways to be gone, because a wrap-up may do either: the directory is
// deleted (the common case — `git worktree remove`), or the directory survives
// but git no longer registers it. Anything unclear — no main checkout captured,
// git unavailable, a stat error that isn't "not exist" — reports NOT gone. The
// gate this feeds guards a kill-session, so uncertainty must hold it shut.
func worktreeIsGone(ctx context.Context, workDir, mainCheckout string) bool {
	if workDir == "" {
		return false
	}
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		return true
	}
	if mainCheckout == "" {
		return false
	}
	out, err := exec.CommandContext(ctx, "git", "-C", mainCheckout,
		"worktree", "list", "--porcelain").Output()
	if err != nil {
		return false
	}
	return !worktreeListed(string(out), workDir)
}
