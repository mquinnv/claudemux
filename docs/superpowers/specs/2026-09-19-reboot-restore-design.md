# Reboot restore — design

Date: 2026-09-19
Status: approved in chat, pending spec review

## Problem

A macOS update rebooted the machine while several claudemux sessions were live.
Every tmux session, head and claude process died with it. Getting back meant
remembering which projects had sessions, relaunching each one, and finding each
conversation to `/resume` by hand.

What survives a reboot today is not enough to do this automatically:

- The pane map (`~/.claude/claudemux/panes/<pane#>.json`) holds `session_id` and
  `cwd` per claude pane, but it accumulates files for panes closed deliberately
  (pruned only after 7 days), has no tmux session name, and is keyed by pane
  numbers that restart with the tmux server.
- `@claudemux_claude_cmd` and the head's `c` restart already know how to relaunch
  claude with `--resume <id>` — but only into a pane that still exists.

The missing piece is a record of **which sessions were alive when the machine
went down**.

## Goals

- After a reboot (or tmux server death), the first `claudemux` switchboard
  offers to bring back every session that was live at the time.
- A restored session comes back under its old tmux name, in its project's
  normal layout/color/op_env handling, with claude resumed on the same
  conversation.
- Nothing is started without the user seeing the offer.

## Non-goals

- Auto-prompting interrupted sessions to continue. Restored sessions wait at
  their prompt; ones that were mid-turn are only *flagged*.
- Restoring shell-pane state (running commands, scrollback). The shell pane is
  launched fresh, running `launch.shell_command` if configured, as on any launch.
- Recovering the 2026-09-18 sessions automatically (they predate the records).
  That is a one-off manual restore from the pane map after this ships.
- A launchd login agent. Restore is offered by the switchboard.

## Design

### 1. Session records (claudemux-head)

Each head (not the lobby, not the `boot` holder) maintains one record per tmux
session:

`~/.claude/claudemux/sessions/<tmux-session-name>.json`

```json
{
  "session_name": "remix-2",
  "launch_dir": "/Users/michael/Projects/remix",
  "session_id": "b7a8741e-…",
  "claude_cwd": "/Users/michael/Projects/remix/.claude/worktrees/foo",
  "state": "Tool:Bash",
  "topic": "Fix idle detection in background agents",
  "last_seen": 1789000000
}
```

- `session_name` — `#{session_name}`; `launch_dir` — `#{session_path}` (the
  `-c` dir the launcher created the session with).
- `session_id` / `claude_cwd` — from the pane map entry the head already follows
  (after continuation resolution). A head with no bound session yet does not
  write a record — there is nothing to resume.
- `state` — the same machine value it publishes as `@claudemux_state`.
- `topic` — the summary line, for display in the restore picker only.
- `last_seen` — unix seconds of the write.

Written about every 30s from the existing poll loop, atomically (temp file +
rename, same as the pane-map hook). Filenames are the session name with any
`/` replaced; the real name lives inside the record.

Removal: a completed `/done` teardown deletes its session's record, since that
session was ended on purpose. Other deliberate closes (killing the session,
exiting everything) just leave the record to go stale — the selection rule
below ignores stale records, and records older than 7 days are pruned whenever
the directory is scanned.

### 2. Lost-session selection (pure function)

Input: all records, the cutoff time, and the set of live tmux session names and
live claude `session_id`s. Cutoff = max(`kern.boottime`, tmux server
`#{start_time}`).

A record is **lost** when all of:

1. `last_seen < cutoff` — it was last alive before this boot/server.
2. `last_seen >= newest_pre_cutoff - 10min` — it belongs to the cluster that
   died together, not a session closed hours or days earlier.
   `newest_pre_cutoff` is the newest `last_seen` among records satisfying (1).
3. No live tmux session has its `session_name`, and no live pane is already
   running its `session_id` (it was already restored by hand).

A lost record is **interrupted** when its `state` was a working state
(`Thinking`, `Tool:*`, `Background:*`, `Compacting`) — the same set the head
treats as "not waiting".

The result is sorted by `last_seen` descending. The offer's timestamp is
`newest_pre_cutoff`.

### 3. The offer (switchboard lobby)

On open, and on each lobby refresh until handled, the lobby runs the selection.
If it finds lost sessions, it shows a strip above the session list:

```
⏻ 7 sessions were running before the reboot (Sep 18 13:52) · r restore all · s select · x dismiss
```

- `r` restores every lost session.
- `s` opens a checklist: name (in project color when known), topic, age;
  interrupted ones marked `⚡ interrupted`. Space toggles, Enter restores the
  checked set, Esc backs out to the strip.
- `x` dismisses.

Restore and dismiss both **archive** the handled records into
`sessions/restored-<cutoff unix>/`, so the offer appears once per boot and the
records stay inspectable. Records left unchecked in the picker are archived too
— the offer is a one-time decision. Keys `r`/`s`/`x` only act while the strip is showing. `R` is not used:
the lobby already binds it to restart itself; `r`, `s` and `x` are unbound
today.

Restore runs sequentially, off the Update loop, one launcher call per session.
The strip shows progress (`restoring 3/7…`) and then any failures by name. The
new sessions show up in the lobby as any `-d` launch does; the conductor then
behaves normally (restored sessions are Idle at their prompt — they count as
waiting and will be escorted to as usual).

### 4. Relaunch (bin/claudemux)

Three new launcher options:

- `-r <session-id>` — append `--resume <id>` to `claude_cmd`. The id is
  validated with the same alphabet as `resumeIDOK`.
- `-N <name>` — use exactly this tmux session name instead of the directory
  basename (and its `-2` suffixing). If a session with that name exists, fail
  rather than attach — restore must not silently merge into another session.
- `-C <dir>` — start the claude pane in this dir instead of the launch dir.

The restore call per session is:

```
claudemux -d -W -N <session_name> -r <session_id> -C <claude_cwd> <launch_dir>
```

- `-W`: the session already has whatever worktree it had; do not mark it to
  create another.
- `-C`: claude looks up `--resume` ids under the project dir of its cwd, and a
  session that entered a worktree has its transcript under the worktree's
  project dir. If `claude_cwd` no longer exists (worktree removed), `-C` is
  omitted and claude starts in `launch_dir`; if the resume then fails, claude's
  pane shows the error as any failed launch does (remain-on-exit).
- If `launch_dir` itself no longer exists, the lobby skips that session and
  reports it as failed without calling the launcher.

Everything else — layout, head rows, shell size/command, project color and
name, op_env holder and deferred launch — is the ordinary launch path.

## Error handling

- Unreadable/corrupt record files are skipped (and not archived, so they can be
  looked at).
- `sysctl` or tmux `start_time` unavailable: use whichever one is available; if
  neither, no offer is made (never guess).
- A launcher failure for one session doesn't stop the rest; failures are listed
  on the strip.
- Record writes are best-effort: a failed write never affects the head's display.

## Testing

Go unit tests:

- Selection: cutoff boundary; 10-minute cluster (in and out); live name
  exclusion; live `session_id` exclusion; interrupted classification across all
  state values; empty and all-stale inputs; corrupt file skipped.
- Record: marshal/unmarshal round trip; atomic write; filename sanitising;
  prune of >7-day records; archive move.
- Restore argv construction, including missing `claude_cwd` and invalid ids.
- Lobby: strip rendering, `r`/`s`/`x` handling, picker toggle, strip gone
  after archive.

Launcher: `-r`/`-N`/`-C` covered by whatever shell-level tests exist in the repo
at implementation time; otherwise exercised by the manual end-to-end check.

Manual end-to-end: launch two or three sessions (one mid-turn, one in a
worktree), `tmux kill-server`, run `claudemux`, restore — each comes back under
its name, resumed, the mid-turn one flagged.
