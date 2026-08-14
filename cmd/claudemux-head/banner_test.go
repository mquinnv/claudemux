package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestRenderBannerCardShowsSessionAndEscortLine(t *testing.T) {
	lines := renderBannerCard("remix-2")
	if len(lines) != 5 {
		t.Fatalf("want 5 lines (smoke, roof, body, wheels, escort), got %d: %q", len(lines), lines)
	}
	if !strings.Contains(lines[2], "remix-2") {
		t.Errorf("body line must name the session, got %q", lines[2])
	}
	if !strings.Contains(lines[4], "escorted by claudemux") {
		t.Errorf("last line must say who moved the client, got %q", lines[4])
	}
}

// The train's art lines share a frame: the roof, body, and wheels must stay
// aligned as the car stretches to fit the name, and a name shorter than the
// engine's fixed parts must not collapse the wheels.
func TestRenderBannerCardTrainStaysAligned(t *testing.T) {
	for _, session := range []string{"a", "remix-2", strings.Repeat("x", 200)} {
		lines := renderBannerCard(session)
		body := lipgloss.Width(lines[2])
		if roof := lipgloss.Width(lines[1]); roof != body-2 {
			t.Errorf("%q: roof width %d, want body-2 = %d", session, roof, body-2)
		}
		if wheels := lipgloss.Width(lines[3]); wheels != body-2 {
			t.Errorf("%q: wheels width %d, want body-2 = %d", session, wheels, body-2)
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
	// 5 content lines + 2 border rows.
	if h != "7" {
		t.Errorf("popup height = %q, want 7", h)
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
