package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestParseBootArgs(t *testing.T) {
	o, err := parseBootArgs([]string{
		"--label", "Unlocking 1Password environment",
		"--project", "remix",
		"--expected", "25s",
		"--timeout", "90s",
		"--", "/usr/local/bin/claude", "--permission-mode", "auto",
	})
	if err != nil {
		t.Fatalf("parseBootArgs: %v", err)
	}
	if o.label != "Unlocking 1Password environment" {
		t.Errorf("label = %q", o.label)
	}
	if o.project != "remix" {
		t.Errorf("project = %q", o.project)
	}
	if o.expected != 25*time.Second {
		t.Errorf("expected = %v", o.expected)
	}
	if o.timeout != 90*time.Second {
		t.Errorf("timeout = %v", o.timeout)
	}
	want := []string{"/usr/local/bin/claude", "--permission-mode", "auto"}
	if !slices.Equal(o.argv, want) {
		t.Errorf("argv = %v, want %v", o.argv, want)
	}
}

// Flag-looking arguments after -- belong to the command. `claude --worktree`
// must not be read as a boot flag, and an unknown flag there must not fail the
// parse — that is the whole reason the split is done by hand.
func TestParseBootArgsCommandFlagsAreNotBootFlags(t *testing.T) {
	o, err := parseBootArgs([]string{"--", "claude", "--worktree", "--label", "x"})
	if err != nil {
		t.Fatalf("parseBootArgs: %v", err)
	}
	want := []string{"claude", "--worktree", "--label", "x"}
	if !slices.Equal(o.argv, want) {
		t.Errorf("argv = %v, want %v", o.argv, want)
	}
	if o.label != "Starting" {
		t.Errorf("label = %q, want the default", o.label)
	}
}

// Only the FIRST -- separates; a later one is part of the command.
func TestParseBootArgsSplitsAtFirstSeparator(t *testing.T) {
	o, err := parseBootArgs([]string{"--", "claude", "--", "prompt"})
	if err != nil {
		t.Fatalf("parseBootArgs: %v", err)
	}
	want := []string{"claude", "--", "prompt"}
	if !slices.Equal(o.argv, want) {
		t.Errorf("argv = %v, want %v", o.argv, want)
	}
}

// No command means there is nothing to become, which is the one thing this
// subcommand cannot do without.
func TestParseBootArgsRejectsMissingCommand(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--label", "x"},
		{"--"},
		{"--label", "x", "--"},
	} {
		if _, err := parseBootArgs(args); err == nil {
			t.Errorf("parseBootArgs(%v) = nil error, want a failure", args)
		}
	}
}

// A zero or negative timeout would make the dead-man switch fire on the first
// tick, turning the holder into a no-op.
func TestParseBootArgsRejectsNonPositiveTimeout(t *testing.T) {
	o, err := parseBootArgs([]string{"--timeout", "0s", "--", "claude"})
	if err != nil {
		t.Fatalf("parseBootArgs: %v", err)
	}
	if o.timeout != bootDefaultTimeout {
		t.Errorf("timeout = %v, want the default %v", o.timeout, bootDefaultTimeout)
	}
}

func TestFormatElapsed(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0:00"},
		{900 * time.Millisecond, "0:00"},
		{5 * time.Second, "0:05"},
		{25 * time.Second, "0:25"},
		{65 * time.Second, "1:05"},
		{754 * time.Second, "12:34"},
		{-time.Second, "0:00"}, // a clock skew must not render "-1:-1"
	}
	for _, tt := range tests {
		if got := formatElapsed(tt.d); got != tt.want {
			t.Errorf("formatElapsed(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestBootLines(t *testing.T) {
	o := bootOptions{
		label:    "Unlocking 1Password environment",
		project:  "remix",
		expected: 25 * time.Second,
		argv:     []string{"/opt/homebrew/bin/claude", "--permission-mode", "auto"},
	}
	got := strings.Join(bootLines(o, 12*time.Second, "⠹", 24), "\n")

	for _, want := range []string{
		"remix",
		"⠹  Unlocking 1Password environment",
		"0:12 / about 0:25",
		"claude starts here",
		"press any key to skip the wait",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lines missing %q:\n%s", want, got)
		}
	}
}

// Without an expected duration there is nothing to compare against, and
// inventing one would be a promise the launcher never made.
func TestBootLinesWithoutExpected(t *testing.T) {
	got := strings.Join(bootLines(bootOptions{label: "Starting", argv: []string{"claude"}}, 3*time.Second, "⠋", 24), "\n")
	if strings.Contains(got, "about") {
		t.Errorf("expected-duration text rendered with no expected set:\n%s", got)
	}
	if !strings.Contains(got, "0:03") {
		t.Errorf("elapsed clock missing:\n%s", got)
	}
}

// No project name means no heading and no blank line where it would have been.
func TestBootLinesWithoutProject(t *testing.T) {
	lines := bootLines(bootOptions{label: "Starting", argv: []string{"claude"}}, 0, "⠋", 24)
	if lines[0] == "" {
		t.Errorf("leading blank line with no project: %q", lines)
	}
}

// A short pane keeps the rows that carry the wait and drops the rest, rather
// than overflowing (lipgloss.Place does not clip vertically).
func TestBootLinesShedRowsToFitHeight(t *testing.T) {
	o := bootOptions{label: "Unlocking", project: "remix", expected: 25 * time.Second, argv: []string{"claude"}}
	for height := 1; height <= 8; height++ {
		lines := bootLines(o, 12*time.Second, "⠹", height)
		if len(lines) > height {
			t.Errorf("height %d: %d rows", height, len(lines))
		}
		if !strings.Contains(lines[0], "Unlocking") && height >= 1 && !strings.Contains(strings.Join(lines, "\n"), "Unlocking") {
			t.Errorf("height %d: lost the label: %q", height, lines)
		}
	}
	if got := bootLines(o, 0, "⠹", 0); got != nil {
		t.Errorf("height 0 rendered %q", got)
	}
}

func TestBootHint(t *testing.T) {
	tests := []struct {
		argv []string
		want string
	}{
		{[]string{"/opt/homebrew/bin/claude", "-n", "x"}, "claude starts here"},
		{[]string{"claude"}, "claude starts here"},
		{[]string{"/trailing/slash/"}, "slash starts here"},
		{nil, "the command starts here"},
	}
	for _, tt := range tests {
		if got := bootHint(tt.argv); !strings.HasPrefix(got, tt.want) {
			t.Errorf("bootHint(%v) = %q, want prefix %q", tt.argv, got, tt.want)
		}
	}
}

// Every rendered row must fit the pane: lipgloss wraps rather than truncates,
// and a wrapped row would shove the centred block off its line.
func TestBootViewFitsItsPane(t *testing.T) {
	o := bootOptions{
		label:    "Unlocking 1Password environment",
		project:  "a-very-long-project-name-that-will-not-fit",
		expected: 25 * time.Second,
		argv:     []string{"claude"},
	}
	for _, size := range []struct{ w, h int }{{120, 30}, {40, 10}, {12, 6}, {1, 1}} {
		out := bootView(o, 12*time.Second, "⠹", size.w, size.h)
		rows := strings.Split(out, "\n")
		if len(rows) != size.h {
			t.Errorf("%dx%d: rendered %d rows, want %d", size.w, size.h, len(rows), size.h)
		}
		for i, row := range rows {
			if w := lipgloss.Width(row); w > size.w {
				t.Errorf("%dx%d: row %d is %d cells wide", size.w, size.h, i, w)
			}
		}
	}
}

// A pane with no size yet (the first frame, before WindowSizeMsg lands) must
// render nothing rather than a block placed in a 0x0 area.
func TestBootViewUnsizedPane(t *testing.T) {
	if got := bootView(bootOptions{argv: []string{"claude"}}, 0, "⠋", 0, 0); got != "" {
		t.Errorf("bootView on an unsized pane = %q, want empty", got)
	}
}

func TestBootModelQuitsOnAnyKey(t *testing.T) {
	m := newBootModel(bootOptions{timeout: time.Minute, argv: []string{"claude"}}, time.Now())
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if cmd == nil {
		t.Fatal("no command from a key press")
	}
	if msg := cmd(); msg != tea.Quit() {
		t.Errorf("key press produced %#v, want tea.Quit", msg)
	}
}

// The dead-man switch: past the timeout the holder stops waiting, and runBoot
// execs anyway.
func TestBootModelQuitsAtTimeout(t *testing.T) {
	start := time.Now()
	m := newBootModel(bootOptions{timeout: 10 * time.Second, argv: []string{"claude"}}, start)

	// The pre-timeout tick schedules the next one; it is not called here
	// because tea.Tick's command sleeps out the interval before returning.
	next, cmd := m.Update(bootTickMsg(start.Add(9 * time.Second)))
	if cmd == nil {
		t.Error("no follow-up tick scheduled before the timeout")
	}
	if got := next.(bootModel).elapsed; got != 9*time.Second {
		t.Errorf("elapsed = %v, want 9s", got)
	}

	_, cmd = m.Update(bootTickMsg(start.Add(10 * time.Second)))
	if cmd == nil {
		t.Fatal("no command at the timeout")
	}
	if msg := cmd(); msg != tea.Quit() {
		t.Errorf("timeout produced %#v, want tea.Quit", msg)
	}
}

// The spinner has to advance on every tick or it reads as a frozen screen.
func TestBootModelAdvancesSpinner(t *testing.T) {
	start := time.Now()
	m := newBootModel(bootOptions{timeout: time.Minute, project: "p", label: "l", argv: []string{"claude"}}, start)
	m.width, m.height = 80, 20

	seen := map[string]bool{}
	for i := 1; i <= len(bootSpinnerFrames); i++ {
		next, _ := m.Update(bootTickMsg(start.Add(time.Duration(i) * bootFrameInterval)))
		m = next.(bootModel)
		seen[bootSpinnerFrames[m.frame%len(bootSpinnerFrames)]] = true
	}
	if len(seen) != len(bootSpinnerFrames) {
		t.Errorf("saw %d distinct frames over a full cycle, want %d", len(seen), len(bootSpinnerFrames))
	}
}

// The success path replaces the process, so only the failure path is testable
// here — and it must be loud, because bin/claudemux keeps the dead pane on
// screen precisely to show this message.
func TestExecBootMissingCommand(t *testing.T) {
	var buf bytes.Buffer
	if got := execBoot([]string{"claudemux-no-such-binary-xyz"}, &buf); got != 127 {
		t.Errorf("exit code = %d, want 127", got)
	}
	if !strings.Contains(buf.String(), "claudemux-no-such-binary-xyz") {
		t.Errorf("stderr did not name the command: %q", buf.String())
	}
}

func TestExecBootNoArgv(t *testing.T) {
	var buf bytes.Buffer
	if got := execBoot(nil, &buf); got != 2 {
		t.Errorf("exit code = %d, want 2", got)
	}
}

// A malformed invocation must not hang holding a pane forever; it exits 2 and
// says how to call it.
func TestRunBootRejectsBadArgs(t *testing.T) {
	var buf bytes.Buffer
	if got := runBoot([]string{"--label", "x"}, &buf); got != 2 {
		t.Errorf("exit code = %d, want 2", got)
	}
	if !strings.Contains(buf.String(), "usage:") {
		t.Errorf("no usage line: %q", buf.String())
	}
}
