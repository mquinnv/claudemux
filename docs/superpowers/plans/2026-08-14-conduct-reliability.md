# Conduct Reliability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make conducting to asking/waiting sessions reliable by (1) merging the head-pane launch fix, (2) auto-restarting both TUIs when the binary on disk is rebuilt, and (3) resuming conduction from a paused conductor once the user hands their current session back to Claude.

**Architecture:** A new `binwatch.go` provides launch-time binary stamping and settled-change detection; both the session head and the switchboard check it on their existing 1s ticks and re-exec through the existing `restartSelf` path. The conductor gains paused-session observation state so a waiting→not-waiting transition of the session the user sits in marks them "conductible" again.

**Tech Stack:** Go, Bubble Tea, tmux. Tests with the stdlib `testing` package, following existing `swconductor_test.go` helpers.

**Spec:** Inline, below (diagnosis session 2026-08-14; no separate spec doc).

## Spec summary

Diagnosed root cause: heads (including the switchboard, which hosts the conductor) run for days and keep their launch-time binary, so features like the Asking state silently don't exist in the running fleet. Confirmed via `lsof` (running text inode ≠ on-disk inode) and a live recording where a session published `Asking` for 4 minutes while the stale conductor ignored it. Requirements, from Michael:

1. Merge branch `worktree-fix-head-pane-new-project` (f8301e4) — heads must not die on brand-new projects.
2. Auto-restart on binary change for BOTH the session head and the switchboard; the switchboard currently has no restart path at all (no `R` key, and `main.go`'s restart check only type-asserts the session-head `model`).
3. Conductor policy: when the user navigated somewhere themselves (swPaused) and then hands that session back to Claude (it transitions waiting → not-waiting under them), conducting resumes: they are escorted to the next waiting session, now or whenever one appears — until they move themselves again or return to the lobby. Jumping into an already-busy session to watch it must NEVER yank them (transition-triggered, not condition-triggered).

## Global Constraints

- Comments explain constraints the code can't show, in the repo's existing discursive style; no "what the next line does" comments.
- Every `c.phase = swPaused` / paused-exit path must keep the new paused-observation fields consistent (see Task 5's clearing rules).
- Auto-restart must never fire mid-teardown (session head) nor while escorting/paused/standby/creating (switchboard).
- `binSettle` is 2 seconds — never act on an mtime younger than that (`go install` writes in place; exec'ing a half-written binary kills the pane).
- Run tests as `go test ./...` from the worktree root; all pre-existing tests must stay green after every task.

---

### Task 1: Merge the head-pane launch fix

**Files:**
- Modify: merge commit touching `cmd/claudemux-head/{main.go,session.go,state.go,statepub.go,tui.go}` plus `waiting_test.go`, `statepub_test.go`

**Interfaces:**
- Consumes: branch `worktree-fix-head-pane-new-project` (commit f8301e4), already in this repo.
- Produces: `StateWaiting` kind (publishes `"Waiting"`, excluded from `isWaiting`), `waitingTranscript` placeholder binding. Later tasks build on the merged tree.

- [ ] **Step 1: Merge**

```bash
git merge worktree-fix-head-pane-new-project
```

If conflicts arise (most likely `tui.go`, `main.go`, `state.go`, `statepub.go` — main has moved 406e2fb→5c52572 with banner/esc-return/budget work): resolve by keeping BOTH sides' functionality; f8301e4's changes are additive (new `StateWaiting` kind, waiting-mode startup in `main.go`, placeholder adoption in `tui.go`). Read f8301e4's full diff first with `git show worktree-fix-head-pane-new-project`.

- [ ] **Step 2: Run the full suite**

Run: `go test ./...`
Expected: PASS everywhere, including the merged `waiting_test.go`.

- [ ] **Step 3: Verify askOverride/Waiting interaction**

`askOverride` (state.go) must still only upgrade `StateIdle, StateThinking, StateBackground` — a `StateWaiting` head has no transcript, so no marker can be fresher than nothing; confirm the switch in `askOverride` does NOT list `StateWaiting` and that `isWaiting` (switchboard.go) does not include `"Waiting"`. No code change expected; this is a read-check. If the merge somehow made either untrue, fix to match this spec.

- [ ] **Step 4: Commit (the merge commit from Step 1 suffices; amend only if Step 3 required fixes)**

---

### Task 2: Binary-change detection (`binwatch.go`)

**Files:**
- Create: `cmd/claudemux-head/binwatch.go`
- Test: `cmd/claudemux-head/binwatch_test.go`

**Interfaces:**
- Produces: `type binStamp struct{ path string; mtime time.Time; size int64 }`, `launchBinStamp() (binStamp, bool)`, `binStampOf(path string) (binStamp, bool)`, `binChanged(launch binStamp, now time.Time) bool`, `binChangedStamps(launch, cur binStamp, now time.Time) bool`, `const binSettle = 2 * time.Second`. Tasks 3 and 4 call `launchBinStamp` and `binChanged`.

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBinChangedStamps(t *testing.T) {
	base := time.Unix(1_754_700_000, 0)
	launch := binStamp{path: "/x", mtime: base, size: 100}
	cases := []struct {
		name string
		cur  binStamp
		now  time.Time
		want bool
	}{
		{"identical", binStamp{path: "/x", mtime: base, size: 100}, base.Add(time.Hour), false},
		{"newer and settled", binStamp{path: "/x", mtime: base.Add(time.Minute), size: 100}, base.Add(time.Minute + binSettle), true},
		{"newer but unsettled", binStamp{path: "/x", mtime: base.Add(time.Minute), size: 100}, base.Add(time.Minute + time.Second), false},
		{"same mtime different size", binStamp{path: "/x", mtime: base, size: 101}, base.Add(time.Hour), true},
	}
	for _, tc := range cases {
		if got := binChangedStamps(launch, tc.cur, tc.now); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestBinChangedStatsRealFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	launch, ok := binStampOf(p)
	if !ok {
		t.Fatal("stampOf failed")
	}
	// Unreplaced binary: never a change, regardless of clock.
	if binChanged(launch, time.Now().Add(time.Hour)) {
		t.Error("unchanged file reported changed")
	}
	// Replaced binary with an old mtime: changed and settled.
	if err := os.WriteFile(p, []byte("v2-longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if !binChanged(launch, time.Now()) {
		t.Error("replaced+settled file not reported changed")
	}
	// A vanished file (mid-replace) is "not changed"; the next poll re-checks.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if binChanged(launch, time.Now()) {
		t.Error("stat failure must read as unchanged")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run 'TestBinChanged' -v`
Expected: FAIL to compile — `binStamp` undefined.

- [ ] **Step 3: Write the implementation**

```go
package main

import (
	"os"
	"path/filepath"
	"time"
)

// Heads and the switchboard run for days while `go install` (or a release
// upgrade) replaces the binary under them, so the running fleet quietly
// diverges from the code on disk — the switchboard's conductor not knowing
// the then-new "Asking" state is exactly how this was noticed. binwatch
// detects "the binary on disk is no longer the one I am" so each TUI can
// re-exec itself (restartSelf) at a safe moment instead of waiting for a
// human to press R.

// binSettle is how long a changed binary's mtime must be in the past before
// the change is acted on. `go install` writes the file in place, and exec'ing
// a half-written binary would kill the pane.
const binSettle = 2 * time.Second

// binStamp identifies one on-disk build of this binary.
type binStamp struct {
	path  string
	mtime time.Time
	size  int64
}

// launchBinStamp stamps the binary this process is running, symlinks resolved
// first for the same reason siblingOfExecutable does. ok=false when anything
// fails; callers then never auto-restart, the safe default.
func launchBinStamp() (binStamp, bool) {
	exe, err := os.Executable()
	if err != nil {
		return binStamp{}, false
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return binStamp{}, false
	}
	return binStampOf(resolved)
}

func binStampOf(path string) (binStamp, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return binStamp{}, false
	}
	return binStamp{path: path, mtime: fi.ModTime(), size: fi.Size()}, true
}

// binChanged reports whether the binary at launch.path has been replaced and
// settled. A failed stat (deleted, or mid-replace) reads as unchanged — the
// caller polls, so the next tick re-checks.
func binChanged(launch binStamp, now time.Time) bool {
	cur, ok := binStampOf(launch.path)
	if !ok {
		return false
	}
	return binChangedStamps(launch, cur, now)
}

// binChangedStamps is the pure comparison: replaced means mtime or size
// moved; settled means the new mtime is at least binSettle in the past.
func binChangedStamps(launch, cur binStamp, now time.Time) bool {
	if cur.mtime.Equal(launch.mtime) && cur.size == launch.size {
		return false
	}
	return now.Sub(cur.mtime) >= binSettle
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/claudemux-head/ -run 'TestBinChanged' -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/binwatch.go cmd/claudemux-head/binwatch_test.go
git commit -m "feat(head): detect a rebuilt binary on disk (binwatch)"
```

---

### Task 3: Session head auto-restarts on binary change

**Files:**
- Modify: `cmd/claudemux-head/tui.go` (model struct near the existing `restart bool` field; `newModel`; the `case tickMsg:` handler)
- Test: `cmd/claudemux-head/restart_test.go` (append)

**Interfaces:**
- Consumes: `launchBinStamp`, `binChanged` from Task 2; existing `m.restart` + `restartSelf` flow in `main.go` (unchanged).
- Produces: `model` fields `launchBin binStamp`, `launchBinOK bool`; method `func (m *model) shouldAutoRestart(now time.Time) bool`.

- [ ] **Step 1: Write the failing test** (append to `restart_test.go`)

```go
func TestShouldAutoRestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp, ok := binStampOf(p)
	if !ok {
		t.Fatal("stampOf failed")
	}
	m := &model{launchBin: stamp, launchBinOK: true}
	now := time.Now()
	if m.shouldAutoRestart(now) {
		t.Error("unchanged binary must not restart")
	}
	if err := os.WriteFile(p, []byte("v2-longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-time.Minute)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if !m.shouldAutoRestart(now) {
		t.Error("replaced binary must restart an idle head")
	}
	m.teardown = teardownSent
	if m.shouldAutoRestart(now) {
		t.Error("must not restart mid-teardown")
	}
	m.teardown = teardownIdle
	m.launchBinOK = false
	if m.shouldAutoRestart(now) {
		t.Error("must not restart when the launch stamp failed")
	}
}
```

Add `"os"`, `"path/filepath"`, `"time"` to the test file's imports if absent. (`teardownSent` is an existing non-idle `teardownPhase` constant — teardown.go:147.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run TestShouldAutoRestart -v`
Expected: FAIL to compile — `launchBin`/`shouldAutoRestart` undefined.

- [ ] **Step 3: Implement**

In the `model` struct, directly under the existing `restart bool` field:

```go
	// launchBin is the on-disk binary this process started as; each tick
	// compares it against the file so a rebuilt head re-execs itself — the
	// R key's job, without waiting for a human to remember. launchBinOK
	// false (stamping failed) disables the feature for this process.
	launchBin   binStamp
	launchBinOK bool
```

In `newModel`, after the `m := model{...}` literal (with the other post-literal field setup):

```go
	m.launchBin, m.launchBinOK = launchBinStamp()
```

New method (next to the tick handling or near `restartArgv`'s callers — put it in `tui.go`):

```go
// shouldAutoRestart reports whether this tick may re-exec into a rebuilt
// binary. Only from a quiet head: a teardown in flight must not be disarmed
// by a restart it didn't ask for (teardown state is deliberately not
// persisted — see the teardown fields).
func (m *model) shouldAutoRestart(now time.Time) bool {
	return m.teardown == teardownIdle && m.launchBinOK && binChanged(m.launchBin, now)
}
```

In `Update`'s `case tickMsg:`, immediately after `now := time.Time(msg)`:

```go
		if m.shouldAutoRestart(now) {
			m.restart = true
			return m, tea.Quit
		}
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/claudemux-head/ -run 'TestShouldAutoRestart|TestRestart' -v` then `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/tui.go cmd/claudemux-head/restart_test.go
git commit -m "feat(head): auto-restart when the binary on disk is rebuilt"
```

---

### Task 4: Switchboard restart plumbing (R key + auto-restart)

**Files:**
- Modify: `cmd/claudemux-head/switchboardtui.go` (swModel struct, `newSwModel`, `Update`'s `swSnapshotMsg` and key cases, footer text, `runSwitchboard`)
- Test: `cmd/claudemux-head/switchboardtui_test.go` (append)

**Interfaces:**
- Consumes: `launchBinStamp`, `binChanged` (Task 2), `restartSelf` (restart.go, unchanged).
- Produces: `swModel` fields `restart bool`, `launchBin binStamp`, `launchBinOK bool`; method `func (m *swModel) shouldAutoRestart(now time.Time) bool`.

- [ ] **Step 1: Write the failing tests** (append to `switchboardtui_test.go`)

```go
func TestSwitchboardRestartKey(t *testing.T) {
	m := newSwModel("%1")
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	sm := got.(swModel)
	if !sm.restart {
		t.Error("R must request a restart")
	}
	if cmd == nil {
		t.Error("R must quit the program")
	}
}

func TestSwitchboardRestartKeyStaysLiteralWhileCreating(t *testing.T) {
	m := newSwModel("%1")
	m.creating = true
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	sm := got.(swModel)
	if sm.restart {
		t.Error("R inside the create prompt is input, not a restart")
	}
	if sm.createInput != "R" {
		t.Errorf("createInput = %q, want R", sm.createInput)
	}
}

func TestSwitchboardShouldAutoRestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp, _ := binStampOf(p)
	m := newSwModel("%1")
	m.launchBin, m.launchBinOK = stamp, true
	now := time.Now()
	if m.shouldAutoRestart(now) {
		t.Error("unchanged binary must not restart")
	}
	if err := os.WriteFile(p, []byte("v2-longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-time.Minute)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if !m.shouldAutoRestart(now) {
		t.Error("replaced binary must restart a parked lobby")
	}
	for _, tweak := range []func(*swModel){
		func(m *swModel) { m.standby = true },
		func(m *swModel) { m.creating = true },
		func(m *swModel) { m.createBusy = true },
		func(m *swModel) { m.cond.phase = swEscorting },
		func(m *swModel) { m.cond.phase = swPaused },
	} {
		mm := newSwModel("%1")
		mm.launchBin, mm.launchBinOK = stamp, true
		tweak(&mm)
		if mm.shouldAutoRestart(now) {
			t.Errorf("non-quiescent lobby must not restart (%+v)", mm)
		}
	}
}
```

Ensure the test file imports `"os"`, `"path/filepath"`, `"time"`, and `tea "github.com/charmbracelet/bubbletea"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run TestSwitchboard -v`
Expected: FAIL to compile — `restart`/`shouldAutoRestart` undefined on swModel.

- [ ] **Step 3: Implement**

swModel struct additions (under `createErr string`):

```go
	// restart records that the user (R) or the binary watcher asked for a
	// re-exec rather than a quit; runSwitchboard checks it after Run
	// returns, mirroring the session head's flow in main().
	restart bool
	// launchBin mirrors the session head's: the on-disk binary at launch,
	// re-stat'd each poll so a rebuilt lobby re-execs itself. This matters
	// MORE here than in the head — the lobby hosts the conductor, and a
	// stale conductor silently doesn't know newer published states.
	launchBin   binStamp
	launchBinOK bool
```

`newSwModel` becomes:

```go
func newSwModel(selfPane string) swModel {
	m := swModel{selfPane: selfPane, cond: newConductor(), rateLimitsPath: defaultRateLimitsPath()}
	m.launchBin, m.launchBinOK = launchBinStamp()
	return m
}
```

New method:

```go
// shouldAutoRestart reports whether this poll may re-exec into a rebuilt
// binary. Only from a quiescent lobby: standby and the create prompt are
// in-memory and would not survive the re-exec, and escorting/paused hold
// snoozes and an escortee whose loss would un-skip sessions the user just
// walked away from. swParked is the lobby's resting state, so waiting for
// it delays the upgrade by seconds, not sessions.
func (m *swModel) shouldAutoRestart(now time.Time) bool {
	return !m.standby && !m.creating && !m.createBusy &&
		m.cond.phase == swParked &&
		m.launchBinOK && binChanged(m.launchBin, now)
}
```

In `Update`, `case swSnapshotMsg:` — after `m.snap = msg.snap` (and the `m.sel` clamping), before the conductor step block:

```go
		if m.shouldAutoRestart(time.Now()) {
			m.restart = true
			return m, tea.Quit
		}
```

In the key switch (same level as `case "q", "ctrl+c":`):

```go
		case "R":
			// Same contract as the session head's R: quit, and let
			// runSwitchboard re-exec the binary from disk.
			m.restart = true
			return m, tea.Quit
```

Footer (line ~718): change to

```go
	footerText := "space conduct/standby · j/k select · enter jump · esc back · n new · R restart · q quit"
```

`runSwitchboard` becomes:

```go
func runSwitchboard(stderr io.Writer) int {
	selfPane := os.Getenv("TMUX_PANE")
	if selfPane == "" {
		fmt.Fprintln(stderr, "claudemux-head switchboard must run inside tmux (start it with `claudemux switch`)")
		return 1
	}
	p := tea.NewProgram(newSwModel(selfPane), tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	// Same re-exec-after-Run contract as main()'s session-head path: the
	// terminal is restored before the replacement starts, and a failed
	// exec leaves the pane open with the reason visible.
	if fm, ok := final.(swModel); ok && fm.restart {
		restartSelf(stderr)
		return 1
	}
	return 0
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/claudemux-head/ -run TestSwitchboard -v` then `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/switchboardtui.go cmd/claudemux-head/switchboardtui_test.go
git commit -m "feat(switchboard): R key and auto-restart on a rebuilt binary"
```

---

### Task 5: Conductor resumes when a paused session is handed back

**Files:**
- Modify: `cmd/claudemux-head/swconductor.go` (conductor struct, `step`'s `swPaused` case and paused-exit, `statusLine`)
- Test: `cmd/claudemux-head/swconductor_test.go` (append; update the statusLine assertion if one exists for the paused string)

**Interfaces:**
- Consumes: existing `swSnapshot`, `isWaiting`, `waitingQueue`, test helpers `snapAt`/`waiting`/`busy`.
- Produces: conductor fields `pausedCur string`, `pausedCurWaiting bool`, `pausedHandedBack bool`; method `func (c *conductor) clearPaused()`. Behavior: in swPaused, a waiting→not-waiting transition of the session the client sits in marks it conductible; from then on, any non-empty queue dispatches (phase→swEscorting). Moving to a different session resets observation; reaching the lobby clears it (parked).

- [ ] **Step 1: Write the failing tests** (append to `swconductor_test.go`)

```go
// pauseAt drives a fresh conductor into swPaused at session name: one step
// parked at the lobby is skipped — the client simply appears off-lobby.
func pauseAt(c *conductor, name string, sessions ...swSession) {
	c.step(snapAt(name, sessions...), time.Unix(1_754_700_000, 0))
}

func TestPausedWatchingBusySessionNeverYanked(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", busy("b"))
	// b was never waiting under the user; a waiting session elsewhere
	// must not move them.
	for i := 0; i < 3; i++ {
		if _, ok := c.step(snapAt("b", busy("b"), waiting("a", 100)), now.Add(time.Duration(i)*time.Second)); ok {
			t.Fatal("watching a busy session must never dispatch")
		}
	}
	if c.phase != swPaused {
		t.Errorf("phase = %v, want paused", c.phase)
	}
}

func TestPausedHandBackDispatchesToWaiting(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", waiting("b", 50))
	// Observe b waiting under the user...
	if _, ok := c.step(snapAt("b", waiting("b", 50), waiting("a", 100)), now); ok {
		t.Fatal("attending a waiting session must not dispatch")
	}
	// ...then the user hands it back: conduct to the queue head.
	act, ok := c.step(snapAt("b", busy("b"), waiting("a", 100)), now.Add(time.Second))
	if !ok || act.Target != "a" {
		t.Fatalf("hand-back must dispatch to a, got %+v ok=%v", act, ok)
	}
	if c.phase != swEscorting || c.escortee != "a" {
		t.Errorf("phase=%v escortee=%q", c.phase, c.escortee)
	}
}

func TestPausedHandBackStickyUntilQueueFills(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", waiting("b", 50))
	c.step(snapAt("b", waiting("b", 50)), now)
	// Hand-back with nothing waiting: stay put...
	if _, ok := c.step(snapAt("b", busy("b")), now.Add(time.Second)); ok {
		t.Fatal("empty queue: nothing to dispatch to")
	}
	// ...but the hand-back is remembered; a session that starts waiting
	// minutes later still collects the user.
	act, ok := c.step(snapAt("b", busy("b"), waiting("a", 900)), now.Add(3*time.Minute))
	if !ok || act.Target != "a" {
		t.Fatalf("late waiter must be dispatched, got %+v ok=%v", act, ok)
	}
}

func TestPausedMovingAgainResetsHandBack(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", waiting("b", 50))
	c.step(snapAt("b", waiting("b", 50)), now)
	c.step(snapAt("b", busy("b")), now.Add(time.Second)) // handed back
	// The user jumps to c2 (busy) themselves: observation restarts there.
	c.step(snapAt("c2", busy("b"), busy("c2")), now.Add(2*time.Second))
	if _, ok := c.step(snapAt("c2", busy("b"), busy("c2"), waiting("a", 900)), now.Add(3*time.Second)); ok {
		t.Fatal("a fresh self-navigation must clear the hand-back")
	}
}

func TestPausedLobbyReturnClearsHandBack(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	pauseAt(&c, "b", waiting("b", 50))
	c.step(snapAt("b", waiting("b", 50)), now)
	c.step(snapAt("b", busy("b")), now.Add(time.Second)) // handed back
	// Return to the lobby: parked, observation cleared.
	c.step(snapAt("switchboard", busy("b")), now.Add(2*time.Second))
	if c.phase != swParked {
		t.Fatalf("phase = %v, want parked", c.phase)
	}
	// Jump back into b (busy): the old hand-back must not linger.
	c.step(snapAt("b", busy("b")), now.Add(3*time.Second))
	if _, ok := c.step(snapAt("b", busy("b"), waiting("a", 900)), now.Add(4*time.Second)); ok {
		t.Fatal("stale hand-back from a previous pause must not dispatch")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run TestPaused -v`
Expected: `TestPausedWatchingBusySessionNeverYanked` and `TestPausedMovingAgainResetsHandBack` and `TestPausedLobbyReturnClearsHandBack` PASS trivially (current code never dispatches from paused); `TestPausedHandBackDispatchesToWaiting` and `TestPausedHandBackStickyUntilQueueFills` FAIL — that failing pair is the feature.

- [ ] **Step 3: Implement**

conductor struct additions (under `snoozed`):

```go
	// Paused-session observation. The user navigated somewhere themselves;
	// swPaused's contract is "never fight the user" — but Michael's actual
	// signal for "done here" is handing the session back to Claude, not
	// walking to the lobby. pausedCur/pausedCurWaiting track the session
	// under the client and whether it was waiting on the last tick;
	// pausedHandedBack latches once that same session transitions
	// waiting → not-waiting under them. From then on any waiting session
	// collects the user (now, or whenever one appears). Latched rather
	// than edge-only: the next waiter may fire minutes after the
	// hand-back. Jumping into an already-busy session never latches, so
	// "go watch a busy session" stays possible.
	pausedCur        string
	pausedCurWaiting bool
	pausedHandedBack bool
```

New method:

```go
// clearPaused forgets the paused-session observation; called on every path
// that leaves swPaused so a later pause at the same session cannot inherit a
// stale hand-back.
func (c *conductor) clearPaused() {
	c.pausedCur = ""
	c.pausedCurWaiting = false
	c.pausedHandedBack = false
}
```

Replace the `case swPaused:` block in `step`:

```go
	case swPaused:
		if cur == s.Lobby {
			c.phase = swParked
			c.clearPaused()
			break
		}
		sess, ok := s.session(cur)
		curWaiting := ok && isWaiting(sess.State)
		if cur != c.pausedCur {
			// First look at this spot (fresh pause, or the user moved
			// again): observation restarts, hand-back forgotten.
			c.pausedCur, c.pausedCurWaiting, c.pausedHandedBack = cur, curWaiting, false
			break
		}
		if c.pausedCurWaiting && !curWaiting {
			c.pausedHandedBack = true
		}
		c.pausedCurWaiting = curWaiting
		if c.pausedHandedBack && !curWaiting && len(queue) > 0 {
			c.clearPaused()
			c.phase = swEscorting
			c.escortee = queue[0].Name
			return swAction{Client: c.client, Target: c.escortee}, true
		}
```

(`break` inside a Go switch case exits the switch; the function's final `return swAction{}, false` still runs — same shape the existing cases rely on.)

Update `statusLine`'s paused string to match the new contract:

```go
	case swPaused:
		return "paused — you navigated away; finish there or return here to resume"
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/claudemux-head/ -run 'TestPaused|TestConductor|TestWaitingQueue|TestStatusLine' -v` then `go test ./...`
Expected: PASS. If a pre-existing test asserts the old paused status string ("return here to resume" exact match), update its expectation to the new string.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/swconductor.go cmd/claudemux-head/swconductor_test.go
git commit -m "feat(conductor): resume conducting when a paused session is handed back"
```

---

### Task 6: Full verification and install

**Files:**
- No new files; runs verification and installs the binary.

- [ ] **Step 1: Full suite + vet**

Run: `go test ./... && go vet ./...`
Expected: PASS, no vet findings.

- [ ] **Step 2: Install**

Run: `go install ./cmd/claudemux-head`
Expected: exit 0; `~/go/bin/claudemux-head` mtime updates. (Running heads pick this up themselves only once they're on this build — this install is the last one that needs manual restarts.)

- [ ] **Step 3: Report**

No commit. Report to the main session: suite results, and the one-time manual step that remains — restart the switchboard (`tmux kill-session -t switchboard; claudemux switch`) and press `R` in old session-head panes.
