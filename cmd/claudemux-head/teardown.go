package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
// the process, leaving nowhere to run git from. That startup call is also why
// this needs a deadline like every other subprocess in this feature, not
// fewer: a hung git here (a network filesystem, a stalled credential helper)
// would block newModel and the head would never render at all. "" is already
// the safe answer on any failure, so bounding the wait only fixes the hang.
func mainCheckoutFor(dir string) string {
	if dir == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse",
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

// teardownSubmitTimeout bounds the wait for evidence that the wrap-up command
// actually reached claude. Injecting keystrokes into someone else's TUI is
// best-effort, so it is checked rather than assumed: a command left typed but
// unsubmitted aborts loudly instead of hanging in "wrapping up…" forever.
const teardownSubmitTimeout = 10 * time.Second

// teardownExitTimeout bounds the wait for claude to exit. On expiry the
// session is left ALIVE — letting claude finish on its own terms is the entire
// reason the kill comes last.
const teardownExitTimeout = 15 * time.Second

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

// teardownKeyDelay separates the literal text from the Enter that submits it.
// Claude Code opens a completion popup as a slash command is typed; an Enter
// arriving in the same burst can be consumed selecting the completion instead
// of submitting the line. A quarter second is imperceptible to the user and
// ample for the TUI to settle.
const teardownKeyDelay = 250 * time.Millisecond

// teardownTmuxTimeout bounds each tmux subprocess, matching the ceiling used
// everywhere else in this package. A wedged tmux server must never block the
// TUI.
const teardownTmuxTimeout = 2 * time.Second

// teardownSentMsg reports the outcome of typing the wrap-up command. note is
// empty on success and an abort reason otherwise — it is rendered verbatim in
// the status chip.
type teardownSentMsg struct{ note string }

// teardownProbeMsg carries one ready-gate observation.
type teardownProbeMsg struct{ worktreeGone bool }

// claudeGoneMsg reports whether any pane in this session is still running
// claude.
type claudeGoneMsg struct{ gone bool }

// teardownSendCmd types text into the session's claude pane and submits it.
//
// The pane is resolved here rather than cached on the model so it is always
// the pane whose transcript the head currently follows, even if the session
// rotated a moment ago.
func teardownSendCmd(selfPane, paneDir, text string) tea.Cmd {
	return func() tea.Msg {
		_, _, pane, ok := mappedTranscript(selfPane, paneDir)
		if !ok || pane == "" {
			return teardownSentMsg{note: "no claude pane"}
		}
		literal, ok := sendLiteralArgs(pane, text)
		if !ok {
			return teardownSentMsg{note: "wrap-up didn't submit"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		defer cancel()
		if err := exec.CommandContext(ctx, "tmux", literal...).Run(); err != nil {
			return teardownSentMsg{note: "wrap-up didn't submit"}
		}

		time.Sleep(teardownKeyDelay) // see teardownKeyDelay; this runs off the Update loop

		enter, ok := sendEnterArgs(pane)
		if !ok {
			return teardownSentMsg{note: "wrap-up didn't submit"}
		}
		if err := exec.CommandContext(ctx, "tmux", enter...).Run(); err != nil {
			return teardownSentMsg{note: "wrap-up didn't submit"}
		}
		// Success here means the keystrokes were delivered, NOT that claude
		// accepted them. The model separately watches the transcript for
		// evidence of a submitted prompt and aborts on teardownSubmitTimeout.
		return teardownSentMsg{}
	}
}

// teardownProbeCmd takes one ready-gate reading.
func teardownProbeCmd(workDir, mainCheckout string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		defer cancel()
		return teardownProbeMsg{worktreeGone: worktreeIsGone(ctx, workDir, mainCheckout)}
	}
}

// claudeGoneCmd reports whether claude has exited.
//
// This goes through listPanes/claudePaneCandidates directly rather than
// mappedTranscript, because mappedTranscript's ok=false collapses two very
// different situations into one value: a genuinely empty pane listing (every
// candidate exited) and a listPanes call that failed or timed out (a wedged
// tmux server, a transient error) and so returned "" regardless. A live
// session always lists at least this pane, so an empty listing here is never
// evidence of exit — it means the observation itself failed. Only a
// non-empty listing with zero claude/node candidates is confirmed exit.
// Outside tmux nothing is observable either, so it reports not-gone in every
// unobservable case — the exit wait then times out rather than falling
// through to an irreversible kill-session.
func claudeGoneCmd(selfPane string) tea.Cmd {
	return func() tea.Msg {
		if selfPane == "" {
			return claudeGoneMsg{}
		}
		listing := listPanes(selfPane)
		if listing == "" {
			return claudeGoneMsg{}
		}
		return claudeGoneMsg{gone: len(claudePaneCandidates(listing, selfPane)) == 0}
	}
}

// killSessionCmd ends the tmux session this pane lives in. It is the last
// thing the process does: the kill takes the head down with everything else,
// so there is no message to return and no state to render afterwards.
//
// nil when there is no pane to resolve a session from, so callers can append
// it unconditionally — the same contract as renameTabCmd.
func killSessionCmd(selfPane string) tea.Cmd {
	if selfPane == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, "tmux", "display-message",
			"-p", "-t", selfPane, "#{session_name}").Output()
		if err != nil {
			return nil
		}
		args, ok := killSessionArgs(strings.TrimSpace(string(out)))
		if !ok {
			return nil
		}
		_ = exec.CommandContext(ctx, "tmux", args...).Run()
		return nil
	}
}
