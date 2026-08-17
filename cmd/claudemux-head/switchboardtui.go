package main

import (
	"bytes"
	"context"
	"errors"
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
	swModelColW = 13            // widest shortModel output ("sonnet 4.5 1M")
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
	// Account rate limits ride along on every poll, tmux error or not: the
	// meters are about the account, not the fleet, so a wedged tmux should
	// not blank them.
	at    time.Time
	rl    RateLimits
	rlErr error
	// conductReq is the raw @claudemux_conduct_request value — a head's space
	// key asking for a mode flip. Like the meters it survives a fleet-listing
	// error: standby is a local flag, and a wedged tmux is no reason to drop a
	// key the user pressed.
	conductReq string
}

var (
	swTitleStyle   = lipgloss.NewStyle().Bold(true)
	swWaitStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	swBusyStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	swUnknownStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	swSelStyle     = lipgloss.NewStyle().Reverse(true)
	swStatusStyle  = lipgloss.NewStyle().Faint(true)
	// Mode badge and status-line tints: conduct-vs-standby must be readable at
	// a glance from either end of the screen, not inferable from one faint
	// word at the bottom.
	swBadgeConductStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("232")).Background(lipgloss.Color("35"))
	swBadgeStandbyStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("232")).Background(lipgloss.Color("214"))
	swBadgePausedStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("254")).Background(lipgloss.Color("241"))
	swStatusConductStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("35"))
	swStatusStandbyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

// swNameStyle picks the style for a session's name cell.
//
// Only the name is tinted. The columns after it — state, age, context, model —
// carry their own meaning in color (orange waiting, blue busy, the context
// bar's thresholds), and tinting the whole row would put the project color in
// direct competition with all of them.
//
// Selection wins outright over the tint rather than compositing with it: the
// highlight has to stay legible against every color a project might pick,
// including one close to the terminal's own foreground.
//
// hex is a tmux user option, so anything at all can be in it. Only a bare
// 6-digit hex is honored — everything else renders plain, because handing junk
// to lipgloss.Color emits a broken escape sequence into the row.
func swNameStyle(hex string, selected bool) lipgloss.Style {
	if selected {
		return swSelStyle
	}
	if !isHex6(hex) {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#" + hex))
}

// swModeBadge renders the title-line mode badge. Escorting shows as
// CONDUCTING — it is the active form of it, and the status line below already
// names the escortee.
func swModeBadge(standby bool, phase swPhase) string {
	switch conductMode(standby, phase) {
	case "standby":
		return swBadgeStandbyStyle.Render(" STANDBY ")
	case "paused":
		return swBadgePausedStyle.Render(" PAUSED ")
	}
	return swBadgeConductStyle.Render(" CONDUCTING ")
}

type swModel struct {
	selfPane string
	snap     swSnapshot
	cond     conductor
	sel      int
	width    int
	height   int
	lastErr  string
	// Account budget meters, same source and shape as the head's: the abtop
	// rate-limits cache re-read on every poll, plus the recent five-hour
	// samples that feed the "empty in X" burn-rate ETA.
	rateLimitsPath string
	rateLimits     RateLimits
	rateOK         bool
	pctSamples     []pctSample
	// conductReq is the last head-requested toggle this lobby applied, kept as
	// the already-applied marker: the head leaves its request published, so
	// every poll re-reads the same press and only an unseen value may toggle.
	// Empty at startup, which needs no seeding — anything left in the option
	// from before this lobby existed is stale by then (see parseConductRequest).
	conductReq string
	// standby stops all conducting (space toggles it): the lobby keeps
	// showing live states but the conductor is never stepped, so the user
	// can sit and look at the fleet without being dispatched.
	standby bool
	// The preview of the selected session's claude pane. previewPane records
	// which pane previewOut came from; previewInFlight serializes captures so
	// a slow tmux cannot queue them up behind each other.
	previewPane     string
	previewOut      string
	previewErr      bool
	previewInFlight bool
	// New-session input mode (`n`): creating means the status line is a text
	// prompt and every printable key is a literal character of createInput.
	// createBusy marks a launch in flight (one at a time); createErr keeps the
	// last failed launch's reason on the status line until the next attempt.
	creating    bool
	createInput string
	createBusy  bool
	createErr   string
	// restart records that the user (R) or the binary watcher asked for a
	// re-exec rather than a quit; runSwitchboard checks it after Run
	// returns, mirroring the session head's flow in main().
	restart bool
	// fleetRestarting marks a ctrl+r sweep in flight: every head has been
	// (or is being) sent R, but self hasn't quit yet. Once a sweep is
	// dispatched, the ONLY allowed quit-to-restart path is swFleetRestartMsg's
	// completion — the auto-restart gate must not race the sweep out from
	// under it (a fresh build landing mid-sweep is exactly the situation a
	// fleet restart is usually pressed for), and a second ctrl+r must not
	// fire an overlapping sweep either.
	fleetRestarting bool
	// launchBin mirrors the session head's: the on-disk binary at launch,
	// re-stat'd each poll so a rebuilt lobby re-execs itself. This matters
	// MORE here than in the head — the lobby hosts the conductor, and a
	// stale conductor silently doesn't know newer published states.
	launchBin   binStamp
	launchBinOK bool
}

func newSwModel(selfPane string) swModel {
	m := swModel{selfPane: selfPane, cond: newConductor(), rateLimitsPath: defaultRateLimitsPath()}
	m.launchBin, m.launchBinOK = launchBinStamp()
	return m
}

// shouldAutoRestart reports whether this poll may re-exec into a rebuilt
// binary. Only from a quiescent lobby: standby and the create prompt are
// in-memory and would not survive the re-exec, and escorting/paused hold
// snoozes and an escortee whose loss would un-skip sessions the user just
// walked away from. swParked is usually the lobby's resting state, so
// waiting for it delays the upgrade by seconds, not sessions — but a live
// snooze can outlive the escort that created it: step() snoozes the
// abandoned session and drops straight into swParked, so the lobby can sit
// parked with conductor.snoozed still holding an entry. conductor.snoozed
// is in-memory only, so a re-exec right then would discard it and the fresh
// conductor would immediately re-escort the user to the session they just
// walked away from — the exact bounce-back the snooze exists to prevent.
// So the restart also waits for snoozed to drain, worst case the full
// swSnoozeTTL. It also stays off for the duration of a fleet-restart sweep
// (fleetRestarting): swRestartFleetCmd's sends run in a goroutine outside
// this model, so a poll landing mid-sweep must not race it into quitting
// early — the same changed-binary condition that made shouldAutoRestart true
// is usually exactly what prompted the user to press ctrl+r in the first
// place, so this case is not rare.
func (m *swModel) shouldAutoRestart(now time.Time) bool {
	return !m.standby && !m.creating && !m.createBusy && !m.fleetRestarting &&
		m.cond.phase == swParked && len(m.cond.snoozed) == 0 &&
		m.launchBinOK && binChanged(m.launchBin, now)
}

// toggleStandby flips conduct mode. Shared by the lobby's own space key and by
// head-requested toggles (conductRequestOption) so a head's space and the
// lobby's cannot drift into meaning different things.
func (m *swModel) toggleStandby() {
	m.standby = !m.standby
	if m.standby {
		// The user asked to stop conducting, not to skip: neutralize the
		// conductor without snoozing whatever it was escorting. Paused
		// self-heals to Parked at the lobby once standby ends.
		m.cond.phase = swPaused
		m.cond.escortee = ""
		// step() never runs while standby is on, so the client can move around
		// invisibly (elsewhere and back) before standby ends. A hand-back
		// latched before standby started can't be trusted to still describe
		// what happened at this session — clear it so resuming re-observes
		// from scratch.
		m.cond.clearPaused()
	}
}

func (m swModel) Init() tea.Cmd { return swPollCmd(m.selfPane, m.rateLimitsPath) }

// selectedPane returns the claude pane of the selected session — "" when there
// is no selection, or when that session has no claude pane to capture.
func (m swModel) selectedPane() string {
	if m.sel < 0 || m.sel >= len(m.snap.Sessions) {
		return ""
	}
	return m.snap.Sessions[m.sel].ClaudePane
}

// previewCmd requests a capture of the current selection, returning nil when
// there is nothing to capture or one is already running. Moving off the pane
// the held capture came from clears it, so the box never shows one session's
// screen under another's title while the new capture is in flight.
func (m *swModel) previewCmd() tea.Cmd {
	pane := m.selectedPane()
	if pane != m.previewPane {
		m.previewOut, m.previewPane, m.previewErr = "", "", false
	}
	if pane == "" || m.previewInFlight {
		return nil
	}
	m.previewInFlight = true
	return swPreviewCmd(pane)
}

// swPollCmd runs the three tmux listings off the update loop. Any failure
// returns the error instead of a snapshot — the model keeps its previous
// data on screen and simply tries again next tick. The rate-limits cache is
// read up front so every message carries it, even the tmux-error ones.
func swPollCmd(selfPane, rlPath string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		rl, rlErr := readRateLimits(rlPath)
		// Read ahead of the listings so a head's toggle still lands on a poll
		// whose fleet listing fails — see swSnapshotMsg.conductReq.
		msg := swSnapshotMsg{at: time.Now(), rl: rl, rlErr: rlErr, conductReq: readConductRequestOption(ctx)}
		sessOut, err := swTmux(ctx, "list-sessions", "-F",
			"#{session_name}\t#{"+statePublishOption+"}\t#{"+statePublishSinceOption+"}\t#{"+infoContextOption+"}\t#{"+infoSummaryOption+"}\t#{"+infoPromptOption+"}\t#{"+infoModelOption+"}\t#{"+infoColorOption+"}")
		if err != nil {
			msg.err = err
			return msg
		}
		paneOut, err := swTmux(ctx, "list-panes", "-a", "-F",
			"#{session_name}\t#{pane_id}\t#{pane_current_command}\t#{window_name}")
		if err != nil {
			msg.err = err
			return msg
		}
		clientOut, err := swTmux(ctx, "list-clients", "-F",
			"#{client_name}\t#{client_session}")
		if err != nil {
			msg.err = err
			return msg
		}
		msg.snap = buildSwSnapshot(sessOut, paneOut, clientOut, selfPane)
		return msg
	}
}

func swTmux(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	return string(out), err
}

type swCreateMsg struct {
	name string // created session's name; "" on error
	err  error
}

// swLastLine is the last non-empty line of a command's output, trimmed —
// claudemux -d prints exactly the session name, but taking the last line keeps
// this robust against anything else that learns to chat on the same stream.
func swLastLine(out string) string {
	last := ""
	for _, l := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(l); s != "" {
			last = s
		}
	}
	return last
}

// swCreateCmd launches a new claudemux session for query via the launcher
// script: `claudemux -n -d` builds the session (forcing a new one on a name
// collision, exactly like the user's own `claudemux -n`) and prints its name
// instead of attaching — attaching is impossible from inside tmux, and the
// caller here wants to switch-client anyway. The query resolves the same way
// it does on the command line: a real directory, else the best zoxide match.
// The fleet list needs no help picking the session up; the next poll sees it.
func swCreateCmd(query string) tea.Cmd {
	return func() tea.Msg {
		// Generous timeout: the launcher shells out to tmux, git, and config
		// lookups. Its ~25s op_env wait is backgrounded and doesn't hold the
		// -d exit up.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "claudemux", "-n", "-d", "--", query)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			// The launcher's stderr says why — an unresolved query, a tmux
			// failure — and its last line beats a bare exit status.
			if detail := swLastLine(stderr.String()); detail != "" {
				return swCreateMsg{err: errors.New(detail)}
			}
			return swCreateMsg{err: err}
		}
		name := swLastLine(stdout.String())
		if name == "" {
			return swCreateMsg{err: errors.New("claudemux -d printed no session name")}
		}
		return swCreateMsg{name: name}
	}
}

// swSwitchCmd moves a client. Fire-and-forget: if tmux refuses (client went
// away mid-tick), the next poll sees reality and the conductor re-decides.
// With banner set, a successful switch is followed by the arrival popup on the
// target session (see banner.go) — only successful, so a vanished client
// doesn't get a banner announcing a move that never happened.
func swSwitchCmd(client, target string, banner bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, "tmux", "switch-client", "-c", client, "-t", target).Run(); err != nil || !banner {
			return nil
		}
		self, err := os.Executable()
		if err != nil {
			return nil
		}
		// The popup blocks for up to swBannerHold by design; give it its own
		// timeout with headroom rather than sharing the switch's 2s budget.
		bctx, bcancel := context.WithTimeout(context.Background(), swBannerHold+3*time.Second)
		defer bcancel()
		_ = exec.CommandContext(bctx, "tmux", swBannerPopupArgs(self, client, target)...).Run()
		return nil
	}
}

// swSwitchLastCmd sends a client back to its last session — tmux's own
// per-client "last session", which is wherever the client was before it landed
// on the lobby. Fire-and-forget like swSwitchCmd: a client with no last
// session (attached straight to the lobby) makes tmux refuse, and the lobby
// simply stays put.
func swSwitchLastCmd(client string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "tmux", "switch-client", "-c", client, "-l").Run()
		return nil
	}
}

// swFleetRestartMsg reports that every session head has been sent the R
// restart key; the lobby restarts itself only on receipt, so the sends are
// complete before this process quits.
type swFleetRestartMsg struct{}

// swFleetRestartArgs builds one tmux send-keys argv per session head pane.
// Sessions with no recorded head pane are skipped — send-keys to an empty
// target would land in whatever pane tmux resolves, which could be a claude
// prompt.
func swFleetRestartArgs(sessions []swSession) [][]string {
	var argvs [][]string
	for _, sess := range sessions {
		if sess.HeadPane == "" {
			continue
		}
		argvs = append(argvs, []string{"send-keys", "-t", sess.HeadPane, "R"})
	}
	return argvs
}

// swRestartFleetCmd types the R restart key into every session head, then
// reports completion. R has been the head's restart key for months, so this
// reaches heads running binaries too old to auto-restart themselves — which
// is exactly when a fleet restart is wanted. Sends run sequentially with the
// same per-call deadline as every other tmux shell-out; a send that fails
// (dead pane, wedged tmux) is skipped rather than aborting the sweep.
func swRestartFleetCmd(sessions []swSession) tea.Cmd {
	argvs := swFleetRestartArgs(sessions)
	return func() tea.Msg {
		for _, args := range argvs {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = exec.CommandContext(ctx, "tmux", args...).Run()
			cancel()
		}
		return swFleetRestartMsg{}
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
		return m, swPollCmd(m.selfPane, m.rateLimitsPath)
	case swSnapshotMsg:
		// Rate limits first, before the tmux-error early return: the meters
		// track the account and stay fresh even when tmux is misbehaving.
		// Sample bookkeeping mirrors the head's dataMsg handling — record a
		// sample only when the percentage moves, trim to the last hour.
		if msg.rlErr == nil {
			m.rateLimits = msg.rl
			m.rateOK = true
			if len(m.pctSamples) == 0 || m.pctSamples[len(m.pctSamples)-1].pct != msg.rl.FiveHour.UsedPercent {
				m.pctSamples = append(m.pctSamples, pctSample{at: msg.at, pct: msg.rl.FiveHour.UsedPercent})
			}
			cutoff := msg.at.Add(-1 * time.Hour)
			trimmed := m.pctSamples[:0]
			for _, s := range m.pctSamples {
				if s.at.After(cutoff) {
					trimmed = append(trimmed, s)
				}
			}
			m.pctSamples = trimmed
		} else {
			m.rateOK = false
		}
		// Apply a head's space before publishing below, so the mode this poll
		// announces already includes a toggle this same poll picked up rather
		// than lagging it by a beat. The token is the raw request value: the
		// head leaves it published, so every poll re-reads the same press, and
		// only a value this lobby hasn't applied yet counts as a new one.
		if tok, ok := parseConductRequest(msg.conductReq, msg.at); ok && tok != m.conductReq {
			m.conductReq = tok
			m.toggleStandby()
		}
		// Publish the conduct-mode heartbeat on every poll, tmux error or not:
		// the heads' staleness window (conductStaleAfter) is sized against this
		// beat, and mode toggles republish immediately on their own.
		pub := publishConductCmd(conductMode(m.standby, m.cond.phase), time.Now())
		// Schedule the next tick from here, not from swTickMsg: polls never
		// overlap, and a slow tmux stretches the interval instead of queueing.
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, tea.Batch(swNextTick(), pub)
		}
		m.lastErr = ""
		m.snap = msg.snap
		if m.sel >= len(m.snap.Sessions) {
			m.sel = len(m.snap.Sessions) - 1
		}
		if m.sel < 0 {
			m.sel = 0
		}
		if m.shouldAutoRestart(time.Now()) {
			m.restart = true
			return m, tea.Quit
		}
		// Refresh the preview on the same beat as the fleet. tea.Batch drops
		// nil commands, so this is a no-op when there is nothing to capture.
		// The conductor also sits out the create flow: dispatching the client
		// away mid-typing would yank the user off the prompt, and dispatching
		// while a launch is in flight would race the switch the launch is
		// about to issue itself.
		pv := m.previewCmd()
		if !m.standby && !m.creating && !m.createBusy {
			if act, ok := m.cond.step(m.snap, time.Now()); ok {
				return m, tea.Batch(swNextTick(),
					swSwitchCmd(act.Client, act.Target, swWantsBanner(act.Target, m.snap.Lobby)), pv, pub)
			}
		}
		return m, tea.Batch(swNextTick(), pv, pub)
	case swFleetRestartMsg:
		// Self last: every head has been told to restart; now pick up the
		// new binary here too, via the same after-Run re-exec as R.
		m.restart = true
		return m, tea.Quit
	case swPreviewMsg:
		// Always clear the flag, stale or not: a dropped result that left the
		// flag set would wedge the preview for the rest of the session.
		m.previewInFlight = false
		if msg.pane != m.selectedPane() {
			// The selection moved on before this capture landed. Re-issue for
			// wherever the cursor is NOW rather than dropping it entirely:
			// previewCmd clears previewOut on a pane change, so without this
			// the box goes blank until the next poll — up to a full second
			// behind a fast j/k, exactly the lag the selection-change
			// trigger exists to remove. This terminates: previewCmd returns
			// nil once the selection settles (or has no claude pane), and
			// the in-flight guard still allows only one capture at a time.
			return m, m.previewCmd()
		}
		m.previewPane = msg.pane
		m.previewErr = msg.err != nil
		m.previewOut = msg.out
		return m, nil
	case swCreateMsg:
		m.createBusy = false
		if msg.err != nil {
			m.createErr = msg.err.Error()
			return m, nil
		}
		m.createErr = ""
		// Jump straight to the new session; the next poll adds it to the
		// list. Same shape as an enter-jump: the conductor sees the client
		// moved and pauses until the user returns to the lobby — and like an
		// enter-jump, no arrival banner: the user asked to go there.
		if m.cond.client != "" {
			return m, swSwitchCmd(m.cond.client, msg.name, false)
		}
		return m, nil
	case tea.KeyMsg:
		if m.creating {
			switch msg.String() {
			case "esc", "ctrl+c":
				m.creating = false
				m.createInput = ""
			case "enter":
				query := strings.TrimSpace(m.createInput)
				m.creating = false
				m.createInput = ""
				if query != "" {
					m.createBusy = true
					m.createErr = ""
					return m, swCreateCmd(query)
				}
			case "backspace":
				if r := []rune(m.createInput); len(r) > 0 {
					m.createInput = string(r[:len(r)-1])
				}
			default:
				// Only literal text lands in the query — navigation chords
				// and function keys are ignored rather than typed. Space is
				// its own key type in Bubble Tea, and a zoxide query can
				// legitimately contain one.
				if msg.Type == tea.KeyRunes && !msg.Alt {
					m.createInput += string(msg.Runes)
				} else if msg.Type == tea.KeySpace {
					m.createInput += " "
				}
			}
			return m, nil
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "R":
			// Same contract as the session head's R: quit, and let
			// runSwitchboard re-exec the binary from disk.
			m.restart = true
			return m, tea.Quit
		case "ctrl+r":
			// Restart the whole fleet: every session head, then self. A
			// second press while a sweep is already in flight is a no-op —
			// dispatching another would race two goroutines' send-keys
			// against each other and double the sends.
			if m.fleetRestarting {
				return m, nil
			}
			m.fleetRestarting = true
			return m, swRestartFleetCmd(m.snap.Sessions)
		case "n":
			// One launch at a time: a second prompt opened while one is in
			// flight could race it for the client.
			if !m.createBusy {
				m.creating = true
				m.createInput = ""
				m.createErr = ""
			}
		case " ":
			m.toggleStandby()
			// Republish immediately rather than waiting out the poll beat, so
			// the heads' chips track the toggle as fast as the badge does.
			return m, publishConductCmd(conductMode(m.standby, m.cond.phase), time.Now())
		case "j", "down":
			if m.sel < len(m.snap.Sessions)-1 {
				m.sel++
				// Two-line form, not `return m, m.previewCmd()`: Go orders
				// function calls relative to each other, not relative to a
				// plain operand, so a single-expression return would rely on
				// evaluation order the language spec doesn't guarantee for
				// the pointer-receiver mutation inside previewCmd.
				cmd := m.previewCmd()
				return m, cmd
			}
		case "k", "up":
			if m.sel > 0 {
				m.sel--
				cmd := m.previewCmd()
				return m, cmd
			}
		case "enter":
			// A manual jump; the conductor notices the client moved on the
			// next poll and pauses — no special bookkeeping here. No banner
			// either: the user chose this destination themselves.
			if m.sel < len(m.snap.Sessions) && m.cond.client != "" {
				return m, swSwitchCmd(m.cond.client, m.snap.Sessions[m.sel].Name, false)
			}
		case "esc":
			// Back out of the lobby to wherever the client came from. Same
			// conductor story as enter: the client moves, the next poll sees
			// it, and the conductor pauses.
			if m.cond.client != "" {
				return m, swSwitchLastCmd(m.cond.client)
			}
		}
	}
	return m, nil
}

// swSessionRows is how many lines a session's row occupies: two when it has a
// summary or prompt to show under it, one when it has neither. Kept next to
// View's own detail-line logic, which must agree with it — if they disagree,
// the list cap below is wrong by one row per session.
func swSessionRows(sess swSession) int {
	if sess.Summary != "" || sess.Prompt != "" {
		return 2
	}
	return 1
}

// swMetersLine renders the account budget gauges as a full-width line — the
// head's renderMetersLine minus the per-session ctx gauge, since the lobby
// has no session of its own. Same degradation and growth rules: when the
// line overflows the pane width, gauges drop from the end in the head's
// order (eta, then wk; 5h always stays), and the surviving bars then widen
// past defaultBarW to consume the leftover columns.
func swMetersLine(m swModel, now time.Time) string {
	build := func(barW int) []string {
		return rateGauges(m.rateLimits, m.pctSamples, now, barW)
	}
	if m.width <= 0 {
		// No size yet: emit the baseline gauges unstyle rather than fitting
		// to a width we don't know.
		return " " + strings.Join(build(defaultBarW), " · ")
	}
	avail := m.width - 2 // columns inside the " "..." " padding below
	if avail < 1 {
		avail = 1
	}
	parts := build(defaultBarW)
	for len(parts) > 1 && lipgloss.Width(strings.Join(parts, " · ")) > avail {
		parts = parts[:len(parts)-1]
	}

	// Only 5h and wk carry bars — the trailing eta segment is plain text — so
	// the slack splits across those two, or the eta's share would just become
	// trailing blank space. Integer division leaves a few columns unspent
	// rather than risking an overflow that clipLine would then chop.
	barCount := len(parts)
	if barCount > 2 {
		barCount = 2
	}
	if grow := (avail - lipgloss.Width(strings.Join(parts, " · "))) / barCount; grow > 0 {
		wide := build(defaultBarW + grow)[:len(parts)]
		if lipgloss.Width(strings.Join(wide, " · ")) <= avail {
			parts = wide
		}
	}
	return statusbarStyle.Width(m.width).Render(clipLine(" "+strings.Join(parts, " · ")+" ", m.width))
}

func (m swModel) View() string {
	now := time.Now()
	var b strings.Builder
	title := swTitleStyle.Render("claudemux switchboard") + "  " + swModeBadge(m.standby, m.cond.phase)
	if m.width > 0 {
		title = clipLine(title, m.width)
	}
	b.WriteString(title + "\n")
	// The account budget meters sit under the title, where the head keeps its
	// meters line: full-width 5h/wk gauges with reset times, plus the ETA when
	// there's burn-rate signal. Omitted entirely (no blank placeholder) when
	// the cache is unreadable — computePreviewLayout is told either way.
	if m.rateOK {
		b.WriteString(swMetersLine(m, now) + "\n")
	}
	b.WriteString("\n")
	if len(m.snap.Sessions) == 0 {
		b.WriteString(swUnknownStyle.Render("no claudemux sessions") + "\n")
	}

	// Rows each session wants on screen. The layout needs this up front so the
	// preview can grow into rows a small fleet would otherwise leave blank.
	want := 0
	for _, sess := range m.snap.Sessions {
		want += swSessionRows(sess)
	}
	lay := computePreviewLayout(m.height, m.lastErr != "", m.rateOK, want)

	// Only when the fleet's total need EXCEEDS the cap is anything dropped —
	// and then one row is held back so the "+N more" line announcing the
	// truncation doesn't itself push the preview box off the bottom.
	budget := lay.ListRows
	if budget > 0 {
		if want <= budget {
			budget = 0 // everything fits; no cap needed
		} else {
			budget--
		}
	}

	used, shown := 0, 0
	for i, sess := range m.snap.Sessions {
		// budget == 0 means uncapped (no preview drawn, or the fleet already
		// fits) — the loop then runs to completion exactly as it did before
		// this task.
		if budget > 0 && used+swSessionRows(sess) > budget {
			break
		}
		marker := "  "
		if isWaiting(sess.State) {
			if m.cond.isSnoozed(sess, now) {
				marker = swUnknownStyle.Render("● ") // waiting, deliberately skipped
			} else {
				marker = swWaitStyle.Render("● ")
			}
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
		// Unset model (pre-publish head) renders a blank cell of the same
		// width, like context: the topics after it must not shift.
		modelTxt := ""
		if sess.Model != "" {
			modelTxt = shortModel(sess.Model)
		}
		// The name cell is padded before styling so the selection highlight
		// covers the whole column, not just the name's own runes. Truncated
		// by display width (not rune count) for the same reason as swCell: a
		// CJK name clipped to swNameColW runes still overruns the column in
		// cells.
		name := swNameStyle(sess.Color, i == m.sel).
			Render(swPad(ansi.Truncate(sess.Name, swNameColW, "…"), swNameColW))
		line := fmt.Sprintf(" %s%s %s%s  %s %s%s", marker, name,
			swCell(state, swStateColW, style, false),
			swCell(age, swAgeColW, swUnknownStyle, true),
			swPad(ctx, swCtxColW),
			swCell(modelTxt, swModelColW, swUnknownStyle, false), topic)
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
		used += swSessionRows(sess)
		shown++
	}
	if shown < len(m.snap.Sessions) {
		// Same width guard as every other line here: this text is short, but
		// consistency is cheaper to maintain than a special case.
		moreLine := fmt.Sprintf("    %s", swStatusStyle.Render(
			fmt.Sprintf("… +%d more", len(m.snap.Sessions)-shown)))
		if m.width > 0 {
			moreLine = clipLine(moreLine, m.width)
		}
		b.WriteString(moreLine + "\n")
	}

	if lay.Show && len(m.snap.Sessions) > 0 {
		var lines []string
		switch {
		case m.selectedPane() == "":
			lines = []string{swStatusStyle.Render("no claude pane")}
		case m.previewErr:
			lines = []string{swStatusStyle.Render("preview unavailable")}
		default:
			lines = previewTail(m.previewOut, lay.Content)
		}
		// The box is full pane width, not the row's swNameColW column, so its
		// title gets the box's own room to breathe — renderPreview clips it
		// (display-width aware) to whatever previewTopBorder has left after
		// the frame. Real session names cross swNameColW (24) in normal use,
		// so passing the untruncated name here means the box can show more
		// of it than the row can.
		title := m.snap.Sessions[m.sel].Name
		// renderPreview's lines are already exactly m.width display cells (or
		// nil for a box too small to draw). Emit the separator only when the
		// box will actually render — otherwise a sub-8-column pane gets a
		// blank row where the box would have been.
		box := renderPreview(title, lines, m.width, lay.Content)
		if box != nil {
			b.WriteString("\n")
			for _, l := range box {
				b.WriteString(l + "\n")
			}
		}
	}

	status := m.cond.statusLine(m.snap, now)
	if m.standby {
		status = fmt.Sprintf("standby · %d waiting — space to conduct",
			len(m.snap.waitingQueue(m.cond.snoozed, now)))
	}
	// The create flow borrows the status line rather than adding one, so the
	// layout (and computePreviewLayout's accounting of it) never shifts.
	statusStyled := true
	// Tinted to match the title badge — conducting green, standby orange — so
	// both ends of the screen agree about the mode. The create texts keep the
	// neutral faint style: they are about the launch, not the conductor.
	statusStyle := swStatusStyle
	switch conductMode(m.standby, m.cond.phase) {
	case "conducting":
		statusStyle = swStatusConductStyle
	case "standby":
		statusStyle = swStatusStandbyStyle
	}
	switch {
	case m.creating:
		status = "new session: " + m.createInput + "▌"
		statusStyled = false // unstyled so the typed query stands out
	case m.createBusy:
		status = "creating session…"
		statusStyle = swStatusStyle
	case m.createErr != "":
		status = "create failed: " + m.createErr
		statusStyle = swStatusStyle
	}
	statusLine := status
	if statusStyled {
		statusLine = statusStyle.Render(status)
	}
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
	footerText := "space conduct/standby · j/k select · enter jump · esc back · n new · R restart · ^R restart all · q quit"
	if m.creating {
		footerText = "enter create · esc cancel"
	}
	footer := swStatusStyle.Render(footerText)
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
	final, err := p.Run()
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	// Same re-exec-after-Run contract as main()'s session-head path: the
	// terminal is restored before the replacement starts, and a failed
	// exec leaves the pane open with the reason visible.
	if fm, ok := final.(swModel); ok && fm.restart {
		restartSelf(stderr)
		return 1
	}
	// A quit for good: drop the conduct option now so heads lose their chip
	// immediately instead of waiting out the staleness window. A restarting
	// lobby skips this (above) — its replacement is publishing again well
	// inside that window, so the chips never blink.
	unsetConductOption()
	return 0
}
