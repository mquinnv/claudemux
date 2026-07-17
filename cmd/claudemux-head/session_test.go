package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEncodeProjectPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/Users/alice/Projects/foo", "-Users-alice-Projects-foo"},
		{"/Users/alice/Projects/claudemux-head", "-Users-alice-Projects-claudemux-head"},
		{"/home/user/code", "-home-user-code"},
	}
	for _, tt := range tests {
		got := encodeProjectPath(tt.input)
		if got != tt.want {
			t.Errorf("encodeProjectPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseSessionsIndex(t *testing.T) {
	data := []byte(`{
		"version": 1,
		"entries": [
			{
				"sessionId": "aaa-111",
				"projectPath": "/Users/test/project",
				"modified": "2026-04-18T10:00:00.000Z",
				"firstPrompt": "hello",
				"messageCount": 5
			},
			{
				"sessionId": "bbb-222",
				"projectPath": "/Users/test/project",
				"modified": "2026-04-18T12:00:00.000Z",
				"firstPrompt": "world",
				"messageCount": 10
			}
		]
	}`)

	entries, err := parseSessionsIndex(data)
	if err != nil {
		t.Fatalf("parseSessionsIndex returned error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].SessionID != "aaa-111" {
		t.Errorf("first entry sessionId = %q, want %q", entries[0].SessionID, "aaa-111")
	}
}

func TestFindMostRecentSession(t *testing.T) {
	entries := []sessionEntry{
		{SessionID: "older", Modified: "2026-04-18T10:00:00.000Z"},
		{SessionID: "newer", Modified: "2026-04-18T12:00:00.000Z"},
	}
	got := findMostRecentSession(entries)
	if got != "newer" {
		t.Errorf("findMostRecentSession = %q, want %q", got, "newer")
	}
}

func TestResolveSessionPrefersFreshJSONLOverStaleIndex(t *testing.T) {
	// sessions-index.json can lag behind reality. The active session may
	// be writing a JSONL file whose mtime is newer than anything the
	// index references. resolveSession must pick the JSONL-mtime winner.
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "-test-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	indexData := `{
		"version": 1,
		"entries": [
			{"sessionId": "stale-session", "modified": "2026-02-03T19:41:12.821Z"}
		]
	}`
	if err := os.WriteFile(filepath.Join(projectDir, "sessions-index.json"), []byte(indexData), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "fresh-session.jsonl"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveSession(tmpDir, "/test/project", "")
	if err != nil {
		t.Fatalf("resolveSession error: %v", err)
	}
	if got != "fresh-session" {
		t.Errorf("resolveSession = %q, want %q (must prefer JSONL mtime over stale index)", got, "fresh-session")
	}
}

func TestResolveSessionExplicitOverride(t *testing.T) {
	got, err := resolveSession("", "", "explicit-id")
	if err != nil {
		t.Fatalf("resolveSession error: %v", err)
	}
	if got != "explicit-id" {
		t.Errorf("resolveSession = %q, want %q", got, "explicit-id")
	}
}

func TestResolveSessionFallbackToJSONL(t *testing.T) {
	// No sessions-index.json, but .jsonl files exist
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "-test-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create two .jsonl files with different mod times
	older := filepath.Join(projectDir, "older-session.jsonl")
	newer := filepath.Join(projectDir, "newer-session.jsonl")
	if err := os.WriteFile(older, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Touch older file to be older
	oldTime := mustParseTime(t, "2026-01-01T00:00:00Z")
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	got, err := resolveSession(tmpDir, "/test/project", "")
	if err != nil {
		t.Fatalf("resolveSession error: %v", err)
	}
	if got != "newer-session" {
		t.Errorf("resolveSession = %q, want %q", got, "newer-session")
	}
}

func TestMostRecentlyActiveSession(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.jsonl")
	b := filepath.Join(dir, "b.jsonl")
	notJSONL := filepath.Join(dir, "sessions-index.json")
	for _, p := range []string{a, b, notJSONL} {
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Backdate a so b is the most-recently-active.
	old := mustParseTime(t, "2026-01-01T00:00:00Z")
	if err := os.Chtimes(a, old, old); err != nil {
		t.Fatal(err)
	}

	got, ok := mostRecentlyActiveSession(dir)
	if !ok {
		t.Fatal("mostRecentlyActiveSession ok=false, want true")
	}
	if got != b {
		t.Errorf("mostRecentlyActiveSession = %q, want %q", got, b)
	}

	if _, ok := mostRecentlyActiveSession(t.TempDir()); ok {
		t.Error("empty dir: ok=true, want false")
	}
}

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("failed to parse time %q: %v", s, err)
	}
	return parsed
}

// transcriptForSession finds a session's transcript by its stable id across
// every project dir, and when the id appears in two dirs (base + worktree
// encodings, as happens across a mid-session cd into a worktree) it returns the
// most recently modified — the live one.
func TestTranscriptForSession(t *testing.T) {
	projects := t.TempDir()
	base := filepath.Join(projects, "-Users-x-repo")
	wt := filepath.Join(projects, "-Users-x-repo--claude-worktrees-feat")
	for _, d := range []string{base, wt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sid := "sess-abc"
	baseFile := filepath.Join(base, sid+".jsonl")
	wtFile := filepath.Join(wt, sid+".jsonl")
	for _, f := range []string{baseFile, wtFile} {
		if err := os.WriteFile(f, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Make the worktree copy strictly newer than the base copy.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(baseFile, old, old); err != nil {
		t.Fatal(err)
	}

	got, ok := transcriptForSession(projects, sid)
	if !ok || got != wtFile {
		t.Fatalf("transcriptForSession = %q ok=%v, want %q true", got, ok, wtFile)
	}

	if _, ok := transcriptForSession(projects, "no-such-session"); ok {
		t.Error("expected not-ok for an unknown session id")
	}
	if _, ok := transcriptForSession(projects, ""); ok {
		t.Error("expected not-ok for an empty session id")
	}
}
