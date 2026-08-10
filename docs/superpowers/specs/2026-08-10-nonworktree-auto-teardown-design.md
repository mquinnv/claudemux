# Non-worktree auto-teardown design

2026-08-10

## Problem

`autoArmTeardown` deliberately declines sessions that are not in a worktree
(`tui.go` auto-arm, reason `not-a-worktree`), so typing `/done` in a plain
session does nothing — the session survives its own wrap-up. Both of the
user's 2026-08-10 attempts hit this: their sessions had never entered a
worktree, and the decline is invisible (debug log only).

The original rationale: for a worktree session, "the worktree disappeared" is
proof the wrap-up succeeded; without it the gate reduces to "turn ended",
which is NOT success evidence — a `/done` that bails (uncommitted work,
declined confirmation, unpushed branch) also ends its turn, and killing the
session then would destroy the user's chance to see and fix the problem.

## Decision (user-approved)

Auto-arm everywhere. For non-worktree sessions, replace the missing
worktree-gone evidence with the same checks `/done` itself performs:

- **Gate for auto-armed, non-worktree teardowns:** turn ended (`StateIdle` —
  `teardownTurnEnded` already blocks Thinking/Tool/Compacting, so a pending
  AskUserQuestion, which classifies as `StateTool`, cannot open the gate)
  AND the session's working tree is clean AND its branch has no unpushed
  commits. Probed off the poll loop via the existing teardown probe
  machinery (`teardownProbeCmd` pattern), never inline.
- A blocked gate shows the existing blocked chip with a reason
  (`dirty tree`, `unpushed`) and keeps probing at the blocked cadence; it
  never kills. The user resolves the state or aborts with `esc`.
- The `x`-flow (manual) is unchanged. Worktree sessions are unchanged.

## Mechanics

- `autoArmTeardown`: remove the `teardownInWorktree` early-decline. Arm with
  `teardownAuto = true` as today; `captureTeardownTarget` already records
  `teardownWorkDir` and `teardownInWorktree` for the gate to branch on.
- Gate: where the teardown state machine consults the worktree-gone probe for
  worktree sessions, an auto-armed non-worktree teardown instead consults a
  new probe result: `git status --porcelain` empty AND
  `git log @{upstream}..HEAD` empty (no upstream counts as *blocked*, reason
  `no-upstream` — a branch that cannot have been pushed is not provably
  wrapped up; do not kill).
- Manual (`x`) non-worktree teardowns keep today's behavior (turn end only),
  because the user is present and pressing keys; only the auto path gains
  the stricter gate. `teardownAuto` distinguishes them.
- Decline/abort/blocked transitions log via `teardownLogf` as today.

## Testing

Pure-logic tests in the existing style: auto-arm no longer declines
non-worktree sessions; the auto+non-worktree gate refuses on dirty/unpushed/
no-upstream probe results and opens on clean; manual path unaffected;
AskUserQuestion-pending (StateTool) keeps the gate shut. Probe argv builders
tested like existing probe tests.

## Out of scope

- Detecting the wrap-up's semantic outcome from the transcript.
- Changing what `/done` itself does.
