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
)

// The switchboard's Bubble Tea shell: a full-screen fleet list that runs the
// poll/conduct loop. All decisions live in the conductor; this file only
// schedules polls, renders, and executes the returned action.

const swPollInterval = time.Second

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
			"#{session_name}\t#{"+statePublishOption+"}\t#{"+statePublishSinceOption+"}")
		if err != nil {
			return swSnapshotMsg{err: err}
		}
		paneOut, err := swTmux(ctx, "list-panes", "-a", "-F",
			"#{session_name}\t#{pane_id}\t#{pane_current_command}")
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
			age = " " + swUnknownStyle.Render(formatDuration(now.Sub(sess.Since)))
		}
		name := fmt.Sprintf("%-24s", sess.Name)
		if i == m.sel {
			name = swSelStyle.Render(name)
		}
		fmt.Fprintf(&b, " %s%s %s%s\n", marker, name, style.Render(state), age)
	}
	status := m.cond.statusLine(m.snap)
	if m.standby {
		status = fmt.Sprintf("standby · %d waiting — space to conduct",
			len(m.snap.waitingQueue(m.cond.snoozed)))
	}
	b.WriteString("\n" + swStatusStyle.Render(status) + "\n")
	if m.lastErr != "" {
		b.WriteString(swStatusStyle.Render("tmux: "+m.lastErr) + "\n")
	}
	b.WriteString(swStatusStyle.Render("space conduct/standby · j/k select · enter jump · q quit"))
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
