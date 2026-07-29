package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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
	got := renderPromptLine("", "hello\n\n  world", 40)
	if !strings.Contains(got, "❯ hello world") {
		t.Errorf("renderPromptLine = %q, want it to contain %q", got, "❯ hello world")
	}

	// A label is shown before the prompt marker.
	labeled := renderPromptLine("first", "the goal", 40)
	if !strings.Contains(labeled, "first") || !strings.Contains(labeled, "❯ the goal") {
		t.Errorf("labeled renderPromptLine = %q, want it to contain label and prompt", labeled)
	}

	// Long prompts truncate with an ellipsis and never exceed the width.
	wide := renderPromptLine("", "this is a very long prompt that will not fit", 20)
	if w := lipgloss.Width(wide); w != 20 {
		t.Errorf("rendered width = %d, want 20", w)
	}
	if !strings.Contains(wide, "…") {
		t.Errorf("expected truncated line to contain ellipsis, got %q", wide)
	}

	// Empty prompt renders the em-dash placeholder.
	if got := renderPromptLine("", "", 20); !strings.Contains(got, "—") {
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
// included (truncated to 24 runes).
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
	if !strings.Contains(out, "⎇ feature-branch") {
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
	if !strings.Contains(got, "⎇ "+name) {
		t.Errorf("renderStateLine = %q, want it to contain the full chip %q", got, name)
	}
	if w := lipgloss.Width(got); w != m.width {
		t.Errorf("renderStateLine width = %d, want %d", w, m.width)
	}
}

// Only when the assembled state line doesn't fit does the chip truncate —
// nothing else shrinks.
func TestRenderStateLineTruncatesChipWhenNarrow(t *testing.T) {
	name := strings.Repeat("x", 60)
	m := model{
		width:     30,
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

// At height 3 the last prompt line joins state and meters.
func TestViewHeightThreeAddsLastPrompt(t *testing.T) {
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
		t.Errorf("last line (index 2) = %q, want the last prompt", lines[2])
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
}

func TestViewShowsTheLiveLineWhenOnlyOneFits(t *testing.T) {
	m := model{
		ready: true, width: 80, height: 3,
		summary: Summary{Topic: "fixing the chip", Now: "running tests"},
	}
	out := m.View()

	if !strings.Contains(out, "running tests") {
		t.Errorf("at height 3 the single row must be `now`\ngot:\n%s", out)
	}
	if strings.Contains(out, "fixing the chip") {
		t.Errorf("at height 3 there is no room for `topic`\ngot:\n%s", out)
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
