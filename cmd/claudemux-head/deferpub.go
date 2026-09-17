package main

import (
	"context"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Defer publication: a session-scoped tmux user option, mirroring the shape
// of statepub.go's options rather than conductpub.go's global heartbeat — the
// mark belongs to one session, not the fleet, and it must survive both head
// and lobby restarts by living on the tmux session itself. Value "1" means
// deferred; unset (or anything else) means normal. There is deliberately no
// heartbeat/staleness handling like conductOption's: a defer mark has no
// process keeping it alive, so it stays exactly as long as the user set it.
const deferOption = "@claudemux_defer"

// deferReasonOption holds the blocker the user typed when deferring — what
// the session is waiting on. It lives beside deferOption on the same session
// and only means anything while deferOption is "1"; clearing the defer unsets
// both, so a stale reason can never resurface on a later defer.
const deferReasonOption = "@claudemux_defer_reason"

// deferReasonMaxCells caps the blocker text in the head's chip. The chip sits
// ahead of the worktree chip on a line clipLine truncates from the right, so
// an essay-length blocker would otherwise push everything after it off-screen.
const deferReasonMaxCells = 48

// deferArgs builds the tmux argv toggling target's mark: set to "1" (plus the
// blocker, when one was given) when on, both unset (-u) when off. target is a
// pane id (as statepub uses) or a session name (as the lobby's row toggle
// uses) — tmux resolves either the same way -t always does. Both options go
// in one ";"-chained tmux invocation, so a poll never sees the mark from one
// defer paired with the reason from another.
func deferArgs(target string, on bool, reason string) []string {
	if !on {
		return []string{"set-option", "-t", target, "-u", deferOption,
			";", "set-option", "-t", target, "-u", deferReasonOption}
	}
	args := []string{"set-option", "-t", target, deferOption, "1", ";"}
	if reason = sanitizeDeferReason(reason); reason != "" {
		// tmux reads any argument ending in ";" as a command separator, even
		// from argv, and would drop it; "\;" is its escape for a literal one.
		if strings.HasSuffix(reason, ";") {
			reason = strings.TrimSuffix(reason, ";") + `\;`
		}
		return append(args, "set-option", "-t", target, deferReasonOption, reason)
	}
	return append(args, "set-option", "-t", target, "-u", deferReasonOption)
}

// sanitizeDeferReason flattens typed text to one trimmed line with no control
// characters. Tabs and newlines matter beyond looks: the lobby reads the
// reason back as a field of a tab-separated list-sessions line.
func sanitizeDeferReason(reason string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, reason))
}

// setDeferCmd fires the toggle, fire-and-forget with the usual hard deadline
// so a wedged tmux can never block either surface's Update loop.
func setDeferCmd(target string, on bool, reason string) tea.Cmd {
	args := deferArgs(target, on, reason)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "tmux", args...).Run()
		return nil
	}
}

// readDeferOption fetches the raw mark and its blocker for the head's poll in
// one tmux call. An unset option expands to an empty string, and a failed
// read is "" for both, same as readConductOption.
func readDeferOption(ctx context.Context, target string) (raw, reason string) {
	out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "-t", target,
		"#{"+deferOption+"}\t#{"+deferReasonOption+"}").Output()
	if err != nil {
		return "", ""
	}
	raw, reason, _ = strings.Cut(strings.TrimRight(string(out), "\n"), "\t")
	return strings.TrimSpace(raw), strings.TrimSpace(reason)
}

// swDeferStyle and swBadgeDeferStyle share one color across both surfaces —
// ANSI 256 "45" (cyan) — deliberately NOT a dim/faint style: a deferred
// session is one that must not be forgotten, so it stays as visually loud as
// a waiting one, just a different hue. Not "39": that's swBusyStyle's
// "Thinking" blue already, and defer needs a hue of its own so a deferred row
// can't be mistaken for a busy one at a glance.
var (
	swDeferStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("45"))
	swBadgeDeferStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("232")).Background(lipgloss.Color("45"))
)

// swDeferBadgeText is the lobby row's DEFER badge, leading separator space
// included — the exact text the row loop appends after the model column.
// Extracted so swTopicW's reserve calculation (switchboardtui.go) and the
// per-row render measure and emit the identical string; if they drifted, a
// too-small reserve would let clipLine truncate the badge again.
func swDeferBadgeText() string {
	return " " + swBadgeDeferStyle.Render(" DEFER ")
}

// deferChip renders the head's defer chip from the raw option value: visible
// only when the mark is actually set, blank otherwise (no lobby-liveness
// gating here, unlike conductChip — the mark means something with or without
// a lobby watching). A recorded blocker rides along after the label, capped
// at deferReasonMaxCells.
func deferChip(raw, reason string) string {
	if raw != "1" {
		return ""
	}
	if reason = sanitizeDeferReason(reason); reason != "" {
		return swDeferStyle.Render("◆ defer: " + ansi.Truncate(reason, deferReasonMaxCells, "…"))
	}
	return swDeferStyle.Render("◆ defer")
}

// deferPromptText is the text of the blocker prompt both surfaces show while
// `d` is collecting one: the typed input with a cursor after it.
func deferPromptText(input string) string {
	return "◆ blocker: " + input + "▌"
}

// deferInputKey applies one keypress to a blocker being typed, the same
// literal-text rules as the lobby's new-session prompt: printable runes and
// space are typed, backspace deletes a rune, everything else is ignored.
func deferInputKey(input string, msg tea.KeyMsg) string {
	switch {
	case msg.Type == tea.KeyBackspace:
		if r := []rune(input); len(r) > 0 {
			return string(r[:len(r)-1])
		}
	case msg.Type == tea.KeyRunes && !msg.Alt:
		return input + string(msg.Runes)
	case msg.Type == tea.KeySpace:
		return input + " "
	}
	return input
}
