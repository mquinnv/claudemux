# Final whole-branch review — fix report

Branch: `branch-worktree-chips`. Base for this pass: `c57c306`.

All findings from the final review addressed in one pass. Full suite green;
`go vet` clean; `gofmt` clean.

---

## Root cause (both Criticals)

`chipSegment`'s rungs decided "will a real character of the name survive
truncation?" by integer arithmetic on the remaining room:

```go
tui.go:1512   if room := avail - bw - sepW;   room > bareW+2 { ... }
tui.go:1523   if room := avail - sepW - bareW; room > lipgloss.Width(branchGlyph)+1 { ... }
tui.go:1546   (fitChip) if avail >= 2 { ... }
```

That is rune-count logic wearing a display-width costume — the fifth instance
of the bug class in this file. It over-counts for wide runes (a two-cell first
character needs one more cell than the guard reserves), and `fitChip` never
received the guard at all.

### Fix

Stop computing survival; truncate first, then MEASURE what survived. New helper
in `cmd/claudemux-head/tui.go`:

```go
// truncNamed fits chip (glyph + space + name) into room cells and reports
// whether any character of the NAME survived. Measuring the truncated result
// is the whole point: computing "will a character fit" from room alone is
// rune-count logic in disguise, and a two-cell first character defeats it.
func truncNamed(chip, glyph string, room int) (string, bool) {
	if room < 1 {
		return "", false
	}
	if lipgloss.Width(chip) <= room {
		return chip, true
	}
	t := ansi.Truncate(chip, room, "…")
	return t, lipgloss.Width(t) > lipgloss.Width(glyph)+1
}
```

- Rung 2 → `if t, ok := truncNamed(w, worktreeGlyph, avail-bw-sepW); ok { return b + chipSep + t }`
- Rung 4 → `if t, ok := truncNamed(b, branchGlyph, avail-sepW-bareW); ok { return t + chipSep + worktreeGlyphBare }`
- `fitChip` gained a `glyph` parameter (signature is now
  `fitChip(chip, glyph, bare string, avail int)`), uses `truncNamed`, and falls
  back to `bare` when it reports false. Callers pass `branchGlyph`/`""` and
  `worktreeGlyph`/`worktreeGlyphBare` respectively.

`TestChipSegmentLadder` passes **unchanged**, including both boundary rows at
avail 21 and 7 — monotonic degradation preserved.

---

## CRITICAL 1 — `fitChip` emitted a glyph plus a nameless ellipsis

**Was:** `chipSegment("lobby-preview","",2) == "⎇…"`, `(…,3) == "⎇ …"`,
`chipSegment("","align-context-meters",3) == "⌂ …"`. Reachable end-to-end in
pure ASCII: a plain checkout at pane width 27/28, and — because `switchSession`
clears `sessionBranch` — every session's first poll after a rotation.

**Now:** the branch yields `""` (a bare `⎇` carries no information) and the
worktree yields the bare `⌂`.

**Covering tests:** `TestChipSegmentSingleChipLadders` (new,
`tui_test.go`) — branch-only and worktree-only ladders walked down through
avail 40/15/14/5/4/3/2/1/0 and 40/22/21/5/4/3/2/1/0; and
`TestStateLineNeverRendersNamelessChip` (new) which sweeps widths 1–200 over
five fixtures (plain checkout, worktree-with-no-branch-yet, both, warning,
wide-rune names) asserting both that `lipgloss.Width(line) == pane width` and
that none of `⎇ …` / `⌂ …` / `⎇…` / `⌂…` ever appears.

**Command:** `go test ./cmd/claudemux-head/ -count=1 -run 'TestChipSegment|TestRenderStateLine|TestWarningChip|TestStateLine'`

Before the fix:

```
--- FAIL: TestChipSegmentSingleChipLadders/branch_only
    chipSegment(branch only, avail=3) = "⎇ …", want ""
    chipSegment(branch only, avail=2) = "⎇…", want ""
--- FAIL: TestChipSegmentSingleChipLadders/worktree_only
    chipSegment(worktree only, avail=3) = "⌂ …", want "⌂"
--- FAIL: TestStateLineNeverRendersNamelessChip/plain_checkout
    width=27: renderStateLine = " ● Idle 0:30 · opus-5 · ⎇… " contains the nameless-ellipsis form "⎇…"
--- FAIL: TestStateLineNeverRendersNamelessChip/worktree,_branch_not_yet_polled
    width=28: renderStateLine = " ● Idle 0:30 · opus-5 · ⌂ … " contains the nameless-ellipsis form "⌂ …"
--- FAIL: TestStateLineNeverRendersNamelessChip/no_worktree_warning
    width=43: renderStateLine = " ● Idle 0:30 · opus-5 · ⚠ no worktree · ⎇… " contains the nameless-ellipsis form "⎇…"
```

After: `ok github.com/mquinnv/claudemux/cmd/claudemux-head`.

---

## CRITICAL 2 — Rungs 2 and 4 defeated by wide runes

**Was:** `chipSegment("main", 12×"宽", 13) == "⎇ main · ⌂ …"` and
`chipSegment(12×"囲", "align-context-meters", 8) == "⎇ … · ⌂"`.

**Now:** `"⎇ main · ⌂"` and `"⌂"` respectively.

**Covering test:** `TestChipSegmentWideRunes`, rewritten (see next finding) with
per-rung expected strings, including a dedicated
`wide worktree beside a narrow branch` subtest that isolates Rung 2 (ASCII
branch, CJK worktree) from Rung 4.

**Wide-rune mutation evidence** — with the three old arithmetic guards restored
verbatim and `truncNamed` bypassed, at CJK inputs:

```
--- FAIL: TestChipSegmentWideRunes/both_chips
    chipSegment(avail=8) = "⎇ … · ⌂", want "⌂"
    chipSegment(avail=8) = "⎇ … · ⌂" contains the nameless-ellipsis form "⎇ …"
--- FAIL: TestChipSegmentWideRunes/wide_worktree_beside_a_narrow_branch
    chipSegment(avail=13) = "⎇ main · ⌂ …", want "⎇ main · ⌂"
    chipSegment(avail=13) = "⎇ main · ⌂ …" contains the nameless-ellipsis form "⌂ …"
--- FAIL: TestChipSegmentWideRunes/branch_only
    chipSegment(branch only)(avail=4) = "⎇ …", want ""
    chipSegment(branch only)(avail=3) = "⎇ …", want ""
--- FAIL: TestChipSegmentWideRunes/worktree_only
    chipSegment(worktree only)(avail=4) = "⌂ …", want "⌂"
    chipSegment(worktree only)(avail=3) = "⌂ …", want "⌂"
--- FAIL: TestStateLineNeverRendersNamelessChip/wide-rune_names
    width=33: renderStateLine = " ● Idle 0:30 · opus-5 · ⎇ … · ⌂  " contains the nameless-ellipsis form "⎇ …"
```

Every one of these rows passes with `truncNamed` in place. Note that
`lipgloss.Width(got) > avail` never fires in either state — which is exactly
why the previous round's mutation check (ASCII only, width-only assertions)
let Critical 2 through.

---

## Important — a test was pinning the defect

`tui_test.go:394`, `TestRenderStateLineTruncatesChipWhenNarrow`. At width 30
with a 60-char worktree name and no branch, the chip slot works out to 3 cells,
so `strings.Contains(got, "…")` was satisfied ONLY by the `⌂ …` bug. Kept the
test (its intent is right), widened the fixture to 40 and strengthened it:

- still asserts `…` present,
- now also asserts `⌂ x` — a real character of the NAME survived,
- now also asserts the line does not contain `⌂ …`.

Probe run against the fixed code, showing why the fixture had to move:

```
width 30 (the old fixture) renders " ● Idle 0:00 · opus 4.7 · ⌂   "; contains ellipsis = false
width 40 (the new fixture) renders " ● Idle 0:00 · opus 4.7 · ⌂ xxxxxxxxxx… "
```

i.e. the original assertion would now fail at its original width — it was
green only because of the bug.

---

## Important — a test that could not fail for the reason it existed

`tui_test.go:3394`, `TestChipSegmentWideRunes` asserted only
`lipgloss.Width(got) > avail`. Line width is the one property that is NOT
broken; it holds everywhere, so the test passed with both wide-rune defects
fully present at the very CJK inputs it was written to cover.

Rewritten with content assertions: a shared `check` helper asserting the exact
expected string per rung, the width bound, and the absence of all four
nameless-ellipsis forms. Three subtests:

- `both chips` — avail 40/30/20/10/8/6/1/0, walking Rungs 2→5 with 12 CJK runes
  on both sides;
- `wide worktree beside a narrow branch` — avail 20/14/13/10, isolating Rung 2;
- `branch only` / `worktree only` — avail 7/5/4/3, isolating `fitChip`.

---

## Important — `fitChip`'s degradation was entirely untested

`tui_test.go:3378`, `TestChipSegmentSingleChips` exercised both single-chip
paths at avail=40 only, where nothing degrades — which is why Critical 1
shipped. Left it in place (it documents the undegraded shape) and added
`TestChipSegmentSingleChipLadders`, an avail ladder for each single-chip path
asserting the bare glyph (worktree) or empty (branch) rather than a nameless
ellipsis, plus the wide-rune single-chip subtests above.

---

## Minor — a fourth pre-existing test whose meaning changed

`tui_test.go:419`, `TestWarningChipHasNoBranchGlyphAndSurvivesNarrowWidth`.
`!strings.Contains(got, "⎇")` used to mean "the warning is not glyph-prefixed";
it now only means "this fixture has no branch", and held solely because
`sessionBranch` was empty. Retargeted at the warning's own prefix:

```go
if strings.Contains(got, branchGlyph+noWorktreeWarning) { ... }
```

and added `sessionBranch: "main"` to the fixture plus a positive
`strings.Contains(got, "⎇ main")` in both the `renderStateLine` and
`renderStatusbar` subtests, so the test now also proves the coexistence the
spec requires — warning in the never-shrunk left group, branch chip rendering
beside it.

---

## Minor — three stale comments

- `tui.go` `View()`: "worktree chip included, truncated to 24 runes" → "branch
  and worktree chips included, capped at packedChipCells display cells".
- `tui.go` `noWorktreeWarning` doc: the sentence argued that the warning is not
  prefixed with "the `⎇ ` branch glyph", which is backwards now that `⎇` means
  BRANCH and renders right beside the warning. Rewritten to say the warning
  carries its own `⚠` and takes no chip glyph (`⌂ ` would be a lie), and that
  the branch chip is unaffected.
- `tui.go` `renderStatusbar` doc: deleted the sentence "Callers pass chip == ""
  once a prompt line is rendering the worktree name instead (see View); the
  branch half still renders from m.sessionBranch regardless." Confirmed false —
  the function has exactly one caller, the `height <= 1` path in `View`, which
  always passes `m.worktreeChip()`.

---

## Deliberately NOT fixed

- **Ledger's deferred minor: `fitChip` "wastes a cell" at avail==2.** Rejected
  as misdiagnosed, per the reviewer. At avail==2 the only way to spend the full
  budget is `⌂…` — precisely the forbidden nameless-ellipsis form. The unspent
  cell is correct. A comment now says so at the fallback in `fitChip`, and
  `TestChipSegmentSingleChipLadders` pins `chipSegment("", w, 2) == "⌂"` with
  an inline note, so a future reader cannot optimize it away silently.
- **`lastGitBranch` keeping a stale branch when a session leaves a git repo.**
  Inherited from `lastMainCwd`, which the spec endorses copying. Untouched.

Also untouched, as instructed: the lobby, `isWaiting`, the conductor. No new
module dependencies.

---

## Verification

| Command | Result |
| --- | --- |
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `gofmt -l cmd/claudemux-head/` | no output |
| `go test ./... -count=1` | `ok github.com/mquinnv/claudemux/cmd/claudemux-head 3.359s` |

The pane-width invariant is re-guarded in code, not just by hand:
`TestStateLineNeverRendersNamelessChip` brute-forces widths 1–200 across five
fixtures (including CJK branch AND worktree names) asserting
`lipgloss.Width(line) == m.width` on every one, so a future regression of the
"line exceeds the pane" class fails in CI rather than in a reviewer's terminal.
