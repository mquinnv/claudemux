# Naming worktrees after the work they hold

## Problem

Every worktree session lands in a directory named by Claude Code's random generator:
`.claude/worktrees/lovely-wandering-lovelace`, on branch `worktree-lovely-wandering-lovelace`.
Meanwhile the head has a perfectly good name for the same session — the `tab` label from
`summary.go`, a 2-5 word lowercase phrase naming the durable goal, sitting in the tmux
window title a few inches away.

The two never meet. `git worktree list` and `git branch` stay a wall of nonsense, and the
one place a real name exists is the one place it cannot be used as a path.

The name is chosen before it can possibly be right. `bin/claudemux` appends `--worktree`
to the launch command, so Claude Code names the worktree during startup — before any
prompt exists, and therefore before there is any evidence of what the session is for.

## Approach: create the worktree later, not rename it afterwards

Two ways to close the gap: name it at birth by delaying birth, or name it at birth
randomly and rename it once the topic is known.

Renaming was rejected. The name is load-bearing in three places, not one — the directory,
the branch (`worktree-<name>`), and the transcript project directory
(`~/.claude/projects/-Users-...--claude-worktrees-<name>`) — and the worktree is `locked`
with the claude pid, so a rename is `git worktree move -f` plus `git branch -m` plus a
now-orphaned project-slug directory plus a stale `pwd` in the shell pane. The head itself
would survive it (`session.go:93` finds transcripts by globbing `projects/*/<sessionID>.jsonl`,
so it never reads the slug), but Claude Code's own exit-time worktree bookkeeping records
the path at launch, and that feeds the `/done` teardown gate. Too much surface for a
cosmetic win.

So the worktree is created on the session's first response instead, once there is a prompt
to name it from.

The trade this accepts, explicitly: worktree isolation stops being a thing git enforces
from keystroke zero and becomes an instruction the model follows. If the model skips the
call, real work happens on the default branch in the shared checkout. Part 4 makes that
failure visible rather than silent.

## 1. The launcher marks the session instead of creating the worktree

`bin/claudemux` keeps all of its decision logic unchanged — `worktree_requested`, the
`-w`/`-W` precedence, `.project.yml`, `auto_worktree_wanted`. Only the action changes:

```bash
if worktree_requested "$work_dir"; then
  claude_cmd+=" --worktree"        # before
fi
```

becomes `CLAUDEMUX_WORKTREE_PENDING=1` prefixed onto the claude command, and exported into
the head's pane as well. `--worktree` is no longer passed at all, under any flag. `-w` now
means "mark this session regardless of config or repo state" rather than "create a
worktree now" — the flag's precedence and its interaction with `-n` are untouched.

Env inheritance through the tmux pane command is already proven here: `claudemux-map.sh`
depends on `$TMUX_PANE` reaching hook subprocesses by the same route.

Removing the append also retires the constraint documented above it — that `--worktree`
must be the last token because `--worktree [name]` would otherwise swallow the `/color X`
prompt argument as a worktree name. Future appends to `claude_cmd` are no longer ordered.

Consequence worth naming: a session opened and never prompted now gets no worktree at all,
where today it would have one. That is the deferral working, not a bug.

## 2. A hook injects the instruction on the first prompt

A new script, `claudemux-worktree.sh`, registered on `UserPromptSubmit`.

It is a separate file from `claudemux-map.sh` rather than a branch inside it. That file's
header states its contract — "MUST stay silent on stdout: UserPromptSubmit stdout is
injected into the model's context" — and putting a deliberately-speaking path inside it
invites exactly the bug the header warns about. Here, speaking on stdout is the whole
point, so the two contracts should not share a file.

The script writes its instruction only when both hold:

- `CLAUDEMUX_WORKTREE_PENDING` is set, and
- the payload's `cwd` is not already inside a linked worktree.

Every other path exits silently. The second condition makes it self-limiting: once
`EnterWorktree` fires the session's cwd is a worktree, and the hook goes quiet for the
rest of the session with no state to track.

The instruction asks for `EnterWorktree` before any other tool call, with a name that is a
2-5 word lowercase dash-separated slug of the task. That phrasing deliberately matches the
`tab` field's description in `summary.go`, so the two names are drawn to the same target,
and it satisfies `EnterWorktree`'s own constraint (letters, digits, dots, underscores and
dashes; 64 characters).

The model picks the name, not the script. Mechanical slugification of a first prompt like
"hey, i wish my worktrees were named the same thing as the title" yields
`hey-i-wish-my-worktrees` — worse than what it replaces.

Known weakness: `EnterWorktree`'s description directs the model to use it only when
"explicitly instructed to work in a worktree — either by the user directly, or by project
instructions." Hook-injected context should read as project instruction, but it carries
less authority than the user typing "use a worktree." This is the compliance risk the
approach accepts, and the reason part 4 exists.

## 3. Registering a second hook

`hook.go` currently installs exactly one script (`hookScriptName`) on two events
(`hookEvents`). It generalizes to a list of script/event pairs: `claudemux-map.sh` on
`SessionStart` + `UserPromptSubmit` as today, `claudemux-worktree.sh` on
`UserPromptSubmit`.

The existing guarantees carry over unchanged and are the reason to extend this code rather
than write beside it: settings are read before anything is copied, so a malformed
`settings.json` leaves the operation a complete no-op; entries are appended, never
replaced, so another tool's hooks survive; unmodelled keys round-trip through the generic
map; a backup is written before any change. The exit-code contract with `install.sh` and
`bin/claudemux` (0 present-or-installed, 2 usage, 3 unparseable, 4 I/O) does not change.

The registered command stays a path to a script copied into `~/.claude/hooks`, for the
reason already documented at `hook.go:81-83`: pointing settings.json at the binary would
bake a Homebrew libexec path into it and break on upgrade. It also cannot be a bare
`claudemux-head` on PATH — `bin/claudemux` documents at length that a tmux pane command
runs under a non-interactive shell whose PATH may predate `~/.local/bin` entirely.

## 4. Head: where the tab comes from

When the head observes a session *transition* from the main checkout into a worktree, it
renders the tab as that worktree's name with dashes turned back into spaces.

The transition is already observable for free. `tui.go:314` recomputes `m.inWorktree` from
the session's transcript cwd on every poll rather than from launch state, so a mid-session
`EnterWorktree` is picked up with no new plumbing — and the same is true of the worktree
chip and the teardown gate, neither of which needs to change.

Gating on the transition rather than on `inWorktree` is what protects the tab from
regressing. A session that started *already* in a worktree — an older one, or one made by
hand — keeps Haiku's tab instead of rendering `lovely wandering lovelace`, which would be
strictly worse than today.

Haiku's `tab` takes over permanently the first time the summarizer's `Topic` changes from
an already-established non-empty value. That is the "materially different" test, and it
reuses the stability rule already in the system prompt — "If a previous topic is given,
KEEP IT VERBATIM unless the human has genuinely changed goals" — rather than inventing a
string-similarity metric. The first summary establishing a topic is not a change; only a
later one replacing it is. The latch is one-way: once Haiku wins, it keeps the tab.

The worktree name is not renamed to follow such a correction. In the minority of sessions
where the first prompt misread the goal, the directory keeps a name derived from real work
— still far better than `lovely-wandering-lovelace` — and the tab, which is the thing
being read continuously, is right.

Degradation: restarting the head (`R`) loses the memory of the transition, and the tab
falls back to Haiku's label. Graceful, and preferable to persisting more state.

## 5. Head: the warning chip

`CLAUDEMUX_WORKTREE_PENDING` reaches the head's pane too. When it is set and the first turn
has ended with the session still outside a worktree, the worktree chip slot shows
`⚠ no worktree`. It clears the moment a worktree appears.

"The first turn has ended" is: the head has seen at least one genuine user prompt, and the
state is not `StateThinking`, `StateTool`, or `StateCompacting` — the same test
`teardownTurnEnded` applies, for the same reason. Requiring a prompt first is what keeps
the chip off a session that is merely sitting at an empty input, where nothing has been
skipped yet.

This is the whole mitigation for the accepted risk. Without it, a skipped `EnterWorktree`
call means editing the default branch in the shared checkout with nothing on screen saying
so; with it, the failure is as visible as the success.

Sessions launched `-W`, or in a repo state `auto_worktree_wanted` declines, carry no marker
and so never show the chip — correctly, since nothing was promised.

## Testing

The head changes are ordinary Go: the tab-source latch and the chip are pure functions of
observed state, testable in the style of `tui_test.go` and `tabreset_test.go`.

The hook script has no natural home — every test in this repo is Go. It is driven from a
Go test with `exec.Command`, feeding hook payloads on stdin and asserting on stdout: silent
for no marker, silent for a cwd already in a worktree, and the instruction exactly once for
a marked session in a main checkout. That is a better fit than keeping the script thin
enough to eyeball, because "stays silent in every path but one" is precisely the property
that breaks quietly.

`hook.go`'s generalization is covered by the existing `hook_test.go` patterns, extended to
assert both scripts land and that a settings file already carrying one of them is not
duplicated.

## Not doing

- Renaming a worktree, ever — see the approach section.
- Renaming the branch to follow a corrected tab.
- Any change to the teardown feature. It reads `m.inWorktree` and the session cwd, both of
  which already track a mid-session worktree entry.
