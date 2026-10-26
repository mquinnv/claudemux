package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

var headShortcuts = []string{
	"R         Reload head from installed binary",
	"?         Open / close this cheatsheet",
	"s         Refresh summary now",
	"r         Pin / unpin tab title",
	"Space     Toggle fleet conduct mode (lobby required)",
	"d / D     Toggle defer / edit blocker",
	"t         Open a shell beside Claude",
	"p         Switch to parent session",
	"c         Restart Claude: c resumes, n starts fresh; Esc cancels",
	"x         Arm wrap-up; press x again when ready to close session",
	"X         Arm direct session close; press X again to confirm",
	"Esc       Cancel armed close; otherwise quit head",
	"q/Ctrl+C  Quit head (closes this pane)",
}

func headHelpRows(height int) int {
	if height < 3 {
		return 1
	}
	return height - 2
}

func (m model) headHelpKey(msg tea.KeyMsg) (model, tea.Cmd) {
	rows := headHelpRows(m.height)
	switch msg.String() {
	case "?", "esc", "q":
		m.helpVisible = false
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	case "down", "j":
		m.helpOffset++
	case "up", "k":
		m.helpOffset--
	case "pgdown", " ":
		m.helpOffset += rows
	case "pgup":
		m.helpOffset -= rows
	case "home", "g":
		m.helpOffset = 0
	case "end", "G":
		m.helpOffset = len(headShortcuts)
	}
	m.helpOffset = max(0, min(m.helpOffset, max(0, len(headShortcuts)-rows)))
	return m, nil
}

func renderHeadHelp(width, height, offset int) string {
	if height < 1 || width < 1 {
		return ""
	}
	rows := headHelpRows(height)
	offset = max(0, min(offset, max(0, len(headShortcuts)-rows)))
	end := min(offset+rows, len(headShortcuts))
	lines := []string{}
	if height >= 3 {
		lines = append(lines, promptEmphasisStyle.Render("Keyboard shortcuts"))
	}
	lines = append(lines, headShortcuts[offset:end]...)
	if height >= 2 {
		lines = append(lines, fmt.Sprintf("?/Esc/q close · ↑↓ scroll · %d–%d/%d", offset+1, end, len(headShortcuts)))
	}
	for i, line := range lines {
		lines[i] = clipLine(" "+line, max(1, width))
	}
	return strings.Join(lines, "\n")
}
