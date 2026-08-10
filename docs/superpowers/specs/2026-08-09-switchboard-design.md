# Switchboard design

2026-08-09

## Purpose

A full-screen utility that automatically ferries your tmux client between claudemux
sessions that are paused waiting on input. You park on a lobby screen; when a session
starts waiting on input, the switchboard switches your client to it; when you answer, it moves
you to the next waiting session, or back to the lobby when nothing is waiting.

## Scope

- Watches **claudemux sessions only** (sessions with a live `claudemux-head` pane).
- Switch policy: **only when resolved** — once it lands you on a waiting session it
  stays put until that session stops waiting. No dwell timer.
- Home base: a **lobby screen** in the switchboard's own tmux session showing the
  fleet and its states.

## What "waiting on input" means

`classifyState` deliberately no longer emits `StateAwaiting` (the 15s stuck-tool
heuristic was removed for false positives — see `state.go:36`, `teardown.go:164`).
A session counts as **waiting** when its published state is either:

- `Idle` — Claude's turn ended; the session is waiting for the next prompt, or
- `Tool:AskUserQuestion` — an AskUserQuestion tool_use is unresolved; Claude is
  literally asking the user something.

Permission prompts are **not** detectable from the transcript and are out of scope.

## Architecture

Two pieces:

1. **State publishing** — `claudemux-head` publishes its computed state onto its tmux
   session as user options.
2. **Switchboard** — a new `claudemux-head switchboard` subcommand: a full-screen
   Bubble Tea app that polls those options and drives `tmux switch-client`.

The head remains the single source of truth for session state; the switchboard never
re-implements transcript tailing.

### 1. State publishing (change to claudemux-head)

On every state-kind transition (and once at startup), the head runs:

```
tmux set-option -t <session> @claudemux_state <value> \; \
     set-option -t <session> @claudemux_state_since <unix-seconds>
```

- `<value>` is the machine form of the state: the kind name (`Idle`, `Thinking`,
  `Compacting`, …), with tool states as `Tool:<ToolName>` (e.g.
  `Tool:AskUserQuestion`).
- `_since` comes from `State.Since`, letting the watcher order the waiting queue
  oldest-first even across watcher restarts.
- Fire-and-forget with a short timeout, following the `tabtitle.go` pattern; a wedged
  tmux never blocks the head.
- No cleanup on exit: the watcher trusts the option only while the session still has a
  live `claudemux-head` pane.

### 2. Switchboard subcommand

Runs full screen in its own tmux session (the lobby). Once per second it takes a
snapshot via two tmux calls:

- `tmux list-sessions -F '#{session_name}\t#{@claudemux_state}\t#{@claudemux_state_since}'`
- `tmux list-panes -a -F '#{session_name}\t#{pane_current_command}'` — liveness: a
  session counts as a claudemux session only if it has a pane running `claudemux-head`.

The snapshot feeds a **conductor** state machine:

- **Parked** — the driven client is on the lobby. If any session is waiting,
  `switch-client` to the oldest-waiting one and move to Escorting.
- **Escorting** — the conductor moved the client to session X. Hold until a poll
  observes X no longer waiting, then switch to the next queued session, or back
  to the lobby if the queue is empty.
- **Paused** — the client's current session is not where the conductor put it (the
  user switched manually, including via the lobby's Enter key). Stop conducting;
  resume (back to Parked) only when the client returns to the lobby. The conductor
  never fights the user for the client.

Client handling: the conductor drives exactly one client — the one attached to the
lobby session when conducting starts (re-resolved if it detaches). The switchboard's
own session is excluded from watching.

Queue: sessions that are waiting (per the definition above) and not snoozed,
ordered by `_since` ascending (name as tiebreak).

Snooze: if the user manually leaves an escorted session that is still waiting, that
session is snoozed for its current waiting episode — it re-queues only when its
`_since` changes (a new waiting episode). Without this, skipping an Idle session
would bounce the client straight back to it on the next return to the lobby. Sessions
with a missing or unparseable option are shown as unknown and never queued.

### 3. Lobby UI

Full-screen fleet view rendered with Bubble Tea + lipgloss (matching the existing
head styling conventions):

- One row per claudemux session: name, colored state chip, time in state, and a queue
  marker on waiting sessions.
- A status line: `conducting` / `paused — you navigated away` / `N waiting`.
- Keys: `j`/`k` (and arrows) to move the selection, `Enter` to switch-client to the
  selected session manually (this pauses conducting), `q` to quit.
- No pane previews and no summary lines in v1.

### 4. Launch entry

`claudemux switch` in `bin/claudemux`: create the `switchboard` session if missing —
its single pane running `claudemux-head switchboard` directly (the no-shell pattern
used for the head and claude panes) — then attach/switch to it.

## Error handling

- tmux command failures during a tick: log to the debug log, skip the tick; the next
  tick retries. Repeated failures leave the lobby visible with stale data rather than
  crashing.
- Publishing failures in the head are best-effort and silent (as with tab renames).
- Heads predating this change publish nothing: their sessions appear as unknown state
  and are never queued.

## Testing

Pure-logic tests in the existing repo style (no tmux integration tests):

- Publish-args builder (mirrors `tabRenameArgs` tests).
- Snapshot parsing from `list-sessions` / `list-panes` output, including missing
  options and the liveness filter.
- Conductor transitions across fake snapshots: queue ordering, hold-until-resolved,
  empty-queue return to lobby, manual-switch pause and lobby-return resume.
- Bubble Tea model update tests for the lobby (selection movement, key handling),
  like `tui_test.go`.

## Out of scope (v1)

- Non-claudemux sessions and bare `claude` runs.
- Pane content previews, session summaries in the lobby.
- Multi-client conducting.
- Configurable poll interval or switch policies.
