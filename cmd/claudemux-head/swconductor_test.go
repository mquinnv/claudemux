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

func deferredWaiting(name string, since int64) swSession {
	return swSession{Name: name, State: "Idle", Since: time.Unix(since, 0), Deferred: true}
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

// A deferred waiter always waits behind every normal waiter, even one that
// started waiting more recently — the mark trades position for being
// remembered, not the other way around.
func TestWaitingQueueDeferredWaitsBehindYoungerNormal(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	s := snapAt("switchboard", deferredWaiting("old-deferred", 50), waiting("young", 200))
	q := s.waitingQueue(nil, now)
	if len(q) != 2 || q[0].Name != "young" || q[1].Name != "old-deferred" {
		t.Errorf("queue = %v, want young before old-deferred despite Since", q)
	}
}

// With no competing normal waiter, a deferred session is still the one
// dispatched — deferred means "wait your turn," not "never."
func TestWaitingQueueDeferredAloneIsDispatched(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	s := snapAt("switchboard", deferredWaiting("solo", 100))
	q := s.waitingQueue(nil, now)
	if len(q) != 1 || q[0].Name != "solo" {
		t.Errorf("queue = %v, want the sole deferred waiter", q)
	}
}

// Snooze semantics apply identically to deferred sessions: a deferred
// session's snoozed episode is excluded from the queue exactly like a
// normal one's.
func TestWaitingQueueSnoozedDeferredExcluded(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	s := snapAt("switchboard", deferredWaiting("a", 100), waiting("b", 200))
	q := s.waitingQueue(map[string]swSnooze{"a": {since: time.Unix(100, 0), at: now}}, now)
	if len(q) != 1 || q[0].Name != "b" {
		t.Errorf("snoozed deferred session must be excluded, queue = %v", q)
	}
}

// Two deferred waiters order oldest-first among themselves, the same rule
// that governs normal waiters.
func TestWaitingQueueTwoDeferredOrderedOldestFirst(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	s := snapAt("switchboard", deferredWaiting("young", 200), deferredWaiting("old", 100))
	q := s.waitingQueue(nil, now)
	if len(q) != 2 || q[0].Name != "old" || q[1].Name != "young" {
		t.Errorf("queue = %v, want old before young among deferred waiters", q)
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
	// Two sessions in the fleet: the lobby is worth returning to, so the
	// sole-session hold below does not apply.
	c.step(snapAt("switchboard", waiting("a", 100), busy("b")), now)
	act, ok := c.step(snapAt("a", busy("a"), busy("b")), now)
	if !ok || act.Target != "switchboard" {
		t.Fatalf("act = %+v ok=%v", act, ok)
	}
	if c.phase != swParked {
		t.Errorf("phase = %v, want parked", c.phase)
	}
}

func TestConductorHoldsOnSoleSession(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100)), now)
	// "a" hands back with nothing else in the fleet: the lobby has nothing
	// to show and nothing to dispatch, so the client stays where it is.
	if act, ok := c.step(snapAt("a", busy("a")), now); ok {
		t.Fatalf("sole session must not be conducted away from, act=%+v", act)
	}
	if c.phase != swEscorting || c.escortee != "a" {
		t.Errorf("phase=%v escortee=%q, want escorting/a", c.phase, c.escortee)
	}
	// The hold is not a one-tick reprieve.
	if _, ok := c.step(snapAt("a", busy("a")), now.Add(time.Minute)); ok {
		t.Error("hold must persist across ticks")
	}
}

func TestConductorSoleSessionHoldReleasesWhenAnotherWaits(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100)), now)
	c.step(snapAt("a", busy("a")), now) // holding
	act, ok := c.step(snapAt("a", busy("a"), waiting("b", 900)), now.Add(time.Minute))
	if !ok || act.Target != "b" {
		t.Fatalf("a new waiter must collect the user, act=%+v ok=%v", act, ok)
	}
	if c.phase != swEscorting || c.escortee != "b" {
		t.Errorf("phase=%v escortee=%q, want escorting/b", c.phase, c.escortee)
	}
}

// Holding keeps the escortee set, which is what lets the existing walk-away
// branch still recognize the user moving themselves.
func TestConductorSoleSessionHoldYieldsToManualLobbyReturn(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100)), now)
	c.step(snapAt("a", busy("a")), now) // holding
	if _, ok := c.step(snapAt("switchboard", busy("a")), now.Add(time.Second)); ok {
		t.Fatal("returning to the lobby yourself is not a dispatch")
	}
	if c.phase != swParked {
		t.Errorf("phase = %v, want parked", c.phase)
	}
	// Parked again: a fresh waiting episode still dispatches normally.
	act, ok := c.step(snapAt("switchboard", waiting("a", 300)), now.Add(2*time.Second))
	if !ok || act.Target != "a" {
		t.Errorf("act = %+v ok=%v", act, ok)
	}
}

// A vanished sole session is not a session to hold on to: the client must be
// returned to the lobby, or it sits in a tmux session that is being killed.
func TestConductorSoleSessionGoneReturnsToLobby(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100)), now)
	act, ok := c.step(snapAt("a"), now)
	if !ok || act.Target != "switchboard" {
		t.Fatalf("act = %+v ok=%v", act, ok)
	}
	if c.phase != swParked {
		t.Errorf("phase = %v, want parked", c.phase)
	}
}

func TestStatusLineReportsSoleSessionHold(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100)), now)
	c.step(snapAt("a", busy("a")), now)
	got := c.statusLine(snapAt("a", busy("a")), now)
	if want := "holding — only session in the fleet"; got != want {
		t.Errorf("statusLine = %q, want %q", got, want)
	}
	// Still escorting while the session actually waits.
	if got := c.statusLine(snapAt("a", waiting("a", 100)), now); got != "escorting → a · 1 waiting" {
		t.Errorf("statusLine = %q, want the escorting line", got)
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

// The deferred suffix appears after the snoozed suffix, and only when a
// deferred session is actually waiting.
func TestStatusLineShowsDeferredCount(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	got := c.statusLine(snapAt("switchboard", waiting("a", 100), deferredWaiting("b", 200)), now)
	if want := "conducting · 2 waiting · 1 deferred"; got != want {
		t.Errorf("statusLine = %q, want %q", got, want)
	}
}

func TestStatusLineOmitsZeroDeferred(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	got := c.statusLine(snapAt("switchboard", waiting("a", 100)), now)
	if want := "conducting · 1 waiting"; got != want {
		t.Errorf("statusLine = %q, want %q", got, want)
	}
}

// Both suffixes together: snoozed first, deferred after — matching the order
// the constraints specify.
func TestStatusLineShowsSnoozedThenDeferred(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.snoozed["a"] = swSnooze{since: time.Unix(100, 0), at: now}
	got := c.statusLine(snapAt("switchboard", waiting("a", 100), deferredWaiting("b", 200)), now)
	if want := "conducting · 1 waiting · 1 snoozed · 1 deferred"; got != want {
		t.Errorf("statusLine = %q, want %q", got, want)
	}
}

// A deferred session that isn't waiting (still busy) must not inflate the
// count: the suffix reports waiting-but-deferred, not merely marked.
func TestStatusLineOmitsDeferredNotWaiting(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	busyDeferred := swSession{Name: "b", State: "Thinking", Since: time.Unix(1754700000, 0), Deferred: true}
	got := c.statusLine(snapAt("switchboard", waiting("a", 100), busyDeferred), now)
	if want := "conducting · 1 waiting"; got != want {
		t.Errorf("statusLine = %q, want %q", got, want)
	}
}

// pauseAt drives a fresh conductor into swPaused at session name: one step
// parked at the lobby is skipped — the client simply appears off-lobby.
func pauseAt(c *conductor, name string, sessions ...swSession) {
	c.step(snapAt(name, sessions...), time.Unix(1_754_700_000, 0))
}

func TestPausedWatchingBusySessionNeverYanked(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", busy("b"))
	// b was never waiting under the user; a waiting session elsewhere
	// must not move them.
	for i := 0; i < 3; i++ {
		if _, ok := c.step(snapAt("b", busy("b"), waiting("a", 100)), now.Add(time.Duration(i)*time.Second)); ok {
			t.Fatal("watching a busy session must never dispatch")
		}
	}
	if c.phase != swPaused {
		t.Errorf("phase = %v, want paused", c.phase)
	}
}

func TestPausedHandBackDispatchesToWaiting(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", waiting("b", 50))
	// Observe b waiting under the user...
	if _, ok := c.step(snapAt("b", waiting("b", 50), waiting("a", 100)), now); ok {
		t.Fatal("attending a waiting session must not dispatch")
	}
	// ...then the user hands it back: conduct to the queue head.
	act, ok := c.step(snapAt("b", busy("b"), waiting("a", 100)), now.Add(time.Second))
	if !ok || act.Target != "a" {
		t.Fatalf("hand-back must dispatch to a, got %+v ok=%v", act, ok)
	}
	if c.phase != swEscorting || c.escortee != "a" {
		t.Errorf("phase=%v escortee=%q", c.phase, c.escortee)
	}
}

func TestPausedHandBackStickyUntilQueueFills(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", waiting("b", 50))
	c.step(snapAt("b", waiting("b", 50)), now)
	// Hand-back with nothing waiting: stay put...
	if _, ok := c.step(snapAt("b", busy("b")), now.Add(time.Second)); ok {
		t.Fatal("empty queue: nothing to dispatch to")
	}
	// ...but the hand-back is remembered; a session that starts waiting
	// minutes later still collects the user.
	act, ok := c.step(snapAt("b", busy("b"), waiting("a", 900)), now.Add(3*time.Minute))
	if !ok || act.Target != "a" {
		t.Fatalf("late waiter must be dispatched, got %+v ok=%v", act, ok)
	}
}

func TestPausedMovingAgainResetsHandBack(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", waiting("b", 50))
	c.step(snapAt("b", waiting("b", 50)), now)
	c.step(snapAt("b", busy("b")), now.Add(time.Second)) // handed back
	// The user jumps to c2 (busy) themselves: observation restarts there.
	c.step(snapAt("c2", busy("b"), busy("c2")), now.Add(2*time.Second))
	if _, ok := c.step(snapAt("c2", busy("b"), busy("c2"), waiting("a", 900)), now.Add(3*time.Second)); ok {
		t.Fatal("a fresh self-navigation must clear the hand-back")
	}
}

func TestPausedLobbyReturnClearsHandBack(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", waiting("b", 50))
	c.step(snapAt("b", waiting("b", 50)), now)
	c.step(snapAt("b", busy("b")), now.Add(time.Second)) // handed back
	// Return to the lobby: parked, observation cleared.
	c.step(snapAt("switchboard", busy("b")), now.Add(2*time.Second))
	if c.phase != swParked {
		t.Fatalf("phase = %v, want parked", c.phase)
	}
	// Jump back into b (busy): the old hand-back must not linger.
	c.step(snapAt("b", busy("b")), now.Add(3*time.Second))
	if _, ok := c.step(snapAt("b", busy("b"), waiting("a", 900)), now.Add(4*time.Second)); ok {
		t.Fatal("stale hand-back from a previous pause must not dispatch")
	}
}
