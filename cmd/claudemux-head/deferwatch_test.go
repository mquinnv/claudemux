package main

import (
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

var linkA = deferLink{On: "blocker", Created: "100"}

func TestParseFleet(t *testing.T) {
	listing := "a\t1\tIdle\t1\tp\n" +
		"broken line\n" +
		"b\t2\tThinking\twait\t\n" +
		"c\t3\tIdle\t\tq"
	want := []fleetRow{
		{Name: "a", Created: "1", State: "Idle", Deferred: true, Parent: "p"},
		{Name: "b", Created: "2", State: "Thinking", Deferred: true},
		{Name: "c", Created: "3", State: "Idle", Parent: "q"},
	}
	if got := parseFleet(listing); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestJudgeBlocker(t *testing.T) {
	row := func(state string, deferred bool) []fleetRow {
		return []fleetRow{{Name: "blocker", Created: "100", State: state, Deferred: deferred}}
	}
	cases := []struct {
		name string
		rows []fleetRow
		seen bool
		want blockerVerdict
	}{
		{"working", row("Thinking", false), false, blockerWorking},
		{"idle seen", row("Idle", false), true, blockerStopped},
		{"idle unseen", row("Idle", false), false, blockerHold},
		{"asking seen", row("Asking", false), true, blockerStopped},
		{"idle seen deferred", row("Idle", true), true, blockerHold},
		{"starting seen", row("Starting", false), true, blockerHold},
		{"absent", nil, true, blockerGone},
	}
	for _, c := range cases {
		if got := judgeBlocker(linkA, c.seen, c.rows, true); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestBlockerVerdictRecycledName(t *testing.T) {
	rows := []fleetRow{{Name: "blocker", Created: "999", State: "Thinking"}}
	if got := judgeBlocker(linkA, true, rows, true); got != blockerGone {
		t.Fatalf("got %v want blockerGone", got)
	}
}

func TestBlockerVerdictUnreadable(t *testing.T) {
	if got := judgeBlocker(linkA, true, nil, false); got != blockerHold {
		t.Fatalf("got %v want blockerHold", got)
	}
}

func stoppedIn(at time.Time) deferWatchInput {
	return deferWatchInput{
		Now:    at,
		Mark:   deferMark{Raw: "1", Link: linkA, Seen: true},
		MarkOK: true, Verdict: blockerStopped, NudgeEnabled: true,
	}
}

func TestDeferWatchGraceThenNudge(t *testing.T) {
	var w deferWatch
	for _, d := range []time.Duration{0, 19 * time.Second} {
		if act, _ := w.step(stoppedIn(t0.Add(d))); act != watchNone {
			t.Fatalf("at +%v got %v", d, act)
		}
	}
	in := stoppedIn(t0.Add(20 * time.Second))
	in.OwnIdle = true
	if act, _ := w.step(in); act != watchClearNudge {
		t.Fatalf("got %v want watchClearNudge", act)
	}
}

func TestDeferWatchNudgeOff(t *testing.T) {
	var w deferWatch
	w.step(stoppedIn(t0))
	in := stoppedIn(t0.Add(20 * time.Second))
	in.OwnIdle, in.NudgeEnabled = true, false
	if act, _ := w.step(in); act != watchClear {
		t.Fatalf("got %v want watchClear", act)
	}
}

func TestDeferWatchNotIdleNoNudge(t *testing.T) {
	var w deferWatch
	w.step(stoppedIn(t0))
	in := stoppedIn(t0.Add(20 * time.Second))
	if act, _ := w.step(in); act != watchClear {
		t.Fatalf("got %v want watchClear", act)
	}
}

func TestDeferWatchOwnTurnClearsEarly(t *testing.T) {
	var w deferWatch
	w.step(stoppedIn(t0))
	in := stoppedIn(t0.Add(5 * time.Second))
	in.OwnWorking = true
	if act, _ := w.step(in); act != watchClear {
		t.Fatalf("got %v want watchClear", act)
	}
}

func TestDeferWatchBlockerResumes(t *testing.T) {
	var w deferWatch
	w.step(stoppedIn(t0))
	in := stoppedIn(t0.Add(5 * time.Second))
	in.Verdict = blockerWorking
	w.step(in)
	w.step(stoppedIn(t0.Add(10 * time.Second)))
	if act, _ := w.step(stoppedIn(t0.Add(29 * time.Second))); act != watchNone {
		t.Fatalf("at +29s got %v want none", act)
	}
	if act, _ := w.step(stoppedIn(t0.Add(30 * time.Second))); act != watchClear {
		t.Fatalf("at +30s got %v want watchClear", act)
	}
}

func TestDeferWatchMarksSeenOnce(t *testing.T) {
	var w deferWatch
	in := stoppedIn(t0)
	in.Verdict = blockerWorking
	in.Mark.Seen = false
	if act, _ := w.step(in); act != watchMarkSeen {
		t.Fatalf("unseen: got %v", act)
	}
	in.Mark.Seen = true
	if act, _ := w.step(in); act != watchNone {
		t.Fatalf("seen: got %v", act)
	}
}

func TestDeferWatchResetsOnLinkChange(t *testing.T) {
	var w deferWatch
	w.step(stoppedIn(t0))
	hold := stoppedIn(t0.Add(10 * time.Second))
	hold.Mark.Link = deferLink{On: "other", Created: "200"}
	hold.Verdict = blockerHold
	w.step(hold)
	b := stoppedIn(t0.Add(25 * time.Second))
	b.Mark.Link = hold.Mark.Link
	if act, _ := w.step(b); act != watchNone {
		t.Fatalf("got %v want none (fresh grace)", act)
	}
}

// A link swapped while a grace period runs, with the new link also stopped,
// must restart the clock rather than inherit the old one.
func TestDeferWatchLinkChangeWhileStopped(t *testing.T) {
	var w deferWatch
	w.step(stoppedIn(t0))
	b := stoppedIn(t0.Add(25 * time.Second))
	b.Mark.Link = deferLink{On: "other", Created: "200"}
	if act, _ := w.step(b); act != watchNone {
		t.Fatalf("got %v want none (fresh grace)", act)
	}
}

func TestDeferWatchSkipsFailedMarkRead(t *testing.T) {
	var w deferWatch
	w.step(stoppedIn(t0))
	bad := stoppedIn(t0.Add(25 * time.Second))
	bad.MarkOK = false
	if act, _ := w.step(bad); act != watchNone {
		t.Fatalf("failed read: got %v", act)
	}
	if act, _ := w.step(stoppedIn(t0.Add(26 * time.Second))); act != watchClear {
		t.Fatalf("next good poll: got %v want watchClear", act)
	}
}

func TestDeferWatchGoneFlag(t *testing.T) {
	var w deferWatch
	in := stoppedIn(t0)
	in.Verdict = blockerGone
	if _, gone := w.step(in); !gone {
		t.Fatal("gone should be true")
	}
}

func TestDeferWatchUnlinkedNever(t *testing.T) {
	var w deferWatch
	for i, v := range []blockerVerdict{blockerHold, blockerWorking, blockerStopped, blockerGone} {
		in := stoppedIn(t0.Add(time.Duration(i) * time.Minute))
		in.Mark.Link = deferLink{}
		in.Verdict = v
		if act, _ := w.step(in); act != watchNone {
			t.Fatalf("verdict %v: got %v", v, act)
		}
	}
}
