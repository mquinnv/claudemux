package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// The arrival banner: after the conductor escorts the client into a session,
// a small display-popup names where it landed. The popup runs `claudemux-head
// banner <session>`, which prints the card and holds until a key is pressed or
// swBannerHold elapses — a popup swallows keystrokes while open, so dismissing
// on the first key caps the cost at one swallowed press.

const (
	swBannerHold = 2 * time.Second
	// swBannerMaxW caps the card's content width so a pathological session
	// name cannot ask tmux for a popup wider than any real client.
	swBannerMaxW = 60
)

var (
	swBannerNameStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	swBannerEscortStyle = lipgloss.NewStyle().Faint(true)
	swBannerSmokeStyle  = lipgloss.NewStyle().Faint(true)
)

// swBannerEscort is the text that trails the session name on the card's last
// line. The name's truncation budget is swBannerMaxW minus this suffix.
const swBannerEscort = " · escorted by claudemux"

// swBannerTrain is the fixed locomotive art, engine and tender, pulling in
// toward the left. The art no longer stretches around the session name — the
// name lives on the caption line below, so the train can afford real detail:
// a smoke plume, boiler domes, a cab, and a coupled tender.
var swBannerTrain = []string{
	`      o  O`,
	`     o`,
	`    o       _____`,
	`   .][__n_n_|DD[  ====____`,
	`  >(________|__|_[_______]|`,
	"  _/oo OOOOO oo`  ooo  ooo",
}

// renderBannerCard is the popup's content: the locomotive above, and a single
// caption line naming the session and who escorted it. Pure, so the popup
// geometry below can be derived from the same lines the banner subcommand
// will actually print. The train and caption are centered on each other, and
// the name is truncated so no line exceeds swBannerMaxW.
func renderBannerCard(session string) []string {
	name := ansi.Truncate(session, swBannerMaxW-lipgloss.Width(swBannerEscort), "…")
	captionW := lipgloss.Width(name) + lipgloss.Width(swBannerEscort)
	trainW := 0
	for _, l := range swBannerTrain {
		if w := lipgloss.Width(l); w > trainW {
			trainW = w
		}
	}
	trainPad, captionPad := 0, 0
	if captionW > trainW {
		trainPad = (captionW - trainW) / 2
	} else {
		captionPad = (trainW - captionW) / 2
	}
	lines := make([]string, 0, len(swBannerTrain)+1)
	for i, l := range swBannerTrain {
		if i < 3 {
			// The rising puffs read as motion, not structure.
			l = swBannerSmokeStyle.Render(l)
		}
		lines = append(lines, strings.Repeat(" ", trainPad)+l)
	}
	lines = append(lines, strings.Repeat(" ", captionPad)+
		swBannerNameStyle.Render(name)+swBannerEscortStyle.Render(swBannerEscort))
	return lines
}

// swWantsBanner: announce arrivals at sessions, not the return to the lobby —
// the lobby is self-identifying — and never for an empty target.
func swWantsBanner(target, lobby string) bool {
	return target != "" && target != lobby
}

// swShellQuote single-quotes s for the shell that display-popup -E hands its
// command to, closing and reopening the quotes around any embedded '.
func swShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// swBannerPopupArgs builds the tmux argv that shows the arrival banner on the
// driven client, sized to exactly fit the card plus the popup border.
func swBannerPopupArgs(self, client, session string) []string {
	lines := renderBannerCard(session)
	w := 0
	for _, l := range lines {
		if lw := lipgloss.Width(l); lw > w {
			w = lw
		}
	}
	return []string{
		"display-popup", "-c", client,
		"-w", strconv.Itoa(w + 2), "-h", strconv.Itoa(len(lines) + 2),
		"-E", swShellQuote(self) + " banner " + swShellQuote(session),
	}
}

// runBanner is the `claudemux-head banner <session>` entry point, run inside
// the popup's pty.
func runBanner(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: claudemux-head banner <session>")
		return 2
	}
	for _, l := range renderBannerCard(args[0]) {
		fmt.Fprintln(stdout, l)
	}
	waitBannerDismiss(os.Stdin, swBannerHold)
	return 0
}

// waitBannerDismiss holds the popup open until a keypress or the deadline.
// Raw mode so the key doesn't need Enter; if the tty refuses (not a terminal),
// fall back to a plain sleep rather than closing instantly.
func waitBannerDismiss(tty *os.File, d time.Duration) {
	st, err := term.MakeRaw(tty.Fd())
	if err != nil {
		time.Sleep(d)
		return
	}
	defer term.Restore(tty.Fd(), st)
	if tty.SetReadDeadline(time.Now().Add(d)) != nil {
		time.Sleep(d)
		return
	}
	buf := make([]byte, 1)
	_, _ = tty.Read(buf)
}
