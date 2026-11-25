package main

import (
	"testing"
	"time"
)

func rcSess(name, state string, since time.Time) swSession {
	return swSession{Name: name, State: state, Since: since}
}

func TestRemoteCursorPinsHead(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	sessions := []swSession{
		rcSess("busy", "Tool:Bash", t0),
		rcSess("b", "Idle", t0.Add(2*time.Minute)),
		rcSess("a", "Awaiting", t0.Add(time.Minute)),
	}
	var c remoteCursor
	s, pos, total, ok := c.pick(sessions, t0.Add(5*time.Minute))
	if !ok || s.Name != "a" || pos != 1 || total != 2 {
		t.Fatalf("got %q pos=%d total=%d ok=%v", s.Name, pos, total, ok)
	}
	if _, _, _, ok := (&remoteCursor{}).pick(nil, t0); ok {
		t.Fatal("empty fleet reported ok")
	}
}

func TestRemoteCursorKeepsPinWhenNewArrives(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	now := t0.Add(5 * time.Minute)
	var c remoteCursor
	c.pick([]swSession{rcSess("a", "Idle", t0.Add(time.Minute))}, now)
	sessions := []swSession{
		rcSess("a", "Idle", t0.Add(time.Minute)),
		rcSess("older", "Idle", t0),
	}
	s, pos, total, ok := c.pick(sessions, now)
	if !ok || s.Name != "a" || pos != 2 || total != 2 {
		t.Fatalf("got %q pos=%d total=%d ok=%v", s.Name, pos, total, ok)
	}
}

func TestRemoteCursorMovesOnWhenAnswered(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	now := t0.Add(5 * time.Minute)
	var c remoteCursor
	c.pick([]swSession{rcSess("a", "Idle", t0), rcSess("b", "Idle", t0.Add(time.Minute))}, now)
	sessions := []swSession{rcSess("a", "Tool:Bash", t0), rcSess("b", "Idle", t0.Add(time.Minute))}
	s, pos, total, ok := c.pick(sessions, now)
	if !ok || s.Name != "b" || pos != 1 || total != 1 {
		t.Fatalf("got %q pos=%d total=%d ok=%v", s.Name, pos, total, ok)
	}
	// A new episode of the pinned session is a different Since: re-pick.
	c.pick([]swSession{rcSess("b", "Idle", t0.Add(time.Minute)), rcSess("c", "Idle", t0)}, now)
	s, _, _, _ = c.pick([]swSession{rcSess("b", "Idle", t0.Add(3*time.Minute)), rcSess("c", "Idle", t0)}, now)
	if s.Name != "c" {
		t.Fatalf("new episode kept the pin: %q", s.Name)
	}
}

func TestRemoteCursorSkipExpires(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	sessions := []swSession{rcSess("a", "Idle", t0), rcSess("b", "Idle", t0.Add(time.Minute))}
	now := t0.Add(5 * time.Minute)
	var c remoteCursor
	s, _, _, _ := c.pick(sessions, now)
	c.skip(s.Name, s.Since, now)
	s, pos, total, _ := c.pick(sessions, now.Add(time.Second))
	if s.Name != "b" || pos != 1 || total != 1 {
		t.Fatalf("after skip got %q pos=%d total=%d", s.Name, pos, total)
	}
	// Past the TTL, a is queued again, ahead of the still-pinned b.
	s, pos, total, _ = c.pick(sessions, now.Add(swSnoozeTTL))
	if s.Name != "b" || pos != 2 || total != 2 {
		t.Fatalf("after TTL got %q pos=%d total=%d", s.Name, pos, total)
	}
	if len(c.skips) != 0 {
		t.Fatalf("expired skip kept: %v", c.skips)
	}
}

func TestRemoteCursorSkipsReleasedWhenQueueEmpty(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	now := t0.Add(time.Minute)
	sessions := []swSession{rcSess("a", "Idle", t0)}
	var c remoteCursor
	s, _, _, _ := c.pick(sessions, now)
	c.skip(s.Name, s.Since, now)
	s, pos, total, ok := c.pick(sessions, now.Add(time.Second))
	if !ok || s.Name != "a" || pos != 1 || total != 1 {
		t.Fatalf("got %q pos=%d total=%d ok=%v", s.Name, pos, total, ok)
	}
	if len(c.skips) != 0 {
		t.Fatalf("skips not released: %v", c.skips)
	}
	// A deferred session is never released into the queue.
	d := swSession{Name: "d", State: "Idle", Since: t0, Deferred: true}
	var c2 remoteCursor
	c2.skip("d", t0, now)
	if _, _, _, ok := c2.pick([]swSession{d}, now); ok {
		t.Fatal("deferred session surfaced")
	}
}

func TestRemoteCursorOpenNonWaiting(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	sessions := []swSession{rcSess("a", "Idle", t0), rcSess("busy", "Tool:Bash", t0)}
	var c remoteCursor
	c.open("busy")
	s, pos, total, ok := c.pick(sessions, t0.Add(time.Minute))
	if !ok || s.Name != "busy" || pos != 0 || total != 1 {
		t.Fatalf("got %q pos=%d total=%d ok=%v", s.Name, pos, total, ok)
	}
	// Once the opened session is gone the cursor falls back to the head.
	s, pos, _, ok = c.pick(sessions[:1], t0.Add(time.Minute))
	if !ok || s.Name != "a" || pos != 1 {
		t.Fatalf("fallback got %q pos=%d ok=%v", s.Name, pos, ok)
	}
}
