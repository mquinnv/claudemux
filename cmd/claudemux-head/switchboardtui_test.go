package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func swTestModel() swModel {
	m := newSwModel("%9")
	m.width, m.height = 100, 24
	m.snap = swSnapshot{
		Sessions: []swSession{
			{Name: "api", State: "Idle", Since: time.Now().Add(-2 * time.Minute),
				Context: 37, Topic: "build fixes", Summary: "fixing the build", Prompt: "run the tests",
				Model: "claude-opus-4-7"},
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
//
// Finding 3 changed what happens next: a dropped result now re-issues a
// capture for the current selection (session 1, pane "%5") instead of just
// clearing the flag and waiting for the next poll. That re-issue goes through
// previewCmd, which clears previewOut/previewPane whenever the pane being
// requested differs from what's currently held — so the sentinel "current"
// value (deliberately parked under the OLD pane, "%2", to prove it isn't the
// thing a same-pane comparison would keep) gets wiped rather than kept. What
// must still hold is the actual invariant: the stale reply's own payload
// ("stale") is never the thing painted.
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
	if got.previewOut == "stale" || got.previewPane == "%2" {
		t.Errorf("the stale reply must never be painted: out=%q pane=%q", got.previewOut, got.previewPane)
	}
	// A dropped capture with a claude pane still selected re-issues (Finding
	// 3), so the flag goes back to true here rather than staying cleared.
	if !got.previewInFlight {
		t.Error("a dropped capture with a claude pane still selected must re-issue, leaving the flag set")
	}
}

// Finding 3: without a re-issue, a stale-dropped capture leaves the preview
// blank until the next 1s poll — up to a full second behind a fast j/k, the
// exact lag the selection-change trigger exists to remove. previewCmd clears
// previewOut on a pane change (see previewCmd), so "blank" not "wrong" is
// what a fast scroll used to show.
func TestSwModelStalePreviewReissuesForCurrentSelection(t *testing.T) {
	m := swPreviewModel()
	if cmd := m.previewCmd(); cmd == nil {
		t.Fatal("request against session 0 (%2) must fire")
	}
	m.previewOut = "current"
	m.previewPane = "%2"
	m.sel = 1 // user moves to session 1 ("%5") before the capture lands
	next, cmd := m.Update(swPreviewMsg{pane: "%2", out: "stale"})
	if cmd == nil {
		t.Fatal("a stale result must re-issue a capture for the current selection, not just drop it")
	}
	if !next.(swModel).previewInFlight {
		t.Error("the re-issued capture must be marked in flight")
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

func TestSwModelEscReturnsToLastSession(t *testing.T) {
	m := swTestModel()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Error("esc with a client must produce a switch-back cmd")
	}
	m.cond.client = ""
	m.snap.Clients = map[string]string{}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Error("esc without a client must be a no-op")
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
	for _, want := range []string{"37%", "build fixes", "fixing the build", "opus 4.7"} {
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
		{Name: "api", State: "Idle", Since: now.Add(-2 * time.Minute), Context: 7, Topic: "one-digit ctx",
			Model: "claude-opus-4-7"},
		{Name: "a-very-long-session-name-that-overflows", State: "Tool:AskUserQuestion",
			Since: now.Add(-3 * time.Hour), Context: 100, Topic: "long name, long state",
			Model: "claude-sonnet-4-5[1m]"},
		{Name: "web", State: "Thinking", Since: now, Context: -1, Topic: "no context yet"},
		{Name: "scratch", Context: -1, Topic: "no state, no age"},
	}}

	const (
		ctxCol   = 1 + 2 + swNameColW + 1 + swStateColW + swAgeColW + 2
		topicCol = ctxCol + swCtxColW + 1 + swModelColW + 2
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
	// Scoped to the fleet row itself (line index 2: title, blank, then the
	// first session's row) rather than the whole view. This test's intent is
	// "the name COLUMN truncates" — it must not also constrain the preview
	// box title, which is full pane width and free to show more of a long
	// name than the row's fixed swNameColW allows.
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	row := lines[2]
	if strings.Contains(row, strings.Repeat("x", swNameColW+1)) {
		t.Errorf("over-long name must be truncated to %d cells in the fleet row:\n%s", swNameColW, row)
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
		topicCol = ctxCol + swCtxColW + 1 + swModelColW + 2
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

// A head's space lands here as a request on the poll, and flips conduct mode
// through the same path the lobby's own space key uses.
func TestSwModelAppliesHeadConductRequest(t *testing.T) {
	now := time.Now()
	m := swTestModel()
	m.cond.phase = swEscorting
	m.cond.escortee = "api"
	req := conductRequestValue(now)
	next, _ := m.Update(swSnapshotMsg{at: now, conductReq: req, err: errors.New("tmux is wedged")})
	m = next.(swModel)
	// Deliberately on the tmux-error path: standby is a local flag, and a
	// fleet listing that failed is no reason to ignore a key the user pressed.
	if !m.standby {
		t.Fatal("a fresh request must enter standby")
	}
	if m.cond.escortee != "" || m.cond.phase != swPaused {
		t.Errorf("a requested toggle must neutralize the conductor, got phase=%v escortee=%q",
			m.cond.phase, m.cond.escortee)
	}
	// The same token again is the same press, re-read because the head leaves
	// it published; re-applying it would toggle the mode back on every poll.
	next, _ = m.Update(swSnapshotMsg{at: now, conductReq: req})
	m = next.(swModel)
	if !m.standby {
		t.Error("a re-read of an already-applied request must not toggle again")
	}
	// A new press does toggle again.
	next, _ = m.Update(swSnapshotMsg{at: now, conductReq: conductRequestValue(now.Add(time.Millisecond))})
	if next.(swModel).standby {
		t.Error("a second request must leave standby")
	}
}

// A request older than the staleness window is a keypress from before this
// lobby existed (or from one that could not reach it), and must not fire late.
func TestSwModelIgnoresStaleConductRequest(t *testing.T) {
	m := swTestModel()
	stale := conductRequestValue(time.Now().Add(-conductStaleAfter - time.Second))
	next, _ := m.Update(swSnapshotMsg{at: time.Now(), conductReq: stale})
	if next.(swModel).standby {
		t.Error("a stale request must not toggle conduct mode")
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

// Regression: standby freezes step() entirely, so a hand-back latched right
// before standby began must not survive it. Otherwise the client could
// wander off to another session and back while standby was on (invisible to
// the conductor) and, on standby-off, get yanked by a latch describing a
// completely different moment.
func TestSwModelStandbySpaceClearsStaleHandBackLatch(t *testing.T) {
	m := swTestModel()
	// Simulate: the user had already handed "api" back (latched
	// pausedHandedBack with the queue empty at that instant, so nothing
	// dispatched yet), and is still sitting at "api" when standby toggles on.
	m.cond.phase = swPaused
	m.cond.pausedCur = "api"
	m.cond.pausedCurWaiting = false
	m.cond.pausedHandedBack = true
	m.snap.Clients = map[string]string{"/dev/ttys001": "api"}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	m = next.(swModel)
	if !m.standby {
		t.Fatal("space must enter standby")
	}
	if m.cond.pausedCur != "" || m.cond.pausedCurWaiting || m.cond.pausedHandedBack {
		t.Fatalf("standby must clear the paused observation, got pausedCur=%q pausedCurWaiting=%v pausedHandedBack=%v",
			m.cond.pausedCur, m.cond.pausedCurWaiting, m.cond.pausedHandedBack)
	}

	// Leave standby, then feed a snapshot where "api" is current again (busy)
	// and a session is waiting. If the stale latch had survived, this would
	// dispatch immediately; it must not.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	m = next.(swModel)
	if m.standby {
		t.Fatal("space again must leave standby")
	}
	snap := swSnapshot{
		Sessions: []swSession{
			{Name: "api", State: "Thinking", Since: time.Unix(1_754_700_000, 0)},
			{Name: "queued", State: "Idle", Since: time.Unix(1_754_700_000, 0)},
		},
		Lobby:   "switchboard",
		Clients: map[string]string{"/dev/ttys001": "api"},
	}
	next, _ = m.Update(swSnapshotMsg{snap: snap})
	m = next.(swModel)
	if m.cond.phase == swEscorting {
		t.Errorf("a stale hand-back latch must not survive standby: phase=%v escortee=%q", m.cond.phase, m.cond.escortee)
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

// Finding 1 regression: under the old `list < 2` guard, height 16 admitted
// the box (list=2) but left only a 1-row budget after View's own "+N more"
// reservation — not enough for even one session, which has a 2-row floor
// once it carries a summary or prompt. The lobby rendered a box and ZERO
// session rows. With the guard raised to `list < 3`, height 16 now falls
// below the floor and the box is omitted, so the fleet renders exactly as
// it does with no preview at all.
func TestSwModelViewOneRowShortDropsBoxAndShowsFleet(t *testing.T) {
	m := swPreviewModel()
	m.height = 16
	view := ansi.Strip(m.View())
	if strings.Contains(view, "┌") {
		t.Errorf("height 16 is below the preview floor; the box must not render:\n%s", view)
	}
	if !strings.Contains(view, "fixing the build") {
		t.Errorf("the fleet must still render at least one full session row:\n%s", view)
	}
}

// Finding 1: at the smallest height where computePreviewLayout now shows a
// box (list=3, budget 2 after the "+N more" reservation), the fleet must
// still render at least one full session row alongside it — not the
// zero-rows-plus-box the old `list < 2` floor allowed one row lower.
func TestSwModelViewSmallestPreviewHeightStillShowsASessionRow(t *testing.T) {
	m := swPreviewModel()
	m.height = 17
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "┌") {
		t.Errorf("height 17 should be tall enough to show the preview box:\n%s", view)
	}
	if !strings.Contains(view, "fixing the build") {
		t.Errorf("even at the smallest height that shows a box, a session row must render:\n%s", view)
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
	raw := m.View()
	view := ansi.Strip(raw)
	if !strings.Contains(view, "more") {
		t.Errorf("a capped list must say how many it dropped:\n%s", view)
	}
	if strings.Contains(view, "sess-29") {
		t.Errorf("the list must be capped, not rendered in full:\n%s", view)
	}
	// The box must have actually rendered — a regression that dropped it
	// while keeping the row cap would otherwise pass this test silently.
	if !strings.Contains(view, "┌") {
		t.Errorf("the preview box must still render when the list is capped:\n%s", view)
	}
	// strings.Count counts SEPARATORS: an N-line view (the footer has no
	// trailing newline) contains N-1 of them, so Count permits m.height+1
	// lines and would not have caught a one-row overflow here.
	// len(strings.Split(...)) counts the lines themselves. Proven by
	// mutation: deleting `budget--` in View's row-budget computation lets a
	// 6th session row through at this height, producing 21 lines in this
	// 20-row pane — Count(view, "\n") stays at 20 (<= m.height) and the old
	// assertion would have stayed green.
	rawLines := strings.Split(raw, "\n")
	if len(rawLines) > m.height {
		t.Errorf("view is %d lines, want at most %d", len(rawLines), m.height)
	}
	// Every line must also fit within the pane's width — a row-count check
	// alone would miss a box or row that overflows sideways.
	for i, l := range rawLines {
		if w := lipgloss.Width(l); w > m.width {
			t.Errorf("line %d is %d cells wide, want at most %d: %q", i, w, m.width, l)
		}
	}
}

// A small fleet must not strand rows as blank space between the list and the
// preview: the box grows into whatever the fleet doesn't use, filling the pane.
func TestSwModelViewPreviewGrowsIntoUnusedListRows(t *testing.T) {
	m := swPreviewModel() // fleet wants 4 rows; height 46
	raw := m.View()
	view := ansi.Strip(raw)
	lines := strings.Split(view, "\n")
	top, bottom := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "┌") {
			top = i
		}
		if strings.Contains(l, "└") {
			bottom = i
		}
	}
	if top < 0 || bottom < 0 {
		t.Fatalf("no preview box rendered:\n%s", view)
	}
	// avail 40, claim 13, list share 25, fleet needs 4 -> 34 content rows.
	if got := bottom - top - 1; got != 34 {
		t.Errorf("box has %d content rows, want 34 (grown past the %d-row ceiling):\n%s",
			got, swPreviewMaxRows, view)
	}
	// The whole point: the view fills the pane instead of leaving dead space.
	if got := len(strings.Split(raw, "\n")); got != m.height {
		t.Errorf("view is %d lines, want %d (the full pane)", got, m.height)
	}
}

func TestSwModelNKeyOpensCreatePrompt(t *testing.T) {
	m := swTestModel()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = next.(swModel)
	if !m.creating {
		t.Fatal("n must open the create prompt")
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "new session:") {
		t.Errorf("view must show the create prompt:\n%s", view)
	}
	if !strings.Contains(view, "esc cancel") {
		t.Errorf("footer must switch to the input-mode keys:\n%s", view)
	}
}

// While typing a query, every printable key is a literal character: q must
// not quit, j/k must not move the selection, and space must not toggle
// standby — a zoxide query can legitimately contain any of them.
func TestSwModelCreateInputIsLiteral(t *testing.T) {
	m := swTestModel()
	m.creating = true
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyRunes, Runes: []rune{'j'}},
		{Type: tea.KeySpace},
		{Type: tea.KeyRunes, Runes: []rune{'k'}},
	} {
		next, cmd := m.Update(key)
		if cmd != nil {
			t.Fatalf("typing %q must not produce a command", key.String())
		}
		m = next.(swModel)
	}
	if m.createInput != "qj k" {
		t.Errorf("createInput = %q, want %q", m.createInput, "qj k")
	}
	if m.sel != 0 || m.standby {
		t.Errorf("literal keys leaked into navigation: sel=%d standby=%v", m.sel, m.standby)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := next.(swModel).createInput; got != "qj " {
		t.Errorf("backspace must trim the last rune: %q", got)
	}
}

func TestSwModelCreateEscCancels(t *testing.T) {
	m := swTestModel()
	m.creating = true
	m.createInput = "api"
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(swModel)
	if m.creating || m.createInput != "" {
		t.Errorf("esc must cancel and clear: creating=%v input=%q", m.creating, m.createInput)
	}
	if cmd != nil || m.createBusy {
		t.Error("a cancelled prompt must not launch anything")
	}
}

func TestSwModelCreateEnterLaunches(t *testing.T) {
	m := swTestModel()
	m.creating = true
	m.createInput = "  api  "
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(swModel)
	if cmd == nil {
		t.Fatal("enter with a query must launch a create command")
	}
	if !got.createBusy || got.creating {
		t.Errorf("a launched create must be busy and out of input mode: busy=%v creating=%v",
			got.createBusy, got.creating)
	}
}

func TestSwModelCreateEnterBlankIsNoop(t *testing.T) {
	m := swTestModel()
	m.creating = true
	m.createInput = "   "
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(swModel)
	if cmd != nil || got.createBusy {
		t.Error("a blank query must not launch anything")
	}
	if got.creating {
		t.Error("enter on a blank query must still close the prompt")
	}
}

func TestSwModelNKeyIgnoredWhileBusy(t *testing.T) {
	m := swTestModel()
	m.createBusy = true
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if next.(swModel).creating {
		t.Error("n while a launch is in flight must not reopen the prompt")
	}
}

// The conductor must sit out the create flow: dispatching mid-typing yanks
// the user off the prompt, and dispatching mid-launch races the switch the
// landed swCreateMsg is about to issue.
func TestSwModelCreateSuppressesConductor(t *testing.T) {
	snap := swSnapshot{
		Sessions: []swSession{{Name: "api", State: "Idle", Since: time.Unix(100, 0)}},
		Lobby:    "switchboard",
		Clients:  map[string]string{"/dev/ttys001": "switchboard"},
	}
	for _, tc := range []struct {
		name string
		prep func(*swModel)
	}{
		{"typing", func(m *swModel) { m.creating = true }},
		{"launch in flight", func(m *swModel) { m.createBusy = true }},
	} {
		m := swTestModel()
		tc.prep(&m)
		next, _ := m.Update(swSnapshotMsg{snap: snap})
		got := next.(swModel)
		if got.cond.phase != swParked || got.cond.escortee != "" {
			t.Errorf("%s: conductor dispatched: phase=%v escortee=%q",
				tc.name, got.cond.phase, got.cond.escortee)
		}
	}
}

func TestSwModelCreateResultSwitchesToNewSession(t *testing.T) {
	m := swTestModel()
	m.createBusy = true
	next, cmd := m.Update(swCreateMsg{name: "api-2"})
	got := next.(swModel)
	if got.createBusy {
		t.Error("a landed create must clear the busy flag")
	}
	if cmd == nil {
		t.Error("a created session must be switched to")
	}

	m = swTestModel()
	m.createBusy = true
	m.cond.client = ""
	if _, cmd := m.Update(swCreateMsg{name: "api-2"}); cmd != nil {
		t.Error("with no client to move, a landed create must not switch")
	}
}

func TestSwModelCreateFailureShownOnStatusLine(t *testing.T) {
	m := swTestModel()
	m.createBusy = true
	next, cmd := m.Update(swCreateMsg{err: errors.New("'nope' is not a directory and has no zoxide match")})
	got := next.(swModel)
	if cmd != nil {
		t.Error("a failed create must not switch anywhere")
	}
	if got.createBusy {
		t.Error("a failed create must clear the busy flag")
	}
	if view := ansi.Strip(got.View()); !strings.Contains(view, "create failed: 'nope' is not a directory") {
		t.Errorf("the failure must reach the status line:\n%s", view)
	}
}

func TestSwModelCreateBusyStatusLine(t *testing.T) {
	m := swTestModel()
	m.createBusy = true
	if view := ansi.Strip(m.View()); !strings.Contains(view, "creating session") {
		t.Errorf("an in-flight launch must say so on the status line:\n%s", view)
	}
}

func TestSwModelViewEmptyFleetHasNoPreview(t *testing.T) {
	m := newSwModel("%9")
	m.width, m.height = 80, 46
	if view := ansi.Strip(m.View()); strings.Contains(view, "┌") {
		t.Errorf("an empty fleet must not draw a preview box:\n%s", view)
	}
}

func TestSwitchboardRestartKey(t *testing.T) {
	m := newSwModel("%1")
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	sm := got.(swModel)
	if !sm.restart {
		t.Error("R must request a restart")
	}
	if cmd == nil {
		t.Error("R must quit the program")
	}
}

func TestSwitchboardRestartKeyStaysLiteralWhileCreating(t *testing.T) {
	m := newSwModel("%1")
	m.creating = true
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	sm := got.(swModel)
	if sm.restart {
		t.Error("R inside the create prompt is input, not a restart")
	}
	if sm.createInput != "R" {
		t.Errorf("createInput = %q, want R", sm.createInput)
	}
}

func TestSwitchboardShouldAutoRestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp, _ := binStampOf(p)
	m := newSwModel("%1")
	m.launchBin, m.launchBinOK = stamp, true
	now := time.Now()
	if m.shouldAutoRestart(now) {
		t.Error("unchanged binary must not restart")
	}
	if err := os.WriteFile(p, []byte("v2-longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-time.Minute)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if !m.shouldAutoRestart(now) {
		t.Error("replaced binary must restart a parked lobby")
	}
	for _, tweak := range []func(*swModel){
		func(m *swModel) { m.standby = true },
		func(m *swModel) { m.creating = true },
		func(m *swModel) { m.createBusy = true },
		func(m *swModel) { m.cond.phase = swEscorting },
		func(m *swModel) { m.cond.phase = swPaused },
		func(m *swModel) { m.fleetRestarting = true },
	} {
		mm := newSwModel("%1")
		mm.launchBin, mm.launchBinOK = stamp, true
		tweak(&mm)
		if mm.shouldAutoRestart(now) {
			t.Errorf("non-quiescent lobby must not restart (%+v)", mm)
		}
	}
}

// TestSwitchboardShouldAutoRestartLiveSnooze covers the case a parked lobby
// still has a live snooze: the user just walked an escortee back to the
// lobby (step() snoozes that session and lands in swParked), so a re-exec
// right now would drop conductor.snoozed and un-skip the session the user
// just skipped. shouldAutoRestart must wait out the snooze — worst case the
// full swSnoozeTTL — before taking a pending rebuild.
func TestSwitchboardShouldAutoRestartLiveSnooze(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp, _ := binStampOf(p)
	m := newSwModel("%1")
	m.launchBin, m.launchBinOK = stamp, true
	m.cond.phase = swParked

	if err := os.WriteFile(p, []byte("v2-longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-time.Minute)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}

	m.cond.snoozed = map[string]swSnooze{"x": {since: time.Unix(100, 0), at: time.Now()}}
	if m.shouldAutoRestart(now) {
		t.Error("parked lobby with a live snooze must not restart")
	}

	m.cond.snoozed = map[string]swSnooze{}
	if !m.shouldAutoRestart(now) {
		t.Error("parked lobby with no live snoozes must restart on a changed binary")
	}
}

func TestFleetRestartArgs(t *testing.T) {
	sessions := []swSession{
		{Name: "a", HeadPane: "%10"},
		{Name: "b"}, // no head pane recorded: must be skipped, not sent to pane ""
		{Name: "c", HeadPane: "%12"},
	}
	got := swFleetRestartArgs(sessions)
	want := [][]string{
		{"send-keys", "-t", "%10", "R"},
		{"send-keys", "-t", "%12", "R"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d argvs, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if strings.Join(got[i], " ") != strings.Join(want[i], " ") {
			t.Errorf("argv[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestSwitchboardFleetRestartKey(t *testing.T) {
	m := newSwModel("%1")
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	sm := got.(swModel)
	if sm.restart {
		t.Error("ctrl+r must not restart self before the sends complete")
	}
	if !sm.fleetRestarting {
		t.Error("ctrl+r must mark a sweep in flight, so a racing poll can't auto-restart out from under it")
	}
	if cmd == nil {
		t.Fatal("ctrl+r must dispatch the fleet-restart cmd")
	}
	got2, cmd2 := sm.Update(swFleetRestartMsg{})
	sm2 := got2.(swModel)
	if !sm2.restart {
		t.Error("fleet-restart completion must request self-restart")
	}
	if cmd2 == nil {
		t.Error("fleet-restart completion must quit the program")
	}
}

// TestSwitchboardFleetRestartKeyDoublePress covers the case a second ctrl+r
// lands while a sweep is already in flight: dispatching another sweep would
// race two goroutines' send-keys against each other, so the second press
// must be a no-op rather than issuing swRestartFleetCmd again.
func TestSwitchboardFleetRestartKeyDoublePress(t *testing.T) {
	m := newSwModel("%1")
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	sm := got.(swModel)

	got2, cmd2 := sm.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	sm2 := got2.(swModel)
	if cmd2 != nil {
		t.Error("a second ctrl+r while a sweep is in flight must not dispatch another")
	}
	if sm2.restart != sm.restart || !sm2.fleetRestarting {
		t.Errorf("model must be unchanged by the no-op second press: got restart=%v fleetRestarting=%v",
			sm2.restart, sm2.fleetRestarting)
	}
}

func TestSwitchboardFleetRestartKeyInertWhileCreating(t *testing.T) {
	m := newSwModel("%1")
	m.creating = true
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	sm := got.(swModel)
	if sm.restart {
		t.Error("ctrl+r inside the create prompt must be inert")
	}
	if sm.createInput != "" {
		t.Errorf("ctrl+r must not type into the create prompt, got %q", sm.createInput)
	}
}

// The title line carries a mode badge so conduct-vs-standby is readable at a
// glance, not just in the faint status line at the bottom.
func TestSwModeBadge(t *testing.T) {
	cases := []struct {
		name    string
		standby bool
		phase   swPhase
		want    string
	}{
		{"conducting", false, swParked, "CONDUCTING"},
		{"escorting is conducting", false, swEscorting, "CONDUCTING"},
		{"paused", false, swPaused, "PAUSED"},
		{"standby", true, swParked, "STANDBY"},
	}
	for _, c := range cases {
		if got := swModeBadge(c.standby, c.phase); !strings.Contains(got, c.want) {
			t.Errorf("%s: swModeBadge = %q, want it to contain %q", c.name, got, c.want)
		}
	}
}

func TestSwModelViewShowsModeBadge(t *testing.T) {
	m := swTestModel()
	if view := m.View(); !strings.Contains(view, "CONDUCTING") {
		t.Errorf("view missing CONDUCTING badge:\n%s", view)
	}
	m.standby = true
	if view := m.View(); !strings.Contains(view, "STANDBY") {
		t.Errorf("standby view missing STANDBY badge:\n%s", view)
	}
	m.standby = false
	m.cond.phase = swPaused
	if view := m.View(); !strings.Contains(view, "PAUSED") {
		t.Errorf("paused view missing PAUSED badge:\n%s", view)
	}
}

// A session's project color tints its name cell, so a row is identifiable by
// the same color as its tmux status bar and terminal tab.
func TestSwNameStyleTintsWithProjectColor(t *testing.T) {
	got := swNameStyle("b34dff", false).GetForeground()
	if want := lipgloss.Color("#b34dff"); got != want {
		t.Errorf("foreground = %v, want %v", got, want)
	}
}

// @claudemux_color is a tmux user option: anything can write it. A value that
// is not a bare 6-digit hex must render as a plain name rather than reaching
// lipgloss, which would emit a broken escape sequence into the row.
func TestSwNameStyleRejectsNonHexColor(t *testing.T) {
	for _, in := range []string{"", "purple", "#b34dff", "b34df", "b34dfff", "zzzzzz"} {
		if fg := swNameStyle(in, false).GetForeground(); fg != lipgloss.TerminalColor(lipgloss.NoColor{}) {
			t.Errorf("swNameStyle(%q) foreground = %v, want none", in, fg)
		}
	}
}

// Selection must stay legible on every project color, so the highlight wins
// outright rather than compositing with a tint.
func TestSwNameStyleSelectionBeatsProjectColor(t *testing.T) {
	if !swNameStyle("b34dff", true).GetReverse() {
		t.Error("selected name lost its reverse highlight")
	}
	if fg := swNameStyle("b34dff", true).GetForeground(); fg != lipgloss.TerminalColor(lipgloss.NoColor{}) {
		t.Errorf("selected name foreground = %v, want none", fg)
	}
}
