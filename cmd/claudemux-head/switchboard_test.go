package main

import (
	"testing"
	"time"
)

// Raw tmux outputs as the switchboard's three -F formats produce them.
// An unset user option renders as an empty field.
const (
	swSessOut = "api\tIdle\t1754700000\t37\tfixing the build\trun the tests\n" +
		"web\tTool:AskUserQuestion\t1754700100\t82\tpicking a color\twhich hue?\n" +
		"scratch\t\t\t\t\t\n" +
		"switchboard\t\t\t\t\t\n" +
		"plain\t\t\t\t\t\n"
	swPaneOut = "api\t%1\tclaudemux-head\tbuild fixes\n" +
		"api\t%2\tclaude\tbuild fixes\n" +
		"web\t%5\tclaudemux-head\tcolor picker\n" +
		"scratch\t%7\tclaudemux-head\t\n" +
		"switchboard\t%9\tclaudemux-head\tswitchboard\n" +
		"plain\t%11\tzsh\tplain\n"
	swClientOut = "/dev/ttys001\tswitchboard\n/dev/ttys002\tplain\n"
)

func TestBuildSwSnapshot(t *testing.T) {
	s := buildSwSnapshot(swSessOut, swPaneOut, swClientOut, "%9")
	if s.Lobby != "switchboard" {
		t.Errorf("Lobby = %q, want switchboard", s.Lobby)
	}
	// plain has no head pane; switchboard is the lobby: both excluded.
	if len(s.Sessions) != 3 {
		t.Fatalf("Sessions = %v, want api, web, scratch", s.Sessions)
	}
	api, ok := s.session("api")
	if !ok || api.State != "Idle" || !api.Since.Equal(time.Unix(1754700000, 0)) {
		t.Errorf("api = %+v, ok=%v", api, ok)
	}
	if api.Context != 37 || api.Topic != "build fixes" || api.Summary != "fixing the build" || api.Prompt != "run the tests" {
		t.Errorf("api info fields = %+v", api)
	}
	web, _ := s.session("web")
	if web.State != "Tool:AskUserQuestion" {
		t.Errorf("web.State = %q", web.State)
	}
	scratch, _ := s.session("scratch")
	if scratch.State != "" || !scratch.Since.IsZero() {
		t.Errorf("unset options must parse to zero values, got %+v", scratch)
	}
	if scratch.Context != -1 {
		t.Errorf("unset context must parse to -1, got %d", scratch.Context)
	}
	if scratch.Topic != "" || scratch.Summary != "" || scratch.Prompt != "" {
		t.Errorf("unset info fields must be empty, got %+v", scratch)
	}
	if s.Clients["/dev/ttys001"] != "switchboard" || s.Clients["/dev/ttys002"] != "plain" {
		t.Errorf("Clients = %v", s.Clients)
	}
}

func TestBuildSwSnapshotUnknownSelfPane(t *testing.T) {
	s := buildSwSnapshot(swSessOut, swPaneOut, swClientOut, "%404")
	if s.Lobby != "" {
		t.Errorf("Lobby = %q, want empty for unknown pane", s.Lobby)
	}
	// Without a lobby to exclude, all four head-bearing sessions survive.
	if len(s.Sessions) != 4 {
		t.Errorf("Sessions = %v, want 4", s.Sessions)
	}
}

func TestBuildSwSnapshotMalformedLines(t *testing.T) {
	s := buildSwSnapshot("half\n\n", "junk\n", "alsojunk\n", "%9")
	if len(s.Sessions) != 0 || len(s.Clients) != 0 {
		t.Errorf("malformed lines must be skipped, got %+v", s)
	}
	// Old-format (3-field) session and pane lines must be skipped
	oldFmt := buildSwSnapshot("api\tIdle\t1754700000\n", "api\t%1\tclaudemux-head\n", "", "%9")
	if len(oldFmt.Sessions) != 0 {
		t.Errorf("old-format lines must be skipped, got %+v", oldFmt.Sessions)
	}
}

func TestIsWaiting(t *testing.T) {
	cases := map[string]bool{
		"Idle":                 true,
		"Tool:AskUserQuestion": true,
		"Thinking":             false,
		"Tool:Bash":            false,
		"Compacting":           false,
		"":                     false,
	}
	for state, want := range cases {
		if got := isWaiting(state); got != want {
			t.Errorf("isWaiting(%q) = %v, want %v", state, got, want)
		}
	}
}
