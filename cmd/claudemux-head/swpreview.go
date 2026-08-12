package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The lobby's preview box: capturing the selected session's claude pane and
// drawing it below the fleet list. Everything here except swPreviewCmd is a
// pure function over strings, so the whole feature is testable without tmux.

// previewTail returns the last n lines of a captured pane, after trailing
// blank lines are dropped.
//
// The tail, not the head: a claude pane's newest turn and its input box are at
// the BOTTOM, and that is what tells you whether the session needs you. The
// trim matters because a pane parked at an idle input box ends in several
// blank rows — an untrimmed tail would spend most of the box on them. Blank is
// judged after stripping SGR, since tmux happily emits a line that is nothing
// but a color reset.
func previewTail(capture string, n int) []string {
	if n <= 0 {
		return nil
	}
	lines := strings.Split(strings.TrimRight(capture, "\n"), "\n")
	end := len(lines)
	for end > 0 && strings.TrimSpace(ansi.Strip(lines[end-1])) == "" {
		end--
	}
	if end == 0 {
		return nil
	}
	lines = lines[:end]
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// swPreviewBorderStyle dims the box frame so the captured pane, which brings
// its own colors, stays the loudest thing in it.
var swPreviewBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

// ansiReset closes any color a captured line left open. Without it a truncated
// mid-color line paints the border, and everything after it, in that color.
const ansiReset = "\x1b[0m"

// renderPreview draws the preview box: a titled top border, exactly height
// content rows, and a bottom border. Every returned line is exactly width
// display cells, which is what keeps the lobby's one-row-per-line invariant
// under a payload the lobby did not generate.
//
// Returns nil for a box too small to be worth drawing — the caller renders
// nothing rather than a frame with no room inside it.
func renderPreview(title string, lines []string, width, height int) []string {
	if width < 8 || height < 1 {
		return nil
	}
	inner := width - 4 // "│ " + content + " │"
	out := make([]string, 0, height+2)
	out = append(out, previewTopBorder(title, width))
	edge := swPreviewBorderStyle.Render("│")
	for i := 0; i < height; i++ {
		content := ""
		if i < len(lines) {
			content = ansi.Truncate(lines[i], inner, "…") + ansiReset
		}
		out = append(out, edge+" "+swPad(content, inner)+" "+edge)
	}
	out = append(out, swPreviewBorderStyle.Render("└"+strings.Repeat("─", width-2)+"┘"))
	return out
}

// previewTopBorder builds "┌─ title ──────┐", clipping the title to whatever
// the width allows and dropping it entirely when nothing fits.
func previewTopBorder(title string, width int) string {
	// "┌─ " + title + " " + fill + "┐": 5 cells of frame around the title.
	if room := width - 5; room < 1 {
		title = ""
	} else {
		title = ansi.Truncate(title, room, "…")
	}
	if title == "" {
		return swPreviewBorderStyle.Render("┌" + strings.Repeat("─", width-2) + "┐")
	}
	fill := width - 5 - lipgloss.Width(title)
	if fill < 0 {
		fill = 0
	}
	return swPreviewBorderStyle.Render("┌─ " + title + " " + strings.Repeat("─", fill) + "┐")
}
