package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A successor that exists but holds no conversation yet is not adoptable:
// switching to it wipes the background tracker (the old session's running
// agent reads as Idle) and seeds a summary from nothing. The head keeps its
// current binding until a user or assistant record lands in the successor.
//
// This is the remix-2 window of 2026-09-10 11:14–11:21: the continued-in
// record was written at 11:14, the fork's file held only bookkeeping until
// the session next ran at 11:21, and for those seven minutes the head said
// Idle under a tab that read "session setup".
func TestFollowContinuationWaitsForConversation(t *testing.T) {
	projects := t.TempDir()
	proj := filepath.Join(projects, "p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(proj, "old-id.jsonl")
	next := filepath.Join(proj, "new-id.jsonl")
	if err := os.WriteFile(old, []byte(conversationLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(next, []byte(bookkeepingStub), 0o600); err != nil {
		t.Fatal(err)
	}
	superseded := map[string]string{"old-id": "new-id"}

	if got := followContinuation(old, superseded, projects); got != old {
		t.Errorf("stub successor: followed to %q, want to stay on %q", got, old)
	}
	if got := resolveActiveTranscript(old, old, superseded, projects); got != "" {
		t.Errorf("stub successor via pane map: got %q, want \"\" (stay)", got)
	}

	// The conversation arrives (Claude Code copies the history in when the
	// session next runs): now the successor is the live transcript.
	f, err := os.OpenFile(next, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(conversationLine + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := followContinuation(old, superseded, projects); got != next {
		t.Errorf("after conversation landed: followed to %q, want %q", got, next)
	}
	if got := resolveActiveTranscript(old, old, superseded, projects); got != next {
		t.Errorf("after conversation landed via pane map: got %q, want %q", got, next)
	}
}

// An empty file is the other shape a not-yet-written successor can take
// (created, nothing flushed). Same rule: stay put.
func TestFollowContinuationIgnoresEmptySuccessor(t *testing.T) {
	projects := t.TempDir()
	proj := filepath.Join(projects, "p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(proj, "old-id.jsonl")
	next := filepath.Join(proj, "new-id.jsonl")
	if err := os.WriteFile(old, []byte(conversationLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(next, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := followContinuation(old, map[string]string{"old-id": "new-id"}, projects); got != old {
		t.Errorf("empty successor: followed to %q, want to stay on %q", got, old)
	}
}
