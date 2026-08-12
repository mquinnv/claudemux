# SDD ledger — plan: docs/superpowers/plans/2026-08-11-lobby-preview.md

Branch: lobby-preview, from main at 26a3ab0 (also the merge base for the
final review). Worktree: .claude/worktrees/align-context-meters.

Pre-flight scan — the plan is a day old and switchboardtui.go changed under it
when the background-work branch's fix wave landed. Two drift items, neither a
plan/rubric conflict, both handled in the dispatches rather than by asking:
  1. Task 6's View snippets no longer match the file. Line 2 (`detail`) is now
     built as `line2` and passed through clipLine, the status/error/footer
     lines are clipped too, and the name cell uses ansi.Truncate. The
     implementer must INTEGRATE the row cap and the box, not paste.
  2. Task 3's previewTopBorder uses truncateRunes for the box title. The
     codebase now truncates display cells with ansi.Truncate everywhere for
     exactly this reason — a CJK title clipped to N runes overruns the border,
     the bug just fixed a few lines above. Dispatch specifies ansi.Truncate.

Task 1: complete (commits 26a3ab0..242404a, review clean)
Task 2: complete (commits 242404a..e34a7b5, review clean)
Task 3: implemented 34ca95f with my directed ansi.Truncate correction.
  Plan defect found and correctly handled by the implementer: the brief's
  TestRenderPreviewGeometry asserts an unused content row TrimSpaces to "",
  which is unsatisfiable against the brief's OWN implementation — every
  content row carries │ borders. Implementer rewrote the assertion and
  disclosed it. No adjudication needed; the plan text is simply impossible,
  not in conflict.
  Review: SPEC ✅, 2 Important (both test-quality), 1 Minor.
Task 3: fix round 1 dispatched — (a) the only title-truncation test is ASCII,
  so it passes identically under truncateRunes and cannot prove the very
  correction it exists for; needs a CJK title plus mutation evidence.
  (b) the unused-row assertion checks only the border chars while its comment
  claims it checks the interior is blank.
Task 3: minor (deferred): task-3-report.md cited an ASCII-only test as
  validating CJK correctness; being corrected in the same round.
Task 3: fix round 1/5 (2 addressed, 0 open — CJK title test now discriminates
  the two truncation strategies (20 cells vs 34, verified independently by the
  re-reviewer); unused-row assertion now checks the interior, not the borders;
  commits 34ca95f..ba6cd80)
Task 3: minor (deferred): the CJK test's inline comment says "15 runes (30
  cells)" where truncateRunes actually yields 14 runes + ellipsis = 29 cells.
  Cosmetic; the test outcome and the 34-cell mutation figure are correct.
Task 3: complete (commits e34a7b5..ba6cd80, review clean)
Task 4: complete (commits ba6cd80..dae4c9b, review clean) — reviewer
  recomputed all 7 table rows independently, no shared error between the
  table and the implementation; drop boundary confirmed at height 15/16.
Task 5: implemented ed0c04d. Review SPEC ✅; Update paths traced correct.
  2 Important (both test gaps, no code bug), 1 Minor.
Task 5: fix round 1 dispatched — (a) the stale-drop test leaves m.sel on the
  session whose pane it uses, so previewPane and selectedPane() are identical
  and a wrong-comparison regression passes; (b) nothing tests that the POLL
  path requests a capture, so deleting it from the swSnapshotMsg case would
  go unnoticed and the preview would only refresh on j/k.
Task 5: minor (deferred) — PLAN-INHERITED, for the final review to triage:
  when a capture is in flight and the selection changes, the new request is
  dropped and nothing re-issues it when the stale result lands, so the
  preview waits for the next 1s poll. That narrowly reintroduces the lag the
  selection trigger exists to remove. Comes verbatim from my brief's Step 4/5,
  not an implementer deviation. Window is small since capture-pane is fast.
Task 5: fix round 1/5 (2 addressed, 0 open — stale-drop test now diverges
  selectedPane %5 from previewPane %2 at delivery; poll-path test asserts
  previewInFlight, which only previewCmd() sets; commits ed0c04d..2e6186d)
Task 5: minor (deferred): TestSwModelSnapshotRequestsPreview's `cmd == nil`
  check is redundant scaffolding — tea.Batch(swNextTick(), pv) is non-nil
  regardless of pv. The previewInFlight assertion carries the test.
Task 5: complete (commits dae4c9b..2e6186d, review clean)
Task 6: complete (commits 2e6186d..44fb96b, review clean) — reviewer verified
  the row budget algebraically AND hand-counted height 20 / 30 sessions
  (19 lines vs a 20-row pane), and confirmed swSessionRows agrees with View's
  own two-row condition.
Task 6: disclosed deviation, ENDORSED by the reviewer — the preview box title
  is truncated to swNameColW because the pre-existing
  TestSwModelViewClipsLongName asserts an over-long name never appears at
  full length ANYWHERE in the view, and my "don't alter existing assertions"
  constraint left no other compliant path. Truncation is ansi.Truncate
  (display-width aware), and binding the title to the row's column width is a
  defensible consistency argument. Cost: on wide panes the box could show a
  longer title than it does.
Task 6: minor (deferred): TestSwModelViewShowsPreview's "api" assertion is
  weak — that name appears in the fleet row whether or not the box renders.
  Brief-mandated, so a spec nit rather than an implementation defect.
Task 6: minor (deferred): moreLine goes through clipLine, which the brief did
  not ask for; consistent with the file's clip-everything convention.

ALL SIX TASKS COMPLETE. Deferred minors for the final review to triage:
  - Task 3: CJK test comment says "15 runes (30 cells)"; actual is 14 + "…"
    = 29. Cosmetic; outcome and mutation figure correct.
  - Task 5: PLAN-INHERITED, the substantive one — an in-flight capture plus a
    selection change drops the new request with no re-issue when the stale
    result lands, so the preview waits for the next 1s poll. Narrowly
    reintroduces the lag the selection trigger exists to remove.
  - Task 5: TestSwModelSnapshotRequestsPreview's `cmd == nil` check is
    redundant; the previewInFlight assertion carries the test.
  - Task 6: the two above.

Final whole-branch review (26a3ab0..44fb96b, opus): NOT READY.
  Important 1 — at pane height 16 the fleet renders ZERO sessions while an
  8-row box takes the space. computePreviewLayout admits the box at list >= 2
  on MY spec's reasoning that "2 rows means one session", but a session with a
  summary takes TWO rows and View reserves one more for "+N more" — 1 usable
  row, which fits nothing. Contradicts the spec's own fleet-wins rule and
  regresses against main. Fix: list < 3.
  Important 2 — the row-budget test counts NEWLINES (N-1 for N lines), so it
  permits height+1. Mutation-proven: deleting `budget--` renders 17 lines in a
  16-row pane with the whole suite green.
  Promoted from deferred: the in-flight + selection-change drop leaves the box
  BLANK (previewCmd clears previewOut on pane change), and held-down j repeats
  at ~30ms — faster than a capture round trip — so it can stay blank until the
  next poll. One line plus one test; being closed now.
  Minors: unspecified Go evaluation order on `return m, m.previewCmd()`;
  stray blank line under 8 columns; spec's chrome=5 (code's 6 is right);
  spec's "only SGR" claim (tmux 3.2+ also emits OSC 8 — code handles it);
  README not updated.
  Ledger triage by the final reviewer: items 1, 3, 4, 5 ACCEPT; item 2 (the
  plan-inherited lag) FIX NOW.
  Title truncation: endorsed to merge, but the reviewer noted the REASON is a
  test artifact — TestSwModelViewClipsLongName searches the whole view, so it
  constrains every element that can show a session name. Recommended scoping
  it to the fleet row and freeing the title. Included in the fix wave.
  Verified sound and worth recording: width/height invariants held across
  widths 5-171 x heights 1-60 x fleets 0-200 with CJK and SGR payloads; the
  stale path clears previewOut so a fast j/k shows blank, never another
  session's screen under the wrong title; captures cannot pile up or wedge.

One fix wave dispatched (findings 1-8) — landed as 5bd1478.

Fix-wave re-review (opus, empirical): ALL 8 ADDRESSED, no new breakage.
  Height sweep w=80, 5 sessions with summaries: box appears only at h>=17,
  and every box-drawn height renders >=1 full session row. Box-absent
  renders identically to the no-preview lobby.
  Finding 2 mutation: with budget-- deleted, Count(newlines)=20 (old
  assertion green) vs len(Split)=21 (new assertion red) in a 20-row pane.
  Finding 3 terminates: each reply yields at most one new capture aimed at
  the CURRENT pane, so a further re-issue needs a further selection change.
  Finding 7: at w=120 the box title now runs 53 cells while the fleet row
  still clips at 24; CJK titles measured exactly the target width at w=8-80.
  UNAUTHORIZED ASSERTION CHANGE (TestSwModelDropsStalePreview) — ACCEPTED.
  Verified forced and non-lossy: the sentinel is parked under the OLD pane,
  which is what keeps the two comparisons distinguishable; parking it under
  the new pane (the only way to keep the old assertion) makes the mutant pass.
  The wedge guarantee moved to a sibling test rather than disappearing.

RESIDUALS PARKED (no second fix wave; surfacing to Michael):
  - parked — swpreview.go:166 still carries the old "capture-pane emits no
    cursor motion or OSC" claim, which the corrected spec now contradicts.
    Ruling: real, one line, same documentation-that-lies class that cost us
    three rounds on the background-work branch. Not load-bearing; worth
    closing but not worth a wave on its own.
  - parked — raising the floor to 3 moves h=16 from the capped path into the
    uncapped one, so a tall fleet can now overflow a 16-row pane where before
    it fit while showing zero sessions. Ruling: this is the authorized trade
    and matches the spec's fleet-wins rule; overflow-with-content beats
    no-content, and the uncapped path is pre-existing behavior.
  - parked — the "claudemux switchboard" title and "no claudemux sessions"
    lines are never clipped, so they overflow below 21 columns. Ruling:
    pre-existing on main, untouched by this branch, out of scope.
  - parked — TestSwModelViewClipsLongName indexes lines[2] positionally and
    would target the wrong line if the header changed shape. Ruling: correct
    today and does fail under the relevant mutation.
