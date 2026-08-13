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
	now := time.Unix(1_754_700_000, 0)
	s := snapAt("switchboard", waiting("young", 200), waiting("old", 100), busy("work"))
	q := s.waitingQueue(nil, now)
	if len(q) != 2 || q[0].Name != "old" || q[1].Name != "young" {
		t.Errorf("queue = %v", q)
	}
}

func TestWaitingQueueSnoozeAndTiebreak(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	s := snapAt("switchboard", waiting("b", 100), waiting("a", 100))
	q := s.waitingQueue(map[string]swSnooze{"a": {since: time.Unix(100, 0), at: now}}, now)
	if len(q) != 1 || q[0].Name != "b" {
		t.Errorf("snoozed session must be excluded, queue = %v", q)
	}
	// A new waiting episode (different Since) un-snoozes.
	q = s.waitingQueue(map[string]swSnooze{"a": {since: time.Unix(50, 0), at: now}}, now)
	if len(q) != 2 || q[0].Name != "a" || q[1].Name != "b" {
		t.Errorf("same-Since tiebreak is by name, queue = %v", q)
	}
}

func TestConductorParkedDispatchesOldest(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	act, ok := c.step(snapAt("switchboard", waiting("old", 100), waiting("young", 200)), now)
	if !ok || act.Target != "old" || act.Client != "/dev/ttys001" {
		t.Fatalf("act = %+v ok=%v", act, ok)
	}
	if c.phase != swEscorting || c.escortee != "old" {
		t.Errorf("phase=%v escortee=%q", c.phase, c.escortee)
	}
}

func TestConductorParkedIdleFleetNoAction(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	if _, ok := c.step(snapAt("switchboard", busy("work")), now); ok {
		t.Error("nothing waiting: no switch")
	}
	if c.phase != swParked {
		t.Errorf("phase = %v, want parked", c.phase)
	}
}

func TestConductorEscortHoldsWhileWaiting(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("old", 100)), now)
	if _, ok := c.step(snapAt("old", waiting("old", 100)), now); ok {
		t.Error("must hold while escortee still waits")
	}
}

func TestConductorEscortAdvancesOnResolve(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100), waiting("b", 200)), now)
	act, ok := c.step(snapAt("a", busy("a"), waiting("b", 200)), now)
	if !ok || act.Target != "b" {
		t.Fatalf("act = %+v ok=%v", act, ok)
	}
	if c.phase != swEscorting || c.escortee != "b" {
		t.Errorf("phase=%v escortee=%q", c.phase, c.escortee)
	}
}

func TestConductorEscortReturnsToLobbyWhenQueueEmpty(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100)), now)
	act, ok := c.step(snapAt("a", busy("a")), now)
	if !ok || act.Target != "switchboard" {
		t.Fatalf("act = %+v ok=%v", act, ok)
	}
	if c.phase != swParked {
		t.Errorf("phase = %v, want parked", c.phase)
	}
}

func TestConductorEscortGoneSessionCountsResolved(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100)), now)
	act, ok := c.step(snapAt("a"), now)
	if !ok || act.Target != "switchboard" {
		t.Fatalf("vanished escortee must resolve, act=%+v ok=%v", act, ok)
	}
}

func TestConductorManualLeavePausesAndSnoozes(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100)), now)
	// User switched the client to some other session while a still waits.
	if _, ok := c.step(snapAt("elsewhere", waiting("a", 100)), now); ok {
		t.Fatal("manual navigation must not trigger a switch")
	}
	if c.phase != swPaused {
		t.Fatalf("phase = %v, want paused", c.phase)
	}
	if got, ok := c.snoozed["a"]; !ok || !got.since.Equal(time.Unix(100, 0)) {
		t.Errorf("snoozed[a] = %v ok=%v", got, ok)
	}
	// Back at the lobby: resume. The snoozed episode must NOT re-dispatch.
	if _, ok := c.step(snapAt("switchboard", waiting("a", 100)), now); ok {
		t.Error("resume tick must not redispatch a snoozed session")
	}
	if c.phase != swParked {
		t.Errorf("phase = %v, want parked after lobby return", c.phase)
	}
	// New waiting episode: dispatch again.
	act, ok := c.step(snapAt("switchboard", waiting("a", 300)), now)
	if !ok || act.Target != "a" {
		t.Errorf("new episode must dispatch, act=%+v ok=%v", act, ok)
	}
}

func TestConductorParkedUserWandersOff(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", busy("a")), now)
	if _, ok := c.step(snapAt("a", busy("a")), now); ok {
		t.Fatal("no switch when user wandered off")
	}
	if c.phase != swPaused {
		t.Errorf("phase = %v, want paused", c.phase)
	}
}

func TestConductorNoClient(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	s := swSnapshot{Sessions: []swSession{waiting("a", 100)}, Lobby: "switchboard", Clients: map[string]string{}}
	if _, ok := c.step(s, now); ok {
		t.Error("no client: nothing to drive")
	}
}

func TestConductorPicksLobbyClientDeterministically(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	s := swSnapshot{
		Sessions: []swSession{waiting("a", 100)},
		Lobby:    "switchboard",
		Clients: map[string]string{
			"/dev/ttys009": "switchboard",
			"/dev/ttys001": "switchboard",
		},
	}
	act, ok := c.step(s, now)
	if !ok || act.Client != "/dev/ttys001" {
		t.Errorf("must pick lexicographically smallest lobby client, act=%+v ok=%v", act, ok)
	}
}

func TestConductorClientChurnMidEscort(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	// Dispatch to "a" on old client.
	c.step(swSnapshot{
		Sessions: []swSession{waiting("a", 100)},
		Lobby:    "switchboard",
		Clients:  map[string]string{"/dev/ttys001": "switchboard"},
	}, now)
	// Old client vanishes, new one appears at lobby; "a" still waits.
	// This is not a user walk-away, so no snooze.
	if _, ok := c.step(swSnapshot{
		Sessions: []swSession{waiting("a", 100)},
		Lobby:    "switchboard",
		Clients:  map[string]string{"/dev/ttys002": "switchboard"},
	}, now); ok {
		t.Error("client churn: no switch this tick")
	}
	if len(c.snoozed) > 0 {
		t.Errorf("client churn: no snooze entry, got %v", c.snoozed)
	}
	if c.phase != swParked {
		t.Errorf("phase = %v, want parked", c.phase)
	}
	// On next tick with the new client at lobby and "a" waiting, dispatch "a".
	act, ok := c.step(swSnapshot{
		Sessions: []swSession{waiting("a", 100)},
		Lobby:    "switchboard",
		Clients:  map[string]string{"/dev/ttys002": "switchboard"},
	}, now)
	if !ok || act.Target != "a" || act.Client != "/dev/ttys002" {
		t.Errorf("re-dispatch after churn, act=%+v ok=%v", act, ok)
	}
}

// A snooze means "not this episode RIGHT NOW", not "never again": an idle
// session's episode can last hours (its Since only moves on a real state
// transition), and an unexpiring snooze starves it — observed live
// 2026-08-13: 4 sessions publishing Idle, lobby saying "1 waiting".
func TestSnoozeExpiresAfterTTL(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	s := snapAt("switchboard", waiting("a", 100))
	sn := map[string]swSnooze{"a": {since: time.Unix(100, 0), at: now}}
	if q := s.waitingQueue(sn, now.Add(swSnoozeTTL-time.Minute)); len(q) != 0 {
		t.Errorf("inside TTL: queue = %v, want empty", q)
	}
	if q := s.waitingQueue(sn, now.Add(swSnoozeTTL+time.Minute)); len(q) != 1 {
		t.Errorf("past TTL: queue = %v, want the session back", q)
	}
}

func TestExpiredSnoozeIsPruned(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.snoozed["a"] = swSnooze{since: time.Unix(100, 0), at: now.Add(-swSnoozeTTL - time.Minute)}
	c.pruneSnoozes(snapAt("switchboard", waiting("a", 100)), now)
	if _, ok := c.snoozed["a"]; ok {
		t.Error("expired snooze must be pruned")
	}
}

func TestStatusLineShowsSnoozedCount(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.snoozed["a"] = swSnooze{since: time.Unix(100, 0), at: now}
	got := c.statusLine(snapAt("switchboard", waiting("a", 100), waiting("b", 200)), now)
	if want := "conducting · 1 waiting · 1 snoozed"; got != want {
		t.Errorf("statusLine = %q, want %q", got, want)
	}
}

func TestStatusLineOmitsZeroSnoozed(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	got := c.statusLine(snapAt("switchboard", waiting("a", 100)), now)
	if want := "conducting · 1 waiting"; got != want {
		t.Errorf("statusLine = %q, want %q", got, want)
	}
}

// The snoozed count must be computed live against the snapshot, not from
// len(c.snoozed): pruning only runs inside step(), which standby skips, so a
// stale (TTL-expired) map entry must not inflate the suffix.
func TestStatusLineOmitsExpiredSnoozeFromCount(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.snoozed["a"] = swSnooze{since: time.Unix(100, 0), at: now.Add(-swSnoozeTTL - time.Minute)}
	got := c.statusLine(snapAt("switchboard", waiting("a", 100)), now)
	if want := "conducting · 1 waiting"; got != want {
		t.Errorf("statusLine = %q, want %q (expired snooze must not count)", got, want)
	}
}
