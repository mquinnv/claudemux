package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func swTestModel() swModel {
	m := newSwModel("%9")
	m.width, m.height = 80, 24
	m.snap = swSnapshot{
		Sessions: []swSession{
			{Name: "api", State: "Idle", Since: time.Now().Add(-2 * time.Minute),
				Context: 37, Topic: "build fixes", Summary: "fixing the build", Prompt: "run the tests"},
			{Name: "web", State: "Thinking", Since: time.Now(), Context: -1},
			{Name: "scratch", Context: -1},
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

func TestSwModelViewShowsInfo(t *testing.T) {
	m := swTestModel()
	view := m.View()
	for _, want := range []string{"37%", "build fixes", "fixing the build"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestSwModelViewPromptFallback(t *testing.T) {
	m := swTestModel()
	m.snap.Sessions[0].Summary = ""
	m.snap.Sessions[0].Prompt = "run the tests"
	if view := m.View(); !strings.Contains(view, "run the tests") {
		t.Errorf("prompt fallback missing:\n%s", view)
	}
}

func TestSwModelViewOmitsUnsetContext(t *testing.T) {
	m := swTestModel()
	if view := m.View(); strings.Contains(view, "-1%") {
		t.Errorf("unset context must be omitted, not rendered:\n%s", view)
	}
}

// Every line-1 field sits in a fixed-width column, so the context meters (and
// the topics after them) stack instead of drifting with each row's name,
// state, and age. Asserting on the topic's start column catches drift
// anywhere to its left; asserting the meter cell's own span catches a bar or
// a percentage that grew past its column.
func TestSwModelViewAlignsColumns(t *testing.T) {
	now := time.Now()
	m := newSwModel("%9")
	m.width, m.height = 100, 24
	m.snap = swSnapshot{Sessions: []swSession{
		{Name: "api", State: "Idle", Since: now.Add(-2 * time.Minute), Context: 7, Topic: "one-digit ctx"},
		{Name: "a-very-long-session-name-that-overflows", State: "Tool:AskUserQuestion",
			Since: now.Add(-3 * time.Hour), Context: 100, Topic: "long name, long state"},
		{Name: "web", State: "Thinking", Since: now, Context: -1, Topic: "no context yet"},
		{Name: "scratch", Context: -1, Topic: "no state, no age"},
	}}

	const (
		ctxCol   = 1 + 2 + swNameColW + 1 + swStateColW + swAgeColW + 2
		topicCol = ctxCol + swCtxColW + 2
	)
	view := m.View()
	for _, sess := range m.snap.Sessions {
		line := ""
		for _, l := range strings.Split(view, "\n") {
			if strings.Contains(ansi.Strip(l), sess.Topic) {
				line = ansi.Strip(l)
			}
		}
		if line == "" {
			t.Fatalf("no row rendered for %q:\n%s", sess.Name, view)
		}
		r := []rune(line)
		if got := string(r[topicCol:]); got != sess.Topic {
			t.Errorf("%s: topic starts at the wrong column; from %d got %q, want %q\n%s",
				sess.Name, topicCol, got, sess.Topic, view)
		}
		wantCtx := strings.Repeat(" ", swCtxColW)
		if sess.Context >= 0 {
			wantCtx = fmt.Sprintf("%d%%", sess.Context)
		}
		if got := string(r[ctxCol : ctxCol+swCtxColW]); !strings.HasSuffix(got, wantCtx) {
			t.Errorf("%s: context cell = %q, want it to end in %q\n%s", sess.Name, got, wantCtx, view)
		}
	}
}

// A name wider than its column is clipped rather than allowed to push every
// column to its right — the one case where alignment costs information.
func TestSwModelViewClipsLongName(t *testing.T) {
	m := swTestModel()
	m.snap.Sessions[0].Name = strings.Repeat("x", swNameColW+10)
	if view := ansi.Strip(m.View()); strings.Contains(view, strings.Repeat("x", swNameColW+1)) {
		t.Errorf("over-long name must be truncated to %d cells:\n%s", swNameColW, view)
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
