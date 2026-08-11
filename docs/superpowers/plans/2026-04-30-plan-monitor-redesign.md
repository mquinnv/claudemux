# plan-monitor Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild the plan-monitor TUI as an ambient "Glance Dashboard" that shows live state, model, context %, rate-limit budgets with reset times and ETA-to-empty, last user prompt, plan/tasks summary, and a recent-tool-calls activity feed.

**Architecture:** All code remains in `package main` (single binary). Existing files (`main.go`, `session.go`, `plan.go`, `tasks.go`, `tui.go`) are kept; some are simplified. New files split data-layer concerns from rendering: `events.go` (incremental JSONL tail + event types), `state.go` (state classifier), `budget.go` (context %, windows, burn rate, ETA), `activity.go` (activity feed formatting), `header.go` (header line + git status). Rendering stays orchestrated in `tui.go`. Polling switches from full-file rescan to incremental tail with persistent file offset.

**Tech Stack:** Go 1.26, charmbracelet/bubbletea, charmbracelet/lipgloss. Standard-library `os/exec` for `git status`. No new dependencies.

**Reference:** Spec at `docs/superpowers/specs/2026-04-30-plan-monitor-redesign-design.md`.

---

## Task 1: Investigate rate-limit data sources

**Files:**
- Create: `docs/superpowers/specs/2026-04-30-plan-monitor-redesign-design.md` (already exists; append findings to "Open questions" section)

This is a **research-only task**. The spec flagged five investigation items. We need answers before Task 4 (budget tracker). No code change in this task — just append findings to the spec.

- [ ] **Step 1: Inspect Claude Code credentials/auth state**

Run:
```bash
ls -la ~/.claude/ 2>/dev/null | head -30
test -f ~/.claude/.credentials.json && jq 'keys' ~/.claude/.credentials.json 2>/dev/null
test -f ~/.claude/settings.json && jq 'keys' ~/.claude/settings.json 2>/dev/null
```
Look for: subscription type field, plan tier, rate-limit cap data.

- [ ] **Step 2: Inspect a JSONL for rate-limit headers**

Pick a recent session JSONL and grep for header-shaped data:
```bash
ls -t ~/.claude/projects/*/*.jsonl | head -1 | xargs -I {} sh -c "grep -oE 'ratelimit|rate-limit|x-ratelimit|anthropic-ratelimit' {} | sort -u"
```
Also look at the structure of `assistant` events for usage data:
```bash
ls -t ~/.claude/projects/*/*.jsonl | head -1 | xargs -I {} sh -c "head -200 {} | jq -c 'select(.type==\"assistant\") | .message.usage // empty' 2>/dev/null | head -5"
```

- [ ] **Step 3: Inspect reference tools**

Look at how `claude-monitor` and `ccusage` source data:
```bash
which claude-monitor ccusage
file $(which claude-monitor 2>/dev/null) 2>/dev/null
# If JS/TS tools, look at their source via npm root or by string-extraction:
strings $(which abtop) 2>/dev/null | grep -iE 'ratelimit|claude|anthropic|projects/' | head -20
```

- [ ] **Step 4: Append a findings block to the spec**

Edit `docs/superpowers/specs/2026-04-30-plan-monitor-redesign-design.md` and replace the "Open questions / investigation items" section with a "Resolved data sources" section documenting:
- Where subscription type lives (or "not exposed; assume Max-style")
- Whether usage data is in JSONL `message.usage` (per-turn) and what fields it has
- Whether rate-limit headers are persisted (likely no)
- Decision on cap detection: read-from-JSONL vs. self-track-against-table
- Heuristic chosen for "Awaiting permission" detection

- [ ] **Step 5: Commit findings**

```bash
git add docs/superpowers/specs/2026-04-30-plan-monitor-redesign-design.md
git commit -m "docs(plan-monitor): record rate-limit data-source findings"
```

---

## Task 2: Incremental JSONL event reader

**Files:**
- Create: `events.go`
- Create: `events_test.go`

This is the foundation for state, budget, and activity-feed tasks. Replaces the current `findPlanFileFromJSONL`-style full-file scan with a stateful tail reader that tracks a byte offset between calls and only parses new content.

- [ ] **Step 1: Write failing tests**

Create `events_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEventReaderInitialReadFromEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	preamble := `{"type":"user","message":{"content":"old"}}` + "\n"
	if err := os.WriteFile(path, []byte(preamble), 0o644); err != nil {
		t.Fatal(err)
	}

	r := newEventReader(path)
	r.SeedFromEnd(10) // request last 10 events; only 1 in file → returns 1
	seeded, err := r.Seeded()
	if err != nil {
		t.Fatalf("Seeded error: %v", err)
	}
	if len(seeded) != 1 {
		t.Fatalf("expected 1 seeded event, got %d", len(seeded))
	}
	if seeded[0].Type != "user" {
		t.Errorf("seeded[0].Type = %q, want %q", seeded[0].Type, "user")
	}
}

func TestEventReaderTailReturnsOnlyNewLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"content":"a"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := newEventReader(path)
	r.SeedFromEnd(10)
	if _, err := r.Seeded(); err != nil {
		t.Fatal(err)
	}

	// Append two more events
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"assistant","message":{"content":"b"}}` + "\n")
	f.WriteString(`{"type":"user","message":{"content":"c"}}` + "\n")
	f.Close()

	newEvents, err := r.Tail()
	if err != nil {
		t.Fatalf("Tail error: %v", err)
	}
	if len(newEvents) != 2 {
		t.Fatalf("expected 2 new events, got %d", len(newEvents))
	}
	if newEvents[0].Type != "assistant" || newEvents[1].Type != "user" {
		t.Errorf("got types %q, %q", newEvents[0].Type, newEvents[1].Type)
	}

	// Subsequent Tail with no new bytes returns empty.
	more, err := r.Tail()
	if err != nil {
		t.Fatalf("Tail error: %v", err)
	}
	if len(more) != 0 {
		t.Errorf("expected 0 events on second Tail, got %d", len(more))
	}
}

func TestEventReaderHandlesPartialFinalLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	complete := `{"type":"user","message":{"content":"a"}}` + "\n"
	partial := `{"type":"assistant"`
	if err := os.WriteFile(path, []byte(complete+partial), 0o644); err != nil {
		t.Fatal(err)
	}

	r := newEventReader(path)
	r.SeedFromEnd(10)
	seeded, err := r.Seeded()
	if err != nil {
		t.Fatalf("Seeded error: %v", err)
	}
	if len(seeded) != 1 {
		t.Fatalf("expected 1 complete event, got %d", len(seeded))
	}

	// Finish writing the partial line.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`,"message":{"content":"b"}}` + "\n")
	f.Close()

	newEvents, err := r.Tail()
	if err != nil {
		t.Fatalf("Tail error: %v", err)
	}
	if len(newEvents) != 1 {
		t.Fatalf("expected 1 tailed event, got %d", len(newEvents))
	}
	if newEvents[0].Type != "assistant" {
		t.Errorf("Type = %q, want %q", newEvents[0].Type, "assistant")
	}
}

func TestEventReaderToolUseAndResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	body := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}` + "\n" +
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":false}]}}` + "\n"
	os.WriteFile(path, []byte(body), 0o644)

	r := newEventReader(path)
	r.SeedFromEnd(10)
	events, err := r.Seeded()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if len(events[0].ToolUses) != 1 || events[0].ToolUses[0].Name != "Bash" {
		t.Errorf("expected tool_use Bash, got %+v", events[0].ToolUses)
	}
	if len(events[1].ToolResults) != 1 || events[1].ToolResults[0].ToolUseID != "t1" {
		t.Errorf("expected tool_result for t1, got %+v", events[1].ToolResults)
	}
}
```

- [ ] **Step 2: Run tests, confirm they fail**

```bash
go test ./... -run EventReader -v
```
Expected: FAIL with `undefined: newEventReader`.

- [ ] **Step 3: Implement `events.go`**

```go
package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

// Usage holds the per-turn token usage reported by Claude.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// ToolUse is one tool invocation embedded inside an assistant event.
type ToolUse struct {
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

// ToolResult is the response to a ToolUse, embedded inside a user event.
type ToolResult struct {
	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error"`
	Content   string `json:"-"` // we don't need full content for the panel
}

// Event is the normalized shape we care about. Raw transcript lines have
// many shapes; we project them into this struct.
type Event struct {
	Type        string       // "user" | "assistant" | "system" | other
	Timestamp   string       // RFC3339; empty if not present
	Model       string       // assistant turns only
	UserText    string       // user turns: extracted plain text content
	ToolUses    []ToolUse    // assistant turns may contain N tool_use blocks
	ToolResults []ToolResult // user turns may contain N tool_result blocks
	Usage       *Usage       // assistant turns may have usage data
	RawLine     string       // original JSONL line (for debugging)
}

// EventReader does an incremental tail of a JSONL session log.
type EventReader struct {
	path    string
	offset  int64
	seeded  []Event
	seedErr error
}

func newEventReader(path string) *EventReader {
	return &EventReader{path: path}
}

// SeedFromEnd reads up to maxEvents events from the end of the file and
// positions the offset at EOF, ready for Tail() to return only newly
// appended events.
func (r *EventReader) SeedFromEnd(maxEvents int) {
	f, err := os.Open(r.path)
	if err != nil {
		r.seedErr = err
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		r.seedErr = err
		return
	}

	// Read whole file (for simplicity; sessions are typically a few MB).
	data, err := io.ReadAll(f)
	if err != nil {
		r.seedErr = err
		return
	}
	r.offset = stat.Size()

	events := parseLines(data, true) // true = drop trailing partial line
	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}
	r.seeded = events
}

// Seeded returns the events captured by SeedFromEnd.
func (r *EventReader) Seeded() ([]Event, error) {
	return r.seeded, r.seedErr
}

// Tail reads any bytes appended since the last Tail (or SeedFromEnd) call
// and returns the newly parsed events. A trailing partial line is left for
// the next call.
func (r *EventReader) Tail() ([]Event, error) {
	f, err := os.Open(r.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if _, err := f.Seek(r.offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	// Find last newline; everything after is partial.
	lastNL := -1
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == '\n' {
			lastNL = i
			break
		}
	}
	var consume []byte
	if lastNL == -1 {
		consume = nil // no complete lines this round
	} else {
		consume = data[:lastNL+1]
		r.offset += int64(lastNL + 1)
	}

	if len(consume) == 0 {
		return nil, nil
	}
	return parseLines(consume, false), nil
}

func parseLines(data []byte, dropPartial bool) []Event {
	var events []Event
	scanner := bufio.NewScanner(bytesReader(data))
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	// If dropPartial, drop the last line if file doesn't end with newline.
	hasTrailingNL := len(data) > 0 && data[len(data)-1] == '\n'

	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if dropPartial && !hasTrailingNL && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}

	for _, line := range lines {
		if e, ok := parseEvent(line); ok {
			events = append(events, e)
		}
	}
	return events
}

func bytesReader(b []byte) *bufio.Reader { // small wrapper to avoid bytes.Reader import noise
	return bufio.NewReader(&byteSliceReader{b: b})
}

type byteSliceReader struct{ b []byte; pos int }

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}

func parseEvent(line string) (Event, bool) {
	if line == "" {
		return Event{}, false
	}
	var raw struct {
		Type      string          `json:"type"`
		Timestamp string          `json:"timestamp"`
		Message   json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return Event{}, false
	}

	ev := Event{Type: raw.Type, Timestamp: raw.Timestamp, RawLine: line}

	if len(raw.Message) > 0 {
		var msg struct {
			Model   string          `json:"model"`
			Content json.RawMessage `json:"content"`
			Usage   *Usage          `json:"usage"`
		}
		if err := json.Unmarshal(raw.Message, &msg); err == nil {
			ev.Model = msg.Model
			ev.Usage = msg.Usage
			extractContent(&ev, msg.Content)
		}
	}
	return ev, true
}

func extractContent(ev *Event, content json.RawMessage) {
	if len(content) == 0 {
		return
	}
	// Content can be a plain string or an array of blocks.
	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		ev.UserText = asString
		return
	}
	var blocks []map[string]interface{}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return
	}
	for _, b := range blocks {
		switch b["type"] {
		case "text":
			if s, ok := b["text"].(string); ok && ev.UserText == "" {
				ev.UserText = s
			}
		case "tool_use":
			tu := ToolUse{}
			if id, ok := b["id"].(string); ok {
				tu.ID = id
			}
			if name, ok := b["name"].(string); ok {
				tu.Name = name
			}
			if inp, ok := b["input"].(map[string]interface{}); ok {
				tu.Input = inp
			}
			ev.ToolUses = append(ev.ToolUses, tu)
		case "tool_result":
			tr := ToolResult{}
			if id, ok := b["tool_use_id"].(string); ok {
				tr.ToolUseID = id
			}
			if e, ok := b["is_error"].(bool); ok {
				tr.IsError = e
			}
			ev.ToolResults = append(ev.ToolResults, tr)
		}
	}
}
```

- [ ] **Step 4: Run tests, confirm they pass**

```bash
go test ./... -run EventReader -v
```
Expected: PASS for all four tests.

- [ ] **Step 5: Commit**

```bash
git add events.go events_test.go
git commit -m "feat(plan-monitor): add incremental JSONL event reader"
```

---

## Task 3: Session state classifier

**Files:**
- Create: `state.go`
- Create: `state_test.go`

Derive the current session state (`Idle`, `Thinking`, `Tool: <name>`, `Awaiting`, `Error`, `Compacting`) from the event stream.

- [ ] **Step 1: Write failing tests**

Create `state_test.go`:

```go
package main

import (
	"testing"
	"time"
)

func TestClassifyEmptyStream(t *testing.T) {
	got := classifyState(nil, time.Now())
	if got.Kind != StateIdle {
		t.Errorf("empty stream: got %v, want StateIdle", got.Kind)
	}
}

func TestClassifyLastIsAssistantText(t *testing.T) {
	events := []Event{{Type: "assistant", UserText: "all done"}}
	got := classifyState(events, time.Now())
	if got.Kind != StateIdle {
		t.Errorf("got %v, want StateIdle", got.Kind)
	}
}

func TestClassifyToolInFlight(t *testing.T) {
	events := []Event{
		{Type: "assistant", ToolUses: []ToolUse{{ID: "t1", Name: "Bash"}}},
	}
	got := classifyState(events, time.Now())
	if got.Kind != StateTool || got.ToolName != "Bash" {
		t.Errorf("got %v/%q, want StateTool/Bash", got.Kind, got.ToolName)
	}
}

func TestClassifyToolCompletedNotInFlight(t *testing.T) {
	events := []Event{
		{Type: "assistant", ToolUses: []ToolUse{{ID: "t1", Name: "Bash"}}},
		{Type: "user", ToolResults: []ToolResult{{ToolUseID: "t1"}}},
	}
	got := classifyState(events, time.Now())
	if got.Kind != StateThinking {
		t.Errorf("after tool result with no new assistant turn: got %v, want StateThinking", got.Kind)
	}
}

func TestClassifyAwaitingHeuristic(t *testing.T) {
	now := time.Now()
	events := []Event{
		{Type: "assistant", ToolUses: []ToolUse{{ID: "t1", Name: "Bash"}}, Timestamp: now.Add(-30 * time.Second).Format(time.RFC3339)},
	}
	got := classifyState(events, now)
	if got.Kind != StateAwaiting {
		t.Errorf("stuck tool_use after 30s: got %v, want StateAwaiting", got.Kind)
	}
}

func TestClassifyError(t *testing.T) {
	events := []Event{
		{Type: "assistant", ToolUses: []ToolUse{{ID: "t1", Name: "Bash"}}},
		{Type: "user", ToolResults: []ToolResult{{ToolUseID: "t1", IsError: true}}},
		{Type: "assistant", UserText: "I hit an error"},
	}
	got := classifyState(events, time.Now())
	// Last event is assistant text — that's idle, not error. Errors only
	// surface as a header when the most recent terminal event is an errored
	// tool_result. This test guards against false positives.
	if got.Kind == StateError {
		t.Errorf("assistant text after error tool_result should not be StateError")
	}
}
```

- [ ] **Step 2: Run tests, confirm they fail**

```bash
go test ./... -run Classify -v
```
Expected: FAIL — `undefined: classifyState`.

- [ ] **Step 3: Implement `state.go`**

```go
package main

import "time"

type StateKind int

const (
	StateIdle StateKind = iota
	StateThinking
	StateTool
	StateAwaiting
	StateError
	StateCompacting
)

type State struct {
	Kind     StateKind
	ToolName string // populated for StateTool
	Since    time.Time
}

// awaitingThreshold is how long a tool_use can sit without a matching
// tool_result before we assume it's blocked on user permission.
const awaitingThreshold = 15 * time.Second

func classifyState(events []Event, now time.Time) State {
	if len(events) == 0 {
		return State{Kind: StateIdle, Since: now}
	}

	// Build set of tool_use IDs that have been resolved.
	resolved := map[string]bool{}
	for _, e := range events {
		for _, tr := range e.ToolResults {
			resolved[tr.ToolUseID] = true
		}
	}

	// Walk events newest-first to determine current state.
	last := events[len(events)-1]

	// Find the most recent unresolved tool_use, if any.
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		for _, tu := range e.ToolUses {
			if !resolved[tu.ID] {
				since := parseTimestamp(e.Timestamp)
				if since.IsZero() {
					since = now
				}
				if now.Sub(since) >= awaitingThreshold {
					return State{Kind: StateAwaiting, ToolName: tu.Name, Since: since}
				}
				return State{Kind: StateTool, ToolName: tu.Name, Since: since}
			}
		}
	}

	// No unresolved tool_use. Last event is the signal.
	switch last.Type {
	case "assistant":
		if last.UserText != "" {
			return State{Kind: StateIdle, Since: parseTimestampOr(last.Timestamp, now)}
		}
		return State{Kind: StateThinking, Since: parseTimestampOr(last.Timestamp, now)}
	case "user":
		// Last event is a user turn (likely a tool_result with no new
		// assistant response yet) → Claude is about to think.
		return State{Kind: StateThinking, Since: parseTimestampOr(last.Timestamp, now)}
	}
	return State{Kind: StateIdle, Since: now}
}

func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func parseTimestampOr(s string, fallback time.Time) time.Time {
	t := parseTimestamp(s)
	if t.IsZero() {
		return fallback
	}
	return t
}

func (s State) Label() string {
	switch s.Kind {
	case StateIdle:
		return "Idle"
	case StateThinking:
		return "Thinking"
	case StateTool:
		return "Tool: " + s.ToolName
	case StateAwaiting:
		return "Awaiting"
	case StateError:
		return "Error"
	case StateCompacting:
		return "Compacting"
	}
	return "Unknown"
}
```

- [ ] **Step 4: Run tests, confirm they pass**

```bash
go test ./... -run Classify -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add state.go state_test.go
git commit -m "feat(plan-monitor): classify session state from event stream"
```

---

## Task 4: Budget tracker (context %, rate-limit file reader, burn rate, ETA)

**Files:**
- Create: `budget.go`
- Create: `budget_test.go`

> **Revised after Task 1.** The investigation surfaced a real cap source: `~/.claude/abtop-rate-limits.json` is written by the Claude Code statusline hook every turn and contains `{five_hour, seven_day}.{used_percentage, resets_at}`. We **do not** self-track tokens against a hardcoded plan table. We **do not** track per-turn `usageSamples`. Instead:
>
> - **Cap percentages and reset times:** read from the file. If the file is missing, we degrade (omit line 2).
> - **Burn rate:** rate of change of `five_hour.used_percentage` over the last 15 min, computed from a small ring of `pctSample` snapshots taken on each poll.
> - **ETA-to-empty:** `(100 - usedPct) / burnRatePctPerMin`.
> - **Context %:** still derived from JSONL `message.usage` (per-turn) — that part is unchanged.

- [ ] **Step 1: Write failing tests**

Create `budget_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestContextPercentFromUsage(t *testing.T) {
	model := "claude-opus-4-7"
	u := Usage{InputTokens: 50_000, CacheReadInputTokens: 80_000, OutputTokens: 1_000}
	pct := contextPercent(model, u)
	// budget for opus-4-7 is 200_000; total = 131_000 → 65.5
	if pct < 65 || pct > 66 {
		t.Errorf("contextPercent = %v, want ~65", pct)
	}
}

func TestContextPercentUnknownModel(t *testing.T) {
	u := Usage{InputTokens: 50_000}
	pct := contextPercent("some-future-model", u)
	if pct < 0 {
		t.Errorf("unknown model should default budget; got %v", pct)
	}
}

func TestReadRateLimits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rate-limits.json")
	body := `{"source":"claude","updated_at":1777557367,` +
		`"five_hour":{"used_percentage":9,"resets_at":1777572000},` +
		`"seven_day":{"used_percentage":11,"resets_at":1777928400}}`
	os.WriteFile(path, []byte(body), 0o644)

	rl, err := readRateLimits(path)
	if err != nil {
		t.Fatalf("readRateLimits error: %v", err)
	}
	if rl.FiveHour.UsedPercent != 9 || rl.SevenDay.UsedPercent != 11 {
		t.Errorf("percentages = %d/%d, want 9/11", rl.FiveHour.UsedPercent, rl.SevenDay.UsedPercent)
	}
	if rl.FiveHour.ResetsAt.Unix() != 1777572000 {
		t.Errorf("five_hour ResetsAt = %v, want unix 1777572000", rl.FiveHour.ResetsAt)
	}
	if rl.UpdatedAt.Unix() != 1777557367 {
		t.Errorf("UpdatedAt = %v", rl.UpdatedAt)
	}
}

func TestReadRateLimitsMissingFile(t *testing.T) {
	_, err := readRateLimits("/nonexistent/path.json")
	if err == nil {
		t.Errorf("expected error for missing file")
	}
}

func TestBurnRatePctRolling(t *testing.T) {
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	// pct climbed from 5% to 20% over 15min → 1 pct/min
	samples := []pctSample{
		{at: now.Add(-30 * time.Minute), pct: 2},  // outside window
		{at: now.Add(-15 * time.Minute), pct: 5},
		{at: now.Add(-10 * time.Minute), pct: 10},
		{at: now, pct: 20},
	}
	rate := burnRatePctPerMin(samples, now)
	// (20 - 5) / 15 = 1.0
	if rate < 0.9 || rate > 1.1 {
		t.Errorf("burnRatePctPerMin = %v, want ~1.0", rate)
	}
}

func TestBurnRatePctInsufficientData(t *testing.T) {
	now := time.Now()
	samples := []pctSample{{at: now.Add(-30 * time.Second), pct: 5}}
	rate := burnRatePctPerMin(samples, now)
	if rate != 0 {
		t.Errorf("burnRatePctPerMin with <2min data = %v, want 0 (sentinel)", rate)
	}
}

func TestBurnRatePctNonPositive(t *testing.T) {
	now := time.Now()
	// pct flat or decreasing — not burning
	samples := []pctSample{
		{at: now.Add(-15 * time.Minute), pct: 50},
		{at: now, pct: 50},
	}
	rate := burnRatePctPerMin(samples, now)
	if rate != 0 {
		t.Errorf("flat pct should give 0 burn rate, got %v", rate)
	}
}

func TestETAToEmptyPct(t *testing.T) {
	// 60% used, burn 2 pct/min → (100-60)/2 = 20 min
	eta := etaToEmptyPct(60, 2.0)
	if eta != 20*time.Minute {
		t.Errorf("etaToEmptyPct = %v, want 20m", eta)
	}
}

func TestETAToEmptyPctZeroRate(t *testing.T) {
	if etaToEmptyPct(50, 0) != 0 {
		t.Errorf("zero-rate ETA should be 0 sentinel")
	}
}

func TestETAToEmptyPctAlreadyFull(t *testing.T) {
	if etaToEmptyPct(100, 5) != 0 {
		t.Errorf("at-or-past 100%% should be 0 sentinel")
	}
}
```

- [ ] **Step 2: Run tests, confirm they fail**

```bash
go test ./... -run "ContextPercent|ReadRateLimits|BurnRatePct|ETAToEmptyPct" -v
```
Expected: FAIL — symbols undefined.

- [ ] **Step 3: Implement `budget.go`**

```go
package main

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// Per-model context window budgets (in tokens). Falls back to default if
// model not listed; prefix-match supported (e.g. "claude-opus-4-7-foo").
var modelContextBudget = map[string]int{
	"claude-opus-4-7":     200_000,
	"claude-sonnet-4-6":   200_000,
	"claude-haiku-4-5":    200_000,
	"claude-opus-4-7[1m]": 1_000_000,
}

const defaultContextBudget = 200_000

func contextPercent(model string, u Usage) float64 {
	budget := defaultContextBudget
	for k, v := range modelContextBudget {
		if model == k || strings.HasPrefix(model, k) {
			budget = v
			break
		}
	}
	total := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens + u.OutputTokens
	return 100.0 * float64(total) / float64(budget)
}

// RateLimits is the in-memory shape of ~/.claude/abtop-rate-limits.json.
type RateLimits struct {
	Source    string
	UpdatedAt time.Time
	FiveHour  Window
	SevenDay  Window
}

type Window struct {
	UsedPercent int
	ResetsAt    time.Time
}

// rawRateLimitsFile mirrors the on-disk JSON layout (unix timestamps).
type rawRateLimitsFile struct {
	Source    string `json:"source"`
	UpdatedAt int64  `json:"updated_at"`
	FiveHour  struct {
		UsedPercentage int   `json:"used_percentage"`
		ResetsAt       int64 `json:"resets_at"`
	} `json:"five_hour"`
	SevenDay struct {
		UsedPercentage int   `json:"used_percentage"`
		ResetsAt       int64 `json:"resets_at"`
	} `json:"seven_day"`
}

// readRateLimits parses the abtop-rate-limits.json cache file. The default
// path is ~/.claude/abtop-rate-limits.json; pass an explicit path for
// tests or to override.
func readRateLimits(path string) (RateLimits, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RateLimits{}, err
	}
	var raw rawRateLimitsFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return RateLimits{}, err
	}
	return RateLimits{
		Source:    raw.Source,
		UpdatedAt: time.Unix(raw.UpdatedAt, 0),
		FiveHour: Window{
			UsedPercent: raw.FiveHour.UsedPercentage,
			ResetsAt:    time.Unix(raw.FiveHour.ResetsAt, 0),
		},
		SevenDay: Window{
			UsedPercent: raw.SevenDay.UsedPercentage,
			ResetsAt:    time.Unix(raw.SevenDay.ResetsAt, 0),
		},
	}, nil
}

// pctSample is a snapshot of the five_hour.used_percentage at a point in
// time, captured by the controller on each poll. We use these to compute
// a rolling burn rate.
type pctSample struct {
	at  time.Time
	pct int
}

// burnRatePctPerMin returns percentage-points per minute over the last
// ~15 minutes. Returns 0 as a sentinel for "not enough data" (less than
// 2 minutes of samples) or "rate is zero or negative" (idle / window
// resetting).
func burnRatePctPerMin(samples []pctSample, now time.Time) float64 {
	cutoff := now.Add(-15 * time.Minute)
	var earliest pctSample
	var latest pctSample
	earliestSet := false
	for _, s := range samples {
		if !s.at.After(cutoff) {
			continue
		}
		if !earliestSet || s.at.Before(earliest.at) {
			earliest = s
			earliestSet = true
		}
		if s.at.After(latest.at) {
			latest = s
		}
	}
	if !earliestSet {
		return 0
	}
	span := latest.at.Sub(earliest.at)
	if span < 2*time.Minute {
		return 0
	}
	delta := float64(latest.pct - earliest.pct)
	if delta <= 0 {
		return 0
	}
	return delta / span.Minutes()
}

// etaToEmptyPct projects time until usedPct reaches 100 at the given
// percentage-per-minute burn rate. Returns 0 sentinel if rate is non-
// positive or window is already full.
func etaToEmptyPct(usedPct int, ratePctPerMin float64) time.Duration {
	if ratePctPerMin <= 0 || usedPct >= 100 {
		return 0
	}
	remaining := float64(100 - usedPct)
	mins := remaining / ratePctPerMin
	return time.Duration(mins * float64(time.Minute))
}

// totalTokens returns the per-turn charge from a Usage record.
func totalTokens(u Usage) int {
	return u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens + u.OutputTokens
}

// defaultRateLimitsPath returns the path Claude Code's statusline hook
// writes to. Override with PLAN_MONITOR_RATE_LIMITS_PATH env var.
func defaultRateLimitsPath() string {
	if p := os.Getenv("PLAN_MONITOR_RATE_LIMITS_PATH"); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home + "/.claude/abtop-rate-limits.json"
	}
	return ""
}
```

- [ ] **Step 4: Run tests, confirm they pass**

```bash
go test ./... -run "ContextPercent|WindowUsage|BurnRate|ETAToEmpty" -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add budget.go budget_test.go
git commit -m "feat(plan-monitor): add context/window/burn-rate budget tracker"
```

---

## Task 5: Activity feed line formatter

**Files:**
- Create: `activity.go`
- Create: `activity_test.go`

Format a `ToolUse` into a single feed line. Pure function; no I/O.

- [ ] **Step 1: Write failing tests**

Create `activity_test.go`:

```go
package main

import "testing"

func TestFormatToolBash(t *testing.T) {
	tu := ToolUse{Name: "Bash", Input: map[string]interface{}{"command": "go test ./..."}}
	got := formatTool(tu)
	want := "Bash $ go test ./..."
	if got != want {
		t.Errorf("formatTool(Bash) = %q, want %q", got, want)
	}
}

func TestFormatToolReadWithLines(t *testing.T) {
	tu := ToolUse{Name: "Read", Input: map[string]interface{}{
		"file_path": "/some/path/tui.go", "offset": float64(10), "limit": float64(50),
	}}
	got := formatTool(tu)
	want := "Read tui.go:10-60"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatToolReadNoLines(t *testing.T) {
	tu := ToolUse{Name: "Read", Input: map[string]interface{}{"file_path": "/x/foo.go"}}
	got := formatTool(tu)
	if got != "Read foo.go" {
		t.Errorf("got %q, want %q", got, "Read foo.go")
	}
}

func TestFormatToolGrep(t *testing.T) {
	tu := ToolUse{Name: "Grep", Input: map[string]interface{}{"pattern": "tickMsg", "glob": "*.go"}}
	got := formatTool(tu)
	if got != `Grep "tickMsg" *.go` {
		t.Errorf("got %q", got)
	}
}

func TestFormatToolAgent(t *testing.T) {
	tu := ToolUse{Name: "Task", Input: map[string]interface{}{
		"subagent_type": "Explore", "description": "find session bug",
	}}
	got := formatTool(tu)
	if got != `Agent Explore "find session bug"` {
		t.Errorf("got %q", got)
	}
}

func TestFormatToolMCP(t *testing.T) {
	tu := ToolUse{Name: "mcp__linear__get_issue", Input: map[string]interface{}{}}
	got := formatTool(tu)
	if got != "mcp linear/get_issue" {
		t.Errorf("got %q", got)
	}
}

func TestFormatToolWebFetch(t *testing.T) {
	tu := ToolUse{Name: "WebFetch", Input: map[string]interface{}{"url": "https://docs.anthropic.com/api/foo"}}
	got := formatTool(tu)
	if got != "Web fetch docs.anthropic.com" {
		t.Errorf("got %q", got)
	}
}

func TestTruncateArg(t *testing.T) {
	got := truncateArg("a very long bash command here", 15)
	if got != "a very long ba…" {
		t.Errorf("got %q", got)
	}
}
```

- [ ] **Step 2: Run tests, confirm they fail**

```bash
go test ./... -run "FormatTool|TruncateArg" -v
```
Expected: FAIL — `undefined: formatTool`.

- [ ] **Step 3: Implement `activity.go`**

```go
package main

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// formatTool renders a tool_use into a single short feed line.
// Format: "<ToolName> <arg>". The age, status glyph, and styling are
// applied by the caller.
func formatTool(tu ToolUse) string {
	switch {
	case tu.Name == "Bash":
		cmd, _ := tu.Input["command"].(string)
		return "Bash $ " + cmd
	case tu.Name == "Read":
		fp, _ := tu.Input["file_path"].(string)
		base := filepath.Base(fp)
		off, hasOff := numericInput(tu.Input, "offset")
		lim, hasLim := numericInput(tu.Input, "limit")
		if hasOff && hasLim {
			return fmt.Sprintf("Read %s:%d-%d", base, off, off+lim)
		}
		return "Read " + base
	case tu.Name == "Edit" || tu.Name == "Write":
		fp, _ := tu.Input["file_path"].(string)
		return tu.Name + " " + filepath.Base(fp)
	case tu.Name == "Grep":
		pat, _ := tu.Input["pattern"].(string)
		glob, _ := tu.Input["glob"].(string)
		if glob != "" {
			return fmt.Sprintf("Grep %q %s", pat, glob)
		}
		return fmt.Sprintf("Grep %q", pat)
	case tu.Name == "Glob":
		pat, _ := tu.Input["pattern"].(string)
		return "Glob " + pat
	case tu.Name == "Task" || tu.Name == "Agent":
		st, _ := tu.Input["subagent_type"].(string)
		desc, _ := tu.Input["description"].(string)
		if st == "" {
			st = "Agent"
		}
		return fmt.Sprintf("Agent %s %q", st, desc)
	case tu.Name == "WebFetch":
		raw, _ := tu.Input["url"].(string)
		host := raw
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			host = u.Host
		}
		return "Web fetch " + host
	case tu.Name == "WebSearch":
		q, _ := tu.Input["query"].(string)
		return fmt.Sprintf("Web search %q", q)
	case tu.Name == "Skill":
		name, _ := tu.Input["skill"].(string)
		return "Skill " + name
	case strings.HasPrefix(tu.Name, "mcp__"):
		// mcp__server__tool → mcp server/tool
		rest := strings.TrimPrefix(tu.Name, "mcp__")
		parts := strings.SplitN(rest, "__", 2)
		if len(parts) == 2 {
			return "mcp " + parts[0] + "/" + parts[1]
		}
		return "mcp " + rest
	}
	return tu.Name
}

func numericInput(m map[string]interface{}, key string) (int, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

// truncateArg cuts a string to maxRunes runes, adding "…" if truncated.
// maxRunes counts the ellipsis as one rune.
func truncateArg(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	if maxRunes == 1 {
		return "…"
	}
	return string(r[:maxRunes-1]) + "…"
}
```

- [ ] **Step 4: Run tests, confirm they pass**

```bash
go test ./... -run "FormatTool|TruncateArg" -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add activity.go activity_test.go
git commit -m "feat(plan-monitor): format tool calls into activity feed lines"
```

---

## Task 6: Header line + git status

**Files:**
- Create: `header.go`
- Create: `header_test.go`

Compute the project basename, git branch, and dirty flag for the header line.

- [ ] **Step 1: Write failing tests**

Create `header_test.go`:

```go
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProjectBasename(t *testing.T) {
	if got := projectBasename("/Users/michael/Projects/plan-monitor"); got != "plan-monitor" {
		t.Errorf("got %q", got)
	}
}

func TestGitInfoNoRepo(t *testing.T) {
	info := gitInfo(t.TempDir())
	if info.Branch != "" || info.Dirty {
		t.Errorf("non-repo: %+v", info)
	}
}

func TestGitInfoCleanRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@test")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "commit", "--allow-empty", "-m", "init")
	info := gitInfo(dir)
	if info.Branch != "main" {
		t.Errorf("branch = %q, want main", info.Branch)
	}
	if info.Dirty {
		t.Errorf("clean repo should not be dirty")
	}
}

func TestGitInfoDirtyRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@test")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "commit", "--allow-empty", "-m", "init")
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("hi"), 0o644)
	info := gitInfo(dir)
	if !info.Dirty {
		t.Errorf("dirty repo should be dirty")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
```

- [ ] **Step 2: Run tests, confirm they fail**

```bash
go test ./... -run "ProjectBasename|GitInfo" -v
```
Expected: FAIL.

- [ ] **Step 3: Implement `header.go`**

```go
package main

import (
	"os/exec"
	"path/filepath"
	"strings"
)

type GitStatus struct {
	Branch string
	Dirty  bool
}

func projectBasename(cwd string) string {
	return filepath.Base(cwd)
}

func gitInfo(cwd string) GitStatus {
	branch, err := runGitCmd(cwd, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return GitStatus{}
	}
	out, err := runGitCmd(cwd, "status", "--porcelain")
	dirty := err == nil && strings.TrimSpace(out) != ""
	return GitStatus{Branch: strings.TrimSpace(branch), Dirty: dirty}
}

func runGitCmd(cwd string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	return string(out), err
}
```

- [ ] **Step 4: Run tests, confirm they pass**

```bash
go test ./... -run "ProjectBasename|GitInfo" -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add header.go header_test.go
git commit -m "feat(plan-monitor): add header git status helpers"
```

---

## Task 7: Simplify plan discovery (drop full markdown render)

**Files:**
- Modify: `plan.go`
- Modify: `plan_test.go`

Remove the responsibility of returning full markdown. New return: `(title string, currentStep string)`. The current step is the line of an in-progress task in the plan markdown — for now, parse the first occurrence of `- [⟳]` or `- [ ]` (depending on convention used by superpowers plans). If no in-progress marker is found, return empty current step.

- [ ] **Step 1: Update `plan_test.go` to expect the new signature**

Replace `TestDiscoverPlan` and `TestDiscoverPlanProjectLocal` to assert `(title, currentStep)`:

```go
func TestDiscoverPlan(t *testing.T) {
	tmpDir := t.TempDir()
	plansDir := filepath.Join(tmpDir, "plans")
	os.MkdirAll(plansDir, 0o755)
	jsonlPath := filepath.Join(tmpDir, "session.jsonl")

	planContent := "# My Test Plan\n\n- [x] Step 1\n- [ ] **Step 2: do the thing**\n- [ ] Step 3\n"
	os.WriteFile(filepath.Join(plansDir, "test-plan.md"), []byte(planContent), 0o644)

	content := `{"input":{"file_path":"` + plansDir + `/test-plan.md"}}` + "\n"
	os.WriteFile(jsonlPath, []byte(content), 0o644)

	title, step := discoverPlan(plansDir, jsonlPath, "")
	if title != "My Test Plan" {
		t.Errorf("title = %q", title)
	}
	if step != "Step 2: do the thing" {
		t.Errorf("step = %q, want %q", step, "Step 2: do the thing")
	}
}

func TestDiscoverPlanNoUncheckedSteps(t *testing.T) {
	tmpDir := t.TempDir()
	plansDir := filepath.Join(tmpDir, "plans")
	os.MkdirAll(plansDir, 0o755)
	jsonlPath := filepath.Join(tmpDir, "session.jsonl")

	planContent := "# Done Plan\n- [x] Step 1\n- [x] Step 2\n"
	os.WriteFile(filepath.Join(plansDir, "done.md"), []byte(planContent), 0o644)

	content := `{"input":{"file_path":"` + plansDir + `/done.md"}}` + "\n"
	os.WriteFile(jsonlPath, []byte(content), 0o644)

	title, step := discoverPlan(plansDir, jsonlPath, "")
	if title != "Done Plan" {
		t.Errorf("title = %q", title)
	}
	if step != "" {
		t.Errorf("step should be empty, got %q", step)
	}
}
```

Remove `TestDiscoverPlanNoPlanInSession`'s reliance on the `content` second return; update it to expect both empty strings. Same for `TestDiscoverPlanProjectLocal` — assert title only.

- [ ] **Step 2: Run tests, confirm failures**

```bash
go test ./... -run Discover -v
```
Expected: FAIL — return-arity mismatch.

- [ ] **Step 3: Update `plan.go`**

Replace `discoverPlan` body so the return is `(title, currentStep string)`. Remove the markdown-content path.

```go
func discoverPlan(plansDir string, jsonlPath string, cwd string) (title string, currentStep string) {
	planPath := findPlanFileFromJSONL(jsonlPath, plansDir, cwd)
	if planPath == "" {
		return "", ""
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		return "", ""
	}
	planContent := string(data)

	for _, line := range strings.Split(planContent, "\n") {
		if strings.HasPrefix(line, "# ") && title == "" {
			title = strings.TrimPrefix(line, "# ")
		}
		if currentStep == "" {
			if step := extractCurrentStep(line); step != "" {
				currentStep = step
			}
		}
	}
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(planPath), ".md")
	}
	return title, currentStep
}

// extractCurrentStep returns the text of the first unchecked task line in
// markdown, e.g. "- [ ] Step 2: do thing" → "Step 2: do thing". Empty
// string when the line isn't an unchecked task.
func extractCurrentStep(line string) string {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "- [ ]") {
		return ""
	}
	rest := strings.TrimSpace(strings.TrimPrefix(t, "- [ ]"))
	// Strip leading bold markers like **Step 2: ...**
	rest = strings.TrimPrefix(rest, "**")
	rest = strings.TrimSuffix(rest, "**")
	return rest
}
```

- [ ] **Step 4: Update `tui.go` to consume the new signature**

This is just a stub change to keep the build green — full UI wiring happens in Task 9. For now, in `tui.go`:

```go
// In the model struct, remove `planRendered`. Keep `planTitle`. Add:
//   planStep string

// In dataMsg, replace planRendered with planStep.

// In pollData(), replace:
//   title, content := discoverPlan(...)
//   var rendered string
//   if content != "" { rendered, _ = renderMarkdown(content, width-4) }
// with:
//   title, step := discoverPlan(plansDir, jsonlPath, cwd)

// In Update's dataMsg branch, replace planRendered assignment with planStep.

// In View(), comment out the planRendered rendering block (or leave a stub).
//   The full new layout lands in Task 9.
```

- [ ] **Step 5: Run all tests**

```bash
go test ./...
go build ./...
```
Expected: PASS, build succeeds.

- [ ] **Step 6: Commit**

```bash
git add plan.go plan_test.go tui.go
git commit -m "refactor(plan-monitor): drop plan markdown render, return current step"
```

---

## Task 8: Tasks progress bar helper

**Files:**
- Modify: `tasks.go`
- Modify: `tasks_test.go`

Add a helper to render a unicode block progress bar, and a helper to cap the visible task list to N entries (always including in-progress tasks).

- [ ] **Step 1: Write failing tests**

Append to `tasks_test.go`:

```go
func TestProgressBar(t *testing.T) {
	cases := []struct{ done, total, width int; want string }{
		{4, 7, 7, "▰▰▰▰▱▱▱"},
		{0, 5, 5, "▱▱▱▱▱"},
		{5, 5, 5, "▰▰▰▰▰"},
		{0, 0, 5, "▱▱▱▱▱"},
	}
	for _, c := range cases {
		got := progressBar(c.done, c.total, c.width)
		if got != c.want {
			t.Errorf("progressBar(%d,%d,%d) = %q, want %q", c.done, c.total, c.width, got, c.want)
		}
	}
}

func TestCapTasksKeepsInProgress(t *testing.T) {
	tasks := []task{
		{ID: "1", Status: "completed"},
		{ID: "2", Status: "completed"},
		{ID: "3", Status: "completed"},
		{ID: "4", Status: "completed"},
		{ID: "5", Status: "in_progress"},
		{ID: "6", Status: "pending"},
		{ID: "7", Status: "pending"},
	}
	visible, hidden := capTasks(tasks, 3)
	if hidden != 4 {
		t.Errorf("hidden = %d, want 4", hidden)
	}
	// Must include the in_progress task even if it would otherwise be cut.
	foundInProgress := false
	for _, t := range visible {
		if t.Status == "in_progress" {
			foundInProgress = true
		}
	}
	if !foundInProgress {
		t.Errorf("visible tasks must include in_progress task")
	}
	if len(visible) > 3 {
		t.Errorf("visible exceeds cap: %d", len(visible))
	}
}
```

- [ ] **Step 2: Run tests, confirm they fail**

```bash
go test ./... -run "ProgressBar|CapTasks" -v
```
Expected: FAIL.

- [ ] **Step 3: Implement helpers in `tasks.go`**

```go
// progressBar renders `done/total` filled out of `width` cells using
// unicode block characters. Empty total is treated as zero progress.
func progressBar(done, total, width int) string {
	filled := 0
	if total > 0 {
		filled = done * width / total
	}
	var b strings.Builder
	for i := 0; i < width; i++ {
		if i < filled {
			b.WriteString("▰")
		} else {
			b.WriteString("▱")
		}
	}
	return b.String()
}

// capTasks returns up to `cap` tasks plus the count of hidden tasks.
// Always includes any in-progress task even if it falls outside the cap.
func capTasks(tasks []task, cap int) (visible []task, hidden int) {
	if len(tasks) <= cap {
		return tasks, 0
	}
	// Reserve slots for in-progress tasks.
	var inProgress []task
	for _, t := range tasks {
		if t.Status == "in_progress" {
			inProgress = append(inProgress, t)
		}
	}
	// Fill the rest in order, skipping in-progress (added separately).
	rest := make([]task, 0, cap)
	for _, t := range tasks {
		if t.Status == "in_progress" {
			continue
		}
		if len(rest)+len(inProgress) >= cap {
			break
		}
		rest = append(rest, t)
	}
	visible = append(rest, inProgress...)
	hidden = len(tasks) - len(visible)
	return visible, hidden
}
```

- [ ] **Step 4: Run tests, confirm pass**

```bash
go test ./... -run "ProgressBar|CapTasks" -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tasks.go tasks_test.go
git commit -m "feat(plan-monitor): add progress bar and task-list cap helpers"
```

---

## Task 9: Wire status block + last prompt into `tui.go`

**Files:**
- Modify: `tui.go`

This is the first big rendering integration. Replace the existing layout with the new top sections: header, status line 1 (state + model + ctx %), status line 2 (budgets + ETA), last prompt. Plan/tasks/activity/footer come in subsequent tasks.

State held by the model expands to track the event reader and accumulated usage samples.

- [ ] **Step 1: Extend `model` struct fields**

In `tui.go`, replace the `model` struct with:

```go
type model struct {
	// Config
	tasksDir  string
	plansDir  string
	jsonlPath string
	cwd       string
	sessionID string

	// Persistent state
	reader         *EventReader
	allEvents      []Event       // bounded ring (cap 1000)
	rateLimitsPath string        // ~/.claude/abtop-rate-limits.json or override
	pctSamples     []pctSample   // 5h-window snapshots over time, for burn-rate

	// Latest snapshot
	state        State
	modelName    string  // most recent model from JSONL
	contextPct   float64
	rateLimits   RateLimits // last successful read of rateLimitsPath
	rateOK       bool       // true if rateLimits is fresh and parsed
	planTitle    string
	planStep     string
	tasks        []task
	lastPrompt   string
	gitStatus    GitStatus

	// UI
	lastUpdate time.Time
	width      int
	height     int
	ready      bool
	polling    bool
	err        error
}
```

- [ ] **Step 2: Update `newModel` to initialize reader and rate-limits source**

```go
func newModel(tasksDir, plansDir, jsonlPath, cwd, sessionID string) model {
	r := newEventReader(jsonlPath)
	r.SeedFromEnd(500)
	seeded, _ := r.Seeded()

	m := model{
		tasksDir:       tasksDir,
		plansDir:       plansDir,
		jsonlPath:      jsonlPath,
		cwd:            cwd,
		sessionID:      sessionID,
		reader:         r,
		allEvents:      seeded,
		rateLimitsPath: defaultRateLimitsPath(),
		gitStatus:      gitInfo(cwd),
	}
	m.recomputeFromEvents(time.Now())
	return m
}

func (m *model) recomputeFromEvents(now time.Time) {
	m.state = classifyState(m.allEvents, now)
	for i := len(m.allEvents) - 1; i >= 0; i-- {
		if m.allEvents[i].Model != "" {
			m.modelName = m.allEvents[i].Model
			break
		}
	}
	if last := lastUserPrompt(m.allEvents); last != "" {
		m.lastPrompt = last
	}
	if last := lastUsage(m.allEvents); last != nil {
		m.contextPct = contextPercent(m.modelName, *last)
	}
}

func sampleAlreadyAt(samples []pctSample, at time.Time) bool {
	for _, s := range samples {
		if s.at.Equal(at) {
			return true
		}
	}
	return false
}

func lastUserPrompt(events []Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Type != "user" {
			continue
		}
		// Skip events whose user "message" is just tool_results.
		if len(e.ToolResults) > 0 && e.UserText == "" {
			continue
		}
		if e.UserText != "" {
			return e.UserText
		}
	}
	return ""
}

func lastUsage(events []Event) *Usage {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Usage != nil {
			return events[i].Usage
		}
	}
	return nil
}
```

- [ ] **Step 3: Replace `pollData` with an incremental version**

```go
func (m model) pollData() tea.Cmd {
	reader := m.reader
	tasksDir := m.tasksDir
	plansDir := m.plansDir
	jsonlPath := m.jsonlPath
	cwd := m.cwd
	rlPath := m.rateLimitsPath
	return func() tea.Msg {
		newEvents, _ := reader.Tail()
		tasks, _ := readTasks(tasksDir)
		title, step := discoverPlan(plansDir, jsonlPath, cwd)
		git := gitInfo(cwd)
		rl, rlErr := readRateLimits(rlPath)
		return dataMsg{
			time:         time.Now(),
			newEvents:    newEvents,
			tasks:        tasks,
			planTitle:    title,
			planStep:     step,
			gitStatus:    git,
			rateLimits:   rl,
			rateLimitErr: rlErr,
		}
	}
}

type dataMsg struct {
	time         time.Time
	newEvents    []Event
	tasks        []task
	planTitle    string
	planStep     string
	gitStatus    GitStatus
	rateLimits   RateLimits
	rateLimitErr error
}
```

- [ ] **Step 4: Update `Update` for dataMsg**

```go
case dataMsg:
	m.polling = false
	m.tasks = msg.tasks
	m.planTitle = msg.planTitle
	m.planStep = msg.planStep
	m.gitStatus = msg.gitStatus
	if len(msg.newEvents) > 0 {
		m.allEvents = append(m.allEvents, msg.newEvents...)
		// Cap ring at 1000.
		if len(m.allEvents) > 1000 {
			m.allEvents = m.allEvents[len(m.allEvents)-1000:]
		}
	}
	if msg.rateLimitErr == nil {
		m.rateLimits = msg.rateLimits
		m.rateOK = true
		// Append a fresh percent sample if it differs from the last one
		// or no samples exist yet. Cap retained samples at 1 hour.
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
	m.recomputeFromEvents(msg.time)
	m.lastUpdate = msg.time
```

- [ ] **Step 5: Replace `View()` body — top sections only for now**

Keep tasks rendering in place as a stub at the bottom; activity feed comes in Task 10. New View top:

```go
func (m model) View() string {
	if !m.ready {
		return "Loading..."
	}

	var b strings.Builder

	// Header
	b.WriteString(renderHeader(m.cwd, m.gitStatus))
	b.WriteString("\n")

	// Status line 1
	b.WriteString(renderStatusLine1(m.state, m.modelName, m.contextPct, time.Now()))
	b.WriteString("\n")

	// Status line 2 (budgets) — only when we have a fresh rate-limits read
	if m.rateOK {
		line2 := renderStatusLine2(m.rateLimits, m.pctSamples, time.Now())
		if line2 != "" {
			b.WriteString(line2)
			b.WriteString("\n")
		}
	}

	// Last prompt
	if m.lastPrompt != "" {
		b.WriteString("\n")
		b.WriteString(renderLastPrompt(m.lastPrompt, m.width))
		b.WriteString("\n")
	}

	// Plan summary
	if m.planTitle != "" {
		b.WriteString("\n")
		b.WriteString(headerStyle.Render("Plan: " + m.planTitle))
		b.WriteString("\n")
		if m.planStep != "" {
			b.WriteString(inProgressStyle.Render("  ⟳ " + m.planStep))
			b.WriteString("\n")
		}
	}

	// Tasks (existing rendering, unchanged for now)
	completed, total := taskCounts(m.tasks)
	b.WriteString("\n")
	bar := progressBar(completed, total, 7)
	b.WriteString(headerStyle.Render(fmt.Sprintf("Tasks %s %d/%d", bar, completed, total)))
	b.WriteString("\n")
	visible, hidden := capTasks(m.tasks, 10)
	for _, t := range visible {
		b.WriteString(renderTaskLine(t))
		b.WriteString("\n")
	}
	if hidden > 0 {
		b.WriteString(pendingStyle.Render(fmt.Sprintf("  …and %d more", hidden)))
		b.WriteString("\n")
	}

	// Footer (placeholder — Task 11 finalizes)
	b.WriteString("\n")
	elapsed := time.Since(m.lastUpdate).Truncate(time.Second)
	shortID := m.sessionID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	b.WriteString(footerStyle.Render(fmt.Sprintf("sess %s · upd %s", shortID, elapsed)))

	return b.String()
}

func renderTaskLine(t task) string {
	switch t.Status {
	case "completed":
		return completedStyle.Render("  ✓ " + t.Subject)
	case "in_progress":
		s := t.Subject
		if t.ActiveForm != "" {
			s = t.ActiveForm
		}
		return inProgressStyle.Render("  ⟳ " + s)
	default:
		return pendingStyle.Render("  ○ " + t.Subject)
	}
}
```

- [ ] **Step 6: Implement renderHeader, renderStatusLine1, renderStatusLine2, renderLastPrompt**

Add to `tui.go` (or a new `render.go` file, your call — keep them grouped near `View`):

```go
var (
	dotIdle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#04B575")).Render("●")
	dotThinking = lipgloss.NewStyle().Foreground(lipgloss.Color("#3B82F6")).Render("●")
	dotTool     = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFCC00")).Render("●")
	dotError    = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Render("●")
	dotCompact  = lipgloss.NewStyle().Foreground(lipgloss.Color("#A855F7")).Render("●")
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	branchStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	dirtyBranch = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFCC00"))
	promptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Italic(true)
)

func renderHeader(cwd string, g GitStatus) string {
	prefix := titleStyle.Render("▸ plan-monitor")
	name := projectBasename(cwd)
	if g.Branch == "" {
		return prefix + " · " + name
	}
	branchStr := "(" + g.Branch + ")"
	if g.Dirty {
		branchStr = dirtyBranch.Render(branchStr)
	} else {
		branchStr = branchStyle.Render(branchStr)
	}
	return prefix + " · " + name + " " + branchStr
}

func renderStatusLine1(s State, model string, ctxPct float64, now time.Time) string {
	var dot string
	switch s.Kind {
	case StateIdle:
		dot = dotIdle
	case StateThinking:
		dot = dotThinking
	case StateTool:
		dot = dotTool
	case StateAwaiting, StateError:
		dot = dotError
	case StateCompacting:
		dot = dotCompact
	default:
		dot = dotIdle
	}

	durStr := "0:00"
	if !s.Since.IsZero() {
		durStr = formatDuration(now.Sub(s.Since))
	}
	modelShort := shortModel(model)

	ctxColor := lipgloss.NewStyle()
	if ctxPct >= 85 {
		ctxColor = ctxColor.Foreground(lipgloss.Color("#EF4444"))
	} else if ctxPct >= 70 {
		ctxColor = ctxColor.Foreground(lipgloss.Color("#FFCC00"))
	}
	ctxStr := ctxColor.Render(fmt.Sprintf("ctx %d%%", int(ctxPct+0.5)))

	return fmt.Sprintf("%s %s %s · %s · %s", dot, s.Label(), durStr, modelShort, ctxStr)
}

func renderStatusLine2(rl RateLimits, pctSamples []pctSample, now time.Time) string {
	rate := burnRatePctPerMin(pctSamples, now)
	var etaStr string
	switch {
	case rate == 0:
		etaStr = "measuring…"
	default:
		eta := etaToEmptyPct(rl.FiveHour.UsedPercent, rate)
		// "safe" if projected exhaustion is past both reset times
		if eta == 0 || now.Add(eta).After(rl.FiveHour.ResetsAt) {
			etaStr = "safe until reset"
		} else {
			etaStr = "empty in " + formatDuration(eta)
		}
	}

	return fmt.Sprintf("5h %d%% → %s · wk %d%% → %s · %s",
		rl.FiveHour.UsedPercent, rl.FiveHour.ResetsAt.Local().Format("3:04p"),
		rl.SevenDay.UsedPercent, rl.SevenDay.ResetsAt.Local().Format("Mon"),
		etaStr)
}

func renderLastPrompt(text string, width int) string {
	max := width - len("You: ") - 2
	if max < 30 {
		max = 30
	}
	one := strings.ReplaceAll(text, "\n", " ")
	return promptStyle.Render("You: " + truncateArg(one, max))
}

func shortModel(m string) string {
	switch {
	case strings.Contains(m, "opus"):
		return "opus"
	case strings.Contains(m, "sonnet"):
		return "sonnet"
	case strings.Contains(m, "haiku"):
		return "haiku"
	case m == "":
		return "—"
	default:
		return m
	}
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
```

> **Note on degraded mode:** When `~/.claude/abtop-rate-limits.json` is missing or unreadable, `m.rateOK` is false and line 2 is omitted. Context % on line 1 still works since it's derived from JSONL. This matches the spec's degraded-mode philosophy: better to omit line 2 than show wrong numbers.

- [ ] **Step 7: Build, run all tests, manually launch**

```bash
go build ./...
go test ./...
```
Then run the binary in a real Claude Code session and visually confirm the new top sections render and update.

- [ ] **Step 8: Commit**

```bash
git add tui.go
git commit -m "feat(plan-monitor): wire status block, header, last prompt"
```

---

## Task 10: Wire activity feed into `tui.go`

**Files:**
- Modify: `tui.go`

Render the last ~7 tool calls below the tasks section, newest at top.

- [ ] **Step 1: Add activity-feed builder helper**

In `tui.go`:

```go
type feedEntry struct {
	tu      ToolUse
	at      time.Time
	status  feedStatus
	summary string // formatTool result, possibly with " (denied)" / " (exit N)"
}

type feedStatus int

const (
	feedRunning feedStatus = iota
	feedSuccess
	feedFailed
	feedDenied
)

// buildActivityFeed walks events newest-first, collecting tool_use events
// paired with their tool_results. Returns up to `cap` entries.
func buildActivityFeed(events []Event, cap int) []feedEntry {
	// Map tool_use_id → result.
	results := map[string]ToolResult{}
	for _, e := range events {
		for _, tr := range e.ToolResults {
			results[tr.ToolUseID] = tr
		}
	}
	var feed []feedEntry
	for i := len(events) - 1; i >= 0 && len(feed) < cap; i-- {
		e := events[i]
		for _, tu := range e.ToolUses {
			at := parseTimestamp(e.Timestamp)
			st := feedSuccess
			if tr, ok := results[tu.ID]; ok {
				if tr.IsError {
					st = feedFailed
				}
			} else {
				st = feedRunning
			}
			feed = append(feed, feedEntry{
				tu: tu, at: at, status: st, summary: formatTool(tu),
			})
			if len(feed) >= cap {
				break
			}
		}
	}
	return feed
}

func renderActivityFeed(feed []feedEntry, now time.Time, width int) string {
	if len(feed) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(headerStyle.Render("Activity"))
	b.WriteString("\n")
	maxArg := width - 12 // " GLYPH AGE  " prefix budget
	if maxArg < 20 {
		maxArg = 20
	}
	for _, fe := range feed {
		var glyph string
		switch fe.status {
		case feedRunning:
			glyph = inProgressStyle.Render("⟳")
		case feedSuccess:
			glyph = completedStyle.Render("✓")
		case feedFailed, feedDenied:
			glyph = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Render("✗")
		}
		age := "—"
		if !fe.at.IsZero() {
			age = formatAge(now.Sub(fe.at))
		}
		summary := truncateArg(fe.summary, maxArg)
		b.WriteString(fmt.Sprintf("  %s %3s   %s\n", glyph, age, summary))
	}
	return b.String()
}

func formatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < 5*time.Minute {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return "5m+"
}
```

- [ ] **Step 2: Insert activity feed into `View()` between tasks and footer**

```go
// after tasks section in View():
feed := buildActivityFeed(m.allEvents, 7)
if rendered := renderActivityFeed(feed, time.Now(), m.width); rendered != "" {
	b.WriteString("\n")
	b.WriteString(rendered)
}
```

- [ ] **Step 3: Run tests + build + manual smoke**

```bash
go test ./...
go build ./...
```

- [ ] **Step 4: Commit**

```bash
git add tui.go
git commit -m "feat(plan-monitor): render activity feed of recent tool calls"
```

---

## Task 11: Footer with error counter

**Files:**
- Modify: `tui.go`

Replace placeholder footer with `sess <id> · upd <Ns> · N errors` (errors only when > 0, in red).

- [ ] **Step 1: Add error counter**

```go
func countErrors(events []Event) int {
	n := 0
	for _, e := range events {
		for _, tr := range e.ToolResults {
			if tr.IsError {
				n++
			}
		}
	}
	return n
}
```

- [ ] **Step 2: Replace footer block in `View()`**

```go
// Footer
b.WriteString("\n")
elapsed := time.Since(m.lastUpdate).Truncate(time.Second)
shortID := m.sessionID
if len(shortID) > 8 {
	shortID = shortID[:8]
}
errCount := countErrors(m.allEvents)
errStr := ""
if errCount > 0 {
	errStr = " · " + lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Render(fmt.Sprintf("%d error%s", errCount, plural(errCount)))
}
b.WriteString(footerStyle.Render(fmt.Sprintf("sess %s · upd %s%s", shortID, elapsed, errStr)))

// helper
func plural(n int) string { if n == 1 { return "" }; return "s" }
```

- [ ] **Step 3: Build + commit**

```bash
go build ./...
go test ./...
git add tui.go
git commit -m "feat(plan-monitor): footer with session error counter"
```

---

## Task 12: Manual end-to-end verification + screenshot

**Files:**
- None (verification task)

- [ ] **Step 1: Run plan-monitor against a real active session**

```bash
go build -o /tmp/plan-monitor . && /tmp/plan-monitor
```

Verify each section renders:
- Header shows `▸ plan-monitor · plan-monitor (main)` with branch dimmed (or yellow if dirty)
- Status line 1 dot color matches Claude's actual state right now
- Status line 2 shows real percentages and an ETA (or `measuring…` early on)
- Last prompt shows your most recent message, truncated
- Plan section shows the right title and current step (or absent if none)
- Tasks section has the progress bar
- Activity feed lists recent tool calls newest-first with correct glyphs
- Footer shows session id, elapsed time, error count if any

- [ ] **Step 2: Force each state, confirm dot color**

- Idle: send a message and wait for Claude to finish, confirm green dot
- Thinking: send a message, watch dot turn blue while assistant streams
- Tool: while a Bash runs, confirm yellow dot + `Tool: Bash`
- Error: cause a tool error (e.g. typo in command), then send another message — verify error count in footer increments

- [ ] **Step 3: Run tests one last time**

```bash
go test ./... -v
```
Expected: all PASS, no skipped tests except the git ones if git is missing.

- [ ] **Step 4: Final commit if anything was tweaked**

```bash
git add .
git diff --cached --stat
git commit -m "chore(plan-monitor): final polish from manual verification" || true
```

---

## Notes for the implementer

- **YAGNI:** the spec deliberately omits scrolling, tabs, drill-downs, plan-markdown rendering, and most keyboard input. Resist adding them.
- **DRY:** the rendering helpers (`renderHeader`, `renderStatusLine1`, etc.) are intentionally pure functions taking primitive inputs so they can be unit-tested later without TUI plumbing. Keep them that way.
- **Frequent commits:** every task ends in a commit. Don't batch.
- **Cap data is read from `~/.claude/abtop-rate-limits.json`** (resolved in Task 1). When the file is missing, `m.rateOK` is false and line 2 is omitted entirely — degraded-mode behavior per the spec.
- **Subagent JSONLs are out of scope.** When `Agent` is in flight, just show `Agent <type> "<desc>"` — don't try to follow the child session.
