# Un-defer Explain Prompt Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `d` on a deferred session opens an inline `◆ unblocked:` prompt; enter clears the defer and submits the explanation to that session's Claude.

**Architecture:** Pure helpers and one tea.Cmd live beside the existing defer code in `deferpub.go`; the keystroke sequence is factored out of `teardownSendCmd` so both callers share it. The lobby (`swModel`) and the head (`model`) each gain a small "undefer" mode on top of their existing blocker-prompt state.

**Tech Stack:** Go, bubbletea, lipgloss, tmux (`set-option`, `send-keys`). All code in `cmd/claudemux-head/` (package `main`).

**Spec:** `docs/superpowers/specs/2026-11-14-undefer-explain-prompt-design.md`

## Global Constraints

- Prompt text: `◆ unblocked: ` + input + `▌`, in `swDeferStyle`.
- Message: `Blocker cleared (was: <blocker>): <text>`; with no recorded blocker, `Blocker cleared: <text>`.
- Enter on an empty (whitespace-only) line clears the defer and sends nothing. Esc leaves the session deferred.
- No `C-u` before typing the message (teardown keeps its `C-u`).
- Nothing is ever typed into a session whose published state is `Awaiting` or `Starting`, or that has no claude pane. While that holds, enter with text is a no-op and the prompt says why.
- `D` and the defer direction of `d` are unchanged.
- Run tests from `cmd/claudemux-head`: `go test ./...` and `go vet ./...` must pass at every commit.
- Commit messages follow the repo style (`defer: …`, `lobby: …`) and end with:
  `Co-Authored-By: Claude Fable 5.2 <noreply@anthropic.com>` and
  `Claude-Session: https://claude.ai/code/session_016sp4ELtJPoeuhU73fUqBYr`

## Review Focus

- A blocker or typed text ending in `;` or starting with `-`: it must reach Claude verbatim (`send-keys -l --` already protects this; Task 1 tests the argv).
- The session leaves the deferred state by other means (another surface clears it) while the prompt is open: enter must still behave sanely, clearing an already-clear mark and sending the message (Task 2 test).
- The selected lobby row changes or the list reorders while the prompt is open: the prompt stays bound to the session it opened on, by name (Task 2 test).
- Text typed while the session flips to `Awaiting`: enter must not send and must not clear; the input survives (Tasks 2 and 3 tests).
- Teardown's wrap-up send must behave exactly as before the refactor, `C-u` included (Task 1 test).

---

### Task 1: Shared helpers and the undefer command

**Files:**
- Modify: `cmd/claudemux-head/teardown.go` (`teardownSendCmd`, ~line 557)
- Modify: `cmd/claudemux-head/deferpub.go`
- Test: `cmd/claudemux-head/deferpub_test.go`, `cmd/claudemux-head/teardown_test.go`

**Interfaces:**
- Produces:
  - `func promptKeysArgs(pane, text string, clear bool) (pre [][]string, enter []string, ok bool)` in `teardown.go`: the tmux argvs to run before the delay (optional `sendClearArgs`, then `sendLiteralArgs`) and the Enter argv. `ok` is false when pane or text is empty.
  - `func sendPromptKeys(pane, text string, clear bool) error` in `teardown.go`: runs `pre`, sleeps `teardownKeyDelay`, runs `enter`; each tmux call under its own `teardownTmuxTimeout`. `teardownSendCmd` calls it with `clear=true` and keeps its existing notes and return values.
  - `func undeferPromptText(input string) string`
  - `func undeferMessage(blocker, text string) string`
  - `func undeferBlocked(state, claudePane string) string`: `"no claude pane"` when pane is empty, `"on a permission prompt"` for state `Awaiting`, `"still starting"` for `Starting`, else `""`. `state` is a `statePublishValue` string.
  - `func undeferCmd(target, claudePane, message string) tea.Cmd`: runs `deferArgs(target, false, "")` (2s deadline, like `setDeferCmd`), then, when `message != ""`, `sendPromptKeys(claudePane, message, false)`. Returns nil msg; errors are dropped like `setDeferCmd`'s.

- [ ] **Step 1: Write the failing tests**

In `deferpub_test.go`:

```go
func TestUndeferMessage(t *testing.T) {
	if got := undeferMessage("waiting on Ana", " she approved "); got != "Blocker cleared (was: waiting on Ana): she approved" {
		t.Errorf("got %q", got)
	}
	if got := undeferMessage("  ", "CI is green"); got != "Blocker cleared: CI is green" {
		t.Errorf("got %q", got)
	}
	// Control characters in either part are flattened, as sanitizeDeferReason does.
	if got := undeferMessage("a\tb", "c\nd"); got != "Blocker cleared (was: a b): c d" {
		t.Errorf("got %q", got)
	}
}

func TestUndeferBlocked(t *testing.T) {
	for _, c := range []struct{ state, pane, want string }{
		{"Idle", "%2", ""}, {"Thinking", "%2", ""}, {"Asking", "%2", ""},
		{"Awaiting", "%2", "on a permission prompt"},
		{"Starting", "%2", "still starting"},
		{"Idle", "", "no claude pane"},
	} {
		if got := undeferBlocked(c.state, c.pane); got != c.want {
			t.Errorf("undeferBlocked(%q, %q) = %q, want %q", c.state, c.pane, got, c.want)
		}
	}
}

func TestUndeferPromptText(t *testing.T) {
	if got := undeferPromptText("ok"); got != "◆ unblocked: ok▌" {
		t.Errorf("got %q", got)
	}
}
```

In `teardown_test.go`:

```go
func TestPromptKeysArgs(t *testing.T) {
	pre, enter, ok := promptKeysArgs("%2", "-p x;", true)
	if !ok || len(pre) != 2 ||
		!reflect.DeepEqual(pre[0], []string{"send-keys", "-t", "%2", "C-u"}) ||
		!reflect.DeepEqual(pre[1], []string{"send-keys", "-t", "%2", "-l", "--", "-p x;"}) ||
		!reflect.DeepEqual(enter, []string{"send-keys", "-t", "%2", "Enter"}) {
		t.Fatalf("clear=true: pre=%v enter=%v ok=%v", pre, enter, ok)
	}
	if pre, _, ok := promptKeysArgs("%2", "hi", false); !ok || len(pre) != 1 || pre[0][3] != "-l" {
		t.Fatalf("clear=false must send only the literal, got %v", pre)
	}
	if _, _, ok := promptKeysArgs("", "hi", false); ok {
		t.Fatal("empty pane must not be ok")
	}
	if _, _, ok := promptKeysArgs("%2", "", false); ok {
		t.Fatal("empty text must not be ok")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd cmd/claudemux-head && go test ./... -run 'TestUndefer|TestPromptKeysArgs'`
Expected: build failure, undefined `undeferMessage` etc.

- [ ] **Step 3: Implement the six functions above**

`undeferMessage` sanitises both parts with `sanitizeDeferReason`. `teardownSendCmd` keeps resolving its pane via `mappedTranscript` and maps any `sendPromptKeys` error to `teardownSentMsg{note: "wrap-up didn't submit"}`. Keep the explanatory comments now in `teardownSendCmd` (per-call deadlines, why the delay) by moving them onto `sendPromptKeys`. Give each new function a doc comment in the file's existing style, including why undefer does not send `C-u` (a draft the user left in Claude's input is kept).

- [ ] **Step 4: Run all tests**

Run: `cd cmd/claudemux-head && go test ./... && go vet ./...`
Expected: PASS (existing teardown tests included)

- [ ] **Step 5: Commit** (`defer: helpers for telling a session its blocker cleared`)

---

### Task 2: Lobby prompt

**Files:**
- Modify: `cmd/claudemux-head/switchboardtui.go` (model fields ~381, key handling ~1003 and ~1100, View ~1608 and ~1646)
- Modify: `cmd/claudemux-head/headhelp.go` (`switchboardShortcuts`)
- Test: `cmd/claudemux-head/switchboardtui_test.go`

**Interfaces:**
- Consumes: `undeferPromptText`, `undeferMessage`, `undeferBlocked`, `undeferCmd` from Task 1; existing `setDeferCmd`, `deferInputKey`, `swDeferTarget`, `swSnapshot.session(name)`.
- Produces: `swModel.deferClearing bool` (the open prompt is an undefer), `swModel.deferWas string` (the blocker recorded when it opened).

Behaviour:

- `d` on a deferred row: set `deferring`, `deferClearing`, empty `deferInput`, `deferTarget`, `deferName`, `deferWas = sess.DeferReason`. No tmux command yet. (`d` on a non-deferred row and `D` are unchanged and leave `deferClearing` false.)
- While `deferring && deferClearing`:
  - esc: close the prompt, no command.
  - enter, input blank: close, return `setDeferCmd(target, false, "")`.
  - enter, input has text: look the session up by `deferName` in `m.snap`. If it is gone, close with no command. If `undeferBlocked(sess.State, sess.ClaudePane) != ""`, do nothing (prompt and input stay). Otherwise close and return `undeferCmd(target, sess.ClaudePane, undeferMessage(m.deferWas, m.deferInput))`.
  - other keys: `deferInputKey`.
  - closing always resets `deferClearing` and `deferWas`.
- View: status line `swDeferStyle.Render("undefer "+name+" ") + undeferPromptText(input)`. Footer `enter undefer · esc cancel`; when the session is blocked (same lookup), footer `can't send: <why> · enter on an empty line undefers silently · esc cancel`.
- `switchboardShortcuts`: replace `"d Defer"` with `"d Defer / undefer and say why"`. The default footer text is unchanged.

- [ ] **Step 1: Write the failing tests** in `switchboardtui_test.go`, following the existing defer-prompt tests near line 1335 for model setup (a snapshot with a deferred session `web`, `DeferReason: "waiting on Ana"`, `ClaudePane: "%2"`, `State: "Idle"`):

  - `TestUndeferOpensPrompt`: `d` → `deferring && deferClearing`, `deferWas == "waiting on Ana"`, returned cmd is nil.
  - `TestUndeferEscKeepsDefer`: esc → prompt closed, cmd nil.
  - `TestUndeferEmptyEnterClearsSilently`: enter (and enter after typing only spaces) → prompt closed, cmd non-nil.
  - `TestUndeferEnterSends`: type `ok`, enter → prompt closed, cmd non-nil, `deferClearing` false.
  - `TestUndeferBlockedHoldsInput`: with `State: "Awaiting"`, type `ok`, enter → still `deferring`, `deferInput == "ok"`, cmd nil; `View()` contains `can't send: on a permission prompt`.
  - `TestUndeferBoundToNameAcrossReorder`: open on `web`, move `m.sel` and reorder `m.snap.Sessions`, type `ok`, enter → closes with a non-nil cmd (the lookup is by name, and `deferTarget` is unchanged).
  - `TestUndeferSessionAlreadyClear`: open on `web`, then replace the snapshot with `web` no longer `Deferred`, type `ok`, enter → closes with a non-nil cmd.
  - `TestUndeferViewPrompt`: `View()` contains `undefer web` and `◆ unblocked: ` and the footer `enter undefer · esc cancel`.
  - Extend the existing test that asserts `d` on a deferred row clears immediately so it now expects the prompt.

- [ ] **Step 2: Run to verify they fail** — `go test ./... -run TestUndefer` → FAIL
- [ ] **Step 3: Implement** as described under Behaviour.
- [ ] **Step 4: Run** `go test ./... && go vet ./...` → PASS
- [ ] **Step 5: Commit** (`lobby: d on a deferred row asks why it is unblocked and tells the session`)

---

### Task 3: Head prompt and README

**Files:**
- Modify: `cmd/claudemux-head/tui.go` (fields ~312, `d` key ~1534, `deferPromptKey` ~2924, `deferChipText` ~2943)
- Modify: `cmd/claudemux-head/headhelp.go` (`headShortcuts`)
- Modify: `README.md` (**Deferring a session**, ~lines 215-225, and the line near 409 that says `d` toggles defer)
- Test: `cmd/claudemux-head/tui_test.go`

**Interfaces:**
- Consumes: the Task 1 functions; existing `m.deferRaw`, `m.deferReason`, `m.claudePane`, `m.selfPane`, `statePublishValue(m.state)`.
- Produces: `model.deferClearing bool`, `model.deferWas string`.

Behaviour (the head's own session):

- `d` while `isDeferValue(m.deferRaw)`: set `deferPrompting`, `deferClearing`, empty `deferInput`, `deferWas = m.deferReason`. No command.
- In `deferPromptKey` when `deferClearing`:
  - esc: close, no command.
  - enter, blank: close, `setDeferCmd(m.selfPane, false, "")`.
  - enter, text: if `undeferBlocked(statePublishValue(m.state), m.claudePane) != ""`, no-op; else close and return `undeferCmd(m.selfPane, m.claudePane, undeferMessage(m.deferWas, m.deferInput))`.
  - closing resets `deferClearing` and `deferWas`.
- `deferChipText` when `deferPrompting && deferClearing`: `swDeferStyle.Render(undeferPromptText(input))`, with ` (can't send: <why>)` appended inside the style when blocked.
- `headShortcuts`: `"d Defer"` → `"d Defer / undefer and say why"`.
- README: rewrite the sentence that says pressing `d` on a deferred session clears it with no prompt. New text states: `d` on a deferred session asks why it is unblocked; enter sends Claude `Blocker cleared (was: <blocker>): <what you typed>` and clears the defer; enter on an empty line clears without telling the session; esc leaves it deferred; while the session is on a permission prompt or still starting the explanation cannot be sent and the prompt says so. Fix the ~line 409 mention to match.

- [ ] **Step 1: Write the failing tests** in `tui_test.go`, following the defer-prompt tests near line 5400 (model with `ready: true, selfPane: "%1", claudePane: "%2", deferRaw: "1", deferReason: "waiting on Ana"`):

  - `TestHeadUndeferOpensPrompt`: `d` → `deferPrompting && deferClearing`, `deferWas == "waiting on Ana"`, cmd nil.
  - `TestHeadUndeferEscKeepsDefer`, `TestHeadUndeferEmptyEnterClears` (cmd non-nil), `TestHeadUndeferEnterSends` (cmd non-nil, flags reset).
  - `TestHeadUndeferBlockedHoldsInput`: `m.state = State{Kind: StateAwaiting}`, type `ok`, enter → still prompting, input kept, cmd nil, `deferChipText()` contains `can't send: on a permission prompt`.
  - `TestHeadUndeferNoClaudePane`: `claudePane: ""` → same hold, chip contains `can't send: no claude pane`.
  - `TestHeadUndeferChipText`: contains `◆ unblocked: `.
  - Update any existing test asserting `d` on a deferred head clears immediately.

- [ ] **Step 2: Run to verify they fail** — `go test ./... -run TestHeadUndefer` → FAIL
- [ ] **Step 3: Implement**, then edit `headhelp.go` and `README.md`. If `headhelp_test.go` pins shortcut text or layout widths, update it to the new entry.
- [ ] **Step 4: Run** `go test ./... && go vet ./...` → PASS
- [ ] **Step 5: Commit** (`head: d on a deferred session asks why it is unblocked; README`)
