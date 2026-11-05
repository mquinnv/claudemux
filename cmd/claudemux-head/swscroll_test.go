package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestSwListWindow(t *testing.T) {
	ones := []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
	tests := []struct {
		name       string
		rows       []int
		budget     int
		sel, prev  int
		start, end int
	}{
		{"selection inside the window does not scroll", ones, 4, 2, 0, 0, 4},
		{"moving past the bottom scrolls by one", ones, 4, 4, 0, 1, 5},
		{"moving back up inside the window holds", ones, 4, 2, 1, 1, 5},
		{"moving past the top scrolls up", ones, 4, 0, 1, 0, 4},
		{"the last row pins the window to the end", ones, 4, 9, 0, 6, 10},
		{"leftover room at the end is filled from above", ones, 4, 9, 8, 6, 10},
		{"mixed heights keep the selection whole", []int{2, 2, 2, 2}, 4, 2, 0, 1, 3},
		{"a selection taller than the budget still shows", []int{2, 2}, 1, 1, 0, 1, 2},
		{"a stale offset past the fleet is clamped to the selection", ones, 4, 3, 50, 3, 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := swListWindow(tt.rows, tt.budget, tt.sel, tt.prev)
			if start != tt.start || end != tt.end {
				t.Errorf("swListWindow(budget %d, sel %d, prev %d) = [%d,%d), want [%d,%d)",
					tt.budget, tt.sel, tt.prev, start, end, tt.start, tt.end)
			}
		})
	}
}

func swBigFleetModel(n int) swModel {
	m := swPreviewModel()
	m.height = 20
	var sessions []swSession
	for i := 0; i < n; i++ {
		sessions = append(sessions, swSession{
			Name: fmt.Sprintf("sess-%02d", i), State: "Idle", Context: -1, ClaudePane: "%2",
		})
	}
	m.snap.Sessions = sessions
	// Pinned: a fleet this size crowds the box, which would otherwise close on
	// its own. These tests are about scrolling beside the box.
	m.previewPinned = true
	return m
}

func swAssertFits(t *testing.T, m swModel, raw string) {
	t.Helper()
	if got := len(strings.Split(raw, "\n")); got > m.height {
		t.Errorf("view is %d lines, want at most %d:\n%s", got, m.height, ansi.Strip(raw))
	}
}

func swPress(model tea.Model, r rune) (tea.Model, tea.Cmd) {
	return model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
}

// Walking the cursor to the bottom of a fleet longer than the list must scroll
// the list so the selected row is always on screen, and say what is above it.
func TestSwModelScrollsToFollowSelection(t *testing.T) {
	var model tea.Model = swBigFleetModel(30)
	for i := 0; i < 29; i++ {
		model, _ = swPress(model, 'j')
		raw := model.(swModel).View()
		want := fmt.Sprintf("sess-%02d", i+1)
		if !strings.Contains(ansi.Strip(raw), want) {
			t.Fatalf("after %d presses the selected %s is off screen:\n%s", i+1, want, ansi.Strip(raw))
		}
		swAssertFits(t, model.(swModel), raw)
	}
	view := ansi.Strip(model.(swModel).View())
	if strings.Contains(view, "sess-00") || !strings.Contains(view, "↑") {
		t.Errorf("a list scrolled to the end must hide the top and say so:\n%s", view)
	}
	if !strings.Contains(view, "┌") {
		t.Errorf("scrolling must not cost the preview box:\n%s", view)
	}
}

// Without the preview a fleet taller than the pane used to render uncapped and
// push the status and footer lines off the bottom.
func TestSwModelHiddenPreviewCapsAndScrolls(t *testing.T) {
	next, cmd := swPress(swBigFleetModel(30), 'p')
	m := next.(swModel)
	if !m.previewHidden || cmd != nil {
		t.Fatalf("p must hide the preview without requesting a capture (hidden=%v, cmd=%v)", m.previewHidden, cmd != nil)
	}
	raw := m.View()
	view := ansi.Strip(raw)
	if strings.Contains(view, "┌") {
		t.Errorf("a hidden preview must not draw its box:\n%s", view)
	}
	if !strings.Contains(view, "p preview") {
		t.Errorf("the footer must stay on screen:\n%s", view)
	}
	swAssertFits(t, m, raw)
	withBox := strings.Count(ansi.Strip(swBigFleetModel(30).View()), "sess-")
	if without := strings.Count(view, "sess-"); without <= withBox {
		t.Errorf("hiding the preview must give the list more rows (%d with box, %d without)", withBox, without)
	}

	var model tea.Model = m
	for i := 0; i < 29; i++ {
		model, _ = swPress(model, 'j')
	}
	raw = model.(swModel).View()
	if !strings.Contains(ansi.Strip(raw), "sess-29") {
		t.Errorf("the last session must be reachable with the preview hidden:\n%s", ansi.Strip(raw))
	}
	swAssertFits(t, model.(swModel), raw)

	next, cmd = swPress(model, 'p')
	if next.(swModel).previewHidden || cmd == nil {
		t.Error("p again must restore the preview and request a capture")
	}
}

// A preview capture must not run while the box is hidden.
func TestSwModelHiddenPreviewSkipsCaptures(t *testing.T) {
	m := swPreviewModel()
	m.previewHidden = true
	if _, cmd := swPress(m, 'j'); cmd != nil {
		t.Error("moving the selection with the preview hidden must not capture")
	}
}

// A fleet that no longer fits beside the box closes the box on its own: seeing
// every session matters more than seeing one session's screen.
func TestSwModelPreviewClosesWhenFleetOutgrowsIt(t *testing.T) {
	m := swBigFleetModel(30)
	m.previewPinned = false
	raw := m.View()
	view := ansi.Strip(raw)
	if strings.Contains(view, "┌") {
		t.Errorf("a box that crowds the fleet must close on its own:\n%s", view)
	}
	swAssertFits(t, m, raw)
	pinned := strings.Count(ansi.Strip(swBigFleetModel(30).View()), "sess-")
	if got := strings.Count(view, "sess-"); got <= pinned {
		t.Errorf("the closed box must give its rows to the list (%d with box, %d without)", pinned, got)
	}
	if _, cmd := swPress(m, 'j'); cmd != nil {
		t.Error("a box closed for crowding must not capture")
	}
}

// A fleet that fits beside the box leaves it open, exactly as before.
func TestSwModelPreviewStaysOpenWhenFleetFits(t *testing.T) {
	m := swBigFleetModel(3)
	m.previewPinned = false
	m.followSelection()
	if view := ansi.Strip(m.View()); !strings.Contains(view, "┌") {
		t.Errorf("a fleet that fits must keep the box:\n%s", view)
	}
}

// p reopens a box that closed itself, and it stays open (pinned) while the
// fleet still overflows; p again hides it as usual.
func TestSwModelPReopensAutoClosedPreview(t *testing.T) {
	m := swBigFleetModel(30)
	m.previewPinned = false
	next, cmd := swPress(m, 'p')
	m = next.(swModel)
	if !m.previewPinned || m.previewHidden || cmd == nil {
		t.Fatalf("p on an auto-closed box must pin it open and capture (pinned=%v hidden=%v cmd=%v)",
			m.previewPinned, m.previewHidden, cmd != nil)
	}
	next, _ = swPress(m, 'j')
	m = next.(swModel)
	if view := ansi.Strip(m.View()); !strings.Contains(view, "┌") {
		t.Errorf("a pinned box must survive moving the selection:\n%s", view)
	}
	next, _ = swPress(m, 'p')
	m = next.(swModel)
	if !m.previewHidden || m.previewPinned {
		t.Errorf("p on an open box must hide it and drop the pin (hidden=%v pinned=%v)", m.previewHidden, m.previewPinned)
	}
}

// The pin lasts only while the fleet overflows: once it fits again the pin is
// dropped, so the next time the fleet outgrows the box it closes on its own.
func TestSwModelPinClearsWhenFleetFitsAgain(t *testing.T) {
	m := swBigFleetModel(30)
	m.snap.Sessions = m.snap.Sessions[:3]
	m.sel = 0
	m.followSelection()
	if m.previewPinned {
		t.Fatal("the pin must clear once the fleet fits beside the box")
	}
	m.snap.Sessions = swBigFleetModel(30).snap.Sessions
	m.followSelection()
	if view := ansi.Strip(m.View()); strings.Contains(view, "┌") {
		t.Errorf("after the pin cleared, an outgrown box must close again:\n%s", view)
	}
}
