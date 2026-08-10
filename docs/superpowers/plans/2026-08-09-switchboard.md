# Switchboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `claudemux-head switchboard` subcommand — a full-screen lobby that auto-ferries the tmux client to claudemux sessions waiting on input — plus state publishing from the head and a `claudemux switch` launcher entry.

**Architecture:** `claudemux-head` publishes its computed state to tmux session user options (`@claudemux_state`, `@claudemux_state_since`) on every transition. The switchboard polls those options once per second, feeds a pure "conductor" state machine, and issues `tmux switch-client` per its decisions. Spec: `docs/superpowers/specs/2026-08-09-switchboard-design.md`.

**Tech Stack:** Go 1.26, Bubble Tea v1, lipgloss, tmux CLI via `os/exec`. All Go code lives in `cmd/claudemux-head/` (single-package repo convention: package `main`, tests beside code).

## Global Constraints

- All tmux subprocess calls carry a 2s `context.WithTimeout` and discard/tolerate failure — a wedged tmux must never block or crash a TUI (pattern: `tabtitle.go:59-70`).
- "Waiting on input" means published state `Idle` or `Tool:AskUserQuestion` — nothing else.
- No tmux integration tests: tests are pure-logic on args builders, parsers, the conductor, and Bubble Tea models.
- Run `gofmt -l cmd/claudemux-head` before every commit; it must print nothing.
- Run tests with `go test ./...` from the repo root.
- Comments follow the repo's house style: explain *why*/constraints, not what the next line does.

---

### Task 1: State publish value + tmux args + tea.Cmd (`statepub.go`)

**Files:**
- Create: `cmd/claudemux-head/statepub.go`
- Test: `cmd/claudemux-head/statepub_test.go`

**Interfaces:**
- Consumes: `State`, `StateKind` (`state.go`), `tea.Cmd` (bubbletea).
- Produces:
  - `func statePublishValue(s State) string` — `"Idle"`, `"Thinking"`, `"Tool:<name>"`, `"Awaiting"`, `"Error"`, `"Compacting"`; `""` for unknown kinds.
  - `func statePublishArgs(selfPane, value string, since time.Time) ([]string, bool)` — the tmux argv (without the leading `tmux`); `ok=false` when `selfPane` or `value` is empty.
  - `func publishStateCmd(selfPane string, s State, now time.Time) tea.Cmd` — nil when there is nothing to publish.

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"reflect"
	"testing"
	"time"
)

func TestStatePublishValue(t *testing.T) {
	cases := []struct {
		name string
		s    State
		want string
	}{
		{"idle", State{Kind: StateIdle}, "Idle"},
		{"thinking", State{Kind: StateThinking}, "Thinking"},
		{"tool", State{Kind: StateTool, ToolName: "Bash"}, "Tool:Bash"},
		{"ask", State{Kind: StateTool, ToolName: "AskUserQuestion"}, "Tool:AskUserQuestion"},
		{"awaiting", State{Kind: StateAwaiting}, "Awaiting"},
		{"error", State{Kind: StateError}, "Error"},
		{"compacting", State{Kind: StateCompacting}, "Compacting"},
		{"unknown", State{Kind: StateKind(99)}, ""},
	}
	for _, c := range cases {
		if got := statePublishValue(c.s); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStatePublishArgs(t *testing.T) {
	since := time.Unix(1754700000, 0)
	args, ok := statePublishArgs("%3", "Idle", since)
	if !ok {
		t.Fatal("expected ok")
	}
	want := []string{
		"set-option", "-t", "%3", "@claudemux_state", "Idle",
		";",
		"set-option", "-t", "%3", "@claudemux_state_since", "1754700000",
	}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("got %v, want %v", args, want)
	}
}

func TestStatePublishArgsSkips(t *testing.T) {
	if _, ok := statePublishArgs("", "Idle", time.Now()); ok {
		t.Error("outside tmux (empty selfPane) must not publish")
	}
	if _, ok := statePublishArgs("%3", "", time.Now()); ok {
		t.Error("empty value must not publish")
	}
}

func TestPublishStateCmdNilWhenUnpublishable(t *testing.T) {
	if cmd := publishStateCmd("", State{Kind: StateIdle}, time.Now()); cmd != nil {
		t.Error("expected nil cmd outside tmux")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestStatePublish|TestPublishState' -v`
Expected: FAIL — `undefined: statePublishValue` (compile error).

- [ ] **Step 3: Write the implementation**

```go
package main

import (
	"context"
	"os/exec"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Session user options the head publishes for external tooling (the
// switchboard). @claudemux_state holds the machine form of the current state
// (statePublishValue); @claudemux_state_since its start as unix seconds.
const (
	statePublishOption      = "@claudemux_state"
	statePublishSinceOption = "@claudemux_state_since"
)

// statePublishValue is the machine form of s for the @claudemux_state option:
// the kind name, with tool states carrying the tool ("Tool:AskUserQuestion").
// Consumers key on exact values — see isWaiting — so this string is an
// interface, not display text; Label() stays free to change independently.
func statePublishValue(s State) string {
	switch s.Kind {
	case StateIdle:
		return "Idle"
	case StateThinking:
		return "Thinking"
	case StateTool:
		return "Tool:" + s.ToolName
	case StateAwaiting:
		return "Awaiting"
	case StateError:
		return "Error"
	case StateCompacting:
		return "Compacting"
	}
	return ""
}

// statePublishArgs builds one tmux invocation setting both options. `;` is a
// single argv element: tmux treats it as a command separator, so both options
// land in one subprocess. `-t` takes the pane id; tmux resolves the owning
// session for session-scoped options. ok=false outside tmux (selfPane empty)
// or with nothing to say.
func statePublishArgs(selfPane, value string, since time.Time) ([]string, bool) {
	if selfPane == "" || value == "" {
		return nil, false
	}
	return []string{
		"set-option", "-t", selfPane, statePublishOption, value,
		";",
		"set-option", "-t", selfPane, statePublishSinceOption, strconv.FormatInt(since.Unix(), 10),
	}, true
}

// publishStateCmd returns a tea.Cmd publishing s, or nil when there is nothing
// to do. Fire-and-forget with a hard deadline, like renameTabCmd: a wedged
// tmux must never block the TUI, and a failed publish just leaves the previous
// value for the next transition to overwrite.
func publishStateCmd(selfPane string, s State, now time.Time) tea.Cmd {
	since := s.Since
	if since.IsZero() {
		since = now
	}
	args, ok := statePublishArgs(selfPane, statePublishValue(s), since)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "tmux", args...).Run()
		return nil
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -run 'TestStatePublish|TestPublishState' -v`
Expected: PASS (all four).

- [ ] **Step 5: gofmt, full test run, commit**

```bash
gofmt -l cmd/claudemux-head        # must print nothing
go test ./...
git add cmd/claudemux-head/statepub.go cmd/claudemux-head/statepub_test.go
git commit -m "feat(head): state publish args and cmd for tmux user options"
```

---

### Task 2: Wire publishing into the head's poll loop (`tui.go`)

**Files:**
- Modify: `cmd/claudemux-head/tui.go` (model struct ~line 104-115; `dataMsg` case ~line 849-896)
- Test: `cmd/claudemux-head/statepub_test.go` (append)

**Interfaces:**
- Consumes: `statePublishValue`, `publishStateCmd` (Task 1); `model` fields `state`, `selfPane`.
- Produces: `func (m *model) maybePublishState(now time.Time) tea.Cmd` and model field `publishedState string`. Publishing happens on every `dataMsg` poll where the value changed.

- [ ] **Step 1: Write the failing test** (append to `statepub_test.go`)

```go
func TestMaybePublishState(t *testing.T) {
	m := &model{selfPane: "%3", state: State{Kind: StateIdle, Since: time.Unix(1754700000, 0)}}
	now := time.Now()
	if cmd := m.maybePublishState(now); cmd == nil {
		t.Fatal("first call after a state change must publish")
	}
	if m.publishedState != "Idle" {
		t.Errorf("publishedState = %q, want %q", m.publishedState, "Idle")
	}
	if cmd := m.maybePublishState(now); cmd != nil {
		t.Error("unchanged state must not republish")
	}
	m.state = State{Kind: StateThinking, Since: time.Unix(1754700100, 0)}
	if cmd := m.maybePublishState(now); cmd == nil {
		t.Error("changed state must publish again")
	}
}

func TestMaybePublishStateOutsideTmux(t *testing.T) {
	m := &model{state: State{Kind: StateIdle}}
	if cmd := m.maybePublishState(time.Now()); cmd != nil {
		t.Error("empty selfPane must yield nil cmd")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run TestMaybePublishState -v`
Expected: FAIL — `m.maybePublishState undefined` / `m.publishedState undefined`.

- [ ] **Step 3: Implement**

3a. Add the model field next to `selfPane` (`tui.go` ~line 110):

```go
	// publishedState is the last @claudemux_state value pushed to tmux, so the
	// poll loop republishes only on change — one subprocess per transition,
	// not per tick.
	publishedState string
```

3b. Add the method to `statepub.go`:

```go
// maybePublishState returns a cmd publishing the current state when its
// machine form changed since the last publish, nil otherwise. Cheap enough to
// call every poll. Outside tmux the cmd is nil every time — publishedState
// still updates, which is harmless: there is no consumer without tmux.
func (m *model) maybePublishState(now time.Time) tea.Cmd {
	v := statePublishValue(m.state)
	if v == m.publishedState {
		return nil
	}
	m.publishedState = v
	return publishStateCmd(m.selfPane, m.state, now)
}
```

3c. Wire into the `dataMsg` case. In `tui.go`, directly after `m.recomputeFromEvents(msg.time)` (~line 886), the case currently reads:

```go
		m.recomputeFromEvents(msg.time)
		m.lastUpdate = msg.time
		m.autoArmTeardown(prevTyped, msg.time)
		if m.shouldSummarize(prevKind, msg.time) {
			m.summarizing = true
			return m, m.summarize()
		}
		if m.shouldRetrySummarize(msg.time) {
			m.summarizing = true
			return m, m.summarize()
		}
```

Change it to:

```go
		m.recomputeFromEvents(msg.time)
		pubCmd := m.maybePublishState(msg.time)
		m.lastUpdate = msg.time
		m.autoArmTeardown(prevTyped, msg.time)
		if m.shouldSummarize(prevKind, msg.time) {
			m.summarizing = true
			return m, tea.Batch(m.summarize(), pubCmd)
		}
		if m.shouldRetrySummarize(msg.time) {
			m.summarizing = true
			return m, tea.Batch(m.summarize(), pubCmd)
		}
		return m, pubCmd
```

Before adding the trailing `return m, pubCmd`, confirm the `dataMsg` case previously fell through the switch to a bare `return m, nil` with no intervening code after the switch (it does today — verify nothing has been added). `tea.Batch` drops nil cmds, so the batched returns are safe when `pubCmd` is nil.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -run TestMaybePublishState -v` then `go test ./...`
Expected: PASS, full suite green (the wiring must not break existing `tui_test.go` expectations — if a test asserts on the `dataMsg` return cmd, adjust for the batch).

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -l cmd/claudemux-head
go test ./...
git add cmd/claudemux-head/tui.go cmd/claudemux-head/statepub.go cmd/claudemux-head/statepub_test.go
git commit -m "feat(head): publish session state to tmux user options on change"
```

---

### Task 3: Snapshot types + tmux output parsing (`switchboard.go`)

**Files:**
- Create: `cmd/claudemux-head/switchboard.go`
- Test: `cmd/claudemux-head/switchboard_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks (parses raw strings; option names re-declared via Task 1's constants).
- Produces:
  - `type swSession struct { Name, State string; Since time.Time }`
  - `type swSnapshot struct { Sessions []swSession; Lobby string; Clients map[string]string }` — `Sessions` holds live claudemux sessions (lobby excluded) in `list-sessions` order; `Clients` maps client name → its current session.
  - `func buildSwSnapshot(sessOut, paneOut, clientOut, selfPane string) swSnapshot`
  - `func isWaiting(state string) bool`
  - `func (s swSnapshot) session(name string) (swSession, bool)`

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"testing"
	"time"
)

// Raw tmux outputs as the switchboard's three -F formats produce them.
// An unset user option renders as an empty field.
const (
	swSessOut = "api\tIdle\t1754700000\n" +
		"web\tTool:AskUserQuestion\t1754700100\n" +
		"scratch\t\t\n" +
		"switchboard\t\t\n" +
		"plain\t\t\n"
	swPaneOut = "api\t%1\tclaudemux-head\n" +
		"api\t%2\tclaude\n" +
		"web\t%5\tclaudemux-head\n" +
		"scratch\t%7\tclaudemux-head\n" +
		"switchboard\t%9\tclaudemux-head\n" +
		"plain\t%11\tzsh\n"
	swClientOut = "/dev/ttys001\tswitchboard\n/dev/ttys002\tplain\n"
)

func TestBuildSwSnapshot(t *testing.T) {
	s := buildSwSnapshot(swSessOut, swPaneOut, swClientOut, "%9")
	if s.Lobby != "switchboard" {
		t.Errorf("Lobby = %q, want switchboard", s.Lobby)
	}
	// plain has no head pane; switchboard is the lobby: both excluded.
	if len(s.Sessions) != 3 {
		t.Fatalf("Sessions = %v, want api, web, scratch", s.Sessions)
	}
	api, ok := s.session("api")
	if !ok || api.State != "Idle" || !api.Since.Equal(time.Unix(1754700000, 0)) {
		t.Errorf("api = %+v, ok=%v", api, ok)
	}
	web, _ := s.session("web")
	if web.State != "Tool:AskUserQuestion" {
		t.Errorf("web.State = %q", web.State)
	}
	scratch, _ := s.session("scratch")
	if scratch.State != "" || !scratch.Since.IsZero() {
		t.Errorf("unset options must parse to zero values, got %+v", scratch)
	}
	if s.Clients["/dev/ttys001"] != "switchboard" || s.Clients["/dev/ttys002"] != "plain" {
		t.Errorf("Clients = %v", s.Clients)
	}
}

func TestBuildSwSnapshotUnknownSelfPane(t *testing.T) {
	s := buildSwSnapshot(swSessOut, swPaneOut, swClientOut, "%404")
	if s.Lobby != "" {
		t.Errorf("Lobby = %q, want empty for unknown pane", s.Lobby)
	}
	// Without a lobby to exclude, all four head-bearing sessions survive.
	if len(s.Sessions) != 4 {
		t.Errorf("Sessions = %v, want 4", s.Sessions)
	}
}

func TestBuildSwSnapshotMalformedLines(t *testing.T) {
	s := buildSwSnapshot("half\n\n", "junk\n", "alsojunk\n", "%9")
	if len(s.Sessions) != 0 || len(s.Clients) != 0 {
		t.Errorf("malformed lines must be skipped, got %+v", s)
	}
}

func TestIsWaiting(t *testing.T) {
	cases := map[string]bool{
		"Idle":                 true,
		"Tool:AskUserQuestion": true,
		"Thinking":             false,
		"Tool:Bash":            false,
		"Compacting":           false,
		"":                     false,
	}
	for state, want := range cases {
		if got := isWaiting(state); got != want {
			t.Errorf("isWaiting(%q) = %v, want %v", state, got, want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestBuildSwSnapshot|TestIsWaiting' -v`
Expected: FAIL — `undefined: buildSwSnapshot`.

- [ ] **Step 3: Implement**

```go
package main

import (
	"strconv"
	"strings"
	"time"
)

// The switchboard: a lobby session that ferries the attached tmux client to
// claudemux sessions waiting on input. This file is the poll side — snapshot
// types and parsers for the three tmux listing calls. The conductor that
// consumes snapshots lives in swconductor.go; the TUI in switchboardtui.go.
// Design: docs/superpowers/specs/2026-08-09-switchboard-design.md.

// swHeadCommand identifies a claudemux session: some pane runs this binary.
// pane_current_command reports the executable basename.
const swHeadCommand = "claudemux-head"

type swSession struct {
	Name  string
	State string    // raw @claudemux_state; "" when unset (pre-publish head)
	Since time.Time // zero when unset or unparseable
}

type swSnapshot struct {
	// Sessions holds live claudemux sessions — those with a claudemux-head
	// pane — excluding the lobby itself (whose own pane also runs this
	// binary), in list-sessions order.
	Sessions []swSession
	Lobby    string            // session owning selfPane; "" if not found
	Clients  map[string]string // client name -> session it is attached to
}

// isWaiting reports whether a published state means "paused waiting on
// input": Claude's turn ended, or an AskUserQuestion is pending. Exact-match
// on statePublishValue strings — anything unknown is not waiting.
func isWaiting(state string) bool {
	return state == "Idle" || state == "Tool:AskUserQuestion"
}

func (s swSnapshot) session(name string) (swSession, bool) {
	for _, sess := range s.Sessions {
		if sess.Name == name {
			return sess, true
		}
	}
	return swSession{}, false
}

// buildSwSnapshot assembles a snapshot from the raw output of the three tmux
// calls (see swPollCmd). Malformed lines are skipped, not fatal: a snapshot
// built from whatever parsed keeps the lobby rendering through transient
// oddities. Formats (tab-separated):
//
//	sessOut:   #{session_name} #{@claudemux_state} #{@claudemux_state_since}
//	paneOut:   #{session_name} #{pane_id} #{pane_current_command}
//	clientOut: #{client_name} #{client_session}
func buildSwSnapshot(sessOut, paneOut, clientOut, selfPane string) swSnapshot {
	snap := swSnapshot{Clients: map[string]string{}}

	heads := map[string]bool{}
	for _, line := range strings.Split(paneOut, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		if f[2] == swHeadCommand {
			heads[f[0]] = true
		}
		if f[1] == selfPane && selfPane != "" {
			snap.Lobby = f[0]
		}
	}

	for _, line := range strings.Split(sessOut, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 || f[0] == "" {
			continue
		}
		if !heads[f[0]] || f[0] == snap.Lobby {
			continue
		}
		sess := swSession{Name: f[0], State: f[1]}
		if secs, err := strconv.ParseInt(f[2], 10, 64); err == nil {
			sess.Since = time.Unix(secs, 0)
		}
		snap.Sessions = append(snap.Sessions, sess)
	}

	for _, line := range strings.Split(clientOut, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 2 || f[0] == "" {
			continue
		}
		snap.Clients[f[0]] = f[1]
	}
	return snap
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -run 'TestBuildSwSnapshot|TestIsWaiting' -v`
Expected: PASS.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -l cmd/claudemux-head
go test ./...
git add cmd/claudemux-head/switchboard.go cmd/claudemux-head/switchboard_test.go
git commit -m "feat(head): switchboard snapshot types and tmux output parsing"
```

---

### Task 4: The conductor state machine (`swconductor.go`)

**Files:**
- Create: `cmd/claudemux-head/swconductor.go`
- Test: `cmd/claudemux-head/swconductor_test.go`

**Interfaces:**
- Consumes: `swSnapshot`, `swSession`, `isWaiting` (Task 3).
- Produces:
  - `type conductor struct` with exported-to-package fields `phase swPhase`, `client string`, `escortee string`, `snoozed map[string]time.Time`
  - `swPhase` constants: `swParked`, `swEscorting`, `swPaused`
  - `func newConductor() conductor`
  - `func (c *conductor) step(s swSnapshot) (swAction, bool)` — `swAction{Client, Target string}`; `ok=true` means "issue `tmux switch-client -c Client -t Target`"
  - `func (c *conductor) statusLine(s swSnapshot) string` — for the lobby's status row
  - `func (s swSnapshot) waitingQueue(snoozed map[string]time.Time) []swSession`

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"testing"
	"time"
)

// snapAt builds a snapshot with one client on `at`, lobby "switchboard".
func snapAt(at string, sessions ...swSession) swSnapshot {
	return swSnapshot{
		Sessions: sessions,
		Lobby:    "switchboard",
		Clients:  map[string]string{"/dev/ttys001": at},
	}
}

func waiting(name string, since int64) swSession {
	return swSession{Name: name, State: "Idle", Since: time.Unix(since, 0)}
}

func busy(name string) swSession {
	return swSession{Name: name, State: "Thinking", Since: time.Unix(1754700000, 0)}
}

func TestWaitingQueueOrdersOldestFirst(t *testing.T) {
	s := snapAt("switchboard", waiting("young", 200), waiting("old", 100), busy("work"))
	q := s.waitingQueue(nil)
	if len(q) != 2 || q[0].Name != "old" || q[1].Name != "young" {
		t.Errorf("queue = %v", q)
	}
}

func TestWaitingQueueSnoozeAndTiebreak(t *testing.T) {
	s := snapAt("switchboard", waiting("b", 100), waiting("a", 100))
	q := s.waitingQueue(map[string]time.Time{"a": time.Unix(100, 0)})
	if len(q) != 1 || q[0].Name != "b" {
		t.Errorf("snoozed session must be excluded, queue = %v", q)
	}
	// A new waiting episode (different Since) un-snoozes.
	q = s.waitingQueue(map[string]time.Time{"a": time.Unix(50, 0)})
	if len(q) != 2 || q[0].Name != "a" || q[1].Name != "b" {
		t.Errorf("same-Since tiebreak is by name, queue = %v", q)
	}
}

func TestConductorParkedDispatchesOldest(t *testing.T) {
	c := newConductor()
	act, ok := c.step(snapAt("switchboard", waiting("old", 100), waiting("young", 200)))
	if !ok || act.Target != "old" || act.Client != "/dev/ttys001" {
		t.Fatalf("act = %+v ok=%v", act, ok)
	}
	if c.phase != swEscorting || c.escortee != "old" {
		t.Errorf("phase=%v escortee=%q", c.phase, c.escortee)
	}
}

func TestConductorParkedIdleFleetNoAction(t *testing.T) {
	c := newConductor()
	if _, ok := c.step(snapAt("switchboard", busy("work"))); ok {
		t.Error("nothing waiting: no switch")
	}
	if c.phase != swParked {
		t.Errorf("phase = %v, want parked", c.phase)
	}
}

func TestConductorEscortHoldsWhileWaiting(t *testing.T) {
	c := newConductor()
	c.step(snapAt("switchboard", waiting("old", 100)))
	if _, ok := c.step(snapAt("old", waiting("old", 100))); ok {
		t.Error("must hold while escortee still waits")
	}
}

func TestConductorEscortAdvancesOnResolve(t *testing.T) {
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100), waiting("b", 200)))
	act, ok := c.step(snapAt("a", busy("a"), waiting("b", 200)))
	if !ok || act.Target != "b" {
		t.Fatalf("act = %+v ok=%v", act, ok)
	}
	if c.phase != swEscorting || c.escortee != "b" {
		t.Errorf("phase=%v escortee=%q", c.phase, c.escortee)
	}
}

func TestConductorEscortReturnsToLobbyWhenQueueEmpty(t *testing.T) {
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100)))
	act, ok := c.step(snapAt("a", busy("a")))
	if !ok || act.Target != "switchboard" {
		t.Fatalf("act = %+v ok=%v", act, ok)
	}
	if c.phase != swParked {
		t.Errorf("phase = %v, want parked", c.phase)
	}
}

func TestConductorEscortGoneSessionCountsResolved(t *testing.T) {
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100)))
	act, ok := c.step(snapAt("a"))
	if !ok || act.Target != "switchboard" {
		t.Fatalf("vanished escortee must resolve, act=%+v ok=%v", act, ok)
	}
}

func TestConductorManualLeavePausesAndSnoozes(t *testing.T) {
	c := newConductor()
	c.step(snapAt("switchboard", waiting("a", 100)))
	// User switched the client to some other session while a still waits.
	if _, ok := c.step(snapAt("elsewhere", waiting("a", 100))); ok {
		t.Fatal("manual navigation must not trigger a switch")
	}
	if c.phase != swPaused {
		t.Fatalf("phase = %v, want paused", c.phase)
	}
	if got, ok := c.snoozed["a"]; !ok || !got.Equal(time.Unix(100, 0)) {
		t.Errorf("snoozed[a] = %v ok=%v", got, ok)
	}
	// Back at the lobby: resume. The snoozed episode must NOT re-dispatch.
	if _, ok := c.step(snapAt("switchboard", waiting("a", 100))); ok {
		t.Error("resume tick must not redispatch a snoozed session")
	}
	if c.phase != swParked {
		t.Errorf("phase = %v, want parked after lobby return", c.phase)
	}
	// New waiting episode: dispatch again.
	act, ok := c.step(snapAt("switchboard", waiting("a", 300)))
	if !ok || act.Target != "a" {
		t.Errorf("new episode must dispatch, act=%+v ok=%v", act, ok)
	}
}

func TestConductorParkedUserWandersOff(t *testing.T) {
	c := newConductor()
	c.step(snapAt("switchboard", busy("a")))
	if _, ok := c.step(snapAt("a", busy("a"))); ok {
		t.Fatal("no switch when user wandered off")
	}
	if c.phase != swPaused {
		t.Errorf("phase = %v, want paused", c.phase)
	}
}

func TestConductorNoClient(t *testing.T) {
	c := newConductor()
	s := swSnapshot{Sessions: []swSession{waiting("a", 100)}, Lobby: "switchboard", Clients: map[string]string{}}
	if _, ok := c.step(s); ok {
		t.Error("no client: nothing to drive")
	}
}

func TestConductorPicksLobbyClientDeterministically(t *testing.T) {
	c := newConductor()
	s := swSnapshot{
		Sessions: []swSession{waiting("a", 100)},
		Lobby:    "switchboard",
		Clients: map[string]string{
			"/dev/ttys009": "switchboard",
			"/dev/ttys001": "switchboard",
		},
	}
	act, ok := c.step(s)
	if !ok || act.Client != "/dev/ttys001" {
		t.Errorf("must pick lexicographically smallest lobby client, act=%+v ok=%v", act, ok)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestConductor|TestWaitingQueue' -v`
Expected: FAIL — `undefined: newConductor`.

- [ ] **Step 3: Implement**

```go
package main

import (
	"fmt"
	"sort"
	"time"
)

// The conductor decides when to move the driven tmux client. It is pure —
// step() consumes a snapshot and returns at most one switch-client action —
// so every policy in the spec is unit-testable without tmux.
type swPhase int

const (
	// swParked: the client sits on the lobby; dispatch when something waits.
	swParked swPhase = iota
	// swEscorting: the conductor moved the client to escortee; hold until
	// that session stops waiting.
	swEscorting
	// swPaused: the client is somewhere the conductor didn't put it. Never
	// fight the user — resume only when they return to the lobby.
	swPaused
)

type swAction struct {
	Client string // tmux client_name to move
	Target string // session to switch it to
}

type conductor struct {
	phase    swPhase
	client   string
	escortee string
	// snoozed maps session -> the Since of a waiting episode the user
	// deliberately walked away from. That episode never re-queues; a new
	// episode (different Since) does. Without this, skipping an Idle session
	// would bounce the client straight back to it from the lobby.
	snoozed map[string]time.Time
}

func newConductor() conductor {
	return conductor{snoozed: map[string]time.Time{}}
}

// waitingQueue lists waiting, un-snoozed sessions oldest-first (name as
// tiebreak so equal timestamps still order deterministically).
func (s swSnapshot) waitingQueue(snoozed map[string]time.Time) []swSession {
	var q []swSession
	for _, sess := range s.Sessions {
		if !isWaiting(sess.State) {
			continue
		}
		if t, ok := snoozed[sess.Name]; ok && t.Equal(sess.Since) {
			continue
		}
		q = append(q, sess)
	}
	sort.SliceStable(q, func(i, j int) bool {
		if !q[i].Since.Equal(q[j].Since) {
			return q[i].Since.Before(q[j].Since)
		}
		return q[i].Name < q[j].Name
	})
	return q
}

// resolveClient keeps driving the same client while it exists, else adopts
// the lexicographically smallest client attached to the lobby (deterministic
// under Go's random map order). No lobby client means nothing to drive.
func (c *conductor) resolveClient(s swSnapshot) {
	if c.client != "" {
		if _, ok := s.Clients[c.client]; ok {
			return
		}
		c.client = ""
	}
	names := make([]string, 0, len(s.Clients))
	for name, sess := range s.Clients {
		if sess == s.Lobby && s.Lobby != "" {
			names = append(names, name)
		}
	}
	if len(names) > 0 {
		sort.Strings(names)
		c.client = names[0]
	}
}

// pruneSnoozes drops snoozes whose episode ended: session gone, no longer
// waiting, or waiting anew with a different Since. Keeping the map minimal
// makes state inspectable and stops unbounded growth across long runs.
func (c *conductor) pruneSnoozes(s swSnapshot) {
	for name, since := range c.snoozed {
		sess, ok := s.session(name)
		if !ok || !isWaiting(sess.State) || !sess.Since.Equal(since) {
			delete(c.snoozed, name)
		}
	}
}

// step advances the conductor by one poll. ok=true carries the single
// switch-client to issue this tick.
func (c *conductor) step(s swSnapshot) (swAction, bool) {
	c.pruneSnoozes(s)
	c.resolveClient(s)
	if c.client == "" {
		return swAction{}, false
	}
	cur := s.Clients[c.client]
	queue := s.waitingQueue(c.snoozed)

	switch c.phase {
	case swParked:
		if cur != s.Lobby {
			c.phase = swPaused
			return swAction{}, false
		}
		if len(queue) > 0 {
			c.phase = swEscorting
			c.escortee = queue[0].Name
			return swAction{Client: c.client, Target: c.escortee}, true
		}
	case swEscorting:
		if cur != c.escortee {
			// The user moved themselves. Snooze the abandoned session's
			// current episode so the lobby doesn't bounce them right back.
			if sess, ok := s.session(c.escortee); ok && isWaiting(sess.State) {
				c.snoozed[c.escortee] = sess.Since
			}
			c.escortee = ""
			if cur == s.Lobby {
				c.phase = swParked
			} else {
				c.phase = swPaused
			}
			return swAction{}, false
		}
		if sess, ok := s.session(c.escortee); !ok || !isWaiting(sess.State) {
			c.escortee = ""
			if len(queue) > 0 {
				c.escortee = queue[0].Name
				return swAction{Client: c.client, Target: c.escortee}, true
			}
			c.phase = swParked
			return swAction{Client: c.client, Target: s.Lobby}, true
		}
	case swPaused:
		if cur == s.Lobby {
			c.phase = swParked
		}
	}
	return swAction{}, false
}

// statusLine summarizes the conductor for the lobby's bottom row.
func (c *conductor) statusLine(s swSnapshot) string {
	n := len(s.waitingQueue(c.snoozed))
	switch c.phase {
	case swPaused:
		return "paused — you navigated away; return here to resume"
	case swEscorting:
		return fmt.Sprintf("escorting → %s · %d waiting", c.escortee, n)
	}
	return fmt.Sprintf("conducting · %d waiting", n)
}
```

Note for the implementer: `TestConductorManualLeavePausesAndSnoozes` returns to the lobby while `a` is still snoozed — the resume tick flips Paused→Parked and must NOT dispatch in the same step (dispatch happens from Parked on the *next* tick, and the snooze keeps `a` out of the queue anyway). The `TestConductorEscortAdvancesOnResolve` queue excludes the resolved escortee naturally because it is no longer waiting.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -run 'TestConductor|TestWaitingQueue' -v`
Expected: PASS (all twelve).

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -l cmd/claudemux-head
go test ./...
git add cmd/claudemux-head/swconductor.go cmd/claudemux-head/swconductor_test.go
git commit -m "feat(head): switchboard conductor state machine"
```

---

### Task 5: Switchboard TUI + subcommand dispatch (`switchboardtui.go`, `main.go`)

**Files:**
- Create: `cmd/claudemux-head/switchboardtui.go`
- Modify: `cmd/claudemux-head/main.go:45` (add dispatch after the `hook` block, before `flag.String`)
- Test: `cmd/claudemux-head/switchboardtui_test.go`

**Interfaces:**
- Consumes: `buildSwSnapshot`, `swSnapshot`, `isWaiting` (Task 3); `conductor`, `newConductor`, `swAction` (Task 4); `formatDuration` (`tui.go:1537`).
- Produces: `runSwitchboard(stderr io.Writer) int`; `swModel` implementing `tea.Model`; messages `swTickMsg`, `swSnapshotMsg`; `main.go` dispatches `claudemux-head switchboard`.

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func swTestModel() swModel {
	m := newSwModel("%9")
	m.width, m.height = 80, 24
	m.snap = swSnapshot{
		Sessions: []swSession{
			{Name: "api", State: "Idle", Since: time.Now().Add(-2 * time.Minute)},
			{Name: "web", State: "Thinking", Since: time.Now()},
			{Name: "scratch"},
		},
		Lobby:   "switchboard",
		Clients: map[string]string{"/dev/ttys001": "switchboard"},
	}
	m.cond.client = "/dev/ttys001"
	return m
}

func TestSwModelSelectionKeys(t *testing.T) {
	m := swTestModel()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = next.(swModel)
	if m.sel != 1 {
		t.Errorf("sel = %d, want 1", m.sel)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = next.(swModel)
	if m.sel != 0 {
		t.Errorf("sel = %d, want 0", m.sel)
	}
	// k at the top and j past the end clamp.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if next.(swModel).sel != 0 {
		t.Error("k at top must clamp")
	}
}

func TestSwModelEnterSwitchesToSelection(t *testing.T) {
	m := swTestModel()
	m.sel = 1
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Error("enter with a client must produce a switch cmd")
	}
	m.cond.client = ""
	m.snap.Clients = map[string]string{}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("enter without a client must be a no-op")
	}
}

func TestSwModelQuits(t *testing.T) {
	m := swTestModel()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("q must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q must produce tea.Quit")
	}
}

func TestSwModelSnapshotClampsSelection(t *testing.T) {
	m := swTestModel()
	m.sel = 2
	next, _ := m.Update(swSnapshotMsg{snap: swSnapshot{
		Sessions: []swSession{{Name: "api", State: "Idle"}},
		Lobby:    "switchboard",
		Clients:  map[string]string{"/dev/ttys001": "switchboard"},
	}})
	if next.(swModel).sel != 0 {
		t.Errorf("sel = %d, want clamped to 0", next.(swModel).sel)
	}
}

func TestSwModelViewShowsFleet(t *testing.T) {
	m := swTestModel()
	view := m.View()
	for _, want := range []string{"api", "web", "scratch", "waiting"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestSwModelViewEmptyFleet(t *testing.T) {
	m := newSwModel("%9")
	m.width, m.height = 80, 24
	if v := m.View(); !strings.Contains(v, "no claudemux sessions") {
		t.Errorf("empty fleet needs a hint, got:\n%s", v)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run TestSwModel -v`
Expected: FAIL — `undefined: newSwModel`.

- [ ] **Step 3: Implement `switchboardtui.go`**

```go
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The switchboard's Bubble Tea shell: a full-screen fleet list that runs the
// poll/conduct loop. All decisions live in the conductor; this file only
// schedules polls, renders, and executes the returned action.

const swPollInterval = time.Second

type swTickMsg time.Time

type swSnapshotMsg struct {
	snap swSnapshot
	err  error
}

var (
	swTitleStyle   = lipgloss.NewStyle().Bold(true)
	swWaitStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	swBusyStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	swUnknownStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	swSelStyle     = lipgloss.NewStyle().Reverse(true)
	swStatusStyle  = lipgloss.NewStyle().Faint(true)
)

type swModel struct {
	selfPane string
	snap     swSnapshot
	cond     conductor
	sel      int
	width    int
	height   int
	lastErr  string
}

func newSwModel(selfPane string) swModel {
	return swModel{selfPane: selfPane, cond: newConductor()}
}

func (m swModel) Init() tea.Cmd { return swPollCmd(m.selfPane) }

// swPollCmd runs the three tmux listings off the update loop. Any failure
// returns the error instead of a snapshot — the model keeps its previous
// data on screen and simply tries again next tick.
func swPollCmd(selfPane string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sessOut, err := swTmux(ctx, "list-sessions", "-F",
			"#{session_name}\t#{"+statePublishOption+"}\t#{"+statePublishSinceOption+"}")
		if err != nil {
			return swSnapshotMsg{err: err}
		}
		paneOut, err := swTmux(ctx, "list-panes", "-a", "-F",
			"#{session_name}\t#{pane_id}\t#{pane_current_command}")
		if err != nil {
			return swSnapshotMsg{err: err}
		}
		clientOut, err := swTmux(ctx, "list-clients", "-F",
			"#{client_name}\t#{client_session}")
		if err != nil {
			return swSnapshotMsg{err: err}
		}
		return swSnapshotMsg{snap: buildSwSnapshot(sessOut, paneOut, clientOut, selfPane)}
	}
}

func swTmux(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	return string(out), err
}

// swSwitchCmd moves a client. Fire-and-forget: if tmux refuses (client went
// away mid-tick), the next poll sees reality and the conductor re-decides.
func swSwitchCmd(client, target string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "tmux", "switch-client", "-c", client, "-t", target).Run()
		return nil
	}
}

func swNextTick() tea.Cmd {
	return tea.Tick(swPollInterval, func(t time.Time) tea.Msg { return swTickMsg(t) })
}

func (m swModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case swTickMsg:
		return m, swPollCmd(m.selfPane)
	case swSnapshotMsg:
		// Schedule the next tick from here, not from swTickMsg: polls never
		// overlap, and a slow tmux stretches the interval instead of queueing.
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, swNextTick()
		}
		m.lastErr = ""
		m.snap = msg.snap
		if m.sel >= len(m.snap.Sessions) {
			m.sel = len(m.snap.Sessions) - 1
		}
		if m.sel < 0 {
			m.sel = 0
		}
		if act, ok := m.cond.step(m.snap); ok {
			return m, tea.Batch(swNextTick(), swSwitchCmd(act.Client, act.Target))
		}
		return m, swNextTick()
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			if m.sel < len(m.snap.Sessions)-1 {
				m.sel++
			}
		case "k", "up":
			if m.sel > 0 {
				m.sel--
			}
		case "enter":
			// A manual jump; the conductor notices the client moved on the
			// next poll and pauses — no special bookkeeping here.
			if m.sel < len(m.snap.Sessions) && m.cond.client != "" {
				return m, swSwitchCmd(m.cond.client, m.snap.Sessions[m.sel].Name)
			}
		}
	}
	return m, nil
}

func (m swModel) View() string {
	var b strings.Builder
	b.WriteString(swTitleStyle.Render("claudemux switchboard") + "\n\n")
	if len(m.snap.Sessions) == 0 {
		b.WriteString(swUnknownStyle.Render("no claudemux sessions") + "\n")
	}
	now := time.Now()
	for i, sess := range m.snap.Sessions {
		marker := "  "
		if isWaiting(sess.State) {
			marker = swWaitStyle.Render("● ")
		}
		state, style := sess.State, swBusyStyle
		switch {
		case sess.State == "":
			state, style = "unknown", swUnknownStyle
		case isWaiting(sess.State):
			style = swWaitStyle
		}
		age := ""
		if !sess.Since.IsZero() {
			age = " " + swUnknownStyle.Render(formatDuration(now.Sub(sess.Since)))
		}
		name := sess.Name
		if i == m.sel {
			name = swSelStyle.Render(name)
		}
		fmt.Fprintf(&b, " %s%-24s %s%s\n", marker, name, style.Render(state), age)
	}
	b.WriteString("\n" + swStatusStyle.Render(m.cond.statusLine(m.snap)) + "\n")
	if m.lastErr != "" {
		b.WriteString(swStatusStyle.Render("tmux: "+m.lastErr) + "\n")
	}
	b.WriteString(swStatusStyle.Render("j/k select · enter jump · q quit"))
	return b.String()
}

// runSwitchboard is the `claudemux-head switchboard` entry point.
func runSwitchboard(stderr io.Writer) int {
	selfPane := os.Getenv("TMUX_PANE")
	if selfPane == "" {
		fmt.Fprintln(stderr, "claudemux-head switchboard must run inside tmux (start it with `claudemux switch`)")
		return 1
	}
	p := tea.NewProgram(newSwModel(selfPane), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}
```

Caveats for the implementer:
- `%-24s` pads by byte length; the selected row's `name` carries ANSI codes so its padding differs. Render the marker/name/state as separately styled cells if the columns visibly misalign (lipgloss `Width()` helps) — but only if it misaligns; keep it simple first. The name-selection test only checks behavior, not alignment.
- `formatDuration` already exists at `tui.go:1537` — do NOT write a new one. Check its signature first and adapt the call if it differs from `func(time.Duration) string`.

3b. Add dispatch in `main.go` after the `hook` block (line 45), before the `sessionFlag` declaration:

```go
	if len(os.Args) > 1 && os.Args[1] == "switchboard" {
		os.Exit(runSwitchboard(os.Stderr))
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -run TestSwModel -v` then `go test ./...` and `go vet ./...`
Expected: PASS, suite green, vet clean.

- [ ] **Step 5: Smoke-run the binary** (no tmux needed for the guard)

Run: `go build -o /tmp/cmh ./cmd/claudemux-head && env -u TMUX_PANE /tmp/cmh switchboard; echo "exit=$?"`
Expected: the inside-tmux error message and `exit=1`.

- [ ] **Step 6: gofmt + commit**

```bash
gofmt -l cmd/claudemux-head
git add cmd/claudemux-head/switchboardtui.go cmd/claudemux-head/switchboardtui_test.go cmd/claudemux-head/main.go
git commit -m "feat(head): switchboard TUI and subcommand"
```

---

### Task 6: `claudemux switch` launcher entry + README

**Files:**
- Modify: `bin/claudemux` (insert after `shift $((OPTIND - 1))`, line 54 — but see placement note)
- Modify: `README.md` (new section after "The pane model")

**Interfaces:**
- Consumes: `claudemux-head switchboard` (Task 5).
- Produces: `claudemux switch` creates/attaches the `switchboard` tmux session.

- [ ] **Step 1: Add the subcommand to `bin/claudemux`**

Placement: the check must run before the no-args/positional-args dispatch at the bottom (line ~577 `if [ "$#" -eq 0 ]`), and it needs no helper functions, so put it immediately before that block:

```bash
# `claudemux switch` — the switchboard: a lobby session whose pane runs
# `claudemux-head switchboard`, auto-ferrying the attached client to sessions
# waiting on input. The pane runs the program directly (no shell), matching
# the head/claude panes: exiting the switchboard closes the pane. A project
# directory literally named "switchboard" collides with this session name;
# the switchboard tolerates it (it just watches from inside that session),
# so first-wins is acceptable.
if [ "${1:-}" = "switch" ]; then
  head_bin="$(command -v claudemux-head || true)"
  head_bin="${head_bin:-claudemux-head}"
  if ! tmux has-session -t '=switchboard' 2>/dev/null; then
    tmux new-session -d -s switchboard "$head_bin switchboard"
  fi
  if [ -n "${TMUX:-}" ]; then
    exec tmux switch-client -t '=switchboard'
  fi
  tmux attach -t '=switchboard'
  exit 0
fi
```

Note the `=` prefix: tmux targets are prefix-matched by default; `=switchboard` forces an exact match so a session named e.g. `switchboard-2` can't be grabbed by mistake.

- [ ] **Step 2: Syntax-check and smoke-test**

Run: `bash -n bin/claudemux`
Expected: no output.

Run (only if a tmux server is safe to touch on this machine — skip otherwise and say so):
`PATH="$PWD/bin:$PATH" claudemux switch` from outside tmux in a scratch terminal is a manual step for the human; do not automate it. Instead verify the command wiring statically: `grep -n 'switchboard' bin/claudemux` shows the new block.

- [ ] **Step 3: Document in README.md**

Insert after the "The pane model" section:

```markdown
## The switchboard

`claudemux switch` opens a **switchboard**: a full-screen lobby session that watches
every claudemux session and automatically carries your tmux client to whichever one
is waiting on input — Claude's turn ended, or it asked you a question — oldest first.
Answer, and it moves you to the next waiting session; when nothing waits, you're
returned to the lobby.

It never fights you for the client: switch away manually and it pauses until you
come back to the lobby. A session you deliberately walk away from isn't re-queued
until it starts waiting again for a new reason.

Keys in the lobby: `j`/`k` select, `Enter` jumps to a session (and pauses
conducting), `q` quits.
```

- [ ] **Step 4: Full suite + commit**

```bash
bash -n bin/claudemux
go test ./...
git add bin/claudemux README.md
git commit -m "feat: claudemux switch launcher entry and README section"
```
