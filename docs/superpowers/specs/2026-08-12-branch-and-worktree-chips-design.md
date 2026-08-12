# Branch and worktree chips design

2026-08-12

## Purpose

The head's status line shows one chip, `⎇ <worktree>` — a branch glyph in front of a
worktree name. The branch itself is nowhere in the UI, and the two diverge constantly:
in a single session on 2026-08-12 the worktree stayed `align-context-meters` while the
branch went `main` → `worktree-align-context-meters` → `fix/1-env-fifo-flake` →
`fix/2-testdata-redaction` → detached → `lobby-preview` → detached. The pane reported the
same string throughout.

Show both, and let the branch glyph mean branch.

## The chips

```
● Thinking 0:06 · opus-5 · ⎇ lobby-preview · ⌂ align-context-meters
```

`⎇` moves to the branch, which is what it has always meant everywhere else. The worktree
takes `⌂`. Branch first: it changes more often and it is the thing you act on.

## Where the branch comes from

The transcript, not git. Every entry carries `gitBranch` at the top level, beside the
`cwd` the worktree chip already reads:

```json
{"type":"assistant","cwd":"/Users/michael/Projects/example","gitBranch":"lobby-preview", …}
```

So this is the `lastMainCwd` shape exactly (`tui.go:402`): scan the ring newest-first,
take the first non-empty value from a **non-sidechain** entry, and return the previous
value unchanged when the ring holds none.

Three properties come free from copying that pattern, and all three are load-bearing:

- **No subprocess.** A `git` call per poll, per session, is latency the head does not
  need and a failure mode it does not want.
- **Sidechain entries are skipped.** A subagent working in a different worktree must not
  hijack the chip — the same reason `lastMainCwd` filters them.
- **A quiet poll keeps the last known value.** A tail of pure subagent activity, or a
  not-yet-seeded ring, must not blank a chip that was correct a second ago.

`sessionEntry.GitBranch` in `session.go:22` is dead code — declared, never read. It
belongs to the sessions *index*, not the transcript, and is not the source here. Delete
it or leave it; it is not part of this design.

## Degradation

The worktree chip never disappears while the session is in a worktree; it degrades to its
glyph. The branch chip keeps its name or goes.

The asymmetry is deliberate: a bare `⌂` carries information — *you are in a worktree* —
while a bare `⎇` carries none, since a session is always on some branch.

| Room | Renders |
|---|---|
| plenty | `⎇ lobby-preview · ⌂ align-context-meters` |
| less | `⎇ lobby-preview · ⌂ align-cont…` |
| less | `⎇ lobby-preview · ⌂` |
| least | `⌂` |

Order of sacrifice, tightest last: the worktree name truncates, then the worktree name
drops to the bare glyph, then the branch name truncates, then the branch drops entirely,
leaving `⌂`. The state and model text never shrink — unchanged from today.

All truncation is display-width aware (`ansi.Truncate`), not rune count. A CJK branch
name clipped to N runes measures 2N cells and overruns the line; this file has already
been fixed for that once.

## The no-worktree warning is unchanged

`worktreeChipText` keeps its current rules: a real worktree always wins, and
`⚠ no worktree` shows only for a session that was marked as wanting one, whose first turn
ended, and which has seen a real prompt. It keeps its current placement too — in the left
group with the state and model text rather than the chip slot — because it is the only
visible mitigation for a risk the worktree design accepts, and must not be the first
thing clipped.

Under maximum pressure it degrades to a bare `⚠` on the same principle as the bare `⌂`.

**The warning and the branch chip coexist.** They occupy different slots — the warning
sits in the left group, the branch in the chip slot — so a marked session that never got
its worktree renders `● Idle 0:03 · opus-5 · ⚠ no worktree · ⎇ main`. Knowing which
branch it is sitting on is exactly what you want when deciding what to do about it. There
is no `⌂` in that state because there is no worktree to name.

## Edge cases

| Case | Renders |
|---|---|
| detached HEAD | `⎇ detached` — the transcript records the literal string `HEAD`, which would read as a branch named HEAD |
| `gitBranch` absent (not a git directory) | no branch chip; worktree chip unchanged |
| plain checkout, no worktree | `⎇ main` alone; plus the warning in the left group when its own rules fire |
| session rotation (`switchSession`) | the branch resets with the rest of the derived state, then re-derives on the next poll |

## Scope

The head's status line only, in both layouts — the split state line (`renderStateLine`)
and the packed single-line fallback (`renderStatusbar`) — since both assemble the same
chip today and would otherwise disagree.

The lobby rows are **out of scope**. They sit on a fixed column grid, and a second
variable-width field there is a separate decision with its own layout consequences.

## Testing

- `lastGitBranch` gets `lastMainCwd`'s table: a plain value, a sidechain entry ignored,
  an empty poll keeping the previous value, an unseeded ring, and `HEAD` mapping to
  `detached`.
- Chip assembly gets a width-ladder test asserting every rung of the degradation table,
  including that the bare `⌂` survives at the narrowest width and that the state and
  model text never shrink.
- A wide-rune branch name asserts the line still measures at or under the pane width —
  the regression this file has had before.
- The existing no-worktree warning tests must pass unchanged.
