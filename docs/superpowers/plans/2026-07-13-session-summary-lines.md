# Session Summary Lines Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace claude-head's `first`/`last` raw-prompt rows with two Haiku-generated lines — `topic` (what the session is about) and `now` (what it is doing) — and fix the bug that pins the first prompt to `/clear` forever.

**Architecture:** A new `summary.go` holds a `Summarizer` that calls the Anthropic Messages API through the official Go SDK, forcing a `summarize` tool call so both lines arrive as validated JSON. The TUI fires one call on the `busy → idle` edge (behind an in-flight flag and a minimum interval), as a `tea.Cmd` that never blocks the render loop. When no API key is set or a call fails, the pane falls back to today's raw prompt rows — so `EventReader.firstPrompt` must stop freezing on a slash command.

**Tech Stack:** Go 1.26, Bubble Tea, `github.com/anthropics/anthropic-sdk-go` v1.57.0 (already added to `go.mod`).

## Global Constraints

- **Model:** `anthropic.ModelClaudeHaiku4_5` (the constant — do not hand-write the ID string).
- **Auth:** `ANTHROPIC_API_KEY` from the environment, passed explicitly via `option.WithAPIKey`. **Never construct the SDK client with zero options.** A bare `anthropic.NewClient()` falls back to the local OAuth profile, which would spend Michael's Claude subscription rate limit — the exact budget this TUI exists to display. If the env var is empty, do not construct a client at all.
- **No network in tests.** Every test injects a fake via `option.WithHTTPClient`, whose interface is `Do(*http.Request) (*http.Response, error)`.
- Existing code style: no comment unless it states a constraint the code can't show. Table-driven tests, matching `events_test.go`.
- Run `go test ./...` and `go vet ./...` before every commit.

---

### Task 1: Stop `/clear` from freezing the first prompt

`EventReader.firstPrompt` is captured once in `SeedFromEnd` and never revisited. A session seeded while `/clear` was its only prompt takes `/clear` as the fallback and keeps it forever — the real prompt arrives on a later `Tail()`, and `firstUserPrompt` never runs again. This is the bug behind the complaint, and the fallback path in Task 5 depends on it working.

**Files:**
- Modify: `events.go` (add `upgradeFirstPrompt`, call it from `Tail`)
- Modify: `tui.go:116-130` (`recomputeFromEvents` re-reads `FirstPrompt()`)
- Test: `events_test.go`

**Interfaces:**
- Consumes: `firstUserPrompt(events []Event) string` from `tui.go:194` (unchanged).
- Produces: `(*EventReader).upgradeFirstPrompt(events []Event)`.

- [ ] **Step 1: Write the failing test**

Append to `events_test.go`:

```go
func TestUpgradeFirstPrompt(t *testing.T) {
	user := func(text string) Event { return Event{Type: "user", UserText: text} }

	tests := []struct {
		name  string
		seed  string   // firstPrompt captured at seed time
		tail  []Event  // events arriving on a later Tail
		want  string
	}{
		{
			name: "command placeholder yields to a real prompt",
			seed: "/clear",
			tail: []Event{user("fix the worktree chip")},
			want: "fix the worktree chip",
		},
		{
			name: "command holds when nothing else arrives",
			seed: "/clear",
			tail: []Event{user("/color purple")},
			want: "/clear",
		},
		{
			name: "a real prompt is frozen and never replaced",
			seed: "fix the worktree chip",
			tail: []Event{user("now clip the status lines")},
			want: "fix the worktree chip",
		},
		{
			name: "empty seed takes whatever arrives first",
			seed: "",
			tail: []Event{user("/clear"), user("fix the chip")},
			want: "fix the chip",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &EventReader{firstPrompt: tt.seed}
			r.upgradeFirstPrompt(tt.tail)
			if got := r.FirstPrompt(); got != tt.want {
				t.Errorf("FirstPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./... -run TestUpgradeFirstPrompt`
Expected: FAIL — `r.upgradeFirstPrompt undefined (type *EventReader has no field or method upgradeFirstPrompt)`

- [ ] **Step 3: Write the implementation**

In `events.go`, add after `FirstPrompt()` (line 132):

```go
// upgradeFirstPrompt lets a genuine prompt displace a slash-command placeholder.
// firstPrompt is captured once at seed, so a session seeded when `/clear` was its
// only prompt would otherwise keep `/clear` forever: the real prompt arrives on a
// later Tail, and firstUserPrompt never runs again. Once a non-command prompt is
// stored it freezes — that one is the session's genuine first prompt.
func (r *EventReader) upgradeFirstPrompt(events []Event) {
	if r.firstPrompt != "" && !strings.HasPrefix(r.firstPrompt, "/") {
		return
	}
	p := firstUserPrompt(events)
	if p == "" {
		return
	}
	if r.firstPrompt == "" || !strings.HasPrefix(p, "/") {
		r.firstPrompt = p
	}
}
```

In `Tail()`, replace the final `return parseLines(consume, false), nil` (line 167) with:

```go
	events := parseLines(consume, false)
	r.upgradeFirstPrompt(events)
	return events, nil
```

In `tui.go`, add to the end of `recomputeFromEvents` (after line 129):

```go
	if m.reader != nil {
		m.firstPrompt = m.reader.FirstPrompt()
	}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./... && go vet ./...`
Expected: PASS, no vet diagnostics.

- [ ] **Step 5: Commit**

```bash
git add events.go events_test.go tui.go
git commit -m "fix(tui): let a real prompt displace a /clear placeholder

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Condense the event stream into a transcript

A pure function that flattens events into the plain-text transcript Haiku reads. Isolated from the network so it tests on its own.

**Files:**
- Create: `summary.go`
- Test: `summary_test.go`

**Interfaces:**
- Consumes: `Event` (`events.go:61`) — fields `Type`, `UserText`, `ToolUses[].Name`, `IsMeta`.
- Produces: `condenseTranscript(firstPrompt string, events []Event, maxEvents int) string`.

- [ ] **Step 1: Write the failing test**

Create `summary_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestCondenseTranscript(t *testing.T) {
	events := []Event{
		{Type: "user", UserText: "fix the worktree chip"},
		{Type: "assistant", UserText: "Looking at tui.go now."},
		{Type: "assistant", ToolUses: []ToolUse{{Name: "Read"}, {Name: "Edit"}}},
		{Type: "user", UserText: "<system-reminder>ignore me</system-reminder>"},
		{Type: "file-history-snapshot", UserText: "bookkeeping"},
		{Type: "user", IsMeta: true, UserText: "caveat notice"},
	}

	got := condenseTranscript("fix the worktree chip", events, 30)

	for _, want := range []string{
		"first prompt: fix the worktree chip",
		"user: fix the worktree chip",
		"assistant: Looking at tui.go now.",
		"tool: Read",
		"tool: Edit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript missing %q\ngot:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"system-reminder", "bookkeeping", "caveat notice"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("transcript should not contain %q\ngot:\n%s", unwanted, got)
		}
	}
}

func TestCondenseTranscriptKeepsTail(t *testing.T) {
	var events []Event
	for _, s := range []string{"one", "two", "three"} {
		events = append(events, Event{Type: "user", UserText: s})
	}

	got := condenseTranscript("one", events, 2)

	if strings.Contains(got, "user: one") {
		t.Errorf("oldest event should be dropped when over maxEvents\ngot:\n%s", got)
	}
	if !strings.Contains(got, "user: three") {
		t.Errorf("newest event must survive\ngot:\n%s", got)
	}
	if !strings.Contains(got, "first prompt: one") {
		t.Errorf("first prompt anchors the topic and must survive truncation\ngot:\n%s", got)
	}
}

func TestCondenseTranscriptTruncatesLongText(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := condenseTranscript("", []Event{{Type: "user", UserText: long}}, 30)

	for _, line := range strings.Split(got, "\n") {
		if len([]rune(line)) > 220 {
			t.Errorf("line exceeds truncation budget: %d runes", len([]rune(line)))
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./... -run TestCondense`
Expected: FAIL — `undefined: condenseTranscript`

- [ ] **Step 3: Write the implementation**

Create `summary.go`:

```go
package main

import (
	"strings"
)

const transcriptTextLimit = 200

// condenseTranscript flattens the event ring into the plain text the summarizer
// reads: the session's first prompt (which anchors the topic even after the ring
// has truncated the head away), then the last maxEvents events as user text,
// assistant text, and tool names. Bookkeeping events are dropped — they say
// nothing about what the session is doing.
func condenseTranscript(firstPrompt string, events []Event, maxEvents int) string {
	var b strings.Builder
	if firstPrompt != "" {
		b.WriteString("first prompt: " + truncateRunes(firstPrompt, transcriptTextLimit) + "\n")
	}

	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}
	for _, e := range events {
		switch e.Type {
		case "user", "last-prompt":
			if genuinePrompt(e) {
				b.WriteString("user: " + truncateRunes(e.UserText, transcriptTextLimit) + "\n")
			}
		case "assistant":
			if e.UserText != "" {
				b.WriteString("assistant: " + truncateRunes(e.UserText, transcriptTextLimit) + "\n")
			}
			for _, tu := range e.ToolUses {
				b.WriteString("tool: " + tu.Name + "\n")
			}
		}
	}
	return b.String()
}

func truncateRunes(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add summary.go summary_test.go
git commit -m "feat(tui): condense the event ring into a summarizer transcript

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: The Summarizer

One Haiku call returns both lines. A forced tool call carries the JSON, so nothing parses prose. The previous `topic` is fed back in as an anchor — without it the topic re-derives from scratch each turn and jitters.

**Files:**
- Modify: `summary.go`
- Test: `summary_test.go`

**Interfaces:**
- Consumes: `condenseTranscript` (Task 2).
- Produces:
  - `type Summary struct { Topic, Now string }`
  - `type Summarizer struct { client anthropic.Client }`
  - `newSummarizer(opts ...option.RequestOption) *Summarizer` — returns `nil` when `ANTHROPIC_API_KEY` is unset and no options are given.
  - `(*Summarizer).Summarize(ctx context.Context, firstPrompt string, events []Event, prevTopic string) (Summary, error)`

**SDK symbols, verified against `anthropic-sdk-go@v1.57.0` — use these exactly:**
- `anthropic.NewClient(opts ...option.RequestOption) anthropic.Client` (a value, not a pointer)
- `option.WithAPIKey(string)`, `option.WithHTTPClient(HTTPClient)` where `HTTPClient` is `interface{ Do(*http.Request) (*http.Response, error) }`
- `anthropic.ToolChoiceParamOfTool(name string) anthropic.ToolChoiceUnionParam`
- `anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{...}}`
- `anthropic.ToolInputSchemaParam{Properties: map[string]any{...}}`
- `client.Messages.New(ctx, anthropic.MessageNewParams{...})`
- `block.AsAny().(anthropic.ToolUseBlock)`, then `variant.JSON.Input.Raw()` for the raw JSON string

- [ ] **Step 1: Write the failing test**

Append to `summary_test.go`. Its import block becomes:

```go
import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)
```

```go
type fakeDoer struct {
	body   string
	status int
	err    error
	gotReq map[string]any
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &f.gotReq)
	}
	status := f.status
	if status == 0 {
		status = 200
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Request:    req,
	}, nil
}

func toolUseResponse(topic, now string) string {
	input, _ := json.Marshal(map[string]string{"topic": topic, "now": now})
	body, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant",
		"model": "claude-haiku-4-5", "stop_reason": "tool_use",
		"content": []any{map[string]any{
			"type": "tool_use", "id": "toolu_1", "name": summarizeToolName,
			"input": json.RawMessage(input),
		}},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	return string(body)
}

func testSummarizer(d *fakeDoer) *Summarizer {
	return newSummarizer(option.WithAPIKey("test-key"), option.WithHTTPClient(d))
}

func TestSummarizeReturnsBothLines(t *testing.T) {
	d := &fakeDoer{body: toolUseResponse("fixing the worktree chip", "running the tui tests")}
	s := testSummarizer(d)

	got, err := s.Summarize(context.Background(), "fix the chip", []Event{{Type: "user", UserText: "fix the chip"}}, "")
	if err != nil {
		t.Fatalf("Summarize() error = %v", err)
	}
	if got.Topic != "fixing the worktree chip" {
		t.Errorf("Topic = %q, want %q", got.Topic, "fixing the worktree chip")
	}
	if got.Now != "running the tui tests" {
		t.Errorf("Now = %q, want %q", got.Now, "running the tui tests")
	}
}

func TestSummarizeForcesTheTool(t *testing.T) {
	d := &fakeDoer{body: toolUseResponse("a", "b")}
	s := testSummarizer(d)

	if _, err := s.Summarize(context.Background(), "", nil, ""); err != nil {
		t.Fatalf("Summarize() error = %v", err)
	}

	choice, ok := d.gotReq["tool_choice"].(map[string]any)
	if !ok {
		t.Fatalf("request has no tool_choice: %v", d.gotReq)
	}
	if choice["type"] != "tool" || choice["name"] != summarizeToolName {
		t.Errorf("tool_choice = %v, want a forced %q call", choice, summarizeToolName)
	}
	if d.gotReq["model"] != string(anthropic.ModelClaudeHaiku4_5) {
		t.Errorf("model = %v, want %v", d.gotReq["model"], anthropic.ModelClaudeHaiku4_5)
	}
}

func TestSummarizeAnchorsThePreviousTopic(t *testing.T) {
	d := &fakeDoer{body: toolUseResponse("a", "b")}
	s := testSummarizer(d)

	if _, err := s.Summarize(context.Background(), "", nil, "fixing the worktree chip"); err != nil {
		t.Fatalf("Summarize() error = %v", err)
	}

	raw, _ := json.Marshal(d.gotReq)
	if !strings.Contains(string(raw), "fixing the worktree chip") {
		t.Errorf("previous topic must be sent as an anchor; request was:\n%s", raw)
	}
}

func TestSummarizeErrorsOnAPIFailure(t *testing.T) {
	d := &fakeDoer{status: 500, body: `{"type":"error","error":{"type":"api_error","message":"boom"}}`}
	s := testSummarizer(d)

	if _, err := s.Summarize(context.Background(), "", nil, ""); err == nil {
		t.Error("Summarize() error = nil, want an error so the TUI falls back to raw prompts")
	}
}

func TestNewSummarizerNilWithoutKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if s := newSummarizer(); s != nil {
		t.Error("newSummarizer() must return nil without a key — a keyless SDK client would fall back to the OAuth profile and spend the subscription budget")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./... -run 'TestSummarize|TestNewSummarizer'`
Expected: FAIL — `undefined: Summarizer`, `undefined: newSummarizer`, `undefined: summarizeToolName`

- [ ] **Step 3: Write the implementation**

Add to `summary.go` (imports become `context`, `encoding/json`, `errors`, `fmt`, `os`, `strings`, plus `github.com/anthropics/anthropic-sdk-go` and `.../option`):

```go
const (
	summarizeToolName = "summarize"
	summaryMaxEvents  = 30
	summaryMaxTokens  = 200
)

const summarySystemPrompt = `You label a live coding session for a two-line terminal status display.

Read the transcript and call the summarize tool with:
- topic: what this session is FOR. The durable goal, not the current step.
- now: what it is doing RIGHT NOW. The current step.

Each line: lowercase, under 60 characters, no trailing period, no quotes.
Name concrete things (files, commands, features) — never "working on the task".

If a previous topic is given, KEEP IT VERBATIM unless the session has clearly
moved on to a different goal. A stable topic is worth more than a fresh one.`

type Summary struct {
	Topic string `json:"topic"`
	Now   string `json:"now"`
}

// Summarizer turns a session transcript into the topic/now status lines.
type Summarizer struct {
	client anthropic.Client
}

// newSummarizer returns nil when there is no API key and no explicit options, which
// disables the feature and leaves the pane on its raw-prompt fallback. The key is
// passed explicitly and never left to the SDK's own resolution: a keyless client
// falls back to the local Claude Code OAuth profile, which would spend the very
// subscription rate limit this TUI exists to display.
func newSummarizer(opts ...option.RequestOption) *Summarizer {
	if len(opts) == 0 {
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return nil
		}
		opts = []option.RequestOption{option.WithAPIKey(key)}
	}
	return &Summarizer{client: anthropic.NewClient(opts...)}
}

func (s *Summarizer) Summarize(ctx context.Context, firstPrompt string, events []Event, prevTopic string) (Summary, error) {
	transcript := condenseTranscript(firstPrompt, events, summaryMaxEvents)

	prompt := "Transcript:\n" + transcript
	if prevTopic != "" {
		prompt = fmt.Sprintf("Previous topic: %s\n\n%s", prevTopic, prompt)
	}

	tool := anthropic.ToolParam{
		Name:        summarizeToolName,
		Description: anthropic.String("Report the session's topic and current activity."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"topic": map[string]any{
					"type":        "string",
					"description": "What the session is for. Durable across turns.",
				},
				"now": map[string]any{
					"type":        "string",
					"description": "What the session is doing right now.",
				},
			},
			Required: []string{"topic", "now"},
		},
	}

	resp, err := s.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeHaiku4_5,
		MaxTokens: summaryMaxTokens,
		System:    []anthropic.TextBlockParam{{Text: summarySystemPrompt}},
		Tools:     []anthropic.ToolUnionParam{{OfTool: &tool}},
		ToolChoice: anthropic.ToolChoiceParamOfTool(summarizeToolName),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return Summary{}, err
	}

	for _, block := range resp.Content {
		tu, ok := block.AsAny().(anthropic.ToolUseBlock)
		if !ok || tu.Name != summarizeToolName {
			continue
		}
		var out Summary
		if err := json.Unmarshal([]byte(tu.JSON.Input.Raw()), &out); err != nil {
			return Summary{}, fmt.Errorf("summarize tool input: %w", err)
		}
		if out.Topic == "" && out.Now == "" {
			return Summary{}, errors.New("summarize returned both lines empty")
		}
		return out, nil
	}
	return Summary{}, errors.New("no summarize tool call in response")
}
```

If `ToolInputSchemaParam` has no `Required` field, or any other symbol does not compile, run `go doc github.com/anthropics/anthropic-sdk-go.<Symbol>` and fix against the real signature — do not invent a name. The compiler is the source of truth here.

- [ ] **Step 4: Run the tests**

Run: `go test ./... && go vet ./...`
Expected: PASS. No test may reach the network — if one hangs, a `fakeDoer` is not wired in.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum summary.go summary_test.go
git commit -m "feat(tui): summarize a session's topic and current step via Haiku

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Fire the summarizer on turn boundaries

The call runs as a `tea.Cmd` and lands back as a `summaryMsg`, so the render loop never blocks on the network. It fires when the session goes `busy → idle` — a turn just finished, so there is real news — plus once at seed.

**Files:**
- Modify: `tui.go` (model fields, `newModel`, `Init`, `Update`)
- Test: `tui_test.go`

**Interfaces:**
- Consumes: `Summary`, `Summarizer`, `newSummarizer` (Task 3); `State`/`StateIdle` (`state.go`).
- Produces:
  - model fields `summarizer *Summarizer`, `summary Summary`, `summarizing bool`, `lastSummaryAt time.Time`
  - `type summaryMsg struct { summary Summary; err error; at time.Time }`
  - `(model).shouldSummarize(prevKind StateKind, now time.Time) bool`
  - `(model).summarize() tea.Cmd`

- [ ] **Step 1: Write the failing test**

Append to `tui_test.go`:

```go
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
				summarizer:    tt.summarizer,
				state:         State{Kind: tt.kind},
				summarizing:   tt.summarizing,
				lastSummaryAt: tt.lastAt,
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
```

Add `"errors"` to `tui_test.go`'s imports if it is not already there.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./... -run 'TestShouldSummarize|TestSummaryMsg'`
Expected: FAIL — `unknown field summarizer in struct literal`, `undefined: summaryMsg`

- [ ] **Step 3: Write the implementation**

In `tui.go`, add to the `model` struct's "Latest snapshot" block (after `lastPrompt` on line 79):

```go
	summary     Summary
```

and to the "UI" block (after `polling` on line 88):

```go
	summarizer    *Summarizer
	summarizing   bool
	lastSummaryAt time.Time
```

Add the constant and message type near `tickMsg` (line 38):

```go
// minSummaryInterval keeps a burst of one-line turns from hammering the API.
const minSummaryInterval = 20 * time.Second

type summaryMsg struct {
	summary Summary
	err     error
	at      time.Time
}
```

In `newModel`, set the field in the struct literal (alongside `firstPrompt` on line 106):

```go
		summarizer:     newSummarizer(),
```

Add the trigger predicate and the command, next to `pollData`:

```go
// shouldSummarize reports whether this poll crossed the busy → idle edge — a turn
// just finished, so there is something new to say. Guarded by an in-flight flag and
// a minimum interval so short back-to-back turns can't hammer the API.
func (m model) shouldSummarize(prevKind StateKind, now time.Time) bool {
	if m.summarizer == nil || m.summarizing {
		return false
	}
	if now.Sub(m.lastSummaryAt) < minSummaryInterval {
		return false
	}
	return prevKind != StateIdle && m.state.Kind == StateIdle
}

func (m model) summarize() tea.Cmd {
	s := m.summarizer
	first := m.firstPrompt
	events := m.allEvents
	prevTopic := m.summary.Topic
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := s.Summarize(ctx, first, events, prevTopic)
		return summaryMsg{summary: out, err: err, at: time.Now()}
	}
}
```

Add `"context"` to `tui.go`'s imports.

In `Init`, seed a summary so a freshly attached pane isn't blank:

```go
func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.pollData(), m.tick()}
	if m.summarizer != nil {
		cmds = append(cmds, m.summarize())
	}
	return tea.Batch(cmds...)
}
```

Note this leaves `m.summarizing` false for the seed call — the model returned by `newModel` is a value and `Init` has a value receiver, so it cannot set the flag. The minimum-interval guard covers the window until the first `summaryMsg` lands.

In `Update`, add a case for the new message:

```go
	case summaryMsg:
		m.summarizing = false
		m.lastSummaryAt = msg.at
		if msg.err == nil {
			m.summary = msg.summary
		}
```

In `Update`'s `dataMsg` case, capture the state before it is recomputed and fire on the edge. Replace the tail of the case (lines 325-326):

```go
		prevKind := m.state.Kind
		m.recomputeFromEvents(msg.time)
		m.lastUpdate = msg.time
		if m.shouldSummarize(prevKind, msg.time) {
			m.summarizing = true
			return m, m.summarize()
		}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tui.go tui_test.go
git commit -m "feat(tui): summarize on the busy-to-idle edge, off the render loop

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Render topic and now

The summary lines take the slots `first` and `last` hold today. When there is no summary — no API key, a failed call, or nothing back yet — the pane renders the raw prompt rows exactly as it does now.

**Files:**
- Modify: `tui.go:332-367` (`View`)
- Test: `tui_test.go`

**Interfaces:**
- Consumes: `renderPromptLine(label, prompt string, width int) string` (`tui.go:373`, unchanged); model fields from Task 4.
- Produces: `(model).promptRows() (topLabel, top, bottomLabel, bottom string)` — the two rows to render, already resolved to summary-or-fallback.

- [ ] **Step 1: Write the failing test**

Append to `tui_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./... -run 'TestPromptRow|TestViewShows'`
Expected: FAIL — `m.promptRows undefined (type model has no field or method promptRows)`

- [ ] **Step 3: Write the implementation**

In `tui.go`, add next to `renderPromptLine`:

```go
// promptRows resolves the two context rows. The Haiku summary is preferred; with
// no summary yet — no API key, a failed call, or nothing back — the pane falls back
// to the raw first/last prompts, so nothing regresses without a key.
func (m model) promptRows() (topLabel, top, bottomLabel, bottom string) {
	if m.summary.Topic != "" || m.summary.Now != "" {
		return "topic", m.summary.Topic, "now  ", m.summary.Now
	}
	return "first", m.firstPrompt, "last ", m.lastPrompt
}
```

In `View`, replace the `height == 3` and `default` cases (lines 348-360):

```go
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
```

- [ ] **Step 4: Run the tests**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Verify it against a real session**

Build and run the monitor against a live transcript, with the key set, and confirm both lines render and that the `/clear` case no longer sticks:

```bash
go build -o /tmp/claude-head . && ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY /tmp/claude-head
```

Then confirm the fallback path is intact — with no key, the pane must show `first`/`last`:

```bash
env -u ANTHROPIC_API_KEY /tmp/claude-head
```

- [ ] **Step 6: Commit**

```bash
git add tui.go tui_test.go
git commit -m "feat(tui): render the topic/now summary rows, falling back to raw prompts

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```
