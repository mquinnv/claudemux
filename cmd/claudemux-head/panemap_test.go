package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestClaudePaneCandidatesPrefersClaudeOverEarlierNode(t *testing.T) {
	// %30 (node dev server) is listed before %33 (claude), both in self's
	// window (@1) — claude must still be preferred.
	listing := "%35 @1 claudemux-head\n%30 @1 node\n%33 @1 claude\n"
	got := claudePaneCandidates(listing, "%35")
	want := []string{"%33", "%30"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestClaudePaneCandidatesPrefersSameWindow(t *testing.T) {
	// %40 is claude in another window (@2); %33 is claude in self's window
	// (@1). Same-window must be preferred over other-window even though
	// %40 appears first in listing order.
	listing := "%35 @1 claudemux-head\n%40 @2 claude\n%33 @1 claude\n"
	got := claudePaneCandidates(listing, "%35")
	want := []string{"%33", "%40"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestClaudePaneCandidatesExcludesSelf(t *testing.T) {
	listing := "%35 @1 claude\n"
	got := claudePaneCandidates(listing, "%35")
	if len(got) != 0 {
		t.Fatalf("expected no candidates (self excluded), got %v", got)
	}
}

func TestClaudePaneCandidatesNeverMatchesClaudeHeadOrShell(t *testing.T) {
	listing := "%1 @1 claudemux-head\n%2 @1 bash\n%3 @1 fish\n"
	got := claudePaneCandidates(listing, "%1")
	if len(got) != 0 {
		t.Fatalf("expected no candidates, got %v", got)
	}
}

func TestClaudePaneCandidatesEmptyListing(t *testing.T) {
	got := claudePaneCandidates("", "%1")
	if got != nil {
		t.Fatalf("expected nil for empty listing, got %v", got)
	}
}

func TestClaudePaneCandidatesAllGroups(t *testing.T) {
	// Full ordering: same-window claude, same-window node, other-window
	// claude, other-window node — regardless of listing order.
	listing := strings.Join([]string{
		"%1 @1 claudemux-head", // self
		"%2 @2 node",        // other-window node
		"%3 @2 claude",      // other-window claude
		"%4 @1 node",        // same-window node
		"%5 @1 claude",      // same-window claude
	}, "\n")
	got := claudePaneCandidates(listing, "%1")
	want := []string{"%5", "%4", "%3", "%2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestClaudePaneCandidatesSelfNotFoundTreatsAllOtherWindow(t *testing.T) {
	// self's pane id isn't in the listing at all, so window-based
	// preference can't apply — all candidates fall into other-window
	// groups, still ordered claude-before-node and stable within group.
	listing := "%2 @2 node\n%3 @2 claude\n"
	got := claudePaneCandidates(listing, "%1")
	want := []string{"%3", "%2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestReadPaneMap(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "abc.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `{"session_id":"abc","transcript_path":"` + transcript + `","cwd":"/tmp/foo"}`
	if err := os.WriteFile(filepath.Join(dir, "42.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, cwd, ok := readPaneMap(dir, "%42")
	if !ok || got != transcript {
		t.Fatalf("got %q ok=%v, want %q true", got, ok, transcript)
	}
	if cwd != "/tmp/foo" {
		t.Fatalf("cwd = %q, want %q", cwd, "/tmp/foo")
	}
}

// A map file written by an older version of the hook, before it recorded
// cwd, must still be usable — the missing cwd must not affect ok.
func TestReadPaneMapToleratesMissingCwd(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "abc.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `{"session_id":"abc","transcript_path":"` + transcript + `"}`
	if err := os.WriteFile(filepath.Join(dir, "43.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, cwd, ok := readPaneMap(dir, "%43")
	if !ok || got != transcript {
		t.Fatalf("got %q ok=%v, want %q true", got, ok, transcript)
	}
	if cwd != "" {
		t.Fatalf("cwd = %q, want empty", cwd)
	}
}

func TestReadPaneMapRejectsMissingTranscript(t *testing.T) {
	dir := t.TempDir()
	body := `{"session_id":"abc","transcript_path":"` + filepath.Join(dir, "gone.jsonl") + `"}`
	if err := os.WriteFile(filepath.Join(dir, "7.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _, ok := readPaneMap(dir, "%7"); ok {
		t.Fatalf("expected not-ok for missing transcript, got %q", got)
	}
}

func TestReadPaneMapRejectsNullTranscriptPath(t *testing.T) {
	dir := t.TempDir()
	body := `{"session_id":"x","transcript_path":null}`
	if err := os.WriteFile(filepath.Join(dir, "8.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _, ok := readPaneMap(dir, "%8"); ok {
		t.Fatalf("expected not-ok for null transcript_path, got %q", got)
	}
}

func TestReadPaneMapRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "9.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := readPaneMap(dir, "%9"); ok {
		t.Fatal("expected not-ok for garbage map file")
	}
	if _, _, ok := readPaneMap(dir, "%404"); ok {
		t.Fatal("expected not-ok for absent map file")
	}
}
