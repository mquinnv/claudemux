package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNoteContinuationsRecordsSuccessor(t *testing.T) {
	events := []Event{
		{Type: "assistant", UserText: "done"},
		{Type: "continued-in", ContinuedIn: "new-id"},
	}
	got := noteContinuations(nil, "old-id", events)
	if got["old-id"] != "new-id" {
		t.Errorf("superseded[old-id] = %q, want new-id", got["old-id"])
	}
	// Nothing to note leaves the map untouched (and nil stays nil).
	if m := noteContinuations(nil, "x", []Event{{Type: "user"}}); m != nil {
		t.Errorf("got %v, want nil for events with no continuation", m)
	}
	// A record naming the session itself is noise, not a cycle.
	if m := noteContinuations(nil, "same", []Event{{ContinuedIn: "same"}}); m != nil {
		t.Errorf("got %v, want nil for a self-continuation", m)
	}
}

// followContinuation walks path through the successors on disk: the file
// the harness named must exist, or the head keeps what it has.
func TestFollowContinuation(t *testing.T) {
	projects := t.TempDir()
	proj := filepath.Join(projects, "-Users-x-proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(proj, "old-id.jsonl")
	next := filepath.Join(proj, "new-id.jsonl")
	for _, p := range []string{old, next} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	superseded := map[string]string{"old-id": "new-id"}

	if got := followContinuation(old, superseded, projects); got != next {
		t.Errorf("followed to %q, want %q", got, next)
	}
	if got := followContinuation(next, superseded, projects); got != next {
		t.Errorf("successor itself resolved to %q, want unchanged", got)
	}
	if got := followContinuation(old, nil, projects); got != old {
		t.Errorf("no map: got %q, want unchanged", got)
	}
	// Successor not on disk yet (the fork's file appears later): stay put.
	if got := followContinuation(old, map[string]string{"old-id": "missing"}, projects); got != old {
		t.Errorf("missing successor: got %q, want unchanged", got)
	}
}

// Two hops resolve (a fork of a fork); a cycle terminates.
func TestFollowContinuationChainsAndTerminates(t *testing.T) {
	projects := t.TempDir()
	proj := filepath.Join(projects, "p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(proj, id+".jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	chain := map[string]string{"a": "b", "b": "c"}
	if got := followContinuation(filepath.Join(proj, "a.jsonl"), chain, projects); got != filepath.Join(proj, "c.jsonl") {
		t.Errorf("chain resolved to %q, want c.jsonl", got)
	}
	cycle := map[string]string{"a": "b", "b": "a"}
	got := followContinuation(filepath.Join(proj, "a.jsonl"), cycle, projects)
	if got != filepath.Join(proj, "a.jsonl") && got != filepath.Join(proj, "b.jsonl") {
		t.Errorf("cycle resolved to %q, want one of a/b (and to return at all)", got)
	}
}

// The scenario that stuck ag-admin on 2026-09-04: the pane map names the
// parked file, the fork's file exists, and the head is currently bound to
// the parked file. Adopt the fork — and once on it, do not bounce back.
func TestResolveActiveTranscriptFollowsPark(t *testing.T) {
	projects := t.TempDir()
	proj := filepath.Join(projects, "p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	parked := filepath.Join(proj, "ebe355f0.jsonl")
	fork := filepath.Join(proj, "6de04257.jsonl")
	for _, p := range []string{parked, fork} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	superseded := map[string]string{"ebe355f0": "6de04257"}

	if got := resolveActiveTranscript(parked, parked, superseded, projects); got != fork {
		t.Errorf("bound to parked, map says parked: got %q, want fork", got)
	}
	if got := resolveActiveTranscript(parked, fork, superseded, projects); got != "" {
		t.Errorf("bound to fork, map still says parked: got %q, want \"\" (stay)", got)
	}
	if got := resolveActiveTranscript("", parked, superseded, projects); got != fork {
		t.Errorf("no map, bound to parked: got %q, want fork", got)
	}
	if got := resolveActiveTranscript("", parked, nil, projects); got != "" {
		t.Errorf("nothing recorded: got %q, want \"\"", got)
	}
	// Ordinary rotation is untouched: a different mapped file with no
	// continuation involved is still adopted.
	other := filepath.Join(proj, "other.jsonl")
	if got := resolveActiveTranscript(other, parked, nil, projects); got != other {
		t.Errorf("plain rotation: got %q, want %q", got, other)
	}
}
