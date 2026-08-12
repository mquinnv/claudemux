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
