# Lobby pane preview — execution archive

2026-08-12

The working record of the branch that added the lobby's preview box. Kept for the
same reason as its sibling archive: the evidence here came from probing behavior
across hundreds of pane geometries and from mutation-testing the tests themselves,
and none of it is reconstructible from the diffs.

| File | What it holds |
|---|---|
| `progress.md` | The ledger: every task, review, fix round, parked residual and ruling. Read this first. |
| `final-fix-report.md` | The wave answering the final whole-branch review. |

Not archived: the `review-*.diff` packages (reproducible with `git diff`) and the six
per-task briefs and reports, which are routine.

## What this branch is worth remembering for

**A green suite proved nothing twice, in the same specific way.** Two separate tests
here could not fail for the reason they existed:

- the box-title truncation test used an ASCII title, where rune count equals display
  width — so it passed identically whether the code truncated by runes or by cells,
  which was the entire point of the change it was guarding;
- the row-budget test counted `\n` separators rather than lines. A view of N lines has
  N−1 of them, so it permitted exactly one row of overflow — and deleting the row cap
  produced a 17-line view in a 16-row pane with the whole suite still green.

Both were caught by reviewers who mutated the code and watched what the tests did,
not by reading them. That technique found more here than any amount of careful
reading, and it is cheap.

**The sizing floor was wrong in the spec, not the code.** The design said a 2-row
list budget meant "one session fits". It does not: a session carrying a summary
occupies two rows, and the view reserves a third for the `… +N more` line. At a
16-row pane the lobby rendered zero sessions beneath an 8-row preview box —
decoration displacing the thing being decorated, and a regression against the
uncapped behavior it replaced. The floor is 3.

**One test was quietly constraining unrelated work.** `TestSwModelViewClipsLongName`
searched the whole rendered view for an over-long session name, intending to check
that the *name column* truncates. Because the preview box also displays a session
name, that assertion silently capped the box title at the fleet column's width —
24 cells — on a box that is full pane width. The fix was to scope the assertion to
the row it actually meant. Worth watching for: an assertion that searches an entire
rendering will constrain every future element that renders similar content.
