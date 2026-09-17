# tmux target resolution, launch sizing, and the lobby that never restarts

**Date:** 2026-09-17
**Status:** agreed, ready to implement

Three defects found while diagnosing two user-visible symptoms: "the head pane
in the claudemux project starts ~11 rows tall and I have to drag it every
time", and "I'm still being conducted into deferred sessions after the fix".

---

## 1. Bare tmux targets resolve to a window, not a session

### The rule

tmux resolves a **bare** `-t NAME` by trying, in order, a window-name *prefix*
match **in the caller's own session**, and only then a session name. A target
written `NAME:` is unambiguous — tmux resolves it as a session and nothing
else. (`=NAME` is not a substitute: `set -t =claudemux` fails with `no such
session: =claudemux`.)

### Why it fires here and nowhere else

The lobby's window is named **`claudemux-head`**. A project directory named
**`claudemux`** is a prefix of it. The lobby's `n` key runs the launcher as its
own child (`swCreateCmd`: `claudemux -n -d -- <query>`), so every bare `-t
"$session_name"` in `bin/claudemux` is evaluated with the *lobby* as the
caller's session — and `claudemux` matches the lobby's window before it ever
reaches the session table.

Measured on the live fleet (tmux 3.7c), running as a child of the lobby pane:

```
tmux set -t claudemux @probe 1   → landed on switchboard   (claudemux: 0)
tmux set -t gh-hud    @probe 1   → landed on gh-hud        (switchboard: 0)
tmux set -t claudemux: @probe 1  → landed on claudemux     (switchboard: 0)
```

This is the same bug already fixed once for the defer toggle, whose comment
(`switchboardtui.go`, `swDeferTarget`) names the hazard exactly: *"the only
session in the fleet it could happen to, and the one it did."* It was fixed
there and nowhere else.

### What it broke

Everything in the launcher's tail landed on the lobby instead of the new
session:

| site | command | consequence |
|---|---|---|
| `bin/claudemux:952` | `set-hook window-resized "resize-pane -t <head> -y 5"` | **the reported symptom** — new session gets no re-pin hook |
| `bin/claudemux:964-965` | `set-titles` / `set-titles-string` | terminal tab stops naming the project |
| `bin/claudemux:248` | `status-left-length 32` (in `attach`) | session name truncated in the status bar |
| `bin/claudemux:217,219` | `status-style`, `pane-active-border-style` | **the lobby's status bar gets painted the project's colour** |

Observed leftovers on the live lobby: `set-titles-string "claudemux · #W"`,
`status-left-length 32`, `status-style "bg=#b34dff"` (this project's purple).
The claudemux session was the only one of ten in the fleet missing all four.

The Go side has the same shape at `swSwitchCmd` (`switch-client -c <client> -t
<target>`), which is how the conductor escorts. Unverified whether `-c`
re-bases resolution onto the client's session; the implementation must
establish this with a harness before deciding the fix is cosmetic.

### Decision

Use the `NAME:` form for every session-scoped target in `bin/claudemux` and in
the Go code. Add a guard test so the next bare target is caught in CI rather
than by a user dragging a pane.

---

## 2. The head pane's height, and where ~11 rows comes from

`bin/claudemux:878` sizes the new session from `tput`:

```bash
tmux new-session -d -s "$session_name" -c "$work_dir" -x "$(tput cols)" -y "$(tput lines)"
```

Under the lobby there is no tty, so `tput` reports its terminfo default,
**80x24** — measured. The head is split at `-l 5` of those 24 rows. When a
51-row client switches in, tmux scales the panes proportionally: 5/24 × 51 ≈
**11 rows**. The `window-resized` hook exists precisely to re-pin it — and
under defect 1 that hook went to the lobby, so nothing corrected it and the
user dragged the pane by hand.

Fixing defect 1 alone makes the symptom disappear. This is still worth fixing:
a session built at a quarter of its real size makes every split-time `-l`
meaningless, and the same wrong size is used when the launcher runs from
*inside* any tmux pane (`tput` then reports the **pane's** size, not the
client's).

### Decision

Prefer the size of the client that will actually display the session, falling
back to the tty, falling back to tmux's own default:

1. inside tmux (`$TMUX` set) → `tmux display-message -p -t "$TMUX_PANE"
   '#{client_width}x#{client_height}'` (verified to work from a non-tty
   subprocess: returned the real client's `148x52` where `tput` said `80x24`)
2. else a tty on stdout (`[ -t 1 ]`) → `tput cols` / `tput lines`
3. else omit `-x`/`-y` entirely and let tmux default

Guard the parse: a non-numeric or empty answer falls through to the next
source rather than reaching `new-session` verbatim.

---

## 3. The lobby never picks up a new binary

`shouldAutoRestart` (`switchboardtui.go`) requires `swParked` **and** an empty
snooze map, on top of the UI-transient guards. `swParked` means the driven
client is sitting on the lobby. A user who spends the day inside sessions
leaves the conductor in `swEscorting`/`swPaused` with snoozes accumulating, so
the gate never opens.

Measured: the running lobby had the binary from **eight days** earlier mapped
(`lsof -p 1601` txt inode 131121133 / 17006306 bytes vs the installed
131271100 / 17022930). Its PID and start time are no evidence either way —
`restartSelf` re-execs in place, preserving both. The practical effect: the
conductor's *defer* fix, committed and installed at 14:43, was still not
running at 15:30, and the user kept being escorted into deferred sessions and
reasonably concluded the fix did not work.

The existing gate is not arbitrary — its comment is right that
`conductor.snoozed` is in-memory and that discarding it re-escorts the user
into the session they just walked away from. The answer is to carry the state
across the re-exec rather than to wait for a moment when there is none.

### Decision

A **one-shot handoff file**, written immediately before `restartSelf` and
consumed once at startup:

- `~/.claude/claudemux/conductor-handoff.json`, with a
  `CLAUDEMUX_CONDUCT_HANDOFF_PATH` override for tests (the established pattern:
  `defaultUsageCachePath`, `defaultStatuslineCachePath`).
- Carries `phase`, `escortee`, the snooze map, and the paused-session
  observation (`pausedCur`, `pausedCurWaiting`, `pausedHandedBack`).
- Stamped with `at`. A file older than **30s** is ignored and deleted: it means
  a crash or a stale leftover, not a handoff, and a conductor resuming a
  half-hour-old phase would fight the user.
- Read exactly once — deleted whether or not it was used — so a restart that
  is not a re-exec (a fresh `claudemux switch`) never inherits someone else's
  phase.

Writing on every restart path, not only the automatic one, keeps `R` and `^R`
from bouncing the user back into a session they walked away from.

With the state preserved, `shouldAutoRestart` drops the `swParked` and empty-
snooze conditions and keeps only the transient-UI guards (`standby`,
`creating`, `createBusy`, `deferring`, `fleetRestarting`), which protect
in-memory state that genuinely cannot survive a re-exec.

---

## Out of scope

- Cleaning the mis-targeted options already sitting on the user's lobby. They
  are re-applied by the next claudemux launch; once defect 1 is fixed they stop
  appearing, and the stale ones can be cleared by hand.
- The head's own restart gate (`tui.go`), which already restarts freely.
- Anything about `audit-branches`, teardown, or the defer feature itself.
