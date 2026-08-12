# Branch and Worktree Chips Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show the git branch and the worktree as two separate chips in `claudemux-head`'s status line, with `⎇` finally meaning branch.

**Architecture:** The branch is read from the transcript's per-entry `gitBranch` field — the same place and the same way the worktree chip already reads `cwd` — so there is no git subprocess. A single pure function assembles both chips within a width budget, degrading in a fixed order, and both status-line layouts call it.

**Tech Stack:** Go, `github.com/charmbracelet/lipgloss`, `github.com/charmbracelet/x/ansi`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-12-branch-and-worktree-chips-design.md`

## Global Constraints

- Work on branch `branch-worktree-chips` in the worktree `.claude/worktrees/align-context-meters`. Do not create another worktree or branch.
- No new module dependencies.
- Every task ends green: `go build ./... && go vet ./... && go test ./cmd/claudemux-head/`, and `gofmt -l .` empty.
- All truncation is display-width aware (`ansi.Truncate`), never rune count (`truncateRunes`). A CJK name clipped to N runes measures 2N cells and overruns the line; this file has been fixed for that twice.
- The state and model text never shrink. Only chips degrade.
- The `⚠ no worktree` warning keeps its current rules and its current placement in the left group. Do not move it, do not change when it fires.
- Comments explain **why**, matching the density of the surrounding code.
- Out of scope, do not touch: the lobby (`switchboardtui.go`, `swpreview.go`), `isWaiting`, the conductor.

## File Structure

| File | Responsibility |
|---|---|
| `cmd/claudemux-head/events.go` (modify) | Parse the transcript's top-level `gitBranch` into `Event`. |
| `cmd/claudemux-head/tui.go` (modify) | `lastGitBranch` accessor, the `sessionBranch` model field, `chipSegment` and its glyph constants, and the two render sites. |
| `cmd/claudemux-head/events_test.go`, `tui_test.go` (modify) | Tests. |

`chipSegment` lives in `tui.go` beside `renderStateLine` and `renderStatusbar`, its only two callers. It is a pure function of three arguments, so it is fully testable without a model.

---

### Task 1: Parse the transcript's gitBranch

**Files:**
- Modify: `cmd/claudemux-head/events.go` (the `raw` struct in `parseEvent`, and the `Event` struct)
- Test: `cmd/claudemux-head/events_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `Event.GitBranch string` — the entry's `gitBranch`, `""` when the field is absent.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/claudemux-head/events_test.go`:

```go
// The branch rides along on every transcript entry, next to the cwd the
// worktree chip already reads — which is why the head needs no git subprocess.
func TestParseEventGitBranch(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-08-12T10:00:00Z",` +
		`"cwd":"/Users/x/repo","gitBranch":"lobby-preview","message":{"content":"hi"}}`
	ev, ok := parseEvent(line)
	if !ok {
		t.Fatal("parse failed")
	}
	if ev.GitBranch != "lobby-preview" {
		t.Errorf("GitBranch = %q, want lobby-preview", ev.GitBranch)
	}
}

// A transcript from a directory that is not a git repo carries no branch.
func TestParseEventGitBranchAbsent(t *testing.T) {
	ev, ok := parseEvent(`{"type":"assistant","timestamp":"2026-08-12T10:00:00Z","cwd":"/tmp/x"}`)
	if !ok {
		t.Fatal("parse failed")
	}
	if ev.GitBranch != "" {
		t.Errorf("GitBranch = %q, want empty", ev.GitBranch)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run TestParseEventGitBranch -v`
Expected: FAIL — `ev.GitBranch undefined (type Event has no field or method GitBranch)`.

- [ ] **Step 3: Add the field and parse it**

In `cmd/claudemux-head/events.go`, add to the `Event` struct next to `Cwd`:

```go
	Cwd         string // transcript's per-entry cwd; tracks worktree moves
	// GitBranch is the entry's branch, recorded by Claude Code on every line.
	// Reading it here is what lets the head show a branch without shelling out
	// to git on every poll. Empty when the session's directory is not a repo.
	// A detached HEAD records the literal string "HEAD" — see lastGitBranch.
	GitBranch string
```

Add the field to `parseEvent`'s `raw` struct, beside `Cwd`:

```go
		Cwd         string          `json:"cwd"`
		GitBranch   string          `json:"gitBranch"`
```

And carry it into the event, in the same statement that already carries `Cwd`:

```go
	ev := Event{Type: raw.Type, IsMeta: raw.IsMeta, Timestamp: raw.Timestamp, Cwd: raw.Cwd,
		GitBranch: raw.GitBranch, IsSidechain: raw.IsSidechain, RawLine: line}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -v 2>&1 | tail -10`
Expected: PASS, including every pre-existing parser test.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/events.go cmd/claudemux-head/events_test.go
git commit -m "feat(head): parse the transcript's gitBranch"
```

---

### Task 2: Track the session's branch on the model

**Files:**
- Modify: `cmd/claudemux-head/tui.go` (model fields near `sessionCwd` ~line 134, `recomputeFromEvents` ~line 373, `switchSession` ~line 417, and a new accessor beside `lastMainCwd` ~line 402)
- Test: `cmd/claudemux-head/tui_test.go`

**Interfaces:**
- Consumes: `Event.GitBranch` (Task 1).
- Produces:
  - `lastGitBranch(events []Event, prev string) string` — newest-first scan, non-sidechain only, returns `prev` when the ring holds none, and maps the literal `"HEAD"` to `"detached"`.
  - Model field `sessionBranch string`, refreshed in `recomputeFromEvents` and reset in `switchSession`.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/claudemux-head/tui_test.go`:

```go
func TestLastGitBranch(t *testing.T) {
	// Newest wins.
	if got := lastGitBranch([]Event{{GitBranch: "main"}, {GitBranch: "lobby-preview"}}, ""); got != "lobby-preview" {
		t.Errorf("lastGitBranch = %q, want lobby-preview", got)
	}
	// A subagent working in another worktree must not hijack the chip — same
	// rule lastMainCwd applies to cwd.
	if got := lastGitBranch([]Event{{GitBranch: "main"}, {GitBranch: "agent-branch", IsSidechain: true}}, ""); got != "main" {
		t.Errorf("lastGitBranch (sidechain tail) = %q, want main", got)
	}
	// A poll of pure sidechain activity, or a not-yet-seeded ring, keeps what
	// was already known rather than blanking a chip that was right a second ago.
	if got := lastGitBranch([]Event{{GitBranch: "agent", IsSidechain: true}}, "prev-branch"); got != "prev-branch" {
		t.Errorf("lastGitBranch (no usable event) = %q, want prev-branch", got)
	}
	if got := lastGitBranch(nil, "prev-branch"); got != "prev-branch" {
		t.Errorf("lastGitBranch (empty ring) = %q, want prev-branch", got)
	}
	// Claude Code records a detached HEAD as the literal string "HEAD", which
	// would read as a branch named HEAD.
	if got := lastGitBranch([]Event{{GitBranch: "HEAD"}}, ""); got != "detached" {
		t.Errorf("lastGitBranch (detached) = %q, want detached", got)
	}
}

// A rotated session must not inherit the previous session's branch.
func TestSwitchSessionResetsBranch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.jsonl")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	m := model{sessionBranch: "old-branch"}
	m.switchSession(path, time.Now())
	if m.sessionBranch != "" {
		t.Errorf("sessionBranch = %q, want empty after rotation", m.sessionBranch)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestLastGitBranch|TestSwitchSessionResetsBranch' -v`
Expected: FAIL — `undefined: lastGitBranch`, `unknown field sessionBranch`.

- [ ] **Step 3: Add the accessor**

In `cmd/claudemux-head/tui.go`, directly after `lastMainCwd`:

```go
// lastGitBranch returns the branch of the most recent main-session
// (non-sidechain) event that carries one, scanning the ring newest-first. It
// mirrors lastMainCwd exactly, for the same two reasons: a subagent working in
// another worktree must not hijack the chip, and a poll that brings no usable
// event must keep the branch already known rather than blanking it.
//
// A detached HEAD is recorded by Claude Code as the literal string "HEAD".
// Reporting that verbatim would read as a branch named HEAD, so it is mapped
// here — the honest answer to "what branch is this session on" is "detached".
func lastGitBranch(events []Event, prev string) string {
	for i := len(events) - 1; i >= 0; i-- {
		if e := events[i]; e.GitBranch != "" && !e.IsSidechain {
			if e.GitBranch == "HEAD" {
				return "detached"
			}
			return e.GitBranch
		}
	}
	return prev
}
```

- [ ] **Step 4: Add the model field and wire it**

Add to the model, next to `sessionCwd`:

```go
	// sessionBranch is the branch the *main* session last recorded. Derived
	// the same way as sessionCwd and from the same transcript entries — see
	// lastGitBranch.
	sessionBranch string
```

In `recomputeFromEvents`, beside the existing `sessionCwd` line:

```go
	m.sessionCwd = lastMainCwd(m.allEvents, m.sessionCwd)
	m.sessionBranch = lastGitBranch(m.allEvents, m.sessionBranch)
```

In `switchSession`, where the other derived state is discarded, add:

```go
	m.sessionBranch = ""
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -v 2>&1 | tail -10`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/claudemux-head/tui.go cmd/claudemux-head/tui_test.go
git commit -m "feat(head): track the session's git branch from the transcript"
```

---

### Task 3: chipSegment — assemble both chips within a width budget

**Files:**
- Modify: `cmd/claudemux-head/tui.go` (new constants and function, placed directly above `renderStateLine`)
- Test: `cmd/claudemux-head/tui_test.go`

**Interfaces:**
- Consumes: nothing (pure).
- Produces:
  - `branchGlyph = "⎇ "`, `worktreeGlyph = "⌂ "`, `worktreeGlyphBare = "⌂"`
  - `chipSegment(branch, worktree string, avail int) string` — the assembled chips, never wider than `avail` display cells, `""` when nothing fits.

- [ ] **Step 1: Write the failing test**

Add to `cmd/claudemux-head/tui_test.go`:

```go
// The degradation ladder from the spec, rung by rung. The worktree never
// vanishes while one exists — it falls back to its bare glyph, which still
// says "you are in a worktree". The branch keeps its name or goes, because a
// bare branch glyph says nothing: a session is always on some branch.
func TestChipSegmentLadder(t *testing.T) {
	const b, w = "lobby-preview", "align-context-meters"
	tests := []struct {
		name  string
		avail int
		want  string
	}{
		{"both in full", 40, "⎇ lobby-preview · ⌂ align-context-meters"},
		{"worktree name truncates", 30, "⎇ lobby-preview · ⌂ align-cont…"},
		{"worktree down to its glyph", 19, "⎇ lobby-preview · ⌂"},
		{"branch name truncates", 14, "⎇ lobby-pr… · ⌂"},
		{"worktree glyph alone", 4, "⌂"},
		{"nothing fits", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chipSegment(b, w, tt.avail)
			if got != tt.want {
				t.Errorf("chipSegment(avail=%d) = %q, want %q", tt.avail, got, tt.want)
			}
			if lipgloss.Width(got) > tt.avail {
				t.Errorf("chipSegment(avail=%d) measures %d cells", tt.avail, lipgloss.Width(got))
			}
		})
	}
}

func TestChipSegmentSingleChips(t *testing.T) {
	// No worktree: the branch stands alone and truncates like any other chip.
	if got := chipSegment("main", "", 40); got != "⎇ main" {
		t.Errorf("branch only = %q, want ⎇ main", got)
	}
	// No branch (not a git directory) but inside a worktree.
	if got := chipSegment("", "align-context-meters", 40); got != "⌂ align-context-meters" {
		t.Errorf("worktree only = %q, want the worktree chip", got)
	}
	if got := chipSegment("", "", 40); got != "" {
		t.Errorf("neither = %q, want empty", got)
	}
}

// Wide runes measure two cells each. Truncating by rune count would overrun
// the budget — the regression this file has had twice.
func TestChipSegmentWideRunes(t *testing.T) {
	for _, avail := range []int{6, 10, 20, 40} {
		got := chipSegment(strings.Repeat("囲", 12), strings.Repeat("宽", 12), avail)
		if lipgloss.Width(got) > avail {
			t.Errorf("avail=%d: %q measures %d cells", avail, got, lipgloss.Width(got))
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run TestChipSegment -v`
Expected: FAIL — `undefined: chipSegment`.

- [ ] **Step 3: Write the implementation**

In `cmd/claudemux-head/tui.go`, directly above `renderStateLine`:

```go
// Chip glyphs. "⎇" is a branch symbol and now means the branch; the worktree
// takes "⌂". Until this change the branch glyph fronted the WORKTREE name,
// which is how a session on `lobby-preview` inside the `align-context-meters`
// worktree came to report only the latter.
const (
	branchGlyph       = "⎇ "
	worktreeGlyph     = "⌂ "
	worktreeGlyphBare = "⌂"
	chipSep           = " · "
)

// chipSegment assembles the branch and worktree chips into at most avail
// display cells.
//
// The degradation order is fixed: the worktree name truncates, then falls back
// to its bare glyph, then the branch name truncates, then the branch drops
// entirely. The asymmetry is deliberate — a bare "⌂" still carries information
// (you are in a worktree) while a bare "⎇" carries none, since a session is
// always on some branch. The worktree name is also recoverable elsewhere: it
// names the tmux tab until Haiku takes over. The branch is nowhere else.
func chipSegment(branch, worktree string, avail int) string {
	if avail < 1 {
		return ""
	}
	b, w := "", ""
	if branch != "" {
		b = branchGlyph + branch
	}
	if worktree != "" {
		w = worktreeGlyph + worktree
	}

	switch {
	case b == "" && w == "":
		return ""
	case w == "":
		return fitChip(b, "", avail)
	case b == "":
		return fitChip(w, worktreeGlyphBare, avail)
	}

	sepW := lipgloss.Width(chipSep)
	bareW := lipgloss.Width(worktreeGlyphBare)
	bw := lipgloss.Width(b)

	// Rung 1: both in full.
	if bw+sepW+lipgloss.Width(w) <= avail {
		return b + chipSep + w
	}
	// Rung 2: truncate the worktree name, so long as a cell of it survives.
	if room := avail - bw - sepW; room > bareW+1 {
		return b + chipSep + ansi.Truncate(w, room, "…")
	}
	// Rung 3: the worktree down to its bare glyph.
	if bw+sepW+bareW <= avail {
		return b + chipSep + worktreeGlyphBare
	}
	// Rung 4: truncate the branch, still keeping the worktree glyph.
	if room := avail - sepW - bareW; room > lipgloss.Width(branchGlyph) {
		return ansi.Truncate(b, room, "…") + chipSep + worktreeGlyphBare
	}
	// Rung 5: the worktree glyph alone.
	if bareW <= avail {
		return worktreeGlyphBare
	}
	return ""
}

// fitChip renders a lone chip within avail. bare is what remains when not even
// a truncated name fits — callers pass the worktree's glyph, which means
// something on its own, and "" for the branch, whose glyph does not.
func fitChip(chip, bare string, avail int) string {
	if lipgloss.Width(chip) <= avail {
		return chip
	}
	if bareW := lipgloss.Width(bare); bare != "" && avail <= bareW+1 {
		if bareW <= avail {
			return bare
		}
		return ""
	}
	if avail >= 2 {
		return ansi.Truncate(chip, avail, "…")
	}
	return ""
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/claudemux-head/ -run TestChipSegment -v`
Expected: PASS. If a ladder row disagrees, work the arithmetic by hand before changing the table — the expected strings were computed from the glyph widths, and `⎇ `/`⌂ ` are each 2 cells.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/tui.go cmd/claudemux-head/tui_test.go
git commit -m "feat(head): assemble branch and worktree chips with a width budget"
```

---

### Task 4: Render both chips in both layouts

**Files:**
- Modify: `cmd/claudemux-head/tui.go` (`renderStateLine` ~line 1419, `renderStatusbar` ~line 1290)
- Test: `cmd/claudemux-head/tui_test.go`

**Interfaces:**
- Consumes: `chipSegment` (Task 3), `m.sessionBranch` (Task 2), the existing `m.worktreeChip()` and `noWorktreeWarning`.
- Produces: no new exported surface — this is the wiring.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/claudemux-head/tui_test.go`:

```go
// The point of the feature: branch and worktree are different facts and the
// pane shows both.
func TestStateLineShowsBothChips(t *testing.T) {
	m := model{ready: true, width: 120, height: 2,
		state: State{Kind: StateIdle}, sessionBranch: "lobby-preview",
		sessionCwd: "/tmp/repo/.claude/worktrees/align-context-meters"}
	line := ansi.Strip(renderStateLine(m, time.Now()))
	for _, want := range []string{"⎇ lobby-preview", "⌂ align-context-meters"} {
		if !strings.Contains(line, want) {
			t.Errorf("state line missing %q: %q", want, line)
		}
	}
}

// The warning and the branch occupy different slots, so a marked session that
// never got its worktree still says which branch it is sitting on.
func TestStateLineWarningKeepsBranch(t *testing.T) {
	m := model{ready: true, width: 120, height: 2, state: State{Kind: StateIdle},
		sessionBranch: "main", worktreePending: true, firstPrompt: "do a thing"}
	line := ansi.Strip(renderStateLine(m, time.Now()))
	if !strings.Contains(line, noWorktreeWarning) {
		t.Errorf("warning missing: %q", line)
	}
	if !strings.Contains(line, "⎇ main") {
		t.Errorf("branch missing alongside the warning: %q", line)
	}
}

// Neither layout may ever emit a line wider than the pane, at any width, with
// wide runes in either name.
func TestStatusLinesNeverExceedWidth(t *testing.T) {
	for _, width := range []int{10, 20, 40, 80, 120} {
		m := model{ready: true, width: width, height: 2, state: State{Kind: StateIdle},
			modelName: "claude-opus-5", sessionBranch: strings.Repeat("囲", 12),
			sessionCwd: "/tmp/repo/.claude/worktrees/" + strings.Repeat("宽", 12)}
		if got := lipgloss.Width(renderStateLine(m, time.Now())); got > width {
			t.Errorf("renderStateLine(width=%d) measures %d", width, got)
		}
		if got := lipgloss.Width(renderStatusbar(m, time.Now(), m.worktreeChip())); got > width {
			t.Errorf("renderStatusbar(width=%d) measures %d", width, got)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestStateLineShowsBothChips|TestStateLineWarningKeepsBranch|TestStatusLinesNeverExceedWidth' -v`
Expected: FAIL — the state line renders `⎇ align-context-meters` and no `⌂`.

- [ ] **Step 3: Rewrite renderStateLine's chip assembly**

Replace everything in `renderStateLine` from `chip := m.worktreeChip()` to the end of the function with:

```go
	chip := m.worktreeChip()
	if chip == noWorktreeWarning {
		// Put the warning in with the state/model text, which never shrinks,
		// rather than the chip slot below, which is the first thing to give
		// way as the pane narrows. A worktree NAME is fine to lose — it is
		// re-derivable from the session. This warning is the entire visible
		// mitigation for a design risk and must not be the first casualty.
		// The branch chip still renders beside it: knowing which branch a
		// worktree-less session sits on is exactly what you need to act.
		parts = append(parts, chip)
		chip = ""
	}
	left := strings.Join(parts, " · ")

	avail := m.width - 2 // columns inside the " "..." " padding below
	if avail < 1 {
		avail = 1
	}
	sep := " · "
	chipAvail := avail - lipgloss.Width(left) - lipgloss.Width(sep)
	chips := ""
	if chipAvail > 0 {
		chips = chipSegment(m.sessionBranch, chip, chipAvail)
	}
	if chips == "" {
		return statusbarStyle.Width(m.width).Render(clipLine(" "+left+" ", m.width))
	}
	return statusbarStyle.Width(m.width).Render(clipLine(" "+left+sep+chips+" ", m.width))
```

- [ ] **Step 4: Rewrite renderStatusbar's chip assembly**

In `renderStatusbar`, replace this block:

```go
	if chip == noWorktreeWarning {
		leftParts = append(leftParts, chip)
	} else if chip != "" {
		leftParts = append(leftParts, "⎇ "+truncateRunes(chip, 24))
	}
```

with:

```go
	if chip == noWorktreeWarning {
		// No glyph: this message is about having NO worktree, and the branch
		// chip below still renders beside it.
		leftParts = append(leftParts, chip)
		chip = ""
	}
	// This packed layout has no width budget to hand out — the right-hand
	// gauges are sized after this point — so the chips get a fixed cap and
	// clipLine remains the final guard, exactly as the single chip did before.
	// The cap is in display CELLS now, not runes: the old truncateRunes(chip,
	// 24) under-truncated a wide-rune name to 48 cells.
	if chips := chipSegment(m.sessionBranch, chip, packedChipCells); chips != "" {
		leftParts = append(leftParts, chips)
	}
```

Add the constant beside the glyph constants from Task 3:

```go
// packedChipCells caps the chips in the packed single-line layout, which
// cannot compute a real budget: its right-hand gauges are sized after the
// left group is built. 40 cells is the old 24-rune worktree cap plus room for
// a branch beside it.
const packedChipCells = 40
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -v 2>&1 | tail -15`
Expected: PASS, including every pre-existing status-line test.

- [ ] **Step 6: Verify the whole package**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: no `gofmt` output, `ok github.com/mquinnv/claudemux/cmd/claudemux-head`.

- [ ] **Step 7: See it for real**

```bash
go install ./cmd/claudemux-head
tmux send-keys -t "$(tmux list-panes -a -F '#{pane_id} #{pane_current_command} #{session_name}' | awk '$2=="claudemux-head" && $3!="switchboard"{print $1; exit}')" R
```

Wait a moment, then capture that pane. Expected: the state line reads
`● … · opus-5 · ⎇ <branch> · ⌂ <worktree>` with the branch matching `git branch --show-current` in that session's directory, and the worktree matching the directory under `.claude/worktrees/`.

- [ ] **Step 8: Commit**

```bash
git add cmd/claudemux-head/tui.go cmd/claudemux-head/tui_test.go
git commit -m "feat(head): show branch and worktree as separate chips"
```

---

## Self-Review

**Spec coverage:**

| Spec section | Task |
|---|---|
| `⎇` means branch, `⌂` means worktree | 3 |
| Branch from the transcript, not git | 1, 2 |
| `lastMainCwd` shape: sidechain skipped, quiet poll keeps previous | 2 |
| Degradation ladder, worktree never vanishes | 3 |
| Bare `⎇` never appears | 3 |
| Warning unchanged, keeps left-group placement | 4 |
| Warning and branch coexist | 4 (test) |
| Detached HEAD → `detached` | 2 |
| No `gitBranch` → no branch chip | 1, 3 |
| Session rotation resets the branch | 2 |
| Both layouts | 4 |
| Display-width truncation throughout | 3, 4 |
| Lobby untouched | no task touches it |

**Placeholder scan:** none — every step carries its code or exact command.

**Type consistency:** `lastGitBranch(events, prev)` is defined in Task 2 and called with that signature in Task 2's own wiring. `chipSegment(branch, worktree, avail)` is defined in Task 3 and called with that argument order in Task 4. `sessionBranch` is defined in Task 2 and read in Task 4. `branchGlyph` / `worktreeGlyph` / `worktreeGlyphBare` / `chipSep` are defined in Task 3; `packedChipCells` is added in Task 4 beside them.

**One spec deviation to note:** the spec's testing section lists "`HEAD` mapping to `detached`" under `lastGitBranch`'s table, and this plan implements the mapping there rather than in a separate presentation helper. That keeps the accessor's answer to "what branch is this session on" honest — there is no branch named HEAD — at the cost of mixing one display decision into the accessor.
