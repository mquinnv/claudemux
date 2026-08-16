package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
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
	// Absence means "you won't be yanked anywhere": standby, a dead lobby's
	// stale publish, and no lobby at all are indistinguishable on purpose.
	for name, raw := range map[string]string{
		"standby": fresh("standby"),
		"stale":   fmt.Sprintf("conducting %d", now.Add(-time.Minute).Unix()),
		"absent":  "",
	} {
		if got := conductChip(raw, now); got != "" {
			t.Errorf("conductChip(%s) = %q, want empty", name, got)
		}
	}
}

// The head's state line carries the conduct chip while the lobby is live and
// not in standby, and drops it otherwise.
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
	if got := renderStateLine(m, now); strings.Contains(got, "conduct") {
		t.Errorf("renderStateLine = %q, want no conduct chip in standby", got)
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
