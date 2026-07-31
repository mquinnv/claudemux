package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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

// teardownPhase is where a session teardown has got to. It advances on the `x`
// key and on poll ticks; every phase but teardownIdle is visible in the status
// line, because a key that arms a kill-session must never be armed silently.
type teardownPhase int

const (
	teardownIdle teardownPhase = iota
	// teardownSent: the wrap-up command has been typed into the claude pane
	// (or skipped, when teardown.command is empty) and the ready gate is
	// being polled.
	teardownSent
	// teardownReady: the gate is open. The next `x` exits claude and kills
	// the session.
	teardownReady
	// teardownExiting: /exit has been sent; waiting for claude to actually be
	// gone before killing the session.
	teardownExiting
)

// teardownNoteTTL is how long an abort reason stays on the status line. Long
// enough to read after glancing away, short enough that it doesn't look like
// persistent state.
const teardownNoteTTL = 5 * time.Second

// teardownTurnEnded reports whether claude has stopped working.
//
// StateAwaiting counts as ended on purpose: the wrap-up command asking for its
// confirmation is a legitimate resting point, and the gate's second condition
// (the worktree being gone) cannot hold until that question is answered
// anyway. StateCompacting does NOT count — the session is mid-turn and about
// to keep going.
func teardownTurnEnded(kind StateKind) bool {
	switch kind {
	case StateThinking, StateTool, StateCompacting:
		return false
	}
	return true
}

// teardownGateOpen reports whether the second `x` press should be offered.
//
// For a worktree session the worktree must be gone, which is the whole signal
// that the wrap-up actually succeeded: a /done that bailed on uncommitted or
// unpushed work leaves it standing, and the gate stays shut. A session that
// was never in a worktree has no such evidence available, so it gates on the
// turn ending alone.
func teardownGateOpen(kind StateKind, inWorktree, worktreeGone bool) bool {
	if !teardownTurnEnded(kind) {
		return false
	}
	if !inWorktree {
		return true
	}
	return worktreeGone
}

// teardownChip renders the status-line chip for a teardown, or "" when there
// is nothing to show.
//
// An abort note is shown only from teardownIdle: it explains why a teardown
// stopped, so a note left over from an earlier attempt must never shadow a
// live phase.
func teardownChip(p teardownPhase, blocked bool, note string, noteAt, now time.Time) string {
	switch p {
	case teardownSent:
		if blocked {
			return "⏻ worktree still present"
		}
		return "⏻ wrapping up…"
	case teardownReady:
		return "⏻ press x to tear down"
	case teardownExiting:
		return "⏻ exiting claude…"
	}
	if note != "" && now.Sub(noteAt) < teardownNoteTTL {
		return "⏻ " + note
	}
	return ""
}

// sendLiteralArgs builds the tmux call that types text into pane verbatim.
//
// -l sends the string literally: without it tmux parses the argument as key
// names, so a configured command containing "Enter" or "C-c" would be
// interpreted rather than typed.
//
// The Enter that submits is a SEPARATE call (sendEnterArgs) with a delay
// between them — see this file's teardownSendCmd for why.
func sendLiteralArgs(pane, text string) ([]string, bool) {
	if pane == "" || text == "" {
		return nil, false
	}
	return []string{"send-keys", "-t", pane, "-l", text}, true
}

// sendEnterArgs builds the tmux call that submits whatever is in pane's input.
func sendEnterArgs(pane string) ([]string, bool) {
	if pane == "" {
		return nil, false
	}
	return []string{"send-keys", "-t", pane, "Enter"}, true
}

// killSessionArgs builds the tmux call that ends the session.
//
// An empty session is refused rather than defaulted: `kill-session` with no -t
// kills the *current* session, so a failed lookup would still destroy
// something, just not necessarily the right thing.
func killSessionArgs(session string) ([]string, bool) {
	if session == "" {
		return nil, false
	}
	return []string{"kill-session", "-t", session}, true
}
