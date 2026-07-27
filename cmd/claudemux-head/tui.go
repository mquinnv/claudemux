package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Styles
var (
	dotIdle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575")).Render("●")
	dotThinking = lipgloss.NewStyle().Foreground(lipgloss.Color("#3B82F6")).Render("●")
	dotTool     = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFCC00")).Render("●")
	dotError    = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Render("●")
	dotCompact  = lipgloss.NewStyle().Foreground(lipgloss.Color("#A855F7")).Render("●")

	statusbarStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#1a1a1a")).
			Foreground(lipgloss.Color("#cccccc"))

	promptStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#1a1a1a")).
			Foreground(lipgloss.Color("#7a7a7a"))

	promptLabelStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#1a1a1a")).
				Foreground(lipgloss.Color("#a0a0a0")).
				Bold(true)
)

type tickMsg time.Time

// summaryCallTimeout is the OUTER bound on a whole summarize() command — the
// SDK's per-request summaryRequestTimeout plus its one retry, plus request
// setup — not a second copy of the per-request timeout.
const summaryCallTimeout = 30 * time.Second

type summaryMsg struct {
	// gen is the model's summaryGen at the moment the call was issued. The
	// session can rotate while a call is in flight, and a summary computed
	// over the old session's events says nothing about the new one; Update
	// drops a message whose gen no longer matches.
	gen     int
	summary Summary
	err     error
	at      time.Time
}

type model struct {
	// Config
	jsonlPath string
	sessionID string

	// followActive makes the monitor re-bind to the most-recently-active
	// .jsonl in the project dir each poll, so it follows a session that
	// rotates underneath it (new session, /clear, resume, compaction).
	// Disabled when an explicit --session was given (the user pinned it).
	followActive bool

	// selfPane/paneDir drive pane-accurate binding: when running inside
	// tmux, each poll prefers the transcript the SessionStart/
	// UserPromptSubmit hook recorded for the sibling claude pane over the
	// MRA glob. The glob cross-binds when two envs share a repo and goes
	// permanently stale when claude runs in a worktree (transcripts land
	// in a different encoded project dir).
	selfPane string
	paneDir  string

	// sessionCwd is the latest cwd the *main* session recorded in its
	// transcript (last non-sidechain entry's cwd), recomputed from the event
	// ring each poll. It — not tmux's pane cwd — drives the worktree chip: the
	// claude process never chdir's when the agent enters a worktree (the
	// EnterWorktree/ExitWorktree and cd-in-a-tool paths all leave the OS
	// process where it launched), so tmux's pane_current_path stays pinned to
	// the base repo, while the transcript's cwd faithfully tracks every move
	// in and out. Subagent sidechains are excluded so a Task running off in
	// its own worktree doesn't hijack the head's chip.
	sessionCwd string

	// cmdWorktree is the worktree the session is driving *at arm's length* —
	// its cwd stays in the main repo while its commands reach into a linked
	// worktree by explicit path (git -C <wt>, cd <wt> && …, container names).
	// Recomputed each poll from the recent command window; "" when no single
	// worktree dominates. Only consulted when sessionCwd itself isn't a
	// worktree (see worktreeChip).
	cmdWorktree string

	// Persistent state
	reader         *EventReader
	allEvents      []Event     // bounded ring (cap 1000)
	rateLimitsPath string      // ~/.claude/abtop-rate-limits.json or override
	pctSamples     []pctSample // 5h-window snapshots over time, for burn-rate

	// Latest snapshot
	state       State
	modelName   string
	contextPct  float64
	firstPrompt string
	lastPrompt  string
	summary     Summary
	rateLimits  RateLimits
	rateOK      bool

	// UI
	lastUpdate         time.Time
	width              int
	height             int
	ready              bool
	polling            bool
	summarizer         *Summarizer
	minSummaryInterval time.Duration
	tabTitle           bool
	summarizing        bool
	lastSummaryAt      time.Time
	// summaryGen identifies the session a summarize call was issued for. It is
	// bumped on every switchSession, so a reply that lands after a rotation can
	// be recognized as belonging to the previous session and dropped.
	summaryGen int
	err        error
}

func newModel(cfg Config, jsonlPath, sessionID string, followActive bool) model {
	r := newEventReader(jsonlPath)
	r.SeedFromEnd(500)
	seeded, _ := r.Seeded()

	summarizer := newSummarizer(cfg.Summary)

	m := model{
		jsonlPath:      jsonlPath,
		sessionID:      sessionID,
		followActive:   followActive,
		selfPane:       os.Getenv("TMUX_PANE"),
		paneDir:        paneMapDir(),
		reader:         r,
		allEvents:      seeded,
		rateLimitsPath: defaultRateLimitsPath(),
		firstPrompt:    r.FirstPrompt(),
		// Init always issues the first poll itself (see Init below), so the
		// flag starts held to prevent the first 1s tick from firing a second,
		// concurrent poll that races on EventReader.offset.
		polling:            true,
		summarizer:         summarizer,
		minSummaryInterval: cfg.Summary.MinInterval.Duration,
		tabTitle:           cfg.Summary.TabTitle,
		// Init unconditionally fires the seed summarize call when summarizer
		// != nil (see Init below); this flag must already be held at that
		// point, for the same reason polling starts true above — Init has a
		// value receiver and cannot set it itself, so a fast busy→idle edge
		// on the very first poll would otherwise race a second concurrent
		// call against the seed call.
		summarizing: summarizer != nil,
	}
	m.recomputeFromEvents(time.Now())
	return m
}

func (m *model) recomputeFromEvents(now time.Time) {
	m.state = classifyState(m.allEvents, now)
	for i := len(m.allEvents) - 1; i >= 0; i-- {
		// Skip placeholder models like "<synthetic>" (error/bookkeeping
		// events) — show the last real API model instead.
		if mm := m.allEvents[i].Model; mm != "" && !strings.HasPrefix(mm, "<") {
			m.modelName = mm
			break
		}
	}
	if last := lastUsage(m.allEvents); last != nil {
		m.contextPct = contextPercent(m.modelName, *last)
	}
	m.lastPrompt = lastUserPrompt(m.allEvents)
	if m.reader != nil {
		m.firstPrompt = m.reader.FirstPrompt()
	}
	m.sessionCwd = lastMainCwd(m.allEvents, m.sessionCwd)
	m.cmdWorktree = commandWorktree(m.sessionCwd, m.allEvents, now)
}

// lastMainCwd returns the cwd of the most recent main-session (non-sidechain)
// event that carries one, scanning the ring newest-first. prev is returned
// unchanged when the ring holds no such event this poll — a tail of pure
// sidechain (subagent) activity must not blank the chip, and a not-yet-seeded
// ring must not clear a cwd already known. It only ever moves forward to a real
// observed cwd.
func lastMainCwd(events []Event, prev string) string {
	for i := len(events) - 1; i >= 0; i-- {
		if e := events[i]; e.Cwd != "" && !e.IsSidechain {
			return e.Cwd
		}
	}
	return prev
}

// switchSession re-binds the monitor to a different session .jsonl: it opens a
// fresh reader, re-seeds from the end, and recomputes all derived state.
// Called when the active session rotates and the monitor is in follow-active
// mode. It returns the summarize command to run for the new session (nil when
// the guards say no); the caller MUST return that command, or the rotated pane
// sits on the raw-prompt fallback until the next turn boundary.
func (m *model) switchSession(jsonlPath string, now time.Time) tea.Cmd {
	sessionID := strings.TrimSuffix(filepath.Base(jsonlPath), ".jsonl")
	r := newEventReader(jsonlPath)
	r.SeedFromEnd(500)
	seeded, _ := r.Seeded()

	m.jsonlPath = jsonlPath
	m.sessionID = sessionID
	m.reader = r
	m.allEvents = seeded
	m.firstPrompt = r.FirstPrompt()
	// Clean slate: the new session's own cwd must come from its own events, not
	// linger from the session we just left. Its seed always carries one.
	m.sessionCwd = ""
	// Same for the context gauge and model: recomputeFromEvents only overwrites
	// them when the new ring carries a usage/model event, and a just-started
	// session (only the first user prompt on disk) carries neither — without
	// this reset the old session's near-full ctx% renders against the new one.
	m.contextPct = 0
	m.modelName = ""

	// The new session starts clean: the old session's topic must not survive as
	// the next call's prevTopic, because summarySystemPrompt tells the model to
	// keep a previous topic verbatim unless the session has clearly moved on.
	// Bumping the generation marks any call still in flight over the old
	// session's events as stale (see the summaryMsg case in Update).
	//
	// lastSummaryAt is deliberately NOT reset: the rate floor is a global
	// invariant on API calls, not a per-session one. Zeroing it would make
	// now.Sub(lastSummaryAt) ~2000 years, so a session-rotation flap could fire
	// a call on every edge with no floor at all. That invariant also depends on
	// the summaryMsg handler advancing lastSummaryAt unconditionally, even for a
	// stale reply — a rotation flap makes every reply land stale, and if the
	// floor only advanced for a kept reply it would freeze at whatever value is
	// here, which is exactly the same no-floor-at-all failure this comment
	// warns against.
	m.summary = Summary{}
	m.summaryGen++

	m.recomputeFromEvents(now)

	// Seed the new session's status lines now — mirroring what Init does at
	// startup — rather than leaving the pane on the raw-prompt fallback until the
	// next busy→idle edge. Still interval-guarded, and the flag is held here
	// because the pointer receiver can (unlike Init's value receiver).
	if !m.canSummarize(now) {
		return nil
	}
	m.summarizing = true
	return m.summarize()
}

func lastUsage(events []Event) *Usage {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Usage != nil {
			return events[i].Usage
		}
	}
	return nil
}

// genuinePrompt reports whether e carries text the human actually sent.
// Harness bookkeeping also arrives as user turns: isMeta caveat notices,
// injected task notifications / system reminders (text starts with "<" —
// humans don't open messages with XML). Slash commands pass; callers that
// want to de-prioritize them do so themselves.
func genuinePrompt(e Event) bool {
	if e.Type != "last-prompt" && e.Type != "user" {
		return false
	}
	if e.IsMeta || e.UserText == "" {
		return false
	}
	return !strings.HasPrefix(strings.TrimSpace(e.UserText), "<")
}

// lastUserPrompt returns the text of the most recent thing the user sent.
// Claude Code emits an explicit `last-prompt` event holding the clean prompt
// text; real `user` turns also carry it as plain-string content (tool_result
// turns leave UserText empty, so they're skipped). Whichever is newest in the
// event stream wins. Harness bookkeeping (isMeta notices, injected XML) is
// filtered out via genuinePrompt.
func lastUserPrompt(events []Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if genuinePrompt(events[i]) {
			return events[i].UserText
		}
	}
	return ""
}

// firstUserPrompt returns the oldest thing the human sent — the session's
// context line. Slash commands like "/clear" say nothing about what the
// session is for, so non-command prompts win; a command is returned only
// when the session has nothing else (e.g. a window whose whole purpose is
// one slash command).
func firstUserPrompt(events []Event) string {
	fallback := ""
	for _, e := range events {
		if !genuinePrompt(e) {
			continue
		}
		if !strings.HasPrefix(e.UserText, "/") {
			return e.UserText
		}
		if fallback == "" {
			fallback = e.UserText
		}
	}
	return fallback
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.pollData(), m.tick()}
	if m.summarizer != nil {
		cmds = append(cmds, m.summarize())
	}
	return tea.Batch(cmds...)
}

func (m model) tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m model) pollData() tea.Cmd {
	reader := m.reader
	jsonlPath := m.jsonlPath
	rlPath := m.rateLimitsPath
	follow := m.followActive
	selfPane := m.selfPane
	paneDir := m.paneDir
	return func() tea.Msg {
		// Follow session rotation: if a newer .jsonl has appeared in the
		// project dir, surface it so Update can re-bind. Empty when not
		// following or nothing newer exists.
		activeJSONL := ""
		if follow {
			if mapped, _, ok := mappedTranscript(selfPane, paneDir); ok {
				// mapped is "" when the pane's live cwd is known but its
				// transcript isn't yet — keep the current binding then rather
				// than adopting an empty path.
				if mapped != "" && mapped != jsonlPath {
					activeJSONL = mapped
				}
			} else if mra, ok := mostRecentlyActiveSession(filepath.Dir(jsonlPath)); ok && mra != jsonlPath {
				activeJSONL = mra
			}
		}
		newEvents, _ := reader.Tail()
		rl, rlErr := readRateLimits(rlPath)
		return dataMsg{
			time:         time.Now(),
			activeJSONL:  activeJSONL,
			newEvents:    newEvents,
			rateLimits:   rl,
			rateLimitErr: rlErr,
		}
	}
}

type dataMsg struct {
	time         time.Time
	activeJSONL  string // non-empty when a newer session file should be adopted
	newEvents    []Event
	rateLimits   RateLimits
	rateLimitErr error
}

// shouldSummarize reports whether this poll crossed the busy → idle edge, which
// is usually a finished turn but not always: classifyState calls any assistant
// event carrying text Idle, so an assistant that emits prose and then a tool
// call in separate JSONL events shows a mid-turn Idle blip that also fires.
// That's acceptable — the in-flight flag serializes calls, and the rate floor
// (summary.min_interval) bounds how often they may fire, so short back-to-back
// edges can't hammer the API. Note the floor is user-configurable and may be
// set to 0, which disables it: the in-flight flag is then the only guard, and
// it serializes calls without bounding them.
func (m model) shouldSummarize(prevKind StateKind, now time.Time) bool {
	if !m.canSummarize(now) {
		return false
	}
	return prevKind != StateIdle && m.state.Kind == StateIdle
}

// canSummarize reports whether a summarize call may be issued at all: the
// feature is enabled, no call is in flight, and the rate floor has elapsed.
// These guards hold for every caller — the busy→idle edge and a session
// rotation alike.
func (m model) canSummarize(now time.Time) bool {
	if m.summarizer == nil || m.summarizing {
		return false
	}
	return now.Sub(m.lastSummaryAt) >= m.minSummaryInterval
}

// tabCmdFor returns the window-rename command for a freshly landed summary, or
// nil when the tab title is disabled, we are not in tmux, or the label is empty.
func (m model) tabCmdFor(s Summary) tea.Cmd {
	if !m.tabTitle {
		return nil
	}
	return renameTabCmd(m.selfPane, s.Tab)
}

func (m model) summarize() tea.Cmd {
	s := m.summarizer
	first := m.firstPrompt
	events := m.allEvents
	prevTopic := m.summary.Topic
	gen := m.summaryGen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), summaryCallTimeout)
		defer cancel()
		out, err := s.Summarize(ctx, first, events, prevTopic)
		return summaryMsg{gen: gen, summary: out, err: err, at: time.Now()}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true

	case tickMsg:
		if m.polling {
			return m, m.tick()
		}
		m.polling = true
		return m, tea.Batch(m.pollData(), m.tick())

	case dataMsg:
		m.polling = false
		// Session rotated: re-bind to the newer file and discard this batch's
		// events (they were tailed from the old reader). They refresh on the
		// next poll.
		if msg.activeJSONL != "" && msg.activeJSONL != m.jsonlPath {
			cmd := m.switchSession(msg.activeJSONL, msg.time)
			m.lastUpdate = msg.time
			return m, cmd
		}
		if len(msg.newEvents) > 0 {
			m.allEvents = append(m.allEvents, msg.newEvents...)
			if len(m.allEvents) > 1000 {
				m.allEvents = m.allEvents[len(m.allEvents)-1000:]
			}
		}
		if msg.rateLimitErr == nil {
			m.rateLimits = msg.rateLimits
			m.rateOK = true
			if len(m.pctSamples) == 0 || m.pctSamples[len(m.pctSamples)-1].pct != msg.rateLimits.FiveHour.UsedPercent {
				m.pctSamples = append(m.pctSamples, pctSample{at: msg.time, pct: msg.rateLimits.FiveHour.UsedPercent})
			}
			cutoff := msg.time.Add(-1 * time.Hour)
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
		prevKind := m.state.Kind
		m.recomputeFromEvents(msg.time)
		m.lastUpdate = msg.time
		if m.shouldSummarize(prevKind, msg.time) {
			m.summarizing = true
			return m, m.summarize()
		}

	case summaryMsg:
		// Clear the in-flight flag FIRST and unconditionally: this message is
		// the call's completion whatever else we do with it, and returning
		// early with the flag still held would wedge the summarizer for the
		// life of the process.
		m.summarizing = false
		// Advance the rate floor FIRST and unconditionally, same reasoning as
		// clearing the in-flight flag above: the call was issued and billed
		// whatever we do with its result. Advancing only for a kept (non-stale)
		// reply leaves the floor stuck at its initial value under a
		// session-rotation flap, where the reply always lands after the next
		// rotation and so is always stale, which makes canSummarize permanently
		// true and fires a fresh call on every single rotation, unbounded.
		m.lastSummaryAt = msg.at
		// Stale: the session rotated while this call was in flight, so its
		// topic/now describe a session we are no longer watching. Drop it —
		// storing it would also make it the next call's prevTopic, which the
		// prompt asks the model to keep verbatim.
		if msg.gen != m.summaryGen {
			return m, nil
		}
		if msg.err == nil {
			m.summary = msg.summary
			return m, m.tabCmdFor(msg.summary)
		}
	}

	return m, nil
}

func (m model) View() string {
	if !m.ready {
		return "Loading..."
	}

	// Pin the status block to the TOP of the pane: state and meters lead,
	// prompt context follows as room allows. Below height 2 there's no room
	// for a split state/meters view, so fall back to today's single packed
	// statusbar (worktree chip included, truncated to 24 runes).
	now := time.Now()
	var lines []string
	switch {
	case m.height <= 1:
		lines = []string{renderStatusbar(m, now, m.worktreeChip())}
	case m.height == 2:
		lines = []string{renderStateLine(m, now), renderMetersLine(m, now)}
	case m.height == 3:
		_, _, bottomLabel, bottom := m.promptRows()
		lines = []string{
			renderStateLine(m, now),
			renderMetersLine(m, now),
			renderPromptLine(bottomLabel, bottom, m.width),
		}
	default: // height >= 4
		topLabel, top, bottomLabel, bottom := m.promptRows()
		lines = []string{
			renderStateLine(m, now),
			renderMetersLine(m, now),
			renderPromptLine(topLabel, top, m.width),
			renderPromptLine(bottomLabel, bottom, m.width),
		}
	}
	fill := m.height - len(lines)
	if fill < 0 {
		fill = 0
	}
	return strings.Join(lines, "\n") + strings.Repeat("\n", fill)
}

// promptRows resolves the two context rows. The Haiku summary is preferred; with
// no summary yet — no API key, a failed call, or nothing back — the pane falls back
// to the raw first/last prompts, so nothing regresses without a key.
func (m model) promptRows() (topLabel, top, bottomLabel, bottom string) {
	if m.summary.Topic != "" && m.summary.Now != "" {
		return "topic", m.summary.Topic, "now  ", m.summary.Now
	}
	return "first", m.firstPrompt, "last ", m.lastPrompt
}

// renderPromptLine renders a single prompt as a background-filled line,
// collapsing whitespace and truncating to width. The label (e.g. "first" or
// "last") is shown dimmed before the prompt text to disambiguate stacked
// lines.
func renderPromptLine(label, prompt string, width int) string {
	if width < 1 {
		width = 1
	}
	text := strings.Join(strings.Fields(prompt), " ")
	if text == "" {
		text = "—"
	}
	prefix := "❯ "
	if label != "" {
		prefix = label + " ❯ "
	}

	avail := width - 2 // columns inside the " "..." " padding below
	if avail < 1 {
		avail = 1
	}

	// Truncate the plain string (no ANSI escapes) so rune counting stays
	// accurate, then dim-style the surviving label prefix.
	content := truncateRunes(prefix+text, avail)
	if label != "" && strings.HasPrefix(content, label) {
		content = promptLabelStyle.Render(label) + strings.TrimPrefix(content, label)
	}
	return promptStyle.Width(width).Render(" " + content + " ")
}

// clipLine is the final guard applied to a fully-assembled line just before
// it's handed to a lipgloss Style.Width(w).Render() call. lipgloss WRAPS
// content wider than the styled width onto multiple terminal lines rather
// than clipping it, which breaks View's one-row-per-line invariant. clipLine
// returns line unchanged when it already fits in width display cells;
// otherwise it ANSI/display-width-aware truncates it down to width (ellipsis
// included), so the rendered cell width can never exceed width.
func clipLine(line string, width int) string {
	if width < 0 {
		width = 0
	}
	if lipgloss.Width(line) <= width {
		return line
	}
	return ansi.Truncate(line, width, "…")
}

// defaultBarW is the gauge bar width used everywhere except the dedicated
// meters line, which grows its bars to fill the pane (see renderMetersLine).
const defaultBarW = 10

// ctxSegment renders the "ctx <bar> <pct>%" gauge segment shared by
// renderStatusbar and renderMetersLine. barW is the bar's cell width.
func ctxSegment(m model, barW int) string {
	return fmt.Sprintf("ctx %s %d%%",
		renderBar(barW, m.contextPct, thresholdColor(m.contextPct)),
		int(m.contextPct+0.5))
}

// truncateRunes clips s to at most max runes, marking elision with an ellipsis.
func truncateRunes(s string, max int) string {
	if max < 1 {
		max = 1
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

// renderStatusbar packs state and budget info onto a single
// background-filled line at the bottom of the pane. chip, if non-empty, is
// the worktree name to show (truncated to 24 runes) between the model and
// ctx segments — callers pass "" once a prompt line is rendering it instead
// (see View).
func renderStatusbar(m model, now time.Time, chip string) string {
	dot := stateDot(m.state.Kind)
	durStr := "0:00"
	if !m.state.Since.IsZero() {
		durStr = formatDuration(now.Sub(m.state.Since))
	}

	leftParts := []string{fmt.Sprintf("%s %s %s", dot, m.state.Label(), durStr)}
	if m.modelName != "" {
		leftParts = append(leftParts, shortModel(m.modelName))
	}
	if chip != "" {
		leftParts = append(leftParts, "⎇ "+truncateRunes(chip, 24))
	}
	leftParts = append(leftParts, ctxSegment(m, defaultBarW))

	rightParts := rateGaugeParts(m, now, defaultBarW)

	left := strings.Join(leftParts, " · ")
	right := strings.Join(rightParts, " · ")
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)

	var line string
	switch {
	case right == "":
		line = " " + left + " "
	case leftW+rightW+4 <= m.width:
		// Plenty of room — right-align the right group with a stretched gap.
		pad := m.width - leftW - rightW - 2
		if pad < 1 {
			pad = 1
		}
		line = " " + left + strings.Repeat(" ", pad) + right + " "
	case leftW+rightW+5 <= m.width:
		// Tight but everything fits inline with a single ` · ` joiner.
		line = " " + left + " · " + right + " "
	default:
		// Too narrow even inline. Drop right-group items from the end
		// (eta → wk → 5h) until packing left + " · " + right fits.
		for len(rightParts) > 0 {
			right = strings.Join(rightParts, " · ")
			if leftW+lipgloss.Width(right)+5 <= m.width {
				break
			}
			rightParts = rightParts[:len(rightParts)-1]
		}
		if len(rightParts) == 0 {
			line = " " + left + " "
		} else {
			line = " " + left + " · " + right + " "
		}
	}
	return statusbarStyle.Width(m.width).Render(clipLine(line, m.width))
}

// stateDot returns the colored state indicator dot for kind, shared by
// renderStatusbar and renderStateLine.
func stateDot(kind StateKind) string {
	switch kind {
	case StateIdle:
		return dotIdle
	case StateThinking:
		return dotThinking
	case StateTool:
		return dotTool
	case StateAwaiting, StateError:
		return dotError
	case StateCompacting:
		return dotCompact
	default:
		return dotIdle
	}
}

// rateGaugeParts builds the right-group budget gauges — 5h, wk, and (when
// there's enough burn-rate signal) a "empty in X" ETA — in the fixed order
// callers drop from when space is tight: eta, then wk, then 5h. Returns nil
// when rate-limit data isn't available (m.rateOK == false). Shared by
// renderStatusbar and renderMetersLine so both panels build the identical
// gauge text from the same rules. barW is each gauge's bar cell width.
func rateGaugeParts(m model, now time.Time, barW int) []string {
	if !m.rateOK {
		return nil
	}
	fhPct := float64(m.rateLimits.FiveHour.UsedPercent)
	wkPct := float64(m.rateLimits.SevenDay.UsedPercent)
	parts := []string{
		fmt.Sprintf("5h %s %d%%→%s",
			renderBar(barW, fhPct, thresholdColor(fhPct)),
			m.rateLimits.FiveHour.UsedPercent,
			m.rateLimits.FiveHour.ResetsAt.Local().Format("3:04p")),
		fmt.Sprintf("wk %s %d%%→%s",
			renderBar(barW, wkPct, thresholdColor(wkPct)),
			m.rateLimits.SevenDay.UsedPercent,
			m.rateLimits.SevenDay.ResetsAt.Local().Format("Mon")),
	}
	rate := burnRatePctPerMin(m.pctSamples, now)
	if rate > 0 {
		eta := etaToEmptyPct(m.rateLimits.FiveHour.UsedPercent, rate)
		if eta > 0 && now.Add(eta).Before(m.rateLimits.FiveHour.ResetsAt) {
			parts = append(parts, "empty in "+formatDuration(eta))
		}
	}
	return parts
}

// renderStateLine renders the top line of the new split layout: the state
// dot, label, duration, model name, and (when the session runs in a
// worktree) the full worktree chip — never truncated to 24 runes the way
// the old single-line statusbar's chip was. Only if the assembled line
// still doesn't fit the pane width does the chip itself shrink (via
// truncateRunes); the state/model text never does.
func renderStateLine(m model, now time.Time) string {
	dot := stateDot(m.state.Kind)
	durStr := "0:00"
	if !m.state.Since.IsZero() {
		durStr = formatDuration(now.Sub(m.state.Since))
	}

	parts := []string{fmt.Sprintf("%s %s %s", dot, m.state.Label(), durStr)}
	if m.modelName != "" {
		parts = append(parts, shortModel(m.modelName))
	}
	left := strings.Join(parts, " · ")

	chip := m.worktreeChip()
	if chip == "" {
		return statusbarStyle.Width(m.width).Render(clipLine(" "+left+" ", m.width))
	}

	chipStr := "⎇ " + chip
	avail := m.width - 2 // columns inside the " "..." " padding below
	if avail < 1 {
		avail = 1
	}
	sep := " · "
	if lipgloss.Width(left)+lipgloss.Width(sep)+lipgloss.Width(chipStr) <= avail {
		return statusbarStyle.Width(m.width).Render(clipLine(" "+left+sep+chipStr+" ", m.width))
	}

	// Doesn't fit — shrink the chip only, never the state/model text. The
	// fits check above is in display cells (lipgloss.Width), so truncation
	// must be display-width-aware too (ansi.Truncate), not rune-count based
	// (truncateRunes) — otherwise wide runes (e.g. CJK branch names)
	// under-truncate and the line still overflows.
	chipAvail := avail - lipgloss.Width(left) - lipgloss.Width(sep)
	if chipAvail < 1 {
		chipAvail = 1
	}
	chipStr = ansi.Truncate(chipStr, chipAvail, "…")
	return statusbarStyle.Width(m.width).Render(clipLine(" "+left+sep+chipStr+" ", m.width))
}

// renderMetersLine renders the second line of the new split layout: the ctx
// bar followed by the same right-group budget gauges renderStatusbar packs
// on the right (5h, wk, eta), here joined left-to-right with " · " (no
// right-alignment needed since it has the full line to itself). When the
// line overflows the pane width, gauges drop from the end in today's order
// (eta → wk → 5h); the ctx gauge always stays. Unlike the packed statusbar,
// the surviving bars then widen past defaultBarW to consume the leftover
// columns, so the meters fill the pane rather than stranding it as padding.
func renderMetersLine(m model, now time.Time) string {
	avail := m.width - 2 // columns inside the " "..." " padding below
	if avail < 1 {
		avail = 1
	}
	build := func(barW int) []string {
		return append([]string{ctxSegment(m, barW)}, rateGaugeParts(m, now, barW)...)
	}

	// First decide how many gauges fit at the baseline bar width, dropping
	// from the end as before. Only the bars flex below, so gauge count is
	// settled here and never changes as they widen.
	parts := build(defaultBarW)
	for len(parts) > 1 && lipgloss.Width(strings.Join(parts, " · ")) > avail {
		parts = parts[:len(parts)-1]
	}

	// Spend the leftover columns widening every surviving bar equally: this
	// line has the pane to itself, so a 10-cell bar wastes most of it on a
	// wide pane. Only ctx/5h/wk carry bars — the trailing eta segment is
	// plain text — so the slack splits across those, not across every part,
	// or the eta's share would just become trailing blank space. Integer
	// division leaves a few columns unspent rather than risking an overflow
	// that clipLine would then chop.
	barCount := len(parts)
	if barCount > 3 {
		barCount = 3
	}
	if grow := (avail - lipgloss.Width(strings.Join(parts, " · "))) / barCount; grow > 0 {
		wide := build(defaultBarW + grow)[:len(parts)]
		if lipgloss.Width(strings.Join(wide, " · ")) <= avail {
			parts = wide
		}
	}
	return statusbarStyle.Width(m.width).Render(clipLine(" "+strings.Join(parts, " · ")+" ", m.width))
}

// renderBar draws a styled progress bar at pct (0-100) with given width and
// solid fill color. Uses bubbles/progress for consistent character rendering.
func renderBar(width int, pct float64, color string) string {
	p := progress.New(
		progress.WithSolidFill(color),
		progress.WithoutPercentage(),
		progress.WithWidth(width),
	)
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return p.ViewAs(pct / 100.0)
}

// thresholdColor returns the bar fill color for a usage percentage:
// green default, yellow >=70, red >=85.
func thresholdColor(pct float64) string {
	switch {
	case pct >= 85:
		return "#EF4444"
	case pct >= 70:
		return "#FFCC00"
	default:
		return "#04B575"
	}
}

// shortModel renders a model id as "<family> <major>.<minor>" with an
// optional " 1M" suffix for the [1m] context variant. Examples:
//
//	claude-opus-4-7        → "opus 4.7"
//	claude-opus-4-7[1m]    → "opus 4.7 1M"
//	claude-haiku-4-5-20251 → "haiku 4.5"
//	(empty)                → "—"
func shortModel(m string) string {
	if m == "" {
		return "—"
	}
	suffix := ""
	if strings.HasSuffix(m, "[1m]") {
		suffix = " 1M"
		m = strings.TrimSuffix(m, "[1m]")
	}
	m = strings.TrimPrefix(m, "claude-")
	parts := strings.Split(m, "-")
	if len(parts) >= 3 && allDigits(parts[1]) && allDigits(parts[2]) {
		return parts[0] + " " + parts[1] + "." + parts[2] + suffix
	}
	return m + suffix
}

// worktreeName extracts the Claude worktree name from a transcript path.
// Worktree sessions write transcripts under an encoded project dir that
// embeds the worktree location, e.g.
// ".../projects/-Users-x-repo--claude-worktrees-<name>/<session>.jsonl".
// Returns "" for non-worktree sessions.
func worktreeName(jsonlPath string) string {
	const marker = "--claude-worktrees-"
	dir := filepath.Base(filepath.Dir(jsonlPath))
	i := strings.LastIndex(dir, marker)
	if i < 0 {
		return ""
	}
	return dir[i+len(marker):]
}

// worktreeNameFromCwd extracts the Claude worktree name from a live cwd path,
// e.g. "/Users/x/repo/.claude/worktrees/feature-branch/sub/dir" -> "feature-branch". Only
// the single path component immediately after the marker is used, so a
// deeper cwd within the worktree still resolves to the worktree's own name.
// Returns "" when cwd doesn't contain the marker, or when nothing follows it.
func worktreeNameFromCwd(cwd string) string {
	const marker = "/.claude/worktrees/"
	i := strings.Index(cwd, marker)
	if i < 0 {
		return ""
	}
	rest := cwd[i+len(marker):]
	if rest == "" {
		return ""
	}
	if j := strings.Index(rest, "/"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// worktreeChip selects the worktree chip's source of truth, in priority order:
//
//  1. sessionCwd genuinely inside a worktree — the session was launched there
//     or entered via native EnterWorktree. worktreeNameForCwd sees both
//     Claude's ".claude/worktrees" layout and plain `git worktree add`
//     siblings. This tracks the session in and out turn by turn, so a base-repo
//     cwd correctly yields no chip here even while the transcript still lives
//     under a worktree-encoded project dir from an earlier turn.
//  2. cmdWorktree — the session's cwd is the main repo but its recent commands
//     drive a linked worktree by explicit path (the common flow: an agent works
//     a worktree at arm's length, never chdir'ing into it).
//  3. The transcript-path fallback (native encoding only), used solely before
//     the first event is seeded, when no cwd is known yet.
func (m model) worktreeChip() string {
	if m.sessionCwd != "" {
		if name := worktreeNameForCwd(m.sessionCwd); name != "" {
			return name
		}
		return m.cmdWorktree
	}
	return worktreeName(m.jsonlPath)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("0:%02d", int(d.Seconds()))
	}
	if d < time.Hour {
		mins := int(d.Minutes())
		secs := int(d.Seconds()) - mins*60
		return fmt.Sprintf("%d:%02d", mins, secs)
	}
	h := int(d.Hours())
	mins := int(d.Minutes()) - h*60
	return fmt.Sprintf("%dh%dm", h, mins)
}
