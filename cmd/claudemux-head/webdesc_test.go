package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProjectDeclaredDescription(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claudemux.yml")
	if got := projectDeclaredDescription(p); got != "" {
		t.Fatalf("missing file = %q, want empty", got)
	}
	if err := os.WriteFile(p, []byte("name: X\ndescription: |\n  Two   lines\n  of\ttext\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := projectDeclaredDescription(p), "Two lines of text"; got != want {
		t.Fatalf("description = %q, want %q", got, want)
	}
}

func TestBuildSwSnapshotReadsDescription(t *testing.T) {
	sess := "api\tIdle\t1754700000\t37\t\t\t\t\t\t\t\tBackend for the storefront\n"
	panes := "api\t%1\tclaudemux-head\tbuild\n"
	snap := buildSwSnapshot(sess, panes, "", "")
	if len(snap.Sessions) != 1 || snap.Sessions[0].Description != "Backend for the storefront" {
		t.Fatalf("sessions = %+v, want api with its description", snap.Sessions)
	}
}

func TestWebViewStateEmojiAndDescription(t *testing.T) {
	snap := swSnapshot{Sessions: []swSession{
		{Name: "a", State: "Thinking", Context: -1, Description: "d"},
		{Name: "b", State: "", Context: -1},
	}}
	v := buildWebFleetView(snap, RateLimits{}, false, nil, time.Now(), webHeadline{})
	if v.Sessions[0].StateEmoji != "🧠" || v.Sessions[0].Description != "d" {
		t.Errorf("a = %+v, want brain emoji and description", v.Sessions[0])
	}
	if v.Sessions[1].StateEmoji != "" {
		t.Errorf("unpublished state emoji = %q, want none", v.Sessions[1].StateEmoji)
	}
}
