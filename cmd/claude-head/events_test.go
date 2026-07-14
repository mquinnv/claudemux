package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanCommandText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "name first with args",
			in:   "<command-name>/myplugin:deploy</command-name>\n  <command-message>deploy</command-message>\n  <command-args>foo bar</command-args>",
			want: "/myplugin:deploy foo bar",
		},
		{
			name: "message first, empty args",
			in:   "<command-message>clear</command-message> <command-name>/clear</command-name> <command-args></command-args>",
			want: "/clear",
		},
		{
			name: "name only, no args tag",
			in:   "<command-name>/color</command-name>",
			want: "/color",
		},
		{
			name: "plain prose untouched",
			in:   "ok, so let's do the first two",
			want: "ok, so let's do the first two",
		},
		{
			name: "args whitespace trimmed",
			in:   "<command-name>/color</command-name><command-args>  purple  </command-args>",
			want: "/color purple",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanCommandText(tt.in); got != tt.want {
				t.Errorf("cleanCommandText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseEventIsMeta(t *testing.T) {
	line := `{"type":"user","isMeta":true,"timestamp":"2026-07-02T12:00:00Z","message":{"content":"Caveat: x"}}`
	e, ok := parseEvent(line)
	if !ok || !e.IsMeta {
		t.Fatalf("parseEvent isMeta: ok=%v IsMeta=%v, want true/true", ok, e.IsMeta)
	}
}

func TestEventReaderInitialReadFromEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	preamble := `{"type":"user","message":{"content":"old"}}` + "\n"
	if err := os.WriteFile(path, []byte(preamble), 0o644); err != nil {
		t.Fatal(err)
	}

	r := newEventReader(path)
	r.SeedFromEnd(10)
	seeded, err := r.Seeded()
	if err != nil {
		t.Fatalf("Seeded error: %v", err)
	}
	if len(seeded) != 1 {
		t.Fatalf("expected 1 seeded event, got %d", len(seeded))
	}
	if seeded[0].Type != "user" {
		t.Errorf("seeded[0].Type = %q, want %q", seeded[0].Type, "user")
	}
}

func TestEventReaderTailReturnsOnlyNewLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"content":"a"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := newEventReader(path)
	r.SeedFromEnd(10)
	if _, err := r.Seeded(); err != nil {
		t.Fatal(err)
	}

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"assistant","message":{"content":"b"}}` + "\n")
	f.WriteString(`{"type":"user","message":{"content":"c"}}` + "\n")
	f.Close()

	newEvents, err := r.Tail()
	if err != nil {
		t.Fatalf("Tail error: %v", err)
	}
	if len(newEvents) != 2 {
		t.Fatalf("expected 2 new events, got %d", len(newEvents))
	}
	if newEvents[0].Type != "assistant" || newEvents[1].Type != "user" {
		t.Errorf("got types %q, %q", newEvents[0].Type, newEvents[1].Type)
	}

	more, err := r.Tail()
	if err != nil {
		t.Fatalf("Tail error: %v", err)
	}
	if len(more) != 0 {
		t.Errorf("expected 0 events on second Tail, got %d", len(more))
	}
}

func TestEventReaderHandlesPartialFinalLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	complete := `{"type":"user","message":{"content":"a"}}` + "\n"
	partial := `{"type":"assistant"`
	if err := os.WriteFile(path, []byte(complete+partial), 0o644); err != nil {
		t.Fatal(err)
	}

	r := newEventReader(path)
	r.SeedFromEnd(10)
	seeded, err := r.Seeded()
	if err != nil {
		t.Fatalf("Seeded error: %v", err)
	}
	if len(seeded) != 1 {
		t.Fatalf("expected 1 complete event, got %d", len(seeded))
	}

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`,"message":{"content":"b"}}` + "\n")
	f.Close()

	newEvents, err := r.Tail()
	if err != nil {
		t.Fatalf("Tail error: %v", err)
	}
	if len(newEvents) != 1 {
		t.Fatalf("expected 1 tailed event, got %d", len(newEvents))
	}
	if newEvents[0].Type != "assistant" {
		t.Errorf("Type = %q, want %q", newEvents[0].Type, "assistant")
	}
}

func TestUpgradeFirstPrompt(t *testing.T) {
	user := func(text string) Event { return Event{Type: "user", UserText: text} }

	tests := []struct {
		name string
		seed string  // firstPrompt captured at seed time
		tail []Event // events arriving on a later Tail
		want string
	}{
		{
			name: "command placeholder yields to a real prompt",
			seed: "/clear",
			tail: []Event{user("fix the worktree chip")},
			want: "fix the worktree chip",
		},
		{
			name: "command holds when nothing else arrives",
			seed: "/clear",
			tail: []Event{user("/color purple")},
			want: "/clear",
		},
		{
			name: "a real prompt is frozen and never replaced",
			seed: "fix the worktree chip",
			tail: []Event{user("now clip the status lines")},
			want: "fix the worktree chip",
		},
		{
			name: "empty seed takes whatever arrives first",
			seed: "",
			tail: []Event{user("/clear"), user("fix the chip")},
			want: "fix the chip",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &EventReader{firstPrompt: tt.seed}
			r.upgradeFirstPrompt(tt.tail)
			if got := r.FirstPrompt(); got != tt.want {
				t.Errorf("FirstPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}
