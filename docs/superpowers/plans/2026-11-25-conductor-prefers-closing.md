# Conductor Prefers Closing Sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The conductor's queue puts sessions that are about to close first, and the lobby tells a wrap-up that is asking a question apart from one that bailed.

**Architecture:** Two pure helpers in `closingpub.go` classify a row from its published closing phase and state. The badge, the row's phase text and `waitingQueue`'s sort all read them. Nothing the head publishes changes.

**Tech Stack:** Go, package `main` in `cmd/claudemux-head`. Tests: `go test ./cmd/claudemux-head/` from the repo root.

**Spec:** `docs/superpowers/specs/2026-11-25-conductor-prefers-closing-design.md`

## Global Constraints

- All work happens in the current worktree; do not `cd` to the main checkout.
- No change to what the head publishes (`closingPublishValue`, `statePublishValue`).
- No change to snooze, defer, preemption or the status line.
- Exact copy: badge label `CONFIRM`; phase text `⏻ wrap-up needs an answer`.
- "Asking" states are exactly `Asking`, `Awaiting`, `Tool:AskUserQuestion`.
- Comments follow the file's existing style: they explain why, in prose.
- Commit messages end with:
  ```
  Co-Authored-By: Claude Fable 5.2 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_016sp4EN4fWu3wYaBeGduKTE
  ```

## Review Focus

- An unrecognized closing value from a newer head, waiting: stays tier 2 and keeps the `CLOSE` badge.
- `ready` or `direct` on a row that is not waiting (e.g. `direct` + `Thinking`): never enters the queue, since the queue only holds waiting rows.
- `exiting` + `Idle`: tier 2, not promoted (nothing for the human to do).
- A closing family member that is deferred: still excluded from the queue.
- The web page's `closing_label` for `blocked` + `Asking` reads `wrap-up needs an answer` (no glyph).

---

### Task 1: Classify closing rows; fix the badge and phase text

**Files:**
- Modify: `cmd/claudemux-head/closingpub.go`
- Modify: `cmd/claudemux-head/switchboardtui.go` (the `closingText(sess.Closing)` call in `swDetailLine`, about line 1316)
- Modify: `cmd/claudemux-head/webfleet.go` (the `ClosingLabel:` line, about line 227)
- Test: `cmd/claudemux-head/closingpub_test.go`

**Interfaces:**
- Produces: `swCloseAsking(closing, state string) bool`, `swCloseTier(closing, state string) int`, `swClosingText(closing, state string) string`.

- [ ] **Step 1: Write the failing tests** in `closingpub_test.go`

```go
func TestSwCloseTier(t *testing.T) {
	cases := []struct {
		closing, state string
		tier           int
		asking         bool
	}{
		{"ready", "Idle", 0, false},
		{"direct", "Idle", 0, false},
		{"wrapup", "Asking", 1, true},
		{"wrapup", "Awaiting", 1, true},
		{"blocked", "Asking", 1, true},
		{"blocked", "Tool:AskUserQuestion", 1, true},
		{"blocked", "Idle", 2, false},
		{"wrapup", "Idle", 2, false},
		{"wrapup", "Thinking", 2, false},
		{"exiting", "Idle", 2, false},
		{"from-a-newer-head", "Asking", 2, false},
		{"", "Asking", 2, false},
		{"", "Idle", 2, false},
	}
	for _, c := range cases {
		if got := swCloseTier(c.closing, c.state); got != c.tier {
			t.Errorf("swCloseTier(%q, %q) = %d, want %d", c.closing, c.state, got, c.tier)
		}
		if got := swCloseAsking(c.closing, c.state); got != c.asking {
			t.Errorf("swCloseAsking(%q, %q) = %v, want %v", c.closing, c.state, got, c.asking)
		}
	}
}

func TestSwClosingText(t *testing.T) {
	if got := swClosingText("blocked", "Asking"); got != "⏻ wrap-up needs an answer" {
		t.Errorf("blocked+Asking = %q", got)
	}
	if got := swClosingText("wrapup", "Awaiting"); got != "⏻ wrap-up needs an answer" {
		t.Errorf("wrapup+Awaiting = %q", got)
	}
	for _, c := range [][2]string{{"blocked", "Idle"}, {"wrapup", "Thinking"}, {"ready", "Idle"}, {"", "Asking"}} {
		if got, want := swClosingText(c[0], c[1]), closingText(c[0]); got != want {
			t.Errorf("swClosingText(%q, %q) = %q, want %q", c[0], c[1], got, want)
		}
	}
}
```

Add two rows to the existing `TestSwCloseBadgeByPhase` table:

```go
{"blocked", "Asking", "CONFIRM", swBadgeCloseWaitStyle},
{"blocked", "Awaiting", "CONFIRM", swBadgeCloseWaitStyle},
```

Add a web test next to the existing `ClosingLabel` assertions (reuse that test's way of building the fleet view): a session with `Closing: "blocked"`, `State: "Asking"` yields `ClosingLabel == "wrap-up needs an answer"`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/claudemux-head/ -run 'TestSwCloseTier|TestSwClosingText|TestSwCloseBadgeByPhase'`
Expected: build failure, `undefined: swCloseTier`.

- [ ] **Step 3: Implement in `closingpub.go`**

- `swCloseAsking`: true when closing is `wrapup` or `blocked` and state is one of the three asking states.
- `swCloseTier`: 0 for `ready`/`direct`; 1 when `swCloseAsking`; else 2.
- `swClosingText`: the fixed asking copy when `swCloseAsking`, else `closingText(closing)`.
- `swCloseBadgeLabel`: check `swCloseAsking` first and return `CONFIRM` in `swBadgeCloseWaitStyle`; the existing `wrapup` + waiting → `CONFIRM` behavior stays.
- Switch the two call sites to `swClosingText(s.Closing, s.State)`; the web one keeps its `strings.TrimPrefix(..., "⏻ ")`.
- Update the comments on `swCloseBadgeLabel` and the three-looks block to say `BLOCKED` now means the wrap-up stopped with the gate shut, and `CONFIRM` covers a question in either phase, with the reason (the head publishes `blocked` for a resting question in a worktree session).

- [ ] **Step 4: Run the package tests**

Run: `go test ./cmd/claudemux-head/`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head
git commit -m "lobby: a wrap-up asking a question reads CONFIRM, not BLOCKED"
```

### Task 2: Queue order by closing tier

**Files:**
- Modify: `cmd/claudemux-head/swconductor.go` (`waitingQueue`)
- Modify: `cmd/claudemux-head/closingpub.go` (header comment only)
- Test: `cmd/claudemux-head/swconductor_test.go`

**Interfaces:**
- Consumes: `swCloseTier(closing, state string) int` from Task 1.

- [ ] **Step 1: Write the failing tests** in `swconductor_test.go`

Add a helper beside `waiting`:

```go
func closingWaiting(name, closing, state string, since int64) swSession {
	return swSession{Name: name, State: state, Since: time.Unix(since, 0), Closing: closing}
}
```

```go
func TestWaitingQueuePrefersClosing(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	s := snapAt("switchboard",
		waiting("old", 100),
		closingWaiting("bailed", "blocked", "Idle", 150),
		closingWaiting("confirm-young", "blocked", "Asking", 400),
		closingWaiting("confirm-old", "wrapup", "Awaiting", 300),
		closingWaiting("ready-young", "ready", "Idle", 600),
		closingWaiting("kill-old", "direct", "Idle", 500),
		closingWaiting("exiting", "exiting", "Idle", 50),
		swSession{Name: "kill-busy", State: "Thinking", Closing: "direct", Since: time.Unix(10, 0)},
	)
	want := []string{"kill-old", "ready-young", "confirm-old", "confirm-young", "exiting", "old", "bailed"}
	q := s.waitingQueue(nil, now)
	// compare names in order; fail with the full got/want lists
}

func TestWaitingQueueClosingStillSnoozedAndDeferred(t *testing.T) {
	// "ready" (Since 100) snoozed for that episode, "ready-deferred" with
	// Deferred: true, and waiting("plain", 200): queue is exactly ["plain"].
}

func TestConductorParkedDispatchesClosingFirst(t *testing.T) {
	// Parked on the lobby with waiting("old", 100) and
	// closingWaiting("ready", "ready", "Idle", 900): step() returns a switch
	// to "ready".
}
```

Write the two sketched tests in full, in the style of `TestWaitingQueueSnoozeAndTiebreak` and `TestConductorParkedDispatchesOldest`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/claudemux-head/ -run 'TestWaitingQueuePrefersClosing|TestConductorParkedDispatchesClosingFirst'`
Expected: FAIL, queue starts with `exiting`/`old`.

- [ ] **Step 3: Implement**

In `waitingQueue`'s `sort.SliceStable` comparator, compare `swCloseTier(q[i].Closing, q[i].State)` first, then `Since`, then `Name` as now.

Extend `waitingQueue`'s doc comment: the order, and why (a shrinking list is better psychologically; these rows cost one keystroke or one answer). Rewrite the last paragraph of `closingpub.go`'s header, which says the conductor ignores the mark on purpose: it still never excludes a closing row, and now orders by it.

- [ ] **Step 4: Run the package tests and vet**

Run: `go vet ./cmd/claudemux-head/ && go test ./cmd/claudemux-head/`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head
git commit -m "conductor: sessions about to close go first in the queue"
```
