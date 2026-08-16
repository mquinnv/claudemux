package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Conduct-mode publication: the lobby tells the fleet whether the conductor is
// live via one GLOBAL tmux user option, the reverse direction of the
// session-scoped options in statepub.go. The value is "<mode> <unix-seconds>",
// re-published on every lobby poll — the timestamp is a heartbeat, so a lobby
// that crashed (or was killed with the option still set) reads as absent once
// the value goes stale, instead of leaving a lying "conducting" behind forever.
const conductOption = "@claudemux_conducting"

// conductStaleAfter bounds trust in a published value. The lobby republishes
// every poll (~1s), so five seconds tolerates a few missed beats from a slow
// tmux without letting a dead lobby's last word stand for long. It also covers
// a lobby restart (R / auto-restart): the fresh process is publishing again
// well inside the window, so heads never blink their chip off across it.
const conductStaleAfter = 5 * time.Second

// conductMode derives the published mode from the lobby's standby flag and the
// conductor's phase. Escorting is just conducting in motion; paused keeps its
// own name because the lobby renders it differently, while heads treat it as
// conduct-on (handing a paused session back can still dispatch the client).
func conductMode(standby bool, phase swPhase) string {
	switch {
	case standby:
		return "standby"
	case phase == swPaused:
		return "paused"
	}
	return "conducting"
}

func conductPublishValue(mode string, now time.Time) string {
	return fmt.Sprintf("%s %d", mode, now.Unix())
}

// parseConductValue decodes a published value against now. ok=false for
// anything that should read as "no live lobby": absent, malformed, or a
// heartbeat older than conductStaleAfter. A slightly-future timestamp (clock
// skew, sub-second truncation) is fresh, not malformed.
func parseConductValue(v string, now time.Time) (string, bool) {
	fields := strings.Fields(v)
	if len(fields) != 2 {
		return "", false
	}
	ts, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return "", false
	}
	if now.Sub(time.Unix(ts, 0)) >= conductStaleAfter {
		return "", false
	}
	return fields[0], true
}

// conductPublishArgs is the tmux argv publishing mode. Global (-g), unlike the
// statepub options: conduct mode is a fleet-wide fact, not a session's.
func conductPublishArgs(mode string, now time.Time) []string {
	return []string{"set-option", "-g", conductOption, conductPublishValue(mode, now)}
}

// publishConductCmd publishes fire-and-forget with the same hard deadline as
// every other tmux shell-out here.
func publishConductCmd(mode string, now time.Time) tea.Cmd {
	args := conductPublishArgs(mode, now)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "tmux", args...).Run()
		return nil
	}
}

// unsetConductOption removes the option, best effort — called synchronously by
// a lobby quitting for good (not restarting), so heads drop their chip
// immediately instead of waiting out the staleness window.
func unsetConductOption() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "tmux", "set-option", "-gu", conductOption).Run()
}

// readConductOption fetches the raw published value for the head's poll. -q
// keeps a missing option to an empty string rather than an error.
func readConductOption(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "tmux", "show-option", "-gqv", conductOption).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

var conductChipStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575"))

// conductChip renders the head's conduct-mode chip from the raw option value,
// or "" when nothing should show. Presence means "the conductor may move you
// when this session finishes"; standby, a stale heartbeat, and no lobby at all
// are deliberately indistinguishable — in all three, nothing will move you.
func conductChip(raw string, now time.Time) string {
	mode, ok := parseConductValue(raw, now)
	if !ok || mode == "standby" {
		return ""
	}
	return conductChipStyle.Render("⏵ conduct")
}
