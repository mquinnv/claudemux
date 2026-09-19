package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionRecordRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := sessionRecord{
		SessionName: "remix-2", LaunchDir: "/p/remix", SessionID: "abc-123",
		ClaudeCwd: "/p/remix/.claude/worktrees/foo", State: "Tool:Bash",
		Topic: "Fix idle detection", LastSeen: 1789000000,
	}
	if err := writeSessionRecord(dir, r); err != nil {
		t.Fatal(err)
	}
	got := readSessionRecords(dir)
	if len(got) != 1 || got[0].Rec != r {
		t.Fatalf("got %+v, want one record %+v", got, r)
	}
	if got[0].Path != sessionRecordFile(dir, "remix-2") {
		t.Errorf("path = %q", got[0].Path)
	}
	// No temp files left behind by the atomic write.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want 1", len(entries))
	}
}

func TestSessionRecordFileSanitizes(t *testing.T) {
	cases := map[string]string{
		"remix":   "remix.json",
		"a/b":     "a_b.json",
		".hidden": "_hidden.json",
		"":        "_.json",
	}
	for name, want := range cases {
		if got := filepath.Base(sessionRecordFile("/d", name)); got != want {
			t.Errorf("sessionRecordFile(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestReadSessionRecordsSkipsJunk(t *testing.T) {
	dir := t.TempDir()
	_ = writeSessionRecord(dir, sessionRecord{SessionName: "ok", SessionID: "id1", LastSeen: 1})
	_ = os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte("{nope"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "noid.json"), []byte(`{"session_name":"x"}`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "ok.json.tmp.123"), []byte(`{}`), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "restored-5"), 0o755)
	_ = writeSessionRecord(filepath.Join(dir, "restored-5"), sessionRecord{SessionName: "old", SessionID: "id2"})
	got := readSessionRecords(dir)
	if len(got) != 1 || got[0].Rec.SessionName != "ok" {
		t.Fatalf("got %+v, want only the ok record", got)
	}
}

func TestReadSessionRecordsMissingDir(t *testing.T) {
	if got := readSessionRecords(filepath.Join(t.TempDir(), "nope")); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
	if got := readSessionRecords(""); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestPruneSessionRecords(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	_ = writeSessionRecord(dir, sessionRecord{SessionName: "fresh", SessionID: "a"})
	_ = writeSessionRecord(dir, sessionRecord{SessionName: "stale", SessionID: "b"})
	old := now.Add(-8 * 24 * time.Hour)
	_ = os.Chtimes(sessionRecordFile(dir, "stale"), old, old)
	_ = os.Chtimes(sessionRecordFile(dir, "fresh"), now, now)
	pruneSessionRecords(dir, now, sessionRecordMaxAge)
	if _, err := os.Stat(sessionRecordFile(dir, "stale")); !os.IsNotExist(err) {
		t.Errorf("stale record survived prune")
	}
	if _, err := os.Stat(sessionRecordFile(dir, "fresh")); err != nil {
		t.Errorf("fresh record pruned: %v", err)
	}
}

func TestArchiveSessionRecords(t *testing.T) {
	dir := t.TempDir()
	_ = writeSessionRecord(dir, sessionRecord{SessionName: "a", SessionID: "1"})
	_ = writeSessionRecord(dir, sessionRecord{SessionName: "b", SessionID: "2"})
	paths := []string{sessionRecordFile(dir, "a"), sessionRecordFile(dir, "b")}
	if err := archiveSessionRecords(dir, 42, paths); err != nil {
		t.Fatal(err)
	}
	if got := readSessionRecords(dir); len(got) != 0 {
		t.Errorf("records still live after archive: %+v", got)
	}
	if got := readSessionRecords(filepath.Join(dir, "restored-42")); len(got) != 2 {
		t.Errorf("archive holds %d records, want 2", len(got))
	}
}

func TestRemoveSessionRecord(t *testing.T) {
	dir := t.TempDir()
	_ = writeSessionRecord(dir, sessionRecord{SessionName: "a", SessionID: "1"})
	removeSessionRecord(dir, "a")
	removeSessionRecord(dir, "a") // idempotent
	removeSessionRecord("", "a")  // no dir: no-op
	removeSessionRecord(dir, "")  // no name: no-op
	if got := readSessionRecords(dir); len(got) != 0 {
		t.Errorf("record survived remove: %+v", got)
	}
}
