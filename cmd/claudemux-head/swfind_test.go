package main

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSwFindRank(t *testing.T) {
	for _, c := range []struct {
		name, q string
		want    int
	}{
		{"remix-3", "rem", 0},
		{"remix-3", "REM", 0},
		{"ag-admin", "adm", 1},
		{"claudemux", "mux", 2},
		{"remix", "rmx", 3},
		{"remix", "xyz", -1},
		{"remix", "", -1},
	} {
		if got := swFindRank(c.name, c.q); got != c.want {
			t.Errorf("swFindRank(%q, %q) = %d, want %d", c.name, c.q, got, c.want)
		}
	}
}

func TestSwFindMatchesBestRankFirst(t *testing.T) {
	sess := []swSession{{Name: "claudemux"}, {Name: "ag-admin"}, {Name: "admin-tools"}, {Name: "scala3d"}}
	got := swFindMatches(sess, "adm")
	// claudemux matches too, as a subsequence (cl-a-u-d-e-m), so it ranks last.
	if want := []int{2, 1, 0}; !slices.Equal(got, want) {
		t.Errorf("matches = %v, want %v (prefix, word-prefix, subsequence)", got, want)
	}
}

func swType(t *testing.T, m swModel, keys ...string) (swModel, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		next, c := m.Update(msg)
		m, cmd = next.(swModel), c
	}
	return m, cmd
}

func TestSwFindSelectsAndJumps(t *testing.T) {
	m := swTestModel() // api, web, scratch
	m, _ = swType(t, m, "/", "s", "c")
	if !m.finding || m.findInput != "sc" {
		t.Fatalf("finding=%v input=%q", m.finding, m.findInput)
	}
	if m.snap.Sessions[m.sel].Name != "scratch" {
		t.Fatalf("selected %q, want scratch", m.snap.Sessions[m.sel].Name)
	}
	// j and k are letters while finding, not navigation.
	m, _ = swType(t, m, "j")
	if m.findInput != "scj" || !m.finding {
		t.Fatalf("j should be typed, input=%q", m.findInput)
	}
	m, _ = swType(t, m, "backspace")
	m, cmd := swType(t, m, "enter")
	if m.finding {
		t.Error("enter should close find mode")
	}
	if cmd == nil {
		t.Error("enter on a match should switch the client")
	}
}

func TestSwFindEscRestoresSelection(t *testing.T) {
	m := swTestModel()
	m.sel = 1 // web
	m, _ = swType(t, m, "/", "a", "p", "i")
	if m.snap.Sessions[m.sel].Name != "api" {
		t.Fatalf("selected %q, want api", m.snap.Sessions[m.sel].Name)
	}
	m, _ = swType(t, m, "esc")
	if m.finding || m.snap.Sessions[m.sel].Name != "web" {
		t.Errorf("after esc: finding=%v selected %q, want web", m.finding, m.snap.Sessions[m.sel].Name)
	}
}

func TestSwFindNoMatchKeepsPromptOpen(t *testing.T) {
	m := swTestModel()
	m, _ = swType(t, m, "/", "z", "z")
	if !strings.Contains(m.View(), "no match") {
		t.Errorf("status should say no match:\n%s", m.View())
	}
	m, cmd := swType(t, m, "enter")
	if !m.finding || cmd != nil {
		t.Errorf("enter on no match should keep finding and not switch: finding=%v cmd=%v", m.finding, cmd != nil)
	}
}

func TestSwFindDownCyclesMatches(t *testing.T) {
	m := swTestModel()
	m.snap.Sessions = append(m.snap.Sessions, swSession{Name: "api-2", Context: -1})
	m, _ = swType(t, m, "/", "a", "p")
	if m.snap.Sessions[m.sel].Name != "api" {
		t.Fatalf("selected %q, want api", m.snap.Sessions[m.sel].Name)
	}
	m, _ = swType(t, m, "down")
	if m.snap.Sessions[m.sel].Name != "api-2" {
		t.Fatalf("after down selected %q, want api-2", m.snap.Sessions[m.sel].Name)
	}
	m, _ = swType(t, m, "down")
	if m.snap.Sessions[m.sel].Name != "api" {
		t.Errorf("down should wrap to api, got %q", m.snap.Sessions[m.sel].Name)
	}
}

func TestSwFindHoldsConductor(t *testing.T) {
	m := swTestModel()
	m, _ = swType(t, m, "/")
	if m.shouldAutoRestart(m.snap.Sessions[0].Since) {
		t.Error("auto-restart must wait out a find prompt")
	}
}
