package main

import (
	"testing"
	"time"
)

// snapAt builds a snapshot with one client on `at`, lobby "switchboard".
func snapAt(at string, sessions ...swSession) swSnapshot {
	return swSnapshot{
		Sessions: sessions,
		Lobby:    "switchboard",
		Clients:  map[string]string{"/dev/ttys001": at},
	}
}

func waiting(name string, since int64) swSession {
	return swSession{Name: name, State: "Idle", Since: time.Unix(since, 0)}
}

func busy(name string) swSession {
	return swSession{Name: name, State: "Thinking", Since: time.Unix(1754700000, 0)}
}

func TestWaitingQueueOrdersOldestFirst(t *testing.T) {
	s := snapAt("switchboard", waiting("young", 200), waiting("old", 100), busy("work"))
	q := s.waitingQueue(nil)
	if len(q) != 2 || q[0].Name != "old" || q[1].Name != "young" {
		t.Errorf("queue = %v", q)
	}
}

func TestWaitingQueueSnoozeAndTiebreak(t *testing.T) {
	s := snapAt("switchboard", waiting("b", 100), waiting("a", 100))
	q := s.waitingQueue(map[string]time.Time{"a": time.Unix(100, 0)})
	if len(q) != 1 || q[0].Name != "b" {
		t.Errorf("snoozed session must be excluded, queue = %v", q)
	}
	// A new waiting episode (different Since) un-snoozes.
	q = s.waitingQueue(map[string]time.Time{"a": time.Unix(50, 0)})
	if len(q) != 2 || q[0].Name != "a" || q[1].Name != "b" {
		t.Errorf("same-Since tiebreak is by name, queue = %v", q)
	}
}

func TestConductorParkedDispatchesOldest(t *testing.T) {
	c := newConductor()
	act, ok := c.step(snapAt("switchboard", waiting("old", 100), waiting("young", 200)))
	if !ok || act.Target != "old" || act.Client != "/dev/ttys001" {
		t.Fatalf("act = %+v ok=%v", act, ok)
	}
	if c.phase != swEscorting || c.escortee != "old" {
		t.Errorf("phase=%v escortee=%q", c.phase, c.escortee)
	}
}

func TestConductorParkedIdleFleetNoAction(t *testing.T) {
	c := newConductor()
	if _, ok := c.step(snapAt("switchboard", busy("work"))); ok {
		t.Error("nothing waiting: no switch")
	}
	if c.phase != swParked {
		t.Errorf("phase = %v, want parked", c.phase)
	}
}

func TestConductorEscortHoldsWhileWaiting(t *testing.T) {
	c := newConductor()
	c.step(snapAt("switchboard", waiting("old", 100)))
	if _, ok := c.step(snapAt("old", waiting("old", 100))); ok {
		t.Error("must hold while escortee still waits")
	}
}

func TestConductorEscortAdvancesOnResolve(t *testing.T) {
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100), waiting("b", 200)))
	act, ok := c.step(snapAt("a", busy("a"), waiting("b", 200)))
	if !ok || act.Target != "b" {
		t.Fatalf("act = %+v ok=%v", act, ok)
	}
	if c.phase != swEscorting || c.escortee != "b" {
		t.Errorf("phase=%v escortee=%q", c.phase, c.escortee)
	}
}

func TestConductorEscortReturnsToLobbyWhenQueueEmpty(t *testing.T) {
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100)))
	act, ok := c.step(snapAt("a", busy("a")))
	if !ok || act.Target != "switchboard" {
		t.Fatalf("act = %+v ok=%v", act, ok)
	}
	if c.phase != swParked {
		t.Errorf("phase = %v, want parked", c.phase)
	}
}

func TestConductorEscortGoneSessionCountsResolved(t *testing.T) {
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100)))
	act, ok := c.step(snapAt("a"))
	if !ok || act.Target != "switchboard" {
		t.Fatalf("vanished escortee must resolve, act=%+v ok=%v", act, ok)
	}
}

func TestConductorManualLeavePausesAndSnoozes(t *testing.T) {
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100)))
	// User switched the client to some other session while a still waits.
	if _, ok := c.step(snapAt("elsewhere", waiting("a", 100))); ok {
		t.Fatal("manual navigation must not trigger a switch")
	}
	if c.phase != swPaused {
		t.Fatalf("phase = %v, want paused", c.phase)
	}
	if got, ok := c.snoozed["a"]; !ok || !got.Equal(time.Unix(100, 0)) {
		t.Errorf("snoozed[a] = %v ok=%v", got, ok)
	}
	// Back at the lobby: resume. The snoozed episode must NOT re-dispatch.
	if _, ok := c.step(snapAt("switchboard", waiting("a", 100))); ok {
		t.Error("resume tick must not redispatch a snoozed session")
	}
	if c.phase != swParked {
		t.Errorf("phase = %v, want parked after lobby return", c.phase)
	}
	// New waiting episode: dispatch again.
	act, ok := c.step(snapAt("switchboard", waiting("a", 300)))
	if !ok || act.Target != "a" {
		t.Errorf("new episode must dispatch, act=%+v ok=%v", act, ok)
	}
}

func TestConductorParkedUserWandersOff(t *testing.T) {
	c := newConductor()
	c.step(snapAt("switchboard", busy("a")))
	if _, ok := c.step(snapAt("a", busy("a"))); ok {
		t.Fatal("no switch when user wandered off")
	}
	if c.phase != swPaused {
		t.Errorf("phase = %v, want paused", c.phase)
	}
}

func TestConductorNoClient(t *testing.T) {
	c := newConductor()
	s := swSnapshot{Sessions: []swSession{waiting("a", 100)}, Lobby: "switchboard", Clients: map[string]string{}}
	if _, ok := c.step(s); ok {
		t.Error("no client: nothing to drive")
	}
}

func TestConductorPicksLobbyClientDeterministically(t *testing.T) {
	c := newConductor()
	s := swSnapshot{
		Sessions: []swSession{waiting("a", 100)},
		Lobby:    "switchboard",
		Clients: map[string]string{
			"/dev/ttys009": "switchboard",
			"/dev/ttys001": "switchboard",
		},
	}
	act, ok := c.step(s)
	if !ok || act.Client != "/dev/ttys001" {
		t.Errorf("must pick lexicographically smallest lobby client, act=%+v ok=%v", act, ok)
	}
}
