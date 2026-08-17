package main

import (
	"context"
	"errors"
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

	// None of the pane's styles set a Background: the head inherits the
	// terminal's own, so it reads correctly under both light and dark themes
	// without having to detect which is in use. A hardcoded background also
	// can't survive this layout — the state dot, every progress bar, and the
	// bold label are each rendered as their own styled fragment ending in a
	// background reset, so an outer Background(...) only paints the gaps
	// between them and the trailing width pad. That banding was invisible
	// against a dark terminal and glaring against a light one.
	//
	// Foregrounds follow suit: the status line takes the terminal's default
	// foreground (the most prominent thing in the pane, legible either way),
	// and the prompt rows use a mid-gray that clears ~4:1 contrast against
	// both white and near-black. The label keeps its emphasis through weight
	// rather than a brighter gray, since "brighter" flips meaning with theme.
	statusbarStyle = lipgloss.NewStyle()

	promptStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#808080"))

	promptLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#808080")).
				Bold(true)
)

type tickMsg time.Time

// summaryCallTimeout is the OUTER bound on a whole summarize() command — the
// SDK's per-request summaryRequestTimeout plus its one retry, plus request
// setup — not a second copy of the per-request timeout.
const summaryCallTimeout = 30 * time.Second

// summaryRetryFloor paces self-initiated retry calls. It is deliberately
// separate from summary.min_interval: that floor is user-configurable and 0
// is documented as "edges only guarded by the in-flight flag", which is safe
// for edge-driven calls (edges are turn-bounded) but would make retries fire
// every poll — a billable call per second against an API that just failed.
const summaryRetryFloor = 30 * time.Second

// summarizerAcquireFloor / summarizerAcquireMax pace and cap re-attempts at
// constructing the summarizer after a keyless startup. Each attempt against a
// writerless FIFO parks up to 2 goroutines for the life of the process (see
// readEnvFileValue), so attempts cannot be unbounded: 120 attempts a minute
// apart covers a two-hour lock at a worst case of ~240 parked goroutines,
// after which the feature stays off until the head restarts.
const (
	summarizerAcquireFloor = time.Minute
	summarizerAcquireMax   = 120
)

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

// summarizerMsg is the completion of one lazy acquisition attempt. s is nil
// when the key still could not be read.
type summarizerMsg struct {
	s  *Summarizer
	at time.Time
}

type model struct {
	// Config
	jsonlPath string
	// sessionID is "" in waiting mode: the project had no transcript at
	// startup (see waitingTranscript) and the head is waiting for the first
	// one to appear. switchSession sets it when rotation adopts a real file.
	sessionID string

	// waitingSince anchors StateWaiting's duration and publish timestamp — a
	// fixed instant, so maybePublishState's anchored-Since guard publishes
	// the waiting state once, not every tick. Zero outside waiting mode.
	waitingSince time.Time

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

	// askDir is where hooks/claudemux-ask.sh drops pending-AskUserQuestion
	// markers. Tests point it at a temp dir; "" disables the override.
	askDir string

	// publishedState is the last @claudemux_state value pushed to tmux, so the
	// poll loop republishes only on change — one subprocess per transition,
	// not per tick.
	publishedState string

	// publishedSince is the Since of the last publish. Paired with
	// publishedState in maybePublishState's guard so a value-identical
	// transition with a new anchored Since (Idle -> busy blip -> Idle)
	// still republishes @claudemux_state_since.
	publishedSince time.Time

	// publishedContext/-Summary/-Prompt/-Model are the last-published info
	// option values (context as integer percent; -1 = never published, since 0
	// is a legal percent). Same publish-on-change contract as publishedState.
	publishedContext int
	publishedSummary string
	publishedPrompt  string
	publishedModel   string

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

	// sessionBranch is the branch the *main* session last recorded. Derived
	// the same way as sessionCwd and from the same transcript entries — see
	// lastGitBranch.
	sessionBranch string

	// cmdWorktree is the worktree the session is driving *at arm's length* —
	// its cwd stays in the main repo while its commands reach into a linked
	// worktree by explicit path (git -C <wt>, cd <wt> && …, container names).
	// Recomputed each poll from the recent command window; "" when no single
	// worktree dominates. Only consulted when sessionCwd itself isn't a
	// worktree (see worktreeChip).
	cmdWorktree string

	// worktreeTab is the tab label derived from the worktree this session
	// created for itself — its name with dashes turned back into spaces. Set
	// only when the head OBSERVES the session move from outside a worktree into
	// one, which is the signature of hooks/claudemux-worktree.sh having worked
	// and therefore of a task-derived name. A session already in a worktree at
	// startup is left alone: its name predates this feature (or was made by
	// hand), and rendering "lovely wandering lovelace" as the tab would be
	// strictly worse than the Haiku label it would replace.
	//
	// sawNonWorktreeCwd records that the session was once observed OUTSIDE a
	// worktree, which is the first half of that transition.
	worktreeTab       string
	sawNonWorktreeCwd bool

	// tabHaikuWins latches on the first time the summarizer REPLACES an
	// established topic, handing the tab to Haiku's label permanently. It
	// reuses the summary prompt's own stability rule ("KEEP IT VERBATIM unless
	// the human has genuinely changed goals") as the "materially different"
	// test, rather than inventing a string-similarity metric. A first summary
	// establishing a topic is not a change.
	tabHaikuWins bool
	// lastTopic is the topic tabHaikuWins compares against.
	lastTopic string

	// Persistent state
	reader    *EventReader
	allEvents []Event // bounded ring (cap 1000)
	// bg holds background tasks this session launched and has not seen finish.
	// Accumulated from new events as they arrive — see bgTracker for why it
	// cannot be recomputed from allEvents.
	bg             bgTracker
	rateLimitsPath string      // ~/.claude/abtop-rate-limits.json or override
	pctSamples     []pctSample // 5h-window snapshots over time, for burn-rate

	// Latest snapshot
	state       State
	modelName   string
	contextPct  float64
	firstPrompt string
	lastPrompt  string
	// lastTyped is the newest prompt from a real `user` turn — see
	// lastTypedPrompt. Only the teardown watch reads it; everything
	// user-facing uses lastPrompt.
	lastTyped  string
	summary    Summary
	rateLimits RateLimits
	rateOK     bool
	// conductRaw is the last-read @claudemux_conducting value, parsed at
	// render time (not poll time) so its heartbeat keeps decaying against the
	// clock even if polls stall — see conductChip.
	conductRaw string
	// conductPendingMode holds the mode this head's space key just asked the
	// lobby for, shown in place of conductRaw until a poll confirms it or
	// conductPendingUntil passes. Without it the chip would answer a keypress
	// only after this head's poll AND the lobby's — up to two beats of looking
	// broken — and would flicker back in between, since a poll already in
	// flight when space was pressed still carries the pre-toggle value.
	conductPendingMode  string
	conductPendingUntil time.Time

	// UI
	lastUpdate time.Time
	width      int
	height     int
	ready      bool
	polling    bool
	summarizer *Summarizer
	// summaryCfg is kept so a keyless startup can retry summarizer
	// construction later (see shouldAcquireSummarizer): the common cause is a
	// locked 1Password FIFO at launch, which unlocks minutes later.
	summaryCfg         SummaryConfig
	acquiringKey       bool
	keyAttempts        int
	lastKeyAttemptAt   time.Time
	minSummaryInterval time.Duration
	tabTitle           bool
	// tabPinned holds the window name and the session's project colors at
	// their launch-time values, suppressing the summary-driven rename until
	// it is toggled off. Deliberately not persisted: a head restart already
	// re-renders the session's identity from scratch, and a pin that outlived
	// the process would be a setting rather than a gesture.
	tabPinned bool
	// restart records that the user asked for a re-exec rather than a quit.
	// The TUI exits either way — main reads this once p.Run() has returned and
	// restored the terminal, then replaces the process. See restartSelf.
	restart bool
	// launchBin is the on-disk binary this process started as; each tick
	// compares it against the file so a rebuilt head re-execs itself — the
	// R key's job, without waiting for a human to remember. launchBinOK
	// false (stamping failed) disables the feature for this process.
	launchBin   binStamp
	launchBinOK bool
	// Teardown state: `x` wraps the session up and kills its tmux session.
	// See teardown.go. Like tabPinned, this is deliberately not persisted —
	// an armed kill that survived a head restart would be a trap.
	teardown teardownPhase
	// teardownCmdText is teardown.command from config, typed into the claude
	// pane on the first press. Empty means skip that step entirely.
	teardownCmdText string
	// teardownAt stamps the current phase, so the submit and exit deadlines
	// measure from the transition rather than from process start.
	teardownAt time.Time
	// teardownPrompt is lastTyped as it stood when the wrap-up was sent. A
	// change in lastTyped is one of the two signals that the keystrokes
	// actually reached claude (the other is the session going busy).
	//
	// lastTyped rather than lastPrompt because the wrap-up is a bare slash
	// command, and those are shadowed out of lastPrompt within the same poll
	// batch — see lastTypedPrompt. Against lastPrompt this signal could never
	// fire for a `/done`, leaving the busy edge to carry the whole burden.
	teardownPrompt    string
	teardownSubmitted bool
	// teardownArmedBusy records that claude was already mid-turn when the
	// teardown was armed. Without it, "claude is busy" would certify a
	// submission that never happened — the busy state predates the keystrokes.
	// Cleared as soon as the turn ends, so a LATER busy edge still counts.
	teardownArmedBusy bool
	// teardownBlocked records that the turn ended with the worktree still
	// standing — a wrap-up that bailed. It drives the status chip; the gate
	// simply stays shut.
	teardownBlocked bool
	// teardownBlockReason is why an auto-armed non-worktree gate is shut
	// ("dirty tree", "unpushed", ...). Rendered beside the blocked chip;
	// empty for worktree blocks, whose reason is always the same (the
	// worktree still exists).
	teardownBlockReason string
	teardownProbing     bool
	// teardownProbeAt stamps the last ready-gate probe that was issued, so a
	// blocked teardown can back off to teardownBlockedProbeInterval instead of
	// forking git every second for as long as it sits on screen.
	teardownProbeAt time.Time
	// teardownAuto records that this teardown was armed by the head itself,
	// off a wrap-up command the user typed into the claude pane, rather than
	// by an `x` press. It only drives the status chip — every transition out
	// of teardownSent is identical for both paths — but the chip must say so:
	// `x` becoming live is otherwise unannounced for a user who never pressed
	// anything.
	teardownAuto bool
	// teardownWorkDir / teardownInWorktree are the ready gate's target,
	// captured from the SESSION's cwd when `x` first arms a teardown — not
	// from workDir below. The head process is started by bin/claudemux with
	// `-c "$work_dir"` (the main checkout) while `claude --worktree` chdirs
	// itself into .claude/worktrees/<name>, so for every worktree session the
	// head's own cwd is the wrong directory to watch: it is never deleted, and
	// worktreeNameForCwd on it is "". sessionCwd is the directory the wrap-up
	// will actually remove (it is what drives the worktree chip, for the same
	// reason). Captured at ARM time rather than per probe because by the time
	// the gate is being evaluated the wrap-up may already have deleted the
	// directory — and once claude exits, sessionCwd stops being refreshed.
	teardownWorkDir    string
	teardownInWorktree bool
	// teardownNote is the reason the last teardown aborted, shown for
	// teardownNoteTTL and then dropped.
	teardownNote   string
	teardownNoteAt time.Time

	// workDir is the head process's OWN launch directory with symlinks
	// resolved — which is NOT necessarily where the session is working (see
	// teardownWorkDir above). It is captured at startup because the wrap-up
	// command may delete it out from under this process: once it is gone
	// os.Getwd() fails, so it cannot be re-derived at the moment it is needed.
	// It serves as the fallback gate target when no sessionCwd has been
	// observed yet, and as the directory `r` resets the tab from.
	//
	// mainCheckout is captured at startup for the same reason — there is no
	// valid cwd left to run git from. It needs no session-cwd equivalent: a
	// linked worktree belongs to the same repo as the checkout the head was
	// launched in, so this is still the right place to run `git worktree list`.
	//
	// inWorktree describes workDir, and is likewise only the gate's fallback.
	workDir      string
	mainCheckout string
	inWorktree   bool

	// worktreePending records that bin/claudemux marked this session as wanting
	// a worktree (CLAUDEMUX_WORKTREE_PENDING). The launcher no longer creates
	// one; hooks/claudemux-worktree.sh asks the model to. When the model skips
	// that call the session works on the default branch in the SHARED checkout,
	// which is exactly what the marking existed to prevent — so a marked
	// session whose first turn ended outside a worktree says so in the chip.
	worktreePending bool

	summarizing   bool
	lastSummaryAt time.Time
	// summaryGen identifies the session a summarize call was issued for. It is
	// bumped on every switchSession, so a reply that lands after a rotation can
	// be recognized as belonging to the previous session and dropped.
	summaryGen int
	// summaryRetry records that a summarize call failed retryably while the
	// pane had no summary at all — the fallback display, with no edge
	// guaranteed to ever fire again (an idle session has none). The dataMsg
	// handler turns it into a fresh call once summaryRetryFloor elapses.
	summaryRetry bool
	err          error
}

func newModel(cfg Config, jsonlPath, sessionID string, followActive bool) model {
	r := newEventReader(jsonlPath)
	r.SeedFromEnd(500)
	seeded, _ := r.Seeded()

	summarizer := newSummarizer(cfg.Summary)

	m := model{
		jsonlPath:    jsonlPath,
		sessionID:    sessionID,
		followActive: followActive,
		selfPane:     os.Getenv("TMUX_PANE"),
		paneDir:      paneMapDir(),
		askDir:       askMarkerDir(),
		// -1: 0 is a legal context percent, so it can't double as "unset".
		publishedContext: -1,
		reader:           r,
		allEvents:        seeded,
		bg:               newBgTracker(),
		rateLimitsPath:   defaultRateLimitsPath(),
		firstPrompt:      r.FirstPrompt(),
		// Init always issues the first poll itself (see Init below), so the
		// flag starts held to prevent the first 1s tick from firing a second,
		// concurrent poll that races on EventReader.offset.
		polling:            true,
		summarizer:         summarizer,
		summaryCfg:         cfg.Summary,
		minSummaryInterval: cfg.Summary.MinInterval.Duration,
		tabTitle:           cfg.Summary.TabTitle,
		teardownCmdText:    cfg.Teardown.Command,
		// Init fires the seed summarize call when summarizer != nil and a
		// session is actually bound (see Init below); this flag must already
		// be held at that point, for the same reason polling starts true
		// above — Init has a value receiver and cannot set it itself, so a
		// fast busy→idle edge on the very first poll would otherwise race a
		// second concurrent call against the seed call. In waiting mode
		// (sessionID "") Init does NOT fire — there is nothing to summarize —
		// so the flag must not be held either, or it would never clear and
		// block every future call.
		summarizing: summarizer != nil && sessionID != "",
	}
	m.launchBin, m.launchBinOK = launchBinStamp()
	if summarizer == nil {
		// Startup itself was an acquisition attempt; stamp it so the lazy
		// loop's first re-attempt waits a full floor rather than one tick.
		m.lastKeyAttemptAt = time.Now()
	}
	if sessionID == "" {
		m.waitingSince = time.Now()
	}
	// Captured while the directory still exists — see the workDir field.
	if wd, err := os.Getwd(); err == nil {
		if resolved, err := filepath.EvalSymlinks(wd); err == nil {
			wd = resolved
		}
		m.workDir = wd
		m.inWorktree = worktreeNameForCwd(wd) != ""
		m.mainCheckout = mainCheckoutFor(wd)
	}
	m.worktreePending = os.Getenv("CLAUDEMUX_WORKTREE_PENDING") != ""
	// The tracker starts from what the transcript already shows: heads
	// restart and rotate while background work is out, and an unseeded
	// tracker would call such a session Idle — the conductor then escorts
	// the user into a session that is busy. Replaying the seed is sound:
	// completions always postdate their launches, so any launch inside the
	// end-anchored seed window has its completion inside it too (or still
	// pending), and expiry drops anything genuinely stale.
	m.bg.subagentsDir = subagentsDirFor(jsonlPath)
	m.bg.observe(seeded, time.Now())
	m.recomputeFromEvents(time.Now())
	return m
}

func (m *model) recomputeFromEvents(now time.Time) {
	bgCount, bgOldest := m.bg.outstanding(now)
	m.state = classifyState(m.allEvents, bgCount, bgOldest, now)
	m.state = askOverride(m.state, m.allEvents, askMarkerTime(m.askDir, m.sessionID))
	if !m.waitingSince.IsZero() {
		// Waiting mode: no transcript exists yet, so classifyState's empty-ring
		// "Idle" is a lie — nothing is waiting on the human, Claude is booting.
		// Keyed on the anchor, not sessionID == "": the anchor is set only by
		// newModel's explicit waiting entry and cleared by switchSession, so a
		// model built some other way without an ID doesn't read as waiting.
		m.state = State{Kind: StateWaiting, Since: m.waitingSince, Anchored: true}
	}
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
	m.lastTyped = lastTypedPrompt(m.allEvents)
	if m.reader != nil {
		m.firstPrompt = m.reader.FirstPrompt()
	}
	m.sessionCwd = lastMainCwd(m.allEvents, m.sessionCwd)
	m.sessionBranch = lastGitBranch(m.allEvents, m.sessionBranch)
	m.cmdWorktree = commandWorktree(m.sessionCwd, m.allEvents, now)
	m.observeWorktreeTransition()
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

// lastGitBranch returns the branch of the most recent main-session
// (non-sidechain) event that carries one, scanning the ring newest-first. It
// mirrors lastMainCwd exactly, for the same two reasons: a subagent working in
// another worktree must not hijack the chip, and a poll that brings no usable
// event must keep the branch already known rather than blanking it.
//
// A detached HEAD is recorded by Claude Code as the literal string "HEAD".
// Reporting that verbatim would read as a branch named HEAD, so it is mapped
// here — the honest answer to "what branch is this session on" is "detached".
func lastGitBranch(events []Event, prev string) string {
	for i := len(events) - 1; i >= 0; i-- {
		if e := events[i]; e.GitBranch != "" && !e.IsSidechain {
			if e.GitBranch == "HEAD" {
				return "detached"
			}
			return e.GitBranch
		}
	}
	return prev
}

// transcriptSessionID is the session id a transcript path names — Claude Code
// writes every transcript as <session-id>.jsonl, so it is the base name minus
// the extension.
func transcriptSessionID(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

// moveSession re-binds the reader when the SAME session's transcript shows up
// at a new path. Claude Code homes a transcript under the project slug of the
// session's cwd, so EnterWorktree and ExitWorktree both move the file
// mid-session — same session id, different directory.
//
// Everything session-scoped survives, because it is the same conversation:
// the summary, the context gauge, and above all an armed teardown watch.
// Routing a move through switchSession instead killed the watch at exactly
// the wrong moment — a /done wrap-up's own ExitWorktree step moved the
// transcript, the false "rotation" aborted the watch, and the worktree
// removal seconds later went unobserved, so the gate never opened.
//
// The ring is reseeded from the new path rather than kept: the old reader may
// have missed events written between its last successful tail and the move,
// and the file at the new path carries them all.
func (m *model) moveSession(jsonlPath string, now time.Time) {
	teardownLogf("move phase=%v from=%s to=%s", m.teardown, m.jsonlPath, jsonlPath)
	r := newEventReader(jsonlPath)
	r.SeedFromEnd(500)
	seeded, _ := r.Seeded()
	m.jsonlPath = jsonlPath
	m.reader = r
	m.allEvents = seeded
	m.firstPrompt = r.FirstPrompt()
	m.recomputeFromEvents(now)
}

// switchSession re-binds the monitor to a different session .jsonl: it opens a
// fresh reader, re-seeds from the end, and recomputes all derived state.
// Called when the active session rotates and the monitor is in follow-active
// mode. It returns the summarize command to run for the new session (nil when
// the guards say no); the caller MUST return that command, or the rotated pane
// sits on the raw-prompt fallback until the next turn boundary.
func (m *model) switchSession(jsonlPath string, now time.Time) tea.Cmd {
	// An armed teardown does not survive a rotation. It was armed against the
	// session that just went away: its wrap-up went to that session's pane, and
	// teardownPrompt refers to that session's transcript. Recomputing below
	// gives lastPrompt the NEW session's value, which the next tick would read
	// as "the prompt changed, so the wrap-up must have submitted" — silently
	// certifying a submission that never happened and removing the only bound
	// on how long teardownSent can sit there. Aborting says why instead.
	teardownLogf("switchSession phase=%v from=%s to=%s", m.teardown, m.jsonlPath, jsonlPath)
	if m.teardown != teardownIdle {
		*m = m.abortTeardown("session rotated", now)
	}

	sessionID := transcriptSessionID(jsonlPath)
	r := newEventReader(jsonlPath)
	r.SeedFromEnd(500)
	seeded, _ := r.Seeded()

	m.jsonlPath = jsonlPath
	m.sessionID = sessionID
	// Adopting a real session ends waiting mode (recomputeFromEvents keys on
	// sessionID, but a stale anchor must not linger into a later rebind).
	m.waitingSince = time.Time{}
	m.reader = r
	m.allEvents = seeded
	m.firstPrompt = r.FirstPrompt()
	// Clean slate: the new session's own cwd must come from its own events, not
	// linger from the session we just left. Its seed always carries one.
	m.sessionCwd = ""
	m.sessionBranch = ""
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
	m.bg = newBgTracker()
	// Same seeding rationale as newModel: the rotated-to session may have
	// work out that only its transcript knows about.
	m.bg.subagentsDir = subagentsDirFor(jsonlPath)
	m.bg.observe(seeded, now)
	m.worktreeTab = ""
	m.sawNonWorktreeCwd = false
	m.tabHaikuWins = false
	m.lastTopic = ""
	m.summaryRetry = false
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

// lastTypedPrompt returns the text of the most recent thing the user actually
// typed, considering only real `user` turns.
//
// This is deliberately narrower than lastUserPrompt, which also accepts Claude
// Code's `last-prompt` bookkeeping events. Those are written for the status
// line's benefit and never carry a bare slash command: invoking `/done` emits
// a `user` turn holding the command, immediately followed by a `last-prompt`
// event repeating the PREVIOUS typed prompt. Both arrive in the same poll
// batch, so a newest-first scan that accepts last-prompt events reports the
// older text and the command is never observable at all — the bug that made
// autoArmTeardown dead code. Restricting the scan to `user` turns makes the
// command visible; the display path keeps the broader scan, which is right for
// it (a bare command says nothing about what the session is doing).
func lastTypedPrompt(events []Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if e := events[i]; e.Type == "user" && genuinePrompt(e) {
			return e.UserText
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
	// The project color is static for the life of a session, so it is published
	// once here rather than from the per-tick publish path.
	if c := publishColorCmd(m.selfPane, m.workDir); c != nil {
		cmds = append(cmds, c)
	}
	// No seed call in waiting mode: the transcript is empty, and
	// switchSession fires the seed when a real session is adopted. Must
	// stay in lockstep with newModel's `summarizing` initializer above.
	if m.summarizer != nil && m.waitingSince.IsZero() {
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
			if mapped, cwd, pane, ok := mappedTranscript(selfPane, paneDir); ok {
				// mapped is "" when the pane's live cwd is known but its
				// transcript isn't yet — keep the current binding then rather
				// than adopting an empty path.
				if mapped != "" && mapped != jsonlPath {
					teardownLogf("rotate via=mapped pane=%s cwd=%s from=%s to=%s",
						pane, cwd, jsonlPath, mapped)
					activeJSONL = mapped
				}
			} else if mra, ok := mostRecentlyActiveSession(filepath.Dir(jsonlPath)); ok && mra != jsonlPath {
				// mappedTranscript said "no claude pane at all" — a wedged or
				// slow tmux (listPanes has a 2s deadline) looks identical to a
				// genuinely absent pane here, and this fallback then adopts
				// whichever transcript in the dir was touched last.
				teardownLogf("rotate via=mru-fallback from=%s to=%s", jsonlPath, mra)
				activeJSONL = mra
			}
		}
		newEvents, _ := reader.Tail()
		rl, rlErr := readRateLimits(rlPath)
		// The lobby's conduct-mode heartbeat, read on the same beat as
		// everything else. Only inside tmux (selfPane set); a failed or absent
		// read is "" and the chip simply stays off.
		conductRaw := ""
		if selfPane != "" {
			cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Second)
			conductRaw = readConductOption(cctx)
			ccancel()
		}
		return dataMsg{
			time:         time.Now(),
			activeJSONL:  activeJSONL,
			newEvents:    newEvents,
			rateLimits:   rl,
			rateLimitErr: rlErr,
			conductRaw:   conductRaw,
		}
	}
}

type dataMsg struct {
	time         time.Time
	activeJSONL  string // non-empty when a newer session file should be adopted
	newEvents    []Event
	rateLimits   RateLimits
	rateLimitErr error
	conductRaw   string // raw @claudemux_conducting value, "" when unset/unreadable
}

// turnEndedByIdle reports whether kind reads as "the main thread's turn
// ended" for summarize purposes: genuinely Idle, or Idle except that work it
// launched is still running (Background). The two look the same to the
// human — there's nothing new to summarize either way — so folding them into
// one side of shouldSummarize's edge check means Idle<->Background never
// fires a spurious extra call by itself, while Thinking/Tool ending the turn
// as either Idle OR Background still does.
func turnEndedByIdle(kind StateKind) bool {
	return kind == StateIdle || kind == StateBackground
}

// shouldSummarize reports whether this poll crossed the busy → ended edge,
// which is usually a finished turn but not always: classifyState calls any
// assistant event carrying text Idle (or Background, if work is still
// outstanding), so an assistant that emits prose and then a tool call in
// separate JSONL events shows a mid-turn ended blip that also fires. That's
// acceptable — the in-flight flag serializes calls, and the rate floor
// (summary.min_interval) bounds how often they may fire, so short back-to-back
// edges can't hammer the API. Note the floor is user-configurable and may be
// set to 0, which disables it: the in-flight flag is then the only guard, and
// it serializes calls without bounding them.
func (m model) shouldSummarize(prevKind StateKind, now time.Time) bool {
	if !m.canSummarize(now) {
		return false
	}
	return !turnEndedByIdle(prevKind) && turnEndedByIdle(m.state.Kind)
}

// shouldRetrySummarize reports whether this poll should re-issue a summarize
// call that previously failed. Only fires while the pane still has no summary
// (an error landing over a good summary keeps the good one; the next edge
// refreshes it) and past both canSummarize's guards and the dedicated retry
// floor above.
func (m model) shouldRetrySummarize(now time.Time) bool {
	if !m.summaryRetry || m.summary != (Summary{}) {
		return false
	}
	if !m.canSummarize(now) {
		return false
	}
	return now.Sub(m.lastSummaryAt) >= summaryRetryFloor
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

// tabLabel picks the window label: the name the session gave its own worktree
// until Haiku has earned the tab, then Haiku's. Either side falls back to the
// other when empty, so a missing summary or a session that never made a
// worktree still gets whatever label exists.
func tabLabel(worktreeTab, haikuTab string, haikuWins bool) string {
	if haikuWins && haikuTab != "" {
		return haikuTab
	}
	if worktreeTab != "" {
		return worktreeTab
	}
	return haikuTab
}

// observeWorktreeTransition watches sessionCwd for the session entering a
// worktree, and adopts that worktree's name as the tab when it does. See the
// worktreeTab field for why only an observed transition counts.
func (m *model) observeWorktreeTransition() {
	name := worktreeNameForCwd(m.sessionCwd)
	if name == "" {
		if m.sessionCwd != "" {
			m.sawNonWorktreeCwd = true
		}
		return
	}
	if m.sawNonWorktreeCwd && m.worktreeTab == "" {
		m.worktreeTab = strings.ReplaceAll(name, "-", " ")
	}
}

// noteTopic records a landed summary's topic and latches tabHaikuWins when it
// REPLACES an established one.
func (m *model) noteTopic(topic string) {
	if topic == "" {
		return
	}
	if m.lastTopic != "" && topic != m.lastTopic {
		m.tabHaikuWins = true
	}
	m.lastTopic = topic
}

// tabCmdFor returns the window-rename command for a freshly landed summary, or
// nil when the tab title is disabled, the tab is pinned, we are not in tmux, or
// the label is empty.
func (m model) tabCmdFor(s Summary) tea.Cmd {
	if !m.tabTitle || m.tabPinned {
		return nil
	}
	return renameTabCmd(m.selfPane, tabLabel(m.worktreeTab, s.Tab, m.tabHaikuWins))
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

// shouldAcquireSummarizer reports whether this tick should re-attempt
// summarizer construction: the feature is enabled but keyless (startup or a
// prior attempt found nothing), no attempt is in flight, and both the floor
// and the lifetime cap allow another one.
func (m model) shouldAcquireSummarizer(now time.Time) bool {
	if m.summarizer != nil || m.acquiringKey || !m.summaryCfg.Enabled {
		return false
	}
	if m.keyAttempts >= summarizerAcquireMax {
		return false
	}
	return now.Sub(m.lastKeyAttemptAt) >= summarizerAcquireFloor
}

// shouldAutoRestart reports whether this tick may re-exec into a rebuilt
// binary. Only from a quiet head: a teardown in flight must not be disarmed
// by a restart it didn't ask for (teardown state is deliberately not
// persisted — see the teardown fields).
func (m *model) shouldAutoRestart(now time.Time) bool {
	return m.teardown == teardownIdle && m.launchBinOK && binChanged(m.launchBin, now)
}

// acquireSummarizer re-runs newSummarizer off the Update loop: reading the
// key can block up to ~2s on a FIFO (envFileTimeout), which must never stall
// rendering.
func (m model) acquireSummarizer() tea.Cmd {
	cfg := m.summaryCfg
	return func() tea.Msg {
		return summarizerMsg{s: newSummarizer(cfg), at: time.Now()}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc":
			// esc cancels an armed teardown rather than quitting: a key that
			// arms a kill-session needs a way out, and adding a second cancel
			// key to a four-key TUI would be worse. An empty note is a no-op
			// for teardownChip's TTL branch, so this doesn't leave a stray
			// abort reason on the status line.
			if m.teardown != teardownIdle {
				return m.abortTeardown("", time.Now()), nil
			}
			return m, tea.Quit
		case "R":
			// Restart in place: quit, and let main re-exec the binary from
			// disk. Without it the only way to pick up a rebuilt head is to
			// quit — which closes the pane, since remain-on-exit is `failed`
			// and a clean quit is not a failure — and relaunch the session.
			//
			// Deliberately allowed from any teardown phase. Teardown state is
			// not persisted across a restart by design (see the teardown
			// fields), so this disarms rather than carries anything over —
			// the safe direction for a key sequence that ends in kill-session.
			m.restart = true
			return m, tea.Quit
		case "s":
			// Force a Haiku refresh. Deliberately skips the rate floor that
			// canSummarize applies: the floor exists to bound automatic,
			// edge-driven calls, and a key the human pressed is neither — a
			// refresh that silently did nothing because an edge fired twenty
			// seconds ago would be indistinguishable from a broken key.
			//
			// The other two guards still hold. No summarizer means no API key
			// and nothing to call; the in-flight flag means a mashed key
			// cannot put two billed calls in the air at once.
			if m.summarizer == nil || m.summarizing {
				return m, nil
			}
			m.summarizing = true
			return m, m.summarize()
		case " ":
			// Toggle fleet conduct mode from here — the same thing space does
			// in the lobby, so the key means one thing wherever you press it.
			// The head owns none of that state: it publishes a request and the
			// lobby applies it (see conductRequestOption).
			//
			// Only while a live lobby is publishing. With no conductor running
			// there is nothing to toggle, and a request nobody will read would
			// leave the chip flashing a mode that never took effect.
			now := time.Now()
			if _, ok := parseConductValue(m.conductRaw, now); !ok {
				return m, nil
			}
			// The direction is read off what is on SCREEN (pending flip
			// included, via conductRawFor), not off the last poll: mashing
			// space twice must come back to where it started rather than
			// re-sending the same direction twice. Always parseable here —
			// conductRawFor returns either the always-fresh pending forgery or
			// the conductRaw the liveness gate above just accepted.
			mode, _ := parseConductValue(m.conductRawFor(now), now)
			if conductOn(mode) {
				m.conductPendingMode = "standby"
			} else {
				m.conductPendingMode = "conducting"
			}
			m.conductPendingUntil = now.Add(conductPendingWindow)
			return m, requestConductToggleCmd(now)
		case "x":
			return m.teardownKey()
		case "r":
			m.tabPinned = !m.tabPinned
			if m.tabPinned {
				// The startup capture, not os.Getwd(): a wrap-up that removed
				// the launch directory makes os.Getwd() fail, and `r` would
				// then pin without ever resetting. workDir is captured for
				// exactly that failure — see the field.
				if m.workDir == "" {
					// Nothing to resolve a color or a name from. The pin still
					// takes effect: it is a statement about this program's own
					// future behavior and must not depend on the filesystem.
					return m, nil
				}
				return m, resetTabCmd(m.selfPane, m.workDir)
			}
			// Unpinning hands control back immediately rather than leaving the
			// tab stale until the next summary lands.
			return m, m.tabCmdFor(m.summary)
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true

	case tickMsg:
		cmds := []tea.Cmd{m.tick()}
		if m.shouldAcquireSummarizer(time.Time(msg)) {
			m.acquiringKey = true
			m.keyAttempts++
			m.lastKeyAttemptAt = time.Time(msg)
			cmds = append(cmds, m.acquireSummarizer())
		}
		if !m.polling {
			m.polling = true
			cmds = append(cmds, m.pollData())
		}

		now := time.Time(msg)
		if m.shouldAutoRestart(now) {
			m.restart = true
			return m, tea.Quit
		}
		switch m.teardown {
		case teardownSent:
			// Evidence the keystrokes landed: claude went busy, or a new
			// prompt appeared in the transcript. A busy state that predates
			// the keystrokes (teardownArmedBusy) proves nothing, so it is
			// excluded until the turn it was armed during actually ends —
			// after that, a LATER busy edge is real evidence again.
			if m.teardownArmedBusy && teardownTurnEnded(m.state.Kind) {
				m.teardownArmedBusy = false
			}
			if !m.teardownSubmitted &&
				(m.lastTyped != m.teardownPrompt ||
					(!teardownTurnEnded(m.state.Kind) && !m.teardownArmedBusy)) {
				m.teardownSubmitted = true
			}
			if !m.teardownSubmitted && now.Sub(m.teardownAt) >= teardownSubmitTimeout {
				return m.abortTeardown("wrap-up didn't submit", now), tea.Batch(cmds...)
			}
			if !m.teardownProbing && m.teardownProbeDue(now) {
				m.teardownProbing = true
				m.teardownProbeAt = now
				cmds = append(cmds, teardownProbeCmd(m.teardownWorkDir, m.mainCheckout,
					m.teardownAuto && !m.teardownInWorktree))
			}
		case teardownExiting:
			if now.Sub(m.teardownAt) >= teardownExitTimeout {
				return m.abortTeardown("claude didn't exit", now), tea.Batch(cmds...)
			}
			if !m.teardownProbing {
				m.teardownProbing = true
				cmds = append(cmds, claudeGoneCmd(m.selfPane))
			}
		}
		return m, tea.Batch(cmds...)

	case dataMsg:
		m.polling = false
		// Ahead of the rotation early-returns below: the conduct chip must
		// track the lobby regardless of which transcript this head is bound to.
		m.conductRaw = msg.conductRaw
		// A poll that agrees with an outstanding space retires it early, so the
		// chip stops being a promise the moment it becomes a fact. Compared by
		// conductOn, not by mode string: the lobby may answer a "conducting"
		// request with "paused", which is the same thing to this chip.
		if m.conductPendingMode != "" {
			if mode, ok := parseConductValue(msg.conductRaw, msg.time); ok &&
				conductOn(mode) == conductOn(m.conductPendingMode) {
				m.conductPendingMode = ""
			}
		}
		// Session rotated: re-bind to the newer file and discard this batch's
		// events (they were tailed from the old reader). They refresh on the
		// next poll.
		if msg.activeJSONL != "" && msg.activeJSONL != m.jsonlPath {
			// Same session id at a new path is a move, not a rotation — see
			// moveSession. The auto-arm edge and the ready-phase resume check
			// are mirrored from the normal path below: the reseed can carry a
			// prompt the old reader never delivered, and a wrap-up in it must
			// still arm just as a post-ready prompt must still disarm.
			if transcriptSessionID(msg.activeJSONL) == m.sessionID {
				prevTyped := m.lastTyped
				m.moveSession(msg.activeJSONL, msg.time)
				m.autoArmTeardown(prevTyped, msg.time)
				if m.teardown == teardownReady && m.lastTyped != prevTyped &&
					!teardownCommandTyped(m.lastTyped, m.teardownCmdText) {
					m = m.abortTeardown("session resumed", msg.time)
				}
				m.lastUpdate = msg.time
				return m, nil
			}
			cmd := m.switchSession(msg.activeJSONL, msg.time)
			m.lastUpdate = msg.time
			return m, cmd
		}
		if len(msg.newEvents) > 0 {
			m.bg.observe(msg.newEvents, msg.time)
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
		// Captured before the recompute so the arming check below can see the
		// EDGE rather than the value — see autoArmTeardown.
		prevTyped := m.lastTyped
		m.recomputeFromEvents(msg.time)
		pubCmd := m.maybePublishState(msg.time)
		infoCmds := m.maybePublishInfo()
		allPub := tea.Batch(append(infoCmds, pubCmd)...)
		m.lastUpdate = msg.time
		m.autoArmTeardown(prevTyped, msg.time)
		// A ready gate is evidence gathered at the moment it opened, not a
		// standing guarantee: if the session keeps going after that (a new
		// prompt lands that is not the wrap-up itself), the evidence may be
		// stale by the time `x` is pressed — the clean/pushed tree a probe
		// saw at noon says nothing about unpushed work made since. Re-arming
		// from teardownIdle already tracks this edge (autoArmTeardown above);
		// this mirrors that edge detection for the ready phase, dropping back
		// to idle so a resumed session is re-armed and re-probed from
		// scratch rather than trusting a latched gate.
		switch m.teardown {
		case teardownReady:
			if m.lastTyped != prevTyped && !teardownCommandTyped(m.lastTyped, m.teardownCmdText) {
				return m.abortTeardown("session resumed", msg.time), allPub
			}
		}
		if m.shouldSummarize(prevKind, msg.time) {
			m.summarizing = true
			return m, tea.Batch(m.summarize(), allPub)
		}
		if m.shouldRetrySummarize(msg.time) {
			m.summarizing = true
			return m, tea.Batch(m.summarize(), allPub)
		}
		return m, allPub

	case summarizerMsg:
		m.acquiringKey = false
		if msg.s == nil || m.summarizer != nil {
			return m, nil
		}
		m.summarizer = msg.s
		// Seed the status lines now, mirroring Init and switchSession: the
		// session may sit idle indefinitely, so waiting for an edge could mean
		// waiting forever — the exact failure lazy acquisition exists to fix.
		if !m.canSummarize(msg.at) {
			return m, nil
		}
		m.summarizing = true
		return m, m.summarize()

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
			m.noteTopic(msg.summary.Topic)
			m.summaryRetry = false
			m.summary = msg.summary
			return m, m.tabCmdFor(msg.summary)
		}
		// A placeholder reply can only recur until new events arrive, and those
		// bring their own edge; every other failure is worth retrying — but only
		// when the pane is showing the raw-prompt fallback, where staying broken
		// is otherwise permanent for an idle session. A placeholder CLEARS the
		// flag rather than merely not setting it: a transport failure may have
		// armed it, and the retry that then lands a placeholder proves the
		// transcript is too thin — looping further bills a call every floor
		// interval that can only fail the same way.
		if errors.Is(msg.err, errPlaceholderSummary) {
			m.summaryRetry = false
		} else if m.summary == (Summary{}) {
			m.summaryRetry = true
		}

	case teardownSentMsg:
		if m.teardown != teardownSent && m.teardown != teardownExiting {
			return m, nil // cancelled while the send was in flight
		}
		if msg.note != "" {
			note := msg.note
			if m.teardown == teardownExiting {
				// This send was the "/exit", not the wrap-up command — its
				// failure note ("wrap-up didn't submit") would be a lie here.
				// Reuse the exit-timeout wording instead of inventing a
				// fourth abort reason.
				note = "claude didn't exit"
			}
			return m.abortTeardown(note, time.Now()), nil
		}

	case teardownProbeMsg:
		m.teardownProbing = false
		if m.teardown != teardownSent {
			return m, nil
		}
		if m.teardownAuto && !m.teardownInWorktree {
			// Auto-armed without worktree evidence: the gate needs the
			// wrap-up's own success bar. Same freshness requirement as the
			// worktree path below.
			//
			// A probe issued before an esc → re-arm boundary can still land
			// here after the model has moved on; checkedClean is what marks
			// it as answering the cleanliness question at all — without it, a
			// worktree-mode probe's zero-value cleanReason ("") would read as
			// "clean" and could open the gate on evidence that was never
			// gathered.
			if !msg.checkedClean {
				return m, nil
			}
			if m.teardownSubmitted && teardownAutoGateOpen(m.state.Kind, false, false, msg.cleanReason) {
				m.teardown = teardownReady
				m.teardownAt = time.Now()
				m.teardownBlocked = false
				m.teardownBlockReason = ""
				return m, nil
			}
			m.teardownBlocked = m.teardownSubmitted && teardownTurnEnded(m.state.Kind) && msg.cleanReason != ""
			if m.teardownBlocked {
				m.teardownBlockReason = msg.cleanReason
			}
			return m, nil
		}
		// A stale probe from the other mode is the same risk as above, mirror
		// image: this path wants a worktree-goneness reading, so a probe that
		// actually checked cleanliness is ignored rather than misread as
		// worktreeGone's zero value (false, which is at least safe — but only
		// by accident, and the next tick re-probes properly either way).
		if msg.checkedClean {
			return m, nil
		}
		// teardownSubmitted is required, not just the gate: m.state.Kind is
		// only as fresh as the last dataMsg, so a probe returning a second
		// after arming can be judged against a StateIdle that was captured
		// before the wrap-up command was even typed. Without this, the gate
		// would open — and invite the irreversible second press — while the
		// wrap-up had barely started. It cannot deadlock: an empty
		// teardown.command sets teardownSubmitted at arm time, and a wrap-up
		// that never submits aborts at teardownSubmitTimeout.
		if m.teardownSubmitted && teardownGateOpen(m.state.Kind, m.teardownInWorktree, msg.worktreeGone) {
			m.teardown = teardownReady
			m.teardownAt = time.Now()
			m.teardownBlocked = false
			m.teardownBlockReason = ""
			return m, nil
		}
		// The turn is over and the worktree is still there: the wrap-up
		// bailed. Say so and keep polling — the user may still be answering.
		// Same freshness requirement as the gate above, for the same reason
		// and one more: "worktree still present" would otherwise accuse a
		// wrap-up that has not started yet, and it also throttles the probe
		// (teardownProbeDue).
		m.teardownBlocked = m.teardownSubmitted &&
			m.teardownInWorktree && teardownTurnEnded(m.state.Kind)

	case claudeGoneMsg:
		m.teardownProbing = false
		if m.teardown != teardownExiting || !msg.gone {
			return m, nil
		}
		return m, killSessionCmd(m.selfPane)
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
	// statusbar (branch and worktree chips included, capped at
	// packedChipCells display cells).
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

// teardownChipText renders this model's teardown chip, or "" when there is
// nothing to show.
func (m model) teardownChipText(now time.Time) string {
	return teardownChip(m.teardown, m.teardownBlocked, m.teardownAuto, m.teardownBlockReason, m.teardownNote, m.teardownNoteAt, now)
}

const (
	summarizingChip = "⟳ summarizing"
	pinnedChip      = "⬚ pinned"
)

// statusChip picks the one chip both status layouts have room for. The order is
// transient-and-actionable, then transient, then ambient:
//
//   - an armed teardown is the only one the user must answer;
//   - a refresh in flight is the sole feedback that `s` registered, and it
//     clears itself within a second or two;
//   - the pin is a mode that stays until toggled, and its effect is visible in
//     the tab itself, so it is the one that can afford to yield.
//
// Shared by renderStateLine and renderStatusbar so the two layouts cannot
// disagree about which chip won.
func (m model) statusChip(now time.Time) string {
	if td := m.teardownChipText(now); td != "" {
		return td
	}
	if m.summarizing {
		return summarizingChip
	}
	if m.tabPinned {
		return pinnedChip
	}
	return ""
}

// conductRawFor is the conduct value the chip should render at now: a pending
// space request's mode while its window stands, else the last real read. The
// pending value is minted fresh on every call rather than stored, so it can
// never go stale under a slow render — the window, not a heartbeat, is what
// ends it. Shared by renderStateLine and renderStatusbar, like statusChip.
func (m model) conductRawFor(now time.Time) string {
	if m.conductPendingMode != "" && now.Before(m.conductPendingUntil) {
		return conductPublishValue(m.conductPendingMode, now)
	}
	return m.conductRaw
}

// renderStatusbar packs state and budget info onto a single
// background-filled line at the bottom of the pane. Between the model and
// ctx segments it shows a chip segment assembled by chipSegment from two
// independent sources — m.sessionBranch for the branch half and chip (the
// worktree name, if non-empty) for the worktree half — capped to
// packedChipCells display cells, since this packed layout has no real width
// budget to hand chipSegment (the right-hand gauges are sized after this
// point).
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
	// The pin sits ahead of the chip, not after it: clipLine (below) only ever
	// truncates from the right, so whichever of the two is closer to the tail
	// disappears first as the pane narrows. The pin is the protected one,
	// because it reports a mode the user switched ON and can only turn off from
	// this pane — losing it would leave the tab frozen with nothing on screen
	// saying why. The worktree chip is merely descriptive and is re-derivable
	// from the session itself, so it is the one that should vanish first.
	//
	// Which of teardown / refresh / pin gets that slot is statusChip's call,
	// shared with renderStateLine so the two layouts cannot disagree. All of
	// them sit ahead of the worktree chip for the clipping reason above.
	if c := m.statusChip(now); c != "" {
		leftParts = append(leftParts, c)
	}
	if c := conductChip(m.conductRawFor(now), now); c != "" {
		leftParts = append(leftParts, c)
	}
	if chip == noWorktreeWarning {
		// No glyph: this message is about having NO worktree, and the branch
		// chip below still renders beside it.
		leftParts = append(leftParts, chip)
		chip = ""
	}
	// This packed layout has no width budget to hand out — the right-hand
	// gauges are sized after this point — so the chips get a fixed cap and
	// clipLine remains the final guard, exactly as the single chip did before.
	// The cap is in display CELLS now, not runes: the old truncateRunes(chip,
	// 24) under-truncated a wide-rune name to 48 cells.
	if chips := chipSegment(m.sessionBranch, chip, packedChipCells); chips != "" {
		leftParts = append(leftParts, chips)
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
	case StateAsking:
		// Blocked on the human, like Idle — same green "come look" dot.
		return dotIdle
	case StateCompacting:
		return dotCompact
	case StateBackground:
		// Work is still running even though the main thread's turn ended —
		// the busy dot is the honest read, not the idle one "Working N" sits
		// next to.
		return dotTool
	case StateWaiting:
		// Claude is booting: something is happening, but not attention-worthy.
		return dotThinking
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
	return rateGauges(m.rateLimits, m.pctSamples, now, barW)
}

// rateGauges is the model-independent core of rateGaugeParts, shared with the
// switchboard (swMetersLine) so both panels build the identical gauge text
// from the same raw rate-limit data.
func rateGauges(rl RateLimits, samples []pctSample, now time.Time, barW int) []string {
	fhPct := float64(rl.FiveHour.UsedPercent)
	wkPct := float64(rl.SevenDay.UsedPercent)
	parts := []string{
		fmt.Sprintf("5h %s %d%%→%s",
			renderBar(barW, fhPct, thresholdColor(fhPct)),
			rl.FiveHour.UsedPercent,
			rl.FiveHour.ResetsAt.Local().Format("3:04p")),
		fmt.Sprintf("wk %s %d%%→%s",
			renderBar(barW, wkPct, thresholdColor(wkPct)),
			rl.SevenDay.UsedPercent,
			rl.SevenDay.ResetsAt.Local().Format("Mon")),
	}
	rate := burnRatePctPerMin(samples, now)
	if rate > 0 {
		eta := etaToEmptyPct(rl.FiveHour.UsedPercent, rate)
		if eta > 0 && now.Add(eta).Before(rl.FiveHour.ResetsAt) {
			parts = append(parts, "empty in "+formatDuration(eta))
		}
	}
	return parts
}

// Chip glyphs. "⎇" is a branch symbol and now means the branch; the worktree
// takes "⌂". Until this change the branch glyph fronted the WORKTREE name,
// which is how a session on `lobby-preview` inside the `align-context-meters`
// worktree came to report only the latter.
const (
	branchGlyph       = "⎇ "
	worktreeGlyph     = "⌂ "
	worktreeGlyphBare = "⌂"
	chipSep           = " · "
)

// packedChipCells caps the chips in the packed single-line layout, which
// cannot compute a real budget: its right-hand gauges are sized after the
// left group is built. 40 cells is the old 24-rune worktree cap plus room for
// a branch beside it.
const packedChipCells = 40

// chipSegment assembles the branch and worktree chips into at most avail
// display cells.
//
// The degradation order is fixed: the worktree name truncates, then falls back
// to its bare glyph, then the branch name truncates, then the branch drops
// entirely. The asymmetry is deliberate — a bare "⌂" still carries information
// (you are in a worktree) while a bare "⎇" carries none, since a session is
// always on some branch. The worktree name is also recoverable elsewhere: it
// names the tmux tab until Haiku takes over. The branch is nowhere else.
func chipSegment(branch, worktree string, avail int) string {
	if avail < 1 {
		return ""
	}
	b, w := "", ""
	if branch != "" {
		b = branchGlyph + branch
	}
	if worktree != "" {
		w = worktreeGlyph + worktree
	}

	switch {
	case b == "" && w == "":
		return ""
	case w == "":
		return fitChip(b, branchGlyph, "", avail)
	case b == "":
		return fitChip(w, worktreeGlyph, worktreeGlyphBare, avail)
	}

	sepW := lipgloss.Width(chipSep)
	bareW := lipgloss.Width(worktreeGlyphBare)
	bw := lipgloss.Width(b)

	// Rung 1: both in full.
	if bw+sepW+lipgloss.Width(w) <= avail {
		return b + chipSep + w
	}
	// Rung 2: truncate the worktree name, so long as a real character of it
	// survives; otherwise fall through to Rung 3's honest bare glyph.
	if t, ok := truncNamed(w, worktreeGlyph, avail-bw-sepW); ok {
		return b + chipSep + t
	}
	// Rung 3: the worktree down to its bare glyph.
	if bw+sepW+bareW <= avail {
		return b + chipSep + worktreeGlyphBare
	}
	// Rung 4: truncate the branch, still keeping the worktree glyph — and, as
	// in Rung 2, only if a real character of the branch name survives.
	if t, ok := truncNamed(b, branchGlyph, avail-sepW-bareW); ok {
		return t + chipSep + worktreeGlyphBare
	}
	// Rung 5: the worktree glyph alone.
	if bareW <= avail {
		return worktreeGlyphBare
	}
	return ""
}

// truncNamed fits chip (glyph + space + name) into room cells and reports
// whether any character of the NAME survived. Measuring the truncated result
// is the whole point: computing "will a character fit" from room alone is
// rune-count logic in disguise, and a two-cell first character defeats it.
// When it reports false the caller must degrade a rung rather than emit the
// result, which would be a glyph plus an ellipsis claiming elided content that
// never had room to exist.
func truncNamed(chip, glyph string, room int) (string, bool) {
	if room < 1 {
		return "", false
	}
	if lipgloss.Width(chip) <= room {
		return chip, true
	}
	t := ansi.Truncate(chip, room, "…")
	return t, lipgloss.Width(t) > lipgloss.Width(glyph)+1
}

// fitChip renders a lone chip within avail. bare is what remains when not even
// a truncated name fits — callers pass the worktree's glyph, which means
// something on its own, and "" for the branch, whose glyph does not.
func fitChip(chip, glyph, bare string, avail int) string {
	if t, ok := truncNamed(chip, glyph, avail); ok {
		return t
	}
	// No character of the name survives, so fall back to bare. At avail == 2
	// this leaves a cell unspent for the worktree, and that is correct, not an
	// optimization waiting to happen: the only way to fill both cells is "⌂…",
	// the very nameless-ellipsis form this ladder exists to prevent.
	if lipgloss.Width(bare) <= avail {
		return bare
	}
	return ""
}

// renderStateLine renders the top line of the new split layout: the state
// dot, label, duration, model name, and (when the session has a branch
// and/or runs in a worktree) the branch and worktree chips assembled by
// chipSegment — given a real, computed width budget here, unlike the packed
// single-line statusbar's fixed cell cap. Only if the assembled line still
// doesn't fit the pane width does the chip segment itself shrink, via
// chipSegment's own degradation ladder; the state/model text never does.
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
	if c := m.statusChip(now); c != "" {
		parts = append(parts, c)
	}
	if c := conductChip(m.conductRawFor(now), now); c != "" {
		parts = append(parts, c)
	}

	chip := m.worktreeChip()
	if chip == noWorktreeWarning {
		// Put the warning in with the state/model text, which never shrinks,
		// rather than the chip slot below, which is the first thing to give
		// way as the pane narrows. A worktree NAME is fine to lose — it is
		// re-derivable from the session. This warning is the entire visible
		// mitigation for a design risk and must not be the first casualty.
		// The branch chip still renders beside it: knowing which branch a
		// worktree-less session sits on is exactly what you need to act.
		parts = append(parts, chip)
		chip = ""
	}
	left := strings.Join(parts, " · ")

	avail := m.width - 2 // columns inside the " "..." " padding below
	if avail < 1 {
		avail = 1
	}
	sep := " · "
	chipAvail := avail - lipgloss.Width(left) - lipgloss.Width(sep)
	chips := ""
	if chipAvail > 0 {
		chips = chipSegment(m.sessionBranch, chip, chipAvail)
	}
	if chips == "" {
		return statusbarStyle.Width(m.width).Render(clipLine(" "+left+" ", m.width))
	}
	return statusbarStyle.Width(m.width).Render(clipLine(" "+left+sep+chips+" ", m.width))
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

// observedWorktree selects the worktree chip's source of truth, in priority
// order:
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
//
// Returns "" for neither — the session is not in, and not driving at arm's
// length, any worktree.
func (m model) observedWorktree() string {
	if m.sessionCwd != "" {
		if name := worktreeNameForCwd(m.sessionCwd); name != "" {
			return name
		}
		return m.cmdWorktree
	}
	return worktreeName(m.jsonlPath)
}

// noWorktreeWarning is the exact text the spec requires the chip slot to
// show when a marked session's first turn ends without a worktree. It carries
// its own "⚠" and takes no chip glyph — it is not a worktree name, so "⌂ "
// would be a lie. The branch chip is unaffected and still renders beside it:
// which branch a worktree-less session sits on is exactly what you need to
// act. renderStatusbar/renderStateLine give the warning priority over clipping —
// unlike a worktree name, it is not "merely descriptive": it is the entire
// user-visible mitigation for the risk this design accepts.
const noWorktreeWarning = "⚠ no worktree"

// worktreeChipText decides what the worktree chip slot shows. A real worktree
// always wins: the warning is only for a marked session that has not got one.
//
// sawPrompt gates the warning so a session sitting at an empty input is not
// accused of skipping anything — nothing has been asked of it yet.
func worktreeChipText(chip string, pending, turnEnded, sawPrompt bool) string {
	if chip != "" {
		return chip
	}
	if pending && turnEnded && sawPrompt {
		return noWorktreeWarning
	}
	return ""
}

func (m model) worktreeChip() string {
	return worktreeChipText(m.observedWorktree(), m.worktreePending,
		teardownTurnEnded(m.state.Kind), m.firstPrompt != "")
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

// teardownKey advances the teardown state machine one press of `x`.
//
// Only two phases accept a press: idle arms the wrap-up, ready commits to the
// kill. The two waiting phases ignore it — a key that does nothing beats a key
// that does something surprising while the pane is mid-sequence.
func (m model) teardownKey() (model, tea.Cmd) {
	switch m.teardown {
	case teardownIdle:
		if m.selfPane == "" {
			return m, nil // not in tmux: nothing to type into, nothing to kill
		}
		m.teardown = teardownSent
		m.teardownAt = time.Now()
		m.teardownPrompt = m.lastTyped
		m.teardownArmedBusy = !teardownTurnEnded(m.state.Kind)
		m.teardownBlocked = false
		m.teardownBlockReason = ""
		m.teardownProbeAt = time.Time{}
		m.teardownNote = ""
		m.teardownAuto = false
		m.captureTeardownTarget()
		if m.teardownCmdText == "" {
			// Nothing was typed, so there is no submission to wait for; the
			// gate is all that stands between here and ready.
			m.teardownSubmitted = true
			return m, nil
		}
		m.teardownSubmitted = false
		return m, teardownSendCmd(m.selfPane, m.paneDir, m.teardownCmdText)

	case teardownReady:
		m.teardown = teardownExiting
		m.teardownAt = time.Now()
		return m, teardownSendCmd(m.selfPane, m.paneDir, "/exit")
	}
	return m, nil
}

// autoArmTeardown arms the teardown watch when the user ran the wrap-up
// command in the claude pane themselves, without pressing `x`.
//
// Typing `/done` by hand is the same act as the first `x` press — the wrap-up
// runs, the worktree goes away — but with nothing watching, so the session is
// left sitting in a directory that no longer exists. Pressing `x` afterwards
// would re-type the command and run the whole wrap-up a second time. Arming
// from the transcript instead makes the two routes converge on the same state
// machine.
//
// The signal is lastTyped, NOT lastPrompt. lastPrompt also accepts Claude
// Code's `last-prompt` bookkeeping events, which are written immediately after
// a bare slash-command turn and carry the previous typed prompt — see
// lastTypedPrompt. Reading lastPrompt here meant the wrap-up was shadowed
// within the same poll batch and this function never fired at all.
//
// prevTyped is lastTyped as it stood before this poll's recompute. The check
// fires on the EDGE, not the value: lastTyped keeps a prompt until the next
// one lands, so matching on the value alone would re-arm on every tick for as
// long as the wrap-up is the newest prompt — including immediately after `esc`
// cancelled it, which would make cancelling impossible. The cost is that a
// second identical wrap-up typed back-to-back (same string, no edge) does not
// re-arm; `x` remains available for that.
//
// Only from teardownIdle: a teardown already in flight — including one the
// head itself started with `x`, whose command reaches the transcript as this
// very prompt — has its own submission evidence and must not be restarted.
//
// Arms for worktree and non-worktree sessions alike. A wrap-up typed by hand
// is submitted the moment it is seen, so what makes the second `x` safe to
// offer is the ready gate, not whether arming happened at all — see
// teardownAutoGateOpen. In a worktree the gate requires the worktree to be
// gone, which is real evidence the wrap-up succeeded. Without one there is no
// such evidence, so the gate instead requires the wrap-up's own success bar —
// a clean, pushed tree — before offering the kill; a dirty reading blocks and
// keeps polling rather than declining to arm. `x` is unchanged for everything
// else.
func (m *model) autoArmTeardown(prevTyped string, now time.Time) {
	if m.teardown != teardownIdle {
		return
	}
	if m.selfPane == "" {
		return // not in tmux: nothing to kill, same as teardownKey
	}
	if m.lastTyped == prevTyped {
		return
	}
	if !teardownCommandTyped(m.lastTyped, m.teardownCmdText) {
		return
	}
	m.captureTeardownTarget()
	teardownLogf("auto-arm prompt=%q worktree=%v cwd=%s",
		m.lastTyped, m.teardownInWorktree, m.teardownWorkDir)
	m.teardown = teardownSent
	m.teardownAt = now
	m.teardownPrompt = m.lastTyped
	// Submitted, not pending: the prompt IS the evidence — it only reached the
	// transcript because claude accepted it. Leaving this false would start the
	// 10s submit deadline against a command already running and abort the watch
	// mid-wrap-up with "wrap-up didn't submit". There is likewise nothing to
	// send, so no teardownSendCmd: typing it again is the duplicate run this
	// exists to prevent.
	m.teardownSubmitted = true
	m.teardownArmedBusy = false
	m.teardownBlocked = false
	m.teardownBlockReason = ""
	m.teardownProbeAt = time.Time{}
	m.teardownNote = ""
	m.teardownAuto = true
}

// captureTeardownTarget records the directory the ready gate will watch, and
// whether it is a linked worktree, for the teardown being armed right now.
//
// The session's cwd is authoritative and the head's own cwd is only the
// fallback — see the teardownWorkDir field for why they differ. The fallback
// matters for the window before the first main-session event has been read
// (sessionCwd is ""), where the head's launch directory is the best available
// guess and is exactly right for a non-worktree session.
//
// Symlinks are resolved because worktreeListed compares cleaned, resolved
// paths: git prints resolved paths, and on macOS a transcript cwd under
// /var/... is really /private/var/..., which would otherwise never match and
// would leave the gate permanently shut.
func (m *model) captureTeardownTarget() {
	wd := m.sessionCwd
	if wd == "" {
		wd = m.workDir
	}
	if resolved, err := filepath.EvalSymlinks(wd); err == nil {
		wd = resolved
	}
	m.teardownWorkDir = wd
	m.teardownInWorktree = worktreeNameForCwd(wd) != ""
}

// teardownProbeDue reports whether the ready gate should be sampled on this
// tick. See teardownBlockedProbeInterval: every tick until a blocked reading
// lands, then at a much lower rate for as long as it stays blocked.
func (m model) teardownProbeDue(now time.Time) bool {
	if !m.teardownBlocked {
		return true
	}
	return now.Sub(m.teardownProbeAt) >= teardownBlockedProbeInterval
}

// abortTeardown returns to idle with a reason on the status line. Nothing that
// already happened is undone — the wrap-up command has run, and only this
// program's own sequencing stops.
func (m model) abortTeardown(note string, now time.Time) model {
	teardownLogf("abort phase=%v note=%q submitted=%v blocked=%v jsonl=%s",
		m.teardown, note, m.teardownSubmitted, m.teardownBlocked, m.jsonlPath)
	m.teardown = teardownIdle
	m.teardownBlocked = false
	m.teardownBlockReason = ""
	m.teardownProbing = false
	m.teardownSubmitted = false
	m.teardownArmedBusy = false
	m.teardownAuto = false
	m.teardownNote = note
	m.teardownNoteAt = now
	return m
}
