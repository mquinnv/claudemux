package main

import "testing"

func rec(name, id, state string, lastSeen int64) loadedRecord {
	return loadedRecord{Path: "/d/" + name + ".json", Rec: sessionRecord{
		SessionName: name, SessionID: id, State: state, LastSeen: lastSeen,
	}}
}

func lostNames(l []lostSession) []string {
	var out []string
	for _, s := range l {
		out = append(out, s.Rec.SessionName)
	}
	return out
}

func TestSelectLostCluster(t *testing.T) {
	const cutoff = 100_000
	recs := []loadedRecord{
		rec("a", "1", "Idle", cutoff-60),           // newest pre-cutoff
		rec("b", "2", "Thinking", cutoff-60-599),   // inside 10 min of newest
		rec("c", "3", "Idle", cutoff-60-600),       // exactly on the edge: in
		rec("d", "4", "Idle", cutoff-60-601),       // outside: closed earlier
		rec("e", "5", "Idle", cutoff+5),            // written after cutoff: alive now
	}
	lost, newest := selectLost(recs, cutoff, nil, nil)
	if newest != cutoff-60 {
		t.Errorf("newest = %d, want %d", newest, cutoff-60)
	}
	got := lostNames(lost)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("lost = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("lost = %v, want %v (sorted newest first)", got, want)
		}
	}
	if !lost[1].Interrupted || lost[0].Interrupted {
		t.Errorf("interrupted flags wrong: %+v", lost)
	}
}

func TestSelectLostCutoffBoundary(t *testing.T) {
	// last_seen == cutoff is NOT before the cutoff: that head was alive at boot.
	lost, _ := selectLost([]loadedRecord{rec("a", "1", "Idle", 500)}, 500, nil, nil)
	if len(lost) != 0 {
		t.Fatalf("lost = %v, want none", lostNames(lost))
	}
}

func TestSelectLostExcludesLive(t *testing.T) {
	recs := []loadedRecord{
		rec("a", "1", "Idle", 90),
		rec("b", "2", "Idle", 90),
		rec("c", "3", "Idle", 90),
	}
	lost, _ := selectLost(recs, 100, map[string]bool{"a": true}, map[string]bool{"2": true})
	if got := lostNames(lost); len(got) != 1 || got[0] != "c" {
		t.Fatalf("lost = %v, want [c]", got)
	}
}

func TestSelectLostEmpty(t *testing.T) {
	if lost, newest := selectLost(nil, 100, nil, nil); len(lost) != 0 || newest != 0 {
		t.Fatalf("got %v %d", lost, newest)
	}
	// Every record written after the cutoff: nothing to offer.
	if lost, _ := selectLost([]loadedRecord{rec("a", "1", "Idle", 200)}, 100, nil, nil); len(lost) != 0 {
		t.Fatalf("got %v", lostNames(lost))
	}
}

func TestInterruptedState(t *testing.T) {
	yes := []string{"Thinking", "Compacting", "Tool:Bash", "Tool:Edit", "Background:2"}
	no := []string{"", "Idle", "Awaiting", "Error", "Asking", "Tool:AskUserQuestion", "Starting", "Unsure:1"}
	for _, s := range yes {
		if !interruptedState(s) {
			t.Errorf("interruptedState(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if interruptedState(s) {
			t.Errorf("interruptedState(%q) = true, want false", s)
		}
	}
}

func TestParseBootTime(t *testing.T) {
	sec, ok := parseBootTime("{ sec = 1789750380, usec = 123456 } Thu Sep 18 13:53:00 2026\n")
	if !ok || sec != 1789750380 {
		t.Errorf("got %d %v", sec, ok)
	}
	for _, bad := range []string{"", "garbage", "{ sec = , usec = 1 }", "{ sec = -5, usec = 0 }"} {
		if _, ok := parseBootTime(bad); ok {
			t.Errorf("parseBootTime(%q) ok, want failure", bad)
		}
	}
}

func TestRestoreCutoff(t *testing.T) {
	if c, ok := restoreCutoff(100, true, 200, true); !ok || c != 200 {
		t.Errorf("both: %d %v", c, ok)
	}
	if c, ok := restoreCutoff(300, true, 200, true); !ok || c != 300 {
		t.Errorf("boot later: %d %v", c, ok)
	}
	if c, ok := restoreCutoff(100, true, 0, false); !ok || c != 100 {
		t.Errorf("boot only: %d %v", c, ok)
	}
	if c, ok := restoreCutoff(0, false, 200, true); !ok || c != 200 {
		t.Errorf("tmux only: %d %v", c, ok)
	}
	if _, ok := restoreCutoff(0, false, 0, false); ok {
		t.Errorf("neither: ok, want no offer")
	}
}
