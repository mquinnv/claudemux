package main

import (
	"strings"

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
