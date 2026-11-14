package main

import (
	"context"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// promptTailLines bounds how much may follow the input box's bottom rule: the
// mode/status footer, which is one line today and wraps to two on a narrow
// pane. A longer tail means the bottom of the pane is something else.
const promptTailLines = 3

// promptPlaceholder is how claude's empty input box starts its hint text
// (`Try "fix typecheck errors"`); the hint is on screen only while the box is
// empty, so it counts as empty, not as a draft.
const promptPlaceholder = `Try "`

// promptEmpty reports whether a capture-pane of the claude pane shows its
// input line empty. Anything it cannot recognise is false.
//
// A wrong "true" types into someone's half-written message, so this keys on the
// whole shape of the idle input box and nothing looser: the last two
// horizontal rules of the capture must enclose exactly ONE line, that line
// must be the "❯" prompt carrying either nothing or the placeholder hint, and
// at most promptTailLines lines may follow the bottom rule. A draft puts text
// after the "❯"; a multi-line draft or a dialog (permission, AskUserQuestion)
// puts several lines between the rules; a "❯" in a dialog's option list is not
// bounded by two rules around a single line.
func promptEmpty(capture string) bool {
	lines := strings.Split(strings.TrimRight(capture, "\n \t"), "\n")
	bottom, top := -1, -1
	for i := len(lines) - 1; i >= 0; i-- {
		if !isRuleLine(lines[i]) {
			continue
		}
		if bottom < 0 {
			bottom = i
			continue
		}
		top = i
		break
	}
	if top < 0 || bottom-top != 2 || len(lines)-1-bottom > promptTailLines {
		return false
	}
	line := strings.TrimSpace(lines[top+1])
	rest, ok := strings.CutPrefix(line, "❯")
	if !ok {
		return false
	}
	rest = strings.TrimSpace(rest)
	return rest == "" || strings.HasPrefix(rest, promptPlaceholder)
}

// isRuleLine reports whether line is a horizontal rule: only "─" cells, and
// enough of them that it is a box edge rather than a stray dash in prose.
func isRuleLine(line string) bool {
	line = strings.TrimSpace(line)
	return strings.Count(line, "─") >= 10 && strings.Trim(line, "─") == ""
}

// deferNudgeText is what gets typed into a session whose blocker has cleared.
// It says what changed and why, because the session was told to wait and has
// no other way to learn the wait is over. A reason that only repeats the
// blocker's name adds nothing, so it is dropped with the empty one.
func deferNudgeText(blocker string, gone bool, reason string) string {
	why := ""
	if reason != "" && reason != blocker {
		why = " (" + reason + ")"
	}
	if gone {
		return "claudemux: " + blocker + " has closed, so this session is no longer deferred on it" + why +
			". Check what it left behind, then pick up where you left off."
	}
	return "claudemux: " + blocker + " has stopped, so this session is no longer deferred on it" + why +
		". Check for its report, then pick up where you left off."
}

// deferDoneChip is the lobby's note that a blocker finished, shown in place of
// the defer chip so the user sees why the session left the deferred group.
func deferDoneChip(blocker string, gone bool) string {
	if gone {
		return swDeferStyle.Render("◆ " + blocker + " closed")
	}
	return swDeferStyle.Render("◆ " + blocker + " finished")
}

// deferReleasedMsg reports that a defer was cleared; nudged is true only when
// the text was both typed and submitted.
type deferReleasedMsg struct{ nudged bool }

// deferReleaseCmd clears selfPane's defer and, when nudge is true and the
// prompt is empty, types text into the claude pane and submits it.
//
// The mark is cleared LAST, whatever happened to the nudge: a conducting lobby
// pulls a session in as soon as it leaves the deferred group, and must not
// carry the user into a pane while text is still being typed. A nudge that
// could not be delivered still clears — the blocker is gone either way.
func deferReleaseCmd(selfPane, paneDir, text string, nudge bool) tea.Cmd {
	return func() tea.Msg {
		nudged := false
		if nudge {
			nudged = nudgePane(selfPane, paneDir, text)
		}
		// One chained invocation, for the same reason as every other clear.
		ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		defer cancel()
		_ = exec.CommandContext(ctx, "tmux", deferArgs(selfPane, false, "")...).Run()
		return deferReleasedMsg{nudged: nudged}
	}
}

// nudgePane types text into the session's claude pane and submits it, only if
// that pane's prompt is verifiably empty. It never clears the line first: a
// draft is the user's, and typing over it is worse than not nudging.
func nudgePane(selfPane, paneDir, text string) bool {
	_, _, pane, ok := mappedTranscript(selfPane, paneDir)
	if !ok || pane == "" {
		return false
	}
	// Each tmux call gets its own deadline; see teardownSendCmd.
	capCtx, cancelCap := context.WithTimeout(context.Background(), teardownTmuxTimeout)
	out, err := exec.CommandContext(capCtx, "tmux", "capture-pane", "-p", "-t", pane).Output()
	cancelCap()
	if err != nil || !promptEmpty(string(out)) {
		return false
	}
	literal, ok := sendLiteralArgs(pane, text)
	if !ok {
		return false
	}
	litCtx, cancelLit := context.WithTimeout(context.Background(), teardownTmuxTimeout)
	err = exec.CommandContext(litCtx, "tmux", literal...).Run()
	cancelLit()
	if err != nil {
		return false
	}
	time.Sleep(teardownKeyDelay) // see teardownKeyDelay; this runs off the Update loop
	enter, ok := sendEnterArgs(pane)
	if !ok {
		return false
	}
	enterCtx, cancelEnter := context.WithTimeout(context.Background(), teardownTmuxTimeout)
	defer cancelEnter()
	return exec.CommandContext(enterCtx, "tmux", enter...).Run() == nil
}
