package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestRenderBannerCardShowsSessionAndEscortLine(t *testing.T) {
	lines := renderBannerCard("remix-2")
	if want := len(swBannerTrain) + 1; len(lines) != want {
		t.Fatalf("want %d lines (train + caption), got %d: %q", want, len(lines), lines)
	}
	caption := lines[len(lines)-1]
	if !strings.Contains(caption, "remix-2") {
		t.Errorf("caption line must name the session, got %q", caption)
	}
	if !strings.Contains(caption, "escorted by claudemux") {
		t.Errorf("caption line must say who moved the client, got %q", caption)
	}
}

// The locomotive is fixed art: only its centering indent may vary with the
// session name, never the drawing itself.
func TestRenderBannerCardTrainIsFixedArt(t *testing.T) {
	base := renderBannerCard("remix-2")
	for _, session := range []string{"a", strings.Repeat("x", 200)} {
		lines := renderBannerCard(session)
		for i := range swBannerTrain {
			if strings.TrimLeft(lines[i], " ") != strings.TrimLeft(base[i], " ") {
				t.Errorf("%q: train line %d changed with the name: %q vs %q",
					session, i, lines[i], base[i])
			}
		}
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
	lines := renderBannerCard("remix-2")
	// Content lines + 2 border rows.
	if want := strconv.Itoa(len(lines) + 2); h != want {
		t.Errorf("popup height = %q, want content+border = %q", h, want)
	}
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
