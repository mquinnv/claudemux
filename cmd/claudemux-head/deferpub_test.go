package main

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// unsetLinkTail is the three-link-option unset chain every clear and every
// link-less set appends, so a stale link can never outlive its defer.
func unsetLinkTail(target string) []string {
	return []string{
		";", "set-option", "-t", target, "-u", deferOnOption,
		";", "set-option", "-t", target, "-u", deferOnCreatedOption,
		";", "set-option", "-t", target, "-u", deferOnSeenOption,
	}
}

// argvSets reports the value tmux would be given for option in argv, and
// whether the option is unset (-u) instead.
func argvSets(argv []string, option string) (val string, set, unset bool) {
	for i, a := range argv {
		if a != option {
			continue
		}
		if i > 0 && argv[i-1] == "-u" {
			return "", false, true
		}
		if i+1 < len(argv) {
			return argv[i+1], true, false
		}
	}
	return "", false, false
}

func TestDeferArgsClearUnsetsLink(t *testing.T) {
	argv := deferArgs("%1", false, "")
	for _, o := range []string{deferOption, deferReasonOption, deferOnOption, deferOnCreatedOption, deferOnSeenOption} {
		if _, _, unset := argvSets(argv, o); !unset {
			t.Errorf("clear argv does not unset %s: %v", o, argv)
		}
	}
	if n := strings.Count(strings.Join(argv, " "), " ; "); n != 4 {
		t.Errorf("clear argv has %d separators, want 4 (one chained invocation): %v", n, argv)
	}
}

func TestDeferArgsPlainSetUnsetsLink(t *testing.T) {
	argv := deferArgs("%1", true, "CI")
	if v, set, _ := argvSets(argv, deferOption); !set || v != "1" {
		t.Errorf("mark = %q set=%v, want 1", v, set)
	}
	if v, set, _ := argvSets(argv, deferReasonOption); !set || v != "CI" {
		t.Errorf("reason = %q set=%v, want CI", v, set)
	}
	for _, o := range []string{deferOnOption, deferOnCreatedOption, deferOnSeenOption} {
		if _, _, unset := argvSets(argv, o); !unset {
			t.Errorf("plain set does not unset %s: %v", o, argv)
		}
	}
}

func TestDeferLinkedArgs(t *testing.T) {
	argv := deferLinkedArgs("%1", deferWaitValue, "waiting on phenix-2: fix", deferLink{"phenix-2", "1763000000"})
	checks := map[string]string{
		deferOption:          deferWaitValue,
		deferReasonOption:    "waiting on phenix-2: fix",
		deferOnOption:        "phenix-2",
		deferOnCreatedOption: "1763000000",
	}
	for o, want := range checks {
		if v, set, _ := argvSets(argv, o); !set || v != want {
			t.Errorf("%s = %q set=%v, want %q", o, v, set, want)
		}
	}
	if _, _, unset := argvSets(argv, deferOnSeenOption); !unset {
		t.Errorf("a new link must start unseen; %s not unset: %v", deferOnSeenOption, argv)
	}
}

func TestDeferLinkedArgsEmptyLinkIsPlain(t *testing.T) {
	got := deferLinkedArgs("%1", deferWaitValue, "why", deferLink{})
	want := deferValueArgs("%1", true, deferWaitValue, "why")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("zero link = %v, want %v", got, want)
	}
}

func TestDeferLinkedArgsReasonSemicolon(t *testing.T) {
	argv := deferLinkedArgs("%1", deferWaitValue, "waiting;", deferLink{"x", "5"})
	if v, _, _ := argvSets(argv, deferReasonOption); v != `waiting\;` {
		t.Errorf("reason = %q, want escaped semicolon", v)
	}
}

func TestParseDeferMark(t *testing.T) {
	m := parseDeferMark("wait\twaiting on x\tphenix-2\t1763000000\t1\n")
	want := deferMark{Raw: "wait", Reason: "waiting on x", Link: deferLink{"phenix-2", "1763000000"}, Seen: true}
	if m != want {
		t.Errorf("parse = %+v, want %+v", m, want)
	}
	if !m.Link.set() {
		t.Error("link should be set")
	}
	if z := parseDeferMark("\t\t\t\t\n"); z != (deferMark{}) {
		t.Errorf("empty line = %+v, want zero mark", z)
	}
	if parseDeferMark("1\tr\tx\t0\t\n").Link.set() {
		t.Error("created 0 must not count as a set link")
	}
}

func TestDeferArgs(t *testing.T) {
	got := deferArgs("api", true, "PR review")
	want := []string{"set-option", "-t", "api", deferOption, "1",
		";", "set-option", "-t", "api", deferReasonOption, "PR review"}
	want = append(want, unsetLinkTail("api")...)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deferArgs(on, reason) = %v, want %v", got, want)
	}
	// No blocker: the mark is set and any old reason is unset, so a previous
	// defer's blocker can't reappear.
	got = deferArgs("api", true, "  ")
	want = []string{"set-option", "-t", "api", deferOption, "1",
		";", "set-option", "-t", "api", "-u", deferReasonOption}
	want = append(want, unsetLinkTail("api")...)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deferArgs(on, blank) = %v, want %v", got, want)
	}
	// A trailing ";" is escaped, or tmux would swallow it as a separator.
	if got := deferArgs("api", true, "CI;"); got[10] != `CI\;` {
		t.Errorf("deferArgs(on, trailing ;) reason arg = %q, want %q", got[10], `CI\;`)
	}
	got = deferArgs("api", false, "ignored")
	want = []string{"set-option", "-t", "api", "-u", deferOption,
		";", "set-option", "-t", "api", "-u", deferReasonOption}
	want = append(want, unsetLinkTail("api")...)
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

// A handoff's mark is a defer that reads "wait", and deferValueArgs sets it.
func TestDeferWaitValue(t *testing.T) {
	if got := deferChip(deferWaitValue, "waiting on api: fix"); !strings.Contains(got, "◆ wait: waiting on api: fix") {
		t.Errorf("deferChip(wait, reason) = %q, want the wait label", got)
	}
	if got := ansi.Strip(deferChip(deferWaitValue, "")); got != "◆ wait" {
		t.Errorf("deferChip(wait) = %q, want %q", got, "◆ wait")
	}
	if !isDeferValue(deferWaitValue) || !isDeferValue(deferOnValue) || isDeferValue("0") {
		t.Error("isDeferValue must accept exactly the two set values")
	}
	got := deferValueArgs("api", true, deferWaitValue, "")
	want := []string{"set-option", "-t", "api", deferOption, "wait",
		";", "set-option", "-t", "api", "-u", deferReasonOption}
	want = append(want, unsetLinkTail("api")...)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deferValueArgs(wait) = %v, want %v", got, want)
	}
	if b := ansi.Strip(swDeferBadgeText(true)); b != "  WAIT " {
		t.Errorf("swDeferBadgeText(true) = %q, want %q", b, "  WAIT ")
	}
	if lipgloss.Width(swDeferBadgeText(true)) > lipgloss.Width(swDeferBadgeText(false)) {
		t.Error("WAIT must not be wider than DEFER, which the topic reserve measures")
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
