package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestRenderBannerCardShowsSessionAndEscortLine(t *testing.T) {
	lines := renderBannerCard("remix-2")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "remix-2") {
		t.Errorf("line 1 must name the session, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "escorted by claudemux") {
		t.Errorf("line 2 must say who moved the client, got %q", lines[1])
	}
}

func TestRenderBannerCardTruncatesLongNames(t *testing.T) {
	lines := renderBannerCard(strings.Repeat("x", 200))
	for i, l := range lines {
		if w := lipgloss.Width(l); w > swBannerMaxW {
			t.Errorf("line %d is %d cells, exceeds cap %d", i, w, swBannerMaxW)
		}
	}
}

func TestSwWantsBanner(t *testing.T) {
	cases := []struct {
		target, lobby string
		want          bool
	}{
		{"remix-2", "claudemux-switch", true},
		{"claudemux-switch", "claudemux-switch", false}, // returning home needs no announcement
		{"", "claudemux-switch", false},
	}
	for _, c := range cases {
		if got := swWantsBanner(c.target, c.lobby); got != c.want {
			t.Errorf("swWantsBanner(%q, %q) = %v, want %v", c.target, c.lobby, got, c.want)
		}
	}
}

func TestSwBannerPopupArgsTargetsClientAndQuotesCommand(t *testing.T) {
	args := swBannerPopupArgs("/usr/local/bin/claudemux-head", "client-0", "remix-2")
	if args[0] != "display-popup" {
		t.Fatalf("want display-popup argv, got %q", args)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-c client-0") {
		t.Errorf("popup must target the driven client, got %q", joined)
	}
	shellCmd := args[len(args)-1]
	if want := "'/usr/local/bin/claudemux-head' banner 'remix-2'"; shellCmd != want {
		t.Errorf("shell command = %q, want %q", shellCmd, want)
	}
}

func TestSwBannerPopupArgsSizesToCard(t *testing.T) {
	args := swBannerPopupArgs("/bin/self", "c", "remix-2")
	var w, h string
	for i, a := range args {
		if a == "-w" && i+1 < len(args) {
			w = args[i+1]
		}
		if a == "-h" && i+1 < len(args) {
			h = args[i+1]
		}
	}
	// 2 content lines + 2 border rows.
	if h != "4" {
		t.Errorf("popup height = %q, want 4", h)
	}
	lines := renderBannerCard("remix-2")
	widest := 0
	for _, l := range lines {
		if lw := lipgloss.Width(l); lw > widest {
			widest = lw
		}
	}
	if want := strconv.Itoa(widest + 2); w != want {
		t.Errorf("popup width = %q, want card+border = %q", w, want)
	}
}

func TestSwShellQuoteEscapesSingleQuotes(t *testing.T) {
	if got, want := swShellQuote("it's"), `'it'\''s'`; got != want {
		t.Errorf("swShellQuote = %q, want %q", got, want)
	}
}
