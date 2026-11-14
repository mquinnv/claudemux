package main

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func completeRows() []fleetRow {
	return []fleetRow{
		{Name: "phenix", Created: "100"},
		{Name: "phenix-2", Created: "200"},
		{Name: "remix", Created: "300"},
	}
}

func TestDeferCandidatesChildrenFirst(t *testing.T) {
	rows := []fleetRow{
		{Name: "phenix"},
		{Name: "beejax"},
		{Name: "beejax-platform", Parent: "beejax"},
		{Name: "remix"},
	}
	got := deferCandidates("beejax", rows)
	want := []string{"beejax-platform", "phenix", "remix"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deferCandidates = %v, want %v", got, want)
	}
}

func TestDeferCompleterPrefix(t *testing.T) {
	cands := []string{"phenix", "phenix-2", "remix"}
	var c deferCompleter
	in := c.next("ph", cands, +1)
	if in != "phenix" {
		t.Fatalf("first tab = %q, want phenix", in)
	}
	if in = c.next(in, cands, +1); in != "phenix-2" {
		t.Fatalf("second tab = %q, want phenix-2", in)
	}
	if in = c.next(in, cands, +1); in != "phenix" {
		t.Fatalf("third tab = %q, want a wrap to phenix", in)
	}
	var back deferCompleter
	if got := back.next("ph", cands, -1); got != "phenix-2" {
		t.Errorf("shift+tab from the start = %q, want phenix-2", got)
	}
}

func TestDeferCompleterCaseInsensitive(t *testing.T) {
	var c deferCompleter
	if got := c.next("PH", []string{"phenix", "remix"}, +1); got != "phenix" {
		t.Errorf("tab on PH = %q, want phenix", got)
	}
}

func TestDeferCompleterEmpty(t *testing.T) {
	cands := []string{"phenix", "phenix-2", "remix"}
	var c deferCompleter
	in := ""
	for i, want := range append(cands, cands[0]) {
		if in = c.next(in, cands, +1); in != want {
			t.Fatalf("tab %d = %q, want %q", i+1, in, want)
		}
	}
}

func TestDeferCompleterNoMatch(t *testing.T) {
	cands := []string{"phenix", "remix"}
	var c deferCompleter
	if got := c.next("zz", cands, +1); got != "zz" {
		t.Fatalf("tab on zz = %q, want zz unchanged", got)
	}
	if got := c.next("ph", cands, +1); got != "phenix" {
		t.Errorf("tab on ph after a miss = %q, want phenix (the miss must not start a cycle)", got)
	}
}

func TestDeferCompleterResetOnEdit(t *testing.T) {
	cands := []string{"phenix", "phenix-2", "remix"}
	var c deferCompleter
	in := c.next("ph", cands, +1)
	c.reset()
	if in = c.next(in, cands, +1); in != "phenix" {
		t.Fatalf("tab after reset = %q, want phenix (base is now phenix)", in)
	}
	if in = c.next(in, cands, +1); in != "phenix-2" {
		t.Errorf("second tab after reset = %q, want phenix-2", in)
	}
}

func TestDeferLinkForExact(t *testing.T) {
	rows := completeRows()
	if got, want := deferLinkFor("phenix", "beejax", rows), (deferLink{On: "phenix", Created: "100"}); got != want {
		t.Errorf("deferLinkFor(phenix) = %+v, want %+v", got, want)
	}
	for _, in := range []string{"phenix-2: note", "Phenix", "beejax", "gone"} {
		self := "beejax"
		rs := append(rows, fleetRow{Name: "beejax", Created: "400"})
		if got := deferLinkFor(in, self, rs); got != (deferLink{}) {
			t.Errorf("deferLinkFor(%q) = %+v, want no link", in, got)
		}
	}
	if got := deferLinkFor("remix", "beejax", []fleetRow{{Name: "remix", Created: "0"}}); got != (deferLink{}) {
		t.Errorf("deferLinkFor with an unknown creation stamp = %+v, want no link", got)
	}
}

func TestDeferPromptTextLinked(t *testing.T) {
	if got := deferPromptText("phenix", true); !strings.Contains(got, "↳ links") {
		t.Errorf("deferPromptText(linked) = %q, want the link marker", got)
	}
	if got := deferPromptText("phenix", false); strings.Contains(got, "↳ links") {
		t.Errorf("deferPromptText(unlinked) = %q, want no link marker", got)
	}
}

func TestFleetFromSnapshot(t *testing.T) {
	got := fleetFromSnapshot([]swSession{
		{Name: "a", Created: 42, State: "Idle", Deferred: true, Parent: "p"},
		{Name: "b"},
	})
	want := []fleetRow{
		{Name: "a", Created: "42", State: "Idle", Deferred: true, Parent: "p"},
		{Name: "b", Created: "0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fleetFromSnapshot = %+v, want %+v", got, want)
	}
	if deferCandidates("x", got)[1] != "b" {
		t.Error("a row with no creation stamp must still be offered")
	}
}

// A fresh defer always writes the "1" mark, linked or not.
func TestDeferSubmitCmdFresh(t *testing.T) {
	writes := captureDeferWrites(t)
	rows := completeRows()
	got := runDeferCmd(t, writes, deferSubmitCmd("%1", "", "phenix ", "beejax", rows))
	want := deferLinkedArgs("%1", deferOnValue, "phenix ", deferLink{On: "phenix", Created: "100"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fresh linked = %v, want %v", got, want)
	}
	got = runDeferCmd(t, writes, deferSubmitCmd("%1", "", "CI", "beejax", rows))
	if want := deferLinkedArgs("%1", deferOnValue, "CI", deferLink{}); !reflect.DeepEqual(got, want) {
		t.Errorf("fresh free text = %v, want %v", got, want)
	}
}

// Editing a defer's blocker to free text touches the reason alone; editing it
// to a session's exact name relinks and keeps the mark's value.
func TestDeferSubmitCmdEdit(t *testing.T) {
	writes := captureDeferWrites(t)
	rows := completeRows()
	got := runDeferCmd(t, writes, deferSubmitCmd("%1", deferWaitValue, "waiting on phenix-2: ship it", "beejax", rows))
	if want := deferReasonArgs("%1", "waiting on phenix-2: ship it"); !reflect.DeepEqual(got, want) {
		t.Errorf("edit to free text = %v, want %v", got, want)
	}
	for _, o := range []string{deferOption, deferOnOption, deferOnCreatedOption, deferOnSeenOption} {
		if _, set, unset := argvSets(got, o); set || unset {
			t.Errorf("free-text edit touched %s: %v", o, got)
		}
	}
	got = runDeferCmd(t, writes, deferSubmitCmd("%1", deferWaitValue, "phenix-2", "beejax", rows))
	want := deferLinkedArgs("%1", deferWaitValue, "phenix-2", deferLink{On: "phenix-2", Created: "200"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edit to a session = %v, want %v", got, want)
	}
}

// captureDeferWrites swaps the defer tmux runner for one that records argv,
// so a test can run the cmd a prompt returned and see the write it makes.
func captureDeferWrites(t *testing.T) *[][]string {
	t.Helper()
	var got [][]string
	prev := deferTmuxRun
	deferTmuxRun = func(args []string) { got = append(got, args) }
	t.Cleanup(func() { deferTmuxRun = prev })
	return &got
}

// runDeferCmd runs cmd and returns the one tmux write it made.
func runDeferCmd(t *testing.T, writes *[][]string, cmd tea.Cmd) []string {
	t.Helper()
	if cmd == nil {
		t.Fatal("no cmd, want a defer write")
	}
	*writes = nil
	cmd()
	if len(*writes) != 1 {
		t.Fatalf("writes = %v, want exactly one", *writes)
	}
	return (*writes)[0]
}

// headCompleteModel is a head whose fleet is completeRows, each publishing a
// claudemux state as a real session's head does, plus its own session.
func headCompleteModel(mark deferMark) model {
	var fleet []fleetRow
	for _, r := range append(completeRows(), fleetRow{Name: "beejax", Created: "50"}) {
		r.State = "Idle"
		fleet = append(fleet, r)
	}
	return model{ready: true, width: 500, height: 4, selfPane: "%1", deferMark: mark,
		sessionName: "beejax", fleetOK: true, fleet: fleet}
}

// The head offers and links only what the lobby lists: sessions that publish
// a claudemux state. The lobby's own session and plain tmux sessions publish
// none.
func TestHeadDeferPromptClaudemuxSessionsOnly(t *testing.T) {
	writes := captureDeferWrites(t)
	hm := headCompleteModel(deferMark{})
	hm.fleet = append(hm.fleet,
		fleetRow{Name: "claudemux", Created: "10"},
		fleetRow{Name: "phlog", Created: "20"})
	var m tea.Model = hm
	for _, k := range []tea.KeyMsg{keyRunes("d"), keyRunes("phl"), keyTab} {
		m, _ = m.Update(k)
	}
	if got := m.(model).deferInput; got != "phl" {
		t.Fatalf("tab on a plain tmux session's prefix = %q, want phl untouched", got)
	}
	var seen []string
	m, _ = m.Update(keyBack)
	m, _ = m.Update(keyBack)
	m, _ = m.Update(keyBack)
	for range 5 {
		m, _ = m.Update(keyTab)
		seen = append(seen, m.(model).deferInput)
	}
	for _, s := range seen {
		if s == "claudemux" || s == "phlog" {
			t.Errorf("tab on an empty line offered %q; cycle = %v", s, seen)
		}
	}
	m = hm
	for _, k := range []tea.KeyMsg{keyRunes("d"), keyRunes("phlog")} {
		m, _ = m.Update(k)
	}
	if line := ansi.Strip(m.(model).deferChipText()); strings.Contains(line, "↳ links") {
		t.Errorf("prompt = %q, want no link marker for a non-claudemux session", line)
	}
	_, cmd := m.Update(keyEnter)
	if got, want := runDeferCmd(t, writes, cmd), deferLinkedArgs("%1", deferOnValue, "phlog", deferLink{}); !reflect.DeepEqual(got, want) {
		t.Errorf("enter wrote %v, want the plain defer %v", got, want)
	}
}

var (
	keyTab      = tea.KeyMsg{Type: tea.KeyTab}
	keyShiftTab = tea.KeyMsg{Type: tea.KeyShiftTab}
	keyBack     = tea.KeyMsg{Type: tea.KeyBackspace}
	keyEnter    = tea.KeyMsg{Type: tea.KeyEnter}
)

func TestHeadDeferPromptTabCompletes(t *testing.T) {
	writes := captureDeferWrites(t)
	var m tea.Model = headCompleteModel(deferMark{})
	for _, k := range []tea.KeyMsg{keyRunes("d"), keyRunes("ph"), keyTab} {
		m, _ = m.Update(k)
	}
	if got := m.(model).deferInput; got != "phenix" {
		t.Fatalf("after tab deferInput = %q, want phenix", got)
	}
	if line := ansi.Strip(m.(model).deferChipText()); !strings.Contains(line, "phenix▌ ↳ links") {
		t.Errorf("prompt = %q, want the link marker for an exact name", line)
	}
	m, _ = m.Update(keyTab)
	if got := m.(model).deferInput; got != "phenix-2" {
		t.Fatalf("second tab = %q, want phenix-2", got)
	}
	// An edit resets the cycle: tab now completes from "phenix-", not onward
	// from phenix-2 (which would wrap to phenix).
	m, _ = m.Update(keyBack)
	m, _ = m.Update(keyTab)
	if got := m.(model).deferInput; got != "phenix-2" {
		t.Fatalf("tab after an edit = %q, want phenix-2 (cycle restarted from phenix-)", got)
	}
	m, _ = m.Update(keyShiftTab)
	m, _ = m.Update(keyBack)
	m, _ = m.Update(keyBack)
	if got := m.(model).deferInput; got != "phenix" {
		t.Fatalf("deferInput = %q, want phenix", got)
	}
	m, cmd := m.Update(keyEnter)
	got := runDeferCmd(t, writes, cmd)
	want := deferLinkedArgs("%1", deferOnValue, "phenix", deferLink{On: "phenix", Created: "100"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("enter wrote %v, want %v", got, want)
	}
	if reason, link := deferSubmit("phenix", "beejax", m.(model).fleet); reason != "phenix" || link.On != "phenix" {
		t.Errorf("deferSubmit = %q %+v, want phenix's link", reason, link)
	}
}

// Without its own session name or a good listing the head cannot tell itself
// from a candidate: Tab does nothing and enter makes a plain defer.
func TestHeadDeferPromptNoScope(t *testing.T) {
	writes := captureDeferWrites(t)
	for _, mut := range []func(*model){
		func(m *model) { m.sessionName = "" },
		func(m *model) { m.fleetOK = false },
	} {
		hm := headCompleteModel(deferMark{})
		mut(&hm)
		var m tea.Model = hm
		for _, k := range []tea.KeyMsg{keyRunes("d"), keyRunes("phenix"), keyTab} {
			m, _ = m.Update(k)
		}
		if got := m.(model).deferInput; got != "phenix" {
			t.Fatalf("deferInput = %q, want phenix untouched by tab", got)
		}
		_, cmd := m.Update(keyEnter)
		if got, want := runDeferCmd(t, writes, cmd), deferLinkedArgs("%1", deferOnValue, "phenix", deferLink{}); !reflect.DeepEqual(got, want) {
			t.Errorf("enter wrote %v, want the plain defer %v", got, want)
		}
	}
}

// D on a deferred session: free text rewrites the reason alone; a session's
// exact name relinks with the mark's own value.
func TestHeadDeferEditKeepsMarkAndLink(t *testing.T) {
	writes := captureDeferWrites(t)
	mark := deferMark{Raw: deferWaitValue, Reason: "waiting on phenix: goal", Link: deferLink{"phenix", "100"}, Seen: true}
	var m tea.Model = headCompleteModel(mark)
	m, _ = m.Update(keyRunes("D"))
	m, _ = m.Update(keyRunes("!"))
	_, cmd := m.Update(keyEnter)
	if got, want := runDeferCmd(t, writes, cmd), deferReasonArgs("%1", "waiting on phenix: goal!"); !reflect.DeepEqual(got, want) {
		t.Errorf("free-text edit wrote %v, want %v", got, want)
	}

	m = headCompleteModel(mark)
	m, _ = m.Update(keyRunes("D"))
	hm := m.(model)
	hm.deferInput = "remix"
	_, cmd = hm.Update(keyEnter)
	want := deferLinkedArgs("%1", deferWaitValue, "remix", deferLink{"remix", "300"})
	if got := runDeferCmd(t, writes, cmd); !reflect.DeepEqual(got, want) {
		t.Errorf("relink wrote %v, want %v", got, want)
	}
}

func lobbyCompleteModel() swModel {
	m := swTestModel()
	for i := range m.snap.Sessions {
		m.snap.Sessions[i].Created = int64(100 * (i + 1))
	}
	m.snap.Sessions[2].Parent = "web"
	m.sel = 1
	return m
}

func TestLobbyDeferPromptTabCompletes(t *testing.T) {
	writes := captureDeferWrites(t)
	var m tea.Model = lobbyCompleteModel()
	m, _ = m.Update(keyRunes("d"))
	var seen []string
	for i := 0; i < 3; i++ {
		m, _ = m.Update(keyTab)
		seen = append(seen, m.(swModel).deferInput)
	}
	if want := []string{"scratch", "api", "scratch"}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("tab cycle = %v, want %v (child first, web itself never)", seen, want)
	}
	m, _ = m.Update(keyTab)
	if view := ansi.Strip(m.View()); !strings.Contains(view, "◆ blocker: api▌ ↳ links") {
		t.Errorf("status line missing the link marker:\n%s", view)
	}
	_, cmd := m.Update(keyEnter)
	want := deferLinkedArgs("web:", deferOnValue, "api", deferLink{"api", "100"})
	if got := runDeferCmd(t, writes, cmd); !reflect.DeepEqual(got, want) {
		t.Errorf("enter wrote %v, want %v", got, want)
	}
}

func TestLobbyDeferEditKeepsMarkAndLink(t *testing.T) {
	writes := captureDeferWrites(t)
	base := lobbyCompleteModel()
	base.snap.Sessions[1].Deferred = true
	base.snap.Sessions[1].DeferWait = true
	base.snap.Sessions[1].DeferReason = "waiting on api: goal"

	var m tea.Model = base
	m, _ = m.Update(keyRunes("D"))
	m, _ = m.Update(keyRunes("!"))
	_, cmd := m.Update(keyEnter)
	if got, want := runDeferCmd(t, writes, cmd), deferReasonArgs("web:", "waiting on api: goal!"); !reflect.DeepEqual(got, want) {
		t.Errorf("free-text edit wrote %v, want %v", got, want)
	}

	m, _ = tea.Model(base).Update(keyRunes("D"))
	sm := m.(swModel)
	sm.deferInput = "api"
	_, cmd = sm.Update(keyEnter)
	want := deferLinkedArgs("web:", deferWaitValue, "api", deferLink{"api", "100"})
	if got := runDeferCmd(t, writes, cmd); !reflect.DeepEqual(got, want) {
		t.Errorf("relink wrote %v, want %v", got, want)
	}
}
