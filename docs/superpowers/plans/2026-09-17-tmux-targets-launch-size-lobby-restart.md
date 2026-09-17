# tmux Targets, Launch Size, and Lobby Restart Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop a project whose name prefixes the lobby's window name from having the launcher's session setup land on the lobby instead, size new sessions to the client that will display them, and let the lobby pick up a rebuilt binary without waiting for a moment it may never reach.

**Architecture:** Three independent changes. (1) `bin/claudemux` writes every session-scoped tmux target in the unambiguous `NAME:` form, guarded by a Go test that reads the script. (2) The session's initial size comes from the tmux client that will receive it, falling back to the tty and then to tmux's own default. (3) The lobby's conductor state (phase, escortee, snoozes, paused observation) is written to a one-shot handoff file immediately before `restartSelf` and consumed once at startup, which lets `shouldAutoRestart` drop the `swParked`/empty-snooze conditions that were keeping the lobby on an eight-day-old binary.

**Tech Stack:** bash (`bin/claudemux`, `#!/usr/bin/env bash`, `set -euo pipefail`), Go 1.2x, Bubble Tea, tmux 3.7c, Go `testing`.

**Spec:** `docs/superpowers/specs/2026-09-17-tmux-target-resolution-and-lobby-restart.md` — read it first; it carries the measurements each task is arguing from.

## Global Constraints

- Work only in this worktree: `/Users/michael/Projects/claudemux/.claude/worktrees/fix-tmux-targets-and-lobby-restart`. Never touch the main checkout at `/Users/michael/Projects/claudemux`. Never `git stash`.
- `go test ./...` must pass after every task. `gofmt -l cmd/` must print nothing before every commit.
- `bash -n bin/claudemux` (syntax check) must pass after every edit to the launcher.
- tmux experiments must use throwaway session names prefixed `__plan_` and must kill them afterwards. **Never** run `resize-window` against a session in the real fleet: it flips that window to `window-size manual` and strands it at the probe size. Build your own harness sessions instead.
- The real fleet contains a session literally named `claudemux` and a lobby named `switchboard`. Do not target, resize, restart, or kill either one.
- Commit after each task, with the trailers:
  ```
  Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_013pXoLGcTKdDb7BdwDPTUpf
  ```

## File Structure

| File | Responsibility | Task |
|---|---|---|
| `bin/claudemux` | session-scoped targets take the `NAME:` form | 1 |
| `cmd/claudemux-head/launchertargets_test.go` (new) | guard test: the launcher never regrows a bare session target | 1 |
| `cmd/claudemux-head/switchboardtui.go` | `swSwitchTarget` helper; `shouldAutoRestart` gate; handoff read/write wiring | 2, 4 |
| `cmd/claudemux-head/switchboardtui_test.go` | `swSwitchTarget` and `shouldAutoRestart` tests | 2, 4 |
| `bin/claudemux` | `session_size` helper feeding `new-session -x/-y` | 3 |
| `cmd/claudemux-head/conducthandoff.go` (new) | one-shot conductor state handoff across `restartSelf` | 4 |
| `cmd/claudemux-head/conducthandoff_test.go` (new) | round-trip, expiry, one-shot-delete tests | 4 |

---

### Task 1: Session-scoped tmux targets in the launcher

**Files:**
- Modify: `bin/claudemux` — lines 217, 219, 248, 266, 952, 964, 965
- Create: `cmd/claudemux-head/launchertargets_test.go`

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: nothing other tasks consume. The guard test reads `../../bin/claudemux` relative to the Go package directory.

- [ ] **Step 1: Confirm the hazard and the fix form against live tmux**

Build a harness that reproduces the caller-session window-prefix match, with invented names so nothing in the real fleet is touched:

```bash
tmux new-session -d -s __plan_lobby__ 'sleep 120'
tmux rename-window -t __plan_lobby__ probe-head        # "probe" is a prefix of "probe-head"
tmux new-session -d -s probe 'sleep 120'
lp=$(tmux list-panes -t __plan_lobby__: -F '#{pane_id}')

# bare target, evaluated as a child of the lobby pane
TMUX_PANE=$lp tmux set -t probe @plan_probe 1
echo "bare -> probe:          $(tmux show-options -t probe: | grep -c plan_probe)"
echo "bare -> __plan_lobby__: $(tmux show-options -t __plan_lobby__: | grep -c plan_probe)"
tmux set -u -t probe: @plan_probe; tmux set -u -t __plan_lobby__: @plan_probe

# colon form
TMUX_PANE=$lp tmux set -t probe: @plan_probe 1
echo "colon -> probe:          $(tmux show-options -t probe: | grep -c plan_probe)"
echo "colon -> __plan_lobby__: $(tmux show-options -t __plan_lobby__: | grep -c plan_probe)"
tmux set -u -t probe: @plan_probe; tmux set -u -t __plan_lobby__: @plan_probe
```

Expected: `bare -> probe: 0` / `bare -> __plan_lobby__: 1`, and `colon -> probe: 1` / `colon -> __plan_lobby__: 0`.

Also check the one form whose acceptance is not yet established — `attach-session` with a trailing colon:

```bash
tmux attach -t 'probe:' \; detach-client 2>&1 | head -2   # expect no "can't find" error
```

If `attach -t "probe:"` errors, use `-t "=probe"` for the attach call site only (it is a target-session, where `=` is exact-match) and note it in the commit message. Keep the harness alive for Step 4; tear it down in Step 6.

- [ ] **Step 2: Write the failing guard test**

Create `cmd/claudemux-head/launchertargets_test.go`:

```go
package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The launcher is a sibling script, not Go, but this is the only test runner
// the repo has — and the bug this guards is worth a CI gate rather than a
// user noticing a pane is the wrong height.
//
// tmux resolves a bare `-t NAME` by trying a window-name PREFIX match in the
// CALLER's session before it tries session names. bin/claudemux runs as a
// child of the lobby pane whenever the lobby's `n` key creates a session, and
// the lobby's window is called "claudemux-head" — so a project directory named
// "claudemux" matched the lobby's window and every bare-target command in the
// launcher's tail configured the LOBBY instead of the new session. A target
// written "NAME:" can only be a session. See
// docs/superpowers/specs/2026-09-17-tmux-target-resolution-and-lobby-restart.md.
const launcherPath = "../../bin/claudemux"

// bareSessionTarget matches `-t "$session_name"` and `-t "${session_name}"`
// without the disambiguating colon. $session_name is never a pane id in this
// script, so any bare use of it is a bug.
var bareSessionTarget = regexp.MustCompile(`-t "\$\{?session_name\}?"`)

func TestLauncherSessionNameTargetsAreUnambiguous(t *testing.T) {
	src, err := os.ReadFile(launcherPath)
	if err != nil {
		t.Fatalf("read launcher: %v", err)
	}
	for i, line := range strings.Split(string(src), "\n") {
		if bareSessionTarget.MatchString(line) {
			t.Errorf("%s:%d: bare session target, use \"$session_name:\": %s",
				launcherPath, i+1, strings.TrimSpace(line))
		}
	}
}

// attach() and apply_status_color() take a SESSION name as $1, so inside those
// two functions a `-t "$1"` is a session target and needs the colon too. Every
// other `-t "$1"` in the script is a pane id (run_in_pane, pane_cmd,
// run_shell_in_pane) and is unambiguous as written.
func TestLauncherDollarOneSessionTargets(t *testing.T) {
	src, err := os.ReadFile(launcherPath)
	if err != nil {
		t.Fatalf("read launcher: %v", err)
	}
	for _, fn := range []string{"attach", "apply_status_color"} {
		body, ok := shellFuncBody(string(src), fn)
		if !ok {
			t.Fatalf("function %s() not found in %s", fn, launcherPath)
		}
		for _, line := range strings.Split(body, "\n") {
			if !strings.Contains(line, "tmux ") {
				continue
			}
			if strings.Contains(line, `-t "$1"`) {
				t.Errorf("%s(): session target written bare, use \"$1:\": %s",
					fn, strings.TrimSpace(line))
			}
		}
	}
}

// shellFuncBody returns the text between `name() {` and the first line that is
// exactly "}" — the script writes every function that way.
func shellFuncBody(src, name string) (string, bool) {
	lines := strings.Split(src, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, name+"() {") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", false
	}
	for i := start; i < len(lines); i++ {
		if lines[i] == "}" {
			return strings.Join(lines[start:i], "\n"), true
		}
	}
	return "", false
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run 'TestLauncher' -v`
Expected: FAIL — `TestLauncherSessionNameTargetsAreUnambiguous` reports lines 952, 964, 965; `TestLauncherDollarOneSessionTargets` reports the `status-left-length`, `status-style` and `pane-active-border-style` lines.

- [ ] **Step 4: Fix the seven call sites**

In `bin/claudemux`, add the colon to each session target. `apply_status_color` (~line 217):

```bash
  tmux set -t "$1:" status-style "bg=#${hex},fg=${fg}"
  # pane-active-border-style is a window option; sessions here are single-window
  tmux set -w -t "$1:" pane-active-border-style "fg=#${hex}"
```

`attach` (~line 248 and ~line 266):

```bash
  tmux set -t "$1:" status-left-length 32
```
```bash
  tmux attach -t "$1:"
```

`create_session` (~line 952 and ~964-965):

```bash
  tmux set-hook -t "$session_name:" window-resized "resize-pane -t $head_pane -y $head_rows"
```
```bash
  tmux set -t "$session_name:" set-titles on
  tmux set -t "$session_name:" set-titles-string \
    "$(session_title_name "$proj_name" "$session_name" "$clone_suffix") · #W"
```

Extend the existing comment above the `set-hook` line with the reason the colon is load-bearing (keep the existing text; append):

```bash
  # The "$session_name:" form is required, not stylistic: tmux resolves a bare
  # `-t NAME` by window-name PREFIX in the CALLER's session first, and the
  # lobby — which runs this script for its `n` key — has a window called
  # "claudemux-head". A project named "claudemux" therefore set this hook on
  # the LOBBY, leaving the new session with no re-pin and a head pane scaled to
  # whatever the attach did to it. Same reason for every other session target
  # in this file.
```

Then verify the script still parses:

Run: `bash -n bin/claudemux`
Expected: no output.

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./cmd/claudemux-head/ -run 'TestLauncher' -v`
Expected: PASS (both tests).

- [ ] **Step 6: Prove it end-to-end against a real launch, then tear the harness down**

The unit test checks the source text; this checks tmux's behaviour. Launch a session **as a child of a lobby-like pane** whose window name prefixes the project name, using stub binaries so no real claude starts:

```bash
mkdir -p /tmp/__plan_stub
printf '#!/bin/sh\nexec sleep 600\n' > /tmp/__plan_stub/claude
cat > /tmp/__plan_stub/claudemux-head <<'STUB'
#!/bin/sh
case "$1" in
  config|hook|project|onboard) exec /Users/michael/go/bin/claudemux-head "$@" ;;
esac
exec sleep 600
STUB
chmod +x /tmp/__plan_stub/claude /tmp/__plan_stub/claudemux-head

lp=$(tmux list-panes -t __plan_lobby__: -F '#{pane_id}')
name=$(TMUX_PANE=$lp PATH=/tmp/__plan_stub:$PATH bash bin/claudemux -n -d -W -- "$PWD" | tail -1)
echo "created: $name"
tmux show-options -t "$name:" | grep -E 'set-titles|status-left-length'   # expect all three present
tmux show-options -t __plan_lobby__: | grep -cE 'set-titles-string|status-left-length'  # expect 0
```

The session is named after this worktree's directory basename (`fix-tmux-targets-and-lobby-restart`), which does not prefix-match `probe-head`, so to exercise the actual hazard rename the harness lobby's window to a prefix of that name first:

```bash
tmux rename-window -t __plan_lobby__: fix-tmux-targets-and-lobby-restart-head
```

and repeat. Expected after the fix: the options land on the new session and the harness lobby stays clean. Tear everything down:

```bash
tmux kill-session -t "$name:"
tmux kill-session -t __plan_lobby__:
tmux kill-session -t probe:
rm -rf /tmp/__plan_stub
tmux list-sessions -F '#{session_name}'   # confirm only the real fleet remains
```

- [ ] **Step 7: Commit**

```bash
git add bin/claudemux cmd/claudemux-head/launchertargets_test.go docs/superpowers/specs/2026-09-17-tmux-target-resolution-and-lobby-restart.md docs/superpowers/plans/2026-09-17-tmux-targets-launch-size-lobby-restart.md
git commit -m "$(cat <<'EOF'
launcher: target sessions unambiguously, not by bare name

tmux resolves a bare `-t NAME` by window-name prefix in the caller's session
before it tries session names, and the lobby's window is "claudemux-head". A
project directory named "claudemux" therefore prefix-matched it, so every
session target in the launcher's tail -- the window-resized hook that pins the
head pane, set-titles, status-left-length, the status and border styles --
configured the LOBBY instead of the session being created. The new session came
up with no re-pin hook, so its head pane kept whatever height the attach scaled
it to (~11 rows of 51) until the user dragged it back by hand, every time. The
lobby meanwhile wore that project's status colour.

The "NAME:" form can only be a session. A guard test reads the script, because
this is the second time this hazard has been found and fixed one call site at a
time (see swDeferTarget).

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013pXoLGcTKdDb7BdwDPTUpf
EOF
)"
```

---

### Task 2: The conductor's own switch-client target

**Files:**
- Modify: `cmd/claudemux-head/switchboardtui.go` (`swSwitchCmd`, ~line 495; add `swSwitchTarget` beside `swDeferTarget`, ~line 1340)
- Test: `cmd/claudemux-head/switchboardtui_test.go`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces: `func swSwitchTarget(session string) string` — returns the unambiguous session target for a session name (`"claudemux"` → `"claudemux:"`). Used by `swSwitchCmd` and by `swCreateCmd`'s follow-up switch.

- [ ] **Step 1: Establish whether `-c` re-bases target resolution**

This decides whether the change is a real fix or a belt-and-braces one, and the answer belongs in the commit message either way. Using the harness from Task 1 Step 1 (recreate it if torn down), attach a client to the lobby-like session and ask tmux to switch it by bare name:

```bash
tmux new-session -d -s __plan_lobby__ 'sleep 120'
tmux rename-window -t __plan_lobby__: probe-head
tmux new-session -d -s probe 'sleep 120'
tmux new-session -d -s __plan_outer__ -x 190 -y 51 'env -u TMUX tmux attach -t __plan_lobby__:'
sleep 1.5
cl=$(tmux list-clients -F '#{client_name} #{client_session}' | grep __plan_lobby__ | awk '{print $1}')
tmux switch-client -c "$cl" -t probe
sleep 0.5
tmux list-clients -F '#{client_name} #{client_session}' | grep "$cl"
```

If the client's session is still `__plan_lobby__`, the escort silently no-ops for such a name and this is a live bug. If it moved to `probe`, `-c` re-bases resolution and the change is defensive. Record which. Tear down: `tmux kill-session -t __plan_outer__: ; tmux kill-session -t __plan_lobby__: ; tmux kill-session -t probe:`.

- [ ] **Step 2: Write the failing test**

Add to `cmd/claudemux-head/switchboardtui_test.go`:

```go
// A session name that prefixes the lobby's window name ("claudemux-head")
// must not be handed to tmux bare — see swDeferTarget's comment and
// docs/superpowers/specs/2026-09-17-tmux-target-resolution-and-lobby-restart.md.
func TestSwSwitchTargetIsSessionScoped(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"claudemux", "claudemux:"},
		{"gh-hud", "gh-hud:"},
		{"switchboard", "switchboard:"},
	} {
		if got := swSwitchTarget(tc.in); got != tc.want {
			t.Errorf("swSwitchTarget(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// An empty session is not a target at all; passing ":" to tmux would resolve to
// the caller's own current session, which is exactly the bug being fixed.
func TestSwSwitchTargetEmpty(t *testing.T) {
	if got := swSwitchTarget(""); got != "" {
		t.Errorf("swSwitchTarget(\"\") = %q, want \"\"", got)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run TestSwSwitchTarget -v`
Expected: FAIL — `undefined: swSwitchTarget`.

- [ ] **Step 4: Implement**

Add beside `swDeferTarget` in `switchboardtui.go`:

```go
// swSwitchTarget is the tmux target the lobby switches a client to. The
// trailing colon is required for the same reason swDeferTarget takes a pane id:
// tmux resolves a bare name by window-name PREFIX in the caller's session
// before session names, and the lobby's window is "claudemux-head" — so a
// session named "claudemux" can resolve to the lobby's own window. Empty in,
// empty out: ":" alone means "the current session", which would be a silent
// no-op escort rather than a visible failure.
func swSwitchTarget(session string) string {
	if session == "" {
		return ""
	}
	return session + ":"
}
```

Use it at the switch site (~line 499):

```go
		if err := exec.CommandContext(ctx, "tmux", "switch-client", "-c", client, "-t", swSwitchTarget(target)).Run(); err != nil || card == nil {
```

Check the other switch in `swCreateCmd`'s follow-up (the `switch-client` issued after the launcher prints the new session name) and any other `switch-client -t <session>` in the file; route each through `swSwitchTarget`. Leave `swSwitchLastCmd` alone — `-l` takes no target.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -run TestSwSwitch -v && go test ./...`
Expected: PASS, whole package green.

- [ ] **Step 6: Commit**

```bash
git add cmd/claudemux-head/switchboardtui.go cmd/claudemux-head/switchboardtui_test.go
git commit -m "$(cat <<'EOF'
lobby: switch clients to a session, never to a bare name

Third call site of the same hazard swDeferTarget documents: a session whose
name prefixes the lobby's window name ("claudemux-head") can resolve to that
window instead of the session. swSwitchTarget puts every escort and every
post-create jump on the unambiguous "NAME:" form.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013pXoLGcTKdDb7BdwDPTUpf
EOF
)"
```

---

### Task 3: Size a new session to the client that will display it

**Files:**
- Modify: `bin/claudemux` — add `session_size()` beside `shell_size_for` (~line 409), change the `new-session` call (~line 878)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: shell function `session_size()` echoing `COLSxROWS`; no Go consumers.

- [ ] **Step 1: Reproduce the wrong size**

```bash
sh -c 'echo "no tty: $(tput cols)x$(tput lines)"'
```

Expected: `no tty: 80x24` — the terminfo default the lobby's launcher has been building every session at. Then confirm the better source answers from a non-tty child of a tmux pane (use the harness lobby, not the real one):

```bash
tmux new-session -d -s __plan_lobby__ -x 190 -y 51 'sleep 120'
tmux new-session -d -s __plan_outer__ -x 190 -y 51 'env -u TMUX tmux attach -t __plan_lobby__:'
sleep 1.5
lp=$(tmux list-panes -t __plan_lobby__: -F '#{pane_id}')
env -u TMUX TMUX_PANE=$lp sh -c 'tmux display-message -p -t "$TMUX_PANE" "#{client_width}x#{client_height}"'
```

Expected: `190x51` (the attached client), not `80x24`.

- [ ] **Step 2: Implement `session_size`**

Add to `bin/claudemux`, above `create_session`:

```bash
# session_size — echo "COLSxROWS" for a new session's initial size.
#
# `tput` was the only source here, and it is wrong in both of the ways this
# launcher is actually run. With no controlling terminal — which is every
# session the lobby's `n` key creates, since the launcher is a child of the
# lobby pane with no tty — tput reports its terminfo DEFAULT, 80x24, so the
# session was built at a quarter of its real size and every `split-window -l`
# in create_session meant a quarter of what it said. Run from inside a tmux
# pane with a tty, tput reports that PANE's size, not the client's.
#
# The size that matters is the client that will display the session, so ask
# tmux for it first. Falls back to tput for a real terminal outside tmux, and
# to tmux's own default when there is no answer at all — never to a garbage
# value, because a bad -x/-y fails new-session outright.
session_size() {
  local size w h
  if [ -n "${TMUX_PANE:-}" ]; then
    size="$(tmux display-message -p -t "$TMUX_PANE" '#{client_width}x#{client_height}' 2>/dev/null || true)"
    case "$size" in
      [1-9]*x[1-9]*) printf '%s\n' "$size"; return ;;
    esac
  fi
  if [ -t 1 ]; then
    w="$(tput cols 2>/dev/null || true)"
    h="$(tput lines 2>/dev/null || true)"
    case "$w" in
      [1-9]*) case "$h" in
        [1-9]*) printf '%sx%s\n' "$w" "$h"; return ;;
      esac ;;
    esac
  fi
  printf '80x24\n'
}
```

- [ ] **Step 3: Use it at the one call site**

Replace the `new-session` line (~878):

```bash
  local sess_size
  sess_size="$(session_size)"
  tmux new-session -d -s "$session_name" -c "$work_dir" -x "${sess_size%x*}" -y "${sess_size#*x}"
```

Declare `sess_size` with the other locals in `create_session` if the surrounding style prefers one `local` block — match what is already there rather than adding a stray declaration mid-function.

Run: `bash -n bin/claudemux`
Expected: no output.

- [ ] **Step 4: Verify each branch of the fallback chain**

```bash
# 1. inside tmux, no tty -> client size
lp=$(tmux list-panes -t __plan_lobby__: -F '#{pane_id}')
env -u TMUX TMUX_PANE=$lp bash -c 'source bin/claudemux 2>/dev/null; true' 2>/dev/null || true
env -u TMUX TMUX_PANE=$lp bash -c 'sed -n "/^session_size()/,/^}/p" bin/claudemux > /tmp/__plan_fn.sh; . /tmp/__plan_fn.sh; session_size'
# expect 190x51

# 2. no tmux, no tty -> default
env -u TMUX -u TMUX_PANE bash -c '. /tmp/__plan_fn.sh; session_size'
# expect 80x24

# 3. a real tty is covered by branch 2's tput path; check it does not crash
env -u TMUX -u TMUX_PANE bash -c '. /tmp/__plan_fn.sh; session_size' < /dev/null
rm -f /tmp/__plan_fn.sh
```

`bin/claudemux` runs `main` at the bottom, so it must not be sourced whole — extracting just the function with `sed` is the reason for the `/tmp/__plan_fn.sh` dance.

- [ ] **Step 5: End-to-end launch check, then tear down**

Re-run Task 1 Step 6's stub launch, from the harness lobby pane, and confirm the new session is built at the client's size rather than 80x24:

```bash
tmux list-windows -t "$name:" -F '#{window_width}x#{window_height}'   # expect 190x51
tmux list-panes -t "$name:" -F '#{pane_id} #{pane_width}x#{pane_height}'  # head pane exactly 5 rows
```

Then kill `$name`, `__plan_lobby__`, `__plan_outer__` and confirm `tmux list-sessions` shows only the real fleet.

- [ ] **Step 6: Commit**

```bash
git add bin/claudemux
git commit -m "$(cat <<'EOF'
launcher: size a new session to the client that will show it

tput was the only source of the new session's -x/-y, and it is wrong in both
ways this script is run. As the lobby's child there is no tty, so tput reported
the terminfo default 80x24 and every session the lobby created was built at a
quarter of its real size -- making every split-window -l in create_session mean
a quarter of what it said. Run from inside a pane with a tty, tput reports that
pane's size rather than the client's.

Ask tmux for the client that will display the session; fall back to tput for a
real terminal outside tmux, and to 80x24 only when nothing can answer.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013pXoLGcTKdDb7BdwDPTUpf
EOF
)"
```

---

### Task 4: Carry conductor state across the re-exec, and open the restart gate

**Files:**
- Create: `cmd/claudemux-head/conducthandoff.go`
- Create: `cmd/claudemux-head/conducthandoff_test.go`
- Modify: `cmd/claudemux-head/switchboardtui.go` (`shouldAutoRestart` ~line 329, `runSwitchboard` ~line 1318 — the handoff is read and written there, never in `newSwModel`)
- Modify: `cmd/claudemux-head/switchboardtui_test.go` — `TestSwitchboardShouldAutoRestart` (~line 915) and `TestSwitchboardShouldAutoRestartLiveSnooze` (~line 957) currently assert the gate this task removes
- Check: `README.md` for any sentence describing the restart gate

**Interfaces:**
- Consumes: `conductor` and `swSnooze` from `swconductor.go` (unexported fields `since`, `at`); `swPhase` constants `swParked`/`swEscorting`/`swPaused`.
- Produces:
  - `const conductHandoffTTL = 30 * time.Second`
  - `func defaultConductHandoffPath() string`
  - `func writeConductHandoff(path string, c conductor, now time.Time) error`
  - `func readConductHandoff(path string, now time.Time) (conductor, bool)` — always deletes the file, returns `ok=false` for missing, unparseable, or expired.

- [ ] **Step 1: Write the failing handoff tests**

Create `cmd/claudemux-head/conducthandoff_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func handoffFixture(now time.Time) conductor {
	return conductor{
		phase:            swEscorting,
		client:           "/dev/ttys013",
		escortee:         "phenix",
		snoozed:          map[string]swSnooze{"ag-admin": {since: now.Add(-time.Hour), at: now.Add(-time.Minute)}},
		pausedCur:        "gh-hud",
		pausedCurWaiting: true,
		pausedHandedBack: true,
	}
}

func TestConductHandoffRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeConductHandoff(path, handoffFixture(now), now); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok := readConductHandoff(path, now.Add(time.Second))
	if !ok {
		t.Fatal("read: ok=false, want true")
	}
	want := handoffFixture(now)
	if got.phase != want.phase || got.escortee != want.escortee {
		t.Errorf("phase/escortee = %v/%q, want %v/%q", got.phase, got.escortee, want.phase, want.escortee)
	}
	sn, hit := got.snoozed["ag-admin"]
	if !hit {
		t.Fatalf("snoozed lost: %#v", got.snoozed)
	}
	if !sn.since.Equal(want.snoozed["ag-admin"].since) || !sn.at.Equal(want.snoozed["ag-admin"].at) {
		t.Errorf("snooze = %v/%v, want %v/%v", sn.since, sn.at,
			want.snoozed["ag-admin"].since, want.snoozed["ag-admin"].at)
	}
	if got.pausedCur != want.pausedCur || !got.pausedCurWaiting || !got.pausedHandedBack {
		t.Errorf("paused observation lost: %q %v %v", got.pausedCur, got.pausedCurWaiting, got.pausedHandedBack)
	}
}

// The handoff is consumed once: a second lobby starting later must not inherit
// a phase that belongs to a process that is already running.
func TestConductHandoffIsOneShot(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeConductHandoff(path, handoffFixture(now), now); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := readConductHandoff(path, now); !ok {
		t.Fatal("first read: ok=false, want true")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still present after read: err=%v", err)
	}
	if _, ok := readConductHandoff(path, now); ok {
		t.Error("second read: ok=true, want false")
	}
}

// An old file is a crash leftover, not a handoff. Resuming a half-hour-old
// escort would fight the user rather than continue their session.
func TestConductHandoffExpires(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeConductHandoff(path, handoffFixture(now), now); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := readConductHandoff(path, now.Add(conductHandoffTTL+time.Second)); ok {
		t.Error("expired handoff: ok=true, want false")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expired file not cleaned up: err=%v", err)
	}
}

func TestConductHandoffMissing(t *testing.T) {
	if _, ok := readConductHandoff(filepath.Join(t.TempDir(), "nope.json"), time.Now()); ok {
		t.Error("missing handoff: ok=true, want false")
	}
}

// writeConductHandoff and readConductHandoff enumerate conductor's fields by
// hand, because they are unexported and encoding/json cannot see them. That
// makes a field added to the struct later silently reset on every restart —
// the exact class of bug (state quietly lost across a re-exec) this file exists
// to remove. So pin the shape: when this fails, read the new field, decide
// whether the handoff should carry it, and only then update the count.
func TestConductorFieldsAreAccountedForInHandoff(t *testing.T) {
	// carried: phase, escortee, snoozed, pausedCur, pausedCurWaiting,
	// pausedHandedBack. Deliberately not carried: client — resolveClient
	// re-adopts on the first tick, and a stale client name is worse than looking.
	const accountedFor = 7
	if got := reflect.TypeOf(conductor{}).NumField(); got != accountedFor {
		t.Fatalf("conductor has %d fields, the handoff accounts for %d — decide whether the new field belongs in writeConductHandoff/readConductHandoff (and in this count) before changing this number", got, accountedFor)
	}
}
```

The test file imports `reflect` alongside `os`, `path/filepath`, `testing` and `time`.

Context for whoever implements this: a concurrent session was editing `cmd/claudemux-head/swconductor.go` in the main checkout while this plan was being executed, adding a `pausedCurDeferred` field to `conductor`. That work is not on this branch, so this test passes here and is *expected* to fail when the two are merged — that failure is the feature. Whoever merges reads the new field and decides whether a lobby restart should preserve it (it should: it is part of the paused-session observation the handoff already carries).

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run TestConductHandoff -v`
Expected: FAIL — `undefined: writeConductHandoff`.

- [ ] **Step 3: Implement the handoff**

Create `cmd/claudemux-head/conducthandoff.go`:

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// The lobby's conductor state — which session it is escorting to, and every
// waiting episode the user deliberately walked away from — lives only in
// memory. That is why shouldAutoRestart used to insist on a parked, snooze-free
// lobby before re-execing into a rebuilt binary: discarding the snoozes would
// send the user straight back to the session they had just left.
//
// In practice that gate almost never opened. A user who works inside sessions
// leaves the conductor escorting or paused with snoozes live, so the lobby sat
// on a binary eight days old while every head around it upgraded — and the
// conductor fix the user had just installed was simply not running.
//
// So carry the state instead of waiting for there to be none: write it
// immediately before restartSelf, read it once on the way up. This is a HANDOFF
// between two processes seconds apart, not a persistence layer — see
// conductHandoffTTL.
const conductHandoffTTL = 30 * time.Second

type rawSnooze struct {
	Since int64 `json:"since"`
	At    int64 `json:"at"`
}

type rawConductHandoff struct {
	At               int64                `json:"at"`
	Phase            int                  `json:"phase"`
	Escortee         string               `json:"escortee"`
	Snoozed          map[string]rawSnooze `json:"snoozed"`
	PausedCur        string               `json:"paused_cur"`
	PausedCurWaiting bool                 `json:"paused_cur_waiting"`
	PausedHandedBack bool                 `json:"paused_handed_back"`
}

// defaultConductHandoffPath matches the other head state files
// (defaultUsageCachePath, defaultStatuslineCachePath), env override included so
// tests never touch the real one.
func defaultConductHandoffPath() string {
	if p := os.Getenv("CLAUDEMUX_CONDUCT_HANDOFF_PATH"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "claudemux", "conductor-handoff.json")
}

// writeConductHandoff records c for the process about to replace this one.
// Best effort by contract: a failure costs the snoozes, not the restart, so
// callers ignore the error.
func writeConductHandoff(path string, c conductor, now time.Time) error {
	if path == "" {
		return nil
	}
	raw := rawConductHandoff{
		At:               now.Unix(),
		Phase:            int(c.phase),
		Escortee:         c.escortee,
		Snoozed:          make(map[string]rawSnooze, len(c.snoozed)),
		PausedCur:        c.pausedCur,
		PausedCurWaiting: c.pausedCurWaiting,
		PausedHandedBack: c.pausedHandedBack,
	}
	for name, sn := range c.snoozed {
		raw.Snoozed[name] = rawSnooze{Since: sn.since.Unix(), At: sn.at.Unix()}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// readConductHandoff consumes the file: it is removed whether or not it was
// usable, so a lobby started fresh minutes later never inherits a phase, and a
// corrupt file cannot wedge every future start. ok=false means "start clean",
// which is always safe — it is the behaviour every lobby had before this
// existed.
//
// The conductor's client is deliberately NOT carried: resolveClient re-adopts
// on the first tick, and a stale client name would be a worse answer than
// looking.
func readConductHandoff(path string, now time.Time) (conductor, bool) {
	if path == "" {
		return conductor{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return conductor{}, false
	}
	_ = os.Remove(path)
	var raw rawConductHandoff
	if err := json.Unmarshal(b, &raw); err != nil {
		return conductor{}, false
	}
	if at := time.Unix(raw.At, 0); now.Sub(at) > conductHandoffTTL || now.Before(at) {
		return conductor{}, false
	}
	c := newConductor()
	c.phase = swPhase(raw.Phase)
	c.escortee = raw.Escortee
	c.pausedCur = raw.PausedCur
	c.pausedCurWaiting = raw.PausedCurWaiting
	c.pausedHandedBack = raw.PausedHandedBack
	for name, sn := range raw.Snoozed {
		c.snoozed[name] = swSnooze{since: time.Unix(sn.Since, 0), at: time.Unix(sn.At, 0)}
	}
	return c, true
}
```

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./cmd/claudemux-head/ -run TestConductHandoff -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Turn the two existing gate tests around**

Two tests in `cmd/claudemux-head/switchboardtui_test.go` assert exactly the behaviour this task removes; they are the specification being changed, so edit them rather than adding tests beside them. Read both first (`TestSwitchboardShouldAutoRestart` ~line 915, `TestSwitchboardShouldAutoRestartLiveSnooze` ~line 957). Both stamp a temp file with `launchBinStampOf` and age it with `os.Chtimes` — keep that construction exactly.

In `TestSwitchboardShouldAutoRestart`, drop the two conductor phases from the must-not-restart table, leaving only the transient-UI guards:

```go
	for _, tweak := range []func(*swModel){
		func(m *swModel) { m.standby = true },
		func(m *swModel) { m.creating = true },
		func(m *swModel) { m.createBusy = true },
		func(m *swModel) { m.deferring = true },
		func(m *swModel) { m.fleetRestarting = true },
	} {
		mm := newSwModel("%1")
		mm.launchBin, mm.launchBinOK = stamp, true
		tweak(&mm)
		if mm.shouldAutoRestart(now) {
			t.Errorf("non-quiescent lobby must not restart (%+v)", mm)
		}
	}

	// The conductor's phase is no longer a gate: writeConductHandoff carries
	// phase, escortee and snoozes across the exec, so the busiest lobby is as
	// good a moment to upgrade as a parked one. Requiring swParked meant a user
	// who works inside sessions never got the upgrade at all.
	for _, phase := range []swPhase{swEscorting, swPaused} {
		mm := newSwModel("%1")
		mm.launchBin, mm.launchBinOK = stamp, true
		mm.cond.phase = phase
		mm.cond.escortee = "phenix"
		if !mm.shouldAutoRestart(now) {
			t.Errorf("phase %v must not block a rebuilt binary", phase)
		}
	}
```

Then rewrite `TestSwitchboardShouldAutoRestartLiveSnooze` — same fixture, inverted conclusion, and a comment that says why the old one was right for its time:

```go
// TestSwitchboardShouldAutoRestartLiveSnooze covers a lobby with a live snooze:
// the user walked an escortee back to the lobby, so conductor.snoozed holds the
// episode they just skipped. This used to block the restart, because a re-exec
// would drop the map and the fresh conductor would escort them straight back
// into that session. The handoff now carries the map across, so the restart
// proceeds and the snooze survives it.
func TestSwitchboardShouldAutoRestartLiveSnooze(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp, _ := launchBinStampOf(p)
	m := newSwModel("%1")
	m.launchBin, m.launchBinOK = stamp, true
	m.cond.phase = swParked

	if err := os.WriteFile(p, []byte("v2-longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-time.Minute)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}

	m.cond.snoozed = map[string]swSnooze{"x": {since: time.Unix(100, 0), at: now}}
	if !m.shouldAutoRestart(now) {
		t.Error("a live snooze must not block a rebuilt binary; the handoff carries it")
	}

	// And the snooze must actually survive the trip, or the escort the old gate
	// was protecting against happens anyway.
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := writeConductHandoff(path, m.cond, now); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok := readConductHandoff(path, now)
	if !ok {
		t.Fatal("handoff not readable")
	}
	if sn, hit := got.snoozed["x"]; !hit || !sn.since.Equal(time.Unix(100, 0)) {
		t.Errorf("snooze lost across the handoff: %#v", got.snoozed)
	}
}
```

- [ ] **Step 6: Run to verify the rewritten tests fail**

Run: `go test ./cmd/claudemux-head/ -run TestSwitchboardShouldAutoRestart -v`
Expected: both FAIL — the phase loop and the snooze case assert the new behaviour against the old gate. (`m.deferring` in the guard table may also fail to compile if that field is named differently; check the struct and use the real name.)

- [ ] **Step 7: Open the gate and wire the handoff**

In `switchboardtui.go`, replace the `shouldAutoRestart` body (keep the doc comment, rewritten — the old one argues for exactly the conditions being removed):

```go
// shouldAutoRestart reports whether this poll may re-exec into a rebuilt
// binary. The in-memory state that cannot survive a re-exec is the transient
// UI: standby, the create prompt, an in-flight create, the defer prompt, and a
// fleet-restart sweep whose sends run in a goroutine outside this model. Those
// still hold the gate shut.
//
// The conductor's own state no longer does. It used to: this waited for
// swParked with no live snoozes, because discarding conductor.snoozed would
// re-escort the user to the session they had just walked away from. But a user
// who works inside sessions leaves the conductor escorting or paused with
// snoozes live indefinitely, so the gate never opened and the lobby ran a
// binary eight days older than the fleet's heads. writeConductHandoff carries
// phase, escortee, snoozes and the paused observation across the exec instead.
func (m *swModel) shouldAutoRestart(now time.Time) bool {
	return !m.standby && !m.creating && !m.createBusy && !m.deferring && !m.fleetRestarting &&
		m.launchBinOK && binChanged(m.launchBin, now)
}
```

Read the handoff in `runSwitchboard`, **not** in `newSwModel`: every test in the package builds models with `newSwModel("%1")`, and a read there would let a real `~/.claude/claudemux/conductor-handoff.json` leak into the suite (and let the suite delete the user's file). Keep the constructor pure:

```go
	m := newSwModel(selfPane)
	// A handoff file means the process this one is replacing wrote its
	// conductor state on the way out, seconds ago. Adopt it so the snoozes and
	// the escort survive an upgrade; anything stale is ignored and removed by
	// readConductHandoff itself.
	if c, ok := readConductHandoff(defaultConductHandoffPath(), time.Now()); ok {
		m.cond = c
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
```

In the same function, write the handoff on every restart path — the automatic one, `R`, and `^R` all reach the same branch:

```go
	if fm, ok := final.(swModel); ok && fm.restart {
		_ = writeConductHandoff(defaultConductHandoffPath(), fm.cond, time.Now())
		restartSelf(stderr)
		return 1
	}
```

- [ ] **Step 8: Run the full suite**

Run: `gofmt -l cmd/ && go test ./...`
Expected: no gofmt output; all packages PASS.

- [ ] **Step 9: Check the README for claims this falsifies**

Run: `grep -n -i "parked\|auto-restart\|restarts itself\|snooze" README.md`

Read each hit. If a sentence says the lobby restarts only when parked or only with no snoozes, rewrite it to describe the handoff. If nothing says it, change nothing — do not add new documentation for its own sake.

- [ ] **Step 10: Commit**

```bash
git add cmd/claudemux-head/conducthandoff.go cmd/claudemux-head/conducthandoff_test.go cmd/claudemux-head/switchboardtui.go cmd/claudemux-head/switchboardtui_test.go README.md
git commit -m "$(cat <<'EOF'
lobby: hand the conductor's state to the restarted process

shouldAutoRestart required a parked, snooze-free lobby before re-execing into a
rebuilt binary, because conductor.snoozed is in-memory and losing it re-escorts
the user into the session they just walked away from. The reasoning was right
and the consequence was that the gate never opened: a user who works inside
sessions leaves the conductor escorting or paused with snoozes live, so the
lobby sat on a binary eight days old -- long enough that a conductor fix,
committed and installed, still was not running an hour later, and looked from
the outside like a fix that did not work.

Carry the state instead of waiting for there to be none. A one-shot handoff
file holds phase, escortee, snoozes and the paused observation; it is written
just before restartSelf and consumed once on the way up, and anything older
than 30s is treated as a crash leftover rather than a handoff. The gate now
keeps only the transient-UI guards, which protect state that genuinely has
nowhere to go.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013pXoLGcTKdDb7BdwDPTUpf
EOF
)"
```

---

## Verification after all tasks

- [ ] `go test ./...` green, `gofmt -l cmd/` silent, `bash -n bin/claudemux` silent.
- [ ] `tmux list-sessions` shows only the real fleet — every `__plan_*` harness session killed, `/tmp/__plan_stub` removed.
- [ ] `go install ./cmd/claudemux-head` then press `R` on the real lobby, and confirm with `lsof -p <lobby pid> | awk '$4=="txt"'` that the running inode now matches `~/go/bin/claudemux-head`. (`restartSelf` re-execs in place, so the PID and start time are unchanged and prove nothing — the inode is the only evidence.)
- [ ] Launch a fresh session for this project from the lobby's `n` key and confirm the head pane comes up at 5 rows and stays there, with `set-titles-string` on the session and not on the lobby.
