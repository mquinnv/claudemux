package main

import (
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// `claudemux-head onboard` is the layout picker: bin/claudemux runs it on a
// session's first launch (when launch.layout is still unset) and `claudemux
// setup` runs it any time after. It is a small, standalone bubbletea
// program in the style of boot.go — plain model, no ticking, no altscreen —
// because unlike boot's holding screen this one has a real outcome to leave
// on screen: the confirmation (or cancellation) line has to stay in normal
// scrollback after the program exits, not vanish with an alt-screen swap.

// onboardColW is the display width of every diagram box, header line
// included — the four boxes in task-3-brief.md's diagrams are all exactly
// this wide, and the header lines are padded out to match so the two
// columns of the grid line up.
const onboardColW = 16

// onboardGap is the blank run between the grid's two columns, matching
// task-3-brief.md's diagrams exactly (16 + 8 + 16 = 40 total).
const onboardGap = "        "

// The diagrams below are copied character-for-character from
// task-3-brief.md — box-drawing runes, spacing, and all. Getting these
// wrong is a silent failure (nothing checks a rendered diagram against the
// pane arrangement it names), so they are quoted here exactly rather than
// built up from parts.
var (
	diagShellRight = []string{
		"┌──────────────┐",
		"│ head         │",
		"├────────┬─────┤",
		"│ claude │shell│",
		"│        │     │",
		"└────────┴─────┘",
	}
	diagNoShell = []string{
		"┌──────────────┐",
		"│ head         │",
		"├──────────────┤",
		"│ claude       │",
		"│              │",
		"└──────────────┘",
	}
	diagShellBottom = []string{
		"┌──────────────┐",
		"│ head         │",
		"├──────────────┤",
		"│ claude       │",
		"│              │",
		"├──────────────┤",
		"│ shell        │",
		"└──────────────┘",
	}
	diagHeadBottom = []string{
		"┌────────┬─────┐",
		"│ claude │shell│",
		"│        │     │",
		"├────────┴─────┤",
		"│ head         │",
		"└──────────────┘",
	}
)

// onboardLayout pairs a legalLayouts value with its on-screen number, label,
// and diagram. onboardLayouts below is deliberately in the same order as
// legalLayouts: that order is both the 1-4 numbering shown on screen and
// the (0,1 top row)/(2,3 bottom row) grid pairing the view renders.
type onboardLayout struct {
	name    string
	header  string
	diagram []string
}

var onboardLayouts = []onboardLayout{
	{"shell-right", "1) shell-right", diagShellRight},
	{"no-shell", "2) no-shell", diagNoShell},
	{"shell-bottom", "3) shell-bottom", diagShellBottom},
	{"head-bottom", "4) head-bottom", diagHeadBottom},
}

var onboardSelStyle = lipgloss.NewStyle().Reverse(true)

// onboardConfirmLine is the exact line task-3-brief.md specifies for a
// successful save.
const onboardConfirmLine = "claudemux: layout saved — change it anytime with: claudemux setup"

// onboardCancelLine explains a cancel's consequence: nothing was written, so
// bin/claudemux keeps treating the unset layout as shell-right until the
// user opts in through `claudemux setup`.
const onboardCancelLine = "claudemux: no layout saved — the default (shell-right) applies until you run: claudemux setup"

// onboardModel is the picker's state. writeLayout is injected rather than
// calling configSet directly so tests can observe what would have been
// written without touching disk — onboardConfigWriter is the production
// wiring runOnboard uses.
type onboardModel struct {
	sel   int
	width int // only the width is tracked: with no altscreen, a tall
	// picker simply scrolls rather than needing to fit a fixed viewport.

	cancelled bool
	saved     string // the layout name written on a successful confirm
	saveErr   error

	writeLayout func(name string) error
}

// onboardConfigWriter is runOnboard's writeLayout: the Task 2 write path,
// called directly (not shelled out to `config set`) so a save failure here
// carries the same comment-preserving, validate-before-write guarantees.
func onboardConfigWriter(name string) error {
	return configSet("launch.layout", name)
}

func newOnboardModel(write func(name string) error) onboardModel {
	return onboardModel{writeLayout: write}
}

// Cursor blink has nowhere sensible to land on a screen with no text
// input, so it's hidden for the same reason boot.go hides it.
func (m onboardModel) Init() tea.Cmd { return tea.HideCursor }

// onboardGridMove maps a direction key to the next selection in the 2x2
// grid (0,1 top row; 2,3 bottom row) — the same pairing the view renders,
// so left/right/up/down always move toward the box that's visually there.
// A move off the grid's edge is a no-op rather than wrapping, matching how
// j/k already behave in the switchboard's own list.
func onboardGridMove(sel int, key string) int {
	switch key {
	case "left", "h":
		if sel%2 == 1 {
			return sel - 1
		}
	case "right", "l":
		if sel%2 == 0 {
			return sel + 1
		}
	case "up", "k":
		if sel >= 2 {
			return sel - 2
		}
	case "down", "j":
		if sel < 2 {
			return sel + 2
		}
	}
	return sel
}

func (m onboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		case "enter":
			name := onboardLayouts[m.sel].name
			if m.writeLayout != nil {
				m.saveErr = m.writeLayout(name)
			}
			if m.saveErr == nil {
				m.saved = name
			}
			return m, tea.Quit
		case "1", "2", "3", "4":
			m.sel = int(msg.String()[0] - '1')
			return m, nil
		default:
			m.sel = onboardGridMove(m.sel, msg.String())
			return m, nil
		}
	}
	return m, nil
}

// onboardPadRight pads s out to w display cells, measuring with lipgloss so
// the box-drawing runes above (which are all single-width) line up exactly
// the way plain ASCII would.
func onboardPadRight(s string, w int) string {
	if n := w - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// onboardRow joins two layout blocks (header + diagram) side by side, in
// task-3-brief.md's exact spacing. shell-bottom's diagram is two rows
// taller than the other three (it has a shell pane the others don't); when
// the right column runs out first, this simply stops emitting it rather
// than padding blank cells after it — that is what makes the rendered grid
// match the brief's example character for character.
func onboardRow(left, right onboardLayout, leftSel, rightSel bool) []string {
	lb := append([]string{left.header}, left.diagram...)
	rb := append([]string{right.header}, right.diagram...)

	n := len(lb)
	if len(rb) > n {
		n = len(rb)
	}

	rows := make([]string, n)
	for i := 0; i < n; i++ {
		var l, r string
		if i < len(lb) {
			l = lb[i]
		}
		if i < len(rb) {
			r = rb[i]
		}
		// Only the LEFT header needs padding: it sits before the gap, so
		// the right column has to start at a fixed column. The right
		// header has nothing after it on the line, so task-3-brief.md
		// leaves it at its natural width — padding it would add trailing
		// spaces the brief's example doesn't have. Diagram lines need no
		// padding either way; they are already onboardColW cells wide.
		if i == 0 {
			l = onboardPadRight(l, onboardColW)
			if leftSel {
				l = onboardSelStyle.Render(l)
			}
			if r != "" && rightSel {
				r = onboardSelStyle.Render(r)
			}
		}
		if r == "" {
			rows[i] = l
		} else {
			rows[i] = l + onboardGap + r
		}
	}
	return rows
}

func (m onboardModel) View() string {
	var b strings.Builder
	b.WriteString(promptLabelStyle.Render("claudemux: choose a pane layout for new sessions") + "\n\n")
	b.WriteString(strings.Join(onboardRow(onboardLayouts[0], onboardLayouts[1], m.sel == 0, m.sel == 1), "\n"))
	b.WriteString("\n\n")
	b.WriteString(strings.Join(onboardRow(onboardLayouts[2], onboardLayouts[3], m.sel == 2, m.sel == 3), "\n"))
	b.WriteString("\n\n")
	b.WriteString(promptStyle.Render("←→↑↓/hjkl/1-4 select · enter confirm · q/esc cancel"))

	out := b.String()
	if m.width <= 0 {
		return out
	}
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = clipLine(l, m.width)
	}
	return strings.Join(lines, "\n")
}

// onboardResult turns a finished onboardModel into the process's exit code,
// printing exactly what task-3-brief.md specifies for each outcome. Split
// out from runOnboard so the exit-code mapping is testable without a real
// terminal to run the bubbletea program against.
func onboardResult(m onboardModel, stderr io.Writer) int {
	switch {
	case m.saved != "":
		fmt.Fprintln(stderr, onboardConfirmLine)
		return 0
	case m.saveErr != nil:
		fmt.Fprintf(stderr, "claudemux-head onboard: %v\n", m.saveErr)
		return 3
	default:
		fmt.Fprintln(stderr, onboardCancelLine)
		return 1
	}
}

// runOnboard is the `claudemux-head onboard` entry point.
func runOnboard(stderr io.Writer) int {
	p := tea.NewProgram(newOnboardModel(onboardConfigWriter))
	final, err := p.Run()
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head onboard: %v\n", err)
		return 1
	}
	m, ok := final.(onboardModel)
	if !ok {
		fmt.Fprintln(stderr, "claudemux-head onboard: internal error")
		return 1
	}
	return onboardResult(m, stderr)
}
