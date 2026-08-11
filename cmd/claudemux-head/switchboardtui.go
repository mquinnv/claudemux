package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The switchboard's Bubble Tea shell: a full-screen fleet list that runs the
// poll/conduct loop. All decisions live in the conductor; this file only
// schedules polls, renders, and executes the returned action.

const swPollInterval = time.Second

// Lobby row column widths. Every row lays line 1 out on this fixed grid so the
// context meters — and the topics after them — stack in a column instead of
// drifting with each session's name, state string, and age. Fields wider than
// their column are truncated rather than allowed to push the grid.
const (
	swNameColW  = 24
	swStateColW = 14 // "Tool:AskUserQuestion" and friends get clipped here
	swAgeColW   = 6  // widest formatDuration output in practice ("23h59m")
	swCtxBarW   = 5
	swCtxColW   = swCtxBarW + 5 // bar + " 100%"
)

// swPad right-pads s to w display cells, measuring with lipgloss so ANSI
// styling and wide runes are counted correctly. Content wider than w is
// returned unchanged — callers truncate first when the column must hold.
func swPad(s string, w int) string {
	if n := w - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// swCell renders text as a fixed-width column cell: truncated to w, styled,
// then padded to w. The padding is applied outside the style so a cell's
// color (or a reverse highlight) stops at the text rather than smearing
// across the gap to the next column. An empty text yields a blank cell of the
// same width, keeping the columns to its right aligned.
func swCell(text string, w int, st lipgloss.Style, right bool) string {
	if text == "" {
		return strings.Repeat(" ", w)
	}
	// Truncate by display width, not rune count: a cell of wide (CJK) runes
	// clipped to w RUNES can still measure past w CELLS, overrunning the
	// column and pushing every column after it.
	text = ansi.Truncate(text, w, "…")
	pad := ""
	if n := w - lipgloss.Width(text); n > 0 {
		pad = strings.Repeat(" ", n)
	}
	if right {
		return pad + st.Render(text)
	}
	return st.Render(text) + pad
}

type swTickMsg time.Time

type swSnapshotMsg struct {
	snap swSnapshot
	err  error
}

var (
	swTitleStyle   = lipgloss.NewStyle().Bold(true)
	swWaitStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	swBusyStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	swUnknownStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	swSelStyle     = lipgloss.NewStyle().Reverse(true)
	swStatusStyle  = lipgloss.NewStyle().Faint(true)
)

type swModel struct {
	selfPane string
	snap     swSnapshot
	cond     conductor
	sel      int
	width    int
	height   int
	lastErr  string
	// standby stops all conducting (space toggles it): the lobby keeps
	// showing live states but the conductor is never stepped, so the user
	// can sit and look at the fleet without being dispatched.
	standby bool
}

func newSwModel(selfPane string) swModel {
	return swModel{selfPane: selfPane, cond: newConductor()}
}

func (m swModel) Init() tea.Cmd { return swPollCmd(m.selfPane) }

// swPollCmd runs the three tmux listings off the update loop. Any failure
// returns the error instead of a snapshot — the model keeps its previous
// data on screen and simply tries again next tick.
func swPollCmd(selfPane string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sessOut, err := swTmux(ctx, "list-sessions", "-F",
			"#{session_name}\t#{"+statePublishOption+"}\t#{"+statePublishSinceOption+"}\t#{"+infoContextOption+"}\t#{"+infoSummaryOption+"}\t#{"+infoPromptOption+"}")
		if err != nil {
			return swSnapshotMsg{err: err}
		}
		paneOut, err := swTmux(ctx, "list-panes", "-a", "-F",
			"#{session_name}\t#{pane_id}\t#{pane_current_command}\t#{window_name}")
		if err != nil {
			return swSnapshotMsg{err: err}
		}
		clientOut, err := swTmux(ctx, "list-clients", "-F",
			"#{client_name}\t#{client_session}")
		if err != nil {
			return swSnapshotMsg{err: err}
		}
		return swSnapshotMsg{snap: buildSwSnapshot(sessOut, paneOut, clientOut, selfPane)}
	}
}

func swTmux(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	return string(out), err
}

// swSwitchCmd moves a client. Fire-and-forget: if tmux refuses (client went
// away mid-tick), the next poll sees reality and the conductor re-decides.
func swSwitchCmd(client, target string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "tmux", "switch-client", "-c", client, "-t", target).Run()
		return nil
	}
}

func swNextTick() tea.Cmd {
	return tea.Tick(swPollInterval, func(t time.Time) tea.Msg { return swTickMsg(t) })
}

func (m swModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case swTickMsg:
		return m, swPollCmd(m.selfPane)
	case swSnapshotMsg:
		// Schedule the next tick from here, not from swTickMsg: polls never
		// overlap, and a slow tmux stretches the interval instead of queueing.
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, swNextTick()
		}
		m.lastErr = ""
		m.snap = msg.snap
		if m.sel >= len(m.snap.Sessions) {
			m.sel = len(m.snap.Sessions) - 1
		}
		if m.sel < 0 {
			m.sel = 0
		}
		if !m.standby {
			if act, ok := m.cond.step(m.snap); ok {
				return m, tea.Batch(swNextTick(), swSwitchCmd(act.Client, act.Target))
			}
		}
		return m, swNextTick()
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case " ":
			m.standby = !m.standby
			if m.standby {
				// The user asked to stop conducting, not to skip: neutralize
				// the conductor without snoozing whatever it was escorting.
				// Paused self-heals to Parked at the lobby once standby ends.
				m.cond.phase = swPaused
				m.cond.escortee = ""
			}
		case "j", "down":
			if m.sel < len(m.snap.Sessions)-1 {
				m.sel++
			}
		case "k", "up":
			if m.sel > 0 {
				m.sel--
			}
		case "enter":
			// A manual jump; the conductor notices the client moved on the
			// next poll and pauses — no special bookkeeping here.
			if m.sel < len(m.snap.Sessions) && m.cond.client != "" {
				return m, swSwitchCmd(m.cond.client, m.snap.Sessions[m.sel].Name)
			}
		}
	}
	return m, nil
}

func (m swModel) View() string {
	var b strings.Builder
	b.WriteString(swTitleStyle.Render("claudemux switchboard") + "\n\n")
	if len(m.snap.Sessions) == 0 {
		b.WriteString(swUnknownStyle.Render("no claudemux sessions") + "\n")
	}
	now := time.Now()
	for i, sess := range m.snap.Sessions {
		marker := "  "
		if isWaiting(sess.State) {
			marker = swWaitStyle.Render("● ")
		}
		state, style := sess.State, swBusyStyle
		switch {
		case sess.State == "":
			state, style = "unknown", swUnknownStyle
		case isWaiting(sess.State):
			style = swWaitStyle
		}
		age := ""
		if !sess.Since.IsZero() {
			age = formatDuration(now.Sub(sess.Since))
		}
		// Unset context (-1, pre-publish head or unparseable) renders a blank
		// cell rather than a misleading "-1%" — and a blank of the same width,
		// so one contextless session doesn't shift the rows under it.
		ctx := ""
		if sess.Context >= 0 {
			pct := float64(sess.Context)
			ctx = renderBar(swCtxBarW, pct, thresholdColor(pct)) + fmt.Sprintf(" %3d%%", sess.Context)
		}
		topic := ""
		if sess.Topic != "" {
			topic = "  " + swUnknownStyle.Render(sess.Topic)
		}
		// The name cell is padded before styling so the selection highlight
		// covers the whole column, not just the name's own runes. Truncated
		// by display width (not rune count) for the same reason as swCell: a
		// CJK name clipped to swNameColW runes still overruns the column in
		// cells.
		name := swPad(ansi.Truncate(sess.Name, swNameColW, "…"), swNameColW)
		if i == m.sel {
			name = swSelStyle.Render(name)
		}
		line := fmt.Sprintf(" %s%s %s%s  %s%s", marker, name,
			swCell(state, swStateColW, style, false),
			swCell(age, swAgeColW, swUnknownStyle, true),
			swPad(ctx, swCtxColW), topic)
		if m.width > 0 {
			line = clipLine(line, m.width)
		}
		b.WriteString(line + "\n")

		// Line 2: summary falls back to prompt, both falls back to
		// "summary · prompt"; omitted entirely when both are empty.
		detail := sess.Summary
		if detail == "" {
			detail = sess.Prompt
		} else if sess.Prompt != "" {
			detail = detail + " · " + sess.Prompt
		}
		if detail != "" {
			// Same width guard as line 1: a rune count is not a cell count,
			// and an unclipped line here wraps in the terminal and shifts
			// every row below it, destroying the column grid.
			line2 := fmt.Sprintf("    %s", swStatusStyle.Render(detail))
			if m.width > 0 {
				line2 = clipLine(line2, m.width)
			}
			b.WriteString(line2 + "\n")
		}
	}
	status := m.cond.statusLine(m.snap)
	if m.standby {
		status = fmt.Sprintf("standby · %d waiting — space to conduct",
			len(m.snap.waitingQueue(m.cond.snoozed)))
	}
	statusLine := swStatusStyle.Render(status)
	if m.width > 0 {
		statusLine = clipLine(statusLine, m.width)
	}
	b.WriteString("\n" + statusLine + "\n")
	if m.lastErr != "" {
		errLine := swStatusStyle.Render("tmux: " + m.lastErr)
		if m.width > 0 {
			errLine = clipLine(errLine, m.width)
		}
		b.WriteString(errLine + "\n")
	}
	// Cosmetic footer, but clipped for the same reason as the rows above it:
	// consistency, and a narrow pane shouldn't wrap it either.
	footer := swStatusStyle.Render("space conduct/standby · j/k select · enter jump · q quit")
	if m.width > 0 {
		footer = clipLine(footer, m.width)
	}
	b.WriteString(footer)
	return b.String()
}

// runSwitchboard is the `claudemux-head switchboard` entry point.
func runSwitchboard(stderr io.Writer) int {
	selfPane := os.Getenv("TMUX_PANE")
	if selfPane == "" {
		fmt.Fprintln(stderr, "claudemux-head switchboard must run inside tmux (start it with `claudemux switch`)")
		return 1
	}
	p := tea.NewProgram(newSwModel(selfPane), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}
