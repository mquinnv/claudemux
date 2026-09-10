package main

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The `⚠ no worktree` warning used to fire when a marked session's first turn
// ended outside a worktree. That was the right signal while the hook asked for
// the worktree "before any other tool call": a turn that ended without one meant
// the model had skipped the call. The hook now asks for the worktree right
// before the FIRST CHANGE instead, so a session that reads, investigates or
// answers a question is supposed to finish turn after turn outside a worktree,
// and a chip that called every one of those a failure would be noise — the
// kind that teaches you to stop reading the chip.
//
// What the marker actually exists to prevent is work landing in the SHARED
// checkout. So that is what this probe watches: `git status --porcelain` of the
// main checkout, compared against a snapshot taken when the head started. Any
// line that is present now and was not present then — a modified tracked file,
// a new untracked one — means the main checkout got dirtier on this session's
// watch while the session was still outside a worktree. That, and only that,
// is worth a ⚠.
//
// A baseline rather than "is the tree clean" because main checkouts are
// routinely a little dirty for reasons that have nothing to do with the
// session — a stray settings.local.json, a scratch file from last week — and
// accusing the session of those would be the same noise in a different hat.

// mainDirtyProbeInterval is how often the main checkout is re-read while a
// marked session is outside a worktree and has not yet dirtied it. One
// `git status` every few seconds on a repo the user is not otherwise touching
// is cheap; the teardown gate probes at a similar cadence. The probe stops on
// its own once the session enters a worktree or the warning latches.
const mainDirtyProbeInterval = 5 * time.Second

// mainDirtyMsg carries one probe's answer back to Update.
type mainDirtyMsg struct{ dirtied bool }

// mainStatusSnapshot returns `git status --porcelain` for dir. ok is false on
// any failure — git missing, not a repo, timeout — and a caller must then
// treat the baseline as unavailable rather than as "clean": an empty baseline
// would make every pre-existing untracked file look like the session's doing.
func mainStatusSnapshot(ctx context.Context, dir string) (status string, ok bool) {
	if dir == "" {
		return "", false
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return "", false
	}
	return string(bytes.TrimRight(out, "\n")), true
}

// dirtiedSince reports whether current contains a porcelain line that baseline
// did not. Lines are compared whole — a file that was untracked at startup and
// is still untracked matches itself; one that was clean and is now modified,
// or did not exist and now does, is a new line. A line that DISAPPEARED (the
// user cleaned something up) is not dirt and is ignored.
func dirtiedSince(baseline, current string) bool {
	seen := make(map[string]bool)
	for _, line := range strings.Split(baseline, "\n") {
		if line != "" {
			seen[line] = true
		}
	}
	for _, line := range strings.Split(current, "\n") {
		if line != "" && !seen[line] {
			return true
		}
	}
	return false
}

// mainDirtyProbeCmd re-reads the main checkout's status and compares it with
// the startup baseline. A probe that cannot read the status reports not
// dirtied: this feeds a warning, not a gate, and a warning on "I could not
// tell" would be exactly the false accusation the baseline exists to avoid.
func mainDirtyProbeCmd(mainCheckout, baseline string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		defer cancel()
		current, ok := mainStatusSnapshot(ctx, mainCheckout)
		if !ok {
			return mainDirtyMsg{}
		}
		return mainDirtyMsg{dirtied: dirtiedSince(baseline, current)}
	}
}

// mainDirtyProbeDue reports whether a probe should be issued now. Only a
// marked session with a usable baseline, a prompt behind it, no worktree yet
// and no warning yet has anything to learn — and only one probe at a time,
// spaced by mainDirtyProbeInterval.
func (m model) mainDirtyProbeDue(now time.Time) bool {
	if !m.worktreePending || !m.mainStatusBaselineOK || m.mainDirtied || m.mainDirtyProbing {
		return false
	}
	if m.firstPrompt == "" || m.observedWorktree() != "" {
		return false
	}
	return now.Sub(m.mainDirtyProbeAt) >= mainDirtyProbeInterval
}
