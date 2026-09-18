package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func handoffFixture(now time.Time) conductor {
	return conductor{
		phase:            swEscorting,
		client:           "/dev/ttys013",
		escortee:         "phenix",
		snoozed:          map[string]swSnooze{"ag-admin": {since: now.Add(-time.Hour), at: now.Add(-time.Minute)}},
		pausedCur:        "gh-hud",
		pausedCurWaiting: true,
		pausedHandedBack: true,
	}
}

func TestConductHandoffRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeConductHandoff(path, handoffFixture(now), now); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok := readConductHandoff(path, now.Add(time.Second))
	if !ok {
		t.Fatal("read: ok=false, want true")
	}
	want := handoffFixture(now)
	if got.phase != want.phase || got.escortee != want.escortee {
		t.Errorf("phase/escortee = %v/%q, want %v/%q", got.phase, got.escortee, want.phase, want.escortee)
	}
	sn, hit := got.snoozed["ag-admin"]
	if !hit {
		t.Fatalf("snoozed lost: %#v", got.snoozed)
	}
	if !sn.since.Equal(want.snoozed["ag-admin"].since) || !sn.at.Equal(want.snoozed["ag-admin"].at) {
		t.Errorf("snooze = %v/%v, want %v/%v", sn.since, sn.at,
			want.snoozed["ag-admin"].since, want.snoozed["ag-admin"].at)
	}
	if got.pausedCur != want.pausedCur || !got.pausedCurWaiting || !got.pausedHandedBack {
		t.Errorf("paused observation lost: %q %v %v", got.pausedCur, got.pausedCurWaiting, got.pausedHandedBack)
	}
}

// The handoff is consumed once: a second lobby starting later must not inherit
// a phase that belongs to a process that is already running.
func TestConductHandoffIsOneShot(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeConductHandoff(path, handoffFixture(now), now); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := readConductHandoff(path, now); !ok {
		t.Fatal("first read: ok=false, want true")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still present after read: err=%v", err)
	}
	if _, ok := readConductHandoff(path, now); ok {
		t.Error("second read: ok=true, want false")
	}
}

// An old file is a crash leftover, not a handoff. Resuming a half-hour-old
// escort would fight the user rather than continue their session.
func TestConductHandoffExpires(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeConductHandoff(path, handoffFixture(now), now); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := readConductHandoff(path, now.Add(conductHandoffTTL+time.Second)); ok {
		t.Error("expired handoff: ok=true, want false")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expired file not cleaned up: err=%v", err)
	}
}

func TestConductHandoffMissing(t *testing.T) {
	if _, ok := readConductHandoff(filepath.Join(t.TempDir(), "nope.json"), time.Now()); ok {
		t.Error("missing handoff: ok=true, want false")
	}
}

// writeConductHandoff and readConductHandoff enumerate conductor's fields by
// hand, because they are unexported and encoding/json cannot see them. That
// makes a field added to the struct later silently reset on every restart —
// the exact class of bug (state quietly lost across a re-exec) this file exists
// to remove. So pin the shape: when this fails, read the new field, decide
// whether the handoff should carry it, and only then update the count.
func TestConductorFieldsAreAccountedForInHandoff(t *testing.T) {
	// carried: phase, escortee, snoozed, pausedCur, pausedCurWaiting,
	// pausedHandedBack. Deliberately not carried: client — resolveClient
	// re-adopts on the first tick, and a stale client name is worse than looking.
	const accountedFor = 7
	if got := reflect.TypeOf(conductor{}).NumField(); got != accountedFor {
		t.Fatalf("conductor has %d fields, the handoff accounts for %d — decide whether the new field belongs in writeConductHandoff/readConductHandoff (and in this count) before changing this number", got, accountedFor)
	}
}
