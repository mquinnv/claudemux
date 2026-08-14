# Restart-All-Heads Key Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One switchboard keystroke (`ctrl+r`) restarts every session head AND the switchboard itself, so the whole fleet picks up a rebuilt binary at once.

**Architecture:** The lobby already knows every session's panes from its poll; record each session's head pane in the snapshot, then have `ctrl+r` type the existing `R` restart key into every head pane via `tmux send-keys` (old binaries have handled `R` for months, so this reaches stale heads too), and finally re-exec the switchboard itself through the `restart` flag added earlier today — self last, so the sends complete before this process quits.

**Tech Stack:** Go, Bubble Tea, tmux. Tests with stdlib `testing`, following existing switchboard test style.

**Spec:** Inline. Michael (2026-08-14): "can we add a restart-all-heads key to the switchboard? and also a self-restart?" — self-restart (`R`) already landed in commit a2ec5a8; this plan adds the fleet-wide key.

## Global Constraints

- `R` stays self-only; the fleet key is `ctrl+r`.
- The switchboard restarts itself LAST — only after every send-keys has completed (sends run synchronously inside one tea.Cmd that then returns a message; the message handler quits).
- Send `R` ONLY to panes whose `pane_current_command` is `claudemux-head` — typing `R` into a claude pane would inject text into the user's prompt.
- `ctrl+r` while the create prompt is open must stay inert (the existing `if m.creating` block already returns before the key switch — do not special-case it).
- Run `go test ./...` from the worktree root; everything stays green.
- Comments in the repo's discursive style: constraints the code can't show.

---

### Task 1: Fleet-restart key

**Files:**
- Modify: `cmd/claudemux-head/switchboard.go` (swSession struct, buildSwSnapshot)
- Modify: `cmd/claudemux-head/switchboardtui.go` (msg type + cmd, Update key case + msg case, footer)
- Test: `cmd/claudemux-head/switchboard_test.go` (or wherever buildSwSnapshot's tests live — append beside them), `cmd/claudemux-head/switchboardtui_test.go`

**Interfaces:**
- Consumes: `swSession`, `buildSwSnapshot` (switchboard.go); `swModel.restart` + quit flow and `swHeadCommand` const (already on the branch).
- Produces: `swSession.HeadPane string`; `swFleetRestartMsg struct{}`; `swFleetRestartArgs(sessions []swSession) [][]string`; `swRestartFleetCmd(sessions []swSession) tea.Cmd`.

- [ ] **Step 1: Write the failing tests**

Append to the file holding `buildSwSnapshot` tests (find it with `grep -rln buildSwSnapshot --include='*_test.go' cmd/claudemux-head/`); mirror its existing input-string style for a case asserting `HeadPane` is populated from a paneOut line whose command is `claudemux-head` and empty for a session with no head pane. Then append to `switchboardtui_test.go`:

```go
func TestFleetRestartArgs(t *testing.T) {
	sessions := []swSession{
		{Name: "a", HeadPane: "%10"},
		{Name: "b"}, // no head pane recorded: must be skipped, not sent to pane ""
		{Name: "c", HeadPane: "%12"},
	}
	got := swFleetRestartArgs(sessions)
	want := [][]string{
		{"send-keys", "-t", "%10", "R"},
		{"send-keys", "-t", "%12", "R"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d argvs, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if strings.Join(got[i], " ") != strings.Join(want[i], " ") {
			t.Errorf("argv[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestSwitchboardFleetRestartKey(t *testing.T) {
	m := newSwModel("%1")
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	sm := got.(swModel)
	if sm.restart {
		t.Error("ctrl+r must not restart self before the sends complete")
	}
	if cmd == nil {
		t.Fatal("ctrl+r must dispatch the fleet-restart cmd")
	}
	got2, cmd2 := sm.Update(swFleetRestartMsg{})
	sm2 := got2.(swModel)
	if !sm2.restart {
		t.Error("fleet-restart completion must request self-restart")
	}
	if cmd2 == nil {
		t.Error("fleet-restart completion must quit the program")
	}
}

func TestSwitchboardFleetRestartKeyInertWhileCreating(t *testing.T) {
	m := newSwModel("%1")
	m.creating = true
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	sm := got.(swModel)
	if sm.restart {
		t.Error("ctrl+r inside the create prompt must be inert")
	}
	if sm.createInput != "" {
		t.Errorf("ctrl+r must not type into the create prompt, got %q", sm.createInput)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestFleetRestart|TestSwitchboardFleetRestart|<name of the new snapshot test>' -v`
Expected: FAIL to compile — `HeadPane`/`swFleetRestartArgs`/`swFleetRestartMsg` undefined. (`TestSwitchboardFleetRestartKeyInertWhileCreating` would pass trivially once it compiles; it exists as a regression guard.)

- [ ] **Step 3: Implement**

`switchboard.go` — add to `swSession` (after `ClaudePane`, with a comment in the struct's style):

```go
	// HeadPane is the session's claudemux-head pane. The fleet-restart key
	// types `R` into it — and must never type into the claude pane, where
	// a stray R would land in the user's prompt.
	HeadPane string
```

In `buildSwSnapshot`'s paneOut loop, inside the `if f[2] == swHeadCommand` branch, record the pane id first-wins alongside `heads`/`topics` (add a `headPanes := map[string]string{}` beside the other maps):

```go
		if f[2] == swHeadCommand {
			heads[f[0]] = true
			topics[f[0]] = f[3]
			if _, ok := headPanes[f[0]]; !ok {
				headPanes[f[0]] = f[1]
			}
		}
```

And in the sessions loop, after `sess.ClaudePane` is set: `sess.HeadPane = headPanes[sess.Name]`.

`switchboardtui.go` — new msg, args builder, and cmd (near swSwitchCmd, matching its timeout style):

```go
// swFleetRestartMsg reports that every session head has been sent the R
// restart key; the lobby restarts itself only on receipt, so the sends are
// complete before this process quits.
type swFleetRestartMsg struct{}

// swFleetRestartArgs builds one tmux send-keys argv per session head pane.
// Sessions with no recorded head pane are skipped — send-keys to an empty
// target would land in whatever pane tmux resolves, which could be a claude
// prompt.
func swFleetRestartArgs(sessions []swSession) [][]string {
	var argvs [][]string
	for _, sess := range sessions {
		if sess.HeadPane == "" {
			continue
		}
		argvs = append(argvs, []string{"send-keys", "-t", sess.HeadPane, "R"})
	}
	return argvs
}

// swRestartFleetCmd types the R restart key into every session head, then
// reports completion. R has been the head's restart key for months, so this
// reaches heads running binaries too old to auto-restart themselves — which
// is exactly when a fleet restart is wanted. Sends run sequentially with the
// same per-call deadline as every other tmux shell-out; a send that fails
// (dead pane, wedged tmux) is skipped rather than aborting the sweep.
func swRestartFleetCmd(sessions []swSession) tea.Cmd {
	argvs := swFleetRestartArgs(sessions)
	return func() tea.Msg {
		for _, args := range argvs {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = exec.CommandContext(ctx, "tmux", args...).Run()
			cancel()
		}
		return swFleetRestartMsg{}
	}
}
```

In `Update`, a new top-level msg case (beside the other msg cases):

```go
	case swFleetRestartMsg:
		// Self last: every head has been told to restart; now pick up the
		// new binary here too, via the same after-Run re-exec as R.
		m.restart = true
		return m, tea.Quit
```

In the key switch, beside `case "R":`:

```go
		case "ctrl+r":
			// Restart the whole fleet: every session head, then self.
			return m, swRestartFleetCmd(m.snap.Sessions)
```

Footer becomes:

```go
	footerText := "space conduct/standby · j/k select · enter jump · esc back · n new · R restart · ^R restart all · q quit"
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/claudemux-head/ -run 'TestFleetRestart|TestSwitchboardFleetRestart|<snapshot test>' -v` then `go test ./...` and `go vet ./...`
Expected: PASS, no vet findings.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/switchboard.go cmd/claudemux-head/switchboardtui.go cmd/claudemux-head/switchboard_test.go cmd/claudemux-head/switchboardtui_test.go
git commit -m "feat(switchboard): ctrl+r restarts every session head, then self"
```

(Adjust the `git add` list if the snapshot test lives in a differently named file.)

---

### Task 2: Banner popup cursor artifact

Reported by Michael (2026-08-14): "the train dialog has an extra line at the bottom with what looks like a cursor on it."

Root cause: `runBanner` (cmd/claudemux-head/banner.go) prints every card line with `fmt.Fprintln`, but `swBannerPopupArgs` sizes the popup to `len(lines) + 2` — borders included, so the inner viewport is exactly `len(lines)` rows. The final line's trailing newline moves the cursor to a row that doesn't fit, scrolling the card up one row and leaving a blank bottom line with the visible cursor parked on it (the top smoke line scrolls off, which is easy to miss because it is sparse).

**Files:**
- Modify: `cmd/claudemux-head/banner.go` (`runBanner` only)
- Test: `cmd/claudemux-head/banner_test.go`

**Interfaces:**
- Consumes: `renderBannerCard`, `waitBannerDismiss` (unchanged).
- Produces: no new names; `runBanner`'s stdout contract changes to newline-JOINED lines (no trailing newline) wrapped in cursor-hide/show sequences.

- [ ] **Step 1: Write the failing test** (append to banner_test.go)

```go
func TestRunBannerOutputFitsPopupViewport(t *testing.T) {
	var out strings.Builder
	if rc := runBanner([]string{"api"}, &out, io.Discard); rc != 0 {
		t.Fatalf("rc = %d", rc)
	}
	s := out.String()
	if !strings.HasPrefix(s, "\x1b[?25l") {
		t.Error("banner must hide the cursor for the popup's lifetime")
	}
	if !strings.HasSuffix(s, "\x1b[?25h") {
		t.Error("banner must restore the cursor before exiting")
	}
	body := strings.TrimPrefix(strings.TrimSuffix(s, "\x1b[?25h"), "\x1b[?25l")
	if strings.HasSuffix(body, "\n") {
		t.Error("a trailing newline scrolls the card and parks the cursor on a blank bottom row")
	}
	if got, want := strings.Count(body, "\n"), len(renderBannerCard("api"))-1; got != want {
		t.Errorf("body has %d newlines, want %d (N lines joined, not terminated)", got, want)
	}
}
```

Add `"io"` and `"strings"` to the test file's imports if absent. Note: `runBanner` calls `waitBannerDismiss(os.Stdin, swBannerHold)`, which in a test (stdin not a tty, or no input) falls back to a sleep of up to 2s — acceptable for one test. If stdin IS a terminal in the test environment and MakeRaw succeeds, the read blocks up to the deadline; either way the test completes within ~2s.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run TestRunBannerOutputFitsPopupViewport -v`
Expected: FAIL — no cursor-hide prefix, and the body ends with a newline.

- [ ] **Step 3: Implement** — replace `runBanner`'s print loop:

```go
	// The popup's inner viewport is exactly len(lines) rows
	// (swBannerPopupArgs asks for len+2, borders included), so the card is
	// written as newline-JOINED lines: a final newline would move the
	// cursor to a row that doesn't fit, scrolling the card up and leaving
	// a blank bottom line. The cursor itself is hidden for the popup's
	// lifetime — there is nothing to type here — and restored on the way
	// out for pty hygiene, though the popup dies with this process anyway.
	fmt.Fprint(stdout, "\x1b[?25l")
	fmt.Fprint(stdout, strings.Join(renderBannerCard(args[0]), "\n"))
	waitBannerDismiss(os.Stdin, swBannerHold)
	fmt.Fprint(stdout, "\x1b[?25h")
	return 0
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/claudemux-head/ -run 'TestRunBanner|TestRenderBanner|TestSwBanner' -v` then `go test ./...` and `go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/banner.go cmd/claudemux-head/banner_test.go
git commit -m "fix(banner): keep the card inside the popup viewport, hide the cursor"
```
