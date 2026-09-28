package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestStayMarked(t *testing.T) {
	yes := []string{
		"#stay",
		"run the tests #stay",
		"#stay run the tests",
		"run the tests #STAY",
		"run the tests #Stay",
		"  run the tests\n\n#stay\n",
		"#stay\tfix it",
		"line one\nline two #stay",
	}
	for _, p := range yes {
		if !stayMarked(p) {
			t.Errorf("stayMarked(%q) = false, want true", p)
		}
	}
	no := []string{
		"",
		"   ",
		"run the tests",
		"stay here",               // no hash
		"# Stay",                  // markdown heading
		"## stay\n\nsome text",    // heading, even lowercase
		"fix it #stayed",          // longer word
		"fix it #stay-alive",      // hyphenated tag
		"fix it #stay.",           // punctuation glued on
		"open page.html#stay",     // URL fragment
		"say `#stay` to skip",     // quoted in backticks, mid-prompt
		"fix it `#stay`",          // quoted in backticks, at the end
		"use #stay mid-prompt ok", // mid-text: only first/last token counts
		"paste:\n#stay\nmore log", // mid-text across lines
		"#stays",
		"＃stay", // fullwidth hash
	}
	for _, p := range no {
		if stayMarked(p) {
			t.Errorf("stayMarked(%q) = true, want false", p)
		}
	}
}

// Stay parses from the twelfth field: "1" is marked, anything else is not.
func TestBuildSwSnapshotParsesStay(t *testing.T) {
	sessOut := "api\tThinking\t1754700000\t37\t\t\t\t\t\t\t\t1\n" +
		"web\tThinking\t1754700000\t37\t\t\t\t\t\t\t\t\n" +
		"scratch\tThinking\t1754700000\t37\t\t\t\t\t\t\t\t0\n"
	paneOut := "api\t%1\tclaudemux-head\tt\n" +
		"web\t%2\tclaudemux-head\tt\n" +
		"scratch\t%3\tclaudemux-head\tt\n"
	s := buildSwSnapshot(sessOut, paneOut, "", "")
	if api, _ := s.session("api"); !api.Stay {
		t.Error("api.Stay = false, want true")
	}
	if web, _ := s.session("web"); web.Stay {
		t.Error("web.Stay = true, want false for an unset option")
	}
	if scratch, _ := s.session("scratch"); scratch.Stay {
		t.Error("scratch.Stay = true, want false for a non-\"1\" value")
	}
}

// A marked prompt typed mid-turn flips the mark under an unchanged busy
// state; that alone must republish (together with the state).
func TestMaybePublishStateRepublishesOnStayFlip(t *testing.T) {
	m := &model{selfPane: "%3", state: State{Kind: StateThinking, Since: time.Unix(1754700000, 0)}, lastTyped: "go"}
	now := time.Now()
	if m.maybePublishState(now) == nil {
		t.Fatal("first publish expected")
	}
	if m.maybePublishState(now) != nil {
		t.Fatal("nothing changed; must not republish")
	}
	m.lastTyped = "and then this #stay"
	if m.maybePublishState(now) == nil || !m.publishedStay {
		t.Fatalf("stay flip must republish, publishedStay=%v", m.publishedStay)
	}
	if m.maybePublishState(now) != nil {
		t.Error("unchanged stay must not republish")
	}
}

func busyStay(name string) swSession {
	s := busy(name)
	s.Stay = true
	return s
}

func TestStayingIn(t *testing.T) {
	if !stayingIn(busyStay("a")) {
		t.Error("busy + marked must be staying")
	}
	if stayingIn(busy("a")) {
		t.Error("unmarked busy is a hand-back, not a stay")
	}
	w := waiting("a", 100)
	w.Stay = true
	if stayingIn(w) {
		t.Error("the mark lingers while waiting but says nothing there")
	}
	st := starting("a")
	st.Stay = true
	if stayingIn(st) {
		t.Error("a booting session is not running a #stay turn")
	}
}

// The core promise: a #stay prompt keeps the user on the session for that
// turn, the turn ending leaves them there as usual, and the next unmarked
// prompt moves them on.
func TestConductorEscortHoldsThroughStayTurn(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100), waiting("b", 200)), now)
	if c.escortee != "a" {
		t.Fatalf("escortee = %q, want a", c.escortee)
	}
	if act, ok := c.step(snapAt("a", busyStay("a"), waiting("b", 200)), now.Add(time.Second)); ok {
		t.Fatalf("a #stay prompt must not escort, got %+v", act)
	}
	if c.phase != swEscorting || c.escortee != "a" {
		t.Fatalf("phase=%v escortee=%q, want still escorting a", c.phase, c.escortee)
	}
	// Turn ends: waiting on the user again, mark still set (it lingers until
	// the next typed prompt) — hold, as for any waiting escortee.
	w := waiting("a", 300)
	w.Stay = true
	if act, ok := c.step(snapAt("a", w, waiting("b", 200)), now.Add(2*time.Second)); ok {
		t.Fatalf("turn over, still waiting on the user: must hold, got %+v", act)
	}
	// Next prompt, unmarked: the head publishes state and mark together, so
	// the lobby sees busy with Stay cleared — a normal hand-back.
	act, ok := c.step(snapAt("a", busy("a"), waiting("b", 200)), now.Add(3*time.Second))
	if !ok || act.Target != "b" {
		t.Fatalf("unmarked prompt must escort to b, got %+v ok=%v", act, ok)
	}
}

// With nothing else waiting, a #stay turn must not bounce the user to the
// lobby either.
func TestConductorStayTurnDoesNotReturnToLobby(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100), busy("b")), now)
	if act, ok := c.step(snapAt("a", busyStay("a"), busy("b")), now.Add(time.Second)); ok {
		t.Fatalf("must not leave a #stay escortee for the lobby, got %+v", act)
	}
}

// Defer overrides the stay, as it overrides every other hold: marking the
// session blocked is an explicit "take me on".
func TestConductorDeferOverridesStay(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100), waiting("b", 200)), now)
	a := busyStay("a")
	a.Deferred = true
	act, ok := c.step(snapAt("a", a, waiting("b", 200)), now.Add(time.Second))
	if !ok || act.Target != "b" {
		t.Fatalf("defer during a #stay turn must move on to b, got %+v ok=%v", act, ok)
	}
}

// Walking away during a #stay turn is still a walk-away: the stay is a
// promise not to move the user, not a leash.
func TestConductorWalkAwayDuringStay(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100), waiting("b", 200)), now)
	c.step(snapAt("a", busyStay("a"), waiting("b", 200)), now.Add(time.Second))
	if _, ok := c.step(snapAt("switchboard", busyStay("a"), waiting("b", 200)), now.Add(2*time.Second)); ok {
		t.Fatal("the walk-away tick itself must not act")
	}
	if c.phase != swParked || c.escortee != "" {
		t.Errorf("phase=%v escortee=%q, want parked with no escortee", c.phase, c.escortee)
	}
}

// Paused: the user walked into a waiting session themselves. A #stay prompt
// is not the hand-back edge, so a waiter must not collect them; the next
// unmarked prompt is.
func TestPausedStayDoesNotLatchHandBack(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", waiting("b", 50))
	c.step(snapAt("b", waiting("b", 50)), now)
	if act, ok := c.step(snapAt("b", busyStay("b"), waiting("a", 100)), now.Add(time.Second)); ok {
		t.Fatalf("#stay prompt must not dispatch, got %+v", act)
	}
	if c.pausedHandedBack {
		t.Fatal("#stay prompt latched the hand-back")
	}
	w := waiting("b", 300)
	w.Stay = true
	if _, ok := c.step(snapAt("b", w, waiting("a", 100)), now.Add(2*time.Second)); ok {
		t.Fatal("turn over, waiting again: must not dispatch")
	}
	act, ok := c.step(snapAt("b", busy("b"), waiting("a", 100)), now.Add(3*time.Second))
	if !ok || act.Target != "a" {
		t.Fatalf("unmarked prompt must dispatch to a, got %+v ok=%v", act, ok)
	}
}

// Marked prompt with no waiting tick seen in between (fast poll): the
// waiting observed before the #stay turn still pairs with the unmarked
// prompt after it.
func TestPausedHandBackSurvivesStayTurnWithoutIdleTick(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", waiting("b", 50))
	c.step(snapAt("b", waiting("b", 50), waiting("a", 100)), now)
	c.step(snapAt("b", busyStay("b"), waiting("a", 100)), now.Add(time.Second))
	act, ok := c.step(snapAt("b", busy("b"), waiting("a", 100)), now.Add(2*time.Second))
	if !ok || act.Target != "a" {
		t.Fatalf("hand-back must dispatch to a, got %+v ok=%v", act, ok)
	}
}

func TestConductorStatusLineStaying(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100), waiting("b", 200)), now)
	s := snapAt("a", busyStay("a"), waiting("b", 200))
	c.step(s, now.Add(time.Second))
	if got := c.statusLine(s, now.Add(time.Second)); !strings.Contains(got, "staying on a (#stay)") {
		t.Errorf("statusLine = %q, want a #stay notice", got)
	}
}

func TestConductChipForStaying(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	fresh := func(mode string) string { return fmt.Sprintf("%s %d", mode, now.Unix()) }
	if got := conductChipFor(fresh("conducting"), now, true); !strings.Contains(got, "#stay") {
		t.Errorf("conducting + staying = %q, want a #stay chip", got)
	}
	if got := conductChipFor(fresh("conducting"), now, false); !strings.Contains(got, "conduct") {
		t.Errorf("conducting, not staying = %q, want the conduct chip", got)
	}
	if got := conductChipFor(fresh("standby"), now, true); strings.Contains(got, "#stay") {
		t.Errorf("standby = %q, must read as standby whatever the turn", got)
	}
	if got := conductChipFor("", now, true); got != "" {
		t.Errorf("no lobby = %q, want empty", got)
	}
}

func TestModelStayingTurn(t *testing.T) {
	m := &model{state: State{Kind: StateThinking}, lastTyped: "do it #stay"}
	if !m.stayingTurn() {
		t.Error("busy on a marked prompt must be a staying turn")
	}
	m.state = State{Kind: StateIdle}
	if m.stayingTurn() {
		t.Error("an idle session is not in a staying turn")
	}
	m.state, m.lastTyped = State{Kind: StateThinking}, "do it"
	if m.stayingTurn() {
		t.Error("unmarked prompt is not a staying turn")
	}
}
