package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestHeadQuestionMarkShowsScrollableCheatsheet(t *testing.T) {
	m := model{ready: true, width: 80, height: 4}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = updated.(model)
	if cmd != nil {
		t.Fatal("opening help invoked an action")
	}
	first := ansi.Strip(m.View())
	if !strings.Contains(first, "Keyboard shortcuts") || !strings.Contains(first, "Reload head") {
		t.Fatalf("missing help: %q", first)
	}
	for i := 0; i < 30; i++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(model)
	}
	if last := ansi.Strip(m.View()); last == first || !strings.Contains(last, "Quit head") {
		t.Fatalf("cannot scroll to remaining shortcuts: %q", last)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if lipgloss.Width(line) > m.width {
			t.Fatalf("help overflows: %q", line)
		}
	}
	if len(strings.Split(m.View(), "\n")) > m.height {
		t.Fatal("help exceeds pane height")
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(model)
	if cmd != nil || strings.Contains(ansi.Strip(m.View()), "Keyboard shortcuts") {
		t.Fatal("escape did not close help safely")
	}
}

func TestHelpSwallowsSessionActions(t *testing.T) {
	m := model{ready: true, width: 80, height: 4, selfPane: "%1"}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = updated.(model)
	for _, key := range []rune{'x', 'X', 'c', 'r', 's', 'd', 't', 'p'} {
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		m = updated.(model)
		if cmd != nil || m.claudeRestartArmed || m.tabPinned || m.deferPrompting || m.teardown != teardownIdle {
			t.Fatalf("help dispatched %q", key)
		}
	}
}
