package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// When the active Claude session in a directory rotates (a newer .jsonl
// appears), an MRA-following monitor must rebind to it: swapping the reader,
// session ID, and reseeding/recomputing derived state. This is the fix for
// the "goes stale on long-running sessions" bug — the monitor was frozen to
// whatever file was newest at launch.
func TestSwitchSessionRebindsToNewerFile(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old-sess.jsonl")
	if err := os.WriteFile(old, []byte(`{"type":"assistant","timestamp":"2026-05-15T10:00:00Z","message":{"model":"claude-old","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := model{
		jsonlPath:    old,
		sessionID:    "old-sess",
		followActive: true,
		reader:       newEventReader(old),
	}
	m.reader.SeedFromEnd(500)
	m.allEvents, _ = m.reader.Seeded()
	m.recomputeFromEvents(time.Now())
	if m.modelName != "claude-old" {
		t.Fatalf("precondition: modelName = %q, want %q", m.modelName, "claude-old")
	}

	// A newer session file appears in the same directory.
	newp := filepath.Join(dir, "new-sess.jsonl")
	if err := os.WriteFile(newp, []byte(`{"type":"assistant","timestamp":"2026-05-29T10:00:00Z","message":{"model":"claude-new","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m.switchSession(newp, time.Now())

	if m.jsonlPath != newp {
		t.Errorf("jsonlPath = %q, want %q", m.jsonlPath, newp)
	}
	if m.sessionID != "new-sess" {
		t.Errorf("sessionID = %q, want %q", m.sessionID, "new-sess")
	}
	if m.modelName != "claude-new" {
		t.Errorf("modelName = %q, want %q (reseed+recompute from new file)", m.modelName, "claude-new")
	}
}

// A freshly-started session has no assistant turns yet, so its transcript
// carries no usage (and often no model). Rotating onto it must RESET the
// context gauge and model — recomputeFromEvents alone can't, because it only
// overwrites those fields when the new ring has something to say, so the old
// session's near-full ctx% would survive the rebind and render as if the new
// session were already at 92%.
func TestSwitchSessionResetsContextAndModel(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old-sess.jsonl")
	if err := os.WriteFile(old, []byte(`{"type":"assistant","timestamp":"2026-05-15T10:00:00Z","message":{"model":"claude-old","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1000,"cache_read_input_tokens":180000,"output_tokens":500}}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := model{
		jsonlPath:    old,
		sessionID:    "old-sess",
		followActive: true,
		reader:       newEventReader(old),
	}
	m.reader.SeedFromEnd(500)
	m.allEvents, _ = m.reader.Seeded()
	m.recomputeFromEvents(time.Now())
	if m.contextPct < 80 {
		t.Fatalf("precondition: contextPct = %v, want the old session near full", m.contextPct)
	}

	// The new session so far holds only the user's first prompt: no assistant
	// turn, no usage, no model.
	newp := filepath.Join(dir, "new-sess.jsonl")
	if err := os.WriteFile(newp, []byte(`{"type":"user","timestamp":"2026-05-29T10:00:00Z","message":{"content":"hello"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m.switchSession(newp, time.Now())

	if m.contextPct != 0 {
		t.Errorf("contextPct = %v, want 0 (fresh session has no usage yet)", m.contextPct)
	}
	if m.modelName != "" {
		t.Errorf("modelName = %q, want \"\" (fresh session has no assistant turn yet)", m.modelName)
	}
}

func TestLastGitBranch(t *testing.T) {
	// Newest wins.
	if got := lastGitBranch([]Event{{GitBranch: "main"}, {GitBranch: "lobby-preview"}}, ""); got != "lobby-preview" {
		t.Errorf("lastGitBranch = %q, want lobby-preview", got)
	}
	// A subagent working in another worktree must not hijack the chip — same
	// rule lastMainCwd applies to cwd.
	if got := lastGitBranch([]Event{{GitBranch: "main"}, {GitBranch: "agent-branch", IsSidechain: true}}, ""); got != "main" {
		t.Errorf("lastGitBranch (sidechain tail) = %q, want main", got)
	}
	// A poll of pure sidechain activity, or a not-yet-seeded ring, keeps what
	// was already known rather than blanking a chip that was right a second ago.
	if got := lastGitBranch([]Event{{GitBranch: "agent", IsSidechain: true}}, "prev-branch"); got != "prev-branch" {
		t.Errorf("lastGitBranch (no usable event) = %q, want prev-branch", got)
	}
	if got := lastGitBranch(nil, "prev-branch"); got != "prev-branch" {
		t.Errorf("lastGitBranch (empty ring) = %q, want prev-branch", got)
	}
	// Claude Code records a detached HEAD as the literal string "HEAD", which
	// would read as a branch named HEAD.
	if got := lastGitBranch([]Event{{GitBranch: "HEAD"}}, ""); got != "detached" {
		t.Errorf("lastGitBranch (detached) = %q, want detached", got)
	}
}

// A rotated session must not inherit the previous session's branch.
func TestSwitchSessionResetsBranch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.jsonl")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	m := model{sessionBranch: "old-branch"}
	m.switchSession(path, time.Now())
	if m.sessionBranch != "" {
		t.Errorf("sessionBranch = %q, want empty after rotation", m.sessionBranch)
	}
}

func TestLastUserPrompt(t *testing.T) {
	events := []Event{
		{Type: "last-prompt", UserText: "first thing"},
		{Type: "assistant", UserText: "i replied"},
		{Type: "user", UserText: ""}, // tool_result turn — no text
		{Type: "last-prompt", UserText: "second thing"},
		{Type: "user", UserText: ""}, // another tool_result
	}
	if got := lastUserPrompt(events); got != "second thing" {
		t.Errorf("lastUserPrompt = %q, want %q", got, "second thing")
	}

	// Falls back to a real user turn when no last-prompt event is present.
	userOnly := []Event{
		{Type: "user", UserText: "typed it"},
		{Type: "assistant", UserText: "ok"},
	}
	if got := lastUserPrompt(userOnly); got != "typed it" {
		t.Errorf("lastUserPrompt = %q, want %q", got, "typed it")
	}

	if got := lastUserPrompt(nil); got != "" {
		t.Errorf("lastUserPrompt(nil) = %q, want empty", got)
	}
}

func TestFirstUserPrompt(t *testing.T) {
	events := []Event{
		{Type: "assistant", UserText: "intro"}, // not a user turn
		{Type: "last-prompt", UserText: "first thing"},
		{Type: "user", UserText: ""}, // tool_result turn — no text
		{Type: "last-prompt", UserText: "second thing"},
	}
	if got := firstUserPrompt(events); got != "first thing" {
		t.Errorf("firstUserPrompt = %q, want %q", got, "first thing")
	}

	// Falls back to a real user turn when no last-prompt event is present.
	userOnly := []Event{
		{Type: "user", UserText: "typed it"},
		{Type: "user", UserText: "typed again"},
	}
	if got := firstUserPrompt(userOnly); got != "typed it" {
		t.Errorf("firstUserPrompt = %q, want %q", got, "typed it")
	}

	if got := firstUserPrompt(nil); got != "" {
		t.Errorf("firstUserPrompt(nil) = %q, want empty", got)
	}
}

func TestRenderPromptLine(t *testing.T) {
	// Newlines and runs of whitespace collapse to single spaces.
	got := renderPromptLine("", "hello\n\n  world", 40, false)
	if !strings.Contains(got, "❯ hello world") {
		t.Errorf("renderPromptLine = %q, want it to contain %q", got, "❯ hello world")
	}

	// A label is shown before the prompt marker.
	labeled := renderPromptLine("first", "the goal", 40, false)
	if !strings.Contains(labeled, "first") || !strings.Contains(labeled, "❯ the goal") {
		t.Errorf("labeled renderPromptLine = %q, want it to contain label and prompt", labeled)
	}

	// Long prompts truncate with an ellipsis and never exceed the width.
	wide := renderPromptLine("", "this is a very long prompt that will not fit", 20, false)
	if w := lipgloss.Width(wide); w != 20 {
		t.Errorf("rendered width = %d, want 20", w)
	}
	if !strings.Contains(wide, "…") {
		t.Errorf("expected truncated line to contain ellipsis, got %q", wide)
	}

	// Empty prompt renders the em-dash placeholder.
	if got := renderPromptLine("", "", 20, false); !strings.Contains(got, "—") {
		t.Errorf("empty prompt = %q, want placeholder %q", got, "—")
	}
}

func TestShortModel(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"claude-opus-4-7", "opus 4.7"},
		{"claude-opus-4-7[1m]", "opus 4.7 1M"},
		{"claude-sonnet-4-6", "sonnet 4.6"},
		{"claude-haiku-4-5-20251001", "haiku 4.5"},
		{"", "—"},
		{"unknown-model", "unknown-model"},
	}
	for _, c := range cases {
		got := shortModel(c.in)
		if got != c.want {
			t.Errorf("shortModel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatBudget(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{200_000, "200k"},
		{1_000_000, "1M"},
		{1_500_000, "1.5M"},
		{500, "500"},
		{0, "0"},
	}
	for _, c := range cases {
		got := formatBudget(c.in)
		if got != c.want {
			t.Errorf("formatBudget(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestContextBudget(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"claude-opus-4-7", 200_000},
		{"claude-opus-4-7[1m]", 1_000_000},
		{"unknown-model", defaultContextBudget},
	}
	for _, c := range cases {
		got := contextBudget(c.model)
		if got != c.want {
			t.Errorf("contextBudget(%q) = %d, want %d", c.model, got, c.want)
		}
	}
}

func TestRecomputeSkipsSyntheticModel(t *testing.T) {
	m := model{allEvents: []Event{
		{Type: "assistant", Model: "claude-opus-4-8"},
		{Type: "assistant", Model: "<synthetic>"},
	}}
	m.recomputeFromEvents(time.Now())
	if m.modelName != "claude-opus-4-8" {
		t.Fatalf("modelName = %q, want claude-opus-4-8", m.modelName)
	}
}

func TestFirstUserPromptSkipsMetaXMLAndCommands(t *testing.T) {
	events := []Event{
		{Type: "user", IsMeta: true, UserText: "Caveat: the messages below were generated..."},
		{Type: "user", UserText: "/clear"},
		{Type: "user", UserText: "<task-notification>...</task-notification>"},
		{Type: "user", UserText: "hey, fix the thing"},
	}
	if got := firstUserPrompt(events); got != "hey, fix the thing" {
		t.Fatalf("firstUserPrompt = %q, want the genuine prompt", got)
	}
}

func TestFirstUserPromptFallsBackToCommand(t *testing.T) {
	events := []Event{
		{Type: "user", IsMeta: true, UserText: "Caveat: ..."},
		{Type: "user", UserText: "/code-review ultra"},
	}
	if got := firstUserPrompt(events); got != "/code-review ultra" {
		t.Fatalf("firstUserPrompt = %q, want the command fallback", got)
	}
}

func TestLastUserPromptSkipsMetaAndXML(t *testing.T) {
	events := []Event{
		{Type: "user", UserText: "run the tests"},
		{Type: "user", IsMeta: true, UserText: "Caveat: ..."},
		{Type: "last-prompt", UserText: "<task-notification>done</task-notification>"},
	}
	if got := lastUserPrompt(events); got != "run the tests" {
		t.Fatalf("lastUserPrompt = %q, want the genuine prompt", got)
	}
}

// At height <= 1 there's no room for anything but the single packed
// statusbar — same line renderStatusbar has always produced, worktree chip
// included (capped to packedChipCells display cells via chipSegment).
func TestViewHeightOnePacksSingleStatusbar(t *testing.T) {
	m := model{
		ready:     true,
		width:     60,
		height:    1,
		jsonlPath: "/Users/alice/.claude/projects/-Users-alice-Projects-webapp--claude-worktrees-feature-branch/abc.jsonl",
	}
	out := m.View()
	lines := strings.Split(out, "\n")
	if len(lines) != 1 {
		t.Fatalf("View() produced %d lines, want 1:\n%s", len(lines), out)
	}
	if !strings.Contains(out, "⌂ feature-branch") {
		t.Errorf("statusbar = %q, want it to contain the worktree chip", out)
	}
}

// At height 4 (and above) the new top-pinned layout stacks the state line,
// the meters line, then the first/last prompt lines, in that order — a full
// reversal of the old bottom-pinned statusbar-last layout.
func TestViewHeightFourOrdersStateMetersPrompts(t *testing.T) {
	m := model{
		ready:       true,
		width:       60,
		height:      4,
		state:       State{Kind: StateIdle, Since: time.Now()},
		firstPrompt: "the session goal",
		lastPrompt:  "the latest ask",
	}
	out := m.View()
	lines := strings.Split(out, "\n")
	if len(lines) != 4 {
		t.Fatalf("View() produced %d lines, want 4:\n%s", len(lines), out)
	}
	state, meters, first, last := lines[0], lines[1], lines[2], lines[3]
	if !strings.Contains(state, "●") {
		t.Errorf("state line (index 0) = %q, want the state dot", state)
	}
	if !strings.Contains(meters, "ctx") {
		t.Errorf("meters line (index 1) = %q, want the ctx gauge", meters)
	}
	if !strings.Contains(first, "the session goal") {
		t.Errorf("first line (index 2) = %q, want the first prompt", first)
	}
	if !strings.Contains(last, "the latest ask") {
		t.Errorf("last line (index 3) = %q, want the last prompt", last)
	}
}

// renderStateLine shows the worktree chip in full (not truncated to 24
// runes like the old statusbar chip) when there's room for it.
func TestRenderStateLineShowsFullWorktreeChip(t *testing.T) {
	name := "an-extremely-long-worktree-branch-name-for-testing-fit" // 54 runes
	m := model{
		width:     100,
		state:     State{Kind: StateIdle, Since: time.Now().Add(-30 * time.Second)},
		modelName: "claude-opus-4-7",
		jsonlPath: "/Users/alice/.claude/projects/-Users-alice-Projects-webapp--claude-worktrees-" + name + "/abc.jsonl",
	}
	now := time.Now()
	got := renderStateLine(m, now)
	if !strings.Contains(got, "⌂ "+name) {
		t.Errorf("renderStateLine = %q, want it to contain the full chip %q", got, name)
	}
	if w := lipgloss.Width(got); w != m.width {
		t.Errorf("renderStateLine width = %d, want %d", w, m.width)
	}
}

// Only when the assembled state line doesn't fit does the chip truncate —
// nothing else shrinks.
//
// The width is chosen to leave the chip slot real room. At width 30 the slot
// works out to 3 cells, where the honest render is the bare "⌂": an ellipsis
// there would be claiming elided content that never fit. Asserting only on
// "…" at that width passed for the wrong reason, pinning the nameless-ellipsis
// defect in place. Assert instead that a character of the NAME survived
// alongside the ellipsis, which is what "truncated" is supposed to mean.
func TestRenderStateLineTruncatesChipWhenNarrow(t *testing.T) {
	name := strings.Repeat("x", 60)
	m := model{
		width:     40,
		state:     State{Kind: StateIdle, Since: time.Now()},
		modelName: "claude-opus-4-7",
		jsonlPath: "/proj--claude-worktrees-" + name + "/abc.jsonl",
	}
	now := time.Now()
	got := renderStateLine(m, now)
	if w := lipgloss.Width(got); w != m.width {
		t.Errorf("renderStateLine width = %d, want %d", w, m.width)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("renderStateLine = %q, want the truncated chip to contain an ellipsis", got)
	}
	if !strings.Contains(got, "⌂ x") {
		t.Errorf("renderStateLine = %q, want part of the worktree name to survive the truncation", got)
	}
	if strings.Contains(got, "⌂ …") {
		t.Errorf("renderStateLine = %q, want no glyph-plus-ellipsis with no name behind it", got)
	}
}

// The "no worktree" warning is not a worktree name: it must render without
// the "⎇ " branch glyph in front of IT (which would read as "here is a branch"
// on a message that means the opposite), and — unlike a real worktree name,
// which is merely descriptive and re-derivable from the session — it must
// survive at a width that would truncate away a same-length worktree chip,
// since it is the entire user-visible mitigation for the design risk this
// session carries.
//
// The fixture carries a branch on purpose. "⎇" appears nowhere in the line is
// no longer the property under test — it only ever held because the fixture
// had no branch, and it would now forbid the coexistence the spec requires
// (warning in the never-shrunk left group, branch chip rendering beside it).
// The real property is that the warning is not the thing the glyph prefixes.
func TestWarningChipHasNoBranchGlyphAndSurvivesNarrowWidth(t *testing.T) {
	warningModel := func(width int) model {
		return model{
			width:           width,
			state:           State{Kind: StateIdle, Since: time.Now()},
			modelName:       "claude-opus-4-7",
			worktreePending: true,
			firstPrompt:     "do the thing",
			sessionBranch:   "main",
			// jsonlPath deliberately NOT a worktree path, and sessionCwd unset:
			// observedWorktree() must be "" so worktreeChip() falls through to
			// the warning.
			jsonlPath: "/proj/abc.jsonl",
		}
	}

	t.Run("renderStateLine", func(t *testing.T) {
		wide := warningModel(100)
		got := renderStateLine(wide, time.Now())
		if !strings.Contains(got, "⚠ no worktree") {
			t.Fatalf("renderStateLine = %q, want it to contain the warning", got)
		}
		if strings.Contains(got, branchGlyph+noWorktreeWarning) {
			t.Errorf("renderStateLine = %q, want the warning not prefixed by the branch glyph", got)
		}
		if !strings.Contains(got, "⎇ main") {
			t.Errorf("renderStateLine = %q, want the branch chip beside the warning", got)
		}

		// Narrow enough that even a short worktree chip would already be the
		// first thing to shrink (compare TestRenderStateLineTruncatesChipWhenNarrow,
		// which loses a chip at width 30), but wide enough to hold the
		// state/model text plus the warning — which is exactly the point:
		// the warning shares the never-shrunk group with state/model, not
		// the chip slot that shrinks first.
		narrow := warningModel(45)
		got = renderStateLine(narrow, time.Now())
		if !strings.Contains(got, "⚠ no worktree") {
			t.Errorf("renderStateLine at width 45 = %q, want the full warning to survive clipping", got)
		}
	})

	t.Run("renderStatusbar", func(t *testing.T) {
		m := warningModel(100)
		got := renderStatusbar(m, time.Now(), m.worktreeChip())
		if !strings.Contains(got, "⚠ no worktree") {
			t.Fatalf("renderStatusbar = %q, want it to contain the warning", got)
		}
		if strings.Contains(got, branchGlyph+noWorktreeWarning) {
			t.Errorf("renderStatusbar = %q, want the warning not prefixed by the branch glyph", got)
		}
		if !strings.Contains(got, "⎇ main") {
			t.Errorf("renderStatusbar = %q, want the branch chip beside the warning", got)
		}
	})
}

// renderMetersLine always keeps the ctx gauge; as width shrinks it drops the
// right-group gauges from the end in today's order: eta, then wk, then 5h.
func TestRenderMetersLineDropsRightGroupKeepsCtx(t *testing.T) {
	now := time.Now()
	m := model{
		width:  200,
		rateOK: true,
		rateLimits: RateLimits{
			FiveHour: Window{UsedPercent: 20, ResetsAt: now.Add(5 * time.Hour)},
			SevenDay: Window{UsedPercent: 30, ResetsAt: now.Add(3 * 24 * time.Hour)},
		},
		pctSamples: []pctSample{
			{at: now.Add(-10 * time.Minute), pct: 10},
			{at: now, pct: 20},
		},
	}
	full := renderMetersLine(m, now)
	for _, want := range []string{"ctx", "5h", "wk", "empty in"} {
		if !strings.Contains(full, want) {
			t.Fatalf("full meters line = %q, want it to contain %q", full, want)
		}
	}

	fullW := lipgloss.Width(full)
	// ctx is the last gauge to drop, but it isn't exempt from the pane-width
	// clip guard (finding 1): below the width the ctx segment itself needs to
	// render intact, clipLine truncates it too rather than wrapping the line.
	// So this loop only asserts "ctx never drops" down to that floor; behavior
	// below the floor is covered by TestRenderLinesNeverWrapAtNarrowWidths.
	ctxFloor := lipgloss.Width(ctxSegment(m, defaultBarW)) + 2
	sawEtaDrop, sawWkDrop := false, false
	for w := fullW; w >= ctxFloor; w-- {
		m.width = w
		line := renderMetersLine(m, now)
		if !strings.Contains(line, "ctx") {
			t.Fatalf("at width %d, ctx gauge dropped: %q", w, line)
		}
		if !sawEtaDrop && !strings.Contains(line, "empty in") {
			sawEtaDrop = true
			if !strings.Contains(line, "5h") || !strings.Contains(line, "wk") {
				t.Fatalf("at width %d, eta dropped but 5h/wk also gone: %q", w, line)
			}
		}
		if sawEtaDrop && !sawWkDrop && !strings.Contains(line, "wk") {
			sawWkDrop = true
			if strings.Contains(line, "empty in") {
				t.Fatalf("at width %d, wk dropped but eta still present: %q", w, line)
			}
		}
	}
	if !sawEtaDrop {
		t.Fatal("never observed eta being dropped as width shrank")
	}
	if !sawWkDrop {
		t.Fatal("never observed wk being dropped as width shrank")
	}
}

// The meters line owns its whole line, so it widens its bars to consume the
// leftover columns instead of leaving a fixed 10-cell bar adrift on a wide
// pane. Slack splits across the three bar-carrying gauges only, so at most
// barCount-1 columns may go unspent.
func TestRenderMetersLineWidensBarsToFillPane(t *testing.T) {
	now := time.Now()
	m := model{
		contextPct: 42,
		rateOK:     true,
		rateLimits: RateLimits{
			FiveHour: Window{UsedPercent: 20, ResetsAt: now.Add(5 * time.Hour)},
			SevenDay: Window{UsedPercent: 73, ResetsAt: now.Add(3 * 24 * time.Hour)},
		},
		pctSamples: []pctSample{
			{at: now.Add(-10 * time.Minute), pct: 10},
			{at: now, pct: 20},
		},
	}

	barCells := func(s string) int {
		n := 0
		for _, r := range s {
			if r == '█' || r == '░' {
				n++
			}
		}
		return n
	}

	var prevBars int
	for _, w := range []int{80, 100, 120, 160, 200} {
		m.width = w
		line := renderMetersLine(m, now)

		// The rendered line is background-filled to the pane width, so measure
		// fill by the content before the trailing pad rather than by width.
		content := len(" ") + lipgloss.Width(strings.TrimRight(ansi.Strip(line), " "))
		if unspent := w - content; unspent > 3 {
			t.Errorf("at width %d, %d columns left unspent (want <= 3): %q", w, unspent, line)
		}
		if bars := barCells(line); bars <= prevBars {
			t.Errorf("at width %d, total bar cells = %d, want more than %d at the previous width", w, bars, prevBars)
		} else {
			prevBars = bars
		}
	}
}

// At height 2 there's room for the state and meters lines but not the
// prompt lines.
func TestViewHeightTwoStateAndMeters(t *testing.T) {
	m := model{
		ready:  true,
		width:  60,
		height: 2,
		state:  State{Kind: StateIdle, Since: time.Now()},
	}
	out := m.View()
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("View() produced %d lines, want 2:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "●") {
		t.Errorf("state line (index 0) = %q, want the state dot", lines[0])
	}
	if !strings.Contains(lines[1], "ctx") {
		t.Errorf("meters line (index 1) = %q, want the ctx gauge", lines[1])
	}
}

// At height 3 one context row joins state and meters: the subject row.
func TestViewHeightThreeAddsSubjectPrompt(t *testing.T) {
	m := model{
		ready:       true,
		width:       60,
		height:      3,
		state:       State{Kind: StateIdle, Since: time.Now()},
		firstPrompt: "do the thing",
	}
	out := m.View()
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("View() produced %d lines, want 3:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[2], "do the thing") {
		t.Errorf("last line (index 2) = %q, want the subject prompt", lines[2])
	}
}

// A blank subject with a populated row under it is worth less than the row
// under it: the single row falls through rather than rendering the em-dash
// placeholder over real context.
func TestViewHeightThreeFallsThroughAnEmptySubject(t *testing.T) {
	m := model{
		ready:      true,
		width:      60,
		height:     3,
		state:      State{Kind: StateIdle, Since: time.Now()},
		lastPrompt: "do the thing",
	}
	out := m.View()
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("View() produced %d lines, want 3:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[2], "do the thing") {
		t.Errorf("last line (index 2) = %q, want the populated row", lines[2])
	}
}

// At height > 4, once state/meters/first/last are placed, the remaining
// rows are blank padding so View keeps its one-row-per-line invariant.
func TestViewHeightSixPadsBlankLinesBelow(t *testing.T) {
	m := model{
		ready:       true,
		width:       60,
		height:      6,
		state:       State{Kind: StateIdle, Since: time.Now()},
		firstPrompt: "goal",
		lastPrompt:  "ask",
	}
	out := m.View()
	lines := strings.Split(out, "\n")
	if len(lines) != 6 {
		t.Fatalf("View() produced %d lines, want 6:\n%s", len(lines), out)
	}
	for i, l := range lines[4:] {
		if l != "" {
			t.Errorf("line %d = %q, want blank padding", 4+i, l)
		}
	}
}

// Regression: at narrow widths, none of the pane-width-constrained line
// renderers may wrap onto a second terminal line — the assembled content
// (including its leading/trailing padding) must be clipped, not wrapped, to
// fit the pane.
func TestRenderLinesNeverWrapAtNarrowWidths(t *testing.T) {
	now := time.Now()
	for _, width := range []int{12, 20} {
		m := model{
			width:      width,
			state:      State{Kind: StateThinking, Since: now.Add(-90 * time.Second)},
			modelName:  "claude-opus-4-7",
			jsonlPath:  "/Users/alice/.claude/projects/-Users-alice-Projects-webapp--claude-worktrees-a-very-long-worktree-name-here/abc.jsonl",
			contextPct: 42,
			rateOK:     true,
			rateLimits: RateLimits{
				FiveHour: Window{UsedPercent: 55, ResetsAt: now.Add(2 * time.Hour)},
				SevenDay: Window{UsedPercent: 30, ResetsAt: now.Add(3 * 24 * time.Hour)},
			},
		}
		cases := map[string]string{
			"renderStateLine":  renderStateLine(m, now),
			"renderMetersLine": renderMetersLine(m, now),
			"renderStatusbar":  renderStatusbar(m, now, worktreeName(m.jsonlPath)),
		}
		for name, out := range cases {
			if strings.Contains(out, "\n") {
				t.Errorf("%s at width %d contains a newline (wrapped): %q", name, width, out)
			}
			if w := lipgloss.Width(out); w > width {
				t.Errorf("%s at width %d has rendered width %d, want <= %d: %q", name, width, w, width, out)
			}
		}
	}
}

// Regression: a wide-rune (CJK) worktree chip must never cause the state
// line to exceed the pane width — display-width-aware truncation, plus the
// clipLine backstop, must both hold under wide runes.
func TestRenderStateLineWideRuneChipNeverExceedsWidth(t *testing.T) {
	name := "機能-ブランチ-名前-とても長い"
	m := model{
		width:     30,
		state:     State{Kind: StateIdle, Since: time.Now()},
		modelName: "claude-opus-4-7",
		jsonlPath: "/proj--claude-worktrees-" + name + "/abc.jsonl",
	}
	now := time.Now()
	got := renderStateLine(m, now)
	if strings.Contains(got, "\n") {
		t.Fatalf("renderStateLine contains a newline (wrapped): %q", got)
	}
	if w := lipgloss.Width(got); w > m.width {
		t.Fatalf("renderStateLine width = %d, want <= %d: %q", w, m.width, got)
	}
}

func TestWorktreeName(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/Users/alice/.claude/projects/-Users-alice-Projects-webapp--claude-worktrees-feature-branch/abc.jsonl", "feature-branch"},
		{"/Users/alice/.claude/projects/-Users-alice-Projects-webapp/abc.jsonl", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := worktreeName(c.path); got != c.want {
			t.Errorf("worktreeName(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestWorktreeNameFromCwd(t *testing.T) {
	cases := []struct{ cwd, want string }{
		{"/Users/alice/Projects/webapp/.claude/worktrees/feature-branch", "feature-branch"},
		// Nested path after the marker: only the first component is the
		// worktree name.
		{"/Users/alice/Projects/webapp/.claude/worktrees/feature-branch/sub/dir", "feature-branch"},
		// Base-repo cwd: no marker at all.
		{"/Users/alice/Projects/webapp", ""},
		// Marker present but nothing follows it.
		{"/Users/alice/Projects/webapp/.claude/worktrees/", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := worktreeNameFromCwd(c.cwd); got != c.want {
			t.Errorf("worktreeNameFromCwd(%q) = %q, want %q", c.cwd, got, c.want)
		}
	}
}

// worktreeChip is driven by the main session's transcript cwd (sessionCwd),
// which tracks the session in and out of worktrees turn by turn. A base-repo
// sessionCwd clears the chip even while the transcript still lives under a
// worktree-encoded project dir from an earlier turn — the core regression this
// fix addresses. The transcript-path fallback applies only before any cwd is
// known.
func TestModelWorktreeChip(t *testing.T) {
	worktreeJSONLPath := "/Users/alice/.claude/projects/-Users-alice-Projects-webapp--claude-worktrees-feature-branch/abc.jsonl"

	// Base-repo session cwd: no chip, even though the transcript dir is
	// worktree-encoded.
	m := model{jsonlPath: worktreeJSONLPath, sessionCwd: "/Users/alice/Projects/webapp"}
	if got := m.worktreeChip(); got != "" {
		t.Errorf("worktreeChip() = %q, want \"\" (base-repo session cwd must clear the chip)", got)
	}

	// No cwd known yet (pre-seed): falls back to the transcript-derived name.
	m = model{jsonlPath: worktreeJSONLPath, sessionCwd: ""}
	if got := m.worktreeChip(); got != "feature-branch" {
		t.Errorf("worktreeChip() = %q, want %q (transcript fallback)", got, "feature-branch")
	}

	// Session cwd under a native worktree: cwd-derived chip.
	m = model{
		jsonlPath:  worktreeJSONLPath,
		sessionCwd: "/Users/alice/Projects/webapp/.claude/worktrees/feature-branch",
	}
	if got := m.worktreeChip(); got != "feature-branch" {
		t.Errorf("worktreeChip() = %q, want %q (native cwd-derived)", got, "feature-branch")
	}
}

// The chip's cwd comes from the last non-sidechain event; a tail of subagent
// (sidechain) activity in a different worktree must not hijack it, and a base
// repo returned-to after worktree work must clear it.
func TestLastMainCwd(t *testing.T) {
	base := "/Users/alice/Projects/webapp"
	wt := base + "/.claude/worktrees/feature"
	agentWt := base + "/.claude/worktrees/agent-xyz"

	// Newest main event wins over an older one.
	got := lastMainCwd([]Event{{Cwd: base}, {Cwd: wt}}, "")
	if got != wt {
		t.Errorf("lastMainCwd = %q, want %q", got, wt)
	}

	// A trailing sidechain event is ignored; the last MAIN cwd stands.
	got = lastMainCwd([]Event{{Cwd: wt}, {Cwd: agentWt, IsSidechain: true}}, "")
	if got != wt {
		t.Errorf("lastMainCwd (sidechain tail) = %q, want %q", got, wt)
	}

	// Returned to base repo: chip source is base, not the earlier worktree.
	got = lastMainCwd([]Event{{Cwd: wt}, {Cwd: base}}, "")
	if got != base {
		t.Errorf("lastMainCwd (returned to base) = %q, want %q", got, base)
	}

	// No usable event this poll (pure sidechain / not-yet-seeded): keep prev.
	got = lastMainCwd([]Event{{Cwd: agentWt, IsSidechain: true}}, "prev-cwd")
	if got != "prev-cwd" {
		t.Errorf("lastMainCwd (no main event) = %q, want %q", got, "prev-cwd")
	}
}

func TestShouldSummarize(t *testing.T) {
	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		summarizer  *Summarizer
		prevKind    StateKind
		kind        StateKind
		summarizing bool
		lastAt      time.Time
		now         time.Time
		want        bool
	}{
		{
			name:       "busy to idle fires",
			summarizer: &Summarizer{},
			prevKind:   StateThinking, kind: StateIdle,
			lastAt: base, now: base.Add(time.Minute),
			want: true,
		},
		{
			name:       "tool to idle fires",
			summarizer: &Summarizer{},
			prevKind:   StateTool, kind: StateIdle,
			lastAt: base, now: base.Add(time.Minute),
			want: true,
		},
		{
			name:       "still idle does not fire",
			summarizer: &Summarizer{},
			prevKind:   StateIdle, kind: StateIdle,
			lastAt: base, now: base.Add(time.Minute),
			want: false,
		},
		{
			name:       "going busy does not fire",
			summarizer: &Summarizer{},
			prevKind:   StateIdle, kind: StateThinking,
			lastAt: base, now: base.Add(time.Minute),
			want: false,
		},
		{
			name:       "a call already in flight does not fire",
			summarizer: &Summarizer{},
			prevKind:   StateThinking, kind: StateIdle,
			summarizing: true,
			lastAt:      base, now: base.Add(time.Minute),
			want: false,
		},
		{
			name:       "a burst inside the minimum interval does not fire",
			summarizer: &Summarizer{},
			prevKind:   StateThinking, kind: StateIdle,
			lastAt: base, now: base.Add(5 * time.Second),
			want: false,
		},
		{
			name:       "no summarizer never fires",
			summarizer: nil,
			prevKind:   StateThinking, kind: StateIdle,
			lastAt: base, now: base.Add(time.Minute),
			want: false,
		},
		// A turn that ends with background work outstanding lands on
		// Background, not Idle. The busy -> ended edge must still fire, or
		// the summary/topic never refreshes until the work clears.
		{
			name:       "busy to background fires",
			summarizer: &Summarizer{},
			prevKind:   StateThinking, kind: StateBackground,
			lastAt: base, now: base.Add(time.Minute),
			want: true,
		},
		{
			name:       "tool to background fires",
			summarizer: &Summarizer{},
			prevKind:   StateTool, kind: StateBackground,
			lastAt: base, now: base.Add(time.Minute),
			want: true,
		},
		// Idle and Background both mean "the turn already ended" — crossing
		// between them is not a busy -> ended edge and must not fire a
		// spurious extra call.
		{
			name:       "idle to background does not fire",
			summarizer: &Summarizer{},
			prevKind:   StateIdle, kind: StateBackground,
			lastAt: base, now: base.Add(time.Minute),
			want: false,
		},
		{
			name:       "background to idle does not fire",
			summarizer: &Summarizer{},
			prevKind:   StateBackground, kind: StateIdle,
			lastAt: base, now: base.Add(time.Minute),
			want: false,
		},
		{
			name:       "still background does not fire",
			summarizer: &Summarizer{},
			prevKind:   StateBackground, kind: StateBackground,
			lastAt: base, now: base.Add(time.Minute),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := model{
				summarizer:         tt.summarizer,
				state:              State{Kind: tt.kind},
				summarizing:        tt.summarizing,
				lastSummaryAt:      tt.lastAt,
				minSummaryInterval: 20 * time.Second,
			}
			if got := m.shouldSummarize(tt.prevKind, tt.now); got != tt.want {
				t.Errorf("shouldSummarize() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSummaryMsgUpdatesModel(t *testing.T) {
	m := model{summarizing: true, ready: true}
	at := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	got, _ := m.Update(summaryMsg{
		summary: Summary{Topic: "fixing the chip", Now: "running tests"},
		at:      at,
	})
	next := got.(model)

	if next.summarizing {
		t.Error("summarizing must clear when the call returns")
	}
	if next.summary.Topic != "fixing the chip" || next.summary.Now != "running tests" {
		t.Errorf("summary = %+v, want both lines set", next.summary)
	}
	if !next.lastSummaryAt.Equal(at) {
		t.Errorf("lastSummaryAt = %v, want %v", next.lastSummaryAt, at)
	}
}

func TestSummaryMsgErrorKeepsLastGoodSummary(t *testing.T) {
	prev := Summary{Topic: "fixing the chip", Now: "running tests"}
	m := model{summarizing: true, ready: true, summary: prev}

	got, _ := m.Update(summaryMsg{err: errors.New("boom"), at: time.Now()})
	next := got.(model)

	if next.summarizing {
		t.Error("summarizing must clear even on error, or the summarizer wedges forever")
	}
	if next.summary != prev {
		t.Errorf("summary = %+v, want the last good summary %+v retained", next.summary, prev)
	}
}

// A rotation must not leave the new session anchored to the old one's topic:
// summarySystemPrompt tells the model to keep a previous topic verbatim unless
// the session clearly moved on, so a topic carried across a rotation would
// re-seed itself as prevTopic forever.
func TestSwitchSessionClearsSummaryState(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old-sess.jsonl")
	if err := os.WriteFile(old, []byte(`{"type":"assistant","timestamp":"2026-05-15T10:00:00Z","message":{"model":"claude-old","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newp := filepath.Join(dir, "new-sess.jsonl")
	if err := os.WriteFile(newp, []byte(`{"type":"assistant","timestamp":"2026-05-29T10:00:00Z","message":{"model":"claude-new","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	lastAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	m := model{
		jsonlPath:     old,
		sessionID:     "old-sess",
		reader:        newEventReader(old),
		summary:       Summary{Topic: "session A topic", Now: "session A step"},
		lastSummaryAt: lastAt,
	}
	genBefore := m.summaryGen

	m.switchSession(newp, lastAt.Add(time.Second))

	if m.summary != (Summary{}) {
		t.Errorf("summary = %+v, want zero (the old session's topic must not anchor the new one)", m.summary)
	}
	// The rate floor is a GLOBAL invariant on API calls, not a per-session one:
	// zeroing it here would make now.Sub(lastSummaryAt) ~2000 years, so a
	// rotation flap could fire a call on every busy→idle edge with no floor at all.
	if !m.lastSummaryAt.Equal(lastAt) {
		t.Errorf("lastSummaryAt = %v, want %v preserved: a rotation must not reset the rate floor", m.lastSummaryAt, lastAt)
	}
	if m.summaryGen == genBefore {
		t.Errorf("summaryGen = %d, want a bump from %d so in-flight calls can be identified as stale", m.summaryGen, genBefore)
	}
}

// A rotated pane has no summary (switchSession cleared it) and would otherwise
// sit on the raw-prompt fallback until the next turn boundary. Rotation fires a
// summarize itself — subject to the same in-flight flag and rate floor as every
// other call.
func TestSwitchSessionFiresIntervalGuardedSummarize(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old-sess.jsonl")
	if err := os.WriteFile(old, []byte(`{"type":"assistant","timestamp":"2026-05-15T10:00:00Z","message":{"model":"claude-old","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newp := filepath.Join(dir, "new-sess.jsonl")
	if err := os.WriteFile(newp, []byte(`{"type":"assistant","timestamp":"2026-05-29T10:00:00Z","message":{"model":"claude-new","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	base := func() model {
		return model{
			jsonlPath:          old,
			sessionID:          "old-sess",
			reader:             newEventReader(old),
			summarizer:         &Summarizer{},
			minSummaryInterval: 20 * time.Second,
		}
	}

	t.Run("fires when the floor has elapsed", func(t *testing.T) {
		m := base()
		m.lastSummaryAt = now.Add(-time.Hour)

		// Deliberately NOT executed — running it would make a network call.
		cmd := m.switchSession(newp, now)

		if cmd == nil {
			t.Error("cmd = nil, want a summarize command so the rotated pane doesn't sit on the raw-prompt fallback")
		}
		if !m.summarizing {
			t.Error("summarizing = false, want true: the in-flight flag must be held for the call it just issued")
		}
	})

	t.Run("respects the rate floor", func(t *testing.T) {
		m := base()
		m.lastSummaryAt = now.Add(-m.minSummaryInterval / 2)

		cmd := m.switchSession(newp, now)

		if cmd != nil {
			t.Error("cmd non-nil, want nil: a rotation inside the rate floor must not fire a call")
		}
		if m.summarizing {
			t.Error("summarizing = true, want false: no call was issued")
		}
	})

	t.Run("no summarizer never fires", func(t *testing.T) {
		m := base()
		m.summarizer = nil
		m.lastSummaryAt = now.Add(-time.Hour)

		if cmd := m.switchSession(newp, now); cmd != nil {
			t.Error("cmd non-nil, want nil with no summarizer configured")
		}
	})
}

// The wiring, not just switchSession: Update's rotation branch returned nil and
// threw the command away, so the summarize it issued never ran.
func TestUpdateDataMsgRotationReturnsSummarizeCmd(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old-sess.jsonl")
	if err := os.WriteFile(old, []byte(`{"type":"assistant","timestamp":"2026-05-15T10:00:00Z","message":{"model":"claude-old","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newp := filepath.Join(dir, "new-sess.jsonl")
	if err := os.WriteFile(newp, []byte(`{"type":"assistant","timestamp":"2026-05-29T10:00:00Z","message":{"model":"claude-new","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	m := model{
		ready:         true,
		jsonlPath:     old,
		sessionID:     "old-sess",
		reader:        newEventReader(old),
		summarizer:    &Summarizer{},
		lastSummaryAt: now.Add(-time.Hour),
	}

	// Deliberately NOT executed — running it would make a network call.
	got, cmd := m.Update(dataMsg{time: now, activeJSONL: newp, rateLimitErr: errors.New("no rate limits in this test")})
	next := got.(model)

	if next.jsonlPath != newp {
		t.Fatalf("precondition: jsonlPath = %q, want the rotated file %q", next.jsonlPath, newp)
	}
	if cmd == nil {
		t.Error("cmd = nil, want the summarize command switchSession issued to be returned and run")
	}
	if !next.summarizing {
		t.Error("summarizing = false, want true: a call is in flight")
	}
}

// A summarize call in flight over session A's events can land after the monitor
// has rotated to session B. Storing it would label B with A's topic, and the
// keep-the-previous-topic prompt would then hold it there indefinitely. Drop
// it — but still clear summarizing, or the summarizer wedges for the life of
// the process and never fires again.
func TestStaleSummaryMsgAfterRotationIsDropped(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old-sess.jsonl")
	if err := os.WriteFile(old, []byte(`{"type":"assistant","timestamp":"2026-05-15T10:00:00Z","message":{"model":"claude-old","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newp := filepath.Join(dir, "new-sess.jsonl")
	if err := os.WriteFile(newp, []byte(`{"type":"assistant","timestamp":"2026-05-29T10:00:00Z","message":{"model":"claude-new","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := model{jsonlPath: old, sessionID: "old-sess", reader: newEventReader(old), ready: true}
	// A call goes out over session A: it captures A's generation and holds the
	// in-flight flag.
	staleGen := m.summaryGen
	m.summarizing = true

	// The session rotates to B while that call is still in flight.
	m.switchSession(newp, time.Now())

	// A's result lands.
	at := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	got, _ := m.Update(summaryMsg{
		gen:     staleGen,
		summary: Summary{Topic: "session A topic", Now: "session A step"},
		at:      at,
	})
	next := got.(model)

	if next.summary != (Summary{}) {
		t.Errorf("summary = %+v, want zero — a summary computed for session A must not become session B's", next.summary)
	}
	if next.summarizing {
		t.Error("summarizing must clear even when the message is dropped, or the summarizer wedges forever")
	}
}

// newModel must hold summarizing at construction whenever it built a
// summarizer, because Init has a value receiver (it cannot set the flag) and
// unconditionally fires a seed summarize() call. Without the flag held, a
// busy→idle edge on the very first poll fires a second, concurrent call.
func TestNewModelSeedsSummarizingWithSummarizer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"assistant","timestamp":"2026-05-15T10:00:00Z","message":{"model":"claude-old","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("with an api key", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "sk-test")
		// summarizerEnvOptions also looks up ANTHROPIC_BASE_URL, which isn't set
		// above, so without this configValue falls through to the real
		// claudemux-head env file on this machine (a 1Password-backed FIFO blocks
		// for envFileTimeout).
		t.Setenv("CLAUDEMUX_ENV", filepath.Join(t.TempDir(), "absent"))
		m := newModel(defaultConfig(), path, "sess", false)
		if m.summarizer == nil {
			t.Fatal("summarizer = nil, want non-nil when ANTHROPIC_API_KEY is set")
		}
		if !m.summarizing {
			t.Error("summarizing = false, want true: Init fires the seed call and cannot set the flag itself")
		}
	})

	t.Run("without an api key", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "")
		// Without this, configValue falls through to the real claudemux-head env
		// file on this machine (and any FIFO-backed one blocks for envFileTimeout).
		t.Setenv("CLAUDEMUX_ENV", filepath.Join(t.TempDir(), "absent"))
		m := newModel(defaultConfig(), path, "sess", false)
		if m.summarizer != nil {
			t.Fatal("summarizer non-nil, want nil when ANTHROPIC_API_KEY is unset")
		}
		if m.summarizing {
			t.Error("summarizing = true, want false: Init fires no seed call without a summarizer")
		}
	})
}

// summary.enabled: false must disable the summarizer OUTRIGHT — no client is
// constructed even though a key is available. A user who turned the feature off
// must not be billed for it.
func TestNewSummarizerDisabledByConfig(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("CLAUDEMUX_ENV", filepath.Join(t.TempDir(), "absent"))

	cfg := defaultConfig().Summary
	cfg.Enabled = false

	if s := newSummarizer(cfg); s != nil {
		t.Fatal("newSummarizer() non-nil, want nil when summary.enabled is false — an explicitly disabled feature must not construct a billable client")
	}
}

func TestNewSummarizerEnabledUsesConfiguredModel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("CLAUDEMUX_ENV", filepath.Join(t.TempDir(), "absent"))

	cfg := defaultConfig().Summary
	cfg.Model = "claude-sonnet-5"

	s := newSummarizer(cfg)
	if s == nil {
		t.Fatal("newSummarizer() = nil, want non-nil when enabled with a key present")
	}
	if s.model != "claude-sonnet-5" {
		t.Errorf("Summarizer.model = %q, want the configured model", s.model)
	}
}

// The rate limit between summary calls comes from config, not a constant.
func TestNewModelUsesConfiguredMinInterval(t *testing.T) {
	cfg := defaultConfig()
	cfg.Summary.MinInterval = Duration{90 * time.Second}

	m := newModel(cfg, filepath.Join(t.TempDir(), "s.jsonl"), "sess", false)

	if m.minSummaryInterval != 90*time.Second {
		t.Errorf("model.minSummaryInterval = %v, want 90s from config", m.minSummaryInterval)
	}
}

// The trigger wiring, not just the predicate: Update must capture the state
// kind BEFORE recomputing from the new events, or the busy→idle edge collapses
// to idle→idle and the feature silently never fires.
func TestUpdateDataMsgFiresSummarizeOnBusyToIdle(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	m := model{
		ready:      true,
		summarizer: &Summarizer{},
		state:      State{Kind: StateThinking, Since: now.Add(-time.Minute)},
		// Well outside minSummaryInterval, so the interval guard can't be what
		// suppresses the call.
		lastSummaryAt: now.Add(-time.Hour),
	}

	// An assistant event carrying text and no pending tool_use classifies Idle.
	got, cmd := m.Update(dataMsg{
		time:         now,
		newEvents:    []Event{{Type: "assistant", Timestamp: now.Format(time.RFC3339), UserText: "done"}},
		rateLimitErr: errors.New("no rate limits in this test"),
	})
	next := got.(model)

	if next.state.Kind != StateIdle {
		t.Fatalf("precondition: state = %v, want Idle after the new event", next.state.Kind)
	}
	if !next.summarizing {
		t.Error("summarizing = false, want true: the busy→idle edge must fire a summarize call")
	}
	// Deliberately NOT executed — running it would make a network call.
	if cmd == nil {
		t.Error("cmd = nil, want the summarize command")
	}
}

// Regression test: mostRecentlyActiveSession picks the newest-mtime .jsonl, so
// two concurrently-active sessions in one project dir make the monitor
// alternate between them on successive polls — a rotation flap. A Haiku
// round-trip takes longer than the poll interval, so the next rotation bumps
// summaryGen before the reply lands: every reply lands stale. If the rate
// floor only advances on a kept (non-stale) reply, lastSummaryAt freezes at
// its initial value, canSummarize is then permanently true, and every
// rotation fires another call — unbounded by the floor, even though the
// calls were issued and billed regardless of whether their replies survive.
//
// This drives the flap through switchSession/Update's own logic with a fake
// clock. It never executes a returned cmd — that closure would dial the
// network — it only checks whether one was issued.
func TestSessionRotationFlapRespectsRateFloor(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "sess-a.jsonl")
	fileB := filepath.Join(dir, "sess-b.jsonl")
	for _, p := range []string{fileA, fileB} {
		if err := os.WriteFile(p, []byte(`{"type":"assistant","timestamp":"2026-05-15T10:00:00Z","message":{"model":"claude","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	m := model{
		jsonlPath:          fileA,
		sessionID:          "sess-a",
		reader:             newEventReader(fileA),
		summarizer:         &Summarizer{},
		lastSummaryAt:      base.Add(-time.Hour), // floor already elapsed at t=0
		minSummaryInterval: 20 * time.Second,
	}

	callsIssued := 0

	// 10 flap cycles across an 18s span, each cycle: rotate away (may fire a
	// call), rotate back before the reply lands (always stale by then, since
	// the second rotation bumps summaryGen past it), then deliver that stale
	// reply.
	for i := 0; i < 10; i++ {
		cycleStart := base.Add(time.Duration(i) * 2 * time.Second)

		issuedGen := 0
		if cmd := m.switchSession(fileB, cycleStart); cmd != nil {
			callsIssued++
			issuedGen = m.summaryGen
		}

		flapBack := cycleStart.Add(300 * time.Millisecond)
		if cmd := m.switchSession(fileA, flapBack); cmd != nil {
			callsIssued++
			issuedGen = m.summaryGen
		}

		if issuedGen == 0 {
			continue // no call went out this cycle to reply to
		}

		// The reply for whichever call went out lands late — stale, since
		// summaryGen has moved on by the second (flap-back) rotation.
		replyAt := cycleStart.Add(1500 * time.Millisecond)
		got, _ := m.Update(summaryMsg{
			gen:     issuedGen,
			summary: Summary{Topic: "stale topic", Now: "stale step"},
			at:      replyAt,
		})
		m = got.(model)
	}

	if callsIssued > 1 {
		t.Errorf("callsIssued = %d over an 18s span, want at most 1: the rate floor must bound a session-rotation flap even though every reply lands stale", callsIssued)
	}
}

func TestPromptRows(t *testing.T) {
	tests := []struct {
		name        string
		summary     Summary
		firstPrompt string
		lastPrompt  string
		wantLabels  [2]string
		wantText    [2]string
	}{
		{
			name:        "summary wins when present",
			summary:     Summary{Topic: "fixing the chip", Now: "running tests"},
			firstPrompt: "fix the worktree chip",
			lastPrompt:  "go test ./...",
			wantLabels:  [2]string{"topic", "now  "},
			wantText:    [2]string{"fixing the chip", "running tests"},
		},
		{
			name:        "falls back to raw prompts with no summary",
			summary:     Summary{},
			firstPrompt: "fix the worktree chip",
			lastPrompt:  "go test ./...",
			wantLabels:  [2]string{"first", "last "},
			wantText:    [2]string{"fix the worktree chip", "go test ./..."},
		},
		{
			name:        "falls back when only topic is populated",
			summary:     Summary{Topic: "fixing the chip", Now: ""},
			firstPrompt: "fix the worktree chip",
			lastPrompt:  "go test ./...",
			wantLabels:  [2]string{"first", "last "},
			wantText:    [2]string{"fix the worktree chip", "go test ./..."},
		},
		{
			name:        "falls back when only now is populated",
			summary:     Summary{Topic: "", Now: "running tests"},
			firstPrompt: "fix the worktree chip",
			lastPrompt:  "go test ./...",
			wantLabels:  [2]string{"first", "last "},
			wantText:    [2]string{"fix the worktree chip", "go test ./..."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := model{summary: tt.summary, firstPrompt: tt.firstPrompt, lastPrompt: tt.lastPrompt}
			topLabel, top, bottomLabel, bottom := m.promptRows()

			if topLabel != tt.wantLabels[0] || bottomLabel != tt.wantLabels[1] {
				t.Errorf("labels = %q/%q, want %q/%q", topLabel, bottomLabel, tt.wantLabels[0], tt.wantLabels[1])
			}
			if top != tt.wantText[0] || bottom != tt.wantText[1] {
				t.Errorf("text = %q/%q, want %q/%q", top, bottom, tt.wantText[0], tt.wantText[1])
			}
		})
	}
}

func TestPromptRowLabelsAreFiveColumns(t *testing.T) {
	// renderPromptLine assumes a fixed-width label; a ragged label shifts the text
	// column between panes.
	m := model{summary: Summary{Topic: "a", Now: "b"}}
	topLabel, _, bottomLabel, _ := m.promptRows()
	for _, l := range []string{topLabel, bottomLabel} {
		if len(l) != 5 {
			t.Errorf("label %q is %d columns, want 5", l, len(l))
		}
	}

	m = model{firstPrompt: "a", lastPrompt: "b"}
	topLabel, _, bottomLabel, _ = m.promptRows()
	for _, l := range []string{topLabel, bottomLabel} {
		if len(l) != 5 {
			t.Errorf("fallback label %q is %d columns, want 5", l, len(l))
		}
	}

	// Same rule for the optional rows: they share renderPromptLine's fixed
	// text column with the two above them.
	m = model{
		summary: Summary{Topic: "a", Now: "b"}, firstPrompt: "c", lastPrompt: "d",
		showLast: true, showFirst: true,
	}
	for _, r := range m.contextRows() {
		if len(r.label) != 5 {
			t.Errorf("context row label %q is %d columns, want 5", r.label, len(r.label))
		}
	}
}

// The row order runs oldest-last: topic, now, last, first. `last` sits next to
// the summary because it is the row that moves; `first` goes to the bottom,
// where the pane drops rows first.
func TestContextRowsOrderPutsLastBeforeFirst(t *testing.T) {
	m := model{
		summary:     Summary{Topic: "fixing the chip", Now: "running tests"},
		firstPrompt: "fix the worktree chip",
		lastPrompt:  "go test ./...",
		showLast:    true,
		showFirst:   true,
	}
	rows := m.contextRows()

	want := []contextRow{
		{"topic", "fixing the chip"},
		{"now  ", "running tests"},
		{"last ", "go test ./..."},
		{"first", "fix the worktree chip"},
	}
	if len(rows) != len(want) {
		t.Fatalf("contextRows() returned %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

// head.show_first ships OFF, so the default pane is topic/now/last and nothing
// below it — the shape HeadConfig.Rows sizes the pane to.
func TestContextRowsDefaultOmitsFirst(t *testing.T) {
	m := model{
		summary:     Summary{Topic: "fixing the chip", Now: "running tests"},
		firstPrompt: "fix the worktree chip",
		lastPrompt:  "go test ./...",
		showLast:    true,
	}
	rows := m.contextRows()

	if len(rows) != 3 {
		t.Fatalf("contextRows() returned %d rows, want 3: %+v", len(rows), rows)
	}
	if rows[2].label != "last " {
		t.Errorf("bottom row = %q, want the `last` row", rows[2].label)
	}
	for _, r := range rows {
		if r.label == "first" {
			t.Error("`first` row is present with show_first off")
		}
	}
}

// Both rows off is the pre-feature pane exactly: the two summary rows and
// nothing else, which is what HeadConfig.Rows()==4 has to keep drawable.
func TestContextRowsBothOffIsJustTheSummaryPair(t *testing.T) {
	m := model{
		summary:     Summary{Topic: "fixing the chip", Now: "running tests"},
		firstPrompt: "fix the worktree chip",
		lastPrompt:  "go test ./...",
	}
	if rows := m.contextRows(); len(rows) != 2 {
		t.Fatalf("contextRows() returned %d rows, want 2: %+v", len(rows), rows)
	}
}

// On the keyless fallback the leading rows ALREADY are first/last, so the
// toggles must not append a second copy of either prompt.
func TestContextRowsSkipTogglesOnTheKeylessFallback(t *testing.T) {
	m := model{
		firstPrompt: "fix the worktree chip",
		lastPrompt:  "go test ./...",
		showLast:    true,
		showFirst:   true,
	}
	rows := m.contextRows()

	if len(rows) != 2 {
		t.Fatalf("fallback contextRows() returned %d rows, want 2 — the toggles duplicated a prompt: %+v", len(rows), rows)
	}
	if rows[0].label != "first" || rows[1].label != "last " {
		t.Errorf("fallback rows = %q/%q, want first/last", rows[0].label, rows[1].label)
	}
}

// The pane at its configured height draws every configured row, in order.
func TestViewDrawsEveryContextRowAtItsConfiguredHeight(t *testing.T) {
	m := model{
		ready: true, width: 100, height: 6,
		state:       State{Kind: StateIdle, Since: time.Now()},
		summary:     Summary{Topic: "fixing the chip", Now: "running tests"},
		firstPrompt: "fix the worktree chip",
		lastPrompt:  "go test ./...",
		showLast:    true,
		showFirst:   true,
	}
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 6 {
		t.Fatalf("View() produced %d lines, want 6:\n%s", len(lines), m.View())
	}
	for i, want := range []string{"fixing the chip", "running tests", "go test ./...", "fix the worktree chip"} {
		if !strings.Contains(lines[i+2], want) {
			t.Errorf("line %d = %q, want it to carry %q", i+2, lines[i+2], want)
		}
	}
}

// A pane SHORTER than its configuration drops from the bottom, keeping the
// most specific rows. Nothing pins the pane's height beyond the launcher's
// resize hook, so this is a real state, not a defensive one.
func TestViewDropsBottomContextRowsWhenThePaneIsShort(t *testing.T) {
	m := model{
		ready: true, width: 100, height: 5,
		state:       State{Kind: StateIdle, Since: time.Now()},
		summary:     Summary{Topic: "fixing the chip", Now: "running tests"},
		firstPrompt: "fix the worktree chip",
		lastPrompt:  "go test ./...",
		showLast:    true,
		showFirst:   true,
	}
	out := m.View()

	if !strings.Contains(out, "go test ./...") {
		t.Errorf("`last` must survive at height 5\ngot:\n%s", out)
	}
	if strings.Contains(out, "fix the worktree chip") {
		t.Errorf("`first` is the bottom row and must be the one dropped at height 5\ngot:\n%s", out)
	}
}

// A pane TALLER than its configuration pads with blank lines rather than
// inventing rows — View's one-row-per-line invariant.
func TestViewPadsBelowTheLastConfiguredRow(t *testing.T) {
	m := model{
		ready: true, width: 100, height: 7,
		state:       State{Kind: StateIdle, Since: time.Now()},
		summary:     Summary{Topic: "fixing the chip", Now: "running tests"},
		firstPrompt: "fix the worktree chip",
		lastPrompt:  "go test ./...",
		showLast:    true,
	}
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 7 {
		t.Fatalf("View() produced %d lines, want 7:\n%s", len(lines), m.View())
	}
	for i, l := range lines[5:] {
		if l != "" {
			t.Errorf("line %d = %q, want blank padding", 5+i, l)
		}
	}
}

// With room for exactly one context row, the row that survives is the SUBJECT
// — the topic — not the running commentary. The topic is the point of what the
// session is doing; `now` only qualifies it.
func TestViewShowsTheTopicWhenOnlyOneRowFits(t *testing.T) {
	m := model{
		ready: true, width: 80, height: 3,
		summary: Summary{Topic: "fixing the chip", Now: "running tests"},
	}
	out := m.View()

	if !strings.Contains(out, "fixing the chip") {
		t.Errorf("at height 3 the single row must be `topic`\ngot:\n%s", out)
	}
	if strings.Contains(out, "running tests") {
		t.Errorf("at height 3 there is no room for `now`\ngot:\n%s", out)
	}
}

// The same rule holds for the keyless fallback: the first prompt is that
// session's subject, so it is what the single row shows.
func TestViewShowsTheFirstPromptWhenOnlyOneRowFits(t *testing.T) {
	m := model{
		ready: true, width: 80, height: 3,
		firstPrompt: "fix the worktree chip", lastPrompt: "go test ./...",
	}
	out := m.View()

	if !strings.Contains(out, "fix the worktree chip") {
		t.Errorf("at height 3 the single fallback row must be `first`\ngot:\n%s", out)
	}
	if strings.Contains(out, "go test ./...") {
		t.Errorf("at height 3 there is no room for `last`\ngot:\n%s", out)
	}
}

// The subject row carries its emphasis through WEIGHT, with no foreground of
// its own, so it inherits the terminal's default — the most prominent color
// available under either theme. A literal white would read as bold on a dark
// terminal and vanish on a light one, which is the trap the pane's other
// styles are written to avoid.
func TestPromptEmphasisStyleHasNoHardcodedColor(t *testing.T) {
	if !promptEmphasisStyle.GetBold() {
		t.Error("promptEmphasisStyle is not bold, so the subject row has no emphasis at all")
	}
	if fg := promptEmphasisStyle.GetForeground(); fg != lipgloss.TerminalColor(lipgloss.NoColor{}) {
		t.Errorf("promptEmphasisStyle sets Foreground(%v); it must stay unset so the terminal default applies", fg)
	}
}

// Emphasis changes only the weight of the text: same label, same text, same
// rendered width as the unemphasized row.
func TestRenderPromptLineEmphasized(t *testing.T) {
	plain := renderPromptLine("topic", "fixing the chip", 40, false)
	bold := renderPromptLine("topic", "fixing the chip", 40, true)

	if got, want := ansi.Strip(bold), " topic ❯ fixing the chip"; !strings.HasPrefix(got, want) {
		t.Errorf("emphasized line = %q, want it to start with %q", got, want)
	}
	if bw, pw := lipgloss.Width(bold), lipgloss.Width(plain); bw != pw {
		t.Errorf("emphasized width = %d, plain width = %d; emphasis must not change layout", bw, pw)
	}
	// Only meaningful where the renderer emits escapes at all.
	if probe := lipgloss.NewStyle().Bold(true).Render("x"); probe != "x" && bold == plain {
		t.Error("emphasized row renders identically to the plain row, so the topic is not emphasized")
	}
}

// lipgloss ends every inner Render with a full reset, which clears the OUTER
// style's foreground for the remainder of the line. The label is an inner
// Render, so a row that leaned on the promptStyle wrapper to color its text
// silently lost the gray from the label onward and drew the text at the
// terminal's default foreground — the same foreground the emphasized row
// inherits, collapsing the two rows' colors into one.
func TestRenderPromptLineKeepsItsGrayPastTheLabel(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	defer lipgloss.SetColorProfile(orig)

	plain := renderPromptLine("now  ", "editing tui.go", 60, false)
	if !strings.Contains(plain, promptStyle.Render("editing tui.go")) {
		t.Errorf("unemphasized text is not carrying promptStyle's gray:\n%q", plain)
	}

	// And the emphasized row must NOT be gray, or there is no contrast step.
	bold := renderPromptLine("topic", "editing tui.go", 60, true)
	if strings.Contains(bold, promptStyle.Render("editing tui.go")) {
		t.Errorf("emphasized text is gray like the row below it:\n%q", bold)
	}
	if !strings.Contains(bold, promptEmphasisStyle.Render("editing tui.go")) {
		t.Errorf("emphasized text is not carrying promptEmphasisStyle:\n%q", bold)
	}
}

func TestTabRenameArgs(t *testing.T) {
	got, ok := tabRenameArgs("%3", "crm bundling")
	if !ok {
		t.Fatal("ok = false, want true for a real pane + label")
	}
	want := []string{"rename-window", "-t", "%3", "crm bundling"}
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// Outside tmux (no TMUX_PANE) there is nothing to rename.
func TestTabRenameArgsNoPane(t *testing.T) {
	if _, ok := tabRenameArgs("", "crm bundling"); ok {
		t.Error("ok = true with empty pane, want false")
	}
}

// A model that ignores the length instruction, or slips in a newline, must
// not put an over-long or multi-line string into the tmux window name.
func TestTabRenameArgsClampsLabel(t *testing.T) {
	got, ok := tabRenameArgs("%3", "fix   the\ncrm bundling regression across the whole webpack pipeline")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	label := got[len(got)-1]
	if r := []rune(label); len(r) > tabTitleMaxRunes {
		t.Errorf("label %q is %d runes, want <= %d", label, len(r), tabTitleMaxRunes)
	}
	if strings.ContainsAny(label, "\n\t") {
		t.Errorf("label %q still contains a newline/tab, want whitespace collapsed", label)
	}
}

// The clamp must cut between words, never mid-word: "tickets tutorial record…"
// in a tmux tab reads as a bug, not a label.
func TestTabRenameArgsTruncatesAtWordBoundary(t *testing.T) {
	got, ok := tabRenameArgs("%3", "fix the crm bundling regression across the whole webpack pipeline")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	label := got[len(got)-1]
	want := "fix the crm bundling regression across…"
	if label != want {
		t.Errorf("label = %q, want %q", label, want)
	}
}

// A single word longer than the clamp has no boundary to prefer — it still
// rune-chops rather than yielding an empty label.
func TestTabRenameArgsClampsSingleLongWord(t *testing.T) {
	got, ok := tabRenameArgs("%3", strings.Repeat("x", 80))
	if !ok {
		t.Fatal("ok = false, want true")
	}
	label := got[len(got)-1]
	want := strings.Repeat("x", tabTitleMaxRunes-1) + "…"
	if label != want {
		t.Errorf("label = %q, want %q", label, want)
	}
}

// A label that is only whitespace normalizes to empty -> nothing to rename.
func TestTabRenameArgsWhitespaceOnlyLabel(t *testing.T) {
	if _, ok := tabRenameArgs("%3", "   \n  "); ok {
		t.Error("ok = true for a whitespace-only label, want false")
	}
}

func TestTabRenameArgsEmptyLabel(t *testing.T) {
	if _, ok := tabRenameArgs("%3", ""); ok {
		t.Error("ok = true with empty label, want false")
	}
}

// renameTabCmd returns nil (no command) whenever there is nothing to do, so the
// caller can append it unconditionally.
func TestRenameTabCmdNilWhenNothingToDo(t *testing.T) {
	if renameTabCmd("", "crm bundling") != nil {
		t.Error("renameTabCmd with no pane should be nil")
	}
	if renameTabCmd("%3", "") != nil {
		t.Error("renameTabCmd with no label should be nil")
	}
}

// A landed summary renames the window only when tabTitle is on, a pane exists,
// and the label is non-empty. This drives the model's summaryMsg handler and
// asserts on the returned command's presence via the model's decision helper.
func TestSummaryTriggersRenameWhenEnabled(t *testing.T) {
	m := model{tabTitle: true, selfPane: "%3", summaryGen: 1}
	if m.tabCmdFor(Summary{Topic: "t", Now: "n", Tab: "crm bundling"}) == nil {
		t.Error("expected a rename command when tabTitle on, pane set, label present")
	}
}

func TestSummaryNoRenameWhenTabTitleOff(t *testing.T) {
	m := model{tabTitle: false, selfPane: "%3", summaryGen: 1}
	if m.tabCmdFor(Summary{Topic: "t", Now: "n", Tab: "crm bundling"}) != nil {
		t.Error("expected no rename command when tabTitle is off")
	}
}

// A summarize call that fails for a transport-ish reason while the pane has no
// summary at all must schedule a retry: without one, an idle session sits on
// the raw-prompt fallback until the next turn edge, which may never come.
func TestSummaryMsgRetryableErrorMarksRetry(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	m := model{summarizer: &Summarizer{}, summarizing: true}

	got, _ := m.Update(summaryMsg{gen: 0, err: errors.New("api: 529 overloaded"), at: now})
	next := got.(model)

	if !next.summaryRetry {
		t.Error("summaryRetry = false, want true after a retryable failure with no summary")
	}
	if next.summarizing {
		t.Error("summarizing = true, want false: the in-flight flag must clear on completion")
	}
}

// A placeholder reply means the transcript is too thin to describe; retrying
// bills another call that can only fail the same way. A placeholder clears an
// armed retry flag: a transport failure may have armed it, and the retry that
// then lands a placeholder proves the transcript is too thin — looping further
// bills a call every floor interval that can only fail the same way.
func TestSummaryMsgPlaceholderErrorDoesNotRetry(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	m := model{summarizer: &Summarizer{}, summarizing: true, summaryRetry: true}

	got, _ := m.Update(summaryMsg{gen: 0, err: errPlaceholderSummary, at: now})
	next := got.(model)

	if next.summaryRetry {
		t.Error("summaryRetry = true, want false for a placeholder reply — it must clear an armed flag")
	}
}

// A stale reply's error describes a session we no longer watch; scheduling a
// retry from it would bill a call for the new session off the old session's
// failure.
func TestSummaryMsgStaleErrorDoesNotRetry(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	m := model{summarizer: &Summarizer{}, summarizing: true, summaryGen: 2}

	got, _ := m.Update(summaryMsg{gen: 1, err: errors.New("api: timeout"), at: now})
	next := got.(model)

	if next.summaryRetry {
		t.Error("summaryRetry = true, want false for a stale reply")
	}
}

// An error that lands while a previous good summary is showing needs no retry:
// the pane is not on the fallback, and the next edge refreshes it.
func TestSummaryMsgErrorWithExistingSummaryDoesNotRetry(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	m := model{
		summarizer:  &Summarizer{},
		summarizing: true,
		summary:     Summary{Topic: "t", Now: "n", Tab: "tab"},
	}

	got, _ := m.Update(summaryMsg{gen: 0, err: errors.New("api: timeout"), at: now})
	next := got.(model)

	if next.summaryRetry {
		t.Error("summaryRetry = true, want false when a good summary is already showing")
	}
}

// The retry itself: a steady-idle poll (no busy→idle edge) past the retry
// floor must re-issue the call. This is the recovery path for a failed seed
// call on an idle session.
func TestDataMsgRetriesAfterRetryableFailure(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	idleEvent := Event{Type: "assistant", Timestamp: now.Add(-10 * time.Minute).Format(time.RFC3339), UserText: "done"}
	m := model{
		ready:         true,
		summarizer:    &Summarizer{},
		summaryRetry:  true,
		allEvents:     []Event{idleEvent},
		state:         State{Kind: StateIdle, Since: now.Add(-10 * time.Minute)},
		lastSummaryAt: now.Add(-time.Hour),
	}

	got, cmd := m.Update(dataMsg{time: now, rateLimitErr: errors.New("no rate limits in this test")})
	next := got.(model)

	if next.state.Kind != StateIdle {
		t.Fatalf("precondition: state = %v, want steady Idle (no edge)", next.state.Kind)
	}
	if !next.summarizing {
		t.Error("summarizing = false, want true: the retry must fire without an edge")
	}
	// Deliberately NOT executed — running it would make a network call.
	if cmd == nil {
		t.Error("cmd = nil, want the summarize command")
	}
}

// Retries have their own floor so a user who set min_interval: 0 (documented
// as removing the EDGE floor) does not get a self-initiated call every poll.
func TestDataMsgRetryRespectsRetryFloor(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	idleEvent := Event{Type: "assistant", Timestamp: now.Add(-10 * time.Minute).Format(time.RFC3339), UserText: "done"}
	m := model{
		ready:         true,
		summarizer:    &Summarizer{},
		summaryRetry:  true,
		allEvents:     []Event{idleEvent},
		state:         State{Kind: StateIdle, Since: now.Add(-10 * time.Minute)},
		lastSummaryAt: now.Add(-summaryRetryFloor / 2), // inside the retry floor
	}

	got, _ := m.Update(dataMsg{time: now, rateLimitErr: errors.New("no rate limits in this test")})
	next := got.(model)

	if next.summarizing {
		t.Error("summarizing = true, want false: retry inside the floor must not fire")
	}
}

// A successful summary ends the retry loop.
func TestSummaryMsgSuccessClearsRetryFlag(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	m := model{summarizer: &Summarizer{}, summarizing: true, summaryRetry: true}

	got, _ := m.Update(summaryMsg{gen: 0, summary: Summary{Topic: "t", Now: "n", Tab: "tab"}, at: now})
	next := got.(model)

	if next.summaryRetry {
		t.Error("summaryRetry = true, want false after a successful summary")
	}
}

// Session rotation resets the retry loop along with the rest of the summary
// state: the old session's failure says nothing about the new session.
func TestSwitchSessionClearsRetryFlag(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "sess-b.jsonl")
	if err := os.WriteFile(file, []byte(`{"type":"assistant","timestamp":"2026-05-15T10:00:00Z","message":{"model":"claude","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	m := model{summaryRetry: true, lastSummaryAt: now} // floor un-elapsed: no call fires
	m.switchSession(file, now)

	if m.summaryRetry {
		t.Error("summaryRetry = true, want false after switchSession")
	}
}

// A head that started with no key (locked 1Password FIFO) must not disable
// the summarizer for its whole lifetime: the tick loop periodically re-attempts
// construction.
func TestTickFiresSummarizerAcquisition(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	m := model{
		polling:          true, // isolate the acquisition path from pollData
		summaryCfg:       SummaryConfig{Enabled: true},
		lastKeyAttemptAt: now.Add(-2 * summarizerAcquireFloor),
	}

	got, cmd := m.Update(tickMsg(now))
	next := got.(model)

	if !next.acquiringKey {
		t.Error("acquiringKey = false, want true: the tick must schedule an acquisition")
	}
	if next.keyAttempts != 1 {
		t.Errorf("keyAttempts = %d, want 1", next.keyAttempts)
	}
	if !next.lastKeyAttemptAt.Equal(now) {
		t.Errorf("lastKeyAttemptAt = %v, want %v", next.lastKeyAttemptAt, now)
	}
	// Deliberately NOT executed — acquisition reads the key source for real.
	if cmd == nil {
		t.Error("cmd = nil, want a batch containing the acquire command")
	}
}

// Acquisition is paced and capped: each timed-out FIFO read parks goroutines
// for the life of the process (see env.go), so attempts must be bounded.
func TestTickAcquisitionGuards(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		m    model
	}{
		{"inside floor", model{polling: true, summaryCfg: SummaryConfig{Enabled: true},
			lastKeyAttemptAt: now.Add(-summarizerAcquireFloor / 2)}},
		{"at attempt cap", model{polling: true, summaryCfg: SummaryConfig{Enabled: true},
			keyAttempts: summarizerAcquireMax, lastKeyAttemptAt: now.Add(-2 * summarizerAcquireFloor)}},
		{"already in flight", model{polling: true, summaryCfg: SummaryConfig{Enabled: true},
			acquiringKey: true, lastKeyAttemptAt: now.Add(-2 * summarizerAcquireFloor)}},
		{"summary disabled", model{polling: true,
			lastKeyAttemptAt: now.Add(-2 * summarizerAcquireFloor)}},
		{"summarizer already present", model{polling: true, summaryCfg: SummaryConfig{Enabled: true},
			summarizer: &Summarizer{}, lastKeyAttemptAt: now.Add(-2 * summarizerAcquireFloor)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.m.keyAttempts
			got, _ := tc.m.Update(tickMsg(now))
			next := got.(model)
			if next.acquiringKey && !tc.m.acquiringKey {
				t.Error("acquiringKey newly set, want no acquisition scheduled")
			}
			if next.keyAttempts != before {
				t.Errorf("keyAttempts = %d, want unchanged %d", next.keyAttempts, before)
			}
		})
	}
}

// A successful late acquisition installs the summarizer and immediately seeds
// the status lines, mirroring what Init does when the key was there at startup.
func TestSummarizerMsgInstallsAndSeeds(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	m := model{acquiringKey: true, lastSummaryAt: now.Add(-time.Hour)}

	got, cmd := m.Update(summarizerMsg{s: &Summarizer{}, at: now})
	next := got.(model)

	if next.acquiringKey {
		t.Error("acquiringKey = true, want false on completion")
	}
	if next.summarizer == nil {
		t.Fatal("summarizer = nil, want installed")
	}
	if !next.summarizing {
		t.Error("summarizing = false, want true: the late seed call must fire")
	}
	// Deliberately NOT executed — running it would make a network call.
	if cmd == nil {
		t.Error("cmd = nil, want the seed summarize command")
	}
}

// A failed attempt (still no key) just clears the in-flight flag; the next
// tick past the floor tries again, until the cap.
func TestSummarizerMsgNilClearsInFlight(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	m := model{acquiringKey: true}

	got, cmd := m.Update(summarizerMsg{s: nil, at: now})
	next := got.(model)

	if next.acquiringKey {
		t.Error("acquiringKey = true, want false on completion")
	}
	if next.summarizer != nil {
		t.Error("summarizer installed from a nil result")
	}
	if next.summarizing {
		t.Error("summarizing = true, want false: nothing to seed")
	}
	if cmd != nil {
		t.Error("cmd != nil, want nil")
	}
}

// newModel must arm the lazy-acquisition clock when startup found no key, so
// the first re-attempt waits a full floor instead of firing on the next tick
// (startup itself was an attempt).
func TestNewModelArmsAcquisitionClock(t *testing.T) {
	// A plain env file with no ANTHROPIC_API_KEY: newSummarizer completes
	// fast (no FIFO) and returns nil.
	env := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(env, []byte("OTHER=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDEMUX_ENV", env)

	jsonl := filepath.Join(t.TempDir(), "sess.jsonl")
	if err := os.WriteFile(jsonl, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := defaultConfig()
	cfg.Summary.Enabled = true
	m := newModel(cfg, jsonl, "sess", false)

	if m.summarizer != nil {
		t.Fatal("precondition: summarizer non-nil, want nil with a keyless env file")
	}
	if !m.summaryCfg.Enabled {
		t.Error("summaryCfg not stored: lazy acquisition has no config to retry with")
	}
	if m.lastKeyAttemptAt.IsZero() {
		t.Error("lastKeyAttemptAt zero: first lazy attempt would fire immediately instead of after the floor")
	}
}

// The head pane deliberately sets NO background: it inherits the terminal's
// own, so it reads correctly under both light and dark themes. A hardcoded
// background also reintroduces a rendering bug — every nested styled fragment
// (the state dot, each progress bar, the bold prompt label) ends with a
// background reset, so an outer background survives only in the gaps between
// them. The line then renders as stripes (dark label, dark trailing pad, bare
// terminal everywhere else) instead of a solid bar, which is invisible on a
// dark terminal and obvious on a light one.
func TestPaneStylesSetNoBackground(t *testing.T) {
	for name, s := range map[string]lipgloss.Style{
		"statusbarStyle":   statusbarStyle,
		"promptStyle":      promptStyle,
		"promptLabelStyle": promptLabelStyle,
	} {
		if _, ok := s.GetBackground().(lipgloss.NoColor); !ok {
			t.Errorf("%s sets Background(%v); it must stay unset so the pane inherits the terminal background",
				name, s.GetBackground())
		}
	}
}

// While pinned, a landed summary must not rename the window. Summaries keep
// running and the rest of the pane keeps updating; only the rename stops.
func TestPinnedSuppressesRename(t *testing.T) {
	m := model{tabTitle: true, selfPane: "%3", summaryGen: 1, tabPinned: true}
	if m.tabCmdFor(Summary{Topic: "t", Now: "n", Tab: "crm bundling"}) != nil {
		t.Error("expected no rename command while pinned")
	}
}

// Unpinned, the existing behavior stands.
func TestUnpinnedAllowsRename(t *testing.T) {
	m := model{tabTitle: true, selfPane: "%3", summaryGen: 1}
	if m.tabCmdFor(Summary{Topic: "t", Now: "n", Tab: "crm bundling"}) == nil {
		t.Error("expected a rename command while unpinned")
	}
}

// r pins, and pinning issues the reset.
func TestKeyRPins(t *testing.T) {
	m := model{ready: true, width: 80, height: 4, selfPane: "%3", workDir: "/tmp/repo"}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	gm := got.(model)
	if !gm.tabPinned {
		t.Error("tabPinned = false after r, want true")
	}
	if cmd == nil {
		t.Error("expected a reset command when pinning")
	}
}

// A second r unpins and re-applies the current summary label, so the toggle is
// symmetric instead of leaving the tab stale until the next summary lands.
func TestKeyRUnpinsAndReapplies(t *testing.T) {
	m := model{
		ready: true, width: 80, height: 4, selfPane: "%3",
		tabTitle: true, tabPinned: true,
		summary: Summary{Topic: "t", Now: "n", Tab: "crm bundling"},
	}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	gm := got.(model)
	if gm.tabPinned {
		t.Error("tabPinned = true after second r, want false")
	}
	if cmd == nil {
		t.Error("expected the summary label to be re-applied on unpin")
	}
}

// `R` restarts the head in place. It quits like `q` — main re-execs after the
// TUI has restored the terminal — so the only thing distinguishing it from a
// plain quit is the flag main reads afterwards.
func TestKeyShiftRRequestsRestart(t *testing.T) {
	m := model{ready: true, width: 80, height: 4}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	if !got.(model).restart {
		t.Error("restart = false; main would let the pane close instead of re-execing")
	}
	if cmd == nil {
		t.Fatal("R produced no command; the TUI never exits and the re-exec never happens")
	}
	if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
		t.Errorf("R issued %T, want tea.QuitMsg", cmd())
	}
	// Lowercase r is the tab pin and must be untouched by the new binding.
	if got.(model).tabPinned {
		t.Error("R pinned the tab; it took the r branch")
	}
}

// Every other way out must leave the flag clear, or quitting the head would
// resurrect it forever and `q` would be impossible.
func TestQuitKeysDoNotRequestRestart(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyCtrlC},
		{Type: tea.KeyEsc},
	} {
		got, _ := model{ready: true, width: 80, height: 4}.Update(key)
		if got.(model).restart {
			t.Errorf("%s set restart; the head would never stay closed", key)
		}
	}
}

// r must not quit. It shares the key switch with q/ctrl+c/esc, and a fallthrough
// bug there would take the pane down on every reset. Update always returns a
// model even when quitting, so assert on the command: run it and confirm it does
// not yield tea.QuitMsg the way "q" does.
func TestKeyRDoesNotQuit(t *testing.T) {
	m := model{ready: true, width: 80, height: 4}

	_, quitCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if quitCmd == nil {
		t.Fatal("q produced no command; the control for this test is broken")
	}
	if _, isQuit := quitCmd().(tea.QuitMsg); !isQuit {
		t.Fatal("q did not produce tea.QuitMsg; the control for this test is broken")
	}

	// Do NOT execute r's command — resetTabCmd shells out to bash and tmux, and
	// a unit test must not spawn subprocesses. Taking the r branch rather than
	// the quit branch is observable in the model instead: only the r case
	// touches tabPinned.
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if !got.(model).tabPinned {
		t.Error("r left tabPinned false; it fell through to another case")
	}
}

// The pin indicator belongs in the group that is never shrunk — it reports a
// mode the user switched on and can only turn off from this pane, so it must
// not silently disappear, while the worktree chip is descriptive and
// re-derivable. So the worktree chip must truncate away before the pin does.
// clipLine only ever truncates from the right (ansi.Truncate), so this
// requires the pin to sit to the LEFT of the chip in the assembled line —
// checking that ordering here is equivalent to checking that the chip is
// lost first as the pane narrows.
func TestRenderStatusbarPinPrecedesChip(t *testing.T) {
	now := time.Now()
	m := model{ready: true, width: 500, height: 4, tabPinned: true}
	line := renderStatusbar(m, now, "some-worktree-branch")

	pinIdx := strings.Index(line, "⬚ pinned")
	chipIdx := strings.Index(line, "⌂")
	if pinIdx < 0 {
		t.Fatalf("pin indicator missing from %q", line)
	}
	if chipIdx < 0 {
		t.Fatalf("worktree chip missing from %q", line)
	}
	if pinIdx > chipIdx {
		t.Errorf("pin (index %d) comes after chip (index %d) in %q; "+
			"clipLine truncates from the right, so the chip would be lost "+
			"after the pin, not before it", pinIdx, chipIdx, line)
	}
}

// The pin is visible in both layouts, and invisible when unpinned.
func TestPinnedIndicatorRenders(t *testing.T) {
	now := time.Now()
	pinned := model{ready: true, width: 120, height: 4, tabPinned: true}
	unpinned := model{ready: true, width: 120, height: 4}

	for name, line := range map[string]string{
		"state line": renderStateLine(pinned, now),
		"statusbar":  renderStatusbar(pinned, now, ""),
	} {
		if !strings.Contains(line, "pinned") {
			t.Errorf("%s = %q, want it to show the pin", name, line)
		}
	}
	for name, line := range map[string]string{
		"state line": renderStateLine(unpinned, now),
		"statusbar":  renderStatusbar(unpinned, now, ""),
	} {
		if strings.Contains(line, "pinned") {
			t.Errorf("%s = %q, want no pin indicator when unpinned", name, line)
		}
	}
}

// `s` forces a Haiku refresh. The rate floor here is an hour and the last call
// was just now, so an automatic caller could not fire — proving the key skips
// the floor rather than merely riding a window that happened to be open.
func TestRefreshKeyForcesSummarize(t *testing.T) {
	now := time.Now()
	m := model{ready: true, summarizer: &Summarizer{}, minSummaryInterval: time.Hour, lastSummaryAt: now}
	if m.canSummarize(now) {
		t.Fatal("test setup: the rate floor must be closed for this to prove anything")
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if cmd == nil {
		t.Fatal("s must issue a summarize call even inside the rate floor")
	}
	if !next.(model).summarizing {
		t.Error("s must mark the call in flight")
	}
}

// The floor is skippable; the in-flight guard is not. A mashed key must not put
// two billed calls in the air.
func TestRefreshKeyRespectsInFlight(t *testing.T) {
	m := model{ready: true, summarizer: &Summarizer{}, summarizing: true}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}}); cmd != nil {
		t.Error("s must not issue a second call while one is in flight")
	}
}

func TestRefreshKeyWithoutSummarizer(t *testing.T) {
	m := model{ready: true}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}}); cmd != nil {
		t.Error("s must be a no-op with no summarizer (no API key)")
	}
}

// One chip slot, three claimants. Teardown is transient AND actionable so it
// wins; the refresh indicator is transient so it beats the ambient pin.
func TestStatusChipPriority(t *testing.T) {
	now := time.Now()

	m := model{summarizing: true, tabPinned: true}
	if got := m.statusChip(now); got != summarizingChip {
		t.Errorf("statusChip = %q, want %q: a refresh in flight outranks the pin", got, summarizingChip)
	}

	m.summarizing = false
	if got := m.statusChip(now); got != pinnedChip {
		t.Errorf("statusChip = %q, want %q", got, pinnedChip)
	}

	td := teardownTestModel()
	td.summarizing = true
	td.tabPinned = true
	td.teardown = teardownReady
	chip := td.statusChip(now)
	if chip == summarizingChip || chip == pinnedChip {
		t.Errorf("statusChip = %q, want the teardown chip to win", chip)
	}
	if chip == "" {
		t.Error("an armed teardown must render a chip")
	}
}

// StateBackground means work is still running even though the main thread's
// turn ended, so the dot next to "Working N" must read as busy, not idle —
// stateDot used to fall through to the default case and render dotIdle.
//
// dotIdle and dotTool render as byte-identical plain "●" in this non-TTY
// test binary (no ANSI color survives), so comparing the real values can't
// tell them apart. Swap in distinguishable sentinels for the duration of
// the test so the assertion actually exercises which case fired.
func TestStateDotBackgroundIsBusy(t *testing.T) {
	origIdle, origTool := dotIdle, dotTool
	dotIdle, dotTool = "SENTINEL-IDLE", "SENTINEL-TOOL"
	defer func() { dotIdle, dotTool = origIdle, origTool }()

	if got := stateDot(StateBackground); got != dotTool {
		t.Errorf("stateDot(StateBackground) = %q, want dotTool (busy), not the idle dot", got)
	}
}

// A model with just enough set up to exercise teardown transitions.
// teardownTestModel is a head watching a worktree session, shaped the way
// bin/claudemux really launches one: the head's OWN cwd is the main checkout
// (workDir / inWorktree), while the session chdir'd into a linked worktree
// (sessionCwd). The teardown* target fields are pre-captured as teardownKey
// would capture them, so tests that start mid-teardown skip the arming press.
func teardownTestModel() model {
	return model{
		ready:              true,
		width:              120,
		height:             4,
		selfPane:           "%1",
		paneDir:            "/tmp/panemap",
		workDir:            "/tmp/repo",
		inWorktree:         false,
		sessionCwd:         "/tmp/repo/.claude/worktrees/wt",
		teardownWorkDir:    "/tmp/repo/.claude/worktrees/wt",
		teardownInWorktree: true,
		teardownCmdText:    "/done",
		state:              State{Kind: StateIdle},
	}
}

// pollPrompt drives one data poll carrying a user prompt, the way a real poll
// delivers a hand-typed slash command: parseEvent has already unwrapped
// <command-name> into clean text by the time it reaches the model, so the
// prompt arrives as the canonicalized string ("/ameriglide-core:done"), not as
// raw XML. rateLimitErr is set so the poll does not have to carry a plausible
// RateLimits payload — nothing in the teardown path reads it.
func pollPrompt(m model, prompt string, now time.Time) (model, tea.Cmd) {
	msg := dataMsg{time: now, rateLimitErr: errors.New("no rate limits in this test")}
	if prompt != "" {
		msg.newEvents = []Event{{
			Type:      "user",
			UserText:  prompt,
			Timestamp: now.Format(time.RFC3339),
		}}
	}
	next, cmd := m.Update(msg)
	return next.(model), cmd
}

// A wrap-up command typed into the claude pane by hand arms the watch by
// itself. Claude Code canonicalizes slash commands, so the transcript may
// record any of these spellings for the same `/done`.
func TestTeardownAutoArmsFromTypedCommand(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   bool
	}{
		{"bare", "/done", true},
		{"plugin-qualified", "/ameriglide-core:done", true},
		{"another plugin", "/anyplugin:done", true},
		{"a different, longer command", "/done-something", false},
		{"a command ending in the name", "/undone", false},
		{"ordinary prompt", "fix the flaky test", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now()
			got, cmd := pollPrompt(teardownTestModel(), tt.prompt, now)
			if !tt.want {
				if got.teardown != teardownIdle {
					t.Fatalf("phase = %v, want teardownIdle for %q", got.teardown, tt.prompt)
				}
				return
			}
			if got.teardown != teardownSent {
				t.Fatalf("phase = %v, want teardownSent for %q", got.teardown, tt.prompt)
			}
			// The command is already running, so nothing should retype the
			// wrap-up into the pane — asserted below via teardownSubmitted,
			// the actual "already running" signal. cmd itself can no longer be
			// asserted nil: every Idle→Thinking edge now also carries a
			// state-publish cmd (maybePublishState), unrelated to teardown.
			_ = cmd
			// Without this the 10s submit deadline runs against a wrap-up
			// that has already been submitted and aborts mid-run.
			if !got.teardownSubmitted {
				t.Error("submitted = false; the prompt in the transcript IS the submission")
			}
			if !got.teardownAuto {
				t.Error("auto = false; the chip would not say the head armed itself")
			}
			if got.teardownWorkDir != "/tmp/repo/.claude/worktrees/wt" {
				t.Errorf("gate target = %q, want the session's worktree", got.teardownWorkDir)
			}
			if !got.teardownInWorktree {
				t.Error("inWorktree = false; the gate would rest on turn-end alone")
			}
		})
	}
}

// Claude Code writes a `last-prompt` bookkeeping event immediately after a
// bare slash-command turn, and that event carries the PREVIOUS typed prompt —
// the command itself never appears in one. Both land in the same poll batch,
// so a newest-first scan that accepts last-prompt events comes back with the
// older text and the wrap-up is never seen. Observed verbatim in a real
// transcript (Claude Code 2.1.220): a `/ameriglide-core:done` user turn
// followed five lines later by `last-prompt: "ok, so do your cleaner fix"`.
func TestTeardownAutoArmSurvivesLastPromptShadow(t *testing.T) {
	now := time.Now()
	m := teardownTestModel()
	msg := dataMsg{time: now, rateLimitErr: errors.New("no rate limits in this test")}
	msg.newEvents = []Event{
		{Type: "user", UserText: "/ameriglide-core:done", Timestamp: now.Format(time.RFC3339)},
		{Type: "last-prompt", UserText: "ok, so do your cleaner fix"},
	}
	next, cmd := m.Update(msg)
	got := next.(model)
	if got.teardown != teardownSent {
		t.Fatalf("phase = %v, want teardownSent; the wrap-up was shadowed", got.teardown)
	}
	if !got.teardownAuto {
		t.Error("auto = false; the head armed itself and the chip must say so")
	}
	// cmd can no longer be asserted nil here: the Idle→Thinking edge also
	// carries a state-publish cmd (maybePublishState), unrelated to teardown.
	// "The wrap-up is already running" is covered by teardownSubmitted
	// elsewhere (see TestTeardownAutoArmsFromTypedCommand).
	_ = cmd
	// The status line still wants the shadow: it is what Claude Code considers
	// the session's live prompt, and a bare command says nothing about it.
	if got.lastPrompt != "ok, so do your cleaner fix" {
		t.Errorf("lastPrompt = %q, want the last-prompt event's text", got.lastPrompt)
	}
}

// A transcript that reappears at a new path with the SAME session id is the
// session moving between project slugs — Claude Code re-homes the file on
// EnterWorktree/ExitWorktree — not a rotation. The armed watch must survive:
// the move is caused by the very wrap-up the watch is following (its
// ExitWorktree step), and aborting here left /done's worktree removal
// unobserved, so "press x to tear down" never appeared.
func TestUpdateDataMsgSameSessionMovePreservesTeardown(t *testing.T) {
	dir := t.TempDir()
	oldDir := filepath.Join(dir, "proj--worktree-slug")
	newDir := filepath.Join(dir, "proj-main-slug")
	for _, d := range []string{oldDir, newDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := filepath.Join(oldDir, "sess-1.jsonl")
	newp := filepath.Join(newDir, "sess-1.jsonl")
	if err := os.WriteFile(newp, []byte(`{"type":"assistant","timestamp":"2026-08-13T14:50:00Z","message":{"model":"claude-moved","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := teardownTestModel()
	m.jsonlPath = old
	m.sessionID = "sess-1"
	m.reader = newEventReader(old)
	m.teardown = teardownSent
	m.teardownAuto = true
	m.teardownSubmitted = true

	next, _ := m.Update(dataMsg{time: time.Now(), activeJSONL: newp,
		rateLimitErr: errors.New("no rate limits in this test")})
	got := next.(model)

	if got.teardown != teardownSent {
		t.Fatalf("phase = %v, want teardownSent to survive the move", got.teardown)
	}
	if !got.teardownAuto || !got.teardownSubmitted {
		t.Error("teardown evidence lost across the move")
	}
	if got.teardownWorkDir != "/tmp/repo/.claude/worktrees/wt" {
		t.Errorf("gate target = %q, want it preserved", got.teardownWorkDir)
	}
	if got.jsonlPath != newp {
		t.Errorf("jsonlPath = %q, want rebound to %q", got.jsonlPath, newp)
	}
	if got.sessionID != "sess-1" {
		t.Errorf("sessionID = %q, want unchanged", got.sessionID)
	}
	if got.modelName != "claude-moved" {
		t.Errorf("modelName = %q, want %q (reseed + recompute from the new path)", got.modelName, "claude-moved")
	}
}

// A different session id at a new path is a genuine rotation and must still
// abort the watch: the wrap-up went to a session that is gone, and certifying
// its submission against the new session's prompts would be a lie.
func TestUpdateDataMsgRotationAbortsTeardown(t *testing.T) {
	dir := t.TempDir()
	newp := filepath.Join(dir, "sess-2.jsonl")
	if err := os.WriteFile(newp, []byte(`{"type":"assistant","timestamp":"2026-08-13T14:50:00Z","message":{"model":"claude-new","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := teardownTestModel()
	m.jsonlPath = filepath.Join(dir, "sess-1.jsonl")
	m.sessionID = "sess-1"
	m.reader = newEventReader(m.jsonlPath)
	m.teardown = teardownSent
	m.teardownSubmitted = true

	next, _ := m.Update(dataMsg{time: time.Now(), activeJSONL: newp,
		rateLimitErr: errors.New("no rate limits in this test")})
	got := next.(model)

	if got.teardown != teardownIdle {
		t.Fatalf("phase = %v, want teardownIdle after a real rotation", got.teardown)
	}
	if got.teardownNote != "session rotated" {
		t.Errorf("note = %q, want %q", got.teardownNote, "session rotated")
	}
	if got.sessionID != "sess-2" {
		t.Errorf("sessionID = %q, want the rotated session", got.sessionID)
	}
}

// The reseed can be the first delivery of the wrap-up prompt itself: a /done
// whose hooks move the transcript within one poll interval leaves the old
// reader never having tailed the turn. The move path mirrors the normal
// path's auto-arm edge, so the wrap-up seen only in the reseeded ring still
// arms the watch.
func TestUpdateDataMsgMoveArmsWrapUpFromReseed(t *testing.T) {
	dir := t.TempDir()
	oldDir := filepath.Join(dir, "a")
	newDir := filepath.Join(dir, "b")
	for _, d := range []string{oldDir, newDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	newp := filepath.Join(newDir, "sess-1.jsonl")
	if err := os.WriteFile(newp, []byte(`{"type":"user","timestamp":"2026-08-13T14:50:00Z","cwd":"/tmp/repo/.claude/worktrees/wt","message":{"content":"<command-message>done</command-message>\n<command-name>/done</command-name>"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := teardownTestModel()
	m.jsonlPath = filepath.Join(oldDir, "sess-1.jsonl")
	m.sessionID = "sess-1"
	m.reader = newEventReader(m.jsonlPath)
	m.lastTyped = "an earlier prompt"

	next, _ := m.Update(dataMsg{time: time.Now(), activeJSONL: newp,
		rateLimitErr: errors.New("no rate limits in this test")})
	got := next.(model)

	if got.teardown != teardownSent {
		t.Fatalf("phase = %v, want teardownSent: the reseeded wrap-up must arm", got.teardown)
	}
	if !got.teardownAuto {
		t.Error("auto = false; the head armed itself and the chip must say so")
	}
	if got.teardownWorkDir != "/tmp/repo/.claude/worktrees/wt" {
		t.Errorf("gate target = %q, want the worktree cwd from the reseeded ring", got.teardownWorkDir)
	}
}

// Auto-arm outside a worktree now captures the session's real (non-worktree)
// directory as the gate target rather than declining and clearing it — the
// safety net moved to teardownAutoGateOpen's cleanliness requirement (see
// TestTeardownAutoArmsOutsideWorktree, TestTeardownProbeMsgAutoNonWorktree),
// not to refusing to arm at all. This exercises a real temp directory, so
// worktreeNameForCwd genuinely resolves it to "not a worktree" rather than
// relying on a made-up path never matching.
func TestTeardownAutoArmCapturesRealNonWorktreeTarget(t *testing.T) {
	m := teardownTestModel()
	dir := filepath.Join(t.TempDir(), "plain-checkout")
	m.sessionCwd = dir
	got, cmd := pollPrompt(m, "/done", time.Now())
	if got.teardown != teardownSent {
		t.Errorf("phase = %v, want teardownSent outside a worktree", got.teardown)
	}
	_ = cmd
	if got.teardownInWorktree {
		t.Error("inWorktree = true for a plain directory")
	}
	if got.teardownWorkDir != dir {
		t.Errorf("teardownWorkDir = %q, want %q", got.teardownWorkDir, dir)
	}
}

// The `x` path already works. Seeing its own wrap-up land in the transcript
// must not restart it — the re-arm would reset teardownAt and drop the
// evidence the running teardown had already gathered.
func TestTeardownAutoArmIgnoredWhenAlreadyArmed(t *testing.T) {
	for _, phase := range []teardownPhase{teardownSent, teardownReady, teardownExiting} {
		m := teardownTestModel()
		m.teardown = phase
		armedAt := time.Now().Add(-3 * time.Second)
		m.teardownAt = armedAt
		got, _ := pollPrompt(m, "/ameriglide-core:done", time.Now())
		if got.teardown != phase {
			t.Errorf("phase = %v, want %v (unchanged)", got.teardown, phase)
		}
		if got.teardownAuto {
			t.Errorf("phase %v: auto = true; this teardown was armed by a key press", phase)
		}
		if !got.teardownAt.Equal(armedAt) {
			t.Errorf("phase %v: teardownAt moved; the deadline was restarted", phase)
		}
	}
}

// lastPrompt keeps its value across every poll until a newer prompt lands, so
// arming must fire on the edge. Otherwise a cancelled teardown re-arms on the
// very next tick and `esc` can never take effect.
func TestTeardownAutoArmsOncePerSubmission(t *testing.T) {
	now := time.Now()
	armed, _ := pollPrompt(teardownTestModel(), "/done", now)
	if armed.teardown != teardownSent {
		t.Fatalf("phase = %v, want teardownSent", armed.teardown)
	}
	armedAt := armed.teardownAt

	// Further polls with no new prompt must leave the armed teardown alone.
	still := armed
	for i := 0; i < 3; i++ {
		still, _ = pollPrompt(still, "", now.Add(time.Duration(i+1)*time.Second))
		if !still.teardownAt.Equal(armedAt) {
			t.Fatalf("poll %d re-armed: teardownAt moved", i+1)
		}
	}

	// After esc, the same unchanged lastPrompt must not arm it again.
	next, _ := still.Update(tea.KeyMsg{Type: tea.KeyEsc})
	cancelled := next.(model)
	if cancelled.teardown != teardownIdle {
		t.Fatalf("esc left phase = %v", cancelled.teardown)
	}
	again, _ := pollPrompt(cancelled, "", now.Add(10*time.Second))
	if again.teardown != teardownIdle {
		t.Errorf("phase = %v after cancel; a stale lastPrompt re-armed the teardown", again.teardown)
	}
	if again.teardownAuto {
		t.Error("auto = true after cancel")
	}
}

// Outside tmux there is no session to kill, so there is nothing to watch for
// either — the same reason `x` is inert there.
func TestTeardownAutoArmInertOutsideTmux(t *testing.T) {
	m := teardownTestModel()
	m.selfPane = ""
	got, _ := pollPrompt(m, "/done", time.Now())
	if got.teardown != teardownIdle {
		t.Errorf("phase = %v, want teardownIdle outside tmux", got.teardown)
	}
}

// A non-worktree session now auto-arms too — the not-a-worktree decline is
// gone. The auto gate (teardownAutoGateOpen) is what keeps this safe: without
// worktree evidence it additionally requires a clean, pushed tree before the
// second `x` becomes live, so arming itself is no longer the risky part.
func TestTeardownAutoArmsOutsideWorktree(t *testing.T) {
	m := teardownTestModel()
	m.sessionCwd = "/tmp/plain-project"
	prev := m.lastTyped
	m.lastTyped = "/done"
	m.autoArmTeardown(prev, time.Now())
	if m.teardown != teardownSent {
		t.Fatalf("teardown = %v, want teardownSent (auto-arm must no longer decline non-worktree sessions)", m.teardown)
	}
	if !m.teardownAuto {
		t.Error("auto-armed teardown must set teardownAuto")
	}
	if m.teardownInWorktree {
		t.Error("captured target must record non-worktree")
	}
}

func TestTeardownKeyArmsFromIdle(t *testing.T) {
	m := teardownTestModel()
	got, cmd := m.teardownKey()
	if got.teardown != teardownSent {
		t.Errorf("phase = %v, want teardownSent", got.teardown)
	}
	if cmd == nil {
		t.Error("no command issued to send the wrap-up")
	}
}

// Outside tmux there is nothing to type into and nothing to kill.
func TestTeardownKeyInertOutsideTmux(t *testing.T) {
	m := teardownTestModel()
	m.selfPane = ""
	got, cmd := m.teardownKey()
	if got.teardown != teardownIdle {
		t.Errorf("phase = %v, want teardownIdle", got.teardown)
	}
	if cmd != nil {
		t.Error("command issued outside tmux")
	}
}

// An empty teardown.command still arms, but types nothing.
func TestTeardownKeyEmptyCommandSkipsSend(t *testing.T) {
	m := teardownTestModel()
	m.teardownCmdText = ""
	got, cmd := m.teardownKey()
	if got.teardown != teardownSent {
		t.Errorf("phase = %v, want teardownSent", got.teardown)
	}
	if cmd != nil {
		t.Error("command issued despite empty teardown.command")
	}
	if !got.teardownSubmitted {
		t.Error("submitted = false; nothing was sent, so nothing can be awaited")
	}
}

// A press while the gate is still shut must not advance anything.
func TestTeardownKeyIgnoredWhileSent(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownSent
	got, cmd := m.teardownKey()
	if got.teardown != teardownSent {
		t.Errorf("phase = %v, want teardownSent", got.teardown)
	}
	if cmd != nil {
		t.Error("command issued from teardownSent")
	}
}

func TestTeardownKeyFromReadyExits(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownReady
	got, cmd := m.teardownKey()
	if got.teardown != teardownExiting {
		t.Errorf("phase = %v, want teardownExiting", got.teardown)
	}
	if cmd == nil {
		t.Error("no command issued to exit claude")
	}
}

// The direct ladder: `X` arms and `X` confirms, with no wrap-up command typed
// and no ready gate to earn. It exists because the gated ladder depends on
// evidence that a `/done` can fail to leave behind — see teardownDirect.
func TestTeardownDirectKeyArmsFromIdle(t *testing.T) {
	m := teardownTestModel()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	got := next.(model)
	if got.teardown != teardownDirect {
		t.Errorf("phase = %v, want teardownDirect", got.teardown)
	}
	if cmd != nil {
		t.Error("arming the direct ladder must type nothing into the claude pane")
	}
	if got.teardownAuto {
		t.Error("a keypress is not an auto-arm")
	}
}

// Outside tmux there is nothing to kill, same as the gated ladder.
func TestTeardownDirectKeyInertOutsideTmux(t *testing.T) {
	m := teardownTestModel()
	m.selfPane = ""
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	if got := next.(model); got.teardown != teardownIdle {
		t.Errorf("phase = %v, want teardownIdle", got.teardown)
	}
	if cmd != nil {
		t.Error("command issued outside tmux")
	}
}

func TestTeardownDirectKeyConfirmExits(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownDirect
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	if got := next.(model); got.teardown != teardownExiting {
		t.Errorf("phase = %v, want teardownExiting", got.teardown)
	}
	if cmd == nil {
		t.Error("no command issued to exit claude")
	}
}

// The two ladders never cross: a half-finished wrap-up cannot be committed by
// `X`, and a direct arm cannot be committed by `x`.
func TestTeardownLaddersDoNotCross(t *testing.T) {
	for _, phase := range []teardownPhase{teardownSent, teardownReady, teardownExiting} {
		m := teardownTestModel()
		m.teardown = phase
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
		if got := next.(model); got.teardown != phase {
			t.Errorf("X from %v moved to %v", phase, got.teardown)
		}
		if cmd != nil {
			t.Errorf("X from %v issued a command", phase)
		}
	}
	m := teardownTestModel()
	m.teardown = teardownDirect
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if got := next.(model); got.teardown != teardownDirect {
		t.Errorf("x from teardownDirect moved to %v", got.teardown)
	}
	if cmd != nil {
		t.Error("x from teardownDirect issued a command")
	}
}

func TestEscCancelsDirectTeardown(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownDirect
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := next.(model); got.teardown != teardownIdle {
		t.Errorf("phase = %v, want teardownIdle", got.teardown)
	}
	if cmd != nil {
		t.Error("esc during a direct teardown issued a command (quit?)")
	}
}

// A direct arm carries no evidence at all — it is a standing decision to kill
// this session. Any prompt landing afterwards means the session is being used
// again, so the decision is dropped. Unlike the gated ladder there is no
// exception for the wrap-up command: a `/done` starting up is exactly when a
// one-keystroke kill must not stay armed.
func TestTeardownDirectAbortsOnResumedWork(t *testing.T) {
	base, _ := pollPrompt(teardownTestModel(), "start the work", time.Now())

	for _, prompt := range []string{"one more thing", "/done"} {
		m := base
		m.teardown = teardownDirect
		got, _ := pollPrompt(m, prompt, time.Now())
		if got.teardown != teardownIdle {
			t.Errorf("prompt %q: phase = %v, want teardownIdle", prompt, got.teardown)
		}
		if got.teardownNote != "session resumed" {
			t.Errorf("prompt %q: note = %q, want %q", prompt, got.teardownNote, "session resumed")
		}
	}

	m := base
	m.teardown = teardownDirect
	got, _ := pollPrompt(m, "", time.Now())
	if got.teardown != teardownDirect {
		t.Errorf("phase = %v, want teardownDirect unchanged when lastTyped didn't move", got.teardown)
	}
}

// esc cancels an armed teardown instead of quitting the head.
func TestEscCancelsTeardown(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownReady
	m.teardownBlocked = true
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := next.(model)
	if got.teardown != teardownIdle {
		t.Errorf("phase = %v, want teardownIdle", got.teardown)
	}
	if got.teardownBlocked {
		t.Error("blocked flag survived cancel")
	}
	if cmd != nil {
		t.Error("esc during teardown issued a command (quit?)")
	}
}

// With no teardown armed, esc still quits — the pre-existing binding.
func TestEscQuitsWhenIdle(t *testing.T) {
	m := teardownTestModel()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc did not quit from teardownIdle")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("esc issued %T, want tea.QuitMsg", cmd())
	}
}

// The probe opening the gate promotes sent → ready.
func TestTeardownProbeOpensGate(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownSent
	m.teardownSubmitted = true
	m.teardownProbing = true
	next, _ := m.Update(teardownProbeMsg{worktreeGone: true})
	got := next.(model)
	if got.teardown != teardownReady {
		t.Errorf("phase = %v, want teardownReady", got.teardown)
	}
	if got.teardownProbing {
		t.Error("probing flag still held")
	}
}

// A wrap-up that bailed leaves the worktree standing: the gate stays shut and
// the pane says why.
func TestTeardownProbeBlocksWhenWorktreeSurvives(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownSent
	m.teardownSubmitted = true
	next, _ := m.Update(teardownProbeMsg{worktreeGone: false})
	got := next.(model)
	if got.teardown != teardownSent {
		t.Errorf("phase = %v, want teardownSent", got.teardown)
	}
	if !got.teardownBlocked {
		t.Error("blocked = false, want true")
	}
}

// The auto/non-worktree probe path branches off the worktree logic above:
// no worktree evidence is available, so the gate rests on cleanliness
// instead, and a dirty reading blocks with the reason surfaced (never a
// kill) rather than promoting straight to teardownReady.
func TestTeardownProbeMsgAutoNonWorktree(t *testing.T) {
	arm := func() model {
		m := teardownTestModel()
		m.sessionCwd = "/tmp/plain-project"
		prev := m.lastTyped
		m.lastTyped = "/done"
		m.autoArmTeardown(prev, time.Now())
		m.teardownSubmitted = true
		m.state = State{Kind: StateIdle, Since: time.Now()}
		return m
	}

	t.Run("clean opens the gate", func(t *testing.T) {
		m := arm()
		next, _ := m.Update(teardownProbeMsg{cleanReason: "", checkedClean: true})
		got := next.(model)
		if got.teardown != teardownReady {
			t.Errorf("teardown = %v, want teardownReady", got.teardown)
		}
	})

	t.Run("dirty blocks with reason and never kills", func(t *testing.T) {
		m := arm()
		next, _ := m.Update(teardownProbeMsg{cleanReason: "dirty tree", checkedClean: true})
		got := next.(model)
		if got.teardown != teardownSent {
			t.Errorf("teardown = %v, want still teardownSent", got.teardown)
		}
		if !got.teardownBlocked || got.teardownBlockReason != "dirty tree" {
			t.Errorf("blocked=%v reason=%q, want blocked with dirty tree", got.teardownBlocked, got.teardownBlockReason)
		}
	})
}

// A stale probe answering the wrong question must not be misread as its
// answer to the right one. teardownProbeMsg's zero value (checkedClean:
// false, cleanReason: "") looks exactly like "clean" to the auto path unless
// checkedClean says otherwise -- a worktree-mode probe landing here (e.g.
// across an esc -> re-arm boundary) must not open the gate on evidence that
// was never gathered.
func TestTeardownProbeMsgIgnoresWrongMode(t *testing.T) {
	arm := func() model {
		m := teardownTestModel()
		m.sessionCwd = "/tmp/plain-project"
		prev := m.lastTyped
		m.lastTyped = "/done"
		m.autoArmTeardown(prev, time.Now())
		m.teardownSubmitted = true
		m.state = State{Kind: StateIdle, Since: time.Now()}
		return m
	}

	t.Run("unchecked clean reading (worktree-mode zero value) does not open the gate", func(t *testing.T) {
		m := arm()
		next, _ := m.Update(teardownProbeMsg{cleanReason: "", checkedClean: false})
		got := next.(model)
		if got.teardown != teardownSent {
			t.Errorf("teardown = %v, want still teardownSent; an unchecked probe must not be read as clean", got.teardown)
		}
	})

	t.Run("a checked clean reading does open the gate", func(t *testing.T) {
		m := arm()
		next, _ := m.Update(teardownProbeMsg{cleanReason: "", checkedClean: true})
		got := next.(model)
		if got.teardown != teardownReady {
			t.Errorf("teardown = %v, want teardownReady", got.teardown)
		}
	})

	t.Run("a checkedClean reading is ignored on the worktree path", func(t *testing.T) {
		m := teardownTestModel() // teardownInWorktree: true, not auto
		m.teardown = teardownSent
		m.teardownSubmitted = true
		next, _ := m.Update(teardownProbeMsg{worktreeGone: true, checkedClean: true})
		got := next.(model)
		if got.teardown != teardownSent {
			t.Errorf("teardown = %v, want still teardownSent; a cleanliness-mode probe must not be read as worktreeGone", got.teardown)
		}
	})
}

// A ready gate is evidence gathered at the moment it opened, not a standing
// guarantee. type /done at noon -> ready latches; keep working; press x at
// 5pm intending to START a fresh wrap-up -> without this check it sends
// /exit immediately on stale evidence. A new prompt landing while ready --
// unless it is the wrap-up command itself, which is a legitimate re-arm --
// means work resumed and the gate must be re-earned.
func TestTeardownReadyAbortsOnResumedWork(t *testing.T) {
	// Seed a first prompt so there is a real lastTyped value to edge away
	// from, then arm to teardownReady the way other tests do: set the phase
	// and fields directly, bypassing the probe that would normally get here.
	base, _ := pollPrompt(teardownTestModel(), "start the work", time.Now())

	t.Run("a new non-wrap-up prompt aborts", func(t *testing.T) {
		m := base
		m.teardown = teardownReady
		got, _ := pollPrompt(m, "one more thing before we wrap up", time.Now())
		if got.teardown != teardownIdle {
			t.Fatalf("phase = %v, want teardownIdle; resumed work must not sail through on a stale gate", got.teardown)
		}
		if got.teardownNote != "session resumed" {
			t.Errorf("note = %q, want %q", got.teardownNote, "session resumed")
		}
	})

	t.Run("no new prompt does not abort", func(t *testing.T) {
		m := base
		m.teardown = teardownReady
		got, _ := pollPrompt(m, "", time.Now())
		if got.teardown != teardownReady {
			t.Errorf("phase = %v, want teardownReady unchanged when lastTyped didn't move", got.teardown)
		}
	})

	t.Run("the edge landing on the wrap-up command itself does not abort", func(t *testing.T) {
		m := base
		m.teardown = teardownReady
		got, _ := pollPrompt(m, "/done", time.Now())
		if got.teardown != teardownReady {
			t.Errorf("phase = %v, want teardownReady; re-typing the wrap-up command is not resumed work", got.teardown)
		}
	})
}

// Claude exiting during the wait triggers the kill.
func TestClaudeGoneTriggersKill(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownExiting
	_, cmd := m.Update(claudeGoneMsg{gone: true})
	if cmd == nil {
		t.Error("no kill command issued once claude exited")
	}
}

// Claude still alive keeps waiting.
func TestClaudeStillAliveKeepsWaiting(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownExiting
	next, cmd := m.Update(claudeGoneMsg{gone: false})
	if got := next.(model); got.teardown != teardownExiting {
		t.Errorf("phase = %v, want teardownExiting", got.teardown)
	}
	if cmd != nil {
		t.Error("kill command issued while claude was still running")
	}
}

// A wrap-up that never reaches the transcript aborts rather than hanging.
func TestTeardownSubmitTimeoutAborts(t *testing.T) {
	now := time.Now()
	m := teardownTestModel()
	m.teardown = teardownSent
	m.teardownAt = now.Add(-teardownSubmitTimeout - time.Second)
	next, _ := m.Update(tickMsg(now))
	got := next.(model)
	if got.teardown != teardownIdle {
		t.Errorf("phase = %v, want teardownIdle", got.teardown)
	}
	if got.teardownNote != "wrap-up didn't submit" {
		t.Errorf("note = %q, want %q", got.teardownNote, "wrap-up didn't submit")
	}
}

// A claude that will not exit aborts, leaving the session alive.
func TestTeardownExitTimeoutAborts(t *testing.T) {
	now := time.Now()
	m := teardownTestModel()
	m.teardown = teardownExiting
	m.teardownAt = now.Add(-teardownExitTimeout - time.Second)
	next, _ := m.Update(tickMsg(now))
	got := next.(model)
	if got.teardown != teardownIdle {
		t.Errorf("phase = %v, want teardownIdle", got.teardown)
	}
	if got.teardownNote != "claude didn't exit" {
		t.Errorf("note = %q, want %q", got.teardownNote, "claude didn't exit")
	}
}

// Evidence that the command reached claude clears the submit deadline — but
// only when the busy state actually postdates the keystrokes. A busy state
// that predates the press (teardownArmedBusy) proves nothing, so it must not
// certify a submission, and the submit deadline must still be reachable.
func TestTeardownSubmitObservedViaBusyState(t *testing.T) {
	t.Run("armed while already busy proves nothing", func(t *testing.T) {
		now := time.Now()
		m := teardownTestModel()
		m.state = State{Kind: StateTool} // busy BEFORE the key is pressed
		armed, _ := m.teardownKey()
		if !armed.teardownArmedBusy {
			t.Fatal("teardownArmedBusy = false, want true when armed mid-turn")
		}
		armed.teardownAt = now.Add(-time.Second)
		next, _ := armed.Update(tickMsg(now))
		got := next.(model)
		if got.teardownSubmitted {
			t.Error("submitted = true from a busy state that predates the keystrokes")
		}

		// The stale busy reading must not silently satisfy the submit
		// deadline forever: past the timeout it still aborts.
		got.teardownAt = now.Add(-teardownSubmitTimeout - time.Second)
		next2, _ := got.Update(tickMsg(now))
		got2 := next2.(model)
		if got2.teardown != teardownIdle {
			t.Errorf("phase = %v, want teardownIdle; submit timeout must still fire", got2.teardown)
		}
	})

	t.Run("busy edge after an idle arm is real evidence", func(t *testing.T) {
		now := time.Now()
		m := teardownTestModel() // state: StateIdle
		armed, _ := m.teardownKey()
		if armed.teardownArmedBusy {
			t.Fatal("teardownArmedBusy = true, want false when armed while idle")
		}
		armed.teardownAt = now.Add(-time.Second)
		armed.state = State{Kind: StateTool} // claude goes busy afterward
		next, _ := armed.Update(tickMsg(now))
		if got := next.(model); !got.teardownSubmitted {
			t.Error("submitted = false despite claude going busy after an idle arm")
		}
	})
}

// A new prompt in the transcript is the other piece of evidence. It is read
// off lastTyped, not lastPrompt: the wrap-up is a bare slash command, and
// lastPrompt is shadowed back to the previous prompt in the same poll batch.
func TestTeardownSubmitObservedViaNewPrompt(t *testing.T) {
	now := time.Now()
	m := teardownTestModel()
	m.teardown = teardownSent
	m.teardownAt = now.Add(-time.Second)
	m.teardownPrompt = "earlier thing"
	m.lastTyped = "/done"
	m.lastPrompt = "earlier thing" // the shadow; must not mask the evidence
	next, _ := m.Update(tickMsg(now))
	if got := next.(model); !got.teardownSubmitted {
		t.Error("submitted = false despite a new prompt landing")
	}
}

// The mirror image: nothing new was typed, so nothing is certified. Guards the
// test above against passing on a zero-valued lastTyped rather than on a real
// change.
func TestTeardownSubmitNotObservedWithoutNewPrompt(t *testing.T) {
	now := time.Now()
	m := teardownTestModel()
	m.teardown = teardownSent
	m.teardownAt = now.Add(-time.Second)
	m.teardownPrompt = "earlier thing"
	m.lastTyped = "earlier thing"
	m.lastPrompt = "a last-prompt event repeating something else"
	next, _ := m.Update(tickMsg(now))
	if got := next.(model); got.teardownSubmitted {
		t.Error("submitted = true; nothing new was typed")
	}
}

// A failed send aborts immediately with the reason the command reported.
func TestTeardownSentMsgWithNoteAborts(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownSent
	next, _ := m.Update(teardownSentMsg{note: "no claude pane"})
	got := next.(model)
	if got.teardown != teardownIdle {
		t.Errorf("phase = %v, want teardownIdle", got.teardown)
	}
	if got.teardownNote != "no claude pane" {
		t.Errorf("note = %q, want %q", got.teardownNote, "no claude pane")
	}
}

// A send failure during the exit wait (teardownExiting) must report why
// claude didn't exit, not the wrap-up phase's wording — teardownSendCmd
// returns the same note string regardless of which command it was sending.
func TestTeardownSentMsgDuringExitingReportsExitReason(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownExiting
	next, _ := m.Update(teardownSentMsg{note: "wrap-up didn't submit"})
	got := next.(model)
	if got.teardown != teardownIdle {
		t.Errorf("phase = %v, want teardownIdle", got.teardown)
	}
	if got.teardownNote != "claude didn't exit" {
		t.Errorf("note = %q, want %q", got.teardownNote, "claude didn't exit")
	}
}

// The one irreversible action in this feature must never fire off a stale
// signal: a claudeGoneMsg that arrives after the user cancelled back to idle
// must not kill the session.
func TestClaudeGoneAfterCancelIssuesNoCommand(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownExiting
	cancelled, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := cancelled.(model)
	if got.teardown != teardownIdle {
		t.Fatalf("phase = %v, want teardownIdle after cancel", got.teardown)
	}
	if cmd != nil {
		t.Fatal("cancel itself issued a command")
	}
	next, killCmd := got.Update(claudeGoneMsg{gone: true})
	if killCmd != nil {
		t.Error("claudeGoneMsg after cancel issued a command (kill-session?)")
	}
	if final := next.(model); final.teardown != teardownIdle {
		t.Errorf("phase = %v, want teardownIdle to remain undisturbed", final.teardown)
	}
}

// Both abort paths must release the probing flag, or a subsequent arm would
// see teardownProbing already held and skip issuing its own probe/gone
// command forever.
func TestAbortClearsProbingFlag(t *testing.T) {
	t.Run("submit timeout", func(t *testing.T) {
		now := time.Now()
		m := teardownTestModel()
		m.teardown = teardownSent
		m.teardownProbing = true
		m.teardownAt = now.Add(-teardownSubmitTimeout - time.Second)
		next, _ := m.Update(tickMsg(now))
		if got := next.(model); got.teardownProbing {
			t.Error("probing flag still held after submit-timeout abort")
		}
	})

	t.Run("exit timeout", func(t *testing.T) {
		now := time.Now()
		m := teardownTestModel()
		m.teardown = teardownExiting
		m.teardownProbing = true
		m.teardownAt = now.Add(-teardownExitTimeout - time.Second)
		next, _ := m.Update(tickMsg(now))
		if got := next.(model); got.teardownProbing {
			t.Error("probing flag still held after exit-timeout abort")
		}
	})
}

// The sent→ready transition re-stamps teardownAt, so a gate opened long
// after the wrap-up was sent still gets a full exit budget rather than one
// that is instantly (or already) blown.
func TestTeardownReadyGetsFreshDeadline(t *testing.T) {
	now := time.Now()
	m := teardownTestModel()
	m.teardown = teardownSent
	m.teardownSubmitted = true
	// The wrap-up was sent long ago — if teardownAt weren't re-stamped, the
	// exit-timeout check on the very next tick would fire immediately.
	m.teardownAt = now.Add(-time.Hour)
	next, _ := m.Update(teardownProbeMsg{worktreeGone: true})
	got := next.(model)
	if got.teardown != teardownReady {
		t.Fatalf("phase = %v, want teardownReady", got.teardown)
	}
	if got.teardownAt.Before(now.Add(-time.Second)) {
		t.Errorf("teardownAt = %v, want re-stamped near %v", got.teardownAt, now)
	}
}

// A tick that aborts a teardown must still return the batched tick command —
// otherwise the poll loop stops scheduling itself and the pane freezes.
func TestAbortingTickStillReturnsTickCommand(t *testing.T) {
	now := time.Now()
	m := teardownTestModel()
	m.teardown = teardownSent
	m.teardownAt = now.Add(-teardownSubmitTimeout - time.Second)
	_, cmd := m.Update(tickMsg(now))
	if cmd == nil {
		t.Fatal("no command returned from an aborting tick; the poll loop would stall")
	}
}

func TestTeardownChipInStateLine(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownReady
	if got := renderStateLine(m, time.Now()); !strings.Contains(got, "⏻ press x to tear down") {
		t.Errorf("state line missing teardown chip:\n%s", got)
	}
}

func TestTeardownChipInStatusbar(t *testing.T) {
	m := teardownTestModel()
	m.height = 1
	m.teardown = teardownSent
	if got := renderStatusbar(m, time.Now(), ""); !strings.Contains(got, "⏻ wrapping up…") {
		t.Errorf("statusbar missing teardown chip:\n%s", got)
	}
}

// Both chips compete for one slot; the transient, actionable one wins.
func TestTeardownChipBeatsPinned(t *testing.T) {
	m := teardownTestModel()
	m.tabPinned = true
	m.teardown = teardownReady
	got := renderStateLine(m, time.Now())
	if !strings.Contains(got, "⏻ press x to tear down") {
		t.Errorf("teardown chip missing:\n%s", got)
	}
	if strings.Contains(got, "⬚ pinned") {
		t.Errorf("pin chip should have yielded to the teardown chip:\n%s", got)
	}
}

// With no teardown running the pin renders exactly as before.
func TestPinnedChipUnaffectedWhenIdle(t *testing.T) {
	m := teardownTestModel()
	m.tabPinned = true
	got := renderStateLine(m, time.Now())
	if !strings.Contains(got, "⬚ pinned") {
		t.Errorf("pin chip missing when no teardown is armed:\n%s", got)
	}
}

// The gate's worktree half must target the SESSION's directory, not the head's.
//
// bin/claudemux starts the head pane with `-c "$work_dir"` (the main checkout)
// and `claude --worktree` chdirs itself into .claude/worktrees/<name>. So for
// every session launched with -w / launch.auto_worktree / `worktree: true`, the
// head's own cwd is not a worktree — and arming off it would silently reduce
// the gate to "the turn looks ended", never checking the evidence that the
// wrap-up actually succeeded.
func TestTeardownArmsAgainstSessionCwdNotHeadCwd(t *testing.T) {
	m := teardownTestModel()
	m.workDir = "/tmp/repo" // where the head was launched: the main checkout
	m.inWorktree = false
	m.sessionCwd = "/tmp/repo/.claude/worktrees/floating-harp" // where claude went
	m.teardownWorkDir = ""
	m.teardownInWorktree = false

	got, _ := m.teardownKey()

	if got.teardownWorkDir != "/tmp/repo/.claude/worktrees/floating-harp" {
		t.Errorf("teardownWorkDir = %q, want the session's cwd", got.teardownWorkDir)
	}
	if !got.teardownInWorktree {
		t.Error("teardownInWorktree = false for a session working in a worktree; " +
			"the gate would degrade to turn-end alone and never verify the wrap-up")
	}
}

// Before the first main-session event is read there is no sessionCwd, so the
// head's own launch directory is the best available target — and it is exactly
// right for a session that never entered a worktree.
func TestTeardownFallsBackToHeadCwd(t *testing.T) {
	m := teardownTestModel()
	m.workDir = "/tmp/repo"
	m.inWorktree = false
	m.sessionCwd = ""
	m.teardownWorkDir = ""
	m.teardownInWorktree = true

	got, _ := m.teardownKey()

	if got.teardownWorkDir != "/tmp/repo" {
		t.Errorf("teardownWorkDir = %q, want the startup capture %q", got.teardownWorkDir, "/tmp/repo")
	}
	if got.teardownInWorktree {
		t.Error("teardownInWorktree = true for a non-worktree launch directory")
	}
}

// The gate must not open against a turn-end reading that predates the wrap-up.
// m.state.Kind is only as fresh as the last dataMsg, so a probe returning a
// second after arming can see the StateIdle captured before the command was
// typed. teardownSubmitted is the evidence that the wrap-up actually reached
// claude, and the gate waits for it.
func TestTeardownGateStaysShutUntilSubmitted(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownSent
	m.teardownSubmitted = false
	m.teardownProbing = true
	m.state = State{Kind: StateIdle} // stale: captured before /done was typed

	next, _ := m.Update(teardownProbeMsg{worktreeGone: true})
	got := next.(model)

	if got.teardown != teardownSent {
		t.Errorf("phase = %v, want teardownSent; the gate opened on a pre-wrap-up "+
			"state reading and invited the irreversible second press", got.teardown)
	}
	if got.teardownBlocked {
		t.Error("blocked = true; nothing has failed yet, the wrap-up just hasn't landed")
	}
}

// An empty teardown.command is the documented case where nothing is typed:
// teardownKey marks it submitted at arm time, so requiring submission cannot
// wedge it.
func TestTeardownGateOpensWithEmptyCommand(t *testing.T) {
	m := teardownTestModel()
	m.teardownCmdText = ""
	m, _ = m.teardownKey()
	if !m.teardownSubmitted {
		t.Fatal("precondition: empty command should arm as already submitted")
	}

	next, _ := m.Update(teardownProbeMsg{worktreeGone: true})
	if got := next.(model); got.teardown != teardownReady {
		t.Errorf("phase = %v, want teardownReady", got.teardown)
	}
}

func TestTeardownProbeDue(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		blocked bool
		last    time.Duration // how long ago the last probe was issued
		want    bool
	}{
		{"not blocked: every tick", false, 0, true},
		{"blocked, just probed", true, 0, false},
		{"blocked, a second ago", true, time.Second, false},
		{"blocked, interval elapsed", true, teardownBlockedProbeInterval, true},
		{"blocked, never probed", true, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := teardownTestModel()
			m.teardownBlocked = tt.blocked
			m.teardownProbeAt = now.Add(-tt.last)
			if got := m.teardownProbeDue(now); got != tt.want {
				t.Errorf("teardownProbeDue = %v, want %v", got, tt.want)
			}
		})
	}
}

// A blocked teardown is a resting state a user may leave up all night. Probing
// it at 1 Hz forks `git worktree list` ~30k times overnight to re-answer a
// question that only changes when the human does something.
func TestBlockedTeardownBacksOffProbing(t *testing.T) {
	now := time.Now()

	m := teardownTestModel()
	m.teardown = teardownSent
	m.teardownSubmitted = true
	m.teardownBlocked = true
	m.teardownProbeAt = now.Add(-time.Second)

	next, _ := m.Update(tickMsg(now))
	if next.(model).teardownProbing {
		t.Error("a blocked teardown probed again one second later")
	}

	// It must still recover: once the interval is up the gate is re-sampled,
	// so a worktree that finally disappears is noticed.
	m.teardownProbeAt = now.Add(-teardownBlockedProbeInterval - time.Second)
	next, _ = m.Update(tickMsg(now))
	if !next.(model).teardownProbing {
		t.Error("a blocked teardown stopped probing entirely")
	}
}

// An unblocked teardown keeps the responsive 1 Hz cadence — the normal path
// must open the gate promptly.
func TestUnblockedTeardownProbesEveryTick(t *testing.T) {
	now := time.Now()
	m := teardownTestModel()
	m.teardown = teardownSent
	m.teardownSubmitted = true
	m.teardownProbeAt = now.Add(-time.Second)

	next, _ := m.Update(tickMsg(now))
	if !next.(model).teardownProbing {
		t.Error("no probe issued on the tick after the previous one")
	}
}

// A session rotation must abort an armed teardown rather than let it drift.
// switchSession recomputes lastTyped from the NEW session's transcript; the
// next tick would read that change as proof the wrap-up submitted, removing the
// only bound on how long teardownSent can sit armed.
func TestSwitchSessionAbortsArmedTeardown(t *testing.T) {
	dir := t.TempDir()
	newp := filepath.Join(dir, "new-sess.jsonl")
	if err := os.WriteFile(newp, []byte(`{"type":"user","timestamp":"2026-07-31T10:00:00Z","message":{"role":"user","content":"a different prompt"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	m := teardownTestModel()
	m.jsonlPath = filepath.Join(dir, "old-sess.jsonl")
	m.teardown = teardownSent
	m.teardownPrompt = "the prompt the wrap-up was armed against"
	m.lastTyped = m.teardownPrompt

	m.switchSession(newp, now)

	if m.teardown != teardownIdle {
		t.Errorf("phase = %v, want teardownIdle after a rotation", m.teardown)
	}
	if m.teardownNote != "session rotated" {
		t.Errorf("note = %q, want %q", m.teardownNote, "session rotated")
	}
	if m.teardownSubmitted {
		t.Error("submitted = true; the rotation certified a submission that never happened")
	}
}

// The rotation abort only fires when something is armed — an idle head must
// not start showing a note every time the session rotates.
func TestSwitchSessionLeavesIdleTeardownAlone(t *testing.T) {
	dir := t.TempDir()
	newp := filepath.Join(dir, "new-sess.jsonl")
	if err := os.WriteFile(newp, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := teardownTestModel()
	m.jsonlPath = filepath.Join(dir, "old-sess.jsonl")

	m.switchSession(newp, time.Now())

	if m.teardownNote != "" {
		t.Errorf("note = %q, want empty", m.teardownNote)
	}
}

func TestTabLabelPrefersWorktreeUntilHaikuWins(t *testing.T) {
	tests := []struct {
		name        string
		worktreeTab string
		haikuTab    string
		haikuWins   bool
		want        string
	}{
		{"worktree name wins before any correction",
			"rename worktrees on topic", "worktree naming", false, "rename worktrees on topic"},
		{"haiku wins after a topic change",
			"rename worktrees on topic", "worktree naming", true, "worktree naming"},
		{"no worktree observed falls back to haiku",
			"", "worktree naming", false, "worktree naming"},
		{"haiku empty falls back to the worktree name",
			"rename worktrees on topic", "", true, "rename worktrees on topic"},
		{"neither yields empty",
			"", "", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tabLabel(tt.worktreeTab, tt.haikuTab, tt.haikuWins); got != tt.want {
				t.Errorf("tabLabel(%q, %q, %v) = %q, want %q",
					tt.worktreeTab, tt.haikuTab, tt.haikuWins, got, tt.want)
			}
		})
	}
}

func TestWorktreeTabSetOnlyOnTransition(t *testing.T) {
	// A session that was already in a worktree when the head started must not
	// adopt its name — it is a random one from before this feature.
	m := &model{sessionCwd: "/repo/.claude/worktrees/lovely-wandering-lovelace"}
	m.observeWorktreeTransition()
	if m.worktreeTab != "" {
		t.Errorf("adopted a pre-existing worktree name: %q", m.worktreeTab)
	}

	// A session observed OUTSIDE a worktree and then inside one did the
	// transition, so its name is task-derived and may be adopted.
	m2 := &model{sessionCwd: "/repo"}
	m2.observeWorktreeTransition()
	m2.sessionCwd = "/repo/.claude/worktrees/rename-worktrees-on-topic"
	m2.observeWorktreeTransition()
	if got, want := m2.worktreeTab, "rename worktrees on topic"; got != want {
		t.Errorf("worktreeTab = %q, want %q", got, want)
	}
}

func TestHaikuWinsOnlyOnTopicChange(t *testing.T) {
	m := &model{}
	// First summary establishes a topic — not a change.
	m.noteTopic("naming worktrees after their work")
	if m.tabHaikuWins {
		t.Error("first topic counted as a correction")
	}
	// Same topic again — still not a change.
	m.noteTopic("naming worktrees after their work")
	if m.tabHaikuWins {
		t.Error("an unchanged topic counted as a correction")
	}
	// A genuinely different topic is the correction signal.
	m.noteTopic("debugging the teardown gate")
	if !m.tabHaikuWins {
		t.Error("a changed topic did not hand the tab to haiku")
	}
	// The latch is one-way.
	m.noteTopic("naming worktrees after their work")
	if !m.tabHaikuWins {
		t.Error("latch reverted")
	}
}

func TestWorktreeChipTextWarnsWhenNoneAppeared(t *testing.T) {
	tests := []struct {
		name                      string
		chip                      string
		pending, ended, sawPrompt bool
		want                      string
	}{
		{"warns once the first turn ends with no worktree",
			"", true, true, true, "⚠ no worktree"},
		{"silent before a prompt",
			"", true, true, false, ""},
		{"silent mid-turn",
			"", true, false, true, ""},
		{"silent when the session was never marked",
			"", false, true, true, ""},
		{"a worktree that appeared wins over the warning",
			"rename-worktrees-on-topic", true, true, true, "rename-worktrees-on-topic"},
		{"unmarked session with a worktree still shows it",
			"some-worktree", false, true, true, "some-worktree"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := worktreeChipText(tt.chip, tt.pending, tt.ended, tt.sawPrompt)
			if got != tt.want {
				t.Errorf("worktreeChipText(%q, %v, %v, %v) = %q, want %q",
					tt.chip, tt.pending, tt.ended, tt.sawPrompt, got, tt.want)
			}
		})
	}
}

// worktreeChipText (above) is well tested as a pure function, but the wiring
// that feeds it — m.worktreePending reading CLAUDEMUX_WORKTREE_PENDING off
// the real environment in newModel, composed with teardownTurnEnded and
// m.firstPrompt in worktreeChip — is the entire mitigation for the risk this
// design accepts: a session marked for a worktree whose first turn ends
// outside one. Exercise it through newModel, not a hand-built &model{}, so a
// regression in the env lookup itself (not just in worktreeChipText's logic)
// would be caught.
func TestWorktreeChipWiredThroughEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")
	// A real user prompt followed by an assistant text reply: the last
	// conversation event is the assistant one with UserText set, which
	// classifyState reads as StateIdle — i.e. teardownTurnEnded == true, a
	// turn that has ended. Nothing here puts sessionCwd inside a worktree
	// (no "cwd" field, and the temp jsonl path itself doesn't carry the
	// ".claude/worktrees" marker worktreeName looks for), so observedWorktree
	// stays "" throughout.
	lines := `{"type":"user","timestamp":"2026-08-06T10:00:00Z","message":{"content":"rename the worktrees"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-08-06T10:00:05Z","message":{"model":"claude","content":[{"type":"text","text":"done"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	// Avoid newSummarizer touching the real, possibly 1Password-backed
	// CLAUDEMUX_ENV FIFO — see TestNewModelSeedsSummarizingWithSummarizer.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDEMUX_ENV", filepath.Join(t.TempDir(), "absent"))

	t.Run("marked session whose first turn ended outside a worktree warns", func(t *testing.T) {
		t.Setenv("CLAUDEMUX_WORKTREE_PENDING", "1")
		m := newModel(defaultConfig(), path, "sess", false)
		if !m.worktreePending {
			t.Fatal("worktreePending = false, want true with CLAUDEMUX_WORKTREE_PENDING set")
		}
		if got, want := m.worktreeChip(), "⚠ no worktree"; got != want {
			t.Errorf("worktreeChip() = %q, want %q", got, want)
		}
	})

	t.Run("unmarked session shows no chip", func(t *testing.T) {
		t.Setenv("CLAUDEMUX_WORKTREE_PENDING", "")
		m := newModel(defaultConfig(), path, "sess", false)
		if m.worktreePending {
			t.Fatal("worktreePending = true, want false with CLAUDEMUX_WORKTREE_PENDING unset")
		}
		if got := m.worktreeChip(); got != "" {
			t.Errorf("worktreeChip() = %q, want empty when the session was never marked", got)
		}
	})
}

// observeWorktreeTransition (TestWorktreeTabSetOnlyOnTransition) is exercised
// on a hand-built &model{} there. This covers the same gate through newModel
// with a seeded event history instead, so the wiring — not just the pure
// transition logic — is under test: a session already inside a worktree at
// startup (e.g. the head was restarted mid-session) must not adopt its name,
// since that name is a leftover from before this feature and not
// task-derived.
func TestWorktreeTabNotAdoptedForPreExistingWorktreeThroughNewModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")
	line := `{"type":"user","timestamp":"2026-08-06T10:00:00Z","cwd":"/repo/.claude/worktrees/lovely-wandering-lovelace","message":{"content":"go"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDEMUX_ENV", filepath.Join(t.TempDir(), "absent"))

	m := newModel(defaultConfig(), path, "sess", false)
	if m.sessionCwd != "/repo/.claude/worktrees/lovely-wandering-lovelace" {
		t.Fatalf("sessionCwd = %q, want the seeded cwd — the control for this test is broken", m.sessionCwd)
	}
	if m.worktreeTab != "" {
		t.Errorf("worktreeTab = %q, want empty: a worktree observed on the very first recompute must not be adopted as a transition", m.worktreeTab)
	}
}

// End to end through the model: a launch arriving on a poll, with the turn
// already ended, must publish Background rather than Idle.
func TestModelBackgroundStateFromPoll(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 10, 0, 0, time.UTC)
	m := model{bg: newBgTracker()}
	events := append(
		bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"),
		Event{Type: "assistant", Timestamp: "2026-08-11T10:01:00Z", UserText: "Kicked that off in the background."},
	)
	m.bg.observe(events, now)
	m.allEvents = events
	m.recomputeFromEvents(now)
	if m.state.Kind != StateBackground || m.state.BgCount != 1 {
		t.Errorf("state = %v count=%d, want StateBackground count=1", m.state.Kind, m.state.BgCount)
	}

	m.bg.observe([]Event{bgDoneEvent("aaa")}, now)
	m.recomputeFromEvents(now)
	if m.state.Kind != StateIdle {
		t.Errorf("state = %v, want StateIdle once the task finished", m.state.Kind)
	}
}

// A rotated session must not inherit the previous session's outstanding work.
func TestSwitchSessionResetsBackgroundWork(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	path := filepath.Join(dir, "new.jsonl")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	m := model{bg: newBgTracker()}
	m.bg.observe(bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"), now)
	m.switchSession(path, now)
	if n, _ := m.bg.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0: a rotated session starts clean", n)
	}
}

// bgSeedTranscript writes a minimal transcript whose tail is: a background
// shell launch, then an assistant text turn (so classifyState lands on Idle
// and only the tracker can upgrade it to Background).
func bgSeedTranscript(t *testing.T, dir, id string, at time.Time) string {
	t.Helper()
	ts := at.UTC().Format(time.RFC3339)
	use := "toolu_" + id
	lines := []string{
		bgToolUseLine(t, use, "Bash", ts, map[string]any{
			"command": "sleep 300", "run_in_background": true,
		}),
		bgResultLine(t, use, "Command running in background with ID: "+id, ts, bgShellResult(id)),
		bgMarshalLine(t, map[string]any{
			"type": "assistant", "timestamp": ts,
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "launched, waiting"},
			}},
		}),
	}
	path := filepath.Join(dir, "seedsess.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A launch already on disk when the head starts must count: heads restart
// and sessions rotate while work is out, and an unseeded tracker calls the
// session Idle — sending the conductor into a busy session.
func TestNewModelSeedsBgTracker(t *testing.T) {
	path := bgSeedTranscript(t, t.TempDir(), "seedaaa", time.Now().Add(-2*time.Minute))
	m := newModel(Config{}, path, "seedsess", false)
	if m.state.Kind != StateBackground {
		t.Errorf("state = %v (%s), want StateBackground: seeded launches must count",
			m.state.Kind, m.state.Label())
	}
}

func TestSwitchSessionSeedsBgTracker(t *testing.T) {
	dir := t.TempDir()
	first := bgSeedTranscript(t, dir, "seedbbb", time.Now().Add(-2*time.Minute))
	m := newModel(Config{}, first, "seedsess", true)
	next := filepath.Join(dir, "rotated.jsonl")
	// Reuse the same fixture shape under the rotated path.
	data, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(next, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = m.switchSession(next, time.Now())
	if m.state.Kind != StateBackground {
		t.Errorf("state after rotation = %v, want StateBackground", m.state.Kind)
	}
}

// The tracker must consult the ROTATED session's subagents dir, not the old
// one's — otherwise agent liveness stats the wrong directory forever.
func TestSwitchSessionRetargetsSubagentsDir(t *testing.T) {
	dir := t.TempDir()
	first := bgSeedTranscript(t, dir, "seedccc", time.Now().Add(-2*time.Minute))
	m := newModel(Config{}, first, "seedsess", true)
	next := filepath.Join(dir, "rotated.jsonl")
	if err := os.WriteFile(next, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = m.switchSession(next, time.Now())
	if want := subagentsDirFor(next); m.bg.subagentsDir != want {
		t.Errorf("subagentsDir = %q, want %q", m.bg.subagentsDir, want)
	}
}

// The trigger wiring, not just the predicate: TestModelBackgroundStateFromPoll
// calls bg.observe and recomputeFromEvents directly, so it would still pass if
// the dataMsg case fed observe the wrong slice or the observe call moved after
// the ring trim. This drives the real entry point instead.
func TestUpdateDataMsgFeedsBackgroundTracker(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 10, 0, 0, time.UTC)
	m := model{bg: newBgTracker()}

	got, _ := m.Update(dataMsg{
		time: now,
		newEvents: append(
			bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"),
			Event{Type: "assistant", Timestamp: "2026-08-11T10:01:00Z", UserText: "Kicked that off in the background."},
		),
		rateLimitErr: errors.New("no rate limits in this test"),
	})
	next := got.(model)
	if next.state.Kind != StateBackground || next.state.BgCount != 1 {
		t.Fatalf("state = %v count=%d, want StateBackground count=1", next.state.Kind, next.state.BgCount)
	}

	got, _ = next.Update(dataMsg{
		time:         now,
		newEvents:    []Event{bgDoneEvent("aaa")},
		rateLimitErr: errors.New("no rate limits in this test"),
	})
	next = got.(model)
	if next.state.Kind != StateIdle {
		t.Errorf("state = %v, want StateIdle once the task finished", next.state.Kind)
	}
}

// The degradation ladder from the spec, rung by rung. The worktree never
// vanishes while one exists — it falls back to its bare glyph, which still
// says "you are in a worktree". The branch keeps its name or goes, because a
// bare branch glyph says nothing: a session is always on some branch.
func TestChipSegmentLadder(t *testing.T) {
	const b, w = "lobby-preview", "align-context-meters"
	tests := []struct {
		name  string
		avail int
		want  string
	}{
		{"both in full", 40, "⎇ lobby-preview · ⌂ align-context-meters"},
		{"worktree name truncates", 30, "⎇ lobby-preview · ⌂ align-con…"},
		// Rung 2 boundary: room = avail(21) - bw(15) - sepW(3) = 3, which is
		// only enough for the worktree glyph, space, and ellipsis — no real
		// character of the name. Must fall through to Rung 3's bare glyph
		// rather than emit "⎇ lobby-preview · ⌂ …".
		{"worktree truncation boundary falls through to bare glyph", 21, "⎇ lobby-preview · ⌂"},
		{"worktree down to its glyph", 19, "⎇ lobby-preview · ⌂"},
		{"branch name truncates", 14, "⎇ lobby-p… · ⌂"},
		// Rung 4 boundary: room = avail(7) - sepW(3) - bareW(1) = 3, which is
		// only enough for the branch glyph, space, and ellipsis — no real
		// character of the branch name. Must fall through to Rung 5's
		// worktree glyph alone rather than emit "⎇ … · ⌂".
		{"branch truncation boundary falls through to worktree glyph alone", 7, "⌂"},
		{"worktree glyph alone", 4, "⌂"},
		{"nothing fits", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chipSegment(b, w, tt.avail)
			if got != tt.want {
				t.Errorf("chipSegment(avail=%d) = %q, want %q", tt.avail, got, tt.want)
			}
			if lipgloss.Width(got) > tt.avail {
				t.Errorf("chipSegment(avail=%d) measures %d cells", tt.avail, lipgloss.Width(got))
			}
		})
	}
}

func TestChipSegmentSingleChips(t *testing.T) {
	// No worktree: the branch stands alone and truncates like any other chip.
	if got := chipSegment("main", "", 40); got != "⎇ main" {
		t.Errorf("branch only = %q, want ⎇ main", got)
	}
	// No branch (not a git directory) but inside a worktree.
	if got := chipSegment("", "align-context-meters", 40); got != "⌂ align-context-meters" {
		t.Errorf("worktree only = %q, want the worktree chip", got)
	}
	if got := chipSegment("", "", 40); got != "" {
		t.Errorf("neither = %q, want empty", got)
	}
}

// The single-chip paths degrade too, and at widths a real pane reaches: a
// plain checkout (branch, no worktree) narrows into the branch-only ladder,
// and EVERY session takes the worktree-only path on its first poll after a
// rotation, because switchSession clears sessionBranch. Exercising these only
// at avail=40, where nothing degrades, is why a nameless "⎇…" shipped.
//
// The floor differs by chip, for the same reason the two-chip ladder's does:
// a bare "⌂" still says "you are in a worktree", a bare "⎇" says nothing. A
// glyph plus an ellipsis is never right — it claims elided content that never
// had room to exist.
func TestChipSegmentSingleChipLadders(t *testing.T) {
	t.Run("branch only", func(t *testing.T) {
		const b = "lobby-preview" // "⎇ lobby-preview" is 15 cells
		tests := []struct {
			avail int
			want  string
		}{
			{40, "⎇ lobby-preview"},
			{15, "⎇ lobby-preview"}, // exact fit, no ellipsis
			{14, "⎇ lobby-previ…"},
			{5, "⎇ lo…"},
			{4, "⎇ l…"}, // last width holding one real character
			{3, ""},     // "⎇ …" would be a nameless ellipsis
			{2, ""},     // "⎇…" likewise
			{1, ""},     // no bare branch glyph: it carries no information
			{0, ""},
		}
		for _, tt := range tests {
			got := chipSegment(b, "", tt.avail)
			if got != tt.want {
				t.Errorf("chipSegment(branch only, avail=%d) = %q, want %q", tt.avail, got, tt.want)
			}
			if lipgloss.Width(got) > tt.avail {
				t.Errorf("chipSegment(branch only, avail=%d) = %q measures %d cells", tt.avail, got, lipgloss.Width(got))
			}
		}
	})

	t.Run("worktree only", func(t *testing.T) {
		const w = "align-context-meters" // "⌂ align-context-meters" is 22 cells
		tests := []struct {
			avail int
			want  string
		}{
			{40, "⌂ align-context-meters"},
			{22, "⌂ align-context-meters"},
			{21, "⌂ align-context-mete…"},
			{5, "⌂ al…"},
			{4, "⌂ a…"}, // last width holding one real character
			{3, "⌂"},    // "⌂ …" degrades to the honest bare glyph
			{2, "⌂"},    // the spare cell is deliberate; "⌂…" is the forbidden form
			{1, "⌂"},
			{0, ""},
		}
		for _, tt := range tests {
			got := chipSegment("", w, tt.avail)
			if got != tt.want {
				t.Errorf("chipSegment(worktree only, avail=%d) = %q, want %q", tt.avail, got, tt.want)
			}
			if lipgloss.Width(got) > tt.avail {
				t.Errorf("chipSegment(worktree only, avail=%d) = %q measures %d cells", tt.avail, got, lipgloss.Width(got))
			}
		}
	})
}

// Wide runes measure two cells each, so "one more cell of room" does not mean
// "one more character fits". Line width is NOT the property that breaks here —
// it holds even with the rung guards computing survival from the remaining
// room by integer arithmetic — so asserting only on width passes with the bug
// fully present at exactly these inputs. Assert the rendered string instead,
// rung by rung, and forbid every nameless-ellipsis form outright.
func TestChipSegmentWideRunes(t *testing.T) {
	// 12 wide runes each: the branch chip and the worktree chip both measure
	// 26 cells (2 for "glyph + space" + 24 for the name).
	branch, worktree := strings.Repeat("囲", 12), strings.Repeat("宽", 12)

	forbidden := []string{"⎇ …", "⌂ …", "⎇…", "⌂…"}
	check := func(t *testing.T, label string, avail int, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s(avail=%d) = %q, want %q", label, avail, got, want)
		}
		if lipgloss.Width(got) > avail {
			t.Errorf("%s(avail=%d) = %q measures %d cells", label, avail, got, lipgloss.Width(got))
		}
		for _, f := range forbidden {
			if strings.Contains(got, f) {
				t.Errorf("%s(avail=%d) = %q contains the nameless-ellipsis form %q", label, avail, got, f)
			}
		}
	}

	t.Run("both chips", func(t *testing.T) {
		tests := []struct {
			avail int
			want  string
		}{
			// Rung 2: the worktree name truncates. A two-cell first character
			// needs one cell more than "glyph + space + one letter + ellipsis".
			{40, "⎇ 囲囲囲囲囲囲囲囲囲囲囲囲 · ⌂ 宽宽宽宽…"},
			{30, "⎇ 囲囲囲囲囲囲囲囲囲囲囲囲 · ⌂"}, // Rung 3
			{20, "⎇ 囲囲囲囲囲囲… · ⌂"},      // Rung 4
			{10, "⎇ 囲… · ⌂"},
			// room is 4 here: enough for "glyph, space, ellipsis" but not for a
			// two-cell character, so Rung 4 must yield to Rung 5.
			{8, "⌂"},
			{6, "⌂"},
			{1, "⌂"},
			{0, ""},
		}
		for _, tt := range tests {
			check(t, "chipSegment", tt.avail, chipSegment(branch, worktree, tt.avail), tt.want)
		}
	})

	// Rung 2 in isolation: an ASCII branch short enough that the worktree half
	// is the one under pressure. room is 4 at avail 13 — "⌂ " plus an ellipsis
	// with a cell to spare, but not enough for a two-cell name character — so
	// the worktree must fall to its bare glyph.
	t.Run("wide worktree beside a narrow branch", func(t *testing.T) {
		for _, tt := range []struct {
			avail int
			want  string
		}{
			{20, "⎇ main · ⌂ 宽宽宽宽…"},
			{14, "⎇ main · ⌂ 宽…"},
			{13, "⎇ main · ⌂"},
			{10, "⎇ main · ⌂"},
		} {
			check(t, "chipSegment", tt.avail, chipSegment("main", worktree, tt.avail), tt.want)
		}
	})

	t.Run("branch only", func(t *testing.T) {
		for _, tt := range []struct {
			avail int
			want  string
		}{
			{7, "⎇ 囲囲…"},
			{5, "⎇ 囲…"},
			{4, ""}, // room for "⎇ …" only — no branch chip at all
			{3, ""},
		} {
			check(t, "chipSegment(branch only)", tt.avail, chipSegment(branch, "", tt.avail), tt.want)
		}
	})

	t.Run("worktree only", func(t *testing.T) {
		for _, tt := range []struct {
			avail int
			want  string
		}{
			{7, "⌂ 宽宽…"},
			{5, "⌂ 宽…"},
			{4, "⌂"}, // room for "⌂ …" only — degrade to the bare glyph
			{3, "⌂"},
		} {
			check(t, "chipSegment(worktree only)", tt.avail, chipSegment("", worktree, tt.avail), tt.want)
		}
	})
}

// The point of the feature: branch and worktree are different facts and the
// pane shows both.
func TestStateLineShowsBothChips(t *testing.T) {
	m := model{ready: true, width: 120, height: 2,
		state: State{Kind: StateIdle}, sessionBranch: "lobby-preview",
		sessionCwd: "/tmp/repo/.claude/worktrees/align-context-meters"}
	line := ansi.Strip(renderStateLine(m, time.Now()))
	for _, want := range []string{"⎇ lobby-preview", "⌂ align-context-meters"} {
		if !strings.Contains(line, want) {
			t.Errorf("state line missing %q: %q", want, line)
		}
	}
}

// End-to-end, across every pane width: no line ever exceeds the pane, and no
// line ever shows a glyph followed by an ellipsis with no name behind it.
//
// The nameless form was reachable in pure ASCII from ordinary fixtures — a
// plain checkout at width 27/28, and the same session's first poll after a
// rotation, when switchSession has cleared sessionBranch and only the worktree
// chip remains. Sweeping the widths is what catches the handful of columns
// where the chip slot is 2 or 3 cells wide.
func TestStateLineNeverRendersNamelessChip(t *testing.T) {
	now := time.Now()
	fixtures := map[string]model{
		"plain checkout": {
			state:     State{Kind: StateIdle, Since: now.Add(-30 * time.Second)},
			modelName: "claude-opus-5", sessionBranch: "lobby-preview",
			jsonlPath: "/proj/abc.jsonl",
		},
		"worktree, branch not yet polled": {
			state:      State{Kind: StateIdle, Since: now.Add(-30 * time.Second)},
			modelName:  "claude-opus-5",
			sessionCwd: "/tmp/repo/.claude/worktrees/align-context-meters",
		},
		"branch and worktree": {
			state:     State{Kind: StateIdle, Since: now.Add(-30 * time.Second)},
			modelName: "claude-opus-5", sessionBranch: "lobby-preview",
			sessionCwd: "/tmp/repo/.claude/worktrees/align-context-meters",
		},
		"no worktree warning": {
			state:     State{Kind: StateIdle, Since: now.Add(-30 * time.Second)},
			modelName: "claude-opus-5", sessionBranch: "lobby-preview",
			worktreePending: true, firstPrompt: "do the thing",
			jsonlPath: "/proj/abc.jsonl",
		},
		"wide-rune names": {
			state:     State{Kind: StateIdle, Since: now.Add(-30 * time.Second)},
			modelName: "claude-opus-5", sessionBranch: strings.Repeat("囲", 12),
			sessionCwd: "/tmp/repo/.claude/worktrees/" + strings.Repeat("宽", 12),
		},
	}
	forbidden := []string{"⎇ …", "⌂ …", "⎇…", "⌂…"}
	for name, base := range fixtures {
		t.Run(name, func(t *testing.T) {
			for width := 1; width <= 200; width++ {
				m := base
				m.width = width
				got := ansi.Strip(renderStateLine(m, now))
				if w := lipgloss.Width(got); w != width {
					t.Fatalf("width=%d: renderStateLine = %q measures %d cells", width, got, w)
				}
				for _, f := range forbidden {
					if strings.Contains(got, f) {
						t.Fatalf("width=%d: renderStateLine = %q contains the nameless-ellipsis form %q", width, got, f)
					}
				}
			}
		})
	}
}

// The warning and the branch occupy different slots, so a marked session that
// never got its worktree still says which branch it is sitting on.
func TestStateLineWarningKeepsBranch(t *testing.T) {
	m := model{ready: true, width: 120, height: 2, state: State{Kind: StateIdle},
		sessionBranch: "main", worktreePending: true, firstPrompt: "do a thing"}
	line := ansi.Strip(renderStateLine(m, time.Now()))
	if !strings.Contains(line, noWorktreeWarning) {
		t.Errorf("warning missing: %q", line)
	}
	if !strings.Contains(line, "⎇ main") {
		t.Errorf("branch missing alongside the warning: %q", line)
	}
}

// The packed single-line layout gets its chip segment from two independent
// sources — m.sessionBranch and the chip argument — same as renderStateLine.
// Names that comfortably fit packedChipCells must show both, not just one.
func TestStatusbarShowsBothChips(t *testing.T) {
	m := model{ready: true, width: 120, height: 1, state: State{Kind: StateIdle},
		sessionBranch: "main"}
	line := ansi.Strip(renderStatusbar(m, time.Now(), "feature-branch"))
	for _, want := range []string{"⎇ main", "⌂ feature-branch"} {
		if !strings.Contains(line, want) {
			t.Errorf("statusbar missing %q: %q", want, line)
		}
	}
}

// Neither layout may ever emit a line wider than the pane, at any width, with
// wide runes in either name.
func TestStatusLinesNeverExceedWidth(t *testing.T) {
	for _, width := range []int{10, 20, 40, 80, 120} {
		m := model{ready: true, width: width, height: 2, state: State{Kind: StateIdle},
			modelName: "claude-opus-5", sessionBranch: strings.Repeat("囲", 12),
			sessionCwd: "/tmp/repo/.claude/worktrees/" + strings.Repeat("宽", 12)}
		if got := lipgloss.Width(renderStateLine(m, time.Now())); got > width {
			t.Errorf("renderStateLine(width=%d) measures %d", width, got)
		}
		if got := lipgloss.Width(renderStatusbar(m, time.Now(), m.worktreeChip())); got > width {
			t.Errorf("renderStatusbar(width=%d) measures %d", width, got)
		}
	}
}

// rateModelForTest builds a model with live rate-limit data and enough
// burn-rate samples to produce the "empty in X" ETA, mirroring swRateModel in
// swmeters_test.go.
func rateModelForTest(now time.Time, width int) model {
	return model{
		width: width, height: 40, rateOK: true,
		rateLimits: RateLimits{
			FiveHour: Window{UsedPercent: 20, ResetsAt: now.Add(5 * time.Hour)},
			SevenDay: Window{UsedPercent: 30, ResetsAt: now.Add(72 * time.Hour)},
		},
		pctSamples: []pctSample{
			{at: now.Add(-10 * time.Minute), pct: 10},
			{at: now, pct: 20},
		},
	}
}

// Model rows sit after wk and before the eta, because callers drop from the
// END of the slice and the required drop order is eta → models → wk → 5h.
func TestRateGaugesOrdersModelRowsAfterWeek(t *testing.T) {
	now := time.Now()
	rl := RateLimits{
		FiveHour: Window{UsedPercent: 20, ResetsAt: now.Add(5 * time.Hour)},
		SevenDay: Window{UsedPercent: 30, ResetsAt: now.Add(72 * time.Hour)},
	}
	models := []ModelWindow{{Name: "Fable", UsedPercent: 26, ResetsAt: now.Add(72 * time.Hour)}}
	samples := []pctSample{{at: now.Add(-10 * time.Minute), pct: 10}, {at: now, pct: 20}}

	gs := rateGauges(rl, models, samples, now, defaultBarW)
	if len(gs.parts) != 5 {
		t.Fatalf("parts = %q, want 5h, burn, wk, fab, eta", gs.parts)
	}
	if !strings.Contains(gs.parts[0], "5h") {
		t.Errorf("parts[0] = %q, want the 5h gauge", gs.parts[0])
	}
	if !strings.Contains(gs.parts[1], "burn") {
		t.Errorf("parts[1] = %q, want the burn gauge", gs.parts[1])
	}
	if !strings.Contains(gs.parts[2], "wk") {
		t.Errorf("parts[2] = %q, want the wk gauge", gs.parts[2])
	}
	if !strings.Contains(gs.parts[3], "fable") || !strings.Contains(gs.parts[3], "26%") {
		t.Errorf("parts[3] = %q, want the Fable gauge at 26%%", gs.parts[3])
	}
	if !strings.Contains(gs.parts[4], "empty in") {
		t.Errorf("parts[4] = %q, want the eta", gs.parts[4])
	}
	// 5h, burn, wk and fable carry bars; the eta is plain text.
	if gs.barred != 4 {
		t.Errorf("barred = %d, want 4", gs.barred)
	}
}

// No model data — the overwhelmingly common case on Pro, and every case when
// the pull path is broken — must render exactly today's gauges.
func TestRateGaugesWithoutModelsUnchanged(t *testing.T) {
	now := time.Now()
	rl := RateLimits{
		FiveHour: Window{UsedPercent: 20, ResetsAt: now.Add(5 * time.Hour)},
		SevenDay: Window{UsedPercent: 30, ResetsAt: now.Add(72 * time.Hour)},
	}
	gs := rateGauges(rl, nil, nil, now, defaultBarW)
	if len(gs.parts) != 3 {
		t.Fatalf("parts = %q, want just 5h, burn and wk", gs.parts)
	}
	if gs.barred != 3 {
		t.Errorf("barred = %d, want 3", gs.barred)
	}
}

// A row whose percent we have but whose reset time we do not still renders —
// dropping the meter because one field is missing would be a worse outcome.
func TestRateGaugesModelWithoutResetTime(t *testing.T) {
	now := time.Now()
	rl := RateLimits{FiveHour: Window{UsedPercent: 1, ResetsAt: now.Add(time.Hour)}}
	gs := rateGauges(rl, []ModelWindow{{Name: "Fable", UsedPercent: 26}}, nil, now, defaultBarW)
	found := false
	for _, p := range gs.parts {
		if strings.Contains(p, "fable") && strings.Contains(p, "26%") {
			found = true
		}
	}
	if !found {
		t.Errorf("parts = %q, want a fable gauge despite the zero reset time", gs.parts)
	}
}

func TestModelMeterLabel(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		// Spelled out, not abbreviated: "fable" read as noise beside "5h" and
		// "wk" rather than as the name of a model.
		{"fable", "Fable", "fable"},
		{"opus", "Opus", "opus"},
		{"sonnet", "Sonnet", "sonnet"},
		{"single char", "X", "x"},
		// The label is whatever the server sent, so a pathological name must
		// not be allowed to eat the meter line.
		{"absurdly long", "Supercalifragilistic", "supercalifra"},
		// Counting runes, not bytes, so a multi-byte name cannot be cut
		// mid-rune into mojibake.
		{"multi-byte", "日本語モデルの名前です長い", "日本語モデルの名前です長"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelMeterLabel(tc.in); got != tc.want {
				t.Errorf("modelMeterLabel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The full drop order, exercised by shrinking the pane one column at a time.
func TestMetersLineDropOrderWithModels(t *testing.T) {
	now := time.Now()
	m := rateModelForTest(now, 200)
	m.modelWindows = []ModelWindow{{Name: "Fable", UsedPercent: 26, ResetsAt: now.Add(72 * time.Hour)}}

	full := renderMetersLine(m, now)
	for _, want := range []string{"5h", "wk", "fable", "empty in"} {
		if !strings.Contains(full, want) {
			t.Fatalf("full meters line = %q, want %q", full, want)
		}
	}

	sawEta, sawFab := false, false
	for w := lipgloss.Width(full); w >= 20; w-- {
		m.width = w
		line := renderMetersLine(m, now)
		if !sawEta && !strings.Contains(line, "empty in") {
			sawEta = true
			if !strings.Contains(line, "fable") {
				t.Fatalf("at width %d the eta dropped but fablele went with it: %q", w, line)
			}
		}
		if sawEta && !sawFab && !strings.Contains(line, "fable") {
			sawFab = true
			if !strings.Contains(line, "wk") {
				t.Fatalf("at width %d fable dropped but wk went with it: %q", w, line)
			}
		}
		if sawFab && strings.Contains(line, "fable") {
			t.Fatalf("at width %d fable came back after dropping: %q", w, line)
		}
	}
	if !sawEta || !sawFab {
		t.Fatalf("never observed the eta and fable drops (eta=%v fab=%v)", sawEta, sawFab)
	}
}

// A usageMsg carrying the empty result of a LOST single-flight race must not
// blank the rows this pane already has. refreshUsageCache's loser returns
// whatever is on disk — PlanUsage{} when no cache exists yet — so an
// unconditional assignment would wipe good rows every time two panes raced.
func TestUsageMsgEmptyLostRaceKeepsRows(t *testing.T) {
	now := time.Now()
	m := rateModelForTest(now, 200)
	m.modelWindows = []ModelWindow{{Name: "Fable", UsedPercent: 26, ResetsAt: now.Add(72 * time.Hour)}}

	got, cmd := m.Update(usageMsg{usage: PlanUsage{}})
	next := got.(model)
	if len(next.modelWindows) != 1 || next.modelWindows[0].Name != "Fable" {
		t.Errorf("modelWindows = %+v, want the existing Fable row kept", next.modelWindows)
	}
	if !next.usageQuietUntil.IsZero() {
		t.Error("the spawn loop went quiet on an empty lost-race result")
	}
	if cmd == nil {
		t.Error("cmd = nil, want the loop rescheduled after a lost race")
	}
}

// An error keeps the rows too, and keeps ticking.
func TestUsageMsgErrorKeepsRowsAndRetries(t *testing.T) {
	now := time.Now()
	m := rateModelForTest(now, 200)
	m.modelWindows = []ModelWindow{{Name: "Fable", UsedPercent: 26}}

	got, cmd := m.Update(usageMsg{err: errors.New("spawn failed")})
	if next := got.(model); len(next.modelWindows) != 1 {
		t.Errorf("modelWindows = %+v, want the existing row kept through an error", next.modelWindows)
	}
	if cmd == nil {
		t.Error("cmd = nil, want the loop rescheduled after an error")
	}
}

// A real answer replaces the rows, including a subscriber whose plan has no
// per-model windows at all — that is a genuine "no rows", not a lost race.
//
// And that answer must quiet the SPAWNS: a plan with no model-scoped window
// renders nothing no matter how often we ask, so asking every usageTTL for the
// life of the pane buys a Claude Code spawn and six SessionStart hooks per
// quarter hour in exchange for nothing at all. Quieted, never latched — the
// window expires (see TestUsageQuietWindowExpires) in case the plan gains one.
func TestUsageMsgRealAnswerReplacesRows(t *testing.T) {
	now := time.Now()
	m := rateModelForTest(now, 200)
	m.modelWindows = []ModelWindow{{Name: "Fable", UsedPercent: 26}}

	got, cmd := m.Update(usageMsg{usage: PlanUsage{Available: true, FetchedAt: now, Fetched: true}})
	next := got.(model)
	if len(next.modelWindows) != 0 {
		t.Errorf("modelWindows = %+v, want them cleared by a real empty answer", next.modelWindows)
	}
	if cmd == nil {
		t.Error("cmd = nil, want the loop still running")
	}
	if next.usageMaySpawn(now.Add(usageCheckInterval)) {
		t.Error("the next tick may still spawn after an answer with no model rows in it, want the spawns quieted")
	}
}

// An answer with rows keeps the loop fully live: nothing may quiet a pane that
// is actually rendering a model meter.
func TestUsageMsgRowsKeepSpawningLive(t *testing.T) {
	now := time.Now()
	m := rateModelForTest(now, 200)
	m.usageQuietUntil = now.Add(usageQuietPeriod) // as if a previous answer was empty

	got, _ := m.Update(usageMsg{usage: PlanUsage{
		Available: true, FetchedAt: now, Fetched: true,
		Models: []ModelWindow{{Name: "Fable", UsedPercent: 26}},
	}})
	next := got.(model)
	if !next.usageMaySpawn(now.Add(usageCheckInterval)) {
		t.Error("still quiet after an answer that DID carry a model row, want the window cleared")
	}
}

// rate_limits_available:false is a verdict on the credentials of the process
// that asked — this pane's ANTHROPIC_API_KEY or CLAUDE_CODE_USE_BEDROCK — not
// on the machine. The pane that fetched it stops spawning; its rows stay,
// because they describe the account and another pane fetched them.
func TestUsageMsgUnavailableQuietsOwnSpawnsAndKeepsRows(t *testing.T) {
	now := time.Now()
	m := rateModelForTest(now, 200)
	m.modelWindows = []ModelWindow{{Name: "Fable", UsedPercent: 26}}

	got, cmd := m.Update(usageMsg{usage: PlanUsage{Available: false, FetchedAt: now, Fetched: true}})
	next := got.(model)
	if next.usageMaySpawn(now.Add(usageCheckInterval)) {
		t.Error("the next tick may still spawn after our own unavailable answer, want the spawns quieted")
	}
	if len(next.modelWindows) != 1 {
		t.Errorf("modelWindows = %+v, want the rows another pane fetched kept — Available is about our credentials, not the account", next.modelWindows)
	}
	if cmd == nil {
		t.Error("cmd = nil, want the cache half of the loop still ticking so rows another pane fetches still arrive")
	}
}

// The same false arriving from the SHARED CACHE must change nothing: one
// pane's Bedrock credentials cannot be allowed to quiet every other pane on
// the machine. (Nothing writes such a cache any more — see
// TestRefreshUsageCacheKeepsSharedRowsWhenUnavailable — but one written by an
// older build survives an upgrade, and this is the pane that reads it.)
func TestUsageMsgUnavailableFromCacheDoesNotQuiet(t *testing.T) {
	now := time.Now()
	m := rateModelForTest(now, 200)

	got, cmd := m.Update(usageMsg{usage: PlanUsage{Available: false, FetchedAt: now}})
	next := got.(model)
	if !next.usageMaySpawn(now.Add(usageCheckInterval)) {
		t.Error("a cached unavailable verdict quieted this pane, want it ignored: it describes another pane's credentials")
	}
	if cmd == nil {
		t.Error("cmd = nil, want the loop still running")
	}
}

// The quiet window is a delay, never a latch: a `claude login` fixes a missing
// profile scope, and a plan can gain a model-scoped window, neither of which
// restarts this process.
func TestUsageQuietWindowExpires(t *testing.T) {
	now := time.Now()
	m := rateModelForTest(now, 200)

	got, _ := m.Update(usageMsg{usage: PlanUsage{Available: false, FetchedAt: now, Fetched: true}})
	next := got.(model)
	if next.usageMaySpawn(now.Add(usageQuietPeriod - time.Minute)) {
		t.Error("spawning resumed inside the quiet window")
	}
	if !next.usageMaySpawn(now.Add(usageQuietPeriod + time.Minute)) {
		t.Error("still quiet past the window: the pane can never recover without a restart")
	}
}

// The meters line must render identically with the model-window path absent,
// which is the state every Pro session and every broken pull path is in.
func TestMetersLineUnaffectedByBrokenUsagePath(t *testing.T) {
	now := time.Now()
	m := rateModelForTest(now, 120)
	want := renderMetersLine(m, now)

	m.modelWindows = nil
	m.usageQuietUntil = now.Add(usageQuietPeriod)
	m.usageCachePath = "/nonexistent/claudemux-usage.json"
	if got := renderMetersLine(m, now); got != want {
		t.Errorf("meters line changed when the usage path is broken:\n got %q\nwant %q", got, want)
	}
}

// A tick arriving at a quieted pane still reads the cache — that is how it
// picks up rows another pane fetched — but must not spawn.
func TestUsageTickWhileQuietReadsButDoesNotSpawn(t *testing.T) {
	now := time.Now()
	m := rateModelForTest(now, 200)
	m.usageQuietUntil = now.Add(usageQuietPeriod)
	m.usageCachePath = seedFreshUsageCache(t)
	_, cmd := m.Update(usageTickMsg{})
	if cmd == nil {
		t.Fatal("cmd = nil, want a quieted pane to keep reading the shared cache")
	}
	if got, ok := cmd().(usageMsg); !ok || len(got.usage.Models) != 1 {
		t.Errorf("quieted tick produced %#v, want the cached Fable row read back", got)
	}

	sw := swRateModel(now, 200)
	sw.usageQuietUntil = now.Add(usageQuietPeriod)
	sw.usageCachePath = m.usageCachePath
	if _, cmd := sw.Update(usageTickMsg{}); cmd == nil {
		t.Error("lobby cmd = nil, want its quieted tick to keep reading the shared cache too")
	}
}

// barCellCount counts the progress-bar cells in a rendered line.
func barCellCount(s string) int {
	n := 0
	for _, r := range s {
		if r == '█' || r == '░' {
			n++
		}
	}
	return n
}

// The widen step must divide the leftover columns by the ACTUAL number of
// bar-carrying gauges. With a model row the head has four (ctx, 5h, wk, fab),
// not the three it used to hardcode: dividing by three overshoots, the
// overflow guard then rejects the widened set wholesale, and the line is left
// short of the pane by the entire slack. So this asserts the line still fills
// the pane once a model row is present.
func TestMetersLineWidensEveryBarIncludingModelRows(t *testing.T) {
	now := time.Now()
	m := rateModelForTest(now, 0)
	m.contextPct = 42
	m.modelWindows = []ModelWindow{{Name: "Fable", UsedPercent: 26, ResetsAt: now.Add(72 * time.Hour)}}

	var prevBars int
	for _, w := range []int{140, 160, 180, 200} {
		m.width = w
		line := renderMetersLine(m, now)
		// The premise: every gauge survives at these widths, so four bars are
		// on screen and the eta is the only bar-less part.
		for _, want := range []string{"ctx", "5h", "wk", "fable", "empty in"} {
			if !strings.Contains(line, want) {
				t.Fatalf("at width %d the %q gauge is missing: %q", w, want, line)
			}
		}
		// Four bars share the slack, so integer division can strand at most
		// three columns.
		content := len(" ") + lipgloss.Width(strings.TrimRight(ansi.Strip(line), " "))
		if unspent := w - content; unspent > 3 {
			t.Errorf("at width %d, %d columns left unspent (want <= 3): the slack is being divided by the wrong bar count: %q", w, unspent, line)
		}
		if bars := barCellCount(line); bars <= prevBars {
			t.Errorf("at width %d, total bar cells = %d, want more than %d at the previous width", w, bars, prevBars)
		} else {
			prevBars = bars
		}
	}
}

// waitForInitMsg runs every command a panel's Init returned — recursing into
// tea.Batch, each command in its own goroutine — and reports the first message
// matching want. Goroutines only send on a buffered channel and never touch t,
// so any that outlive the test (the 1s tick, a poll) are harmless.
func waitForInitMsg(cmd tea.Cmd, d time.Duration, want func(tea.Msg) bool) (tea.Msg, bool) {
	if cmd == nil {
		return nil, false
	}
	msgs := make(chan tea.Msg, 32)
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		go func() {
			defer func() { _ = recover() }()
			msg := c()
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, sub := range batch {
					run(sub)
				}
				return
			}
			select {
			case msgs <- msg:
			default:
			}
		}()
	}
	run(cmd)

	deadline := time.After(d)
	for {
		select {
		case msg := <-msgs:
			if want(msg) {
				return msg, true
			}
		case <-deadline:
			return nil, false
		}
	}
}

func isUsageMsg(msg tea.Msg) bool {
	_, ok := msg.(usageMsg)
	return ok
}

// seedFreshUsageCache writes a cache young enough that usageCmd serves it
// straight from disk — the loop is exercised end to end without spawning
// anything.
func seedFreshUsageCache(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := writeUsageCache(path, PlanUsage{
		Available: true,
		Models:    []ModelWindow{{Name: "Fable", UsedPercent: 26}},
		FetchedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return path
}

// The head must actually START the usage loop. Nothing else in the suite
// notices if usageCmd is dropped from Init — the meters would simply never
// grow a model row, on every pane, forever, with a green suite.
func TestHeadInitStartsUsageLoop(t *testing.T) {
	t.Setenv("TMUX_PANE", "") // keep pollData off tmux
	t.Setenv("PATH", "")      // and every other command out of a subprocess
	cachePath := seedFreshUsageCache(t)

	jsonl := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(jsonl, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// sessionID "" is waiting mode: Init then adds no summarize call.
	m := newModel(Config{}, jsonl, "", false)
	m.usageCachePath = cachePath

	msg, ok := waitForInitMsg(m.Init(), 3*time.Second, isUsageMsg)
	if !ok {
		t.Fatal("Init produced no usageMsg: the head never starts the usage loop, so per-model rows would never appear")
	}
	got := msg.(usageMsg)
	if got.err != nil {
		t.Fatalf("usageMsg.err = %v, want the seeded cache served", got.err)
	}
	if len(got.usage.Models) != 1 || got.usage.Models[0].Name != "Fable" {
		t.Errorf("usage.Models = %+v, want the seeded Fable row", got.usage.Models)
	}
}

// Same for the lobby: it renders the same gauges and must not be the one panel
// whose loop never starts.
func TestLobbyInitStartsUsageLoop(t *testing.T) {
	t.Setenv("PATH", "") // swPollCmd's tmux calls fail fast instead of running
	cachePath := seedFreshUsageCache(t)

	m := swModel{
		rateLimitsPath: filepath.Join(t.TempDir(), "rate-limits.json"),
		usageCachePath: cachePath,
	}

	msg, ok := waitForInitMsg(m.Init(), 3*time.Second, isUsageMsg)
	if !ok {
		t.Fatal("Init produced no usageMsg: the lobby never starts the usage loop, so per-model rows would never appear")
	}
	got := msg.(usageMsg)
	if got.err != nil {
		t.Fatalf("usageMsg.err = %v, want the seeded cache served", got.err)
	}
	if len(got.usage.Models) != 1 || got.usage.Models[0].Name != "Fable" {
		t.Errorf("usage.Models = %+v, want the seeded Fable row", got.usage.Models)
	}
}

// Gauge rendering is gated on the PUSH path (rateOK): with no 5h/wk data there
// is no meter line at all, so a model row appended to it is worth nothing. A
// user who kept their own statusline — exactly the case setStatusLine
// deliberately declines to take over — sits in that state permanently, and
// before this the pull loop kept spawning a Claude Code every fifteen minutes
// for them anyway, firing every SessionStart hook on the machine each time,
// for zero visible output.
//
// The tick itself keeps running (it is a file read, and it is how rows another
// pane fetched arrive here); only the spawn is withheld, and it comes back by
// itself the moment the push path starts working.
func TestUsageTickDoesNotSpawnWhenTheGaugesCannotRender(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "spawns")
	t.Setenv("CLAUDEMUX_CLAUDE_BIN", countingClaude(t, counter, fixture(t, "get_usage_response.json"), 0))

	now := time.Now()
	m := rateModelForTest(now, 200)
	m.usageCachePath = filepath.Join(dir, "usage.json") // no cache: a spawn is due
	m.rateOK = false                                    // the user's own statusline: no meter line ever

	_, cmd := m.Update(usageTickMsg{})
	if cmd == nil {
		t.Fatal("cmd = nil, want the loop to keep reading the shared cache")
	}
	_ = cmd()
	if n := spawnCount(t, counter); n != 0 {
		t.Fatalf("spawned %d Claude Codes for a pane that renders no gauges at all, want 0", n)
	}

	// The push path starts working — `hook ensure` claimed the slot, or the
	// user pointed their statusLine at us. No restart, no latch to clear.
	m.rateOK = true
	_, cmd = m.Update(usageTickMsg{})
	_ = cmd()
	if n := spawnCount(t, counter); n != 1 {
		t.Errorf("spawned %d times once the gauges could render, want 1: the pane never recovers", n)
	}
}

// The lobby's half of the same rule.
func TestSwUsageTickDoesNotSpawnWhenTheGaugesCannotRender(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "spawns")
	t.Setenv("CLAUDEMUX_CLAUDE_BIN", countingClaude(t, counter, fixture(t, "get_usage_response.json"), 0))

	m := swRateModel(time.Now(), 200)
	m.usageCachePath = filepath.Join(dir, "usage.json")
	m.rateOK = false

	_, cmd := m.Update(usageTickMsg{})
	if cmd == nil {
		t.Fatal("lobby cmd = nil, want the loop to keep reading the shared cache")
	}
	_ = cmd()
	if n := spawnCount(t, counter); n != 0 {
		t.Errorf("the lobby spawned %d Claude Codes while rendering no gauges at all, want 0", n)
	}
}

// seedRateLimitsHome points $HOME at a temp dir holding abtop's cache and
// nothing of ours, and returns (abtop's path, ours).
func seedRateLimitsHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDEMUX_RATE_LIMITS_PATH", "")
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	abtop := filepath.Join(claudeDir, "abtop-rate-limits.json")
	if err := os.WriteFile(abtop, []byte(`{"source":"claude","updated_at":1,"five_hour":{"used_percentage":9,"resets_at":2}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return abtop, filepath.Join(claudeDir, "claudemux", "rate-limits.json")
}

// writeOurRateLimitsCache creates the cache our statusline subcommand writes.
func writeOurRateLimitsCache(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"source":"claudemux","updated_at":3,"five_hour":{"used_percentage":11,"resets_at":4}}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The upgrade gap: `hook ensure` claims the statusLine slot, abtop's shim
// stops writing, and a head starts before Claude Code's next statusline
// render — so ours does not exist yet and the head resolves to abtop's file.
// That file is now dead, so without re-resolution the pane shows abtop's last
// numbers, frozen and perfectly confident, for its entire lifetime. Stale
// meters are worse than absent ones.
func TestHeadMigratesToOurRateLimitsCacheWithoutRestarting(t *testing.T) {
	abtop, ours := seedRateLimitsHome(t)
	now := time.Now()

	m := rateModelForTest(now, 200)
	m.rateLimitsPath = defaultRateLimitsPath()
	if m.rateLimitsPath != abtop {
		t.Fatalf("rateLimitsPath = %q at construction, want abtop's file %q", m.rateLimitsPath, abtop)
	}

	// A tick before ours exists changes nothing.
	got, _ := m.Update(tickMsg(now))
	if p := got.(model).rateLimitsPath; p != abtop {
		t.Fatalf("rateLimitsPath = %q while only abtop's file exists, want %q", p, abtop)
	}

	// Claude Code finally renders a statusline and our cache appears.
	writeOurRateLimitsCache(t, ours)
	got, _ = got.(model).Update(tickMsg(now.Add(time.Second)))
	if p := got.(model).rateLimitsPath; p != ours {
		t.Errorf("rateLimitsPath = %q after our cache appeared, want %q: this pane shows abtop's frozen numbers until it is restarted", p, ours)
	}
}

// The lobby resolves the same path at construction and lives just as long.
func TestLobbyMigratesToOurRateLimitsCacheWithoutRestarting(t *testing.T) {
	abtop, ours := seedRateLimitsHome(t)
	now := time.Now()

	m := newSwModel("%1")
	if m.rateLimitsPath != abtop {
		t.Fatalf("rateLimitsPath = %q at construction, want abtop's file %q", m.rateLimitsPath, abtop)
	}
	writeOurRateLimitsCache(t, ours)
	got, _ := m.Update(swTickMsg(now))
	if p := got.(swModel).rateLimitsPath; p != ours {
		t.Errorf("rateLimitsPath = %q after our cache appeared, want %q", p, ours)
	}
}

// An explicit override is not a fallback and must never be re-resolved away
// from — it is the only handle the tests and a dev have on this pair of files.
func TestRefreshedRateLimitsPathHonorsTheOverride(t *testing.T) {
	_, ours := seedRateLimitsHome(t)
	writeOurRateLimitsCache(t, ours)

	override := filepath.Join(t.TempDir(), "override.json")
	t.Setenv("CLAUDEMUX_RATE_LIMITS_PATH", override)
	if got := refreshedRateLimitsPath(override); got != override {
		t.Errorf("refreshedRateLimitsPath(%q) = %q, want the override kept", override, got)
	}
	if got := refreshedRateLimitsPath(""); got != "" {
		t.Errorf("refreshedRateLimitsPath(\"\") = %q, want \"\" (no home dir to resolve against)", got)
	}
}

// The quiet window must survive the loop's own cache reads. A quieted pane
// goes on reading the shared cache every minute, and that cache is the very
// thing that keeps saying "no rows" — so re-arming the window from those reads
// would push its expiry out on every tick and quietly restore the permanent,
// process-lifetime latch this replaced.
func TestUsageQuietWindowIsNotExtendedByCacheReads(t *testing.T) {
	now := time.Now()
	m := rateModelForTest(now, 200)

	got, _ := m.Update(usageMsg{usage: PlanUsage{Available: true, FetchedAt: now, Fetched: true}})
	next := got.(model)
	armed := next.usageQuietUntil
	if armed.IsZero() {
		t.Fatal("no quiet window armed by our own rowless answer")
	}

	// An hour of ticks, each reading back the same rowless cache.
	for i := 1; i <= 60; i++ {
		at := now.Add(time.Duration(i) * usageCheckInterval)
		g, _ := next.Update(usageMsg{usage: PlanUsage{Available: true, FetchedAt: at}})
		next = g.(model)
	}
	if !next.usageQuietUntil.Equal(armed) {
		t.Errorf("the quiet window moved from %v to %v across cache reads: it would never expire", armed, next.usageQuietUntil)
	}
	if !next.usageMaySpawn(armed.Add(time.Minute)) {
		t.Error("still quiet past the window the fetch armed: the pane can never retry")
	}
}

// The busy→idle edge is not enough on its own: a long first turn produces no
// edge for as long as it runs, so a pane whose seed call failed — typically a
// placeholder over the near-empty transcript a head sees at startup — sits on
// the raw-prompt fallback for the whole run, even once the transcript is rich
// enough that pressing `s` produces a summary immediately. Growth in the
// transcript is the additional basis, gated to panes that have NO summary so
// it can never add billed calls to an already-labelled session.
func TestShouldSummarizeFromGrowth(t *testing.T) {
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	ts := base.Format(time.RFC3339)
	content := func(n int) []Event {
		var out []Event
		for i := 0; i < n; i++ {
			out = append(out, Event{Type: "assistant", Timestamp: ts, UserText: "step"})
		}
		return out
	}

	tests := []struct {
		name        string
		summarizer  *Summarizer
		summary     Summary
		events      []Event
		lastEvents  int
		summarizing bool
		lastAt      time.Time
		now         time.Time
		want        bool
	}{
		{
			name:       "new content past the floor with no summary fires",
			summarizer: &Summarizer{},
			events:     content(summaryGrowthMin), lastEvents: 0,
			lastAt: base, now: base.Add(time.Minute),
			want: true,
		},
		{
			name:       "an existing summary never fires",
			summarizer: &Summarizer{},
			summary:    Summary{Topic: "t", Now: "n", Tab: "tab"},
			events:     content(50), lastEvents: 0,
			lastAt: base, now: base.Add(time.Minute),
			want: false,
		},
		{
			name:       "growth below the threshold does not fire",
			summarizer: &Summarizer{},
			events:     content(summaryGrowthMin - 1), lastEvents: 0,
			lastAt: base, now: base.Add(time.Minute),
			want: false,
		},
		{
			name:       "no growth since the last call does not fire",
			summarizer: &Summarizer{},
			events:     content(20), lastEvents: 20,
			lastAt: base, now: base.Add(time.Minute),
			want: false,
		},
		{
			name:       "inside the retry floor does not fire",
			summarizer: &Summarizer{},
			events:     content(50), lastEvents: 0,
			lastAt: base, now: base.Add(5 * time.Second),
			want: false,
		},
		{
			name:       "a call already in flight does not fire",
			summarizer: &Summarizer{},
			events:     content(50), lastEvents: 0,
			summarizing: true,
			lastAt:      base, now: base.Add(time.Minute),
			want: false,
		},
		{
			name:       "no summarizer never fires",
			summarizer: nil,
			events:     content(50), lastEvents: 0,
			lastAt: base, now: base.Add(time.Minute),
			want: false,
		},
		// Bookkeeping records (attachment, mode, permission-mode, ...)
		// outnumber content several to one and say nothing about the session.
		// Counting them would fire on a transcript still too thin to
		// describe — the very failure that left the pane with no summary.
		{
			name:       "bookkeeping records alone do not count as growth",
			summarizer: &Summarizer{},
			events: []Event{
				{Type: "attachment", Timestamp: ts},
				{Type: "mode", Timestamp: ts},
				{Type: "permission-mode", Timestamp: ts},
				{Type: "system", Timestamp: ts},
			},
			lastEvents: 0,
			lastAt:     base, now: base.Add(time.Minute),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := model{
				summarizer:         tt.summarizer,
				summary:            tt.summary,
				allEvents:          tt.events,
				lastSummaryEvents:  tt.lastEvents,
				summarizing:        tt.summarizing,
				lastSummaryAt:      tt.lastAt,
				minSummaryInterval: 20 * time.Second,
			}
			if got := m.shouldSummarizeFromGrowth(tt.now); got != tt.want {
				t.Errorf("shouldSummarizeFromGrowth() = %v, want %v", got, tt.want)
			}
		})
	}
}

// End to end through Update: a poll that stays busy — no busy→idle edge, no
// armed retry flag — must still fire the first call once the transcript has
// grown. This is the long-first-turn case the growth basis exists for.
func TestUpdateDataMsgFiresFirstSummarizeMidTurn(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	ts := now.Format(time.RFC3339)
	m := model{
		ready:      true,
		summarizer: &Summarizer{},
		allEvents: []Event{
			{Type: "user", Timestamp: ts, UserText: "add a growth trigger"},
			{Type: "assistant", Timestamp: ts, UserText: "reading tui.go"},
		},
		state: State{Kind: StateThinking, Since: now.Add(-5 * time.Minute)},
		// Well outside every floor, so no guard but the growth one is in play.
		lastSummaryAt: now.Add(-time.Hour),
	}

	// A tool_use with no result keeps the session busy: this poll crosses no
	// busy→ended edge, so shouldSummarize cannot be what fires the call.
	got, cmd := m.Update(dataMsg{
		time: now,
		newEvents: []Event{{
			Type: "assistant", Timestamp: ts,
			ToolUses: []ToolUse{{ID: "t1", Name: "Bash"}},
		}},
		rateLimitErr: errors.New("no rate limits in this test"),
	})
	next := got.(model)

	if turnEndedByIdle(next.state.Kind) {
		t.Fatalf("precondition: state = %v, want a still-busy state (no edge)", next.state.Kind)
	}
	if !next.summarizing {
		t.Error("summarizing = false, want true: transcript growth must fire the first call mid-turn")
	}
	if next.lastSummaryEvents == 0 {
		t.Error("lastSummaryEvents = 0, want the content count stamped when the call was issued")
	}
	// Deliberately NOT executed — running it would make a network call.
	if cmd == nil {
		t.Error("cmd = nil, want the summarize command")
	}
}

// The stamp is a per-session baseline. A rotation to a fresh session must
// reset it: carrying a long session's count into a new one that holds two
// events makes the growth delta negative, and the new pane would then never
// fire its own first call.
func TestSwitchSessionResetsSummaryEventStamp(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	path := filepath.Join(dir, "sess-new.jsonl")
	line := `{"type":"user","timestamp":"2026-08-21T12:00:00Z","message":{"role":"user","content":"a brand new ask"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	m := model{
		lastSummaryEvents: 400,
		// No summarizer: switchSession's own seed call is then impossible, so
		// what the assertion sees is the reset, not a fresh stamp.
		lastSummaryAt: now.Add(-time.Hour),
	}
	m.switchSession(path, now)

	if m.lastSummaryEvents != 0 {
		t.Errorf("lastSummaryEvents = %d, want 0: the stamp is per-session", m.lastSummaryEvents)
	}
}

// The spike gauge sits right after 5h — it is that bar's derivative — and
// carries a bar, so it counts toward barred while the eta stays plain text.
func TestRateGaugesSpikeMeterFollowsFiveHour(t *testing.T) {
	now := time.Now()
	rl := RateLimits{
		FiveHour: Window{UsedPercent: 20, ResetsAt: now.Add(5 * time.Hour)},
		SevenDay: Window{UsedPercent: 30, ResetsAt: now.Add(72 * time.Hour)},
	}
	// 2 points in 3 minutes: 40% of the window per hour.
	samples := []pctSample{{at: now.Add(-3 * time.Minute), pct: 18}, {at: now, pct: 20}}
	gs := rateGauges(rl, nil, samples, now, defaultBarW)
	if len(gs.parts) != 4 {
		t.Fatalf("parts = %q, want 5h, burn, wk, eta", gs.parts)
	}
	if !strings.Contains(gs.parts[1], "burn") || !strings.Contains(gs.parts[1], "40%/h") {
		t.Errorf("parts[1] = %q, want the burn gauge at 40%%/h", gs.parts[1])
	}
	if !strings.Contains(gs.parts[2], "wk") {
		t.Errorf("parts[2] = %q, want wk after burn", gs.parts[2])
	}
	if gs.barred != 3 {
		t.Errorf("barred = %d, want 3 (5h, burn, wk)", gs.barred)
	}
	// Idle: the meter stays on the line at rest rather than popping in and
	// out and reflowing everything beside it.
	gs = rateGauges(rl, nil, nil, now, defaultBarW)
	if len(gs.parts) != 3 || !strings.Contains(gs.parts[1], "0%/h") {
		t.Errorf("idle parts = %q, want 5h, burn 0%%/h, wk", gs.parts)
	}
}

// Fill and color follow the %/h reading: green under the sustainable 20%/h,
// yellow from 20, red from 40, and the bar is full at a whole window per
// hour (100%/h).
func TestBurnGaugeColorBands(t *testing.T) {
	cases := []struct {
		perHour float64
		want    string
	}{
		{10, thresholdColor(0)},
		{20, thresholdColor(70)},
		{39, thresholdColor(70)},
		{40, thresholdColor(85)},
		{180, thresholdColor(85)},
	}
	for _, c := range cases {
		if got := burnColor(c.perHour); got != c.want {
			t.Errorf("burnColor(%v) = %s, want %s", c.perHour, got, c.want)
		}
	}
	if got := burnFillPct(50); got != 50 {
		t.Errorf("burnFillPct(50) = %v, want 50", got)
	}
	if got := burnFillPct(150); got != 100 {
		t.Errorf("burnFillPct(150) = %v, want 100 (pegged)", got)
	}
}

// The head samples the exact percentage, so readings that differ only in the
// fraction still produce distinct samples for the spike gauge.
func TestDataMsgSamplesExactPercent(t *testing.T) {
	now := time.Now()
	m := model{}
	rl := RateLimits{FiveHour: Window{UsedPercent: 18, Used: 18.2, ResetsAt: now.Add(4 * time.Hour)}}
	next, _ := m.Update(dataMsg{time: now, rateLimits: rl})
	m = next.(model)
	rl.FiveHour.Used = 18.4
	next, _ = m.Update(dataMsg{time: now.Add(time.Minute), rateLimits: rl})
	m = next.(model)
	if len(m.pctSamples) != 2 || m.pctSamples[1].pct != 18.4 {
		t.Fatalf("pctSamples = %+v, want two samples ending at 18.4", m.pctSamples)
	}
}

// dataMsg.deferRaw must land on model.deferRaw, alongside conductRaw, so the
// chip has something to render off the very next poll.
func TestDataMsgCopiesDeferRaw(t *testing.T) {
	m := model{}
	next, _ := m.Update(dataMsg{time: time.Now(), conductRaw: "standby", deferRaw: "1"})
	got := next.(model)
	if got.deferRaw != "1" {
		t.Errorf("deferRaw = %q, want %q", got.deferRaw, "1")
	}
}

// The d key toggles the mark on this head's own session when running inside
// tmux (selfPane set), same as space toggles conduct mode.
func TestKeyDTogglesDeferInsideTmux(t *testing.T) {
	m := model{ready: true, selfPane: "%1", deferRaw: ""}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if cmd == nil {
		t.Fatal("d must issue a toggle cmd inside tmux")
	}
}

// Outside tmux there is no session to mark, so d must be a no-op — same
// contract as space's conduct toggle.
func TestKeyDNoopOutsideTmux(t *testing.T) {
	m := model{ready: true, selfPane: ""}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if cmd != nil {
		t.Error("d must be a no-op outside tmux")
	}
}

// The defer chip renders next to the conduct chip in both statusbar layouts
// once the mark is set.
func TestRenderStatusbarShowsDeferChip(t *testing.T) {
	now := time.Now()
	m := model{ready: true, width: 500, height: 4, deferRaw: "1"}
	line := renderStatusbar(m, now, "")
	if !strings.Contains(line, "defer") {
		t.Errorf("renderStatusbar = %q, want it to contain the defer chip", line)
	}
}

func TestRenderStateLineShowsDeferChip(t *testing.T) {
	now := time.Now()
	m := model{ready: true, width: 500, height: 4, deferRaw: "1"}
	line := renderStateLine(m, now)
	if !strings.Contains(line, "defer") {
		t.Errorf("renderStateLine = %q, want it to contain the defer chip", line)
	}
}

// Unset (or unreadable) mark means no chip in either layout.
func TestRenderLinesOmitDeferChipWhenUnset(t *testing.T) {
	now := time.Now()
	m := model{ready: true, width: 500, height: 4}
	if line := renderStatusbar(m, now, ""); strings.Contains(line, "defer") {
		t.Errorf("renderStatusbar = %q, want no defer chip", line)
	}
	if line := renderStateLine(m, now); strings.Contains(line, "defer") {
		t.Errorf("renderStateLine = %q, want no defer chip", line)
	}
}
