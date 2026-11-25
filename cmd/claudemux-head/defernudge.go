package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// promptTailLines bounds how much may follow the input box's bottom rule: the
// mode/status footer, which is one line today and wraps to two on a narrow
// pane. A longer tail means the bottom of the pane is something else.
const promptTailLines = 3

// sgrDim is the escape claude emits immediately before ghost text in the input
// box: the `Try "fix typecheck errors"` hint of a fresh session and the
// suggested next prompt (`/done`, "run the tests") it offers after a turn.
// Typed text is plain, so only a wholly dim line counts as nothing typed.
const sgrDim = "\x1b[2m"

// promptEmpty reports whether a capture-pane -e (escapes kept) of the claude
// pane shows its input line empty. Anything it cannot recognise is false.
//
// A wrong "true" types into someone's half-written message, so this keys on the
// whole shape of the idle input box and nothing looser: the last two
// horizontal rules of the capture must enclose exactly ONE line, that line
// must be the "❯" prompt carrying either nothing or only dim ghost text (the
// placeholder hint or a suggested prompt; a typed `Try "` is plain text and
// is a draft), and at most promptTailLines
// lines may follow the bottom rule. A multi-line draft or a dialog
// (permission, AskUserQuestion) puts several lines between the rules. A footer
// naming a vim mode other than INSERT is false too; see vimNotInsert. With
// vimMode on (claudeVimMode), the footer must show "-- INSERT --": claude
// prints no indicator at all in NORMAL mode, so its absence is the only sign
// the box would run typed text as vim commands.
func promptEmpty(capture string, vimMode bool) bool {
	rawLines := strings.Split(strings.TrimRight(capture, "\n \t"), "\n")
	lines := make([]string, len(rawLines))
	for i, l := range rawLines {
		lines[i] = ansi.Strip(l)
	}
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
	tail := lines[bottom+1:]
	if vimNotInsert(tail) {
		return false
	}
	if vimMode && !strings.Contains(strings.Join(tail, "\n"), "-- INSERT --") {
		return false
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(lines[top+1]), "❯")
	if !ok {
		return false
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return true
	}
	return dimOnly(rawLines[top+1])
}

// dimOnly reports whether everything after the "❯" on a raw (escapes kept)
// prompt line is drawn dim: the dim escape comes straight after the glyph and
// its padding, and no other escape follows until the text has ended. Ghost
// text typed over disappears; a draft, even one with a dim part, must not be
// typed into, so any plain character or mid-line style change is a no.
func dimOnly(raw string) bool {
	_, after, ok := strings.Cut(raw, "❯")
	if !ok {
		return false
	}
	body, ok := strings.CutPrefix(strings.TrimLeft(after, " \u00a0"), sgrDim)
	if !ok {
		return false
	}
	text, trailer, reset := strings.Cut(body, "\x1b")
	if reset && strings.TrimSpace(ansi.Strip("\x1b"+trailer)) != "" {
		return false
	}
	return strings.TrimSpace(text) != ""
}

// claudeVimMode reports whether claude's editor is in vim mode, read from the
// top-level "editorMode" of .claude.json and settings.json (under
// CLAUDE_CONFIG_DIR when set, else the home directory). True if either file
// says "vim"; false if at least one was read and none says vim; true when
// neither could be read, because a wrong "false" types into a prompt that may
// be in NORMAL mode. Only that one field is decoded.
func claudeVimMode(getenv func(string) string, home string) bool {
	paths := []string{filepath.Join(home, ".claude.json"), filepath.Join(home, ".claude", "settings.json")}
	if dir := getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		paths = []string{filepath.Join(dir, ".claude.json"), filepath.Join(dir, "settings.json")}
	}
	parsed := false
	for _, p := range paths {
		blob, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var body struct {
			EditorMode string `json:"editorMode"`
		}
		if json.Unmarshal(blob, &body) != nil {
			continue
		}
		parsed = true
		if body.EditorMode == "vim" {
			return true
		}
	}
	return !parsed
}

// vimNotInsert reports whether the footer names a vim mode that is not INSERT.
// With vim mode on, claude's footer reads "-- INSERT --" while typing; real
// captures of NORMAL mode show NO indicator at all, so this can only catch
// builds that print one (NORMAL, VISUAL). It is a partial guard, not proof.
func vimNotInsert(tail []string) bool {
	for _, l := range tail {
		for _, m := range []string{"-- NORMAL --", "-- VISUAL --", "-- VISUAL LINE --"} {
			if strings.Contains(l, m) {
				return true
			}
		}
	}
	return false
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

// deferDoneChip is the head's note that a blocker finished, shown in place of
// the defer chip so the user sees why the session left the deferred group.
func deferDoneChip(blocker string, gone bool) string {
	if gone {
		return swDeferStyle.Render("◆ " + blocker + " closed")
	}
	return swDeferStyle.Render("◆ " + blocker + " finished")
}

// deferReleasedMsg reports that a defer was cleared; nudged is true only when
// the text was both typed and submitted, cleared only when the tmux call that
// unsets the mark succeeded.
type deferReleasedMsg struct{ nudged, cleared bool }

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
		err := exec.CommandContext(ctx, "tmux", deferArgs(selfPane, false, "")...).Run()
		return deferReleasedMsg{nudged: nudged, cleared: err == nil}
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
	return nudgeClaudePane(pane, text)
}

// nudgeClaudePane is nudgePane for a caller that already knows the claude
// pane: the lobby reads it off the row, and the head's undefer uses the pane
// its poll last bound to.
func nudgeClaudePane(pane, text string) bool {
	return nudgeClaudePaneOutcome(pane, text) == nudgeSent
}

// nudgeOutcome says how far a nudge got, because a bare false hides a
// difference that matters: text typed but never submitted sits in the prompt.
type nudgeOutcome int

const (
	nudgeRefused nudgeOutcome = iota // nothing typed
	nudgeTyped                       // text typed, Enter not confirmed
	nudgeSent                        // text typed and submitted
)

// nudgeClaudePaneOutcome is nudgeClaudePane reporting how far it got.
func nudgeClaudePaneOutcome(pane, text string) nudgeOutcome {
	if pane == "" {
		return nudgeRefused
	}
	home, _ := os.UserHomeDir()
	vimOn := claudeVimMode(os.Getenv, home)
	// Each tmux call gets its own deadline; see teardownSendCmd.
	capCtx, cancelCap := context.WithTimeout(context.Background(), teardownTmuxTimeout)
	out, err := exec.CommandContext(capCtx, "tmux", "capture-pane", "-e", "-p", "-t", pane).Output()
	cancelCap()
	// The capture and the keystrokes cannot be atomic: a user typing in the
	// gap can still be typed over. The window is milliseconds and tmux has no
	// way to close it.
	if err != nil || !promptEmpty(string(out), vimOn) {
		return nudgeRefused
	}
	literal, ok := sendLiteralArgs(pane, text)
	if !ok {
		return nudgeRefused
	}
	litCtx, cancelLit := context.WithTimeout(context.Background(), teardownTmuxTimeout)
	err = exec.CommandContext(litCtx, "tmux", literal...).Run()
	cancelLit()
	if err != nil {
		return nudgeRefused
	}
	time.Sleep(teardownKeyDelay) // see teardownKeyDelay; this runs off the Update loop
	enter, ok := sendEnterArgs(pane)
	if !ok {
		return nudgeTyped
	}
	enterCtx, cancelEnter := context.WithTimeout(context.Background(), teardownTmuxTimeout)
	defer cancelEnter()
	if exec.CommandContext(enterCtx, "tmux", enter...).Run() != nil {
		return nudgeTyped
	}
	return nudgeSent
}
