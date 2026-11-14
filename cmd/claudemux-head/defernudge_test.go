package main

import (
	"os"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPromptEmpty(t *testing.T) {
	cases := []struct {
		name, capture string
		want          bool
	}{
		{"empty prompt", readFixture(t, "prompt_empty.txt"), true},
		{"half-typed draft", readFixture(t, "prompt_draft.txt"), false},
		{"question dialog", readFixture(t, "prompt_asking.txt"), false},
		{"blank capture", "", false},
		{"no input box", "just some output\nmore output\n", false},
	}
	for _, c := range cases {
		if got := promptEmpty(c.capture); got != c.want {
			t.Errorf("%s: promptEmpty = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDeferNudgeText(t *testing.T) {
	cases := []struct {
		blocker string
		gone    bool
		reason  string
		want    string
	}{
		{"alpha", false, "waiting on review", "claudemux: alpha has stopped, so this session is no longer deferred on it (waiting on review). Check for its report, then pick up where you left off."},
		{"alpha", true, "waiting on review", "claudemux: alpha has closed, so this session is no longer deferred on it (waiting on review). Check what it left behind, then pick up where you left off."},
		{"alpha", false, "", "claudemux: alpha has stopped, so this session is no longer deferred on it. Check for its report, then pick up where you left off."},
		{"alpha", true, "alpha", "claudemux: alpha has closed, so this session is no longer deferred on it. Check what it left behind, then pick up where you left off."},
	}
	for _, c := range cases {
		if got := deferNudgeText(c.blocker, c.gone, c.reason); got != c.want {
			t.Errorf("deferNudgeText(%q,%v,%q)\n got %q\nwant %q", c.blocker, c.gone, c.reason, got, c.want)
		}
	}
}

func TestDeferDoneChip(t *testing.T) {
	if got := ansi.Strip(deferDoneChip("alpha", false)); got != "◆ alpha finished" {
		t.Errorf("stopped chip = %q", got)
	}
	if got := ansi.Strip(deferDoneChip("alpha", true)); got != "◆ alpha closed" {
		t.Errorf("gone chip = %q", got)
	}
}
