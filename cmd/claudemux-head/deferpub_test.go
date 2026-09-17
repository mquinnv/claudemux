package main

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestDeferArgs(t *testing.T) {
	got := deferArgs("api", true, "PR review")
	want := []string{"set-option", "-t", "api", deferOption, "1",
		";", "set-option", "-t", "api", deferReasonOption, "PR review"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deferArgs(on, reason) = %v, want %v", got, want)
	}
	// No blocker: the mark is set and any old reason is unset, so a previous
	// defer's blocker can't reappear.
	got = deferArgs("api", true, "  ")
	want = []string{"set-option", "-t", "api", deferOption, "1",
		";", "set-option", "-t", "api", "-u", deferReasonOption}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deferArgs(on, blank) = %v, want %v", got, want)
	}
	// A trailing ";" is escaped, or tmux would swallow it as a separator.
	if got := deferArgs("api", true, "CI;"); got[len(got)-1] != `CI\;` {
		t.Errorf("deferArgs(on, trailing ;) reason arg = %q, want %q", got[len(got)-1], `CI\;`)
	}
	got = deferArgs("api", false, "ignored")
	want = []string{"set-option", "-t", "api", "-u", deferOption,
		";", "set-option", "-t", "api", "-u", deferReasonOption}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deferArgs(off) = %v, want %v", got, want)
	}
}

// Tabs and newlines would split the lobby's tab-separated listing line.
func TestSanitizeDeferReason(t *testing.T) {
	if got := sanitizeDeferReason(" waiting\ton\nCI \x7f"); got != "waiting on CI" {
		t.Errorf("sanitizeDeferReason = %q, want %q", got, "waiting on CI")
	}
}

func TestDeferChip(t *testing.T) {
	if got := deferChip("1", ""); !strings.Contains(got, "defer") {
		t.Errorf("deferChip(1) = %q, want a visible defer chip", got)
	}
	if got := deferChip("1", "PR review"); !strings.Contains(got, "defer: PR review") {
		t.Errorf("deferChip(1, reason) = %q, want the blocker in the chip", got)
	}
	for name, raw := range map[string]string{
		"empty":   "",
		"unknown": "0",
		"garbage": "yes",
	} {
		if got := deferChip(raw, "PR review"); got != "" {
			t.Errorf("deferChip(%s) = %q, want empty", name, got)
		}
	}
}

// A long blocker is capped so it can't push the rest of the top line off.
func TestDeferChipCapsLongReason(t *testing.T) {
	got := ansi.Strip(deferChip("1", strings.Repeat("x", 200)))
	if w := ansi.StringWidth(got); w > len("◆ defer: ")+deferReasonMaxCells {
		t.Errorf("deferChip width = %d, want capped near %d", w, deferReasonMaxCells)
	}
}

func TestDeferInputKey(t *testing.T) {
	in := deferInputKey("", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("CI")})
	in = deferInputKey(in, tea.KeyMsg{Type: tea.KeySpace})
	in = deferInputKey(in, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	in = deferInputKey(in, tea.KeyMsg{Type: tea.KeyBackspace})
	in = deferInputKey(in, tea.KeyMsg{Type: tea.KeyUp})
	if in != "CI " {
		t.Errorf("deferInputKey sequence = %q, want %q", in, "CI ")
	}
}
