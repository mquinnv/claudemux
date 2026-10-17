# Lobby Handoff Families Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Sessions launched by another Claude session via `claudemux -d` render indented under that session in the lobby and the web page, the family kept together by its liveliest member.

**Architecture:** The launcher stamps two tmux session options (`@claudemux_parent`, `@claudemux_parent_created`) on a session it creates from a Claude pane. The lobby's poll reads them plus `#{session_created}`; a new `swArrangeFamilies` replaces `swSortSessions` and flattens the forest into display order, annotating each row with depth, tree glyphs, family group and an orphan tag. TUI, dividers and web page read those annotations. The head records the parent name for reboot restore; restore relinks after the whole batch.

**Tech Stack:** Go (bubbletea/lipgloss), bash launcher, vanilla JS in `webpage.html`.

**Spec:** `docs/superpowers/specs/2026-10-16-lobby-handoff-families-design.md`

## Global Constraints

- Parent is recorded only when `$TMUX_PANE` is set and its `#{pane_current_command}` is `claude` or `node`; never on attach to an existing session; never for `-N` (restore sets it itself).
- A link holds only when a live lobby session named `@claudemux_parent` has `session_created` == `@claudemux_parent_created`.
- Family group = min `swGroupOf` over members (active < closing < deferred).
- Within a family: children ordered by `session_created` ascending (ties: name). Family block sits at its root's list-sessions position within the family group (stable).
- Orphan (link not matched) → top-level row whose detail line leads with dim `← <parent>`.
- Conductor (`swconductor.go`) unchanged.
- tmux targets naming a session use the trailing colon (`-t "$name:"`) — `launchertargets_test.go` enforces it in the launcher.
- Tree glyphs: `├ `, `└ `, continuation `│ `, blank `  ` — each 2 cells, taken out of `swNameColW`.

## Review Focus

1. A deferred child inside an active family must not draw a `─ deferred` rule above itself, and a family that starts the deferred group must draw exactly one rule above its root — Task 2 test.
2. Selection/`Enter`/`d` on an indented row must act on that row's session (one flat order shared by View and keys) — Task 1 asserts the arranged slice order; Task 2 asserts `View` rows in the same order.
3. Cycle (`a`→`b`→`a`) or self-parent must not hang or drop rows — Task 1 test.
4. Parent name reused by a later unrelated session (created-time mismatch) must render the child as an orphan, not adopt it — Task 1 test.
5. Older 14-field `list-sessions` lines (heads/fixtures predating this) must still parse with no parent — Task 1 test.

---

### Task 1: Snapshot parses parent links and arranges families

**Files:**
- Create: `cmd/claudemux-head/swfamily.go`
- Create: `cmd/claudemux-head/swfamily_test.go`
- Modify: `cmd/claudemux-head/switchboard.go` (swSession fields, `buildSwSnapshot` field parsing, replace the `swSortSessions` call)
- Modify: `cmd/claudemux-head/switchboardtui.go:472-473` (poll format)

**Interfaces:**
- Produces, on `swSession`:
  - `Created int64` (`#{session_created}`, 0 when unparseable)
  - `Parent string`, `ParentCreated int64` (raw options)
  - `Depth int` (0 = top level)
  - `TreePrefix string` (glyph string, `""` at depth 0, e.g. `"│ └ "` at depth 2)
  - `FamilyGroup swGroup` (group of the block the row is drawn in)
  - `From string` (orphan tag: the unmatched `Parent`, `""` when matched or none)
- Produces: `func swArrangeFamilies(sessions []swSession) []swSession` in `swfamily.go`.
- Produces constant `parentOption = "@claudemux_parent"`, `parentCreatedOption = "@claudemux_parent_created"` (in `swfamily.go`).
- `swGroupOf` and `swSortSessions` stay (swSortSessions may become unused by production — delete it and its test only if nothing else references it).

- [ ] **Step 1: Write failing tests in `swfamily_test.go`**

Helper `fam(name string, created int64, parent string, pcreated int64) swSession`. Tests:

```go
func TestArrangeFamiliesNestsAndKeepsActive(t *testing.T) {
	// iai-admin deferred, children beejax (deferred), usm, ag (active).
	in := []swSession{
		fam("remix-3", 10, "", 0),
		withDefer(fam("iai-admin", 20, "", 0)),
		fam("remix-6", 30, "", 0),
		withDefer(fam("digiscreener", 5, "", 0)),
		withDefer(fam("beejax", 40, "iai-admin", 20)),
		fam("usm", 41, "iai-admin", 20),
		fam("ag", 42, "iai-admin", 20),
	}
	got := swArrangeFamilies(in)
	// names in order
	want := []string{"remix-3", "iai-admin", "beejax", "usm", "ag", "remix-6", "digiscreener"}
	// prefixes
	wantPrefix := []string{"", "", "├ ", "├ ", "└ ", "", ""}
	// FamilyGroup: all of iai-admin's block = swGroupActive; digiscreener = swGroupDeferred
}

func TestArrangeFamiliesAllDeferredSinks(t *testing.T)      // parent+children all deferred → block after remix-6, FamilyGroup deferred
func TestArrangeFamiliesGrandchildren(t *testing.T)          // a → b → c, plus a → d: order a,b,c,d; prefixes "", "├ ", "│ └ ", "└ "
func TestArrangeFamiliesNameReuseIsOrphan(t *testing.T)      // child Parent "beejax-2" ParentCreated 100, live beejax-2 Created 200 → top level, From "beejax-2"
func TestArrangeFamiliesMissingParentIsOrphan(t *testing.T)  // Parent "gone" → top level, From "gone"
func TestArrangeFamiliesCycle(t *testing.T)                  // a→b, b→a (created times match) and self-parent c→c: all three present exactly once, no hang
func TestArrangeFamiliesChildOrderByCreated(t *testing.T)    // children given in list order ag(42), usm(41) → usm before ag
func TestBuildSwSnapshotParsesParentFields(t *testing.T)     // 17-field line → Created/Parent/ParentCreated set; 14-field line → all zero/empty, still listed
```

Assert `Depth`, `TreePrefix`, `FamilyGroup`, `From` explicitly in each.

- [ ] **Step 2: Run, verify failure**

Run: `cd cmd/claudemux-head && go test -run 'ArrangeFamilies|ParsesParentFields' .`
Expected: FAIL (undefined: swArrangeFamilies / unknown fields)

- [ ] **Step 3: Implement**

- Poll format: append `\t#{session_created}\t#{@claudemux_parent}\t#{@claudemux_parent_created}` after the closing option.
- `buildSwSnapshot`: accept 12–14 fields as today, or 17; fields 14–16 are created/parent/parent_created. Replace `swSortSessions(snap.Sessions)` with `snap.Sessions = swArrangeFamilies(snap.Sessions)`.
- `swArrangeFamilies` algorithm:
  1. Index sessions by name. A session's parent is matched iff the parent exists, `Created == ParentCreated`, parent != self. Otherwise it's a root; set `From = Parent` when `Parent != ""`.
  2. Break cycles: walk each node's ancestor chain; if it revisits a node, make the node where the revisit was detected a root (with `From` set).
  3. Children of each node sorted by `(Created, Name)`.
  4. Family group of a root = min `swGroupOf` over its subtree.
  5. Stable-sort roots by family group (input order preserved within a group), then emit each root's subtree depth-first, setting `Depth`, `TreePrefix`, `FamilyGroup` on every member.

- [ ] **Step 4: Run, verify pass; run whole package**

Run: `cd cmd/claudemux-head && go test ./...`
Expected: PASS (fix any existing test that pinned 14-field parsing only if it breaks for a reason other than this new behavior)

- [ ] **Step 5: Commit** — `switchboard: arrange handoff families in the snapshot`

---

### Task 2: Lobby renders the tree, family-aware dividers and orphan tag

**Files:**
- Modify: `cmd/claudemux-head/switchboardtui.go` (`swDividerAt`, name cell in `View`, `swDetailLine`)
- Test: `cmd/claudemux-head/switchboardtui_test.go`

**Interfaces:**
- Consumes: `swSession.TreePrefix`, `.FamilyGroup`, `.From`, `.Depth` from Task 1.

- [ ] **Step 1: Write failing tests**

```go
func TestSwDividerAtUsesFamilyGroup(t *testing.T)
// sessions (already arranged): remix-3 (active), iai-admin (deferred, FamilyGroup active),
// beejax (deferred, FamilyGroup active, depth 1), digiscreener (deferred, FamilyGroup deferred)
// swDividerAt(i) == "" for i=0..2, == swDividerLabel for i=3.

func TestSwDetailLineOrphanTagLeads(t *testing.T)
// From "iai-admin", Summary "x" → stripped text starts with "← iai-admin · x"
// From "" → no "←"

func TestViewIndentsChildrenAndKeepsGrid(t *testing.T)
// model with an arranged family; View(): the child's line contains "├ " immediately
// before its name, and the topic column begins at the same display column on the
// parent and child lines (lipgloss.Width of the prefix before the topic text is equal).
```

- [ ] **Step 2: Run, verify failure**

Run: `cd cmd/claudemux-head && go test -run 'SwDividerAtUsesFamilyGroup|OrphanTag|IndentsChildren' .`
Expected: FAIL

- [ ] **Step 3: Implement**

- `swDividerAt`: compare `FamilyGroup` of row i and i-1 instead of `swGroupOf`; same labels and the i==0 closing rule, keyed on `FamilyGroup`.
- Name cell: render `TreePrefix` in `swStatusStyle` (dim) followed by the name truncated/padded to `swNameColW - lipgloss.Width(TreePrefix)` (floor 1), so total width stays `swNameColW`. Selection highlight covers the name part.
- `swDetailLine`: when `sess.From != ""`, prepend `swStatusStyle.Render("← "+sess.From)` as the first part (before the closing phase). `swSessionRows` follows automatically.
- Detail line indent: child rows indent their line 2 by an extra `lipgloss.Width(TreePrefix)`.

- [ ] **Step 4: Run package tests** — `cd cmd/claudemux-head && go test ./...` → PASS

- [ ] **Step 5: Commit** — `lobby: draw handoff families as a tree`

---

### Task 3: Launcher records the parent

**Files:**
- Modify: `bin/claudemux` (`create_session`, after `tmux_new_session -d -s "$session_name" ...`, ~line 1015)
- Test: `cmd/claudemux-head/launchertargets_test.go`

**Interfaces:**
- Produces: session options `@claudemux_parent` (parent session name) and `@claudemux_parent_created` (parent's `#{session_created}`), consumed by Task 1's poll.

- [ ] **Step 1: Write failing test** `TestLauncherRecordsParentOnlyFromClaudePane`: read `bin/claudemux`, extract `record_parent` with the existing `shellFuncBody` helper; assert it exists, contains `pane_current_command`, contains both `claude` and `node`, contains `@claudemux_parent_created`, and that `create_session`'s body calls `record_parent` and the call is inside a branch that excludes `EXACT_NAME` (assert `create_session` body contains `[ -z "$EXACT_NAME" ]` on the same line as, or the line before, `record_parent`).

- [ ] **Step 2: Run, verify failure** — `go test -run RecordsParent ./cmd/claudemux-head/` → FAIL

- [ ] **Step 3: Implement `record_parent SESSION_NAME`** in `bin/claudemux`:
  - return 0 if `$TMUX_PANE` empty;
  - `read` `#{pane_current_command}\t#{session_name}\t#{session_created}` via `tmux display-message -p -t "$TMUX_PANE"`; return 0 on failure;
  - return 0 unless command is `claude` or `node`;
  - return 0 if parent name == `$1`;
  - `tmux set-option -t "$1:" @claudemux_parent "$pname" \; set-option -t "$1:" @claudemux_parent_created "$pcreated"`, errors ignored (`|| true`) — a failed stamp must never fail a launch.
  - Call it right after `tmux_new_session` as `[ -z "$EXACT_NAME" ] && record_parent "$session_name"` (written so `set -e` does not trip: e.g. `if [ -z "$EXACT_NAME" ]; then record_parent "$session_name"; fi`, then adjust the test's assertion to match the form you used).
  - Document `@claudemux_parent` in the header comment's `-d` paragraph (one sentence).

- [ ] **Step 4: Verify** — `go test ./cmd/claudemux-head/` PASS; `bash -n bin/claudemux` clean. Manual: from this Claude session, `bin/claudemux -n -d -W /tmp` (or another dir), then `tmux show-options -t "<printed>:" @claudemux_parent` prints this session's name; `tmux kill-session -t "<printed>:"` afterwards.

- [ ] **Step 5: Commit** — `launcher: record the launching session as @claudemux_parent`

---

### Task 4: Reboot restore carries the link

**Files:**
- Modify: `cmd/claudemux-head/sessionrecord.go` (field), `cmd/claudemux-head/headrecord.go` (`writeRecordCmd`, `parseSessionNamePath`), `cmd/claudemux-head/swrestore.go`, `cmd/claudemux-head/switchboardtui.go` (`swRestoreStepMsg` case)
- Test: `headrecord_test.go`, `swrestore_test.go`

**Interfaces:**
- Produces: `sessionRecord.Parent string \`json:"parent,omitempty"\``.
- Produces: `func relinkArgs(restored []lostSession, created map[string]int64) [][]string` — tmux argv lists, one per child whose `Rec.Parent` is among `restored` names and present in `created`: `{"set-option","-t",child+":",parentOption,parent,";","set-option","-t",child+":",parentCreatedOption,strconv.FormatInt(created[parent],10)}`.
- Produces: `func swRelinkCmd(restored []lostSession) tea.Cmd` — lists `#{session_name}\t#{session_created}`, builds args via `relinkArgs`, runs them; best-effort, returns nil msg.

- [ ] **Step 1: Failing tests**
  - `TestParseSessionNamePathParent`: `"beejax\t/Users/x/beejax\tiai-admin\n"` → name, path, parent `"iai-admin"`; the 2-field form still parses with parent `""`. (Change the function to return a parent too, or add `parseSessionNamePathParent` — implementer's choice; update callers.)
  - `TestRelinkArgs`: restored = {iai-admin, beejax(Parent iai-admin), usm(Parent gone)}; created = {iai-admin: 500, beejax: 501, usm: 502} → exactly one argv, for beejax, with `"500"`.
  - `TestRestoreStartsRelinkAfterLastStep`: in the `swRestoreStepMsg` handler, when the queue empties a non-nil cmd is returned that includes the relink (assert via the offer recording the restored list: add `o.restored []lostSession`, appended on each successful step; test that after the last step `o.restored` holds the successes and the returned cmd is non-nil).

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement**: `writeRecordCmd` format `#{session_name}\t#{session_path}\t#{@claudemux_parent}`, sets `r.Parent`. Track successes in `swRestoreOffer.restored`; on queue empty return `swRelinkCmd(o.restored)` when non-empty.

- [ ] **Step 4: `go test ./cmd/claudemux-head/`** → PASS

- [ ] **Step 5: Commit** — `restore: carry handoff parent links across a reboot`

---

### Task 5: Web status page shows families

**Files:**
- Modify: `cmd/claudemux-head/webfleet.go` (`webSession`, `buildWebFleetView`), `cmd/claudemux-head/webpage.html`
- Test: `cmd/claudemux-head/webfleet_test.go`

**Interfaces:**
- Consumes: `Depth`, `FamilyGroup`, `From` from Task 1.
- Produces JSON on each session: `"depth": int`, `"group": "active"|"closing"|"deferred"` (from `FamilyGroup`), `"from": string,omitempty`.

- [ ] **Step 1: Failing test** `TestWebFleetViewFamilies`: snapshot arranged from the Task 1 iai-admin fixture → beejax has `Depth 1`, `Group "active"`, `Deferred true`, `DeferReason` set; digiscreener `Group "deferred"`; `Counts.Deferred` == 3 (per session, unchanged rule).

- [ ] **Step 2: Run, verify failure**

- [ ] **Step 3: Implement**
  - Go: fill the three fields; leave `Counts` logic as is.
  - JS: the three lists filter on `s.group` (`'closing'`, `'deferred'`, else active) instead of the deferred/closing flags. Each row's name cell gets `padding-left: calc(1.1em * depth)`; no tree glyphs on the web. Active-table rows with `s.deferred` show a small `◆ <defer_reason>` (defer color) after the topic; any row with `s.from` shows a dim `← from` before the summary. Section headings count their own rows (already true: they count the list passed in).

- [ ] **Step 4: `go test ./cmd/claudemux-head/`** → PASS. Then `go build ./cmd/claudemux-head` and confirm `webpage.html` is still embedded (existing `webserver_test` passes).

- [ ] **Step 5: Commit** — `web: show handoff families on the status page`
