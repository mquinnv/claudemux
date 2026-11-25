# Conductor prefers sessions that are about to close

## Intent

When the conductor picks where to take the human next, it should choose
sessions that are one step from disappearing before anything else. A lobby
list that gets shorter is better psychologically than one worked strictly
oldest-first (Michael, 2026-11-25).

## Today

`waitingQueue` (`cmd/claudemux-head/swconductor.go`) lists every waiting
session that is not deferred or snoozed, oldest `Since` first. The closing
phase (`swSession.Closing`, mirrored from `@claudemux_closing`) is ignored
for ordering.

In a worktree session the head publishes `blocked` as soon as the wrap-up
turn rests with the worktree still standing, and a pending question counts as
resting (`teardownTurnEnded`). So a `/done` asking its confirmation shows the
`BLOCKED` badge, the same as a wrap-up that bailed and went idle. `CONFIRM`
(`wrapup` + waiting) is only a flicker before the probe lands.

## Design

### Queue order

`waitingQueue` sorts by tier, then oldest `Since`, then name:

| Tier | Rows | Why |
|------|------|-----|
| 0 | `Closing` is `ready` or `direct` | one keystroke ends the session |
| 1 | `Closing` is `wrapup` or `blocked`, and state is `Asking`, `Awaiting` or `Tool:AskUserQuestion` | the wrap-up is waiting on an answer |
| 2 | everything else | as today |

A closing session that is `blocked` + `Idle` (the wrap-up bailed) is tier 2:
it needs real work and does not shorten the list quickly. An unrecognized
closing value (a newer head's) is tier 2.

The tier is one pure function, `swCloseTier(closing, state string) int`, in
`closingpub.go`. A second, `swCloseAsking(closing, state string) bool`,
reports tier 1 and is shared with the badge.

### Not changing

- No preemption. The order matters only where the conductor already takes
  `queue[0]`; it never pulls the human out of the session they are in.
- Snooze and defer filter exactly as before. A snoozed closing session waits
  its turn; a deferred one is never conducted to.
- The status line.
- What the head publishes. The fix is entirely on the lobby side.

### Badge and label

- `swCloseBadgeLabel`: `CONFIRM` (waiting style) whenever `swCloseAsking` is
  true, for `wrapup` and `blocked` alike. `blocked` otherwise stays
  `BLOCKED`. `wrapup` + `Idle` stays `CONFIRM` as today: that flicker is a
  waiting row and the head reclassifies it within a tick.
- The row's phase text (`closingText`, used by the lobby detail line and the
  web page's `closing_label`) gains a state-aware wrapper,
  `swClosingText(closing, state string) string`, that returns
  `⏻ wrap-up needs an answer` when `swCloseAsking` is true and
  `closingText(closing)` otherwise. Both call sites switch to it.

## Approaches considered

1. **Tiered sort in `waitingQueue`** (chosen). One place, pure, covered by
   the existing conductor tests' style.
2. A config flag for the preference. Rejected: it is wanted always on.
3. Have the head publish a distinct `confirm` phase. Rejected: the lobby
   already has the state it needs, and older heads would not publish it.

## Tests

- `swconductor_test.go`: tier 0 beats tier 1 beats an older tier-2 row; age
  orders within a tier; `blocked` + `Idle` is not promoted; a snoozed closing
  row is filtered; a deferred closing row is excluded; parked dispatch goes
  to the closing row.
- `closingpub_test.go`: `swCloseTier`, `swCloseAsking`, the badge table
  (`blocked` + `Asking` → `CONFIRM`, `blocked` + `Idle` → `BLOCKED`) and
  `swClosingText`.

## Comments to update

`closingpub.go`'s header says the conductor ignores the mark on purpose; it
now orders by it. `waitingQueue`'s doc comment describes the order.
