# Branch and worktree chips — execution archive

2026-08-13

The working record of the branch that split `⎇ <worktree>` into `⎇ <branch> · ⌂ <worktree>`.
Kept for one reason: the width arithmetic under those chips produced the same
defect five times, three of them on this branch, and the record of *how* it kept
surviving is more useful than the diff that finally fixed it.

| File | What it holds |
|---|---|
| `progress.md` | The ledger: every task, review, fix round, parked residual and ruling. |
| `final-fix-report.md` | The wave answering the final whole-branch review. |

Not archived: the `review-*.diff` packages (reproducible with `git diff`) and the
four per-task briefs and reports.

## The lesson worth carrying

**Arithmetic on remaining room is rune-count logic, however it is spelled.**

The chip ladder needs to answer "will a real character of the name survive
truncation?" Every wrong version of it computed that answer from the budget:

```go
if room := avail - bw - sepW; room > bareW+2 {        // wrong
```

That reserves cells as though every character costs one. A two-cell CJK
character needs one more, slips through the guard, and `ansi.Truncate` then
returns a glyph and an ellipsis with no name at all — a chip claiming elided
content that does not exist. The working version does not compute the answer, it
measures it:

```go
t := ansi.Truncate(chip, room, "…")
return t, lipgloss.Width(t) > lipgloss.Width(glyph)+1   // right
```

**And ASCII fixtures cannot test display-width logic.** This is why the bug
survived a fix round that was, on its own terms, done properly: that round
produced genuine mutation evidence — guards reverted, tests observed failing,
guards restored — and every fixture involved was ASCII, where rune count and
display width agree. The evidence was real and proved nothing about the
assumption underneath. A mutation test is only as good as the inputs it mutates
against.

Three tests on this branch could not fail for the reason they existed:

- an expected-output table with two rows a cell over their own budget, which the
  implementation matched;
- a ladder whose `avail` values never landed on the boundary where a real bug
  lived;
- a wide-rune test asserting only line width — the one property that was never
  broken — so it passed with both wide-rune defects fully present, at exactly the
  CJK inputs it was written to cover.

Each was found by a reviewer that ran the code or worked the arithmetic by hand,
never by one that read the test and agreed with its name.
