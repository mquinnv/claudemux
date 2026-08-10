package main

import (
	"testing"
	"time"
)

// Raw tmux outputs as the switchboard's three -F formats produce them.
// An unset user option renders as an empty field.
const (
	swSessOut = "api\tIdle\t1754700000\n" +
		"web\tTool:AskUserQuestion\t1754700100\n" +
		"scratch\t\t\n" +
		"switchboard\t\t\n" +
		"plain\t\t\n"
	swPaneOut = "api\t%1\tclaudemux-head\n" +
		"api\t%2\tclaude\n" +
		"web\t%5\tclaudemux-head\n" +
		"scratch\t%7\tclaudemux-head\n" +
		"switchboard\t%9\tclaudemux-head\n" +
		"plain\t%11\tzsh\n"
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
	web, _ := s.session("web")
	if web.State != "Tool:AskUserQuestion" {
		t.Errorf("web.State = %q", web.State)
	}
	scratch, _ := s.session("scratch")
	if scratch.State != "" || !scratch.Since.IsZero() {
		t.Errorf("unset options must parse to zero values, got %+v", scratch)
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
