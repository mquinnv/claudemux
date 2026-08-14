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

// renderBannerCard is the popup's content: a little locomotive pulling into
// the station with the session's name on its side. Pure, so the popup
// geometry below can be derived from the same lines the banner subcommand
// will actually print.
//
//	       . o O
//	 ______[]_
//	| remix-2 |>
//	 (o)---(o)
//	 escorted by claudemux
//
// The car stretches to fit the name; inner is the interior width between the
// body's side walls, floored so the wheels and stack of a one-letter session
// still have somewhere to sit.
func renderBannerCard(session string) []string {
	// Body width is inner+3 ("|" + inner + "|>"), so this cap keeps the
	// widest line at swBannerMaxW.
	name := ansi.Truncate(session, swBannerMaxW-5, "…")
	inner := lipgloss.Width(name) + 2
	if inner < 8 {
		inner = 8
	}
	pad := inner - 2 - lipgloss.Width(name)
	left, right := pad/2, pad-pad/2
	return []string{
		strings.Repeat(" ", inner-2) + swBannerSmokeStyle.Render(". o O"),
		" " + strings.Repeat("_", inner-3) + "[]_",
		"|" + strings.Repeat(" ", left+1) + swBannerNameStyle.Render(name) + strings.Repeat(" ", right+1) + "|>",
		" (o)" + strings.Repeat("-", inner-6) + "(o)",
		" " + swBannerEscortStyle.Render("escorted by claudemux"),
	}
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
