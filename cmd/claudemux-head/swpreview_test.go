package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestPreviewTail(t *testing.T) {
	tests := []struct {
		name    string
		capture string
		n       int
		want    []string
	}{
		{"tail of a long capture", "a\nb\nc\nd\ne\n", 3, []string{"c", "d", "e"}},
		{"shorter than requested", "a\nb\n", 5, []string{"a", "b"}},
		{"exactly enough", "a\nb\n", 2, []string{"a", "b"}},
		// A claude pane sitting at its input box ends in blank rows; an
		// untrimmed tail would be mostly empty.
		{"trailing blanks trimmed", "a\nb\n\n   \n\n", 2, []string{"a", "b"}},
		// tmux emits colored blanks: a line that is nothing but an SGR reset
		// is still blank.
		{"ansi-only lines are blank", "a\nb\n\x1b[39m\n\x1b[0m   \n", 2, []string{"a", "b"}},
		{"blank interior lines kept", "a\n\nb\n", 3, []string{"a", "", "b"}},
		{"all blank", "\n\n   \n", 3, nil},
		{"empty", "", 3, nil},
		{"zero lines requested", "a\nb\n", 0, nil},
		{"negative lines requested", "a\nb\n", -1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := previewTail(tt.capture, tt.n)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("previewTail(%q, %d) = %q, want %q", tt.capture, tt.n, got, tt.want)
			}
		})
	}
}

func TestRenderPreviewGeometry(t *testing.T) {
	box := renderPreview("cd-receiver", []string{"one", "two"}, 40, 5)
	if len(box) != 7 {
		t.Fatalf("box has %d lines, want 7 (5 content + 2 border):\n%s", len(box), strings.Join(box, "\n"))
	}
	for i, line := range box {
		if w := lipgloss.Width(line); w != 40 {
			t.Errorf("line %d is %d cells, want 40: %q", i, w, ansi.Strip(line))
		}
	}
	if !strings.Contains(ansi.Strip(box[0]), "cd-receiver") {
		t.Errorf("top border must carry the title: %q", ansi.Strip(box[0]))
	}
	// Fewer lines than the box is tall: the remainder is blank, not missing.
	// Check that unused content rows exist and contain only padding (no actual text).
	stripped := ansi.Strip(box[4])
	if !strings.HasPrefix(stripped, "│") || !strings.HasSuffix(stripped, "│") {
		t.Errorf("unused row must have borders: %q", stripped)
	}
	// After removing "│ " and " │", the remainder should be empty (only spaces).
	content := strings.TrimPrefix(strings.TrimSuffix(stripped, " │"), "│ ")
	if strings.TrimSpace(content) != "" {
		t.Errorf("unused row has content between borders: %q", content)
	}
}

// A colored capture must not overflow the box or leak its color past the
// border — the two ways an embedded ANSI line breaks a TUI.
func TestRenderPreviewClipsAndResetsAnsi(t *testing.T) {
	long := "\x1b[38;5;114m" + strings.Repeat("wide ", 40)
	box := renderPreview("api", []string{long}, 30, 1)
	if len(box) != 3 {
		t.Fatalf("box has %d lines, want 3", len(box))
	}
	if w := lipgloss.Width(box[1]); w != 30 {
		t.Errorf("content row is %d cells, want 30", w)
	}
	if !strings.Contains(box[1], "\x1b[0m") {
		t.Errorf("content row must be reset-terminated so color cannot bleed: %q", box[1])
	}
}

// An over-long title cannot be allowed to push the border out.
func TestRenderPreviewTruncatesTitle(t *testing.T) {
	box := renderPreview(strings.Repeat("x", 100), nil, 20, 1)
	if w := lipgloss.Width(box[0]); w != 20 {
		t.Errorf("top border is %d cells, want 20: %q", w, ansi.Strip(box[0]))
	}
}

// CJK titles must be truncated by display width, not rune count. 囲 is 1 rune
// but 2 cells wide; truncating by rune count (the incorrect way) would clip
// to 15 runes (30 cells) and overflow the 20-cell box. ansi.Truncate clips
// by display width and keeps the border width-correct.
func TestRenderPreviewCJKTitleWidth(t *testing.T) {
	box := renderPreview(strings.Repeat("囲", 40), nil, 20, 1)
	if w := lipgloss.Width(box[0]); w != 20 {
		t.Errorf("top border with CJK title is %d cells, want 20: %q", w, ansi.Strip(box[0]))
	}
}

func TestRenderPreviewRefusesTinyBoxes(t *testing.T) {
	if got := renderPreview("api", nil, 4, 3); got != nil {
		t.Errorf("width 4 must render nothing, got %q", got)
	}
	if got := renderPreview("api", nil, 40, 0); got != nil {
		t.Errorf("height 0 must render nothing, got %q", got)
	}
}

func TestComputePreviewLayout(t *testing.T) {
	// listWant 99 throughout the "big fleet" cases: a fleet larger than any
	// list budget here, so the preview never grows past its claimed share.
	tests := []struct {
		name     string
		height   int
		hasErr   bool
		listWant int
		want     swLayout
	}{
		// avail 40 -> 40/3 = 13 content, list 40-15 = 25.
		{"a full-screen lobby", 46, false, 99, swLayout{Show: true, Content: 13, ListRows: 25}},
		// avail 18 -> 6 content (floor), list 18-8 = 10.
		{"a small lobby hits the floor", 24, false, 99, swLayout{Show: true, Content: 6, ListRows: 10}},
		// avail 94 -> 31 clamped to 16, list 94-18 = 76.
		{"a tall lobby hits the ceiling", 100, false, 99, swLayout{Show: true, Content: 16, ListRows: 76}},
		// avail 11 -> 6 content, list 3: the last height that fits both
		// (list=3 -> budget 2 -> one 2-row session + the more line).
		{"the smallest lobby with a preview", 17, false, 99, swLayout{Show: true, Content: 6, ListRows: 3}},
		// avail 10 -> list would be 2, one short of a full session row
		// surviving the cap: fleet wins, preview is dropped.
		{"one row short: fleet wins", 16, false, 99, swLayout{}},
		// avail 9 -> list would be 1: fleet wins, preview is dropped.
		{"too short for both", 15, false, 99, swLayout{}},
		{"no size yet", 0, false, 99, swLayout{}},
		// An error line costs a row: avail 39 -> 13 content, list 39-15 = 24.
		{"an error line shifts the budget", 46, true, 99, swLayout{Show: true, Content: 13, ListRows: 24}},
		// A small fleet returns its unused rows to the preview: avail 40,
		// claim 13, list share 25, fleet only needs 4 -> content 13+21 = 34.
		{"a small fleet grows the preview", 46, false, 4, swLayout{Show: true, Content: 34, ListRows: 4}},
		// Growth ignores the 16-row ceiling — the ceiling caps the claim, not
		// rows the fleet was never going to fill: avail 94, claim 16, list
		// share 76, fleet needs 4 -> content 16+72 = 88.
		{"growth is not capped at the ceiling", 100, false, 4, swLayout{Show: true, Content: 88, ListRows: 4}},
		// A fleet exactly filling its share changes nothing.
		{"an exactly-fitting fleet", 46, false, 25, swLayout{Show: true, Content: 13, ListRows: 25}},
		// avail 10 -> list share 2: too small for a truncated fleet (see "one
		// row short" above), but a fleet that fits WHOLE in 2 rows needs no
		// truncation floor, so both render.
		{"a tiny fleet fits where a big one would not", 16, false, 2, swLayout{Show: true, Content: 6, ListRows: 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computePreviewLayout(tt.height, tt.hasErr, tt.listWant); got != tt.want {
				t.Errorf("computePreviewLayout(%d, %v, %d) = %+v, want %+v", tt.height, tt.hasErr, tt.listWant, got, tt.want)
			}
		})
	}
}
