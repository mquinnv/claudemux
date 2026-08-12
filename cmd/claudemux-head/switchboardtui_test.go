package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

// swPreviewModel is swTestModel with claude panes attached — the preview needs
// something to capture.
func swPreviewModel() swModel {
	m := swTestModel()
	m.height = 46
	m.snap.Sessions[0].ClaudePane = "%2"
	m.snap.Sessions[1].ClaudePane = "%5"
	// Sessions[2] ("scratch") deliberately keeps none.
	return m
}

func TestSwModelSelectionRequestsPreview(t *testing.T) {
	m := swPreviewModel()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if cmd == nil {
		t.Fatal("moving the selection must request a capture")
	}
	if !next.(swModel).previewInFlight {
		t.Error("a requested capture must be marked in flight")
	}
}

func TestSwModelNoPreviewWithoutClaudePane(t *testing.T) {
	m := swPreviewModel()
	m.sel = 2 // scratch: no claude pane
	if cmd := m.previewCmd(); cmd != nil {
		t.Error("a session with no claude pane must not request a capture")
	}
}

func TestSwModelPreviewInFlightGuard(t *testing.T) {
	m := swPreviewModel()
	if cmd := m.previewCmd(); cmd == nil {
		t.Fatal("the first request must fire")
	}
	if cmd := m.previewCmd(); cmd != nil {
		t.Error("a second request must be dropped while one is in flight")
	}
}

func TestSwModelStorePreview(t *testing.T) {
	m := swPreviewModel()
	m.previewInFlight = true
	next, _ := m.Update(swPreviewMsg{pane: "%2", out: "hello\n"})
	got := next.(swModel)
	if got.previewOut != "hello\n" || got.previewPane != "%2" {
		t.Errorf("preview not stored: out=%q pane=%q", got.previewOut, got.previewPane)
	}
	if got.previewInFlight {
		t.Error("a landed capture must clear the in-flight flag")
	}
	if got.previewErr {
		t.Error("a successful capture must not be flagged as an error")
	}
}

// A capture that lands after the cursor moved belongs to a session the user is
// no longer looking at; painting it would flash the wrong screen under the
// right title. The drop must compare against the CURRENTLY selected session's
// pane, not against the pane the request was issued for: the two happen to be
// the same value at request time, so the selection has to move between
// request and reply for a test to tell those comparisons apart.
func TestSwModelDropsStalePreview(t *testing.T) {
	m := swPreviewModel()
	if cmd := m.previewCmd(); cmd == nil {
		t.Fatal("request against session 0 (%2) must fire")
	}
	m.previewOut = "current"
	m.previewPane = "%2"
	m.sel = 1 // user moves to session 1 ("%5") before the capture lands
	next, _ := m.Update(swPreviewMsg{pane: "%2", out: "stale"})
	got := next.(swModel)
	if got.previewOut != "current" {
		t.Errorf("previewOut = %q, want the current capture kept", got.previewOut)
	}
	if got.previewInFlight {
		t.Error("even a dropped capture must clear the in-flight flag, or the preview wedges")
	}
}

// The poll and the key handler are two independent triggers for a capture;
// nothing else exercises the poll path landing a request, so a regression
// that dropped the previewCmd() call from the swSnapshotMsg case would leave
// the preview refreshing only on j/k and no test would notice.
func TestSwModelSnapshotRequestsPreview(t *testing.T) {
	m := swPreviewModel()
	next, cmd := m.Update(swSnapshotMsg{snap: m.snap})
	if cmd == nil {
		t.Fatal("a snapshot must always schedule the next tick")
	}
	if !next.(swModel).previewInFlight {
		t.Error("a snapshot poll must also request a capture for the selected session")
	}
}

func TestSwModelPreviewErrorFlagged(t *testing.T) {
	m := swPreviewModel()
	m.previewInFlight = true
	next, _ := m.Update(swPreviewMsg{pane: "%2", err: errors.New("no such pane")})
	got := next.(swModel)
	if !got.previewErr {
		t.Error("a failed capture must be flagged")
	}
	if got.lastErr != "" {
		t.Errorf("lastErr = %q: a preview failure must not claim the fleet-poll error line", got.lastErr)
	}
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

// Mutation guard for the clipLine call on line 1 (and, by the same
// mechanism, line 2 and the status/footer lines): every rendered line must
// fit within m.width in display cells. A long name/state/topic combination
// assembles a line far wider than the pane before clipping, so if the width
// guard is ever dropped this fails instead of merely looking wrong on a real
// terminal.
func TestSwModelViewNeverExceedsWidth(t *testing.T) {
	now := time.Now()
	m := newSwModel("%9")
	m.width, m.height = 40, 24
	m.snap = swSnapshot{Sessions: []swSession{
		{Name: "a-very-long-session-name-that-overflows-the-grid",
			State: "Tool:AskUserQuestion", Since: now.Add(-3 * time.Hour), Context: 100,
			Topic:   "a topic long enough to overflow the configured pane width many times over",
			Summary: "a summary long enough to overflow the pane width on its own, several times over"},
	}}
	for _, l := range strings.Split(m.View(), "\n") {
		if w := lipgloss.Width(l); w > m.width {
			t.Errorf("line measures %d cells, want <= %d: %q", w, m.width, l)
		}
	}
}

// CJK runes measure two cells each. The name column must clip and pad by
// display width, not rune count, or a wide-rune session name truncated to
// swNameColW RUNES still measures 2x that many CELLS, overrunning the
// column and pushing state/age/context/topic to the right — same failure
// mode TestSwModelViewAlignsColumns guards for ordinary names.
func TestSwModelViewClipsWideRuneNameToColumn(t *testing.T) {
	now := time.Now()
	m := newSwModel("%9")
	m.width, m.height = 100, 24
	m.snap = swSnapshot{Sessions: []swSession{
		{Name: strings.Repeat("囲", swNameColW+5), State: "Idle", Since: now,
			Context: 42, Topic: "wide-rune-name-alignment"},
	}}
	const (
		ctxCol   = 1 + 2 + swNameColW + 1 + swStateColW + swAgeColW + 2
		topicCol = ctxCol + swCtxColW + 2
	)
	view := ansi.Strip(m.View())
	line := ""
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, "wide-rune-name-alignment") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no row rendered:\n%s", view)
	}
	// Cut by display CELL width, not rune index: the name is wide-rune
	// content, so a rune-indexed slice (as ordinary-name tests use) would
	// itself misalign against the CJK text and false-fail. ansi.Cut is the
	// same grapheme/wide-rune-aware measure clipLine and swCell use.
	topic := "wide-rune-name-alignment"
	if got := ansi.Cut(line, topicCol, topicCol+len([]rune(topic))); got != topic {
		t.Errorf("topic starts at the wrong column with a wide-rune name; from cell %d got %q, want %q\n%s",
			topicCol, got, topic, view)
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

func TestSwModelViewShowsPreview(t *testing.T) {
	m := swPreviewModel()
	m.previewPane = "%2"
	m.previewOut = "● Bash(git push)\n⎿  pushed\n"
	view := ansi.Strip(m.View())
	for _, want := range []string{"● Bash(git push)", "⎿  pushed", "api"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestSwModelViewPreviewOmittedWhenShort(t *testing.T) {
	m := swPreviewModel()
	m.height = 15 // below the floor: fleet wins
	m.previewPane = "%2"
	m.previewOut = "captured content here\n"
	if view := ansi.Strip(m.View()); strings.Contains(view, "captured content here") {
		t.Errorf("a short pane must drop the preview, not squeeze it:\n%s", view)
	}
}

func TestSwModelViewPreviewPlaceholders(t *testing.T) {
	m := swPreviewModel()
	m.sel = 2 // scratch has no claude pane
	if view := ansi.Strip(m.View()); !strings.Contains(view, "no claude pane") {
		t.Errorf("a session with no claude pane needs a reason in the box:\n%s", view)
	}

	m = swPreviewModel()
	m.previewErr = true
	if view := ansi.Strip(m.View()); !strings.Contains(view, "preview unavailable") {
		t.Errorf("a failed capture needs a reason in the box:\n%s", view)
	}
}

// The preview must not be pushed off the bottom by a long fleet: the list is
// capped and says so.
func TestSwModelViewCapsListForPreview(t *testing.T) {
	m := swPreviewModel()
	m.height = 20
	var sessions []swSession
	for i := 0; i < 30; i++ {
		sessions = append(sessions, swSession{
			Name: fmt.Sprintf("sess-%02d", i), State: "Idle", Context: -1, ClaudePane: "%2",
		})
	}
	m.snap.Sessions = sessions
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "more") {
		t.Errorf("a capped list must say how many it dropped:\n%s", view)
	}
	if strings.Contains(view, "sess-29") {
		t.Errorf("the list must be capped, not rendered in full:\n%s", view)
	}
	if lines := strings.Count(view, "\n"); lines > m.height {
		t.Errorf("view is %d lines, want at most %d", lines, m.height)
	}
}

func TestSwModelViewEmptyFleetHasNoPreview(t *testing.T) {
	m := newSwModel("%9")
	m.width, m.height = 80, 46
	if view := ansi.Strip(m.View()); strings.Contains(view, "┌") {
		t.Errorf("an empty fleet must not draw a preview box:\n%s", view)
	}
}
