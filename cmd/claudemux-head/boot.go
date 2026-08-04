package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// `claudemux-head boot` holds a tmux pane while something slow happens outside
// it, then becomes the program it was asked to run.
//
// It exists because launching claude in a project with an op_env means waiting
// on `op environment read`, which is not a flicker: measured on op 2.38.1,
// already authorized, three consecutive reads of a ONE-variable Environment
// took 27.4s, 25.4s and 22.4s. bin/claudemux used to spend that time showing an
// interactive shell with an echoed notice in it — a prompt, a typed command,
// and a second prompt underneath — which is both ugly and indistinguishable
// from a session that failed to start.
//
// The contract with bin/claudemux is that this pane BECOMES claude. Every exit
// path here ends in exec: the wait can be cut short by a keypress, abandoned on
// a timeout, or skipped entirely when there is no terminal to draw on, and all
// three still launch the command. A holder that could exit without launching
// would leave the user with a pane that closed for no reason.

// bootSpinnerFrames is the aliveness signal. Braille dots at bootFrameInterval
// read as motion without pulling attention the way a bar or a percentage does —
// there is no progress to report, only the fact that we are still here.
var bootSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const (
	// bootFrameInterval paces the spinner and the elapsed clock. It has to
	// divide a second evenly or the clock visibly stutters between whole
	// seconds.
	bootFrameInterval = 100 * time.Millisecond

	// bootDefaultTimeout is a dead-man switch, not a wait budget. The launcher
	// normally replaces this pane the moment the secrets land; this only fires
	// when the process that was going to do that is gone (the terminal was
	// closed mid-wait, `op` wedged past its own timeouts). Generous enough that
	// it never pre-empts a merely slow read, short enough that an abandoned
	// pane still ends up in claude while the user is at their desk.
	bootDefaultTimeout = 120 * time.Second
)

// bootOptions is a parsed `boot` invocation. argv is the command to exec once
// the wait ends, already split into arguments — bin/claudemux passes it after a
// `--`, so tmux's shell does the splitting and nothing here has to re-parse a
// quoted command line.
type bootOptions struct {
	label    string
	project  string
	expected time.Duration
	timeout  time.Duration
	argv     []string
}

// parseBootArgs splits a `boot` argument list at the first "--": flags before,
// the command to exec after.
//
// The split is done here rather than by the flag package because everything
// after "--" belongs to the command verbatim — a `claude --worktree` must not
// be read as a boot flag, and `flag` would stop at the first non-flag argument
// anyway, silently leaving the command in Args().
func parseBootArgs(args []string) (bootOptions, error) {
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		return bootOptions{}, fmt.Errorf("missing -- followed by a command to run")
	}
	flagArgs, argv := args[:sep], args[sep+1:]
	if len(argv) == 0 || argv[0] == "" {
		return bootOptions{}, fmt.Errorf("no command after --")
	}

	fs := flag.NewFlagSet("boot", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	label := fs.String("label", "Starting", "what is being waited on")
	project := fs.String("project", "", "project name shown above the spinner")
	expected := fs.Duration("expected", 0, "typical duration of the wait; 0 hides it")
	timeout := fs.Duration("timeout", bootDefaultTimeout, "give up waiting and run the command anyway")
	if err := fs.Parse(flagArgs); err != nil {
		return bootOptions{}, err
	}
	if *timeout <= 0 {
		*timeout = bootDefaultTimeout
	}

	return bootOptions{
		label:    *label,
		project:  *project,
		expected: *expected,
		timeout:  *timeout,
		argv:     argv,
	}, nil
}

// formatElapsed renders a duration as m:ss. Minutes are unpadded and seconds
// are: a wait measured in tens of seconds should read as a stopwatch, not as a
// duration with units.
func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

// bootLines builds the holding screen's rows for a pane height rows tall.
// Split out from bootView so the content can be asserted without a centering
// pass.
//
// The elapsed clock is the reason this screen exists at all. Twenty-five
// seconds of a static splash is indistinguishable from a hang; a running clock
// against the typical duration ("0:12 / about 0:25") says both "alive" and "how
// much longer". It deliberately keeps counting past the expected value instead
// of pinning at it, so a read that got slower reads as slow rather than stuck.
//
// Rows are added only while they fit, essentials first, because lipgloss.Place
// does not clip: a block taller than the pane pushes rows out of the pane's
// bottom and breaks View's one-row-per-line invariant. The spinner and the
// clock are what a wait needs; the hint and the project heading are what it can
// spare, in that order.
func bootLines(o bootOptions, elapsed time.Duration, frame string, height int) []string {
	if height <= 0 {
		return nil
	}

	clock := formatElapsed(elapsed)
	if o.expected > 0 {
		clock += " / about " + formatElapsed(o.expected)
	}
	lines := []string{
		strings.TrimSpace(frame + "  " + o.label),
		promptStyle.Render(clock),
	}

	if len(lines)+2 <= height {
		lines = append(lines, "", promptStyle.Render(bootHint(o.argv)))
	}
	if o.project != "" && len(lines)+2 <= height {
		lines = append([]string{promptLabelStyle.Render(o.project), ""}, lines...)
	}

	if len(lines) > height {
		lines = lines[:height]
	}
	return lines
}

// bootHint names the program the pane is about to become, so the screen says
// what happens next rather than only what is happening now. The escape hatch is
// spelled out because it is lossy: a key press launches immediately, which
// means launching WITHOUT whatever the wait was going to provide.
// The name is the basename because bin/claudemux resolves the binary to an
// absolute path before handing it over (see its claude_bin comment); "claude
// starts here" is the sentence, not "/opt/homebrew/bin/claude starts here".
func bootHint(argv []string) string {
	name := "the command"
	if len(argv) > 0 && argv[0] != "" {
		name = filepath.Base(argv[0])
	}
	return name + " starts here · press any key to skip the wait"
}

// bootView centers the holding screen in the pane.
//
// Every line is clipped to the pane width for the same reason the status bar
// clips: lipgloss wraps rather than truncates, and a wrapped line would push
// the block off-centre. A pane too small to hold the block renders as much as
// fits — the launcher's claude pane is most of a window, so this is defensive
// rather than expected.
func bootView(o bootOptions, elapsed time.Duration, frame string, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	lines := bootLines(o, elapsed, frame, height)
	for i, line := range lines {
		lines[i] = clipLine(line, width)
	}
	block := lipgloss.JoinVertical(lipgloss.Center, lines...)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, block)
}

// bootTickMsg advances the spinner and the clock. Named apart from tui.go's
// tickMsg because both live in package main and mean different things.
type bootTickMsg time.Time

func bootTick() tea.Cmd {
	return tea.Tick(bootFrameInterval, func(t time.Time) tea.Msg { return bootTickMsg(t) })
}

type bootModel struct {
	opts    bootOptions
	start   time.Time
	elapsed time.Duration
	frame   int
	width   int
	height  int
}

func newBootModel(o bootOptions, start time.Time) bootModel {
	return bootModel{opts: o, start: start}
}

func (m bootModel) Init() tea.Cmd {
	// Hide the cursor for the duration: it would otherwise sit wherever the
	// last write left it, blinking in the middle of a screen with no input.
	return tea.Batch(bootTick(), tea.HideCursor)
}

// Update ends the wait on any key or at the timeout. Both quit the program, and
// runBoot execs regardless of which one did — there is no "cancelled" outcome
// to distinguish, because there is no outcome here other than launching.
func (m bootModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m, tea.Quit
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case bootTickMsg:
		m.elapsed = time.Time(msg).Sub(m.start)
		m.frame++
		if m.elapsed >= m.opts.timeout {
			return m, tea.Quit
		}
		return m, bootTick()
	}
	return m, nil
}

func (m bootModel) View() string {
	frame := bootSpinnerFrames[m.frame%len(bootSpinnerFrames)]
	return bootView(m.opts, m.elapsed, frame, m.width, m.height)
}

// execBoot replaces this process with argv.
//
// syscall.Exec, not exec.Command: the pane's foreground process must BE the
// program, or tmux reports `claudemux-head` as pane_current_command forever and
// every consumer of it — the pane map the head follows, the teardown's
// exit-wait, deferred_launch's own guard — is looking at the wrong process. It
// also means no wrapper is left holding the pty.
//
// A failure here is reported and exits non-zero on purpose: bin/claudemux sets
// remain-on-exit=failed on this pane, so the message stays on screen instead of
// the pane silently closing.
func execBoot(argv []string, stderr io.Writer) int {
	if len(argv) == 0 {
		fmt.Fprintln(stderr, "claudemux-head boot: nothing to run")
		return 2
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head boot: %v\n", err)
		return 127
	}
	if err := syscall.Exec(path, argv, os.Environ()); err != nil {
		fmt.Fprintf(stderr, "claudemux-head boot: exec %s: %v\n", path, err)
		return 126
	}
	return 0 // unreachable: a successful Exec never returns
}

// runBoot is the `boot` subcommand: hold the pane, then become the command.
//
// A TUI that will not start (no tty, an unusable terminal) is not fatal and is
// not even reported — the wait was cosmetic, the launch is not.
func runBoot(args []string, stderr io.Writer) int {
	opts, err := parseBootArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head boot: %v\n", err)
		fmt.Fprintln(stderr, "usage: claudemux-head boot [--label TEXT] [--project NAME] [--expected DUR] [--timeout DUR] -- COMMAND [ARG...]")
		return 2
	}
	p := tea.NewProgram(newBootModel(opts, time.Now()), tea.WithAltScreen())
	_, _ = p.Run()
	return execBoot(opts.argv, stderr)
}
