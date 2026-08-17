package main

import (
	"testing"
	"time"
)

// Raw tmux outputs as the switchboard's three -F formats produce them.
// An unset user option renders as an empty field.
const (
	swSessOut = "api\tIdle\t1754700000\t37\tfixing the build\trun the tests\tclaude-opus-4-7\tb34dff\n" +
		"web\tTool:AskUserQuestion\t1754700100\t82\tpicking a color\twhich hue?\tclaude-fable-5\t\n" +
		"scratch\t\t\t\t\t\t\t\n" +
		"switchboard\t\t\t\t\t\t\t\n" +
		"plain\t\t\t\t\t\t\t\n"
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
	if api.Model != "claude-opus-4-7" {
		t.Errorf("api.Model = %q, want claude-opus-4-7", api.Model)
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
	if scratch.Topic != "" || scratch.Summary != "" || scratch.Prompt != "" || scratch.Model != "" {
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
	// Old-format (3- and 6-field) session and pane lines must be skipped
	oldFmt := buildSwSnapshot("api\tIdle\t1754700000\n", "api\t%1\tclaudemux-head\n", "", "%9")
	if len(oldFmt.Sessions) != 0 {
		t.Errorf("old-format lines must be skipped, got %+v", oldFmt.Sessions)
	}
	sixField := buildSwSnapshot("api\tIdle\t1754700000\t37\tsum\tprompt\n", "api\t%1\tclaudemux-head\tt\n", "", "%9")
	if len(sixField.Sessions) != 0 {
		t.Errorf("pre-model 6-field lines must be skipped, got %+v", sixField.Sessions)
	}
	sevenField := buildSwSnapshot("api\tIdle\t1754700000\t37\tsum\tprompt\tm\n", "api\t%1\tclaudemux-head\tt\n", "", "%9")
	if len(sevenField.Sessions) != 0 {
		t.Errorf("pre-color 7-field lines must be skipped, got %+v", sevenField.Sessions)
	}
}

// The project color rides along as the eighth field. A session whose project
// declares no color publishes an empty one — the lobby renders those rows
// exactly as it did before there was a color at all.
func TestBuildSwSnapshotParsesColor(t *testing.T) {
	s := buildSwSnapshot(swSessOut, swPaneOut, swClientOut, "%9")
	api, ok := s.session("api")
	if !ok || api.Color != "b34dff" {
		t.Errorf("api.Color = %q, want b34dff", api.Color)
	}
	web, ok := s.session("web")
	if !ok || web.Color != "" {
		t.Errorf("web.Color = %q, want empty: its project declares no color", web.Color)
	}
}

// The preview needs the claude pane, not the head pane: swPaneOut gives api
// both, web only a head pane.
func TestBuildSwSnapshotRecordsClaudePane(t *testing.T) {
	s := buildSwSnapshot(swSessOut, swPaneOut, swClientOut, "%9")
	api, ok := s.session("api")
	if !ok || api.ClaudePane != "%2" {
		t.Errorf("api.ClaudePane = %q, want %%2", api.ClaudePane)
	}
	web, ok := s.session("web")
	if !ok || web.ClaudePane != "" {
		t.Errorf("web.ClaudePane = %q, want empty: it has no claude pane", web.ClaudePane)
	}
}

// HeadPane records the session's own claudemux-head pane, distinct from
// ClaudePane: the fleet-restart key needs it to know where to type R.
func TestBuildSwSnapshotRecordsHeadPane(t *testing.T) {
	s := buildSwSnapshot(swSessOut, swPaneOut, swClientOut, "%9")
	api, ok := s.session("api")
	if !ok || api.HeadPane != "%1" {
		t.Errorf("api.HeadPane = %q, want %%1", api.HeadPane)
	}
	web, ok := s.session("web")
	if !ok || web.HeadPane != "%5" {
		t.Errorf("web.HeadPane = %q, want %%5", web.HeadPane)
	}
	// plain has no claudemux-head pane at all: it must not surface as a
	// session, let alone with a stray HeadPane.
	if _, ok := s.session("plain"); ok {
		t.Error("plain has no head pane and must be excluded from Sessions")
	}
}

// "node" identifies claude only as a fallback. A session that has both a real
// claude pane and some other node process must preview claude, whatever order
// tmux lists them in.
func TestBuildSwSnapshotPrefersClaudeOverNode(t *testing.T) {
	paneOut := "api\t%1\tclaudemux-head\ttopic\n" +
		"api\t%2\tnode\ttopic\n" +
		"api\t%3\tclaude\ttopic\n" +
		"shim\t%4\tclaudemux-head\ttopic\n" +
		"shim\t%5\tnode\ttopic\n"
	sessOut := "api\tIdle\t1754700000\t37\t\t\t\t\n" +
		"shim\tIdle\t1754700000\t37\t\t\t\t\n"
	s := buildSwSnapshot(sessOut, paneOut, swClientOut, "")
	api, _ := s.session("api")
	if api.ClaudePane != "%3" {
		t.Errorf("api.ClaudePane = %q, want %%3: a real claude pane outranks node", api.ClaudePane)
	}
	shim, _ := s.session("shim")
	if shim.ClaudePane != "%5" {
		t.Errorf("shim.ClaudePane = %q, want %%5: node is the fallback", shim.ClaudePane)
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
