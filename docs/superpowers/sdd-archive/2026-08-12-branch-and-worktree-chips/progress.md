# SDD ledger — plan: docs/superpowers/plans/2026-08-12-branch-and-worktree-chips.md

Branch: branch-worktree-chips, from main at ff32d33. Task 1 base: 917cf8c
(the spec and plan commits sit on this branch ahead of the merge base).
Merge base for the final review: ff32d33.
Worktree: .claude/worktrees/align-context-meters.

Pre-flight scan: clean. One deliberate deviation is already recorded in the
plan's own self-review and was surfaced to Michael before execution — the
HEAD -> "detached" mapping lives inside lastGitBranch rather than in a
separate presentation helper. Reviewers may argue with it; it is not hidden.

Task 1: complete (commits 917cf8c..7702b57, review clean)
  Reviewer confirmed the field's position against REAL transcripts, not just
  the fixture: gitBranch is a top-level sibling of cwd/type. Its ⚠️ was a
  forward reference to lastGitBranch — resolved by me, that is Task 2's
  deliverable, not a gap.
Task 2: complete (commits 7702b57..1c4b764, review clean) — reviewer confirmed
  all three regressions are caught independently (dropped sidechain filter,
  "" instead of prev on an empty scan, removed HEAD mapping), and that
  recomputeFromEvents threads m.sessionBranch through rather than "".
Task 3: implemented ae5fbd7. TWO of my plan's expected-output rows were
  arithmetically wrong (each 1 cell over its own avail); the implementer kept
  the code verbatim, corrected the table, and reported it. I verified both by
  hand and the reviewer verified them again by RUNNING the shipped code —
  corrected values are right. Plan error, not implementer drift.
Task 3: review SPEC ✅; 1 Important, 2 Minor.
Task 3: fix round 1 dispatched — off-by-one in MY ladder code, symmetric in
  two rungs: `room > bareW+1` and `room > width(branchGlyph)` admit room==3,
  but a surviving name character needs 4 (glyph 2 + letter 1 + ellipsis 1).
  Result at avail=21 is "⌂ …" and at avail=7 is "⎇ … · ⌂" — a glyph plus an
  ellipsis and no name, which implies truncated content that does not exist
  and violates the design's own no-bare-⎇ rule. Boundary tests at 21 and 7
  required with mutation evidence; the ladder table never lands on room==3,
  which is why it passed.
Task 3: minor (deferred): fitChip returns the bare glyph at avail==2 on the
  worktree-only path where it could use the full budget. Wastes a cell.
Task 3: fix round 1/5 (1 addressed, 0 open — both guards corrected, boundary
  tests at avail=21 and avail=7 added; commits ae5fbd7..d3bc122). The
  re-reviewer reverted the guards itself and watched both new subtests fail
  with the predicted buggy strings, then restored — mutation verified, not
  trusted.
Task 3: complete (commits 1c4b764..d3bc122, review clean)
Task 4: implemented 0579be7. Two concerns reported:
  (a) altered the glyph assertion in THREE pre-existing tests
      (TestViewHeightOnePacksSingleStatusbar,
      TestRenderStateLineShowsFullWorktreeChip,
      TestRenderStatusbarPinPrecedesChip) from ⎇ to ⌂ — I had forbidden
      altering existing assertions. Its case: those encoded the old glyph
      mapping this task exists to correct, and it verified by git stash that
      they passed pre-change and broke only on the intended swap. Sent to the
      reviewer to judge, including whether anything beyond the glyph moved.
  (b) skipped the live tmux step — its send-keys was blocked by the
      environment. Done by me instead, below.
Task 4: live verification on the installed binary, head %179 in session
  `claudemux`, worktree align-context-meters on branch branch-worktree-chips:
    ● Thinking 0:12 · opus-5 · ⎇ branch-worktree-chips · ⌂ align-context-meters
  Both chips present and correct against ground truth; the same line before
  this branch read "⎇ align-context-meters" with the branch invisible.
  Widest emitted line 117 cells in a 119-column pane.
  That resolves the review's ⚠️ on Step 7 — unverified by implementer and
  reviewer alike, verified by me.
Task 4: review SPEC ✅. The three altered assertions were ENDORSED — reviewer
  read each test in full, only the glyph literal moved, width and ordering
  assertions intact and still discriminating, and the swap is forced by
  chipSegment's own dispatch. My "don't alter existing assertions" rule was
  the wrong instruction for a task whose entire purpose is changing that glyph.
Task 4: fix round 1 dispatched — (a) nothing tests that the PACKED layout
  shows both chips; chipSegment("", chip, packedChipCells) in renderStatusbar
  passes the whole suite. Traces to my brief's Step 1. (b) renderStatusbar's
  doc comment still claims "truncated to 24 runes" and describes `chip` as the
  whole segment — both false after this task. Two adjacent stale-comment
  minors folded in, since comments describing a removed bug are how this bug
  class keeps coming back here.
Task 4: fix round 1/5 (2 addressed, 0 open — TestStatusbarShowsBothChips now
  covers the packed layout, fixture verified to FIT (25 cells vs a 40-cell
  budget) so it cannot pass on a degraded string; all three stale comments
  rewritten and each checked TRUE against the shipped code, not merely
  changed; commits 0579be7..c57c306). Re-reviewer applied the branch-dropping
  mutation itself and observed the predicted failure.
Task 4: minor (deferred): renderStatusbar's doc still claims "callers pass
  chip == "" once a prompt line is rendering the worktree name instead" —
  vestigial and PRE-EXISTING (predates this whole plan). The function has one
  caller, the height<=1 path, which always passes m.worktreeChip(). The fix
  round carried it forward with light rewording instead of correcting it.
Task 4: complete (commits d3bc122..c57c306, review clean)

ALL FOUR TASKS COMPLETE. Deferred minors for the final review to triage:
  - Task 3: fitChip returns the bare glyph at avail==2 on the worktree-only
    path where it could use the full budget. Wastes a cell.
  - Task 4: the vestigial "prompt line" claim in renderStatusbar's doc, above.

Final whole-branch review (ff32d33..c57c306, opus): NOT READY.
  Line-width safety VERIFIED SOUND — brute-forced both layouts over 5 branches
  x 5 cwds x 3 models x 6 states x widths 1-140, plus a warning/rate sweep at
  1-200. Zero overflows. Every defect below is about WHICH RUNG the ladder
  picks, not about overflow.
  CRITICAL 1 — fitChip never got the survival guard Rungs 2/4 received in fix
  round 1, so the single-chip paths emit glyph+ellipsis with no name:
  chipSegment("lobby-preview","",2)=="⎇…", ..3)=="⎇ …", ("","align-…",3)=="⌂ …".
  Reachable in PURE ASCII end-to-end: plain checkout at pane width 27/28, and
  with the ⚠ warning at 43/44. Also hits EVERY session's first poll after
  rotation, since switchSession clears sessionBranch to "".
  CRITICAL 2 — Rungs 2 and 4, the ones fix round 1 "fixed", are rune-count
  arithmetic in disguise: `room > bareW+2` assumes 1 cell per character, so a
  two-cell first character slips through. Fifth instance of this bug class in
  this file. My fix round's mutation evidence was real but only ever exercised
  one-cell characters — the ladder fixture is pure ASCII — which is exactly how
  this survived.
  Important — TestRenderStateLineTruncatesChipWhenNarrow is PINNING the defect:
  at width 30 its `Contains(got,"…")` is satisfied only by the `⌂ …` bug, so a
  correct ladder makes it fail. Its intent is right; its fixture drifted.
  Important — TestChipSegmentWideRunes asserts only line width, the one
  property that is not broken, so it passes with both wide-rune defects fully
  present at the very CJK inputs it exists for. Third specimen of the shape.
  Important — fitChip's degradation has ZERO coverage (single-chip tests run at
  avail=40 only, where nothing degrades). That is why Critical 1 shipped.
  Minors: a FOURTH pre-existing test whose ⎇ assertion changed meaning
  (TestWarningChipHasNoBranchGlyph...); three stale comments (tui.go:1155,
  :1768, :1313); lastGitBranch keeping a stale branch when a session leaves a
  git repo (inherited from lastMainCwd, endorsed by the spec — no change).
  LEDGER TRIAGE by the final reviewer:
    - Task 3 minor (fitChip "wastes a cell" at avail==2): REJECTED as
      misdiagnosed. At avail==2 the only way to use the budget is "⌂…", the
      forbidden nameless-ellipsis form. The wasted cell is CORRECT; acting on
      that minor would reintroduce the defect. A comment goes in instead.
    - Task 4 minor (vestigial "prompt line" claim): MUST-FIX, bundled.
  Judgment calls: (a) HEAD->detached inside the accessor — ACCEPT, with the
  noted cost that sessionBranch is now a display string; split if a second
  consumer appears. (b) the three glyph assertions — ENDORSED, and the
  reviewer agrees my "don't alter existing assertions" rule was the wrong
  instruction for a task whose deliverable is changing that glyph.

One fix wave dispatched (opus — escalated, since this is the second failure on
  the same defect class). Wide-rune mutation evidence required this time.
  Landed as 4d42024: the guards now TRUNCATE THEN MEASURE (truncNamed) instead
  of computing survival from remaining room. Arithmetic on `room` was
  rune-count logic however it was spelled.

Fix-wave re-review (opus, empirical): ALL 8 ADDRESSED, no new breakage.
  671 chipSegment evaluations over avail 0..60 x 11 fixtures (ASCII, CJK,
  mixed, one-char, one-wide-char): zero nameless forms, zero overflows,
  degradation monotonic in both names.
  1540 rendered renderStateLine lines over widths 1..220 x 7 fixtures: every
  line exactly the pane width, zero nameless forms. The four Critical widths
  (27/28/43/44) render honestly.
  Exhaustive old-vs-new behavioral diff, 10 x 10 names x avail 0..60: 85
  differing points, 100% of them "old emitted a nameless form, new does not."
  Nothing shifted beyond the fix.
  MUTATION RE-RUN BY THE REVIEWER: restored the old guards and confirmed the
  new assertions fail at CJK inputs — so this round's evidence IS
  wide-rune-sensitive, which is precisely what last round's was not.
  Scrutiny items: fitChip's new signature has both call sites correct with
  glyph/bare not transposed; moving the truncation test 30 -> 40 is NOT a
  dodge (pane width was never the load-bearing variable — chip SLOT width is,
  and the sweep covers the degenerate slot region), and the moved test is
  stronger now; the 200-width sweep asserts invariants, not golden strings.

FINAL VERIFICATION by me on the shipped code: build/vet/gofmt/test clean, and
  live on head %179 —
    ● Thinking 0:09 · opus-5 · ⎇ branch-worktree-chips · ⌂ align-context-meters

RESIDUALS PARKED (no second fix wave):
  - parked — renderStatusbar (the height<=1 packed fallback) still renders
    nameless forms at narrow widths, e.g. "⎇…" at 26 and "⚠ no worktree · ⎇ …"
    at 43. Ruling: real but NOT from chipSegment — that path gets a fixed
    40-cell budget and then clipLine truncates the WHOLE line, and its ellipsis
    happens to land after a glyph. The mechanism predates this branch (the old
    code had the identical clipLine tail with "⎇ "+truncateRunes), so it is
    pre-existing, not a regression. Closing it means extending the
    no-nameless-chip invariant to the packed layout, which is its own change.
  - parked — TestStateLineNeverRendersNamelessChip uses t.Fatalf so it reports
    only the first failing width per fixture, sweeps only renderStateLine, and
    iterates a map so subtest order is nondeterministic. Ruling: cosmetic; it
    caught the defect it exists for and asserts invariants rather than goldens.

BRANCH COMPLETE. 10 commits from ff32d33. Ready for Michael's merge call.

FINAL FIX WAVE COMPLETE. See final-fix-report.md for the full evidence trail.
  Root cause of both Criticals: rung guards computed name survival by integer
  arithmetic on remaining room (rune-count logic in display-width costume).
  Replaced by truncNamed(chip, glyph, room) — truncate first, MEASURE what
  survived. Rungs 2 and 4 and fitChip all route through it; fitChip gained a
  glyph parameter and falls back to bare when no name character survives.
  TestChipSegmentLadder passes unchanged, both boundary rows included.
  Tests: TestChipSegmentSingleChipLadders (new, covers fitChip degradation),
  TestStateLineNeverRendersNamelessChip (new, widths 1-200 x 5 fixtures incl.
  CJK, asserts exact pane width AND no nameless-ellipsis form),
  TestChipSegmentWideRunes (rewritten with per-rung content assertions +
  a Rung-2-isolating CJK-worktree/ASCII-branch subtest),
  TestRenderStateLineTruncatesChipWhenNarrow (fixture widened 30 -> 40; it was
  green only because of the defect), TestWarningChipHasNoBranchGlyph... (now
  targets branchGlyph+noWorktreeWarning and carries sessionBranch "main", so it
  proves warning/branch coexistence instead of "this fixture has no branch").
  Three stale comments corrected (View, noWorktreeWarning, renderStatusbar).
  Task 3 minor: NOT acted on, per triage; comment added at the guard saying the
  spare cell at avail==2 is correct. lastGitBranch left alone.
  Wide-rune mutation evidence captured: with the old arithmetic guards restored
  the new assertions fail at CJK inputs (avail 8/13/4/3 and state line width
  33) while lipgloss.Width(got) > avail never fires — the precise blind spot
  that let Critical 2 survive the previous round's ASCII-only mutation check.
  go build / go vet / gofmt / go test ./... all clean.
