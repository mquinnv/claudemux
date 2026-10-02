package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A permission dialog is the transcript's other blind spot. Unlike a pending
// AskUserQuestion the tool_use IS flushed, so classifyState reads an honest
// "Tool: X" — and keeps reading it for as long as the dialog sits unanswered,
// which looks exactly like a tool that is working.

// permitDialogCapture is a real capture of a claude pane sitting on an MCP
// permission dialog (claude 2.1.287), trimmed to its tail.
const permitDialogCapture = `⏺ Calling betterstack 2 times…

─────────────────────────────────────────────────────────────────────
 Tool use

   betterstack — Get chart alert instructions Tool: (MCP)

   About the betterstack — Get chart alert instructions Tool:
   │ Get instructions for creating and configuring chart alerts, including alert types,
   (ctrl+o to expand description)

 Do you want to proceed?
 ❯ 1. Yes
   2. No

 Esc to cancel · Tab to amend
`

// permitRunningCapture is the same pane once the dialog was answered and the
// tool is running: the prompt box and statusline are back.
const permitRunningCapture = `⏺ Calling betterstack 2 times…

✻ Cogitating… (12s · esc to interrupt)

─────────────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────────────
  opus 5.5 · ctx 25%
`

func TestPermissionDialogUp(t *testing.T) {
	if !permissionDialogUp(permitDialogCapture) {
		t.Error("a pane sitting on a permission dialog read as no dialog")
	}
	// tmux pads a capture with blank lines down to the pane height.
	if !permissionDialogUp(permitDialogCapture + "\n\n\n\n\n\n\n\n\n\n") {
		t.Error("trailing blank lines hid the dialog")
	}
	if permissionDialogUp(permitRunningCapture) {
		t.Error("a pane with the tool running read as a dialog")
	}
	if permissionDialogUp("") {
		t.Error("an empty capture read as a dialog")
	}
}

// The footer only counts at the bottom of the pane: the same words scrolled
// up into the conversation (claude quoting its own UI, a pasted log) are not
// a dialog.
func TestPermissionDialogUpIgnoresScrollback(t *testing.T) {
	capture := " Esc to cancel · Tab to amend\n" + permitRunningCapture
	if permissionDialogUp(capture) {
		t.Error("dialog footer in the scrollback read as a live dialog")
	}
}

func TestReadPermitMarker(t *testing.T) {
	dir := t.TempDir()
	if mk := readPermitMarker(dir, "sess-a"); !mk.At.IsZero() {
		t.Errorf("no marker: got %+v, want zero", mk)
	}
	path := filepath.Join(dir, "sess-a.permit.json")
	if err := os.WriteFile(path, []byte(`{"session_id":"sess-a","tool_name":"Bash"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mk := readPermitMarker(dir, "sess-a")
	if !mk.At.Equal(fi.ModTime()) || mk.Tool != "Bash" {
		t.Errorf("got %+v, want At=%v Tool=Bash", mk, fi.ModTime())
	}
	if mk := readPermitMarker("", "sess-a"); !mk.At.IsZero() {
		t.Errorf("empty dir: got %+v, want zero", mk)
	}
	if mk := readPermitMarker(dir, ""); !mk.At.IsZero() {
		t.Errorf("empty session: got %+v, want zero", mk)
	}
}

// A marker whose body does not parse still marks the instant; only the tool
// name is lost.
func TestReadPermitMarkerUnparseableKeepsTime(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sess-a.permit.json"), []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	mk := readPermitMarker(dir, "sess-a")
	if mk.At.IsZero() || mk.Tool != "" {
		t.Errorf("got %+v, want a time and no tool", mk)
	}
}

func TestPermitOverrideUpgradesToAwaiting(t *testing.T) {
	now := time.Now()
	mk := permitMarker{At: now.Add(-time.Minute), Tool: "mcp__betterstack__chart_alert_help"}
	// Tool is the common case; the others cover a dialog raised by a
	// background agent while the main thread is between turns.
	for _, kind := range []StateKind{StateTool, StateThinking, StateIdle, StateBackground, StateUnsure} {
		got := permitOverride(State{Kind: kind, ToolName: "x", Since: now}, mk, true)
		if got.Kind != StateAwaiting {
			t.Errorf("kind %v: got %v, want StateAwaiting", kind, got.Kind)
			continue
		}
		if got.ToolName != mk.Tool {
			t.Errorf("kind %v: ToolName = %q, want the tool the dialog is about", kind, got.ToolName)
		}
		if !got.Since.Equal(mk.At) || !got.Anchored {
			t.Errorf("kind %v: Since = %v anchored=%v, want the marker time, anchored — the age column must show how long the dialog has been waiting", kind, got.Since, got.Anchored)
		}
	}
}

// The marker alone proves only that a dialog was ABOUT to open. Without the
// pane confirming it is still up, an approved ten-minute build would read as
// waiting on the human for all ten minutes.
func TestPermitOverrideNeedsConfirmation(t *testing.T) {
	s := State{Kind: StateTool, ToolName: "Bash", Since: time.Now()}
	mk := permitMarker{At: time.Now(), Tool: "Bash"}
	if got := permitOverride(s, mk, false); got != s {
		t.Errorf("unconfirmed: got %+v, want unchanged", got)
	}
	if got := permitOverride(s, permitMarker{}, true); got != s {
		t.Errorf("no marker: got %+v, want unchanged", got)
	}
}

func TestPermitOverrideLeavesSpecificStatesAlone(t *testing.T) {
	mk := permitMarker{At: time.Now(), Tool: "Bash"}
	for _, kind := range []StateKind{StateAsking, StateError, StateCompacting, StateWaiting} {
		s := State{Kind: kind, Since: time.Now()}
		if got := permitOverride(s, mk, true); got != s {
			t.Errorf("kind %v: got %+v, want unchanged", kind, got)
		}
	}
}

func TestAwaitingLabelAndPublish(t *testing.T) {
	s := State{Kind: StateAwaiting, ToolName: "Bash"}
	if got := s.Label(); got != "Permission: Bash" {
		t.Errorf("Label = %q, want %q", got, "Permission: Bash")
	}
	if got := (State{Kind: StateAwaiting}).Label(); got != "Permission" {
		t.Errorf("Label without a tool = %q, want %q", got, "Permission")
	}
	if got := statePublishValue(s); got != "Awaiting" {
		t.Errorf("statePublishValue = %q, want Awaiting", got)
	}
	if !isWaiting("Awaiting") {
		t.Error("isWaiting(Awaiting) = false — a session blocked on a permission dialog is waiting on the human")
	}
	if interruptedState("Awaiting") {
		t.Error("interruptedState(Awaiting) = true — a dialog is waiting on the user, not working")
	}
}

// --- permitWatch: which marker the pane has confirmed ---

func TestPermitWatchConfirmsOnlyAfterSeeingTheDialog(t *testing.T) {
	now := time.Now()
	mk := permitMarker{At: now, Tool: "Bash"}
	var w permitWatch
	if w.confirmed(mk) {
		t.Fatal("confirmed before any probe")
	}
	if !w.probeDue(mk, "%1", now) {
		t.Fatal("no probe due for a brand-new marker")
	}
	w.probing = true
	if w.probeDue(mk, "%1", now) {
		t.Error("probe due while one is already in flight")
	}
	w.observe(mk.At, true, true)
	if !w.confirmed(mk) {
		t.Error("not confirmed after the pane showed the dialog")
	}
	if !w.probeDue(mk, "%1", now.Add(time.Hour)) {
		t.Error("a dialog that is up must keep being probed, however old — that is how its answer is noticed")
	}
}

// Once the dialog was seen and then left, the marker is spent: the tool was
// approved and is running, or denied. No hook fires on either, so the pane is
// the only witness.
func TestPermitWatchDialogAnsweredRetiresTheMarker(t *testing.T) {
	now := time.Now()
	mk := permitMarker{At: now, Tool: "Bash"}
	var w permitWatch
	w.probeDue(mk, "%1", now)
	w.observe(mk.At, true, true)
	w.observe(mk.At, false, true)
	if w.confirmed(mk) {
		t.Error("still confirmed after the dialog left the pane")
	}
	if w.probeDue(mk, "%1", now.Add(time.Second)) {
		t.Error("a spent marker is still being probed")
	}
	// The next dialog writes a new marker, which starts over.
	next := permitMarker{At: now.Add(5 * time.Second), Tool: "Bash"}
	if !w.probeDue(next, "%1", now.Add(5*time.Second)) {
		t.Error("the next dialog's marker is not probed")
	}
	w.observe(next.At, true, true)
	if !w.confirmed(next) {
		t.Error("the next dialog's marker is not confirmed")
	}
}

// The hook fires BEFORE the dialog renders (a PermissionRequest hook may
// decide in the dialog's place), so the first probes can miss it. They must
// not retire the marker — only give up after the window.
func TestPermitWatchEarlyMissDoesNotRetire(t *testing.T) {
	now := time.Now()
	mk := permitMarker{At: now, Tool: "Bash"}
	var w permitWatch
	w.probeDue(mk, "%1", now)
	w.observe(mk.At, false, true)
	if !w.probeDue(mk, "%1", now.Add(2*time.Second)) {
		t.Fatal("gave up on the first miss")
	}
	w.observe(mk.At, true, true)
	if !w.confirmed(mk) {
		t.Error("a dialog that rendered late was not confirmed")
	}
}

func TestPermitWatchGivesUpOnADialogThatNeverShows(t *testing.T) {
	now := time.Now()
	mk := permitMarker{At: now, Tool: "Bash"}
	var w permitWatch
	if !w.probeDue(mk, "%1", now) {
		t.Fatal("no first probe")
	}
	w.observe(mk.At, false, true)
	if w.probeDue(mk, "%1", now.Add(permitProbeWindow+time.Second)) {
		t.Error("still probing a marker whose dialog never appeared, past the window")
	}
}

// A head that starts (binwatch restarts every head on install) while a dialog
// has been up for hours must still find it: the window runs from when THIS
// process first saw the marker, not from the marker's own age.
func TestPermitWatchOldMarkerStillGetsProbedAfterRestart(t *testing.T) {
	now := time.Now()
	mk := permitMarker{At: now.Add(-3 * time.Hour), Tool: "Bash"}
	var w permitWatch
	if !w.probeDue(mk, "%1", now) {
		t.Fatal("an old marker this process has never probed is not probed")
	}
	w.observe(mk.At, true, true)
	if !w.confirmed(mk) {
		t.Error("an old, still-open dialog was not confirmed")
	}
}

func TestPermitWatchNoPaneNoMarkerNoProbe(t *testing.T) {
	now := time.Now()
	var w permitWatch
	if w.probeDue(permitMarker{At: now}, "", now) {
		t.Error("probe due with no claude pane to capture")
	}
	if w.probeDue(permitMarker{}, "%1", now) {
		t.Error("probe due with no marker")
	}
}

// A capture that failed says nothing either way.
func TestPermitWatchFailedProbeChangesNothing(t *testing.T) {
	now := time.Now()
	mk := permitMarker{At: now, Tool: "Bash"}
	var w permitWatch
	w.probeDue(mk, "%1", now)
	w.observe(mk.At, true, true)
	w.probing = true
	w.observe(mk.At, false, false)
	if !w.confirmed(mk) {
		t.Error("a failed capture retired a confirmed dialog")
	}
	if w.probing {
		t.Error("a failed capture left the in-flight flag set")
	}
}

// A probe result for a marker that has since been replaced is about a dialog
// that no longer exists.
func TestPermitWatchStaleProbeResultIgnored(t *testing.T) {
	now := time.Now()
	old := permitMarker{At: now, Tool: "Bash"}
	next := permitMarker{At: now.Add(time.Second), Tool: "Edit"}
	var w permitWatch
	w.probeDue(old, "%1", now)
	w.probeDue(next, "%1", now.Add(time.Second))
	w.observe(old.At, true, true)
	if w.confirmed(next) {
		t.Error("a result for the previous marker confirmed the new one")
	}
}

// The whole path through the head: a session whose transcript ends in an
// unresolved tool_use reads Tool; the hook's marker makes the tick probe the
// claude pane; the pane showing the dialog turns the state to Awaiting, which
// publishes as a waiting state; the dialog leaving turns it back to Tool.
func TestModelPermissionDialogReadsAwaitingWhileOnScreen(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	m := teardownTestModel()
	m.askDir = dir
	m.sessionID = "sess-1"
	m.claudePane = "%9"
	m.polling = true // keep the tick from issuing a real poll
	m.allEvents = []Event{{
		Type:      "assistant",
		Timestamp: now.Add(-time.Minute).Format(time.RFC3339),
		ToolUses:  []ToolUse{{ID: "toolu_1", Name: "mcp__betterstack__chart_alert_help"}},
	}}
	m.recomputeFromEvents(now)
	if m.state.Kind != StateTool {
		t.Fatalf("baseline state = %v, want StateTool", m.state.Kind)
	}

	next, _ := m.Update(tickMsg(now))
	if next.(model).permit.probing {
		t.Fatal("probed the pane with no marker")
	}

	marker := filepath.Join(dir, "sess-1.permit.json")
	if err := os.WriteFile(marker, []byte(`{"session_id":"sess-1","tool_name":"mcp__betterstack__chart_alert_help"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mk := readPermitMarker(dir, "sess-1")

	// The marker alone changes nothing: the dialog is not confirmed yet.
	m.recomputeFromEvents(now)
	if m.state.Kind != StateTool {
		t.Fatalf("state on an unconfirmed marker = %v, want StateTool", m.state.Kind)
	}

	next, cmd := m.Update(tickMsg(now))
	m = next.(model)
	if !m.permit.probing || cmd == nil {
		t.Fatal("a fresh marker did not make the tick probe the claude pane")
	}

	next, _ = m.Update(permitProbeMsg{at: mk.At, up: true, ok: true})
	m = next.(model)
	m.recomputeFromEvents(now)
	if m.state.Kind != StateAwaiting || m.state.ToolName != "mcp__betterstack__chart_alert_help" {
		t.Fatalf("state with the dialog on screen = %+v, want Awaiting naming the tool", m.state)
	}
	if !isWaiting(statePublishValue(m.state)) {
		t.Errorf("published %q is not a waiting state — the switchboard would still show this session as working", statePublishValue(m.state))
	}

	// Approved: the dialog is gone, the tool_use is still unresolved, the
	// tool is running.
	next, _ = m.Update(permitProbeMsg{at: mk.At, up: false, ok: true})
	m = next.(model)
	m.recomputeFromEvents(now)
	if m.state.Kind != StateTool {
		t.Errorf("state after the dialog was answered = %v, want StateTool", m.state.Kind)
	}
	next, _ = m.Update(tickMsg(now.Add(time.Second)))
	if next.(model).permit.probing {
		t.Error("still probing a dialog that was answered")
	}
}
