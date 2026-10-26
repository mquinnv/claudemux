package main

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"strings"
)

var headShortcuts = []string{
	"R Reload head", "? Help", "s Refresh summary", "r Pin/unpin tab",
	"Space Conduct/standby", "d Defer", "D Edit blocker", "t Open shell", "p Parent session",
	"c Restart Claude: c resume / n fresh / Esc cancel",
	"x Wrap up; x again when ready closes session", "X Direct close (press twice)",
	"Esc Cancel armed close / quit head", "q/Ctrl+C Quit head (closes pane)",
}

var switchboardShortcuts = []string{
	"j/k/↑↓ Select", "Enter Jump to session", "Esc Back to last session", "Space Conduct/standby",
	"p Toggle preview", "n New session", "d Defer", "D Edit blocker",
	"R Reload switchboard", "Ctrl+R Reload all heads", "? Help", "q/Ctrl+C Quit switchboard",
	"Restore offer: r restore all / s select / x dismiss",
	"Restore picker: Space toggle / Enter restore / Esc back",
}

// Pack complete shortcut entries across the available width before adding
// rows. A wide four-row head can show every shortcut without scrolling.
func shortcutLines(entries []string, width int) []string {
	avail := max(1, width-2)
	lines := []string{}
	line := ""
	for _, entry := range entries {
		entry = clipLine(entry, avail)
		next := entry
		if line != "" {
			next = line + " · " + entry
		}
		if line != "" && lipgloss.Width(next) > avail {
			lines = append(lines, line)
			line = entry
		} else {
			line = next
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

func helpBodyRows(height int) int { return max(1, height-1) }

func shortcutOffset(key string, offset, total, rows int) int {
	switch key {
	case "down", "j":
		offset++
	case "up", "k":
		offset--
	case "pgdown", " ":
		offset += rows
	case "pgup":
		offset -= rows
	case "home", "g":
		offset = 0
	case "end", "G":
		offset = total
	}
	return max(0, min(offset, max(0, total-rows)))
}

func (m model) headHelpKey(msg tea.KeyMsg) (model, tea.Cmd) {
	switch msg.String() {
	case "?", "esc", "q":
		m.helpVisible = false
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	}
	m.helpOffset = shortcutOffset(msg.String(), m.helpOffset, len(shortcutLines(headShortcuts, m.width)), helpBodyRows(m.height))
	return m, nil
}

func (m swModel) switchboardHelpKey(msg tea.KeyMsg) (swModel, tea.Cmd) {
	switch msg.String() {
	case "?", "esc", "q":
		m.helpVisible = false
		return m, nil
	case "ctrl+c":
		return m, tea.Quit
	}
	m.helpOffset = shortcutOffset(msg.String(), m.helpOffset, len(shortcutLines(switchboardShortcuts, m.width)), helpBodyRows(m.height))
	return m, nil
}

func renderHeadHelp(width, height, offset int) string {
	return renderShortcutHelp("Keyboard shortcuts", headShortcuts, width, height, offset)
}

func renderShortcutHelp(title string, entries []string, width, height, offset int) string {
	if height < 1 || width < 1 {
		return ""
	}
	body := shortcutLines(entries, width)
	rows := helpBodyRows(height)
	offset = max(0, min(offset, max(0, len(body)-rows)))
	end := min(offset+rows, len(body))
	lines := []string{}
	if height >= 2 {
		header := title + " · ?/Esc/q close"
		if len(body) > rows {
			header += fmt.Sprintf(" · ↑↓ more %d–%d/%d", offset+1, end, len(body))
		}
		lines = append(lines, promptEmphasisStyle.Render(header))
	}
	lines = append(lines, body[offset:end]...)
	for i, line := range lines {
		lines[i] = clipLine(" "+line, width)
	}
	return strings.Join(lines, "\n")
}
