package main

import (
	"os"
	"strings"
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

// box builds a synthetic capture of the input box around prompt line(s).
func box(prompt, footer string) string {
	rule := strings.Repeat("─", 100)
	return "some output\n" + rule + "\n" + prompt + "\n" + rule + "\n\n" + footer + "\n"
}

func TestPromptEmpty(t *testing.T) {
	const ins = "  -- INSERT -- ⏵⏵ auto mode on"
	rule := strings.Repeat("─", 100)
	cases := []struct {
		name, capture string
		want          bool
	}{
		{"real: placeholder", readFixture(t, "prompt_empty.txt"), true},
		{"real: bare prompt mid-session", readFixture(t, "prompt_bare.txt"), true},
		{"real: vim normal, no indicator", readFixture(t, "prompt_normal.txt"), true},
		{"real: Try draft, plain", readFixture(t, "prompt_trydraft.txt"), false},
		{"real: half-typed draft", readFixture(t, "prompt_draft.txt"), false},
		{"real: question dialog", readFixture(t, "prompt_asking.txt"), false},
		{"blank capture", "", false},
		{"no input box", "just some output\nmore output\n", false},
		{"bare prompt, vim off", box("❯ ", "  ⏵⏵ auto mode on"), true},
		{"dim placeholder", box("❯ \x1b[2mTry \"x\"\x1b[0m", ins), true},
		{"plain Try draft", box("❯ Try \"x", ins), false},
		{"multi-line draft", "x\n" + rule + "\n❯ first\n  second\n" + rule + "\n\n" + ins + "\n", false},
		{"pasted text placeholder", box("❯ [Pasted text #1 +12 lines]", ins), false},
		{"draft that is only a glyph", box("❯ x", ins), false},
		{"vim NORMAL footer", box("❯ ", "  -- NORMAL -- ⏵⏵ auto mode on"), false},
		{"vim VISUAL footer", box("❯ ", "  -- VISUAL -- ⏵⏵ auto mode on"), false},
		{"tail longer than 3 lines", box("❯ ", ins) + "a\nb\nc\n", false},
		{"prompt without glyph", box("hello", ins), false},
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
