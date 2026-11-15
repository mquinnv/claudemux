package main

import (
	"os"
	"path/filepath"
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
		{"real: Try draft, plain", readFixture(t, "prompt_trydraft.txt"), false},
		{"real: dim suggested prompt after a turn", readFixture(t, "prompt_suggestion.txt"), true},
		{"real: half-typed draft", readFixture(t, "prompt_draft.txt"), false},
		{"real: question dialog", readFixture(t, "prompt_asking.txt"), false},
		{"blank capture", "", false},
		{"no input box", "just some output\nmore output\n", false},
		{"bare prompt, vim off", box("❯ ", "  ⏵⏵ auto mode on"), true},
		{"dim placeholder", box("❯ \x1b[2mTry \"x\"\x1b[0m", ins), true},
		{"plain Try draft", box("❯ Try \"x", ins), false},
		{"dim suggestion", box("\x1b[39m❯\u00a0\x1b[2m/done\x1b[0m", ins), true},
		{"draft with a dim tail", box("❯ fix \x1b[2mthe bug\x1b[0m", ins), false},
		{"dim start, plain tail", box("❯ \x1b[2m/do\x1b[0mne and more", ins), false},
		{"multi-line draft", "x\n" + rule + "\n❯ first\n  second\n" + rule + "\n\n" + ins + "\n", false},
		{"pasted text placeholder", box("❯ [Pasted text #1 +12 lines]", ins), false},
		{"draft that is only a glyph", box("❯ x", ins), false},
		{"vim NORMAL footer", box("❯ ", "  -- NORMAL -- ⏵⏵ auto mode on"), false},
		{"vim VISUAL footer", box("❯ ", "  -- VISUAL -- ⏵⏵ auto mode on"), false},
		{"tail longer than 3 lines", box("❯ ", ins) + "a\nb\nc\n", false},
		{"prompt without glyph", box("hello", ins), false},
	}
	for _, c := range cases {
		// Every case carries "-- INSERT --" or is a negative, so the answer
		// is the same with vim mode on or off, except the one with no footer
		// indicator, which vim mode rejects.
		for _, vim := range []bool{false, true} {
			want := c.want
			if c.name == "bare prompt, vim off" && vim {
				want = false
			}
			if got := promptEmpty(c.capture, vim); got != want {
				t.Errorf("%s (vim=%v): promptEmpty = %v, want %v", c.name, vim, got, want)
			}
		}
	}
	// The real vim-NORMAL capture has no footer indicator, so with vim mode
	// off it is indistinguishable from an empty box (true is correct); with
	// vim mode on, the missing "-- INSERT --" is what marks it unsafe.
	normal := readFixture(t, "prompt_normal.txt")
	if promptEmpty(normal, true) {
		t.Error("vim NORMAL capture read as empty under vim mode")
	}
	if !promptEmpty(normal, false) {
		t.Error("indicator-less empty box should be empty without vim mode")
	}
}

func TestClaudeVimMode(t *testing.T) {
	write := func(t *testing.T, path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	none := func(string) string { return "" }
	t.Run("vim in .claude.json only", func(t *testing.T) {
		h := t.TempDir()
		write(t, filepath.Join(h, ".claude.json"), `{"editorMode":"vim"}`)
		if !claudeVimMode(none, h) {
			t.Error("want true")
		}
	})
	t.Run("vim in settings.json only", func(t *testing.T) {
		h := t.TempDir()
		write(t, filepath.Join(h, ".claude", "settings.json"), `{"editorMode":"vim"}`)
		if !claudeVimMode(none, h) {
			t.Error("want true")
		}
	})
	t.Run("both present, normal and absent", func(t *testing.T) {
		h := t.TempDir()
		write(t, filepath.Join(h, ".claude.json"), `{"editorMode":"normal"}`)
		write(t, filepath.Join(h, ".claude", "settings.json"), `{"other":1}`)
		if claudeVimMode(none, h) {
			t.Error("want false")
		}
	})
	t.Run("neither file", func(t *testing.T) {
		if !claudeVimMode(none, t.TempDir()) {
			t.Error("want true (conservative)")
		}
	})
	t.Run("one malformed, other not vim", func(t *testing.T) {
		h := t.TempDir()
		write(t, filepath.Join(h, ".claude.json"), `{not json`)
		write(t, filepath.Join(h, ".claude", "settings.json"), `{"editorMode":"normal"}`)
		if claudeVimMode(none, h) {
			t.Error("want false")
		}
	})
	t.Run("only malformed", func(t *testing.T) {
		h := t.TempDir()
		write(t, filepath.Join(h, ".claude.json"), `{not json`)
		if !claudeVimMode(none, h) {
			t.Error("want true (nothing parsed)")
		}
	})
	t.Run("CLAUDE_CONFIG_DIR wins over home", func(t *testing.T) {
		h, d := t.TempDir(), t.TempDir()
		write(t, filepath.Join(h, ".claude.json"), `{"editorMode":"vim"}`)
		write(t, filepath.Join(d, "settings.json"), `{"editorMode":"normal"}`)
		env := func(k string) string {
			if k == "CLAUDE_CONFIG_DIR" {
				return d
			}
			return ""
		}
		if claudeVimMode(env, h) {
			t.Error("read home instead of CLAUDE_CONFIG_DIR")
		}
	})
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
