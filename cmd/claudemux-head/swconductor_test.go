package main

import (
	"path/filepath"
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

// A deferred session never enters the queue, even when it is the oldest
// waiter in the fleet: the mark means "do not drive me here."
func TestWaitingQueueDeferredExcluded(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	s := snapAt("switchboard", deferredWaiting("old-deferred", 50), waiting("young", 200))
	q := s.waitingQueue(nil, now)
	if len(q) != 1 || q[0].Name != "young" {
		t.Errorf("queue = %v, want only the non-deferred waiter", q)
	}
}

// With nothing else waiting, a deferred session is still not dispatched —
// the conductor parks rather than reaching for the one session the user
// asked not to be pulled into. This is the case the old "last in line"
// ordering got wrong.
func TestWaitingQueueDeferredAloneIsNotDispatched(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	s := snapAt("switchboard", deferredWaiting("solo", 100))
	if q := s.waitingQueue(nil, now); len(q) != 0 {
		t.Errorf("queue = %v, want empty: a deferred session is never dispatched", q)
	}
}

// The end-to-end shape of the bug this replaced: every normal waiter
// snoozed, one deferred session waiting. The deferred session must never be
// the destination — with the queue otherwise empty the snooze is released and
// the client goes back to the normal waiter instead.
func TestConductorParkedHoldsWhenOnlyDeferredWaits(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.snoozed["normal"] = swSnooze{since: time.Unix(100, 0), at: now}
	s := snapAt("switchboard", waiting("normal", 100), deferredWaiting("blocked", 50))
	act, ok := c.step(s, now)
	if !ok || act.Target != "normal" {
		t.Errorf("act = %+v ok=%v, want the released normal waiter, never the deferred one", act, ok)
	}
}

// A deferred session that was also snoozed is still not a destination: the
// release only ever re-queues sessions the queue would otherwise accept.
func TestConductorParkedHoldsWhenOnlySnoozedDeferredWaits(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.snoozed["blocked"] = swSnooze{since: time.Unix(50, 0), at: now}
	s := snapAt("switchboard", busy("work"), deferredWaiting("blocked", 50))
	if act, ok := c.step(s, now); ok {
		t.Errorf("dispatched %+v, want no action", act)
	}
}

// Every session busy, deferred, or snoozed: the snoozes are released at once
// and the conductor re-conducts through the skipped sessions, oldest first.
// A snooze is an anti-bounce, not a veto; the veto is defer.
func TestConductorReleasesSnoozesWhenQueueEmpties(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.snoozed["a"] = swSnooze{since: time.Unix(100, 0), at: now}
	c.snoozed["b"] = swSnooze{since: time.Unix(200, 0), at: now}
	s := snapAt("switchboard", waiting("b", 200), waiting("a", 100), busy("work"), deferredWaiting("blocked", 50))
	act, ok := c.step(s, now)
	if !ok || act.Target != "a" {
		t.Fatalf("act = %+v ok=%v, want dispatch to the oldest released waiter", act, ok)
	}
	if len(c.snoozed) != 0 {
		t.Errorf("snoozed = %v, want every snooze released", c.snoozed)
	}
	if c.isSnoozed(waiting("b", 200), now) {
		t.Error("b must no longer read as snoozed on the lobby")
	}
}

// The release applies mid-escort too: when the escortee resolves and the only
// other waiter is one the user skipped, the conductor carries them there
// rather than parking them on the lobby with a waiter dimmed behind them.
func TestConductorEscortAdvancesToReleasedSnooze(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100), waiting("b", 200)), now)
	c.snoozed["b"] = swSnooze{since: time.Unix(200, 0), at: now}
	act, ok := c.step(snapAt("a", busy("a"), waiting("b", 200)), now)
	if !ok || act.Target != "b" {
		t.Errorf("act = %+v ok=%v, want the released waiter, not the lobby", act, ok)
	}
	if c.phase != swEscorting || c.escortee != "b" {
		t.Errorf("phase=%v escortee=%q", c.phase, c.escortee)
	}
}

// Skipping the only waiter and returning to the lobby brings the user
// straight back: with nothing else to conduct to, the skip has no one to
// yield to. Staying away from it is what defer is for.
func TestConductorLobbyReturnReleasesSoleSnooze(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100), busy("work")), now)
	if _, ok := c.step(snapAt("switchboard", waiting("a", 100), busy("work")), now); ok {
		t.Fatal("the walk-away tick itself must not switch")
	}
	if _, ok := c.snoozed["a"]; !ok {
		t.Fatal("walking away must snooze a")
	}
	act, ok := c.step(snapAt("switchboard", waiting("a", 100), busy("work")), now)
	if !ok || act.Target != "a" {
		t.Errorf("act = %+v ok=%v, want the sole waiter released and re-escorted", act, ok)
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
	c.step(snapAt("switchboard", waiting("a", 100), waiting("b", 200)), now)
	// User switched the client to some other session while a still waits.
	if _, ok := c.step(snapAt("elsewhere", waiting("a", 100), waiting("b", 200)), now); ok {
		t.Fatal("manual navigation must not trigger a switch")
	}
	if c.phase != swPaused {
		t.Fatalf("phase = %v, want paused", c.phase)
	}
	if got, ok := c.snoozed["a"]; !ok || !got.since.Equal(time.Unix(100, 0)) {
		t.Errorf("snoozed[a] = %v ok=%v", got, ok)
	}
	// Back at the lobby: resume. The snoozed episode yields to the other
	// waiter; it is not the destination while anyone else is waiting.
	if _, ok := c.step(snapAt("switchboard", waiting("a", 100), waiting("b", 200)), now); ok {
		t.Error("resume tick must not switch")
	}
	if c.phase != swParked {
		t.Fatalf("phase = %v, want parked after lobby return", c.phase)
	}
	act, ok := c.step(snapAt("switchboard", waiting("a", 100), waiting("b", 200)), now)
	if !ok || act.Target != "b" {
		t.Errorf("snoozed a must yield to b, act=%+v ok=%v", act, ok)
	}
	// New waiting episode for a while escorting b: a is no longer snoozed,
	// so once b resolves the conductor goes to a on the new episode.
	act, ok = c.step(snapAt("b", waiting("a", 300), busy("b")), now)
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
	if want := "conducting · 1 waiting · 1 deferred"; got != want {
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
	if want := "conducting · 0 waiting · 1 snoozed · 1 deferred"; got != want {
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

// The four tests below drive a conductor restored via the conductor-handoff
// round trip through step(), rather than exercising the handoff file alone.
// They exist because the file-level round trip (TestConductHandoffRoundTrip)
// cannot see what resolveClient does with the restored state on its first
// live tick — which is exactly where the carried-client bug lived: the
// handoff used to not carry `client`, so the first tick after a restore
// looked identical to a client churn and dropped the escortee without
// snoozing it.

// TestConductHandoffRestoredEscortSurvivesFirstTick: the client is still
// sitting on the escortee when the restored conductor takes its first live
// tick. That must be a no-op — the client hasn't moved, so this cannot be
// treated as a user walk-away or a client churn.
func TestConductHandoffRestoredEscortSurvivesFirstTick(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := conductor{
		phase:    swEscorting,
		client:   "/dev/ttys001",
		escortee: "x",
		snoozed:  map[string]swSnooze{},
	}
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeConductHandoff(path, c, now); err != nil {
		t.Fatalf("write: %v", err)
	}
	restored, ok := readConductHandoff(path, now)
	if !ok {
		t.Fatal("handoff not readable")
	}
	if _, ok := restored.step(snapAt("x", waiting("x", 100)), now); ok {
		t.Error("restored escort: first tick after restore must not dispatch")
	}
	if restored.phase != swEscorting || restored.escortee != "x" {
		t.Errorf("phase=%v escortee=%q, want swEscorting/\"x\" preserved", restored.phase, restored.escortee)
	}
}

// TestConductHandoffRestoredEscortWalkAwayStillSnoozes is the regression test
// for the bounce-back bug: without the client in the handoff, the first tick
// after a restore reads as a client churn, the swEscorting branch clears the
// escortee WITHOUT snoozing it (churn is not a walk-away), and a later walk
// to the lobby finds nothing snoozed — so the conductor escorts the user
// straight back into the session they just left. Confirmed failing before
// the fix (see task-4-report.md fix-report section for the RED output);
// carrying `client` in the handoff makes the first tick a no-op (see
// TestConductHandoffRestoredEscortSurvivesFirstTick above), so the walk-away
// below is a genuine user-initiated move and gets snoozed like any other.
// A second waiter "y" makes the snooze observable: the next dispatch must go
// to y, not back to x. (With x the only waiter the snooze would be released
// on the spot — see TestConductorLobbyReturnReleasesSoleSnooze — which is
// the intended behaviour, not the bounce this test guards against.)
func TestConductHandoffRestoredEscortWalkAwayStillSnoozes(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := conductor{
		phase:    swEscorting,
		client:   "/dev/ttys001",
		escortee: "x",
		snoozed:  map[string]swSnooze{},
	}
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeConductHandoff(path, c, now); err != nil {
		t.Fatalf("write: %v", err)
	}
	restored, ok := readConductHandoff(path, now)
	if !ok {
		t.Fatal("handoff not readable")
	}
	// First tick: client still on the escortee, still waiting — a no-op.
	restored.step(snapAt("x", waiting("x", 100), waiting("y", 200)), now)
	// The user walks back to the lobby while "x" is still waiting.
	if _, ok := restored.step(snapAt("switchboard", waiting("x", 100), waiting("y", 200)), now); ok {
		t.Fatal("walk-away tick must not dispatch")
	}
	if restored.phase != swParked {
		t.Fatalf("phase = %v, want swParked after walk-away", restored.phase)
	}
	if _, snoozed := restored.snoozed["x"]; !snoozed {
		t.Fatal("walking away from the restored escort must snooze it")
	}
	// A further tick with "x" still waiting must escort to y, not back into x.
	act, ok := restored.step(snapAt("switchboard", waiting("x", 100), waiting("y", 200)), now)
	if !ok || act.Target != "y" {
		t.Errorf("must go to the other waiter, not bounce back into the snoozed escortee, got %+v ok=%v", act, ok)
	}
}

// TestConductHandoffRestoredPausedObservationSurvives: a restored swPaused
// conductor's paused-session observation must continue rather than restart —
// otherwise a hand-back the user made just before the restart could be
// forgotten and the observation would begin over from a state that looks
// like a fresh pause.
func TestConductHandoffRestoredPausedObservationSurvives(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := conductor{
		phase:            swPaused,
		client:           "/dev/ttys001",
		snoozed:          map[string]swSnooze{},
		pausedCur:        "y",
		pausedCurWaiting: true,
	}
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeConductHandoff(path, c, now); err != nil {
		t.Fatalf("write: %v", err)
	}
	restored, ok := readConductHandoff(path, now)
	if !ok {
		t.Fatal("handoff not readable")
	}
	if _, ok := restored.step(snapAt("y", waiting("y", 100)), now); ok {
		t.Error("restored pause: first tick after restore must not dispatch")
	}
	if restored.pausedCur != "y" || !restored.pausedCurWaiting {
		t.Errorf("paused observation reset: cur=%q waiting=%v, want \"y\"/true", restored.pausedCur, restored.pausedCurWaiting)
	}
}

// TestConductHandoffRestoredPreexistingDeferDoesNotYank: a restored swPaused
// conductor whose paused session was already deferred when the handoff was
// written must not read that mark as a fresh defer on its first live tick. A
// fresh defer (pausedCurDeferred false -> true under the client) moves the
// user on — to the queue head, or back to the lobby — so a handoff that
// dropped pausedCurDeferred would turn a restart landing while the user sits
// in a session they walked into knowing it was deferred into a yank out of it.
func TestConductHandoffRestoredPreexistingDeferDoesNotYank(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := conductor{
		phase:             swPaused,
		client:            "/dev/ttys001",
		snoozed:           map[string]swSnooze{},
		pausedCur:         "b",
		pausedCurWaiting:  true,
		pausedCurDeferred: true,
	}
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeConductHandoff(path, c, now); err != nil {
		t.Fatalf("write: %v", err)
	}
	restored, ok := readConductHandoff(path, now)
	if !ok {
		t.Fatal("handoff not readable")
	}
	// Still deferred, still under the client, and another session waiting
	// that a fresh defer would dispatch to.
	s := snapAt("b", deferredWaiting("b", 50), waiting("a", 100))
	if act, ok := restored.step(s, now); ok {
		t.Fatalf("pre-existing defer read as fresh after restore: dispatched %+v", act)
	}
	if restored.phase != swPaused || restored.pausedCur != "b" || !restored.pausedCurDeferred {
		t.Errorf("phase=%v cur=%q deferred=%v, want swPaused/\"b\"/true", restored.phase, restored.pausedCur, restored.pausedCurDeferred)
	}
}

// TestConductHandoffRestoredStaleClientReadopts: a carried client that no
// longer exists (the tmux client_name churned across the restart) must cost
// exactly what a fresh conductor's first encounter with a vanished client
// costs — one tick of churn, then a normal re-adopt and dispatch. It must
// not wedge, and it must not invent a snooze.
func TestConductHandoffRestoredStaleClientReadopts(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := conductor{
		phase:    swEscorting,
		client:   "/dev/ttys099", // not present in the snapshot below
		escortee: "x",
		snoozed:  map[string]swSnooze{},
	}
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeConductHandoff(path, c, now); err != nil {
		t.Fatalf("write: %v", err)
	}
	restored, ok := readConductHandoff(path, now)
	if !ok {
		t.Fatal("handoff not readable")
	}
	s := swSnapshot{
		Sessions: []swSession{waiting("x", 100)},
		Lobby:    "switchboard",
		Clients:  map[string]string{"/dev/ttys001": "switchboard"},
	}
	// Stale client: treated as churn, same as a fresh conductor facing a
	// vanished client — no dispatch this tick, no bogus snooze.
	if _, ok := restored.step(s, now); ok {
		t.Fatal("stale-client tick: no dispatch yet")
	}
	if restored.client != "/dev/ttys001" {
		t.Errorf("client = %q, want re-adopted /dev/ttys001", restored.client)
	}
	if len(restored.snoozed) != 0 {
		t.Errorf("stale client churn must not snooze anything, got %v", restored.snoozed)
	}
	// Next tick: the re-adopted client at the lobby collects the waiting session.
	act, ok := restored.step(s, now)
	if !ok || act.Client != "/dev/ttys001" || act.Target != "x" {
		t.Errorf("re-adopted client must dispatch, act=%+v ok=%v", act, ok)
	}
}

// Deferring the session you are sitting in is the user saying "I am blocked
// here, take me on" — the escort ends on that same tick, exactly as a
// hand-back does, and the queue head collects them.
func TestConductorEscortDeferDispatchesToNextWaiter(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100), waiting("b", 200)), now)
	act, ok := c.step(snapAt("a", deferredWaiting("a", 100), waiting("b", 200)), now.Add(time.Second))
	if !ok || act.Target != "b" {
		t.Fatalf("act = %+v ok=%v, want a dispatch to b", act, ok)
	}
	if c.phase != swEscorting || c.escortee != "b" {
		t.Errorf("phase=%v escortee=%q, want escorting/b", c.phase, c.escortee)
	}
}

// Nothing else waiting: the lobby is where a defer leaves you.
func TestConductorEscortDeferReturnsToLobbyWhenQueueEmpty(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100), busy("b")), now)
	act, ok := c.step(snapAt("a", deferredWaiting("a", 100), busy("b")), now.Add(time.Second))
	if !ok || act.Target != "switchboard" {
		t.Fatalf("act = %+v ok=%v, want a return to the lobby", act, ok)
	}
	if c.phase != swParked || c.escortee != "" {
		t.Errorf("phase=%v escortee=%q, want parked/empty", c.phase, c.escortee)
	}
	// Parked with the mark still set: the deferred session must not collect
	// them straight back (it is not in the queue at all).
	if act, ok := c.step(snapAt("switchboard", deferredWaiting("a", 100), busy("b")), now.Add(2*time.Second)); ok {
		t.Errorf("deferred session pulled the client back: %+v", act)
	}
}

// The sole-session hold yields to a defer: a one-row lobby is a poor
// destination, but being pinned to the session you just marked blocked is a
// worse one, and the lobby is what the user asked for.
func TestConductorSoleSessionDeferReturnsToLobby(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100)), now)
	act, ok := c.step(snapAt("a", deferredWaiting("a", 100)), now.Add(time.Second))
	if !ok || act.Target != "switchboard" {
		t.Fatalf("act = %+v ok=%v, want a return to the lobby", act, ok)
	}
	if c.phase != swParked || c.escortee != "" {
		t.Errorf("phase=%v escortee=%q, want parked/empty", c.phase, c.escortee)
	}
}

// Same key, same meaning when the user walked in themselves rather than
// being escorted: a defer pressed while paused hands them on.
func TestPausedDeferDispatchesToWaiting(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", waiting("b", 50))
	c.step(snapAt("b", waiting("b", 50), waiting("a", 100)), now) // first look
	act, ok := c.step(snapAt("b", deferredWaiting("b", 50), waiting("a", 100)), now.Add(time.Second))
	if !ok || act.Target != "a" {
		t.Fatalf("act = %+v ok=%v, want a dispatch to a", act, ok)
	}
	if c.phase != swEscorting || c.escortee != "a" {
		t.Errorf("phase=%v escortee=%q, want escorting/a", c.phase, c.escortee)
	}
}

func TestPausedDeferReturnsToLobbyWhenQueueEmpty(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", waiting("b", 50), busy("c2"))
	c.step(snapAt("b", waiting("b", 50), busy("c2")), now) // first look
	act, ok := c.step(snapAt("b", deferredWaiting("b", 50), busy("c2")), now.Add(time.Second))
	if !ok || act.Target != "switchboard" {
		t.Fatalf("act = %+v ok=%v, want a return to the lobby", act, ok)
	}
	if c.phase != swParked {
		t.Errorf("phase = %v, want parked", c.phase)
	}
}

// Walking into a session that was ALREADY deferred is deliberate — going
// there to unblock it, say. Only a defer pressed under the client counts, so
// the conductor leaves them be.
func TestPausedPreexistingDeferDoesNotYank(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", deferredWaiting("b", 50), waiting("a", 100))
	for i := 0; i < 3; i++ {
		s := snapAt("b", deferredWaiting("b", 50), waiting("a", 100))
		if act, ok := c.step(s, now.Add(time.Duration(i)*time.Second)); ok {
			t.Fatalf("tick %d: a session deferred before the user arrived must not move them: %+v", i, act)
		}
	}
	if c.phase != swPaused {
		t.Errorf("phase = %v, want paused", c.phase)
	}
}

// Clearing and re-setting the mark is two separate "take me on" presses.
func TestPausedDeferClearedThenSetFiresAgain(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", waiting("b", 50), busy("c2"))
	c.step(snapAt("b", waiting("b", 50), busy("c2")), now) // first look
	if _, ok := c.step(snapAt("b", deferredWaiting("b", 50), busy("c2")), now.Add(time.Second)); !ok {
		t.Fatal("first defer must move the client")
	}
	// The user comes back and un-defers, then defers again later.
	c.step(snapAt("b", waiting("b", 50), busy("c2")), now.Add(2*time.Second)) // paused, first look
	c.step(snapAt("b", waiting("b", 50), busy("c2")), now.Add(3*time.Second))
	act, ok := c.step(snapAt("b", deferredWaiting("b", 50), busy("c2")), now.Add(4*time.Second))
	if !ok || act.Target != "switchboard" {
		t.Fatalf("act = %+v ok=%v, want a second return to the lobby", act, ok)
	}
}

// starting is a session whose head is bound to the waiting placeholder: the
// pane map names a session id whose transcript is not on disk yet.
func starting(name string) swSession {
	return swSession{Name: name, State: "Starting", Since: time.Unix(1754700000, 0)}
}

// `/clear` rotates the session id. Claude Code fires SessionStart with the
// new id before it creates the new transcript, so for a poll or two the
// head publishes Starting. That is neither "waiting" nor "working": the
// human is sitting at a prompt they just cleared. Reading it as a hand-back
// escorted them out of the session they had cleared to keep working in.
func TestConductorEscortHoldsThroughStarting(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100), waiting("b", 200)), now)
	if _, ok := c.step(snapAt("a", starting("a"), waiting("b", 200)), now.Add(time.Second)); ok {
		t.Fatal("a session booting under the user has not been handed back")
	}
	if c.phase != swEscorting || c.escortee != "a" {
		t.Fatalf("phase=%v escortee=%q, want still escorting a", c.phase, c.escortee)
	}
	// The cleared session comes back as Idle with a fresh episode: still
	// nothing to do.
	if _, ok := c.step(snapAt("a", waiting("a", 300), waiting("b", 200)), now.Add(2*time.Second)); ok {
		t.Fatal("cleared session waiting again must still hold")
	}
	// Only a real prompt moves the user on.
	act, ok := c.step(snapAt("a", busy("a"), waiting("b", 200)), now.Add(3*time.Second))
	if !ok || act.Target != "b" {
		t.Fatalf("hand-back after the clear must dispatch to b, got %+v ok=%v", act, ok)
	}
}

// Same rotation with the queue empty and a fleet of two: leaving for the
// lobby is exactly the ejection the user complained about.
func TestConductorEscortStartingDoesNotReturnToLobby(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100), busy("b")), now)
	if act, ok := c.step(snapAt("a", starting("a"), busy("b")), now.Add(time.Second)); ok {
		t.Fatalf("must not leave a starting escortee, got %+v", act)
	}
	if c.phase != swEscorting {
		t.Errorf("phase = %v, want escorting", c.phase)
	}
}

// Paused: the user walked into a waiting session themselves and cleared it.
// Starting is not the waiting → not-waiting edge the hand-back latch keys
// on, so a waiter appearing afterwards must not collect them.
func TestPausedStartingDoesNotLatchHandBack(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", waiting("b", 50))
	c.step(snapAt("b", waiting("b", 50)), now)
	if _, ok := c.step(snapAt("b", starting("b"), waiting("a", 100)), now.Add(time.Second)); ok {
		t.Fatal("clear must not dispatch")
	}
	if _, ok := c.step(snapAt("b", waiting("b", 300), waiting("a", 100)), now.Add(2*time.Second)); ok {
		t.Fatal("cleared session waiting again must not dispatch")
	}
	if c.pausedHandedBack {
		t.Error("hand-back latched across a clear")
	}
	// A prompt typed into the cleared session is the real hand-back.
	act, ok := c.step(snapAt("b", busy("b"), waiting("a", 100)), now.Add(3*time.Second))
	if !ok || act.Target != "a" {
		t.Fatalf("prompt after clear must dispatch to a, got %+v ok=%v", act, ok)
	}
}

// Idle → Starting → Thinking with no Idle tick in between (a fast poll
// straddled the clear): the prompt is still a hand-back, so the waiting
// observation must survive the Starting tick rather than reset to false.
func TestPausedHandBackSurvivesStartingTick(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", waiting("b", 50))
	c.step(snapAt("b", waiting("b", 50), waiting("a", 100)), now)
	c.step(snapAt("b", starting("b"), waiting("a", 100)), now.Add(time.Second))
	act, ok := c.step(snapAt("b", busy("b"), waiting("a", 100)), now.Add(2*time.Second))
	if !ok || act.Target != "a" {
		t.Fatalf("hand-back must dispatch to a, got %+v ok=%v", act, ok)
	}
}
