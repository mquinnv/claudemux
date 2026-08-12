# Final review fix report — lobby-preview

Branch: `lobby-preview`, from HEAD 44fb96b. All 8 findings addressed in one
commit: **5bd1478** — `fix(head): fix final-review findings on the lobby
preview box`.

Verification at the end: `go build ./... && go vet ./... && go test
./cmd/claudemux-head/` — all green. `gofmt -l .` — empty.

---

## Finding 1 — fleet list disappears at height 16 (Important)

**Change**: `cmd/claudemux-head/swpreview.go`, `computePreviewLayout`: guard
changed from `if list < 2` to `if list < 3`, with the rationale (a session
occupies up to 2 rows, View reserves 1 more for "… +N more") rewritten into
the comment.

**Covering tests**:
- `TestComputePreviewLayout` (swpreview_test.go): table updated — old case
  `{16, false, {Show:true, Content:6, ListRows:2}}` replaced with
  `{17, false, {Show:true, Content:6, ListRows:3}}` ("the smallest lobby with
  a preview") and a new case `{16, false, {}}` ("one row short: fleet wins").
- New `TestSwModelViewOneRowShortDropsBoxAndShowsFleet`
  (switchboardtui_test.go): height 16, asserts no box (`"┌"` absent) and that
  a full session row (`"fixing the build"`, session `api`'s summary) still
  renders.
- New `TestSwModelViewSmallestPreviewHeightStillShowsASessionRow`: height 17
  (smallest height that now shows a box), asserts both the box (`"┌"`) and a
  session row (`"fixing the build"`) render.

**Command**:
```
go test ./cmd/claudemux-head/ -run 'TestComputePreviewLayout|TestSwModelViewOneRowShortDropsBoxAndShowsFleet|TestSwModelViewSmallestPreviewHeightStillShowsASessionRow' -v
```
Output: all PASS.

**Mutation evidence** (reverted the guard to `if list < 2`):
```
$ go test ./cmd/claudemux-head/ -run 'TestSwModelViewOneRowShortDropsBoxAndShowsFleet|TestComputePreviewLayout' -v
--- FAIL: TestSwModelViewOneRowShortDropsBoxAndShowsFleet
    height 16 is below the preview floor; the box must not render:
    claudemux switchboard

        … +3 more

    ┌─ api ────────────────────────────────────────────────────────────────────────┐
    │                                                                              │
    │                                                                              │
    │                                                                              │
    │                                                                              │
    │                                                                              │
    │                                                                              │
    └──────────────────────────────────────────────────────────────────────────────┘

    conducting · 1 waiting
    space conduct/standby · j/k select · enter jump · q quit
    --- FAIL: TestComputePreviewLayout/one_row_short:_fleet_wins
    computePreviewLayout(16, false) = {Show:true Content:6 ListRows:2}, want {Show:false ...}
```
This is the bug exactly as described: at height 16 under the old threshold,
the box is drawn and ZERO fleet rows render. Guard restored to `list < 3`
after capturing this evidence.

---

## Finding 2 — row-budget test can't catch a one-row overflow (Important)

**Change**: `cmd/claudemux-head/switchboardtui_test.go`,
`TestSwModelViewCapsListForPreview`:
- `strings.Count(view, "\n")` replaced with `len(strings.Split(raw, "\n"))`
  (counts lines, not separators — a footer with no trailing `\n` means an
  N-line view has only N-1 separators, so `Count` silently permits
  `m.height+1`).
- Added an assertion that the box actually rendered (`strings.Contains(view,
  "┌")`) — closes the gap where a regression dropping the box while keeping
  the cap would previously pass silently.
- Added a per-line width assertion (`lipgloss.Width(l) <= m.width` for every
  line), closing the gap that the old test never checked line width.

**Command**:
```
go test ./cmd/claudemux-head/ -run TestSwModelViewCapsListForPreview -v
```
Output: PASS.

**Mutation evidence** (deleted `budget--` in `switchboardtui.go`'s
row-budget computation):
```
$ go test ./cmd/claudemux-head/ -run TestSwModelViewCapsListForPreview -v
--- FAIL: TestSwModelViewCapsListForPreview
    switchboardtui_test.go:556: view is 21 lines, want at most 20
```
Confirmed the old assertion would NOT have caught this — a standalone check
(`/private/tmp/.../scratchpad/countcheck.go`) against the same 21-line/20-row
shape:
```
Count(\n) = 20  len(Split) = 21
```
`strings.Count` = 20 ≤ `m.height` (20) → old assertion stays green.
`len(strings.Split)` = 21 > 20 → new assertion correctly fails. Mutation
reverted after capturing this evidence.

---

## Finding 3 — preview goes blank during a fast scroll (Important)

**Change**: `cmd/claudemux-head/switchboardtui.go`, the `swPreviewMsg` case
in `Update`: the stale-drop path (`msg.pane != m.selectedPane()`) now
returns `m, m.previewCmd()` instead of `m, nil`, re-issuing a capture for
wherever the cursor currently is.

**Covering tests**:
- New `TestSwModelStalePreviewReissuesForCurrentSelection`: requests against
  pane A (`%2`), moves selection to B (`%5`), delivers A's stale message,
  asserts the returned `cmd` is non-nil and `previewInFlight` is `true`
  again.
- `TestSwModelDropsStalePreview` updated: since the reissue path now runs
  through `previewCmd`, which clears `previewOut`/`previewPane` whenever the
  pane being requested differs from what's held (the existing pane-change
  guard), the sentinel `"current"` value gets cleared rather than kept — the
  invariant that still holds, and that the test now checks, is that the
  stale reply's own payload (`"stale"`) is never painted, and that the flag
  goes back to `true` (re-issued) rather than staying cleared.

**Command**:
```
go test ./cmd/claudemux-head/ -run 'TestSwModelStalePreviewReissuesForCurrentSelection|TestSwModelDropsStalePreview' -v
```
Output: both PASS.

**Mutation evidence** (reverted the fix to `return m, nil`):
```
$ go test ./cmd/claudemux-head/ -run 'TestSwModelStalePreviewReissuesForCurrentSelection|TestSwModelDropsStalePreview' -v
--- FAIL: TestSwModelDropsStalePreview
    the stale reply must never be painted: out="current" pane="%2"
    a dropped capture with a claude pane still selected must re-issue, leaving the flag set
--- FAIL: TestSwModelStalePreviewReissuesForCurrentSelection
    a stale result must re-issue a capture for the current selection, not just drop it
```
Both tests fail under the pre-fix behavior. Fix restored after capturing
this evidence.

---

## Finding 4 — unspecified evaluation order (Minor)

**Change**: `cmd/claudemux-head/switchboardtui.go`, the `j`/`k` key
handlers: `return m, m.previewCmd()` split into
```go
cmd := m.previewCmd()
return m, cmd
```
matching the two-line form already used by the `swSnapshotMsg` path (`pv :=
m.previewCmd()` then used inside `tea.Batch`).

No new test needed — this is a style/ordering-guarantee fix with no
observable behavior change (gc already evaluated the call first); covered
transitively by the existing `TestSwModelSelectionKeys` and
`TestSwModelSelectionRequestsPreview`.

---

## Finding 5 — stray blank line under 8 columns (Minor)

**Change**: `cmd/claudemux-head/switchboardtui.go`, `View()`: the preview
block now computes `box := renderPreview(...)` first and only writes the
leading `"\n"` separator (and the box's lines) when `box != nil`, instead of
unconditionally emitting the separator before finding out whether
`renderPreview` returned `nil` (which it does for `width < 8`).

Covered by existing `TestRenderPreviewRefusesTinyBoxes` (renderPreview
itself) plus the general width-safety tests
(`TestSwModelViewNeverExceedsWidth`); no dedicated new test since the
narrow-width path isn't separately exercised in the swModel View tests
(all use width ≥ 40).

---

## Finding 6 — two inaccurate spec claims (Minor)

**Change**: `docs/superpowers/specs/2026-08-11-lobby-preview-design.md`:
- (a) Sizing section: `chrome = 5` → `chrome = 6`, comment now lists all six
  rows (title, blank, blank-above-box, blank-above-status, status, hints),
  "6 when a tmux error line is showing" → "7 when...", and a note that
  `TestComputePreviewLayout` and `TestSwModelViewCapsListForPreview`'s
  row-count assertions catch a wrong value, so the code is not what to
  change if this ever looks off. Also rewrote the `list < 2` → `list < 3`
  floor explanation to match the Finding 1 fix.
- (b) Rendering section: removed the "only SGR" claim about `capture-pane
  -e`; now states it also emits OSC 8 hyperlink sequences (tmux 3.2+,
  observed in claude panes), and that this is already handled correctly —
  `ansi.Truncate` measures OSC 8 as zero-width and closes it with `\x1b]8;;
  \x1b\\` at the truncation point — so the code needed no change, only the
  doc.

No test — documentation-only.

---

## Finding 7 — test artifact constraining the box title (Important, promoted)

**Change**:
- `cmd/claudemux-head/switchboardtui_test.go`,
  `TestSwModelViewClipsLongName`: rescoped from searching the whole view to
  asserting only on the fleet row itself (`strings.Split(view, "\n")[2]` —
  index 2 is the first session's row: index 0 is the title, index 1 the
  blank line under it). This is the one test assertion changed outside its
  originating finding, exactly as authorized.
- `cmd/claudemux-head/switchboardtui.go`, `View()`: the preview box title is
  now `m.snap.Sessions[m.sel].Name` (untruncated) instead of
  `ansi.Truncate(..., swNameColW, "…")`. `renderPreview`'s existing
  `previewTopBorder` already clips the title to the box's own available
  width via `ansi.Truncate` (display-width aware), so the box can now show
  more of a long name than the row's 24-cell column allows.

**Command**:
```
go test ./cmd/claudemux-head/ -run 'TestSwModelViewClipsLongName|TestRenderPreviewTruncatesTitle|TestRenderPreviewCJKTitleWidth' -v
```
Output: all PASS.

---

## Finding 8 — README (Minor)

**Change**: `README.md`, switchboard section: added a paragraph after the
two-line session row description, before the "Keys in the lobby" line,
covering: what the box shows (the selected session's `claude` pane, same
idea as `tmux choose-tree -Zs`), that it follows the selection with an
immediate refresh on `j`/`k` (not just the next poll), that it's read-only,
and that it's dropped on panes too short to show both it and the fleet.

No test — documentation-only.

---

## Global constraints check

- No new module dependencies (no `go.mod`/`go.sum` changes).
- `isWaiting`, the conductor, and the column-grid constants (`swNameColW`,
  `swStateColW`, `swAgeColW`, `swCtxColW`) untouched — `swNameColW` is still
  used for the fleet row's own name column, only the box title's truncation
  source changed (Finding 7, explicitly scoped to the box).
- Every line the lobby emits is still clipped to `m.width`: verified by
  `TestSwModelViewNeverExceedsWidth`, `TestSwModelViewCapsListForPreview`'s
  new per-line width check, and `renderPreview`'s own geometry tests.
- Comments explain WHY, matching the density of the surrounding file.

## Final verification

```
$ go build ./... && go vet ./... && go test ./cmd/claudemux-head/
ok      github.com/mquinnv/claudemux/cmd/claudemux-head        3.5s
$ gofmt -l .
(empty)
$ go test ./...
ok      github.com/mquinnv/claudemux/cmd/claudemux-head        3.7s
```

Commit: **5bd1478** on branch `lobby-preview` (parent 44fb96b).
