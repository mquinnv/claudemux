package main

import (
	"errors"
	"testing"
	"time"
)

// The head-side wiring of the defer watcher, driven through Update the way the
// poll drives it. selfPane stays "" so no publish command competes with the
// watcher's: every non-nil cmd these tests see is the watcher's own. The mark
// read still reports ok, as it would inside tmux.

var phenixLink = deferLink{On: "phenix-2", Created: "100"}

func linkedMark(seen bool) deferMark {
	return deferMark{Raw: deferOnValue, Reason: "waiting on phenix-2", Link: phenixLink, Seen: seen}
}

func blockerFleet(state string) []fleetRow {
	return []fleetRow{
		{Name: "self", Created: "50", State: "Idle", Deferred: true},
		{Name: "phenix-2", Created: "100", State: state},
	}
}

// deferPoll is one poll's dataMsg carrying the defer reads. The rate-limit
// error keeps the credit refresh from issuing a command of its own.
func deferPoll(at time.Time, mark deferMark, fleet []fleetRow, fleetOK bool) dataMsg {
	return dataMsg{
		time: at, deferMark: mark, deferMarkOK: true, fleet: fleet, fleetOK: fleetOK,
		rateLimitErr: errors.New("no rate limits in this test"),
	}
}

// watchedModel is an idle head whose state is already published, so a poll
// that changes nothing issues nothing.
func watchedModel(mark deferMark) model {
	return model{deferMark: mark, deferAutoNudge: true, publishedState: "Idle"}
}

func TestDeferWatchSkipsFailedRead(t *testing.T) {
	mark := linkedMark(true)
	m := watchedModel(mark)
	for _, at := range []time.Time{t0, t0.Add(30 * time.Second)} {
		next, cmd := m.Update(deferPoll(at, mark, nil, false))
		m = next.(model)
		if cmd != nil {
			t.Fatalf("poll at %v returned a cmd; an unreadable fleet must never release", at)
		}
	}
	// A failed mark read keeps the last good mark rather than a zero one.
	msg := deferPoll(t0.Add(60*time.Second), deferMark{}, blockerFleet("Idle"), true)
	msg.deferMarkOK = false
	next, cmd := m.Update(msg)
	m = next.(model)
	if cmd != nil {
		t.Fatal("a failed mark read returned a cmd")
	}
	if m.deferMark != mark {
		t.Errorf("deferMark = %+v, want the last good %+v", m.deferMark, mark)
	}
	if m.deferDoneNote != "" || m.deferRelease != watchNone {
		t.Errorf("note = %q release = %v, want nothing released", m.deferDoneNote, m.deferRelease)
	}
}

// releasedModel runs a linked, seen defer through the grace period against a
// stopped blocker and returns the head just after it issued the release.
func releasedModel(t *testing.T) model {
	t.Helper()
	mark := linkedMark(true)
	m := watchedModel(mark)
	next, cmd := m.Update(deferPoll(t0, mark, blockerFleet("Idle"), true))
	m = next.(model)
	if cmd != nil {
		t.Fatal("first poll returned a cmd; the grace period must run first")
	}
	next, cmd = m.Update(deferPoll(t0.Add(21*time.Second), mark, blockerFleet("Idle"), true))
	m = next.(model)
	if cmd == nil {
		t.Fatal("poll after the grace period returned no cmd, want the release")
	}
	return m
}

func TestHeadReleasesWhenBlockerStops(t *testing.T) {
	m := releasedModel(t)
	if want := deferDoneChip("phenix-2", false); m.deferDoneNote != want {
		t.Errorf("deferDoneNote = %q, want %q", m.deferDoneNote, want)
	}
	if m.deferRelease != watchClearNudge {
		t.Errorf("deferRelease = %v, want watchClearNudge (idle, nudge on)", m.deferRelease)
	}
	// The mark still reads set while the release is in flight: no second one.
	for _, d := range []time.Duration{22 * time.Second, 45 * time.Second} {
		next, cmd := m.Update(deferPoll(t0.Add(d), linkedMark(true), blockerFleet("Idle"), true))
		m = next.(model)
		if cmd != nil {
			t.Fatalf("poll at +%v issued a second release while one was in flight", d)
		}
	}
	// Once a poll shows the mark unset, the watcher is live again.
	next, _ := m.Update(deferPoll(t0.Add(46*time.Second), deferMark{}, blockerFleet("Idle"), true))
	if got := next.(model).deferRelease; got != watchNone {
		t.Errorf("deferRelease = %v after the mark read unset, want watchNone", got)
	}
}

func TestHeadReleasesWithoutNudgeWhenDisabled(t *testing.T) {
	mark := linkedMark(true)
	m := watchedModel(mark)
	m.deferAutoNudge = false
	next, _ := m.Update(deferPoll(t0, mark, blockerFleet("Idle"), true))
	next, cmd := next.(model).Update(deferPoll(t0.Add(21*time.Second), mark, blockerFleet("Idle"), true))
	if cmd == nil || next.(model).deferRelease != watchClear {
		t.Errorf("cmd = %v release = %v, want a clear without a nudge", cmd != nil, next.(model).deferRelease)
	}
}

func TestHeadFailedClearRetriesWithoutNudge(t *testing.T) {
	m := releasedModel(t)
	next, _ := m.Update(deferReleasedMsg{nudged: true, cleared: false})
	m = next.(model)
	if m.deferRelease != watchNone || !m.deferNudgeSpent {
		t.Fatalf("release = %v spent = %v, want the watcher live again and the nudge spent", m.deferRelease, m.deferNudgeSpent)
	}
	mark := linkedMark(true)
	next, _ = m.Update(deferPoll(t0.Add(30*time.Second), mark, blockerFleet("Idle"), true))
	next, cmd := next.(model).Update(deferPoll(t0.Add(51*time.Second), mark, blockerFleet("Idle"), true))
	m = next.(model)
	if cmd == nil {
		t.Fatal("no retry after the failed clear")
	}
	if m.deferRelease != watchClear {
		t.Errorf("deferRelease = %v, want watchClear: the nudge was already typed once", m.deferRelease)
	}
	// The spent nudge is forgotten once the mark reads unset.
	next, _ = m.Update(deferPoll(t0.Add(52*time.Second), deferMark{}, nil, true))
	if next.(model).deferNudgeSpent {
		t.Error("deferNudgeSpent survived the mark being unset")
	}
}

func TestHeadDoneNoteClearsOnNextTurn(t *testing.T) {
	m := releasedModel(t)
	msg := deferPoll(t0.Add(25*time.Second), deferMark{}, blockerFleet("Idle"), true)
	msg.newEvents = []Event{{Type: "assistant", Timestamp: t0.Format(time.RFC3339), ToolUses: []ToolUse{{ID: "t1", Name: "Bash"}}}}
	next, _ := m.Update(msg)
	m = next.(model)
	if teardownTurnEnded(m.state.Kind) {
		t.Fatalf("precondition: state = %v, want mid-turn", m.state.Kind)
	}
	if m.deferDoneNote != "" {
		t.Errorf("deferDoneNote = %q, want it emptied when the next turn starts", m.deferDoneNote)
	}
}

func TestHeadMarksSeen(t *testing.T) {
	mark := linkedMark(false)
	m := watchedModel(mark)
	next, cmd := m.Update(deferPoll(t0, mark, blockerFleet("Thinking"), true))
	m = next.(model)
	if cmd == nil {
		t.Fatal("no cmd, want the seen flag set")
	}
	if m.deferRelease != watchNone || m.deferDoneNote != "" {
		t.Errorf("release = %v note = %q, want no release for a working blocker", m.deferRelease, m.deferDoneNote)
	}
}

func TestDeferChipTextShowsDoneNote(t *testing.T) {
	note := deferDoneChip("phenix-2", true)
	m := model{deferDoneNote: note}
	if got := m.deferChipText(); got != note {
		t.Errorf("deferChipText() = %q, want the done note %q", got, note)
	}
	m.deferMark = deferMark{Raw: deferOnValue}
	if got := m.deferChipText(); got != deferChip(deferOnValue, "") {
		t.Errorf("deferChipText() = %q, want the defer chip while a mark is set", got)
	}
	m.deferMark, m.deferPrompting = deferMark{}, true
	if got := m.deferChipText(); got != swDeferStyle.Render(deferPromptText("", false)) {
		t.Errorf("deferChipText() = %q, want the blocker prompt while it is open", got)
	}
}

func TestDeferOwnState(t *testing.T) {
	cases := []struct {
		s             State
		idle, working bool
	}{
		{State{Kind: StateIdle}, true, false},
		{State{Kind: StateThinking}, false, true},
		{State{Kind: StateTool, ToolName: "Bash"}, false, true},
		{State{Kind: StateCompacting}, false, true},
		{State{Kind: StateTool, ToolName: "AskUserQuestion"}, false, false},
		{State{Kind: StateAsking}, false, false},
		{State{Kind: StateAwaiting}, false, false},
		{State{Kind: StateWaiting}, false, false},
		{State{Kind: StateBackground, BgCount: 1}, false, false},
		{State{Kind: StateUnsure, BgCount: 1}, false, false},
	}
	for _, c := range cases {
		idle, working := deferOwnState(c.s)
		if idle != c.idle || working != c.working {
			t.Errorf("%s: idle = %v working = %v, want %v %v", statePublishValue(c.s), idle, working, c.idle, c.working)
		}
	}
}
