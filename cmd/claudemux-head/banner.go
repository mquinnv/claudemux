package main

import (
	"flag"
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
	swBannerEscortStyle = lipgloss.NewStyle().Faint(true)
	swBannerSmokeStyle  = lipgloss.NewStyle().Faint(true)
	swBannerStatStyle   = lipgloss.NewStyle().Faint(true)
)

// swBannerEscort is the text that trails the session name on the card's name
// line. The name's truncation budget is swBannerMaxW minus this suffix.
const swBannerEscort = " · escorted by claudemux"

// swBannerGutter marks the topic line. The bar carries the project color and
// the topic beside it is the only bold text on the card, so the one fact the
// arriving user most needs — what this session is working on — is what the eye
// lands on first.
const swBannerGutter = "▌ "

// swBannerDefaultAccent is the tint used when the project declares no color:
// the blue the card's name line wore before project colors reached it.
const swBannerDefaultAccent = "39"

// bannerCard is everything the arrival popup says about the session it landed
// on. The lobby already polls every field (see swSession) and the popup is a
// separate process with no tmux of its own, so the facts ride in on argv rather
// than being looked up again inside the popup's two-second life.
type bannerCard struct {
	Session string
	Topic   string // what the session is working on; "" when it has published none
	Color   string // bare 6-digit project hex; "" when the project declares none
	Model   string // raw model id; "" when unpublished
	Context int    // context used, in percent; -1 when unknown
}

// swBannerTrain is the fixed locomotive art, engine and tender, pulling in
// toward the left. The art no longer stretches around the session name — the
// name lives on the caption lines below, so the train can afford real detail:
// a smoke plume, boiler domes, a cab, and a coupled tender.
var swBannerTrain = []string{
	`      o  O`,
	`     o`,
	`    o       _____`,
	`   .][__n_n_|DD[  ====____`,
	`  >(________|__|_[_______]|`,
	"  _/oo OOOOO oo`  ooo  ooo",
}

// swBannerAccent is the card's tint: the session's project color, so an arrival
// is recognizable as "the purple project" before any of the text is read.
//
// The hex arrives from a tmux user option and so can hold anything at all. Only
// a bare 6-digit hex is honored — everything else falls back to the default
// blue, because handing junk to lipgloss.Color emits a broken escape sequence
// into the popup. Same rule as swNameStyle applies to the fleet list.
func swBannerAccent(hex string) lipgloss.Color {
	if !isHex6(hex) {
		return lipgloss.Color(swBannerDefaultAccent)
	}
	return lipgloss.Color("#" + hex)
}

// bannerStats is the faint line under the name: which model is driving, and how
// full its context already is. Each fact is dropped when unpublished rather than
// rendered as a placeholder — this card is glanced at, not read, and an older
// head that publishes neither simply gets no stats line at all.
func bannerStats(c bannerCard) string {
	var parts []string
	if c.Model != "" {
		parts = append(parts, shortModel(c.Model))
	}
	if c.Context >= 0 {
		parts = append(parts, fmt.Sprintf("%d%% context", c.Context))
	}
	return strings.Join(parts, " · ")
}

// renderBannerCard is the popup's content: the locomotive above, and a caption
// block naming what the session is doing, what it is called, and what is driving
// it. Pure, so the popup geometry below can be derived from the same lines the
// banner subcommand will actually print. The train and the caption block are
// centered on each other, the caption lines are left-aligned within the block,
// and every line is truncated so none exceeds swBannerMaxW.
func renderBannerCard(c bannerCard) []string {
	accent := swBannerAccent(c.Color)
	gutterStyle := lipgloss.NewStyle().Foreground(accent)
	topicStyle := lipgloss.NewStyle().Bold(true).Foreground(accent)
	nameStyle := lipgloss.NewStyle().Foreground(accent)

	var caption []string
	if topic := strings.TrimSpace(c.Topic); topic != "" {
		topic = ansi.Truncate(topic, swBannerMaxW-lipgloss.Width(swBannerGutter), "…")
		caption = append(caption, gutterStyle.Render(swBannerGutter)+topicStyle.Render(topic))
	}
	name := ansi.Truncate(c.Session, swBannerMaxW-lipgloss.Width(swBannerEscort), "…")
	caption = append(caption, nameStyle.Render(name)+swBannerEscortStyle.Render(swBannerEscort))
	if stats := bannerStats(c); stats != "" {
		caption = append(caption, swBannerStatStyle.Render(ansi.Truncate(stats, swBannerMaxW, "…")))
	}

	// Widths ignore the styling: lipgloss.Width measures cells, not bytes, so a
	// rendered line and its plain text measure the same.
	trainW, captionW := 0, 0
	for _, l := range swBannerTrain {
		if w := lipgloss.Width(l); w > trainW {
			trainW = w
		}
	}
	for _, l := range caption {
		if w := lipgloss.Width(l); w > captionW {
			captionW = w
		}
	}
	trainPad, captionPad := 0, 0
	if captionW > trainW {
		trainPad = (captionW - trainW) / 2
	} else {
		captionPad = (trainW - captionW) / 2
	}

	lines := make([]string, 0, len(swBannerTrain)+len(caption))
	for i, l := range swBannerTrain {
		if i < 3 {
			// The rising puffs read as motion, not structure.
			l = swBannerSmokeStyle.Render(l)
		}
		lines = append(lines, strings.Repeat(" ", trainPad)+l)
	}
	for _, l := range caption {
		lines = append(lines, strings.Repeat(" ", captionPad)+l)
	}
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

// swBannerCommand is the shell command line the popup runs: this binary's
// banner subcommand, carrying the card's facts as flags.
//
// Flags precede the session name because Go's flag package stops parsing at the
// first non-flag argument. Empty fields are omitted entirely, which keeps the
// command legible in `ps` and lets the subcommand's own defaults stand.
func swBannerCommand(self string, c bannerCard) string {
	parts := []string{swShellQuote(self), "banner"}
	for _, f := range []struct{ name, value string }{
		{"topic", c.Topic},
		{"color", c.Color},
		{"model", c.Model},
	} {
		if f.value != "" {
			parts = append(parts, "--"+f.name, swShellQuote(f.value))
		}
	}
	if c.Context >= 0 {
		parts = append(parts, "--context", strconv.Itoa(c.Context))
	}
	return strings.Join(append(parts, swShellQuote(c.Session)), " ")
}

// swBannerPopupArgs builds the tmux argv that shows the arrival banner on the
// driven client, sized to exactly fit the card plus the popup border.
func swBannerPopupArgs(self, client string, c bannerCard) []string {
	lines := renderBannerCard(c)
	w := 0
	for _, l := range lines {
		if lw := lipgloss.Width(l); lw > w {
			w = lw
		}
	}
	return []string{
		"display-popup", "-c", client,
		"-w", strconv.Itoa(w + 2), "-h", strconv.Itoa(len(lines) + 2),
		"-E", swBannerCommand(self, c),
	}
}

// parseBannerArgs reads the banner subcommand's argv. Every flag is optional: a
// lobby running an older binary invokes this with the session name alone, and
// that still renders — one caption line instead of three.
func parseBannerArgs(args []string, stderr io.Writer) (bannerCard, bool) {
	fs := flag.NewFlagSet("banner", flag.ContinueOnError)
	fs.SetOutput(stderr)
	topic := fs.String("topic", "", "what the session is working on")
	color := fs.String("color", "", "project color, as a bare 6-digit hex")
	model := fs.String("model", "", "raw model id, e.g. claude-opus-4-7")
	contextPct := fs.Int("context", -1, "context used, in percent; -1 when unknown")
	if err := fs.Parse(args); err != nil {
		return bannerCard{}, false
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: claudemux-head banner [--topic T] [--color HEX] [--model ID] [--context N] <session>")
		return bannerCard{}, false
	}
	return bannerCard{
		Session: fs.Arg(0),
		Topic:   *topic,
		Color:   *color,
		Model:   *model,
		Context: *contextPct,
	}, true
}

// runBanner is the `claudemux-head banner [flags] <session>` entry point, run
// inside the popup's pty.
func runBanner(args []string, stdout, stderr io.Writer) int {
	card, ok := parseBannerArgs(args, stderr)
	if !ok {
		return 2
	}
	// The popup's inner viewport is exactly len(lines) rows
	// (swBannerPopupArgs asks for len+2, borders included), so the card is
	// written as newline-JOINED lines: a final newline would move the
	// cursor to a row that doesn't fit, scrolling the card up and leaving
	// a blank bottom line. The cursor itself is hidden for the popup's
	// lifetime — there is nothing to type here — and restored on the way
	// out for pty hygiene, though the popup dies with this process anyway.
	fmt.Fprint(stdout, "\x1b[?25l")
	fmt.Fprint(stdout, strings.Join(renderBannerCard(card), "\n"))
	waitBannerDismiss(os.Stdin, swBannerHold)
	fmt.Fprint(stdout, "\x1b[?25h")
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
