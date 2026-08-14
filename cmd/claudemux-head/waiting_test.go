package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A head started on a brand-new project races Claude Code's boot: claudemux
// launches both panes in the same second, and the transcript only exists once
// claude finishes starting. The head used to exit 1 then ("Make sure you're in
// a directory with an active Claude Code session"), leaving a dead pane on
// every fresh project. Instead it must start in waiting mode — StateWaiting,
// rendered "Starting" — and hold until a transcript appears.
func TestWaitingModeStateIsStarting(t *testing.T) {
	dir := t.TempDir() // project dir with no transcripts yet
	m := newModel(defaultConfig(), waitingTranscript(dir), "", true)

	if m.state.Kind != StateWaiting {
		t.Fatalf("state.Kind = %v, want StateWaiting", m.state.Kind)
	}
	if !m.state.Anchored {
		t.Errorf("state.Anchored = false, want true (waitingSince is a fixed instant)")
	}
	if got := m.state.Label(); got != "Starting" {
		t.Errorf("Label() = %q, want %q", got, "Starting")
	}
	if m.summarizing {
		t.Errorf("summarizing held in waiting mode: Init fires no seed call, so this would never clear")
	}
}

// The waiting placeholder anchors the follow-active MRA scan to the project
// dir. Before any transcript exists — even before the project dir itself
// exists — the scan must report nothing (keep waiting), and once the first
// transcript is written it must win the comparison against the placeholder.
func TestWaitingPlaceholderAnchorsMRAScan(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist-yet")
	if _, ok := mostRecentlyActiveSession(filepath.Dir(waitingTranscript(missing))); ok {
		t.Fatalf("MRA scan of a nonexistent project dir reported a session; want none")
	}

	dir := t.TempDir()
	placeholder := waitingTranscript(dir)
	first := filepath.Join(dir, "first-sess.jsonl")
	if err := os.WriteFile(first, []byte(`{"type":"user","timestamp":"2026-08-13T15:09:30Z","message":{"content":"hello"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mra, ok := mostRecentlyActiveSession(filepath.Dir(placeholder))
	if !ok || mra != first {
		t.Fatalf("MRA = %q, %v; want %q, true", mra, ok, first)
	}
	if mra == placeholder {
		t.Fatalf("placeholder must never win the MRA comparison")
	}
}

// Adopting the first real transcript ends waiting mode: switchSession binds
// the session id, clears the waiting anchor, and the state is recomputed from
// the real events — never StateWaiting again.
func TestWaitingModeAdoptsFirstTranscript(t *testing.T) {
	dir := t.TempDir()
	m := newModel(defaultConfig(), waitingTranscript(dir), "", true)
	if m.state.Kind != StateWaiting {
		t.Fatalf("precondition: state.Kind = %v, want StateWaiting", m.state.Kind)
	}

	first := filepath.Join(dir, "first-sess.jsonl")
	if err := os.WriteFile(first, []byte(`{"type":"user","timestamp":"2026-08-13T15:09:30Z","message":{"content":"hello"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m.switchSession(first, time.Now())

	if m.sessionID != "first-sess" {
		t.Errorf("sessionID = %q, want %q", m.sessionID, "first-sess")
	}
	if !m.waitingSince.IsZero() {
		t.Errorf("waitingSince = %v, want zero after adoption", m.waitingSince)
	}
	if m.state.Kind == StateWaiting {
		t.Errorf("state.Kind still StateWaiting after adopting a real transcript")
	}
}
