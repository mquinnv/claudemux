package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func swTestModel() swModel {
	m := newSwModel("%9")
	m.width, m.height = 80, 24
	m.snap = swSnapshot{
		Sessions: []swSession{
			{Name: "api", State: "Idle", Since: time.Now().Add(-2 * time.Minute)},
			{Name: "web", State: "Thinking", Since: time.Now()},
			{Name: "scratch"},
		},
		Lobby:   "switchboard",
		Clients: map[string]string{"/dev/ttys001": "switchboard"},
	}
	m.cond.client = "/dev/ttys001"
	return m
}

func TestSwModelSelectionKeys(t *testing.T) {
	m := swTestModel()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = next.(swModel)
	if m.sel != 1 {
		t.Errorf("sel = %d, want 1", m.sel)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = next.(swModel)
	if m.sel != 0 {
		t.Errorf("sel = %d, want 0", m.sel)
	}
	// k at the top and j past the end clamp.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if next.(swModel).sel != 0 {
		t.Error("k at top must clamp")
	}
}

func TestSwModelEnterSwitchesToSelection(t *testing.T) {
	m := swTestModel()
	m.sel = 1
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Error("enter with a client must produce a switch cmd")
	}
	m.cond.client = ""
	m.snap.Clients = map[string]string{}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("enter without a client must be a no-op")
	}
}

func TestSwModelQuits(t *testing.T) {
	m := swTestModel()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("q must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q must produce tea.Quit")
	}
}

func TestSwModelSnapshotClampsSelection(t *testing.T) {
	m := swTestModel()
	m.sel = 2
	next, _ := m.Update(swSnapshotMsg{snap: swSnapshot{
		Sessions: []swSession{{Name: "api", State: "Idle"}},
		Lobby:    "switchboard",
		Clients:  map[string]string{"/dev/ttys001": "switchboard"},
	}})
	if next.(swModel).sel != 0 {
		t.Errorf("sel = %d, want clamped to 0", next.(swModel).sel)
	}
}

func TestSwModelViewShowsFleet(t *testing.T) {
	m := swTestModel()
	view := m.View()
	for _, want := range []string{"api", "web", "scratch", "waiting"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestSwModelViewEmptyFleet(t *testing.T) {
	m := newSwModel("%9")
	m.width, m.height = 80, 24
	if v := m.View(); !strings.Contains(v, "no claudemux sessions") {
		t.Errorf("empty fleet needs a hint, got:\n%s", v)
	}
}

func TestSwModelSpaceTogglesStandby(t *testing.T) {
	m := swTestModel()
	m.cond.phase = swEscorting
	m.cond.escortee = "api"
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	m = next.(swModel)
	if !m.standby {
		t.Fatal("space must enter standby")
	}
	if m.cond.escortee != "" || m.cond.phase != swPaused {
		t.Errorf("standby must neutralize the conductor, got phase=%v escortee=%q", m.cond.phase, m.cond.escortee)
	}
	if len(m.cond.snoozed) != 0 {
		t.Error("entering standby must not snooze the escortee")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if next.(swModel).standby {
		t.Error("space again must leave standby")
	}
}

func TestSwModelStandbyNeverDispatches(t *testing.T) {
	m := swTestModel()
	m.standby = true
	m.cond.phase = swPaused
	snap := swSnapshot{
		Sessions: []swSession{{Name: "api", State: "Idle", Since: time.Unix(100, 0)}},
		Lobby:    "switchboard",
		Clients:  map[string]string{"/dev/ttys001": "switchboard"},
	}
	// Two snapshots: an active conductor would go Paused->Parked on the
	// first and dispatch on the second. Standby must do neither.
	for i := 0; i < 2; i++ {
		next, _ := m.Update(swSnapshotMsg{snap: snap})
		m = next.(swModel)
	}
	if m.cond.phase != swPaused || m.cond.escortee != "" {
		t.Errorf("standby stepped the conductor: phase=%v escortee=%q", m.cond.phase, m.cond.escortee)
	}
}

func TestSwModelStandbyStatusLine(t *testing.T) {
	m := swTestModel()
	m.standby = true
	if v := m.View(); !strings.Contains(v, "standby") {
		t.Errorf("view must show standby state:\n%s", v)
	}
}
