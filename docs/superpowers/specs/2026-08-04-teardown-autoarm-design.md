# Auto-arming the teardown watch from a hand-typed wrap-up command

## Problem

The teardown sequence (see `2026-07-31-session-teardown-design.md`) is armed by exactly
one thing: pressing `x` on the status pane. But the wrap-up command is a slash command in
the `claude` pane, and the natural way to run it is to type it there — `/done`, by hand,
in the pane that is already focused.

When that happens the head sees nothing. The wrap-up runs, the worktree disappears, and
the session is left sitting in a directory that no longer exists — precisely the state the
teardown feature exists to clean up. The only way back is to press `x`, which **re-types
the wrap-up command and runs the whole thing a second time**: a second confirmation, a
second pass over an already-removed worktree, a second Linear update.

The two routes into the same wrap-up should converge on the same state machine. Typing the
command by hand is the first `x` press; the head should treat it as one.

## Interaction

Nothing new to press. In a worktree session, when the wrap-up command appears in the
transcript as a user prompt, the head arms the same teardown watch `x` would have armed,
skipping only the step that types the command (it has already been typed).

The status pane shows `⏻ watching your wrap-up…` while it waits, then behaves exactly as
the `x` path does: `⏻ press x to tear down` once the gate opens, `⏻ worktree still
present` if the wrap-up bailed, `esc` to cancel.

The waiting chip has its own wording deliberately. In the `x` path the user pressed a key
and knows a teardown is armed; here they pressed nothing, so without a distinct chip the
first sign of an armed kill-session would be `press x to tear down` appearing unbidden.
Past the wait the chips are shared: `press x to tear down` and `exiting claude…` describe
the action, not who started it, and `worktree still present` means the same thing however
the watch was armed.

## Recognizing the command

Claude Code canonicalizes slash commands. A user who types `/done` has
`/ameriglide-core:done` recorded in the transcript, and a different machine with a
different plugin would record something else again. Matching `teardown.command`
(`/done`) as a whole string would therefore almost never fire in practice.

The match is on the **last segment** — everything after the final `/` or `:` — of both
sides:

| Prompt | `teardown.command` | Arms |
|---|---|---|
| `/done` | `/done` | yes |
| `/ameriglide-core:done` | `/done` | yes |
| `/anyplugin:done` | `/done` | yes |
| `/ameriglide-core:done --force` | `/done` | yes |
| `/done-something` | `/done` | no |
| `/undone` | `/done` | no |
| `please run /done for me` | `/done` | no |
| `scripts/done` | `/done` | no |
| anything | `""` | no |

The segment is the command's own name, which is invariant across spellings; a prefix or
substring test would fire on `/done-something`, a different command entirely.

Three narrowings keep the rule from over-firing, because a false positive here arms a
sequence that ends in `kill-session`:

- **Both sides must begin with `/`.** Otherwise a prose prompt containing a path
  (`scripts/done`) reduces to the same segment. A prompt is a command or it is not.
- **Only the prompt's first whitespace-delimited token is considered**, so `/done --force`
  still arms — slash commands take arguments, and an argument does not make it a
  different command.
- **An empty `teardown.command` matches nothing.** It already means "no wrap-up to run";
  it cannot then mean "every prompt is the wrap-up".

Matching is on `m.lastPrompt`, which is already clean by the time the model sees it:
`parseEvent` runs raw content through `cleanCommandText`, which unwraps
`<command-name>/ameriglide-core:done</command-name>` into the bare string. Nothing here
parses XML.

## Where it hooks in

`m.recomputeFromEvents(msg.time)` in the `dataMsg` case refreshes `lastPrompt` on every
poll. `autoArmTeardown` runs immediately after it, comparing the value captured before
the recompute against the value after — before the `shouldSummarize` early return, which
would otherwise skip it on exactly the busy→idle polls that most often carry a new prompt.

**It fires on the edge, not the value.** `lastPrompt` keeps a prompt until a newer one
lands, so a value test would re-arm on every one-second tick for as long as the wrap-up is
the newest prompt — which also makes `esc` useless, since the cancelled teardown would
re-arm on the following tick. Comparing against the previous poll's value arms exactly
once per submission.

The cost is that a second identical wrap-up typed back-to-back produces no edge and does
not re-arm. `x` still works for that, and re-running a wrap-up that already succeeded is
not the case worth optimizing.

`switchSession` is deliberately not a hook point. It recomputes `lastPrompt` against a
different transcript, so arming from it would arm off another session's history — the same
staleness that makes a rotation abort an armed teardown today.

## Preconditions

Only from `teardownIdle`. A teardown already in flight has its own submission evidence and
its own deadlines; re-arming would reset `teardownAt` and discard them. This also covers
the `x` path seeing its own wrap-up land in the transcript a moment after sending it —
`teardownSent` is not idle, so the observation is ignored.

Only in tmux (`selfPane != ""`), for the same reason `x` is inert there: there is no
session to kill.

**Only for worktree sessions.** This is load-bearing, not conservatism. A hand-typed
wrap-up is submitted the instant it is observed, so the watch starts with
`teardownSubmitted` already true and the ready gate reduces to `teardownGateOpen`'s
remaining conditions. For a session that is not in a worktree that is *turn-end alone* —
the gate would open seconds after the wrap-up finished, and a single stray `x` would then
send `/exit` and kill a session nobody armed. In a worktree the gate additionally requires
the worktree to be gone, which is real evidence the wrap-up succeeded and the work is
over. Non-worktree sessions keep `x` and nothing else.

The target directory is captured through the same `captureTeardownTarget` the `x` path
uses, so the gate watches the session's cwd rather than the head's. When it reports a
non-worktree target the capture is rolled back rather than left half-written: the fields
are only read from `teardownSent`, but "captured" and "armed" should mean the same thing.

## Arming state

Identical to the `x` path except for three fields:

| Field | Value | Why |
|---|---|---|
| `teardownSubmitted` | `true` | The prompt in the transcript **is** the submission — it only got there because claude accepted it. Left false, the 10s `teardownSubmitTimeout` would run against an already-running wrap-up and abort it with `⏻ wrap-up didn't submit`. |
| command issued | none | Nothing to send. `teardownSendCmd` here would re-type the command — the duplicate run this feature exists to prevent. |
| `teardownAuto` | `true` | Drives the waiting chip only. Cleared by `abortTeardown` and by an `x` arm. |

`teardownArmedBusy` is set false: it exists to stop a pre-existing busy state from
certifying a submission, and there is no submission left to certify.

## Failure behavior

Unchanged from the `x` path once armed — same gate, same deadlines, same aborts, same
`esc`. The only new failure modes are of the "did not arm" kind, and all of them degrade
to today's behavior:

| Failure | Behavior |
|---|---|
| Command not recognized (renamed, aliased, wrapped) | no arm; `x` works as before |
| Session not in a worktree | no arm, by design; `x` works as before |
| Not inside tmux | no arm, same as `x` |
| Wrap-up bails (dirty tree, declined) | gate stays shut, `⏻ worktree still present`, exactly as with `x` |
| Prompt arrives while a teardown is armed | ignored |
| User cancels with `esc` | idle, and the unchanged `lastPrompt` does not re-arm |

There is no new subprocess and no new message type: arming is pure model mutation inside
the existing `dataMsg` handler.

## Testing

Table tests beside the existing teardown ones:

- `teardownCommandTyped` over all three positive spellings, both near-miss negatives
  (`/done-something`, `/undone`), prose, a path, arguments, and an empty configured
  command.
- Model-level arming driven through `Update(dataMsg{…})` with a synthetic user event, so
  the hook point itself is covered rather than just the helper: the positive spellings
  arm, the negatives do not, no command is issued, `teardownSubmitted` and `teardownAuto`
  are set, and the gate target is the session's worktree.
- The non-worktree case, asserting both that it does not arm and that it leaves no
  half-captured target.
- The already-armed case across all three non-idle phases, asserting `teardownAt` did not
  move.
- The no-re-arm case: three further polls with no new prompt leave `teardownAt` alone, and
  an `esc` cancel is not undone by the next poll.
- The chip renderer for the auto variants, including that a blocked reading and the ready
  phase are unchanged.

## Documentation

The README's **Tearing down a session** section no longer presents `x` as the only way in:
it opens with the two routes, states the worktree-only restriction on the automatic one
and why, and names the `⏻ watching your wrap-up…` chip.

## Out of scope

- Auto-arming non-worktree sessions. See the preconditions — the gate has nothing to
  verify there.
- Arming off anything other than a user prompt (a hook, a tool call, a shell invocation of
  the wrap-up script).
- Anything past the gate. The kill still needs a human `x`.

## Interaction with the previous design's known gaps

Checked against `2026-07-31-session-teardown-design.md` § Known gaps:

- **No generation counter on teardown messages.** Unchanged. This feature issues no new
  commands and adds no new messages, so there is no new staleness class. The monotone
  argument that makes the existing gap safe still holds.
- **A late `teardownSentMsg` can abort a teardown already in `teardownExiting`.**
  Unchanged, and slightly *less* reachable: an auto-armed teardown never issues a
  `teardownSendCmd` for the wrap-up, so the only send it can have in flight is the
  `/exit` from its own second press.
- **`session rotated` also aborts during `teardownExiting`.** Unchanged. Auto-arming does
  not hook `switchSession`, deliberately.
- **`model.inWorktree` is write-only.** Still true — this feature goes through
  `captureTeardownTarget`, which recomputes the fallback itself, so it neither uses nor
  fixes the dead field.
- **The arm's-length worktree case gets no worktree verification.** This one *does*
  interact, in the safe direction. A session whose cwd stays in the main checkout while it
  drives a worktree by explicit path reads as non-worktree to `captureTeardownTarget` —
  which under the `x` path meant the laxer gate, and here means **no auto-arm at all**.
  The user gets today's behavior (press `x`) rather than a watch resting on turn-end
  alone. That is the intended outcome, and it narrows the gap's blast radius rather than
  widening it.

One further note, not a gap: if an `x`-armed teardown aborts with `⏻ wrap-up didn't
submit` and the prompt then lands late, the auto-arm fires on that edge and picks the
watch back up. That is the right answer — the command did submit after all — and it is why
the abort path clears `teardownAuto` rather than leaving it set.
