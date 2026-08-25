package main

import (
	"context"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Defer publication: a session-scoped tmux user option, mirroring the shape
// of statepub.go's options rather than conductpub.go's global heartbeat — the
// mark belongs to one session, not the fleet, and it must survive both head
// and lobby restarts by living on the tmux session itself. Value "1" means
// deferred; unset (or anything else) means normal. There is deliberately no
// heartbeat/staleness handling like conductOption's: a defer mark has no
// process keeping it alive, so it stays exactly as long as the user set it.
const deferOption = "@claudemux_defer"

// deferArgs builds the tmux argv toggling target's mark: set to "1" when on,
// unset (-u) when off. target is a pane id (as statepub uses) or a session
// name (as the lobby's row toggle uses) — tmux resolves either the same way
// -t always does.
func deferArgs(target string, on bool) []string {
	if on {
		return []string{"set-option", "-t", target, deferOption, "1"}
	}
	return []string{"set-option", "-t", target, "-u", deferOption}
}

// setDeferCmd fires the toggle, fire-and-forget with the usual hard deadline
// so a wedged tmux can never block either surface's Update loop.
func setDeferCmd(target string, on bool) tea.Cmd {
	args := deferArgs(target, on)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "tmux", args...).Run()
		return nil
	}
}

// readDeferOption fetches the raw mark for the head's poll. -q keeps a
// missing option to an empty string rather than an error, same as
// readConductOption.
func readDeferOption(ctx context.Context, target string) string {
	out, err := exec.CommandContext(ctx, "tmux", "show-option", "-t", target, "-qv", deferOption).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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
// a lobby watching).
func deferChip(raw string) string {
	if raw != "1" {
		return ""
	}
	return swDeferStyle.Render("◆ defer")
}
