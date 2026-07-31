# Session teardown: press `x` to wrap up and kill the session

## Problem

A claudemux session outlives its work. When `claude` exits, its pane falls back to a
shell prompt; when the session ran in a worktree that has since been removed, that shell
sits in a directory that no longer exists. The tmux session, its window, and its terminal
tab remain until they are killed by hand.

The wrap-up itself is also a multi-step ritual: run the project's wrap-up command
(`/done`, which verifies nothing is uncommitted or unpushed and then removes the
worktree and branch), exit `claude`, then kill the tmux session — in that order, because
killing the session first would take `claude` down with its pty before it can finish.

This adds one key to `claudemux-head` that drives the whole sequence, with a human gate
at the destructive step.

## Interaction

Focus the status pane and press `x`.

1. **First press** — the wrap-up command is typed into the `claude` pane and submitted.
   The user answers whatever it asks (`/done` takes a single confirmation) exactly as
   they would have by hand. The status line shows `⏻ wrapping up…`.
2. **Gate** — once the head can see that the turn has ended *and* the worktree is gone,
   the status line changes to `⏻ press x to tear down`. If the wrap-up bailed — dirty
   tree, unpushed commits, the user declined — the worktree is still there, the gate
   never opens, and the status line says so.
3. **Second press** — `/exit` is typed into the `claude` pane. The head waits for
   `claude` to actually be gone, then kills the tmux session. The status line shows
   `⏻ exiting claude…` in between.

`esc` cancels a teardown in flight and returns to the normal status line. Any abort
(timeout, unsubmitted command, `claude` that will not exit) returns to the same place and
shows a reason.

### Why `x`

tmux users read `x` as "kill this" (`prefix-x` kills a pane), and the sequence ends in
`kill-session`. The mnemonic alternative (`d`, for `/done`) was rejected because the
wrap-up command is configurable: point `teardown.command` at something else and `d` names
nothing, while `x` still describes the outcome.

`x` is free. The existing keymap is `r` (pin/reset the tab) and `q` / `ctrl+c` / `esc`
(quit the head).

## State machine

One field on the model, four states:

| State | `x` | `esc` | Status line |
|---|---|---|---|
| `teardownIdle` | send wrap-up command → `teardownSent` | quit (unchanged) | — |
| `teardownSent` | ignored | → `teardownIdle` | `⏻ wrapping up…` |
| `teardownReady` | exit claude, kill session → `teardownExiting` | → `teardownIdle` | `⏻ press x to tear down` |
| `teardownExiting` | ignored | → `teardownIdle` | `⏻ exiting claude…` |

`esc` keeps quitting the head in `teardownIdle`; it only means "cancel" while a teardown
is armed. This overload is deliberate: a key that arms a destructive action needs a
cancel, and adding a second cancel key to a four-key TUI is worse.

Transitions out of `teardownSent` and `teardownExiting` are driven by the existing
one-second poll tick, not by keys.

A cancelled or aborted teardown does **not** undo anything already done. The wrap-up
command has run; cancelling only means the head stops driving the rest. This is stated in
the README so nobody reads `esc` as a rollback.

## Sending the wrap-up command

The `claude` pane id already exists inside `panemap.go`: `claudePaneCandidates` ranks the
panes in the session running `claude` (or `node`), and `mappedTranscript` picks one and
discards the id. `mappedTranscript` grows a fourth return value carrying the pane it
chose, so the teardown targets exactly the pane whose transcript the head is following.

The command is sent as two tmux calls, not one:

```
tmux send-keys -t <claudePane> -l -- "<command>"
… short delay …
tmux send-keys -t <claudePane> Enter
```

`--` ends tmux's option parsing: `teardown.command` is user config, and a value beginning
with `-` would otherwise be read as a flag to `send-keys` instead of typed. Each call gets
its own 2s context, per `teardownTmuxTimeout`'s "bounds *each* subprocess" contract — a
single shared one would also have to cover the delay between them.

**Why split.** Typing `/done` in Claude Code opens the slash-command completion popup. An
`Enter` arriving in the same input burst can be consumed by the popup (selecting the
completion) rather than submitting the line. Sending the literal text, letting the TUI
settle, and then sending `Enter` separately is the mitigation.

**Verification.** This is a best-effort keystroke injection into someone else's TUI, so it
is checked rather than assumed: if no new user event appears in the transcript within
`teardownSubmitTimeout` (10s), the sequence aborts to `teardownIdle` with
`⏻ wrap-up didn't submit`. The user's own pane is left exactly as it is — possibly with
`/done` typed but unsent, which they can submit themselves.

An empty `teardown.command` skips this step entirely. The first `x` press still enters
`teardownSent`, but nothing is typed into the `claude` pane and the submit timeout does
not apply; the ready gate is evaluated on the next poll tick as usual, so a session that
is already cleaned up reaches `teardownReady` within a second, and one that is not sits
showing why.

## The ready gate

Evaluated on each poll tick while in `teardownSent`. All three conditions must hold:

0. **The wrap-up was submitted** — the same `teardownSubmitted` evidence the submit
   timeout watches for. `classifyState`'s reading is only as fresh as the last poll, so
   without this a probe returning a second after arming could be judged against the
   `StateIdle` captured *before* the command was typed: the gate would open and invite the
   irreversible second press while the wrap-up had barely started. It cannot deadlock — an
   empty `teardown.command` is marked submitted at arm time, and a wrap-up that never
   submits aborts after 10s.
1. **The turn has ended** — `classifyState` is not `StateThinking` and not `StateTool`.
   `StateAwaiting` counts as ended: the wrap-up command asking its confirmation question
   is a legitimate pause, and condition 2 will not hold yet anyway. (`classifyState` no
   longer emits `StateAwaiting`; the mapping is kept for if it returns.)
2. **The worktree is gone** — see below.

For a session that was never in a worktree, condition 2 is vacuous and the gate opens on
conditions 0 and 1 alone.

The probe runs on every one-second tick until a **blocked** reading lands (turn over,
worktree still standing), then backs off to every five seconds for as long as it stays
blocked. A blocked teardown is a resting state a user can leave on screen indefinitely,
and re-forking `git worktree list` every second to re-answer a question that only changes
when the human acts is thousands of subprocesses for nothing.

### Determining that the worktree is gone

**The head's own working directory is not the session's.** `bin/claudemux` creates the
head pane with `-c "$work_dir"` — the main checkout — and `claude --worktree` chdirs
*itself* into `.claude/worktrees/<name>`. So for every session started with `-w`,
`launch.auto_worktree`, or `.project.yml worktree: true`, the head sits in a directory
that is not a worktree and that the wrap-up will never delete. Arming off it would leave
`inWorktree` false and quietly reduce the gate to condition 1 alone — never checking the
evidence that the wrap-up succeeded, which is the whole point of condition 2.

The directory to watch is therefore the **session's** cwd: `m.sessionCwd`, the last
non-sidechain transcript cwd, which the worktree chip also consults first for exactly
this reason (see `panemap.go` on the pane-cwd glob going stale the moment a session cd's
into a worktree).

**The gate follows the cwd, not the work, and that is narrower than the chip.**
`worktreeChip` falls back to `cmdWorktree` — a dominance heuristic over the recent
command window — so it can label a session that drives a worktree at arm's length while
its cwd stays in the main repo. The gate deliberately does not use that fallback: it is a
heuristic, and a mis-picked directory that someone else deletes would open the gate on
evidence about the wrong worktree. The cost is that arm's-length sessions get condition 2
vacuously and rest on condition 1 alone — the *laxer* outcome, not a stricter one, which
is why the README says so plainly rather than leaving the user to infer it from the chip.

It is captured **when `x` first arms a teardown**, not per probe: by the time the gate is
being evaluated the wrap-up may already have deleted the directory, and once `claude`
exits `sessionCwd` stops being refreshed. The path is symlink-resolved at capture, because
`git worktree list` prints resolved paths (on macOS a `/var/...` cwd is really
`/private/var/...`). The head's own startup-captured launch directory remains the
**fallback**, for the window before the first main-session event has been read — where it
is also exactly right for a session that never entered a worktree. Neither path is ever
re-derived with `os.Getwd()`: that fails as soon as the wrap-up deletes the directory,
which is why the startup capture exists at all.

The main checkout needs no session equivalent: a linked worktree belongs to the same repo
as the checkout the head was launched in, so the startup-resolved main checkout is still
the right place to run `git worktree list` from.

Gone is then true when either holds:

- the captured directory no longer exists (`os.Stat` → `IsNotExist`), or
- it still exists but `git worktree list --porcelain`, run from the repo's main checkout,
  no longer lists it.

The second case covers a wrap-up that unregistered the worktree without deleting the
directory. The main checkout path is also resolved once at startup
(`git rev-parse --path-format=absolute --git-common-dir`, then its parent), because by
the time the check runs there may be no valid cwd to run `git` from.

Both are pure functions over captured inputs — the `git worktree list` output is parsed
by a function that takes the listing as a string, so the whole gate is testable without a
filesystem.

## Exit and kill

On the second `x` press:

1. `send-keys -l "/exit"` then `Enter`, split the same way and for the same reason —
   the same send helper the wrap-up command uses, with different text, rather than a
   second near-identical one.
2. Poll until `claudePaneCandidates` returns no candidates for this session — i.e. no
   pane in it is running `claude` or `node` any more. This reuses the primitive the head
   already runs every second; no new detection mechanism.
3. `tmux kill-session -t <session-id>`, where the id is `#{session_id}` (`$N`) read back
   from this pane — not `#{session_name}`. tmux's `-t` resolves a name by exact match,
   then prefix, then fnmatch pattern, so a name could select a different session; an id
   cannot be reinterpreted, which is what the one irreversible call in this feature wants.

If step 2 does not succeed within `teardownExitTimeout` (15s), abort to `teardownIdle`
with `⏻ claude didn't exit`. The session is **not** killed over the top of a running
`claude` — the entire point of the ordering is to let it exit cleanly.

`kill-session` takes the head down with everything else, so it is the last statement
executed; there is no post-kill state to render.

## Configuration

One new block in `config.yml`:

```yaml
teardown:
  command: /done
```

- `teardown.command` — the wrap-up command typed into the `claude` pane on the first
  press. Default `/done`. Set to `""` to skip the wrap-up entirely, making `x` a
  gated exit-and-kill.

Unknown config keys are already a hard startup error, so this slots into the existing
config struct and its round-trip tests with no new validation machinery. A missing
`teardown:` block means defaults, as with every other block.

## Rendering

The status line already renders `⬚ pinned` as a right-hand chip; teardown state renders
in the same slot with the same treatment:

| State | Chip |
|---|---|
| `teardownSent` | `⏻ wrapping up…` |
| `teardownReady` | `⏻ press x to tear down` |
| `teardownExiting` | `⏻ exiting claude…` |
| abort (transient, 5s) | `⏻ <reason>` |

Abort reasons: `wrap-up didn't submit`, `worktree still present`, `claude didn't exit`,
`no claude pane`, `session rotated`.

`⏻ session rotated` fires when the monitored session rotates (new session, `/clear`,
resume) while a teardown is armed. The teardown was armed against the session that just
went away — its wrap-up went to that session's pane, and `teardownPrompt` refers to that
session's transcript, which the rotation replaces. Left running, the next tick would read
the new session's different `lastPrompt` as proof the wrap-up submitted, removing the only
bound on how long `teardownSent` can sit armed. Aborting says so instead.

`⏻ worktree still present` is shown while in `teardownSent` once the turn has ended but
the worktree survives — that is the "your `/done` bailed" signal, and it stays until the
user cancels or the worktree does disappear.

If both a teardown chip and `⬚ pinned` apply, the teardown chip wins: it is transient and
actionable, the pin is ambient.

## Failure behavior

Every failure degrades to "abort, say why, change nothing further" — the same discipline
`resetTabCmd` follows.

| Failure | Behavior |
|---|---|
| Not inside tmux (`TMUX_PANE` empty) | `x` is inert |
| No `claude` pane found | abort, `⏻ no claude pane` |
| `send-keys` fails | abort, `⏻ wrap-up didn't submit` |
| Command typed but not submitted | abort after 10s, same message |
| Wrap-up bailed, worktree survives | gate stays shut, `⏻ worktree still present` |
| `claude` will not exit | abort after 15s, `⏻ claude didn't exit`, session left alive |
| Session rotates while armed | abort, `⏻ session rotated` |
| `git` missing or not a repo | worktree treated as absent; gate rests on turn-end |
| Wedged tmux server | every subprocess is context-bounded (2s), as elsewhere |

## Testing

Pure functions, tested the way `tabreset_test.go` tests its builders:

- state transitions — a table of (state, key) → (state, command issued)
- the ready-gate predicate over synthetic `(State, worktreeGone, isWorktree)` inputs
- `worktreeGone` over a captured `git worktree list --porcelain` listing plus a stat stub
- tmux argument builders for send-keys (literal), Enter, and kill-session
- the chip renderer for each state, including precedence over `⬚ pinned`
- config round-trip: default, explicit, empty-string, and unknown-key-is-an-error

Subprocess work lives in `tea.Cmd`s off the `Update` loop with bounded contexts,
matching `resetTabCmd` and `renameTabCmd`; nothing in the teardown path can block
rendering.

## Documentation

README gains a "Tearing down a session" subsection beside the existing tab-pinning
section, covering the two-press flow, what the gate means, that `esc` cancels but does
not roll back, and the `teardown.command` key. The `config.yml` block in **Configuration**
gains `teardown:` with its default.

## Out of scope

- Killing a session from outside it (a `claudemux -k` CLI). The head is the only place
  that knows the session's state.
- Any automatic teardown that fires without a keypress.
- Anything that runs after `kill-session`.

## Known gaps

Findings from review that were judged real but not worth blocking the feature. Each was
traced; none can cause a wrong `kill-session`.

- **No generation counter on teardown messages.** `tui.go` uses `summaryGen` for exactly
  this staleness class, and the teardown path does not. Traced safe because both probe
  answers are monotone: a removed worktree does not come back, and claude having exited
  stays exited — so a stale reply can only agree with a fresh one. Worst case is one
  extra read-only subprocess.
- **A late `teardownSentMsg` can abort a teardown already in `teardownExiting`,** relabelling
  it `claude didn't exit`. Aborts in the safe direction — `/exit` was delivered, the session
  survives — but the message is then untrue.
- **`session rotated` also aborts during `teardownExiting`,** where its rationale does not
  apply: it exists because `switchSession` recomputes `lastPrompt` and could forge
  submission evidence, which only matters in `teardownSent`. The kill target is derived
  from `selfPane` and is unaffected by a transcript rebind.
- **`model.inWorktree` is write-only** since the gate moved to `captureTeardownTarget`,
  which recomputes the fallback itself. Dead field, and its comment still describes the
  old role.
- **The arm's-length worktree case gets no worktree verification** — see "Determining that
  the worktree is gone" above. Disclosed in the README rather than papered over.
