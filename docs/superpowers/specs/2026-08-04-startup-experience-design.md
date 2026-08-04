# Startup experience: panes run their program, never a typed-into shell

## Problem

Launching a session looks like someone else driving your terminal. `create_session` builds
three panes as interactive shells and then **types into them** with `send-keys`, so the
first thing a new session shows is a shell prompt, a command appearing character by
character, and — on the `op_env` path — an echoed notice with a second prompt sitting
under it:

```
~/Projects/remix $  echo 'claudemux: unlocking 1Password environment — claude will start here…'
claudemux: unlocking 1Password environment — claude will start here…
~/Projects/remix $
```

Then nothing happens for the length of an `op environment read`, and eventually `claude`
is typed in below it. The user's words: *"it is lame that we see echo 'starting blah...'
and then it echos it, and returns control to a prompt. pretty lame."*

The wait is not incidental and it is not short. Measured on this machine, fully
authorized with the desktop-app integration already approved, three consecutive
`op environment read` calls for the Environment this repo uses took **27.4s / 25.4s /
22.4s** — for an Environment containing exactly one variable. There is no CLI session
cache to warm (`op whoami` reports "account is not signed in" in 0.13s while the read
succeeds anyway), and every new session pays it again. The `~10s` figure in
`deferred_launch`'s comment is stale by 2-3x and is corrected as part of this work.

The cost is **not** decryption, and saying so is the mistake the old comment made. Only
~38% of that wall time is CPU (7.7s user + 5.3s sys of a 33.3s run). For comparison, on
the same machine and account `op item get` decrypts an entire item — secret fields
included — in 0.81s wall / 0.12s CPU, and `op item list` pulls every item in the account
in 7.0s wall / 0.25s CPU. Reading one variable out of an Environment burns roughly 100x
the CPU of decrypting a whole item, reproduced on both op 2.38.1-beta.01 and
2.38.2-beta.01.

That matters for scope: this design makes the wait *bearable*, it does not make it
*correct*. The real fix is to stop using 1Password Environments for launch-critical
secrets (a normal item read via `op read` / `op item get` costs under a second), which
is a data change outside this work. If that happens, the holder below simply flashes
briefly instead of running for 25 seconds — the design degrades gracefully and nothing
here needs to be revisited.

So the goal is not to hide a flicker. It is to hold a pane for ~25 seconds in a way that
looks deliberate and visibly alive, and then become `claude` without the terminal ever
showing through.

## Shape of the fix

Two changes, one principle: **a pane that is going to run a program is spawned running
that program.** Nothing is ever typed into a shell that the user can see.

1. The head pane and the claude pane are *command panes* — `tmux respawn-pane` replaces
   their shell with the program before anything is attached. A command pane never draws a
   prompt, so there is nothing to hide.
2. While the secrets decrypt, the claude pane runs a holding TUI —
   `claudemux-head boot` — which is then replaced *by tmux* with `claude` itself.

The shell pane is untouched: it is a shell, it should look like one.

### Why the pane is created as a shell and immediately respawned

`new-session`/`split-window` accept a command directly, which would save a call. Two
things argue for creating the pane first and respawning it:

- **`remain-on-exit` has to be set before the program can fail.** A command that exits
  immediately (a binary that is not on `PATH`) takes its pane with it, and with
  `new-session` that means the session is gone before the next `tmux` call in
  `create_session` runs — turning a missing binary into a cascade of "can't find pane"
  errors under `set -e`. Setting the option on a pane that already exists removes the
  race entirely.
- **Nothing is attached yet**, so the shell is never drawn. `respawn-pane` clears the
  pane's screen *and* its scrollback and resets the alternate-screen flag (verified),
  so by the time `attach` runs there is no trace of it.

### `remain-on-exit failed` on the two command panes

A command pane vanishes when its program exits, which is right for a clean exit — the
session-teardown spec calls the leftover shell a nuisance — but wrong for a failure,
because the error message goes with it. `remain-on-exit failed` (tmux >= 3.2) keeps the
pane only when the program exits non-zero: a missing `claudemux-head` leaves a dead pane
reading `claudemux-head: command not found` with `pane_dead_status` 127 instead of a
window that silently comes up with a pane missing.

It is set with `2>/dev/null || true`: older tmux rejects the `failed` value, and the
degradation (panes vanish on failure, as they would have anyway) is not worth an error.

Consequence, handled: **a dead pane keeps reporting its command in
`pane_current_command`** (verified). Without a filter, a `claude` that crashed would still
look like a live `claude` pane to `claudePaneCandidates` — the head would bind to a
corpse, and a teardown's "wait for claude to be gone" would never finish. `listPanes`
therefore asks tmux to filter dead panes out (`-f '#{==:#{pane_dead},0}'`), which restores
exactly the old semantics: a pane that is no longer running claude is not a candidate.

### `exec` in front of every pane command

tmux runs a pane command as `default-shell -c '<command>'`. For a single simple command
every shell in play execs it, so `pane_current_command` names the real program — but that
is a property of the shell, not a guarantee, and the whole pane-map mechanism depends on
the claude pane reporting `claude`. The commands are therefore written `exec <command>`.

This is also why the leading `exec` cannot be dropped later if a pane command ever grows a
second statement: `sh -c 'a; b'` leaves the *shell* as the process group leader, and
`pane_current_command` would report the shell forever.

### Absolute paths for `claudemux-head` and `claude`

The old `send-keys` path typed into an **interactive** shell, which had sourced the user's
rc and so had the user's full `PATH`. A pane command runs in a **non-interactive** shell
whose environment comes from the tmux server, which may predate that `PATH`. Both binaries
are therefore resolved with `command -v` in the launcher — whose environment *is* the
user's interactive one — and the resolved path is used. When `command -v` finds nothing
the bare name is used, which is what a shell function or alias needs anyway (a fish
function is invisible to bash's `command -v` but resolves fine inside a fish `-c`).

This closes the binary lookup only. Anything else the rc used to provide (an nvm-managed
`node`, say) is not restored by this; see **Known gaps**.

## The holding TUI (`claudemux-head boot`)

```
claudemux-head boot [--label TEXT] [--project NAME] [--expected DUR] [--timeout DUR] -- CMD [ARG...]
```

It draws a small centered block and, when it stops waiting, **execs `CMD`**:

```
                              remix

                   ⠹  Unlocking 1Password environment
                          0:12 / about 0:25

              claude starts here · press any key to skip the wait
```

- **The spinner and the elapsed clock are the point.** A static splash for 25 seconds is
  indistinguishable from a hang. The elapsed time is shown against the measured typical
  duration (`--expected 25s`, passed by the launcher) so the wait is bounded in the user's
  head rather than open-ended. The tradeoff is that this makes the cost visible: today the
  user waits 25 seconds without being told, and after this change they watch 25 seconds
  tick past. That is deliberate — an honest counter is the only thing that distinguishes
  slow from broken — and it puts a number in front of the person who can decide whether
  the `op` cost itself is worth attacking. It is out of scope here.
- **Its only exit is into `claude`.** Any key, the `--timeout` dead-man switch (120s), and
  even a bubbletea that refuses to start (no tty) all end in the same `syscall.Exec`. A
  holder that could exit *without* launching would leave the user staring at a pane that
  closed for no reason, or a dead pane where their session should be.
- Pressing a key launches `claude` **without** the secrets — the hint says so. That is the
  same outcome as the timeout, and the same outcome as `op` failing.
- It is a Go bubbletea program, not an ANSI hand-roll: `claudemux-head` is already a hard
  dependency of the launcher and already owns this pane's visual language (theme-agnostic
  styles, no background, the same gray). A shell `printf` loop would have to re-solve
  centering, resize, and cursor hiding, and would still not be testable.

The non-`op_env` path does not use the holder at all — there is nothing to wait for, so
the pane is spawned as `claude` directly.

## Handing the pane over

`deferred_launch` no longer types anything. Once the secrets are in the session
environment it respawns the claude pane *with the claude command*:

```
tmux respawn-pane -k -t "$claude_pane" "exec $claude_cmd"
```

A respawned pane inherits the session environment (verified), which is what the shell-pane
respawn has always relied on, so `claude` starts with the injected variables. The holder is
killed and replaced in one call; the pane's screen is cleared as part of it.

### Keeping `deferred_launch`'s safety property

The old guard sampled `pane_current_command` before and after and only acted if it had not
changed, so a pane the user had already started something in was left alone — in
particular, an impatient hand-launched `claude` was neither killed nor double-launched.

Sampling no longer works for the claude pane, and would be a real bug if kept: tmux spawns
the pane command through `default-shell -c`, so for a few milliseconds after the respawn
`pane_current_command` is *the shell*, not the holder. A sample taken then would never
match the reading 25 seconds later and the launch would be silently dropped.

The guarantee is re-established by comparing against the command the launcher **intended**
rather than one it happened to sample: `create_session` passes the holder's name
(`claudemux-head`) to `deferred_launch`, which respawns only while the pane still reports
it. The property is unchanged and now stated instead of inferred:

| Pane reports | Meaning | Action |
|---|---|---|
| `claudemux-head` | still the holder we started | respawn it as `claude` |
| `claude` / `node` | the holder already exec'd (key press or timeout) | leave alone — no double launch |
| anything else | not ours | leave alone |

Two details make this stronger than the sampling version:

- The user cannot accidentally start something in this pane any more — there is no shell
  in it to type into — so the case the old guard defended against is mostly designed out.
  The guard remains because the holder's own escape hatches (key, timeout) can still turn
  the pane into `claude` before the injector returns.
- A **crashed** holder still reports `claudemux-head` (dead panes keep their command), so
  the respawn fires over the corpse and the session still gets its `claude`, with secrets.

The shell pane keeps the original before/after sampling: it really is a shell, its command
is stable from the moment it spawns, and "the user is running something in it" is a live
possibility there.

## Failure behavior

Everything degrades to "the pane still becomes claude, and something says why".

| Failure | Behavior |
|---|---|
| `op environment read` fails | Unchanged: `display-message` says so, the respawn still fires, claude starts without secrets |
| `op` missing entirely | `inject_op_env` returns non-zero, same as above |
| The whole background job dies (terminal closed mid-wait) | The holder's 120s timeout fires and execs `claude` itself, without secrets |
| User is impatient | Any key execs `claude` immediately, without secrets |
| `claudemux-head` not on `PATH` | The claude pane dies at 127 and is kept by `remain-on-exit failed` showing the error; the respawn still fires over it, so claude launches anyway. The head pane shows the same error instead of vanishing |
| `claude` not on `PATH` | Dead pane, exit 127, error visible. Previously: the same message next to a shell prompt |
| `claude` exits non-zero | Pane is kept dead with its output; `listPanes` filters it out so the head does not bind to it |
| `claude` exits cleanly | Pane closes (it no longer falls back to a shell — see Known gaps) |
| tmux < 3.2 | `remain-on-exit failed` is rejected and ignored; failing panes vanish, as they do today |
| No tty for the holder (not attached, weird terminal) | `tea.Program.Run` errors and the holder execs `claude` regardless |

## Testing

The visual result cannot be asserted non-interactively, and this spec does not pretend
otherwise. What is testable was tested:

- **tmux behavior was verified against a live server** on a private socket, not assumed:
  command panes draw no prompt; `respawn-pane -k` inherits the session environment, clears
  scrollback, and resets the alternate screen; `set -p remain-on-exit failed` is accepted;
  a dead pane keeps `pane_current_command` and is excluded by
  `-f '#{==:#{pane_dead},0}'`; a `%q`-quoted argument survives tmux's shell splitting
  intact; `fish`/`zsh`/`bash` all exec a simple `-c` command so `pane_current_command`
  names the program.
- **Go**, in `boot_test.go`, in the style of `tabreset_test.go` — pure functions over
  synthetic inputs: argument splitting at `--` (including flags after it staying in the
  command), the elapsed-time formatter, the view renderer at several sizes and states, the
  key/tick transitions, and the exec failure path (the success path replaces the process
  and cannot be run from a test).
- **Shell**: `bash -n` and `shellcheck` (no new findings against the pre-change baseline).
  `bin/claudemux` executes at load, so it cannot be sourced into a test harness — instead
  it was **run end to end against a throwaway tmux server** (`TMUX` unset and a short
  `TMUX_TMPDIR`, a fake `HOME`, and a `PATH` holding a fake `claude` that prints its argv
  and a fake `op` that sleeps then prints one variable). That run confirmed: the claude
  pane holds on `claudemux-head` while the holder draws; the head pane is 4 rows and stays
  4 across a `resize-window`; `set-titles`/`set-titles-string '#W'`/`status-left-length`
  are unchanged; the handover produces `--permission-mode auto -n 'E2E Demo' '/color blue'
  --worktree` with **`--worktree` still last** and the injected variable present in
  claude's environment; the non-`op_env` path spawns `claude` with no holder at all; a key
  press before the injector returns produces exactly one launch (without secrets) and the
  guard then leaves the pane alone; and with `claudemux-head` removed from `PATH` both
  command panes die visibly at 127 while the claude pane is still respawned into `claude`
  with its secrets.

Not verified without a human at a terminal: that the holder looks right, and that the
handover from holder to `claude` has no visible flash.

## Documentation

README gains a note in **The pane model** that the head and claude panes run their
programs directly (so exiting `claude` closes its pane rather than dropping to a shell),
and the dependency table's `claudemux-head` row is corrected — the failure is now a dead
pane with the error, not a shell that reports command-not-found.

## Out of scope

- **The `op` cost itself.** 25 seconds to decrypt one variable is worth attacking —
  `op read` on a single secret reference, or reusing the FIFO the head already reads at
  `~/.config/claude-env/env` — but that is a decision about where secrets come from, not
  about startup UX, and it changes what `.project.yml` means. This work makes the cost
  visible and survivable, not smaller.
- Prefetching or caching secrets across sessions.
- Any change to the shell pane.
- Attach-time behavior: attaching an existing session already shows no shells and is
  untouched.

## Known gaps

Real, traced, and not worth blocking on.

- **The claude pane no longer falls back to a shell when `claude` exits cleanly** — it
  closes. This is a behavior change for anyone who used `/exit` as a way to get a shell in
  the big pane; the 30% shell pane is right there, and the teardown spec already treats
  the leftover shell as a defect. A non-zero exit keeps the pane (with its output) instead.
- **rc-provided runtime environment is not restored.** Resolving the binaries by absolute
  path fixes the lookup, but a `claude` that needs an interactive-shell-only `PATH` entry
  at *runtime* (an nvm-managed `node`, a shim directory) now runs without it. The pane
  command inherits the tmux server's environment, which on a normal desktop is the user's
  shell environment anyway. If this bites, the fix is `new-session -e PATH=...` (tmux
  >= 3.2), not a return to typing into shells.
- **A key press and the injector's respawn can race.** If the user presses a key at
  ~24.9s, the holder execs `claude` just as `deferred_launch` samples the pane. The guard
  makes the outcome safe in both orders (either the respawn is skipped, or it replaces a
  `claude` that is milliseconds old), but in the second case a just-started `claude` is
  killed and restarted. The window is milliseconds wide and nothing is lost.
- **The holder cannot be cancelled.** There is no key that leaves the pane empty, by
  design: the invariant is "this pane becomes claude". `ctrl+c` launches claude like any
  other key, which is unusual enough to be documented in the hint line.
- **`--expected 25s` is a measurement, not a promise.** It is hardcoded in the launcher
  and will drift with `op`'s performance. It is rendered as "about 0:25" and the elapsed
  clock keeps counting past it rather than pinning at 100%, so drift reads as slow, not
  broken.
