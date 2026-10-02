package main

import (
	"context"
	"os/exec"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Closing publication: the head's teardown phase as a session-scoped tmux
// user option, so the lobby can list sessions that are on their way out apart
// from the ones still doing work. Unset means no teardown is in flight; any
// other value is a phase (closingPublishValue).
//
// Unlike the defer mark next door, this one HAS a process behind it: it
// mirrors state the head deliberately does not persist (an armed kill must not
// survive a head restart), so the head clears it at start and republishes
// whenever its phase moves. A killed session takes the option with it.
//
// The mark changes where a session is listed and nothing else. The conductor
// ignores it on purpose — a closing session that is waiting is waiting on the
// human (the wrap-up's confirmation, the final x, a blocked gate), which is
// exactly what the conductor exists to bring them to.
const closingOption = "@claudemux_closing"

// closingPublishValue is the machine form of a teardown phase, "" when none
// is in flight. Like statePublishValue this is an interface, not display
// text: the lobby keys on it (closingText) and the web page carries it as-is.
// blocked only qualifies the wrap-up watch; it is stale in every other phase.
func closingPublishValue(p teardownPhase, blocked bool) string {
	switch p {
	case teardownSent:
		if blocked {
			return "blocked"
		}
		return "wrapup"
	case teardownReady:
		return "ready"
	case teardownExiting:
		return "exiting"
	case teardownDirect:
		return "direct"
	}
	return ""
}

// closingArgs builds the tmux argv publishing value, or unsetting the option
// when value is "". ok=false outside tmux.
func closingArgs(selfPane, value string) ([]string, bool) {
	if selfPane == "" {
		return nil, false
	}
	if value == "" {
		return []string{"set-option", "-t", selfPane, "-u", closingOption}, true
	}
	return []string{"set-option", "-t", selfPane, closingOption, value}, true
}

// publishClosingCmd fires the publish, fire-and-forget with the usual hard
// deadline. nil outside tmux.
func publishClosingCmd(selfPane, value string) tea.Cmd {
	args, ok := closingArgs(selfPane, value)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "tmux", args...).Run()
		return nil
	}
}

// maybePublishClosing returns a cmd publishing the teardown phase when it
// changed since the last publish. Called from the tick rather than from each
// transition: the phase moves on keys, probe results, transcript edges and
// aborts, and one comparison a second catches all of them — including any
// added later — at a lag the lobby's own poll already imposes.
func (m *model) maybePublishClosing() tea.Cmd {
	if m.selfPane == "" {
		return nil
	}
	v := closingPublishValue(m.teardown, m.teardownBlocked)
	if v == m.publishedClosing {
		return nil
	}
	m.publishedClosing = v
	return publishClosingCmd(m.selfPane, v)
}

// closingText is a published phase as the lobby prints it, in the head's own
// chip wording so the two surfaces read alike. "" for a session that is not
// closing; an unrecognized value (a newer head's) still reads as closing.
//
// The lobby cannot tell a wrap-up the head typed from one the user did, so
// both read "wrapping up…"; and it names no block reason, which only the head
// probed — the row is one keystroke from the pane that says why.
func closingText(v string) string {
	switch v {
	case "":
		return ""
	case "wrapup":
		return "⏻ wrapping up…"
	case "blocked":
		return "⏻ wrap-up blocked"
	case "ready":
		return "⏻ press x to tear down"
	case "exiting":
		return "⏻ exiting claude…"
	case "direct":
		return "⏻ kill session? press X"
	}
	return "⏻ closing"
}

// swCloseStyle and swBadgeCloseStyle are the closing hue — ANSI 256 "204", a
// rose that is none of the lobby's other signals: waiting orange (214), busy
// blue (39), deferred cyan (45), conducting green (35).
var (
	swCloseStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("204"))
	swBadgeCloseStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("232")).Background(lipgloss.Color("204"))
)

// swCloseBadgeText is the lobby row's CLOSE badge, leading separator space
// included. Deliberately the same width as swDeferBadgeText, so the two share
// one reserve in swTopicW.
func swCloseBadgeText() string {
	return " " + swBadgeCloseStyle.Render(" CLOSE ")
}
