package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestConductMode(t *testing.T) {
	cases := []struct {
		name    string
		standby bool
		phase   swPhase
		want    string
	}{
		{"parked", false, swParked, "conducting"},
		{"escorting", false, swEscorting, "conducting"},
		{"paused", false, swPaused, "paused"},
		// Standby wins over every phase: space neutralizes the conductor
		// regardless of what it was doing.
		{"standby parked", true, swParked, "standby"},
		{"standby paused", true, swPaused, "standby"},
	}
	for _, c := range cases {
		if got := conductMode(c.standby, c.phase); got != c.want {
			t.Errorf("%s: conductMode = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestConductPublishValue(t *testing.T) {
	now := time.Unix(1754700000, 0)
	if got := conductPublishValue("conducting", now); got != "conducting 1754700000" {
		t.Errorf("conductPublishValue = %q, want %q", got, "conducting 1754700000")
	}
}

func TestParseConductValue(t *testing.T) {
	now := time.Unix(1754700000, 0)
	fresh := fmt.Sprintf("conducting %d", now.Unix())
	cases := []struct {
		name     string
		value    string
		wantMode string
		wantOK   bool
	}{
		{"fresh", fresh, "conducting", true},
		{"fresh standby", fmt.Sprintf("standby %d", now.Unix()), "standby", true},
		// A lobby that died leaves its last publish behind; past the
		// staleness window the option must read as "no lobby".
		{"stale", fmt.Sprintf("conducting %d", now.Add(-conductStaleAfter-time.Second).Unix()), "", false},
		// Just inside the window still counts.
		{"aging but fresh", fmt.Sprintf("conducting %d", now.Add(-conductStaleAfter+time.Second).Unix()), "conducting", true},
		// Small clock skew forward must not read as stale.
		{"slightly future", fmt.Sprintf("conducting %d", now.Add(2*time.Second).Unix()), "conducting", true},
		{"empty", "", "", false},
		{"no timestamp", "conducting", "", false},
		{"bad timestamp", "conducting soon", "", false},
		{"extra field", "conducting 1754700000 extra", "", false},
	}
	for _, c := range cases {
		mode, ok := parseConductValue(c.value, now)
		if mode != c.wantMode || ok != c.wantOK {
			t.Errorf("%s: parseConductValue(%q) = (%q, %v), want (%q, %v)",
				c.name, c.value, mode, ok, c.wantMode, c.wantOK)
		}
	}
}

func TestConductPublishArgs(t *testing.T) {
	now := time.Unix(1754700000, 0)
	got := conductPublishArgs("conducting", now)
	want := []string{"set-option", "-g", conductOption, "conducting 1754700000"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("conductPublishArgs = %v, want %v", got, want)
	}
}

func TestConductChip(t *testing.T) {
	now := time.Unix(1754700000, 0)
	fresh := func(mode string) string { return fmt.Sprintf("%s %d", mode, now.Unix()) }
	if got := conductChip(fresh("conducting"), now); !strings.Contains(got, "conduct") {
		t.Errorf("conductChip(conducting) = %q, want a visible conduct chip", got)
	}
	// Paused still shows: conduct mode is on, and handing this session back
	// can still dispatch the client elsewhere.
	if got := conductChip(fresh("paused"), now); !strings.Contains(got, "conduct") {
		t.Errorf("conductChip(paused) = %q, want a visible conduct chip", got)
	}
	// Standby is a state the user chose, so it reads back rather than going
	// blank — and it must not read as conducting.
	got := conductChip(fresh("standby"), now)
	if !strings.Contains(got, "stay") {
		t.Errorf("conductChip(standby) = %q, want a visible stay chip", got)
	}
	if strings.Contains(got, "conduct") {
		t.Errorf("conductChip(standby) = %q, must not read as conducting", got)
	}
	// Absence means "there is no lobby to report on": a dead lobby's stale
	// publish and no lobby at all stay indistinguishable on purpose.
	for name, raw := range map[string]string{
		"stale":  fmt.Sprintf("conducting %d", now.Add(-time.Minute).Unix()),
		"absent": "",
	} {
		if got := conductChip(raw, now); got != "" {
			t.Errorf("conductChip(%s) = %q, want empty", name, got)
		}
	}
}

func TestConductRequestValue(t *testing.T) {
	now := time.Unix(1754700000, 123456789)
	if got, want := conductRequestValue(now), "toggle 1754700000123456789"; got != want {
		t.Errorf("conductRequestValue = %q, want %q", got, want)
	}
	// Two presses inside the same second must be distinguishable, or the
	// lobby's already-applied check would swallow the second one.
	a := conductRequestValue(now)
	b := conductRequestValue(now.Add(time.Millisecond))
	if a == b {
		t.Errorf("requests a millisecond apart share a token: %q", a)
	}
}

func TestParseConductRequest(t *testing.T) {
	now := time.Unix(1754700000, 0)
	at := func(d time.Duration) string { return conductRequestValue(now.Add(d)) }
	cases := []struct {
		name   string
		value  string
		wantOK bool
	}{
		{"fresh", at(0), true},
		{"aging but fresh", at(-conductStaleAfter + time.Second), true},
		// A request left behind by a head whose lobby was down must not fire
		// minutes later when a lobby finally starts.
		{"stale", at(-conductStaleAfter - time.Second), false},
		// Clock skew forward reads as fresh, same as the heartbeat's.
		{"slightly future", at(2 * time.Second), true},
		{"empty", "", false},
		{"no timestamp", "toggle", false},
		{"bad timestamp", "toggle soon", false},
		{"extra field", "toggle 1754700000000000000 extra", false},
		// Only one verb exists; anything else is a newer head talking a
		// protocol this lobby doesn't know, and is ignored rather than guessed.
		{"unknown verb", "standby 1754700000000000000", false},
	}
	for _, c := range cases {
		tok, ok := parseConductRequest(c.value, now)
		if ok != c.wantOK {
			t.Errorf("%s: parseConductRequest(%q) ok = %v, want %v", c.name, c.value, ok, c.wantOK)
		}
		if ok && tok != c.value {
			t.Errorf("%s: token = %q, want the raw value %q", c.name, tok, c.value)
		}
	}
}

func TestConductRequestArgs(t *testing.T) {
	now := time.Unix(1754700000, 0)
	got := conductRequestArgs(now)
	want := []string{"set-option", "-g", conductRequestOption, "toggle 1754700000000000000"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("conductRequestArgs = %v, want %v", got, want)
	}
}

func TestConductOn(t *testing.T) {
	for mode, want := range map[string]bool{
		"conducting": true,
		// Paused is conduct-on: handing this session back can still dispatch.
		"paused":  true,
		"standby": false,
		"":        false,
	} {
		if got := conductOn(mode); got != want {
			t.Errorf("conductOn(%q) = %v, want %v", mode, got, want)
		}
	}
}

// Space in a head asks the lobby to flip conduct mode, and flips the local
// chip immediately rather than waiting out two poll beats.
func TestHeadSpaceRequestsConductToggle(t *testing.T) {
	now := time.Now()
	m := model{conductRaw: fmt.Sprintf("conducting %d", now.Unix())}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if cmd == nil {
		t.Fatal("space with a live lobby must publish a toggle request")
	}
	m = next.(model)
	if m.conductPendingMode != "standby" {
		t.Errorf("conductPendingMode = %q, want standby", m.conductPendingMode)
	}
	if got := conductChip(m.conductRawFor(now), now); !strings.Contains(got, "stay") {
		t.Errorf("chip = %q, want it reading stay the instant standby was asked for", got)
	}
	// And back: the pending mode is derived from what is on screen now, so a
	// second press reverses the first even before the lobby has answered.
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if cmd == nil {
		t.Fatal("a second space must publish its own request")
	}
	m = next.(model)
	if m.conductPendingMode != "conducting" {
		t.Errorf("conductPendingMode = %q, want conducting", m.conductPendingMode)
	}
	// The shape a real terminal delivers: bubbletea gives a typed space its own
	// KeySpace type, which the key switch must catch as well as the synthetic
	// rune form the cases above use.
	m.conductPendingMode, m.conductPendingUntil = "", time.Time{}
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	if cmd == nil || next.(model).conductPendingMode != "standby" {
		t.Errorf("a KeySpace press must toggle too, got cmd=%v pending=%q",
			cmd != nil, next.(model).conductPendingMode)
	}
}

// With no live lobby there is no conductor to toggle, so space does nothing —
// rather than flashing a chip for a process that isn't running.
func TestHeadSpaceWithoutLobbyIsNoOp(t *testing.T) {
	now := time.Now()
	for name, raw := range map[string]string{
		"absent": "",
		"stale":  fmt.Sprintf("conducting %d", now.Add(-time.Minute).Unix()),
	} {
		m := model{conductRaw: raw}
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
		if cmd != nil {
			t.Errorf("%s: space must not publish a request", name)
		}
		if got := next.(model).conductPendingMode; got != "" {
			t.Errorf("%s: conductPendingMode = %q, want empty", name, got)
		}
	}
}

// The optimistic flip is a loan against the lobby's answer: it holds while the
// request is outstanding, ends the moment a poll agrees, and expires on its own
// if no poll ever does.
func TestHeadConductPendingWindow(t *testing.T) {
	now := time.Now()
	m := model{conductPendingMode: "standby", conductPendingUntil: now.Add(conductPendingWindow)}
	m.conductRaw = fmt.Sprintf("conducting %d", now.Unix())
	if got := conductChip(m.conductRawFor(now), now); !strings.Contains(got, "stay") {
		t.Errorf("chip = %q, want the pending standby to win over a stale read", got)
	}
	// Expired: reality wins again even though nothing cleared the fields.
	late := now.Add(conductPendingWindow + time.Second)
	m.conductRaw = fmt.Sprintf("conducting %d", late.Unix())
	if got := conductChip(m.conductRawFor(late), late); !strings.Contains(got, "conduct") {
		t.Errorf("chip = %q, want an unanswered request to expire back to what the lobby says", got)
	}
}

// A poll that confirms the requested mode retires the pending flip; one that
// still shows the old mode leaves it standing until the window closes.
func TestHeadConductPendingClearedByPoll(t *testing.T) {
	now := time.Now()
	m := model{conductPendingMode: "standby", conductPendingUntil: now.Add(conductPendingWindow)}
	next, _ := m.Update(dataMsg{time: now, conductRaw: fmt.Sprintf("conducting %d", now.Unix())})
	if got := next.(model).conductPendingMode; got != "standby" {
		t.Errorf("conductPendingMode = %q, want the request still pending", got)
	}
	next, _ = m.Update(dataMsg{time: now, conductRaw: fmt.Sprintf("standby %d", now.Unix())})
	if got := next.(model).conductPendingMode; got != "" {
		t.Errorf("conductPendingMode = %q, want it retired by the confirming poll", got)
	}
	// Paused confirms a "conducting" request: both mean conduct-on, which is
	// the only distinction the chip draws.
	m.conductPendingMode = "conducting"
	next, _ = m.Update(dataMsg{time: now, conductRaw: fmt.Sprintf("paused %d", now.Unix())})
	if got := next.(model).conductPendingMode; got != "" {
		t.Errorf("conductPendingMode = %q, want paused to confirm a conducting request", got)
	}
}

// The head's state line carries whichever chip the live lobby's mode calls
// for, and nothing at all once no lobby is publishing.
func TestRenderStateLineConductChip(t *testing.T) {
	now := time.Now()
	m := model{
		width:      80,
		state:      State{Kind: StateIdle, Since: now.Add(-30 * time.Second)},
		modelName:  "claude-opus-4-7",
		conductRaw: fmt.Sprintf("conducting %d", now.Unix()),
	}
	if got := renderStateLine(m, now); !strings.Contains(got, "conduct") {
		t.Errorf("renderStateLine = %q, want the conduct chip", got)
	}
	m.conductRaw = fmt.Sprintf("standby %d", now.Unix())
	got := renderStateLine(m, now)
	if strings.Contains(got, "conduct") {
		t.Errorf("renderStateLine = %q, want no conduct chip in standby", got)
	}
	if !strings.Contains(got, "stay") {
		t.Errorf("renderStateLine = %q, want the stay chip in standby", got)
	}
	// No lobby: neither chip, because there is nothing to report on.
	m.conductRaw = ""
	got = renderStateLine(m, now)
	if strings.Contains(got, "conduct") || strings.Contains(got, "stay") {
		t.Errorf("renderStateLine = %q, want no chip with no lobby", got)
	}
}

// The packed single-line statusbar (height 1) carries the same chip.
func TestRenderStatusbarConductChip(t *testing.T) {
	now := time.Now()
	m := model{
		width:      120,
		state:      State{Kind: StateIdle, Since: now.Add(-30 * time.Second)},
		modelName:  "claude-opus-4-7",
		conductRaw: fmt.Sprintf("conducting %d", now.Unix()),
	}
	if got := renderStatusbar(m, now, ""); !strings.Contains(got, "conduct") {
		t.Errorf("renderStatusbar = %q, want the conduct chip", got)
	}
}
