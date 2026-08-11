# Background Work State Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop reporting a session as `Idle` while background work it launched (async agents, `run_in_background` shells) is still running, so the switchboard's conductor stops ferrying the user into sessions that are not waiting on them.

**Architecture:** The head pairs launch markers found in `tool_result` text against `<task-id>`s in task-notification events, accumulating outstanding ids in model state as new events arrive. A new `StateBackground` kind overrides `StateIdle` only. `isWaiting()` already treats unrecognized published values as not-waiting, so the conductor needs no change.

**Tech Stack:** Go, standard library `regexp`/`encoding/json`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-11-background-work-state-design.md`

## Global Constraints

- Work in the existing worktree `.claude/worktrees/align-context-meters`, branch `worktree-align-context-meters`. Do not create another worktree.
- No new module dependencies.
- Every task ends green: `go build ./... && go vet ./... && go test ./cmd/claudemux-head/` and `gofmt -l .` empty.
- Comments explain **why**, matching the density of surrounding code.
- Never match the literal substring `task-notification` anywhere in text. It appears in ordinary skill prose. Recognition is always a **prefix** check on the whole payload.
- Nothing `classifyState` gets right today may change: foreground agents keep classifying as `Tool:<name>` through the unresolved-`tool_use` path.

## File Structure

| File | Responsibility |
|---|---|
| `cmd/claudemux-head/events.go` (modify) | Populate `ToolResult.Content`; expose `queue-operation`'s top-level `content` as `Event.QueueText`. |
| `cmd/claudemux-head/bgwork.go` (create) | Launch/completion extraction and the outstanding-task tracker. |
| `cmd/claudemux-head/bgwork_test.go` (create) | Tests for both, with fixtures copied from real transcripts. |
| `cmd/claudemux-head/state.go` (modify) | `StateBackground`, its `Label()`, and the `classifyState` signature. |
| `cmd/claudemux-head/statepub.go` (modify) | `statePublishValue` renders `Background:<n>`. |
| `cmd/claudemux-head/tui.go` (modify) | Model field, feeding the tracker from `dataMsg`, resetting on `switchSession`. |

---

### Task 1: Expose the two payloads the parser drops

**Files:**
- Modify: `cmd/claudemux-head/events.go` (`ToolResult` ~line 55, `Event` ~line 62, `parseEvent` ~line 225, `extractContent` ~line 292)
- Test: `cmd/claudemux-head/events_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `ToolResult.Content string` — the tool_result's text, concatenated when the payload is an array of blocks. Currently declared but never populated.
  - `Event.QueueText string` — the top-level `content` of a `queue-operation` event, `""` for every other type.
  - `flattenText(raw json.RawMessage) string` — used by both.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/claudemux-head/events_test.go`:

```go
// A background shell's launch result is a bare string; an async agent's is an
// array of text blocks. Both carry the id the tracker pairs on, so both shapes
// must survive parsing.
func TestParseEventToolResultContent(t *testing.T) {
	line := `{"type":"user","timestamp":"2026-08-11T10:00:00Z","message":{"content":[` +
		`{"type":"tool_result","tool_use_id":"toolu_1","content":"Command running in background with ID: boigiwsir. Output is being written to: /tmp/x"}` +
		`]}}`
	ev, ok := parseEvent(line)
	if !ok || len(ev.ToolResults) != 1 {
		t.Fatalf("parse failed: ok=%v results=%d", ok, len(ev.ToolResults))
	}
	if !strings.Contains(ev.ToolResults[0].Content, "boigiwsir") {
		t.Errorf("Content = %q, want the string payload", ev.ToolResults[0].Content)
	}
}

func TestParseEventToolResultBlockContent(t *testing.T) {
	line := `{"type":"user","timestamp":"2026-08-11T10:00:00Z","message":{"content":[` +
		`{"type":"tool_result","tool_use_id":"toolu_2","content":[{"type":"text","text":"Async agent launched successfully.\nagentId: afbbf7a8f9ee52e81 (internal ID)"}]}` +
		`]}}`
	ev, ok := parseEvent(line)
	if !ok || len(ev.ToolResults) != 1 {
		t.Fatalf("parse failed: ok=%v results=%d", ok, len(ev.ToolResults))
	}
	if !strings.Contains(ev.ToolResults[0].Content, "afbbf7a8f9ee52e81") {
		t.Errorf("Content = %q, want the flattened block text", ev.ToolResults[0].Content)
	}
}

// A finished background task's notification lands FIRST as a queue-operation
// carrying its payload at the top level, not under message.content.
func TestParseEventQueueText(t *testing.T) {
	line := `{"type":"queue-operation","operation":"enqueue","timestamp":"2026-08-11T10:00:00Z",` +
		`"content":"<task-notification>\n<task-id>boigiwsir</task-id>\n<status>completed</status>\n</task-notification>"}`
	ev, ok := parseEvent(line)
	if !ok {
		t.Fatal("parse failed")
	}
	if !strings.HasPrefix(ev.QueueText, "<task-notification>") {
		t.Errorf("QueueText = %q, want the top-level notification payload", ev.QueueText)
	}
}

// Ordinary events must not grow a QueueText.
func TestParseEventNoQueueTextForOtherTypes(t *testing.T) {
	ev, _ := parseEvent(`{"type":"user","timestamp":"2026-08-11T10:00:00Z","message":{"content":"hello"}}`)
	if ev.QueueText != "" {
		t.Errorf("QueueText = %q, want empty for a user event", ev.QueueText)
	}
}
```

Ensure that file imports `strings`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestParseEvent(ToolResult|QueueText|NoQueueText)' -v`
Expected: FAIL — `Content` is empty on both tool_result tests; `ev.QueueText undefined`.

- [ ] **Step 3: Add the field and the flattener**

In `cmd/claudemux-head/events.go`, add to `Event` after `Cwd`:

```go
	Cwd         string // transcript's per-entry cwd; tracks worktree moves
	// QueueText is the top-level `content` of a queue-operation event, which
	// is where a finished background task's notification arrives first — the
	// delivered user turn only follows when the session next runs. Empty for
	// every other event type.
	QueueText string
```

And add the flattener next to `cleanCommandText`:

```go
// flattenText renders a content field that may be a bare string or an array of
// blocks into one string. Both shapes occur on tool_result: a background shell
// launch returns a string, an async agent launch returns a text block array.
func flattenText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}
```

- [ ] **Step 4: Populate both in the parser**

In `parseEvent`, add `Content` to the raw struct and set `QueueText`:

```go
	var raw struct {
		Type        string          `json:"type"`
		IsMeta      bool            `json:"isMeta"`
		Timestamp   string          `json:"timestamp"`
		Cwd         string          `json:"cwd"`
		IsSidechain bool            `json:"isSidechain"`
		Message     json.RawMessage `json:"message"`
		Content     json.RawMessage `json:"content"` // top level; queue-operation only
		LastPrompt  string          `json:"lastPrompt"` // present on type=last-prompt events
	}
```

Then after the `last-prompt` branch:

```go
	if raw.Type == "queue-operation" {
		ev.QueueText = flattenText(raw.Content)
	}
```

And in `extractContent`, populate the tool_result body:

```go
		case "tool_result":
			var tr ToolResult
			if err := json.Unmarshal(raw, &tr); err == nil {
				// Content is tagged `json:"-"` because the payload is not
				// always a string — flatten whichever shape arrived.
				var body struct {
					Content json.RawMessage `json:"content"`
				}
				if json.Unmarshal(raw, &body) == nil {
					tr.Content = flattenText(body.Content)
				}
				ev.ToolResults = append(ev.ToolResults, tr)
			}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -v 2>&1 | tail -10`
Expected: PASS, including every pre-existing parser test.

- [ ] **Step 6: Commit**

```bash
git add cmd/claudemux-head/events.go cmd/claudemux-head/events_test.go
git commit -m "feat(head): parse tool_result text and queue-operation payloads"
```

---

### Task 2: Extract launches and completions from events

**Files:**
- Create: `cmd/claudemux-head/bgwork.go`
- Create: `cmd/claudemux-head/bgwork_test.go`

**Interfaces:**
- Consumes: `ToolResult.Content`, `Event.QueueText`, `Event.UserText` (Task 1).
- Produces:
  - `bgLaunches(e Event) []string` — ids of background tasks this event launched.
  - `bgCompletions(e Event) []string` — ids this event reports finished.

- [ ] **Step 1: Write the failing test**

Create `cmd/claudemux-head/bgwork_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestBgLaunches(t *testing.T) {
	tests := []struct {
		name string
		ev   Event
		want string
	}{
		{
			"background shell",
			Event{ToolResults: []ToolResult{{Content: "Command running in background with ID: boigiwsir. Output is being written to: /tmp/x"}}},
			"boigiwsir",
		},
		{
			"async agent",
			Event{ToolResults: []ToolResult{{Content: "Async agent launched successfully. (…)\nagentId: afbbf7a8f9ee52e81 (internal ID - do not mention)"}}},
			"afbbf7a8f9ee52e81",
		},
		{
			"an ordinary tool result launches nothing",
			Event{ToolResults: []ToolResult{{Content: "total 42\ndrwxr-xr-x  bin"}}},
			"",
		},
		{
			"prose about background work is not a launch",
			Event{ToolResults: []ToolResult{{Content: "the job is running in background somewhere, no ID here"}}},
			"",
		},
		{"no tool results", Event{Type: "assistant"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bgLaunches(tt.ev)
			if tt.want == "" {
				if len(got) != 0 {
					t.Errorf("bgLaunches = %q, want none", got)
				}
				return
			}
			if len(got) != 1 || got[0] != tt.want {
				t.Errorf("bgLaunches = %q, want [%s]", got, tt.want)
			}
		})
	}
}

func TestBgCompletions(t *testing.T) {
	const payload = "<task-notification>\n<task-id>boigiwsir</task-id>\n" +
		"<tool-use-id>toolu_01VSdCK</tool-use-id>\n<status>completed</status>\n</task-notification>"

	t.Run("queue-operation form", func(t *testing.T) {
		got := bgCompletions(Event{Type: "queue-operation", QueueText: payload})
		if len(got) != 1 || got[0] != "boigiwsir" {
			t.Errorf("bgCompletions = %q, want [boigiwsir]", got)
		}
	})

	t.Run("delivered user turn form", func(t *testing.T) {
		got := bgCompletions(Event{Type: "user", UserText: payload})
		if len(got) != 1 || got[0] != "boigiwsir" {
			t.Errorf("bgCompletions = %q, want [boigiwsir]", got)
		}
	})

	// The literal string appears in ordinary skill documentation. Matching it
	// mid-text would retire tasks that never completed.
	t.Run("prose mentioning the tag is not a completion", func(t *testing.T) {
		prose := "Monitor fires `<task-notification>` messages and wakes this loop. " +
			"See <task-id>boigiwsir</task-id> in the docs."
		if got := bgCompletions(Event{Type: "user", UserText: prose}); len(got) != 0 {
			t.Errorf("bgCompletions = %q, want none: prose is not a notification", got)
		}
	})

	t.Run("notification without a task id", func(t *testing.T) {
		if got := bgCompletions(Event{Type: "user", UserText: "<task-notification>\n<status>failed</status>"}); len(got) != 0 {
			t.Errorf("bgCompletions = %q, want none", got)
		}
	})
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run 'TestBg(Launches|Completions)' -v`
Expected: FAIL — `undefined: bgLaunches`, `undefined: bgCompletions`.

- [ ] **Step 3: Write the implementation**

Create `cmd/claudemux-head/bgwork.go`:

```go
package main

import (
	"regexp"
	"strings"
	"time"
)

// Tracking work a session launched and then stopped waiting on: async agents
// and run_in_background shells. Both resolve their tool_use at LAUNCH, so the
// main thread's turn ends and classifyState would otherwise call the session
// Idle — which isWaiting reads as "waiting on the human", sending the
// switchboard's conductor into a session that is busy.
//
// Launches are recognized by the text of the tool_result that started them;
// completions by the notification that carries the same id back. See
// docs/superpowers/specs/2026-08-11-background-work-state-design.md for the
// verified event shapes.

var (
	bgShellRe  = regexp.MustCompile(`running in background with ID: ([A-Za-z0-9]+)`)
	bgAgentRe  = regexp.MustCompile(`agentId: ([A-Za-z0-9]+)`)
	bgTaskIDRe = regexp.MustCompile(`<task-id>([A-Za-z0-9]+)</task-id>`)
)

// bgNotificationPrefix opens a task-notification payload. Recognition is a
// PREFIX check, never a substring search: the literal tag appears in ordinary
// prose — skill documentation quotes it — and a substring match would retire
// tasks that are still running.
const bgNotificationPrefix = "<task-notification>"

// bgLaunches returns the ids of background tasks this event launched.
func bgLaunches(e Event) []string {
	var ids []string
	for _, tr := range e.ToolResults {
		for _, re := range []*regexp.Regexp{bgShellRe, bgAgentRe} {
			if m := re.FindStringSubmatch(tr.Content); m != nil {
				ids = append(ids, m[1])
			}
		}
	}
	return ids
}

// bgCompletions returns the ids this event reports as finished. Both delivery
// forms count: the queue-operation that lands the moment the task ends, and the
// user turn carrying the same payload once the session wakes to consume it.
// Retiring an id twice is harmless, and handling both means the feature
// survives either form going away.
func bgCompletions(e Event) []string {
	var ids []string
	for _, text := range []string{e.QueueText, e.UserText} {
		if !strings.HasPrefix(strings.TrimSpace(text), bgNotificationPrefix) {
			continue
		}
		if m := bgTaskIDRe.FindStringSubmatch(text); m != nil {
			ids = append(ids, m[1])
		}
	}
	return ids
}
```

Note `time` is imported for Task 3; if the build complains about an unused import at this step, add it in Task 3 instead.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/claudemux-head/ -run 'TestBg(Launches|Completions)' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/bgwork.go cmd/claudemux-head/bgwork_test.go
git commit -m "feat(head): extract background task launches and completions"
```

---

### Task 3: Track outstanding tasks with expiry

**Files:**
- Modify: `cmd/claudemux-head/bgwork.go`
- Test: `cmd/claudemux-head/bgwork_test.go`

**Interfaces:**
- Consumes: `bgLaunches`, `bgCompletions` (Task 2); `genuinePrompt(e Event) bool` (`tui.go:500`); `parseTimestampOr(s string, fallback time.Time) time.Time` (`state.go:100`).
- Produces:

```go
type bgTracker struct{ tasks map[string]time.Time }

func newBgTracker() bgTracker
func (b *bgTracker) observe(events []Event, now time.Time)
func (b *bgTracker) outstanding(now time.Time) (count int, oldest time.Time)
```

- [ ] **Step 1: Write the failing test**

Add to `cmd/claudemux-head/bgwork_test.go`:

```go
func bgLaunchEvent(id, ts string) Event {
	return Event{
		Type:        "user",
		Timestamp:   ts,
		ToolResults: []ToolResult{{Content: "Command running in background with ID: " + id + ". Output is being written to: /tmp/x"}},
	}
}

func bgDoneEvent(id string) Event {
	return Event{Type: "queue-operation", QueueText: "<task-notification>\n<task-id>" + id + "</task-id>\n<status>completed</status>"}
}

func TestBgTrackerPairsLaunchAndCompletion(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe([]Event{bgLaunchEvent("aaa", "2026-08-11T10:00:00Z")}, now)
	if n, _ := b.outstanding(now); n != 1 {
		t.Fatalf("outstanding = %d, want 1 after a launch", n)
	}
	b.observe([]Event{bgDoneEvent("aaa")}, now)
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0 after its completion", n)
	}
}

func TestBgTrackerCountsAndOldest(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 10, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe([]Event{
		bgLaunchEvent("aaa", "2026-08-11T10:00:00Z"),
		bgLaunchEvent("bbb", "2026-08-11T10:05:00Z"),
	}, now)
	n, oldest := b.outstanding(now)
	if n != 2 {
		t.Errorf("outstanding = %d, want 2", n)
	}
	if want := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC); !oldest.Equal(want) {
		t.Errorf("oldest = %v, want %v: the duration must read as how long work has been out", oldest, want)
	}
	// Retiring the older one moves the clock to the survivor.
	b.observe([]Event{bgDoneEvent("aaa")}, now)
	if _, oldest = b.outstanding(now); !oldest.Equal(time.Date(2026, 8, 11, 10, 5, 0, 0, time.UTC)) {
		t.Errorf("oldest = %v, want the surviving launch", oldest)
	}
}

// A task that never notifies must not mark the session busy forever — that
// would make the conductor refuse to ever visit it.
func TestBgTrackerExpiresStaleLaunches(t *testing.T) {
	b := newBgTracker()
	b.observe([]Event{bgLaunchEvent("aaa", "2026-08-11T10:00:00Z")}, time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC))
	late := time.Date(2026, 8, 11, 10, 31, 0, 0, time.UTC)
	if n, _ := b.outstanding(late); n != 0 {
		t.Errorf("outstanding = %d, want 0: a launch past the cap stops counting", n)
	}
}

// If the human typed at the session, whatever it was tracking is moot.
func TestBgTrackerClearedByGenuinePrompt(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe([]Event{bgLaunchEvent("aaa", "2026-08-11T10:00:00Z")}, now)
	b.observe([]Event{{Type: "user", UserText: "what's up?"}}, now)
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0 after a real prompt", n)
	}
}

// The delivered notification turn is a user event, but it is not the human
// typing — it must not be mistaken for one.
func TestBgTrackerNotificationTurnIsNotAPrompt(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe([]Event{
		bgLaunchEvent("aaa", "2026-08-11T10:00:00Z"),
		bgLaunchEvent("bbb", "2026-08-11T10:00:00Z"),
	}, now)
	b.observe([]Event{{Type: "user", UserText: "<task-notification>\n<task-id>aaa</task-id>"}}, now)
	if n, _ := b.outstanding(now); n != 1 {
		t.Errorf("outstanding = %d, want 1: the notification retires its own task, not the set", n)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run TestBgTracker -v`
Expected: FAIL — `undefined: newBgTracker`.

- [ ] **Step 3: Write the implementation**

Append to `cmd/claudemux-head/bgwork.go`:

```go
// bgMaxAge is how long an unfinished launch keeps counting. A task that never
// notifies — killed, crashed, or launched before a head restart — would
// otherwise mark the session busy forever, and the conductor would never visit
// it again: a worse bug than the one this fixes. Thirty minutes is longer than
// any agent this has been observed to run and short enough to self-heal within
// one sitting.
const bgMaxAge = 30 * time.Minute

// bgTracker holds the background tasks a session has launched and not yet seen
// finish, keyed by task id and stamped with the launch time.
//
// Accumulated from each poll's NEW events rather than recomputed from the event
// ring: allEvents is capped at 1000, so a busy session scrolls a long-running
// task's launch out while it is still running, and a recompute would silently
// call the session Idle again.
type bgTracker struct {
	tasks map[string]time.Time
}

func newBgTracker() bgTracker {
	return bgTracker{tasks: map[string]time.Time{}}
}

// observe folds one batch of new events into the tracker.
func (b *bgTracker) observe(events []Event, now time.Time) {
	if b.tasks == nil {
		b.tasks = map[string]time.Time{}
	}
	for _, e := range events {
		// A prompt the human actually typed retires everything: they are
		// looking at the session, so what it was waiting on no longer decides
		// whether it needs them. genuinePrompt already rejects the delivered
		// notification turns (their text opens with "<").
		if genuinePrompt(e) {
			b.tasks = map[string]time.Time{}
			continue
		}
		for _, id := range bgCompletions(e) {
			delete(b.tasks, id)
		}
		for _, id := range bgLaunches(e) {
			b.tasks[id] = parseTimestampOr(e.Timestamp, now)
		}
	}
}

// outstanding reports how many launches are still counting and when the oldest
// of them started. Entries past bgMaxAge are dropped as they are found, so a
// stale tracker heals itself without a separate sweep.
func (b *bgTracker) outstanding(now time.Time) (int, time.Time) {
	var oldest time.Time
	for id, at := range b.tasks {
		if now.Sub(at) > bgMaxAge {
			delete(b.tasks, id)
			continue
		}
		if oldest.IsZero() || at.Before(oldest) {
			oldest = at
		}
	}
	return len(b.tasks), oldest
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/claudemux-head/ -run TestBg -v`
Expected: PASS (all tracker and extraction tests).

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/bgwork.go cmd/claudemux-head/bgwork_test.go
git commit -m "feat(head): track outstanding background tasks with expiry"
```

---

### Task 4: The Background state

**Files:**
- Modify: `cmd/claudemux-head/state.go` (`StateKind` ~line 7, `classifyState` ~line 22, `Label` ~line 108)
- Modify: `cmd/claudemux-head/statepub.go` (`statePublishValue` ~line 24)
- Modify: `cmd/claudemux-head/tui.go:374` (the single `classifyState` call site)
- Test: `cmd/claudemux-head/state_test.go`, `cmd/claudemux-head/statepub_test.go`

**Interfaces:**
- Consumes: nothing from Task 3 directly — the count and oldest are passed in.
- Produces:
  - `StateBackground` kind; `State.BgCount int`.
  - `classifyState(events []Event, bgCount int, bgOldest time.Time, now time.Time) State` — **signature change**; the existing call site and every existing test call must be updated to pass `0, time.Time{}`.
  - `Label()` renders `Working <n>`; `statePublishValue` renders `Background:<n>`.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/claudemux-head/state_test.go`:

```go
// The bug this fixes: the main thread ended its turn while work it launched is
// still running, and the session reported Idle — which isWaiting reads as
// "waiting on the human".
func TestClassifyStateBackgroundOverridesIdle(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 10, 0, 0, time.UTC)
	oldest := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	events := []Event{{Type: "assistant", Timestamp: "2026-08-11T10:09:00Z", UserText: "Kicked that off in the background."}}

	if got := classifyState(events, 0, time.Time{}, now); got.Kind != StateIdle {
		t.Errorf("Kind = %v, want StateIdle with no background work", got.Kind)
	}
	got := classifyState(events, 2, oldest, now)
	if got.Kind != StateBackground {
		t.Errorf("Kind = %v, want StateBackground", got.Kind)
	}
	if got.BgCount != 2 {
		t.Errorf("BgCount = %d, want 2", got.BgCount)
	}
	if !got.Since.Equal(oldest) {
		t.Errorf("Since = %v, want the oldest launch %v", got.Since, oldest)
	}
}

// A foreground tool is the more specific truth and already classifies
// correctly; background work must not mask it.
func TestClassifyStateToolBeatsBackground(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 10, 0, 0, time.UTC)
	events := []Event{{
		Type:      "assistant",
		Timestamp: "2026-08-11T10:09:00Z",
		ToolUses:  []ToolUse{{ID: "toolu_1", Name: "Bash"}},
	}}
	got := classifyState(events, 3, now.Add(-time.Minute), now)
	if got.Kind != StateTool || got.ToolName != "Bash" {
		t.Errorf("got %v/%q, want StateTool/Bash", got.Kind, got.ToolName)
	}
}

func TestBackgroundLabel(t *testing.T) {
	if got := (State{Kind: StateBackground, BgCount: 2}).Label(); got != "Working 2" {
		t.Errorf("Label = %q, want %q", got, "Working 2")
	}
}
```

Add to `cmd/claudemux-head/statepub_test.go`:

```go
func TestStatePublishValueBackground(t *testing.T) {
	got := statePublishValue(State{Kind: StateBackground, BgCount: 2})
	if got != "Background:2" {
		t.Errorf("statePublishValue = %q, want Background:2", got)
	}
	// The conductor's rule is exact-match on the waiting values, so an
	// unrecognized value is already not-waiting. Assert it, because that is
	// what keeps switchboard.go unchanged.
	if isWaiting(got) {
		t.Error("a session with background work must not count as waiting")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestClassifyStateBackground|TestClassifyStateTool|TestBackgroundLabel|TestStatePublishValueBackground' -v`
Expected: FAIL — `undefined: StateBackground`, and `classifyState` takes 2 arguments.

- [ ] **Step 3: Add the kind and the field**

In `cmd/claudemux-head/state.go`:

```go
const (
	StateIdle StateKind = iota
	StateThinking
	StateTool
	StateAwaiting
	StateError
	StateCompacting
	// StateBackground: the main thread's turn ended, but work it launched —
	// an async agent, a run_in_background shell — is still running. Distinct
	// from Idle because Idle means "waiting on the human", and this session
	// is not.
	StateBackground
)

type State struct {
	Kind     StateKind
	ToolName string // populated for StateTool / StateAwaiting
	Since    time.Time
	BgCount  int // outstanding background tasks; populated for StateBackground
}
```

Add to `Label()`:

```go
	case StateBackground:
		return "Working " + strconv.Itoa(s.BgCount)
```

Import `strconv` in `state.go`.

- [ ] **Step 4: Change the signature and apply the override**

In `classifyState`, take the two new parameters and apply the override at the one place `StateIdle` is returned for a finished turn:

```go
func classifyState(events []Event, bgCount int, bgOldest time.Time, now time.Time) State {
```

Then in the `"assistant"` branch, replace the idle return:

```go
	case "assistant":
		if last.UserText != "" {
			idle := State{Kind: StateIdle, Since: parseTimestampOr(last.Timestamp, now)}
			return bgOverride(idle, bgCount, bgOldest)
		}
		return State{Kind: StateThinking, Since: parseTimestampOr(last.Timestamp, now)}
```

And apply it to the other two idle exits — the empty-event and no-conversation-event returns stay `Idle` (a session with no events at all has launched nothing), but the trailing `return State{Kind: StateIdle, Since: now}` at the end of the function must go through the override too:

```go
	return bgOverride(State{Kind: StateIdle, Since: now}, bgCount, bgOldest)
```

Add the helper to `state.go`:

```go
// bgOverride upgrades an Idle verdict to Background when the session has work
// outstanding. Only Idle is overridden: an unresolved foreground tool_use is
// the more specific truth and already classifies correctly, and Thinking is
// not-waiting either way.
func bgOverride(s State, count int, oldest time.Time) State {
	if s.Kind != StateIdle || count <= 0 {
		return s
	}
	since := oldest
	if since.IsZero() {
		since = s.Since
	}
	return State{Kind: StateBackground, Since: since, BgCount: count}
}
```

In `statepub.go`, add to `statePublishValue`:

```go
	case StateBackground:
		return "Background:" + strconv.Itoa(s.BgCount)
```

Import `strconv` in `statepub.go`.

- [ ] **Step 5: Update every existing caller**

`cmd/claudemux-head/tui.go:374` becomes (the real values arrive in Task 5):

```go
	m.state = classifyState(m.allEvents, 0, time.Time{}, now)
```

Then fix the compile errors in `state_test.go` and any other test calling `classifyState`, passing `0, time.Time{}` for the new parameters.

Run: `go build ./... && go test ./cmd/claudemux-head/ 2>&1 | head -20` and repeat until it compiles.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -v 2>&1 | tail -15`
Expected: PASS, including every pre-existing state test — `Idle`, `Thinking`, and `Tool` classification are unchanged when no background work is passed.

- [ ] **Step 7: Commit**

```bash
git add cmd/claudemux-head/state.go cmd/claudemux-head/statepub.go cmd/claudemux-head/tui.go cmd/claudemux-head/state_test.go cmd/claudemux-head/statepub_test.go
git commit -m "feat(head): add the Background state for outstanding work"
```

---

### Task 5: Feed the tracker from the poll

**Files:**
- Modify: `cmd/claudemux-head/tui.go` (model fields ~line 170, `recomputeFromEvents` ~line 373, `switchSession` ~line 417, the `dataMsg` case ~line 879)
- Test: `cmd/claudemux-head/tui_test.go`

**Interfaces:**
- Consumes: `bgTracker` (Task 3), `classifyState`'s new signature (Task 4).
- Produces: model field `bg bgTracker`, wired end to end.

- [ ] **Step 1: Write the failing test**

Add to `cmd/claudemux-head/tui_test.go`:

```go
// End to end through the model: a launch arriving on a poll, with the turn
// already ended, must publish Background rather than Idle.
func TestModelBackgroundStateFromPoll(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 10, 0, 0, time.UTC)
	m := model{bg: newBgTracker()}
	events := []Event{
		bgLaunchEvent("aaa", "2026-08-11T10:00:00Z"),
		{Type: "assistant", Timestamp: "2026-08-11T10:01:00Z", UserText: "Kicked that off in the background."},
	}
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
	m.bg.observe([]Event{bgLaunchEvent("aaa", "2026-08-11T10:00:00Z")}, now)
	m.switchSession(path, now)
	if n, _ := m.bg.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0: a rotated session starts clean", n)
	}
}
```

`tui_test.go` already imports `os`, `path/filepath`, and `time`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestModelBackgroundStateFromPoll|TestSwitchSessionResetsBackgroundWork' -v`
Expected: FAIL — `unknown field bg in struct literal`.

- [ ] **Step 3: Add the field and use it**

In `cmd/claudemux-head/tui.go`, add to the model near `allEvents`:

```go
	allEvents      []Event     // bounded ring (cap 1000)
	// bg holds background tasks this session launched and has not seen finish.
	// Accumulated from new events as they arrive — see bgTracker for why it
	// cannot be recomputed from allEvents.
	bg bgTracker
```

Set it in `newModel` alongside the other initialized fields:

```go
	bg: newBgTracker(),
```

In `recomputeFromEvents`, pass the outstanding work in:

```go
func (m *model) recomputeFromEvents(now time.Time) {
	bgCount, bgOldest := m.bg.outstanding(now)
	m.state = classifyState(m.allEvents, bgCount, bgOldest, now)
```

In `switchSession`, reset it where the other derived state is discarded:

```go
	m.bg = newBgTracker()
```

- [ ] **Step 4: Feed it from the poll**

In the `dataMsg` case, feed new events to the tracker where they are appended to the ring — before the ring trim, so nothing is missed:

```go
		if len(msg.newEvents) > 0 {
			m.bg.observe(msg.newEvents, msg.time)
			m.allEvents = append(m.allEvents, msg.newEvents...)
			if len(m.allEvents) > 1000 {
				m.allEvents = m.allEvents[len(m.allEvents)-1000:]
			}
		}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -v 2>&1 | tail -15`
Expected: PASS.

- [ ] **Step 6: Verify the whole package**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: no `gofmt` output, `ok github.com/mquinnv/claudemux/cmd/claudemux-head`.

- [ ] **Step 7: See it for real**

```bash
go install ./cmd/claudemux-head
```

Then, in any claudemux session, run a background command (`sleep 60 &` via a `run_in_background` tool call) and end the turn. The head's state line must read `● Working 1` rather than `● Idle`, and:

```bash
tmux show-option -t <that-session> -v @claudemux_state
```

must print `Background:1`. Confirm the lobby row shows it too, and that the conductor does not ferry a client into that session while it is working.

- [ ] **Step 8: Commit**

```bash
git add cmd/claudemux-head/tui.go cmd/claudemux-head/tui_test.go
git commit -m "feat(head): report sessions with outstanding background work as Working"
```

---

## Self-Review

**Spec coverage:**

| Spec section | Task |
|---|---|
| `ToolResult.Content` populated, both payload shapes | 1 |
| `queue-operation` top-level content exposed | 1 |
| Launch markers (shell + agent) | 2 |
| Completion recognized by prefix, both delivery forms | 2 |
| Prose containing the tag must not register | 2 (test) |
| Incremental accumulation, not ring recompute | 3 |
| 30-minute expiry | 3 |
| Cleared by a genuine prompt | 3 |
| `StateBackground` overrides Idle only | 4 |
| `Since` = oldest launch | 3 (tracker), 4 (override) |
| `Working <n>` label, `Background:<n>` published | 4 |
| `isWaiting` unchanged, conductor untouched | 4 (test asserts it) |
| Reset on session rotation | 5 |

**Placeholder scan:** none — every step carries the code or the exact command it needs.

**Type consistency:** `bgTracker`/`newBgTracker`/`observe`/`outstanding` are defined in Task 3 and used with those names in Task 5. `classifyState(events, bgCount, bgOldest, now)` is defined in Task 4 and called that way in Tasks 4 and 5. `State.BgCount` is defined in Task 4 and read in Tasks 4 and 5. `bgLaunchEvent`/`bgDoneEvent` are defined in Task 3's test file and reused in Task 5's tests — both are in package `main`, so they are visible across test files.
