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

// Conduct-mode requests: the reverse direction of conductOption, and the only
// way a head can change the mode. The lobby OWNS conduct mode and republishes
// it every poll, so a head writing @claudemux_conducting directly would be
// overwritten inside a beat. Instead space in a head leaves a request here and
// the lobby applies it on its next poll, through the same code path as its own
// space key.
const conductRequestOption = "@claudemux_conduct_request"

// conductRequestVerb is the only request there is. A head deliberately asks
// for a FLIP rather than naming the mode it wants: it would be naming it from
// a value up to a poll old, and two heads pressing space at once would then
// fight over an absolute instead of composing into two flips.
const conductRequestVerb = "toggle"

// conductRequestValue is "<verb> <unix-nanos>". Nanoseconds, unlike the
// heartbeat's seconds: the value doubles as the lobby's already-applied
// marker (see swModel.conductReq), so two presses inside one second must not
// collapse into one token.
func conductRequestValue(now time.Time) string {
	return fmt.Sprintf("%s %d", conductRequestVerb, now.UnixNano())
}

// parseConductRequest decodes a published request against now, returning the
// raw value as the lobby's dedup token. ok=false for absent, malformed, an
// unknown verb (a newer head talking a protocol this lobby doesn't know), or a
// request older than conductStaleAfter — the same window the heartbeat uses,
// here bounding how long a keypress stays live. That staleness check is also
// what keeps a lobby STARTING UP from firing whatever request happens to be
// sitting in the option: anything from before it existed is long expired,
// while a press from a second ago (a head toggling across a lobby restart)
// still lands, which is the behavior the user asked for.
func parseConductRequest(v string, now time.Time) (string, bool) {
	fields := strings.Fields(v)
	if len(fields) != 2 || fields[0] != conductRequestVerb {
		return "", false
	}
	ns, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return "", false
	}
	if now.Sub(time.Unix(0, ns)) >= conductStaleAfter {
		return "", false
	}
	return v, true
}

// conductRequestArgs is the tmux argv publishing a request. Global (-g) like
// the heartbeat it answers: which head pressed space doesn't matter, only that
// somebody did.
func conductRequestArgs(now time.Time) []string {
	return []string{"set-option", "-g", conductRequestOption, conductRequestValue(now)}
}

// requestConductToggleCmd asks the lobby to flip conduct mode, fire-and-forget
// with the same hard deadline as every other tmux shell-out here. A lost
// request just leaves the mode alone; the head's optimistic chip expires on
// its own (see conductPendingWindow).
func requestConductToggleCmd(now time.Time) tea.Cmd {
	args := conductRequestArgs(now)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "tmux", args...).Run()
		return nil
	}
}

// readConductRequestOption fetches the raw request for the lobby's poll, with
// the same -q "missing is empty" handling as readConductOption.
func readConductRequestOption(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "tmux", "show-option", "-gqv", conductRequestOption).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// conductPendingWindow bounds a head's optimistic flip after space. Three
// seconds covers the round trip in the bad case — this head's poll, the
// lobby's, and a slow tmux on each — while staying short enough that a request
// nobody applied (a lobby that quit between the read and the press) corrects
// itself before the user has read the chip twice.
const conductPendingWindow = 3 * time.Second

// conductOn reports whether a published mode means the conductor may move you.
// Paused counts — it is conduct-on, merely idle this instant — so this is
// exactly the distinction the head's chip draws, and the one a head's space
// key flips.
func conductOn(mode string) bool {
	return mode != "" && mode != "standby"
}

var (
	conductChipStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575"))
	// Orange, the same hue the lobby gives its own STANDBY badge and status
	// line (see swBadgeStandbyStyle), so one mode does not wear two colors
	// depending on which surface you read it from.
	stayChipStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

// conductChip renders the head's conduct-mode chip from the raw option value.
//
// Presence means A LIVE LOBBY IS WATCHING; the chip then says what it will do:
// "⏵ conduct" — it may move you when this session finishes — or "⏸ stay",
// standby, which is a state the user deliberately switched on and so reads
// back rather than going quiet. A stale heartbeat and no lobby at all stay
// blank and indistinguishable: there is no process to report on, and a chip
// that outlived its lobby would be describing nobody.
func conductChip(raw string, now time.Time) string {
	mode, ok := parseConductValue(raw, now)
	if !ok {
		return ""
	}
	if !conductOn(mode) {
		return stayChipStyle.Render("⏸ stay")
	}
	return conductChipStyle.Render("⏵ conduct")
}
