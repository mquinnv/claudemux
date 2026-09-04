# Unsure State and continued-in Following Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the conductor escorting a human into a session that only *looks* idle: (1) when the head gives up counting background work on a timer rather than seeing it finish, publish a distinct "Unsure" state the conductor does not treat as waiting; (2) when the harness parks a session and continues it in a new transcript (`continued-in`), follow that record to the successor file instead of reading a dead transcript forever.

**Architecture:** Both changes live in the head (`cmd/claudemux-head`). The background tracker (`bgwork.go`) already distinguishes "retired by completion" from "dropped by expiry" internally; we make it remember expiries until the next conversation turn, and `classifyState` turns that memory into a new `StateUnsure` kind that publishes as `Unsure:N`. Separately, `parseEvent` learns the `continued-in` record's successor id, the model remembers `old session id -> successor id`, and `pollData` resolves both the pane-mapped transcript and the current binding through that map so the existing rotation path (`switchSession`) adopts the successor.

**Tech Stack:** Go 1.2x, Bubble Tea TUI, tmux session options, Go `testing` with the existing fixture helpers in `bgwork_test.go`.

**Spec:** No separate spec. The design was agreed in conversation on 2026-09-04 and is recorded in Task 6's addendum to `docs/superpowers/specs/2026-08-11-background-work-state-design.md`. Background: the ag-admin head counted one backgrounded shell for exactly the 30-minute shell cap, then published `Idle` anchored at the old Stop; the conductor visited; then a "fleet" attach parked the session into a daemon fork whose transcript the head never adopted because the pane map still named the old file.

## Global Constraints

- Every `go test ./...` must pass after every task. Run from the worktree root: `/Users/michael/Projects/claudemux/.claude/worktrees/fix-idle-with-live-subagents`.
- `gofmt` clean. Run `gofmt -l cmd/` and expect no output before each commit.
- Published state strings are an interface consumed by `isWaiting` (`switchboard.go`) with exact matching. `Unsure:N` must NOT be added to `isWaiting`.
- No text-pattern detection of launches (see the header comment of `bgwork.go`). This plan adds none.
- Commit after each task with the trailer lines:
  ```
  Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_018PTqLkgJPvqT6tBX4KSxKd
  ```
- Never `git stash`. Never touch the main checkout at `/Users/michael/Projects/claudemux`.

---

### Task 1: Tracker remembers expiries until the next turn

**Files:**
- Modify: `cmd/claudemux-head/bgwork.go` (struct `bgTracker`, `observe`, `outstanding`)
- Test: `cmd/claudemux-head/bgwork_test.go`

**Interfaces:**
- Consumes: existing `bgTracker.observe(events []Event, now time.Time)`, `bgTracker.outstanding(now time.Time) (int, time.Time)`, `bgTracker.alive(...)`.
- Produces: `func (b *bgTracker) unsure() int` — how many launches were dropped by expiry (not by a completion) since the last conversation turn. Task 2 wires this into `classifyState`.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/claudemux-head/bgwork_test.go`:

```go
// An expiry is a guess, not a fact: the tracker stops counting the task but
// remembers that it gave up, so the head can publish doubt instead of a
// confident Idle. This is the case that sent the conductor into ag-admin on
// 2026-09-04 — a hung ssh past the 30-minute shell cap.
func TestBgTrackerRemembersExpiry(t *testing.T) {
	b := newBgTracker()
	b.observe(bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"), time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC))
	late := time.Date(2026, 8, 11, 10, 31, 0, 0, time.UTC)
	if n, _ := b.outstanding(late); n != 0 {
		t.Fatalf("outstanding = %d, want 0", n)
	}
	if got := b.unsure(); got != 1 {
		t.Errorf("unsure = %d, want 1: an expiry must be remembered", got)
	}
	// Still remembered on the next poll — outstanding is called every tick.
	if n, _ := b.outstanding(late.Add(time.Second)); n != 0 {
		t.Fatalf("outstanding = %d, want 0", n)
	}
	if got := b.unsure(); got != 1 {
		t.Errorf("unsure = %d after second poll, want 1", got)
	}
}

// A completion is a fact: retiring a task through its notification leaves
// no doubt behind.
func TestBgTrackerCompletionLeavesNoDoubt(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe(bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"), now)
	b.observe([]Event{{Type: "user", Timestamp: "2026-08-11T10:05:00Z",
		UserText: "<task-notification>\n<task-id>aaa</task-id>\n<status>completed</status>\n</task-notification>"}}, now)
	if n, _ := b.outstanding(now.Add(6 * time.Minute)); n != 0 {
		t.Fatalf("outstanding = %d, want 0", n)
	}
	if got := b.unsure(); got != 0 {
		t.Errorf("unsure = %d, want 0: a completion is not a guess", got)
	}
}

// Doubt clears the moment the conversation moves on: a user or assistant
// event newer than the expiry proves the human (or Claude) engaged, and a
// later Stop is then a real one. Bookkeeping events do not count — the
// harness writes attachments and snapshots without anyone present.
func TestBgTrackerDoubtClearsOnNewTurn(t *testing.T) {
	b := newBgTracker()
	b.observe(bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"), time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC))
	late := time.Date(2026, 8, 11, 10, 31, 0, 0, time.UTC)
	b.outstanding(late)
	if got := b.unsure(); got != 1 {
		t.Fatalf("unsure = %d, want 1", got)
	}
	b.observe([]Event{{Type: "attachment", Timestamp: "2026-08-11T10:32:00Z"}}, late.Add(time.Minute))
	if got := b.unsure(); got != 1 {
		t.Errorf("unsure = %d after bookkeeping event, want 1", got)
	}
	b.observe([]Event{{Type: "user", Timestamp: "2026-08-11T10:33:00Z", UserText: "how's it going?"}}, late.Add(2*time.Minute))
	if got := b.unsure(); got != 0 {
		t.Errorf("unsure = %d after a new user turn, want 0", got)
	}
}

// An event OLDER than the expiry cannot clear it — a reseed replays history
// that predates the drop.
func TestBgTrackerDoubtIgnoresOlderTurns(t *testing.T) {
	b := newBgTracker()
	b.observe(bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"), time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC))
	late := time.Date(2026, 8, 11, 10, 31, 0, 0, time.UTC)
	b.outstanding(late)
	b.observe([]Event{{Type: "assistant", Timestamp: "2026-08-11T10:01:00Z", UserText: "old text"}}, late.Add(time.Second))
	if got := b.unsure(); got != 1 {
		t.Errorf("unsure = %d, want 1: an older turn does not resolve a later expiry", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/claudemux-head -run 'TestBgTrackerRemembersExpiry|TestBgTrackerCompletionLeavesNoDoubt|TestBgTrackerDoubtClearsOnNewTurn|TestBgTrackerDoubtIgnoresOlderTurns' -v`
Expected: compile error `b.unsure undefined`.

- [ ] **Step 3: Implement**

In `cmd/claudemux-head/bgwork.go`, add two fields to `bgTracker` (after `subagentsDir`):

```go
	// expired counts launches that outstanding dropped because their
	// liveness regime gave up on them — a cap or a stalled transcript —
	// never because a completion arrived. expiredAt is when the latest such
	// drop happened.
	//
	// An expiry is a guess about work the head can no longer see. Publishing
	// a confident Idle on it is what sent the conductor into a session
	// whose pane still read "2 shells still running" (2026-09-04, a hung
	// ssh past bgShellMaxAge). classifyState turns a non-zero count into
	// StateUnsure, which isWaiting does not match. The doubt clears on the
	// next conversation event newer than expiredAt (see observe): a human
	// prompt or an assistant turn means someone engaged, and the Stop that
	// follows is a real one.
	expired   int
	expiredAt time.Time
```

In `observe`, inside the `for _, e := range events` loop, immediately after `at := parseTimestampOr(e.Timestamp, now)`:

```go
		// A newer conversation event resolves any doubt an expiry left —
		// bookkeeping types (attachment, system, snapshots) are written with
		// nobody present and do not count.
		if b.expired > 0 && (e.Type == "user" || e.Type == "assistant") && at.After(b.expiredAt) {
			b.expired = 0
			b.expiredAt = time.Time{}
		}
```

In `outstanding`, replace the `if !b.alive(id, task, now) { delete(b.tasks, id); continue }` block with:

```go
		if !b.alive(id, task, now) {
			delete(b.tasks, id)
			b.expired++
			b.expiredAt = now
			continue
		}
```

Add the accessor after `outstanding`:

```go
// unsure reports how many launches the tracker gave up on since the last
// conversation turn — dropped by expiry, not retired by a completion. Zero
// means every retirement so far was a fact.
func (b *bgTracker) unsure() int {
	return b.expired
}
```

Also update the `outstanding` doc comment's last sentence from "Dead entries are dropped as they are found, so a stale tracker heals itself without a separate sweep." to "Dead entries are dropped as they are found, so a stale tracker heals itself without a separate sweep — but each drop is remembered in expired until the next conversation turn (see unsure)."

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/claudemux-head -run 'TestBgTracker' -v`
Expected: all PASS, including the four new tests and every pre-existing `TestBgTracker*`.

- [ ] **Step 5: Full suite and format**

Run: `gofmt -l cmd/ && go test ./...`
Expected: no gofmt output; `ok` for every package.

- [ ] **Step 6: Commit**

```bash
git add cmd/claudemux-head/bgwork.go cmd/claudemux-head/bgwork_test.go
git commit -m "feat(bgwork): remember expiries as doubt until the next turn

An expiry is a guess, a completion is a fact. The tracker now counts the
launches it dropped by cap or stall so the head can publish doubt instead
of a confident Idle — the case that sent the conductor into a session whose
pane still read '2 shells still running'.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018PTqLkgJPvqT6tBX4KSxKd"
```

---

### Task 2: StateUnsure — classify, publish, label, dot

**Files:**
- Modify: `cmd/claudemux-head/state.go` (`StateKind` consts, `classifyState`, `bgOverride`, `askOverride`, `Label`)
- Modify: `cmd/claudemux-head/statepub.go` (`statePublishValue`)
- Modify: `cmd/claudemux-head/tui.go` (`recomputeFromEvents` ~line 503, `turnEndedByIdle` ~line 990, `stateDot` ~line 2010)
- Modify: `cmd/claudemux-head/switchboard.go` (comment on `isWaiting` only)
- Test: `cmd/claudemux-head/state_test.go`, `cmd/claudemux-head/statepub_test.go`

**Interfaces:**
- Consumes: `bgTracker.unsure() int` from Task 1.
- Produces: `StateUnsure StateKind`; new signature `classifyState(events []Event, bgCount int, bgOldest time.Time, bgUnsure int, now time.Time) State`; publish value `"Unsure:" + N`; label `"Unsure N"`.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/claudemux-head/state_test.go`:

```go
// Idle reached because the tracker gave up on work (an expiry) is doubt,
// not Idle: the conductor must not treat it as waiting on the human.
func TestClassifyStateUnsureAfterExpiry(t *testing.T) {
	now := time.Date(2026, 9, 4, 17, 20, 0, 0, time.UTC)
	events := []Event{{Type: "assistant", UserText: "I'll pick this up when the run finishes.", Timestamp: "2026-09-04T16:45:53Z"}}
	got := classifyState(events, 0, time.Time{}, 2, now)
	if got.Kind != StateUnsure {
		t.Fatalf("kind = %v, want StateUnsure", got.Kind)
	}
	if got.BgCount != 2 {
		t.Errorf("BgCount = %d, want 2 (the expired count)", got.BgCount)
	}
	want, _ := time.Parse(time.RFC3339, "2026-09-04T16:45:53Z")
	if !got.Since.Equal(want) || !got.Anchored {
		t.Errorf("Since = %v anchored=%v, want the Stop's timestamp, anchored", got.Since, got.Anchored)
	}
}

// Live work beats doubt: while anything is still counting, Background is
// the truth and the expired count is irrelevant.
func TestClassifyStateBackgroundBeatsUnsure(t *testing.T) {
	now := time.Date(2026, 9, 4, 17, 20, 0, 0, time.UTC)
	events := []Event{{Type: "assistant", UserText: "done", Timestamp: "2026-09-04T16:45:53Z"}}
	got := classifyState(events, 1, now.Add(-time.Minute), 3, now)
	if got.Kind != StateBackground || got.BgCount != 1 {
		t.Errorf("got %+v, want Background with 1 outstanding", got)
	}
}

// Only Idle is overridden — a session mid-turn is Thinking whatever the
// tracker gave up on earlier.
func TestClassifyStateUnsureDoesNotOverrideThinking(t *testing.T) {
	now := time.Date(2026, 9, 4, 17, 20, 0, 0, time.UTC)
	events := []Event{{Type: "user", UserText: "how's it going?", Timestamp: "2026-09-04T17:19:00Z"}}
	got := classifyState(events, 0, time.Time{}, 2, now)
	if got.Kind != StateThinking {
		t.Errorf("kind = %v, want StateThinking", got.Kind)
	}
}

// A pending AskUserQuestion is a more specific truth than doubt.
func TestAskOverrideUpgradesUnsure(t *testing.T) {
	marker := time.Date(2026, 9, 4, 17, 0, 0, 0, time.UTC)
	events := []Event{{Type: "assistant", UserText: "done", Timestamp: "2026-09-04T16:45:53Z"}}
	s := State{Kind: StateUnsure, BgCount: 1, Since: marker.Add(-time.Hour), Anchored: true}
	if got := askOverride(s, events, marker); got.Kind != StateAsking {
		t.Errorf("kind = %v, want StateAsking", got.Kind)
	}
}

func TestUnsureLabel(t *testing.T) {
	if got := (State{Kind: StateUnsure, BgCount: 2}).Label(); got != "Unsure 2" {
		t.Errorf("Label = %q, want %q", got, "Unsure 2")
	}
}
```

Append to `cmd/claudemux-head/statepub_test.go`:

```go
// Unsure publishes with its count and is NOT escortable: doubt is the whole
// point — the conductor skips it exactly as it skips Background.
func TestStatePublishValueUnsureIsNotEscortable(t *testing.T) {
	v := statePublishValue(State{Kind: StateUnsure, BgCount: 2})
	if v != "Unsure:2" {
		t.Errorf("value = %q, want %q", v, "Unsure:2")
	}
	if isWaiting(v) {
		t.Errorf("isWaiting(%q) = true, want false", v)
	}
}
```

- [ ] **Step 2: Update the twelve existing `classifyState` call sites in `state_test.go`**

Every existing call has the shape `classifyState(events, N, oldest, now)`. Insert `0,` before the final `now`/`time.Now()` argument in each. The lines are 9, 17, 27, 38, 56, 68, 84, 87, 108, 124, 129, 134 (pre-edit numbering). Example: `classifyState(events, 2, oldest, now)` becomes `classifyState(events, 2, oldest, 0, now)`.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./cmd/claudemux-head -run 'TestClassifyStateUnsure|TestClassifyStateBackgroundBeatsUnsure|TestAskOverrideUpgradesUnsure|TestUnsureLabel|TestStatePublishValueUnsure' -v`
Expected: compile errors (`undefined: StateUnsure`, wrong argument count).

- [ ] **Step 4: Implement in `state.go`**

Add the kind at the END of the const block (after `StateWaiting`):

```go
	// StateUnsure: the main thread's turn ended and the head stopped
	// counting background work because a liveness cap gave up on it — not
	// because the work was seen to finish. It is Idle with an asterisk: the
	// transcript says the turn ended, but the pane may well still read
	// "2 shells still running". Distinct from Idle so isWaiting does not
	// match it and the conductor does not escort a human into it; distinct
	// from Background because nothing is known to be running. Clears on the
	// next conversation event (see bgTracker.expired).
	StateUnsure
```

Change the `classifyState` signature and both `bgOverride` calls:

```go
func classifyState(events []Event, bgCount int, bgOldest time.Time, bgUnsure int, now time.Time) State {
```
- `return bgOverride(idle, bgCount, bgOldest)` → `return bgOverride(idle, bgCount, bgOldest, bgUnsure)`
- `return bgOverride(State{Kind: StateIdle, Since: now}, bgCount, bgOldest)` → `return bgOverride(State{Kind: StateIdle, Since: now}, bgCount, bgOldest, bgUnsure)`

Replace `bgOverride` entirely:

```go
// bgOverride upgrades an Idle verdict when the session has work outstanding
// (Background) or when the tracker gave up on work by expiry rather than
// seeing it finish (Unsure). Only Idle is overridden: an unresolved
// foreground tool_use is the more specific truth and already classifies
// correctly, and Thinking is not-waiting either way. Live work wins over
// doubt — while anything is still counting, Background is the fact.
func bgOverride(s State, count int, oldest time.Time, unsure int) State {
	if s.Kind != StateIdle {
		return s
	}
	if count > 0 {
		since, anchored := oldest, true
		if since.IsZero() {
			since, anchored = s.Since, s.Anchored
		}
		return State{Kind: StateBackground, Since: since, BgCount: count, Anchored: anchored}
	}
	if unsure > 0 {
		// Since stays the Stop's timestamp: the doubt began when the turn
		// ended, and the lobby's age column should say how long the human
		// has been unsure-of, not when the cap fired.
		return State{Kind: StateUnsure, Since: s.Since, BgCount: unsure, Anchored: s.Anchored}
	}
	return s
}
```

In `askOverride`, change `case StateIdle, StateThinking, StateBackground:` to `case StateIdle, StateThinking, StateBackground, StateUnsure:`.

In `Label`, add before the `case StateAsking:` line:

```go
	case StateUnsure:
		return "Unsure " + strconv.Itoa(s.BgCount)
```

Update the `State.BgCount` field comment to: `BgCount  int // outstanding background tasks (StateBackground) or tasks given up on (StateUnsure)`.

- [ ] **Step 5: Implement in `statepub.go`**

In `statePublishValue`, add before `case StateAsking:`:

```go
	case StateUnsure:
		// Deliberately NOT in isWaiting's set — see StateUnsure.
		return "Unsure:" + strconv.Itoa(s.BgCount)
```

- [ ] **Step 6: Implement in `tui.go`**

In `recomputeFromEvents` (~line 503) replace the two lines:

```go
	bgCount, bgOldest := m.bg.outstanding(now)
	m.state = classifyState(m.allEvents, bgCount, bgOldest, m.bg.unsure(), now)
```

In `turnEndedByIdle` (~line 990) change the return to:

```go
	return kind == StateIdle || kind == StateBackground || kind == StateUnsure
```

and extend its doc comment with one sentence: `Unsure is a turn-ended verdict too — Idle with doubt attached — so it sits on the same side.`

In `stateDot` (~line 2010) add before `case StateWaiting:`:

```go
	case StateUnsure:
		// Not confidently idle: the amber busy dot, because "come look" green
		// is exactly the claim this state exists to withhold.
		return dotTool
```

- [ ] **Step 7: Update the `isWaiting` comment in `switchboard.go`**

Change the comment above `isWaiting` to end with: `"Tool:AskUserQuestion" stays for heads older than the Asking state, which could publish it in the brief window after a question flushed. "Unsure:N" is deliberately absent: it is Idle the head no longer trusts.`

- [ ] **Step 8: Run tests, format, full suite**

Run: `gofmt -l cmd/ && go test ./...`
Expected: no gofmt output; every package `ok`. If `tui_test.go` has a table over every `StateKind` (search for `StateWaiting` in it), add a `StateUnsure` row mirroring the `StateBackground` row's expectations with dot `dotTool`.

- [ ] **Step 9: Commit**

```bash
git add cmd/claudemux-head/state.go cmd/claudemux-head/statepub.go cmd/claudemux-head/tui.go cmd/claudemux-head/switchboard.go cmd/claudemux-head/state_test.go cmd/claudemux-head/statepub_test.go cmd/claudemux-head/tui_test.go
git commit -m "feat(head): publish Unsure:N when Idle rests on an expired guess

Idle reached because a background cap fired is not the same Idle as a turn
that ended with nothing running. The head now publishes Unsure:N for the
former; isWaiting does not match it, so the conductor skips the session
instead of escorting the human into work that may still be running.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018PTqLkgJPvqT6tBX4KSxKd"
```

---

### Task 3: Parse the `continued-in` record

**Files:**
- Modify: `cmd/claudemux-head/events.go` (`Event` struct ~line 90-125, `parseEvent` ~line 272-300)
- Test: `cmd/claudemux-head/events_test.go`

**Interfaces:**
- Produces: `Event.ContinuedIn string` — the successor session id named by a `type:"continued-in"` record; empty for every other record.

- [ ] **Step 1: Write the failing test**

Append to `cmd/claudemux-head/events_test.go`:

```go
// The harness parks a session and continues it elsewhere by appending a
// continued-in record to the old transcript. Verbatim from ag-admin,
// 2026-09-04: everything after it was written to the successor's file.
func TestParseEventContinuedIn(t *testing.T) {
	line := `{"type":"continued-in","timestamp":"2026-09-04T17:17:50.149Z","sessionId":"ebe355f0-0929-4718-a310-76ac61b31c37","continuedInSessionId":"6de04257-9bb6-4bc8-ba3c-5607593c35f7"}`
	ev, ok := parseEvent(line)
	if !ok {
		t.Fatal("parseEvent rejected the record")
	}
	if ev.Type != "continued-in" {
		t.Errorf("Type = %q, want continued-in", ev.Type)
	}
	if ev.ContinuedIn != "6de04257-9bb6-4bc8-ba3c-5607593c35f7" {
		t.Errorf("ContinuedIn = %q, want the successor id", ev.ContinuedIn)
	}
}

// The field is keyed on the record type, never on the bare key: a
// session's own sessionId is not a continuation.
func TestParseEventContinuedInOnlyOnItsType(t *testing.T) {
	ev, ok := parseEvent(`{"type":"user","timestamp":"2026-09-04T17:17:50.149Z","sessionId":"abc","continuedInSessionId":"zzz","message":{"role":"user","content":"hi"}}`)
	if !ok || ev.ContinuedIn != "" {
		t.Errorf("ContinuedIn = %q on a user record, want empty", ev.ContinuedIn)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head -run 'TestParseEventContinuedIn' -v`
Expected: compile error `ev.ContinuedIn undefined`.

- [ ] **Step 3: Implement**

In the `Event` struct in `events.go`, add after the `BgQueuedAgentID` field (keep the existing field comments intact):

```go
	// ContinuedIn is the successor session id from a `continued-in` record —
	// the harness's note that this transcript is finished and the
	// conversation carries on in <continuedInSessionId>.jsonl (a park into a
	// daemon-hosted fork, observed 2026-09-04). Empty on every other record.
	// The head follows it: a pane map that still names this file would
	// otherwise pin the head to a transcript nobody writes to again.
	ContinuedIn string
```

In `parseEvent`'s `raw` struct, add:

```go
		ContinuedIn string `json:"continuedInSessionId"` // present on type=continued-in
```

After the `if raw.Type == "queue-operation" { ... }` block, add:

```go
	if raw.Type == "continued-in" {
		ev.ContinuedIn = raw.ContinuedIn
	}
```

- [ ] **Step 4: Run tests, format**

Run: `gofmt -l cmd/ && go test ./cmd/claudemux-head`
Expected: no gofmt output; PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/events.go cmd/claudemux-head/events_test.go
git commit -m "feat(events): parse the continued-in record's successor id

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018PTqLkgJPvqT6tBX4KSxKd"
```

---

### Task 4: Follow the continuation when choosing the transcript to poll

**Files:**
- Create: `cmd/claudemux-head/continuation.go`
- Modify: `cmd/claudemux-head/tui.go` (model struct ~line 110-130; `newModel` ~line 423; `moveSession` ~line 594; `switchSession` ~line 640; `pollData` ~line 920-950; the `dataMsg` handler where `newEvents` are appended ~line 1390)
- Test: `cmd/claudemux-head/continuation_test.go`

**Interfaces:**
- Consumes: `Event.ContinuedIn` (Task 3); existing `transcriptSessionID(path string) string` and `transcriptForSession(projectsDir, sessionID string) (string, bool)` in `session.go`; existing `claudeProjectsPath() string`.
- Produces:
  - `func noteContinuations(superseded map[string]string, sessionID string, events []Event) map[string]string`
  - `func followContinuation(path string, superseded map[string]string, projectsDir string) string`
  - model field `superseded map[string]string`

- [ ] **Step 1: Write the failing tests**

Create `cmd/claudemux-head/continuation_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNoteContinuationsRecordsSuccessor(t *testing.T) {
	events := []Event{
		{Type: "assistant", UserText: "done"},
		{Type: "continued-in", ContinuedIn: "new-id"},
	}
	got := noteContinuations(nil, "old-id", events)
	if got["old-id"] != "new-id" {
		t.Errorf("superseded[old-id] = %q, want new-id", got["old-id"])
	}
	// Nothing to note leaves the map untouched (and nil stays nil).
	if m := noteContinuations(nil, "x", []Event{{Type: "user"}}); m != nil {
		t.Errorf("got %v, want nil for events with no continuation", m)
	}
	// A record naming the session itself is noise, not a cycle.
	if m := noteContinuations(nil, "same", []Event{{ContinuedIn: "same"}}); m != nil {
		t.Errorf("got %v, want nil for a self-continuation", m)
	}
}

// followContinuation walks path through the successors on disk: the file
// the harness named must exist, or the head keeps what it has.
func TestFollowContinuation(t *testing.T) {
	projects := t.TempDir()
	proj := filepath.Join(projects, "-Users-x-proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(proj, "old-id.jsonl")
	next := filepath.Join(proj, "new-id.jsonl")
	for _, p := range []string{old, next} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	superseded := map[string]string{"old-id": "new-id"}

	if got := followContinuation(old, superseded, projects); got != next {
		t.Errorf("followed to %q, want %q", got, next)
	}
	if got := followContinuation(next, superseded, projects); got != next {
		t.Errorf("successor itself resolved to %q, want unchanged", got)
	}
	if got := followContinuation(old, nil, projects); got != old {
		t.Errorf("no map: got %q, want unchanged", got)
	}
	// Successor not on disk yet (the fork's file appears later): stay put.
	if got := followContinuation(old, map[string]string{"old-id": "missing"}, projects); got != old {
		t.Errorf("missing successor: got %q, want unchanged", got)
	}
}

// Two hops resolve (a fork of a fork); a cycle terminates.
func TestFollowContinuationChainsAndTerminates(t *testing.T) {
	projects := t.TempDir()
	proj := filepath.Join(projects, "p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(proj, id+".jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	chain := map[string]string{"a": "b", "b": "c"}
	if got := followContinuation(filepath.Join(proj, "a.jsonl"), chain, projects); got != filepath.Join(proj, "c.jsonl") {
		t.Errorf("chain resolved to %q, want c.jsonl", got)
	}
	cycle := map[string]string{"a": "b", "b": "a"}
	got := followContinuation(filepath.Join(proj, "a.jsonl"), cycle, projects)
	if got != filepath.Join(proj, "a.jsonl") && got != filepath.Join(proj, "b.jsonl") {
		t.Errorf("cycle resolved to %q, want one of a/b (and to return at all)", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/claudemux-head -run 'TestNoteContinuations|TestFollowContinuation' -v`
Expected: compile errors, `undefined: noteContinuations` / `followContinuation`.

- [ ] **Step 3: Create `continuation.go`**

```go
package main

// Following a parked session to its successor transcript.
//
// Claude Code can park an interactive session and continue the conversation
// in a daemon-hosted fork (observed 2026-09-04 via a "fleet" attach). The
// old transcript then ends with a `continued-in` record naming the new
// session id, and every later turn is written to <new id>.jsonl in the same
// project dir. Nothing updates the pane map: the fork runs outside the tmux
// pane, so the hook that writes ~/.claude/claudemux/panes/<n>.json never
// fires for it, and mappedTranscript keeps returning the dead file. The
// head then reads a transcript nobody writes to again and publishes Idle
// forever — while the pane is visibly working.
//
// The harness's own record is the fix: whichever path the pane map (or the
// current binding) names, resolve it through the recorded successors before
// deciding whether to rotate. The successor must exist on disk; until it
// does, the head stays on the old file (its last verdict is still the best
// available).

// continuationMaxHops bounds followContinuation. Real chains are one hop
// (a fork of a fork is two); the bound only guards a malformed cycle.
const continuationMaxHops = 8

// noteContinuations records, for the session whose events these are, the
// successor any continued-in record names. Returns the (possibly newly
// allocated) map; a nil input with nothing to record stays nil. A record
// naming the session itself is ignored — it would be a one-step cycle.
func noteContinuations(superseded map[string]string, sessionID string, events []Event) map[string]string {
	for _, e := range events {
		if e.ContinuedIn == "" || sessionID == "" || e.ContinuedIn == sessionID {
			continue
		}
		if superseded == nil {
			superseded = map[string]string{}
		}
		superseded[sessionID] = e.ContinuedIn
	}
	return superseded
}

// followContinuation resolves a transcript path through the recorded
// successors: while the path's session was superseded and the successor's
// transcript exists under projectsDir, step to it. Returns path unchanged
// when there is nothing to follow or the successor is not on disk yet.
func followContinuation(path string, superseded map[string]string, projectsDir string) string {
	for hops := 0; hops < continuationMaxHops; hops++ {
		next, ok := superseded[transcriptSessionID(path)]
		if !ok {
			return path
		}
		resolved, found := transcriptForSession(projectsDir, next)
		if !found {
			return path
		}
		path = resolved
	}
	return path
}
```

- [ ] **Step 4: Run the unit tests to verify they pass**

Run: `go test ./cmd/claudemux-head -run 'TestNoteContinuations|TestFollowContinuation' -v`
Expected: PASS.

- [ ] **Step 5: Wire the model in `tui.go`**

Add a model field next to `jsonlPath`/`sessionID` (~line 114-118):

```go
	// superseded maps a session id to the id its transcript says it
	// continued in (see continuation.go). Kept across rotations: after the
	// head follows a park, the pane map still names the old file, and only
	// this memory stops the next poll rotating straight back.
	superseded map[string]string
```

In `newModel`, right after `seeded, _ := r.Seeded()` (~line 423), add:

```go
	m.superseded = noteContinuations(m.superseded, sessionID, seeded)
```
(`m` must already be declared at that point; if the seed happens before `m := model{...}` is built, place the call immediately after the model literal, still using `seeded` and `sessionID`.)

In `switchSession`, right after `m.sessionID = sessionID`, add:

```go
	m.superseded = noteContinuations(m.superseded, sessionID, seeded)
```

In `moveSession`, find where it reseeds (it calls `r.Seeded()` into a variable; read the function) and after that add the same call using `m.sessionID`.

In the `dataMsg` handler, change:

```go
		if len(msg.newEvents) > 0 {
			m.bg.observe(msg.newEvents, msg.time)
```
to:
```go
		if len(msg.newEvents) > 0 {
			m.superseded = noteContinuations(m.superseded, m.sessionID, msg.newEvents)
			m.bg.observe(msg.newEvents, msg.time)
```

- [ ] **Step 6: Wire `pollData` in `tui.go`**

At the top of `pollData`, alongside the other captured fields, add:

```go
	superseded := make(map[string]string, len(m.superseded))
	for k, v := range m.superseded {
		superseded[k] = v
	}
	projectsDir := claudeProjectsPath()
```

Replace the `if follow { ... }` block with:

```go
		if follow {
			if mapped, cwd, pane, ok := mappedTranscript(selfPane, paneDir); ok {
				// mapped is "" when the pane's live cwd is known but its
				// transcript isn't yet — keep the current binding then rather
				// than adopting an empty path. A mapped file that the harness
				// has since continued elsewhere resolves to its successor:
				// the pane map is not rewritten by a park.
				if mapped != "" {
					mapped = followContinuation(mapped, superseded, projectsDir)
				}
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
			// The current binding itself may have been continued elsewhere
			// with no pane map to say so (no claude pane found, or the map
			// still naming this very file).
			if activeJSONL == "" {
				if cur := followContinuation(jsonlPath, superseded, projectsDir); cur != jsonlPath {
					teardownLogf("rotate via=continued-in from=%s to=%s", jsonlPath, cur)
					activeJSONL = cur
				}
			}
		}
```

- [ ] **Step 7: Model-level test of the rotation decision**

`pollData` shells out to tmux, so test the decision logic it now embeds through a small pure helper instead: extract the "which file should we be on" choice into a function and test that. Add to `continuation.go`:

```go
// resolveActiveTranscript is pollData's rotation decision, pure so it can be
// tested: given the pane-mapped transcript (or "" when none is known), the
// current binding, and the recorded successors, return the path to adopt or
// "" to keep the current one.
func resolveActiveTranscript(mapped, current string, superseded map[string]string, projectsDir string) string {
	if mapped != "" {
		mapped = followContinuation(mapped, superseded, projectsDir)
		if mapped != current {
			return mapped
		}
		return ""
	}
	if cur := followContinuation(current, superseded, projectsDir); cur != current {
		return cur
	}
	return ""
}
```

Then simplify the `pollData` block from Step 6 to use it in the `ok` branch and the trailing check:

```go
			if mapped, cwd, pane, ok := mappedTranscript(selfPane, paneDir); ok {
				if next := resolveActiveTranscript(mapped, jsonlPath, superseded, projectsDir); next != "" {
					teardownLogf("rotate via=mapped pane=%s cwd=%s from=%s to=%s", pane, cwd, jsonlPath, next)
					activeJSONL = next
				}
			} else if mra, ok := mostRecentlyActiveSession(filepath.Dir(jsonlPath)); ok && mra != jsonlPath {
				teardownLogf("rotate via=mru-fallback from=%s to=%s", jsonlPath, mra)
				activeJSONL = mra
			}
			if activeJSONL == "" {
				if next := resolveActiveTranscript("", jsonlPath, superseded, projectsDir); next != "" {
					teardownLogf("rotate via=continued-in from=%s to=%s", jsonlPath, next)
					activeJSONL = next
				}
			}
```

Keep the explanatory comments from Step 6 on these lines. Add to `continuation_test.go`:

```go
// The scenario that stuck ag-admin on 2026-09-04: the pane map names the
// parked file, the fork's file exists, and the head is currently bound to
// the parked file. Adopt the fork — and once on it, do not bounce back.
func TestResolveActiveTranscriptFollowsPark(t *testing.T) {
	projects := t.TempDir()
	proj := filepath.Join(projects, "p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	parked := filepath.Join(proj, "ebe355f0.jsonl")
	fork := filepath.Join(proj, "6de04257.jsonl")
	for _, p := range []string{parked, fork} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	superseded := map[string]string{"ebe355f0": "6de04257"}

	if got := resolveActiveTranscript(parked, parked, superseded, projects); got != fork {
		t.Errorf("bound to parked, map says parked: got %q, want fork", got)
	}
	if got := resolveActiveTranscript(parked, fork, superseded, projects); got != "" {
		t.Errorf("bound to fork, map still says parked: got %q, want \"\" (stay)", got)
	}
	if got := resolveActiveTranscript("", parked, superseded, projects); got != fork {
		t.Errorf("no map, bound to parked: got %q, want fork", got)
	}
	if got := resolveActiveTranscript("", parked, nil, projects); got != "" {
		t.Errorf("nothing recorded: got %q, want \"\"", got)
	}
	// Ordinary rotation is untouched: a different mapped file with no
	// continuation involved is still adopted.
	other := filepath.Join(proj, "other.jsonl")
	if got := resolveActiveTranscript(other, parked, nil, projects); got != other {
		t.Errorf("plain rotation: got %q, want %q", got, other)
	}
}
```

- [ ] **Step 8: Build, test, format**

Run: `gofmt -l cmd/ && go vet ./cmd/claudemux-head && go test ./...`
Expected: clean, all `ok`.

- [ ] **Step 9: Commit**

```bash
git add cmd/claudemux-head/continuation.go cmd/claudemux-head/continuation_test.go cmd/claudemux-head/tui.go
git commit -m "fix(head): follow a continued-in record to the successor transcript

A parked session's pane map keeps naming the dead file, so the head read a
transcript nobody writes to and published Idle while the pane worked. The
harness's own continued-in record now steers the rotation.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018PTqLkgJPvqT6tBX4KSxKd"
```

---

### Task 5: Replay check against the real ag-admin transcript

**Files:**
- Create (temporary, deleted at the end of the task): `cmd/claudemux-head/replay_check_test.go`
- Read-only inputs: `/private/tmp/claude-501/-Users-michael-Projects-claudemux/85ec710a-a3ed-4a6b-b16a-073b45baa234/scratchpad/cut.jsonl` (the ag-admin transcript cut at 2026-09-04T16:45:54Z) and the full parked transcript at `/Users/michael/.claude/projects/-Users-michael-Projects-ag-admin--claude-worktrees-investigate-slack-dm-report/ebe355f0-0929-4718-a310-76ac61b31c37.jsonl`.

**Interfaces:**
- Consumes: everything from Tasks 1–4.
- Produces: evidence only. No committed code.

- [ ] **Step 1: Write the replay test**

```go
package main

import (
	"os"
	"testing"
	"time"
)

func TestReplayAgAdmin(t *testing.T) {
	cut := os.Getenv("REPLAY_CUT")
	full := os.Getenv("REPLAY_FULL")
	if cut == "" || full == "" {
		t.Skip("set REPLAY_CUT and REPLAY_FULL")
	}
	// 1. At turn end plus 31 minutes, the shell cap has fired: expect Unsure:1.
	r := newEventReader(cut)
	r.SeedFromEnd(500)
	seeded, _ := r.Seeded()
	var bg bgTracker
	bg.subagentsDir = subagentsDirFor(cut)
	bg.observe(seeded, time.Date(2026, 9, 4, 16, 46, 0, 0, time.UTC))
	late := time.Date(2026, 9, 4, 17, 17, 0, 0, time.UTC)
	n, oldest := bg.outstanding(late)
	st := classifyState(seeded, n, oldest, bg.unsure(), late)
	t.Logf("at 17:17Z: outstanding=%d unsure=%d state=%s publish=%s", n, bg.unsure(), st.Label(), statePublishValue(st))
	if st.Kind != StateUnsure || statePublishValue(st) != "Unsure:1" {
		t.Errorf("want Unsure:1, got %s", statePublishValue(st))
	}
	// 2. The full parked transcript records the successor.
	r2 := newEventReader(full)
	all, _ := r2.Tail()
	sup := noteContinuations(nil, transcriptSessionID(full), all)
	t.Logf("superseded=%v", sup)
	if sup["ebe355f0-0929-4718-a310-76ac61b31c37"] != "6de04257-9bb6-4bc8-ba3c-5607593c35f7" {
		t.Errorf("continuation not recorded: %v", sup)
	}
	next := resolveActiveTranscript(full, full, sup, claudeProjectsPath())
	t.Logf("would rotate to: %s", next)
	if next == "" {
		t.Errorf("head would stay on the parked file")
	}
}
```

- [ ] **Step 2: Run it**

Run:
```
REPLAY_CUT=/private/tmp/claude-501/-Users-michael-Projects-claudemux/85ec710a-a3ed-4a6b-b16a-073b45baa234/scratchpad/cut.jsonl REPLAY_FULL=/Users/michael/.claude/projects/-Users-michael-Projects-ag-admin--claude-worktrees-investigate-slack-dm-report/ebe355f0-0929-4718-a310-76ac61b31c37.jsonl go test ./cmd/claudemux-head -run TestReplayAgAdmin -v
```
Expected: PASS with logs showing `publish=Unsure:1` and `would rotate to: .../6de04257-9bb6-4bc8-ba3c-5607593c35f7.jsonl`. If either assertion fails, stop and report the log lines verbatim — do not adjust the production code to fit.

- [ ] **Step 3: Delete the file and confirm a clean tree**

```bash
rm cmd/claudemux-head/replay_check_test.go
git status --short
```
Expected: no output from `git status --short`.

---

### Task 6: Document the two states of doubt

**Files:**
- Modify: `docs/superpowers/specs/2026-08-11-background-work-state-design.md` (append after "## Known limits")
- Modify: `README.md` (the state description near the **Asking** paragraphs, ~lines 320-335; read the surrounding text first and match its voice)

- [ ] **Step 1: Spec addendum**

Append to the spec:

```markdown
## Addendum 2026-09-04: doubt after expiry, and following `continued-in`

Two failures from one afternoon, both "the head said Idle while the pane was working".

**Expiry is a guess.** A hung `ssh` backgrounded by the Bash tool ran past
`bgShellMaxAge` (30m). The tracker dropped it, `classifyState` published `Idle`
anchored at the last Stop, and the conductor escorted the human into a pane that
still read "2 shells still running". The cap stays (a backgrounded dev server must
not hide a session forever), but a drop by cap or stall is now remembered in
`bgTracker.expired` until the next `user`/`assistant` event newer than the drop.
`classifyState` turns a non-zero count into `StateUnsure`, published as `Unsure:N`,
labelled `Unsure N`, amber dot. `isWaiting` does not match it, so the conductor
skips the session exactly as it skips `Background:N`. Live work still wins: while
anything counts, the state is `Background`.

**Parked sessions move their transcript.** A "fleet" attach parks the interactive
claude and continues the conversation in a daemon-hosted fork. The old transcript
ends with `{"type":"continued-in","continuedInSessionId":"<new>"}` and every later
turn lands in `<new>.jsonl` in the same project dir. The pane map is not rewritten
(the fork runs outside the pane, so the map hook never fires), so `mappedTranscript`
kept naming the dead file and the head read it forever. `parseEvent` now carries
`Event.ContinuedIn`; the model records `old id -> new id` in `superseded`; and
`pollData` resolves both the mapped path and its own binding through
`followContinuation` (successor must exist on disk) before deciding to rotate. Once
on the fork, the stale map resolves to the same file, so there is no bounce.

Not covered: a `!`-typed command that the harness backgrounds is recorded as a
`user` message beginning `<bash-stdout>Command did not complete within its ...` with
no `toolUseResult`, so it is never tracked at all. Process liveness (snapshot-zsh
children of the pane's claude pid) would catch it; deliberately left for a later
change.
```

- [ ] **Step 2: README**

After the paragraph(s) describing **Asking** (search for "Esc'd question"), add:

```markdown
**Unsure** is Idle the head no longer trusts. Background work is counted from the
transcript and expires on a timer when nothing reports it finished; when that timer,
not a completion, is what emptied the count, the head shows **Unsure N** with an amber
dot and publishes `Unsure:N`. The switchboard does not treat it as waiting, so the
conductor will not escort you into a session whose pane may still say "2 shells still
running". It clears on your next prompt or Claude's next turn.
```

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/specs/2026-08-11-background-work-state-design.md README.md
git commit -m "docs: Unsure state and continued-in following

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018PTqLkgJPvqT6tBX4KSxKd"
```

---

## Self-review notes

- Spec coverage: doubt state (Tasks 1, 2), park following (Tasks 3, 4), evidence (Task 5), docs (Task 6). The `!`-command gap is documented as out of scope, not silently dropped.
- Names used consistently: `unsure()`, `StateUnsure`, `Unsure:N`, `ContinuedIn`, `superseded`, `noteContinuations`, `followContinuation`, `resolveActiveTranscript`, `classifyState(events, bgCount, bgOldest, bgUnsure, now)`.
- Task 4 Step 6 is superseded by Step 7's refactor; the executor ends with the Step 7 shape.
