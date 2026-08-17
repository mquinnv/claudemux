package main

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// recordingWriter stands in for onboardConfigWriter in tests: it records
// what would have been written instead of touching disk, so a test can
// assert on the exact layout name without going anywhere near configSet or
// config.yml.
type recordingWriter struct {
	calls []string
	err   error
}

func (w *recordingWriter) write(name string) error {
	w.calls = append(w.calls, name)
	return w.err
}

func TestOnboardGridMove(t *testing.T) {
	tests := []struct {
		sel  int
		key  string
		want int
	}{
		// Top row: left/right move within the row.
		{0, "right", 1},
		{0, "l", 1},
		{1, "left", 0},
		{1, "h", 0},
		// Edges of a row are a no-op, not a wrap.
		{0, "left", 0},
		{1, "right", 1},
		// up/down move between rows, same column.
		{0, "down", 2},
		{0, "j", 2},
		{1, "down", 3},
		{2, "up", 0},
		{2, "k", 0},
		{3, "up", 1},
		// Edges of a column are a no-op.
		{0, "up", 0},
		{2, "down", 2},
	}
	for _, tt := range tests {
		if got := onboardGridMove(tt.sel, tt.key); got != tt.want {
			t.Errorf("onboardGridMove(%d, %q) = %d, want %d", tt.sel, tt.key, got, tt.want)
		}
	}
}

// The four layouts' names must be legalLayouts' values, in legalLayouts'
// order — that order is both the 1-4 numbering on screen and the (0,1)/(2,3)
// grid pairing, so the two lists drifting apart would be silent.
func TestOnboardLayoutsMatchLegalLayouts(t *testing.T) {
	if len(onboardLayouts) != len(legalLayouts) {
		t.Fatalf("onboardLayouts has %d entries, legalLayouts has %d", len(onboardLayouts), len(legalLayouts))
	}
	for i, l := range onboardLayouts {
		if l.name != legalLayouts[i] {
			t.Errorf("onboardLayouts[%d].name = %q, want %q", i, l.name, legalLayouts[i])
		}
	}
}

func TestOnboardModelNumberKeys(t *testing.T) {
	for i, l := range onboardLayouts {
		m := newOnboardModel(nil)
		key := string(rune('1' + i))
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if cmd != nil {
			t.Errorf("digit key %q produced a command", key)
		}
		if got := next.(onboardModel).sel; got != i {
			t.Errorf("digit key %q: sel = %d, want %d (%s)", key, got, i, l.name)
		}
	}
}

func TestOnboardModelArrowAndVimKeys(t *testing.T) {
	m := newOnboardModel(nil)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(onboardModel)
	if m.sel != 2 {
		t.Fatalf("down: sel = %d, want 2", m.sel)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	m = next.(onboardModel)
	if m.sel != 3 {
		t.Fatalf("l: sel = %d, want 3", m.sel)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(onboardModel)
	if m.sel != 1 {
		t.Fatalf("up: sel = %d, want 1", m.sel)
	}
}

// Enter must call the injected writer with exactly the selected layout's
// name — the one key the write path is allowed to touch — and report the
// choice as saved.
func TestOnboardModelEnterConfirms(t *testing.T) {
	rec := &recordingWriter{}
	m := newOnboardModel(rec.write)
	m.sel = 2 // shell-bottom

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(onboardModel)

	if !slices.Equal(rec.calls, []string{"shell-bottom"}) {
		t.Errorf("writer calls = %v, want [shell-bottom]", rec.calls)
	}
	if got.saved != "shell-bottom" {
		t.Errorf("saved = %q, want shell-bottom", got.saved)
	}
	if got.cancelled {
		t.Error("cancelled = true on a confirm")
	}
	if cmd == nil || cmd() != tea.Quit() {
		t.Error("enter must quit")
	}
}

// A save failure must not be reported as a saved choice — runOnboard's exit
// code depends on saved being empty whenever saveErr is set.
func TestOnboardModelEnterSaveFailure(t *testing.T) {
	boom := errors.New("disk full")
	rec := &recordingWriter{err: boom}
	m := newOnboardModel(rec.write)

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(onboardModel)

	if got.saved != "" {
		t.Errorf("saved = %q, want empty on a write failure", got.saved)
	}
	if got.saveErr != boom {
		t.Errorf("saveErr = %v, want %v", got.saveErr, boom)
	}
	if cmd == nil || cmd() != tea.Quit() {
		t.Error("enter must quit even on failure")
	}
}

// Cancel keys must not touch the writer at all — "writes NOTHING" is the
// contract, not "writes and then someone ignores it".
func TestOnboardModelCancelKeysWriteNothing(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("q")},
		{Type: tea.KeyEsc},
		{Type: tea.KeyCtrlC},
	} {
		rec := &recordingWriter{}
		m := newOnboardModel(rec.write)
		m.sel = 1

		next, cmd := m.Update(key)
		got := next.(onboardModel)

		if len(rec.calls) != 0 {
			t.Errorf("key %v: writer called %v, want no calls", key, rec.calls)
		}
		if !got.cancelled {
			t.Errorf("key %v: cancelled = false", key)
		}
		if got.saved != "" {
			t.Errorf("key %v: saved = %q, want empty", key, got.saved)
		}
		if cmd == nil || cmd() != tea.Quit() {
			t.Errorf("key %v: must quit", key)
		}
	}
}

// A nil writer (defensive: runOnboard always injects one) must not panic —
// enter still resolves to a no-op save rather than a crash.
func TestOnboardModelEnterNilWriter(t *testing.T) {
	m := newOnboardModel(nil)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(onboardModel)
	if got.saved != "shell-right" {
		t.Errorf("saved = %q, want shell-right", got.saved)
	}
	if cmd == nil {
		t.Error("no quit command")
	}
}

// The view must reproduce task-3-brief.md's diagrams character for
// character — every box-drawing line, unstyled, verbatim.
func TestOnboardViewExactDiagrams(t *testing.T) {
	m := newOnboardModel(nil)
	m.width = 100
	view := ansi.Strip(m.View())

	for _, diagram := range [][]string{diagShellRight, diagNoShell, diagShellBottom, diagHeadBottom} {
		for _, line := range diagram {
			if !strings.Contains(view, line) {
				t.Errorf("view missing diagram line %q:\n%s", line, view)
			}
		}
	}
	for _, header := range []string{"1) shell-right", "2) no-shell", "3) shell-bottom", "4) head-bottom"} {
		if !strings.Contains(view, header) {
			t.Errorf("view missing header %q", header)
		}
	}
}

// The exact side-by-side spacing from task-3-brief.md: two 16-wide columns
// joined by an 8-space gap, and shell-bottom's two extra rows trailing with
// nothing to their right.
func TestOnboardViewGridSpacingMatchesBrief(t *testing.T) {
	m := newOnboardModel(nil)
	m.width = 100
	lines := strings.Split(ansi.Strip(m.View()), "\n")

	want := []string{
		"1) shell-right          2) no-shell",
		"┌──────────────┐        ┌──────────────┐",
		"│ head         │        │ head         │",
		"├────────┬─────┤        ├──────────────┤",
		"│ claude │shell│        │ claude       │",
		"│        │     │        │              │",
		"└────────┴─────┘        └──────────────┘",
	}
	for _, w := range want {
		found := false
		for _, l := range lines {
			if l == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no line matched %q exactly", w)
		}
	}

	tailWant := []string{"│ shell        │", "└──────────────┘"}
	for _, w := range tailWant {
		found := false
		for _, l := range lines {
			if l == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no unpaired trailing line matched %q exactly", w)
		}
	}
}

// The selected box's header carries the highlight; the other three don't.
// go test's stdout isn't a tty, so lipgloss's auto-detected profile would
// otherwise strip Reverse() down to plain text and make every header look
// alike — force ANSI so the styling this test is checking actually happens.
func TestOnboardViewHighlightsSelection(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	defer lipgloss.SetColorProfile(orig)

	// sel 3 is "4) head-bottom", the RIGHT column of the second row — the
	// one header onboardRow leaves unpadded (see its comment: nothing
	// follows it on the line, so it keeps its natural width).
	m := newOnboardModel(nil)
	m.sel = 3
	m.width = 100
	raw := m.View()

	if !strings.Contains(raw, onboardSelStyle.Render("4) head-bottom")) {
		t.Error("selected header is not styled with onboardSelStyle")
	}
	// The other right-column header ("2) no-shell") is also unpadded, so it
	// has to be checked the same way; the two left-column headers are
	// padded to onboardColW before styling.
	if strings.Contains(raw, onboardSelStyle.Render("2) no-shell")) {
		t.Error("unselected header \"2) no-shell\" is styled as selected")
	}
	for _, header := range []string{"1) shell-right", "3) shell-bottom"} {
		padded := onboardPadRight(header, onboardColW)
		if strings.Contains(raw, onboardSelStyle.Render(padded)) {
			t.Errorf("unselected header %q is styled as selected", header)
		}
	}
}

// Every rendered row must fit the pane, same guard as boot's and the
// switchboard's views.
func TestOnboardViewFitsItsWidth(t *testing.T) {
	m := newOnboardModel(nil)
	m.width = 30
	for _, l := range strings.Split(m.View(), "\n") {
		if w := lipgloss.Width(l); w > 30 {
			t.Errorf("row %q is %d cells wide, want <= 30", l, w)
		}
	}
}

func TestOnboardResultConfirm(t *testing.T) {
	var buf bytes.Buffer
	code := onboardResult(onboardModel{saved: "no-shell"}, &buf)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if buf.String() != onboardConfirmLine+"\n" {
		t.Errorf("stderr = %q, want %q", buf.String(), onboardConfirmLine+"\n")
	}
}

func TestOnboardResultCancel(t *testing.T) {
	var buf bytes.Buffer
	code := onboardResult(onboardModel{cancelled: true}, &buf)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "claudemux setup") {
		t.Errorf("stderr = %q, want it to mention claudemux setup", buf.String())
	}
}

func TestOnboardResultSaveFailure(t *testing.T) {
	var buf bytes.Buffer
	code := onboardResult(onboardModel{saveErr: errors.New("disk full")}, &buf)
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	if !strings.Contains(buf.String(), "disk full") {
		t.Errorf("stderr = %q, want it to contain the error", buf.String())
	}
}

// A bubbletea program that fails outright (p.Run() erroring, or an
// unexpected final model type) must exit 3 — the same "something went
// wrong" code as a save failure, and distinct from exit 1's user-cancel —
// so a caller can tell "declined" from "broke".
func TestOnboardFatal(t *testing.T) {
	var buf bytes.Buffer
	code := onboardFatal("pty allocation failed", &buf)
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	if !strings.Contains(buf.String(), "pty allocation failed") {
		t.Errorf("stderr = %q, want it to contain the message", buf.String())
	}
	if code == 1 {
		t.Error("TUI failure must not share exit code 1 with user-cancel")
	}
}
