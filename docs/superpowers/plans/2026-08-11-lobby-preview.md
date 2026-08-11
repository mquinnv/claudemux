# Lobby Pane Preview Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the claudemux switchboard lobby a live `capture-pane` preview of the selected session's claude pane, rendered in a bordered box below the fleet list.

**Architecture:** A second Bubble Tea command, independent of the existing 1-second fleet poll, runs `tmux capture-pane -e -p -t <pane_id>` for the selected session and returns the raw capture. An in-flight flag serializes captures and a pane-id comparison drops stale ones. All parsing, sizing, and box rendering are pure functions over strings, tested without tmux.

**Tech Stack:** Go, Bubble Tea, lipgloss, `github.com/charmbracelet/x/ansi` (already a direct dependency at v0.11.6). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-11-lobby-preview-design.md`

## Global Constraints

- Work in the existing worktree `.claude/worktrees/align-context-meters`, branch `worktree-align-context-meters`. Do not create another worktree.
- No new module dependencies. `ansi`, `lipgloss`, and `bubbletea` are already imported in this package.
- Every task ends green: `go build ./... && go vet ./... && go test ./cmd/claudemux-head/` and `gofmt -l .` empty.
- Follow the package's comment style: comments explain **why** a rule exists, not what the line does. Match the density of the surrounding code.
- No tmux in tests. Every new function must be testable as a pure function over strings; the only impure piece is `swPreviewCmd`, which is not unit-tested.
- Preview failures must never disturb the fleet list, `lastErr`, or the conductor.

## File Structure

| File | Responsibility |
|---|---|
| `cmd/claudemux-head/switchboard.go` (modify) | Snapshot model. Gains `swSession.ClaudePane` and the pane-matching rule. |
| `cmd/claudemux-head/swpreview.go` (create) | Everything preview: the capture command and message, the tail extractor, the box renderer, the layout arithmetic. |
| `cmd/claudemux-head/swpreview_test.go` (create) | Tests for the above pure functions. |
| `cmd/claudemux-head/switchboardtui.go` (modify) | Model fields, Update wiring, View integration and the list row cap. |
| `cmd/claudemux-head/switchboardtui_test.go` (modify) | Model and View tests. |
| `cmd/claudemux-head/switchboard_test.go` (modify) | Snapshot tests for `ClaudePane`. |

Preview logic lives in its own file rather than growing `switchboardtui.go` (already 244 lines and holding the model, the poll, the key handling, and the view). The new file owns one concern and can be read in full while working on it.

---

### Task 1: Record each session's claude pane in the snapshot

**Files:**
- Modify: `cmd/claudemux-head/switchboard.go` (the `swSession` struct ~line 19, `buildSwSnapshot` ~line 62)
- Test: `cmd/claudemux-head/switchboard_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `swSession.ClaudePane string` — the tmux pane id (e.g. `"%2"`) of the session's claude pane, `""` when it has none. Every later task reads this field.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/claudemux-head/switchboard_test.go`:

```go
// The preview needs the claude pane, not the head pane: swPaneOut gives api
// both, web only a head pane.
func TestBuildSwSnapshotRecordsClaudePane(t *testing.T) {
	s := buildSwSnapshot(swSessOut, swPaneOut, swClientOut, "%9")
	api, ok := s.session("api")
	if !ok || api.ClaudePane != "%2" {
		t.Errorf("api.ClaudePane = %q, want %%2", api.ClaudePane)
	}
	web, ok := s.session("web")
	if !ok || web.ClaudePane != "" {
		t.Errorf("web.ClaudePane = %q, want empty: it has no claude pane", web.ClaudePane)
	}
}

// "node" identifies claude only as a fallback. A session that has both a real
// claude pane and some other node process must preview claude, whatever order
// tmux lists them in.
func TestBuildSwSnapshotPrefersClaudeOverNode(t *testing.T) {
	paneOut := "api\t%1\tclaudemux-head\ttopic\n" +
		"api\t%2\tnode\ttopic\n" +
		"api\t%3\tclaude\ttopic\n" +
		"shim\t%4\tclaudemux-head\ttopic\n" +
		"shim\t%5\tnode\ttopic\n"
	sessOut := "api\tIdle\t1754700000\t37\t\t\n" +
		"shim\tIdle\t1754700000\t37\t\t\n"
	s := buildSwSnapshot(sessOut, paneOut, swClientOut, "")
	api, _ := s.session("api")
	if api.ClaudePane != "%3" {
		t.Errorf("api.ClaudePane = %q, want %%3: a real claude pane outranks node", api.ClaudePane)
	}
	shim, _ := s.session("shim")
	if shim.ClaudePane != "%5" {
		t.Errorf("shim.ClaudePane = %q, want %%5: node is the fallback", shim.ClaudePane)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run TestBuildSwSnapshot -v`
Expected: FAIL — `api.ClaudePane undefined (type swSession has no field or method ClaudePane)` (a compile error; both tests fail together).

- [ ] **Step 3: Add the field**

In `cmd/claudemux-head/switchboard.go`, add to `swSession` after `Prompt`:

```go
	Prompt  string // the last typed prompt (@claudemux_prompt)
	// ClaudePane is the tmux pane id running claude, "" when the session has
	// none. The lobby previews this pane rather than the session's active one:
	// a session left focused on its shell would preview a shell prompt, and
	// one left on its head pane would preview the four rows the lobby row
	// already summarizes.
	ClaudePane string
```

- [ ] **Step 4: Populate it in buildSwSnapshot**

In `cmd/claudemux-head/switchboard.go`, above `buildSwSnapshot`:

```go
// swClaudeCommand / swClaudeShimCommand are the pane_current_command values
// that identify a claude pane. The shim is only a fallback: some runtimes
// report "node" for claude, but plenty of unrelated processes report it too
// (a dev server in a shell pane), so a real claude pane always wins. Same
// preference order claudePaneCandidates uses — see panemap.go.
const (
	swClaudeCommand     = "claude"
	swClaudeShimCommand = "node"
)
```

Then in the pane loop, add the recording (the loop already walks every pane for `heads`, `topics`, and `snap.Lobby`):

```go
	heads := map[string]bool{}
	topics := map[string]string{}
	claudePanes := map[string]string{}
	shimPanes := map[string]string{}
	for _, line := range strings.Split(paneOut, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			continue
		}
		if f[2] == swHeadCommand {
			heads[f[0]] = true
			topics[f[0]] = f[3]
		}
		// First pane of each kind wins, so the previewed pane is stable
		// across polls even when a session has several candidates.
		switch f[2] {
		case swClaudeCommand:
			if _, ok := claudePanes[f[0]]; !ok {
				claudePanes[f[0]] = f[1]
			}
		case swClaudeShimCommand:
			if _, ok := shimPanes[f[0]]; !ok {
				shimPanes[f[0]] = f[1]
			}
		}
		if f[1] == selfPane && selfPane != "" {
			snap.Lobby = f[0]
		}
	}
```

And in the session loop, next to the existing `sess.Topic = topics[sess.Name]`:

```go
		sess.Topic = topics[sess.Name]
		sess.ClaudePane = claudePanes[sess.Name]
		if sess.ClaudePane == "" {
			sess.ClaudePane = shimPanes[sess.Name]
		}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -run TestBuildSwSnapshot -v`
Expected: PASS, including the pre-existing `TestBuildSwSnapshot`.

- [ ] **Step 6: Commit**

```bash
git add cmd/claudemux-head/switchboard.go cmd/claudemux-head/switchboard_test.go
git commit -m "feat(head): record each session's claude pane in the lobby snapshot"
```

---

### Task 2: previewTail — the last useful lines of a capture

**Files:**
- Create: `cmd/claudemux-head/swpreview.go`
- Create: `cmd/claudemux-head/swpreview_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `previewTail(capture string, n int) []string` — the last `n` lines of `capture` after trailing blank lines are dropped. Returns at most `n` entries and may return fewer, including none.

- [ ] **Step 1: Write the failing test**

Create `cmd/claudemux-head/swpreview_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestPreviewTail(t *testing.T) {
	tests := []struct {
		name    string
		capture string
		n       int
		want    []string
	}{
		{"tail of a long capture", "a\nb\nc\nd\ne\n", 3, []string{"c", "d", "e"}},
		{"shorter than requested", "a\nb\n", 5, []string{"a", "b"}},
		{"exactly enough", "a\nb\n", 2, []string{"a", "b"}},
		// A claude pane sitting at its input box ends in blank rows; an
		// untrimmed tail would be mostly empty.
		{"trailing blanks trimmed", "a\nb\n\n   \n\n", 2, []string{"a", "b"}},
		// tmux emits colored blanks: a line that is nothing but an SGR reset
		// is still blank.
		{"ansi-only lines are blank", "a\nb\n\x1b[39m\n\x1b[0m   \n", 2, []string{"a", "b"}},
		{"blank interior lines kept", "a\n\nb\n", 3, []string{"a", "", "b"}},
		{"all blank", "\n\n   \n", 3, nil},
		{"empty", "", 3, nil},
		{"zero lines requested", "a\nb\n", 0, nil},
		{"negative lines requested", "a\nb\n", -1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := previewTail(tt.capture, tt.n)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("previewTail(%q, %d) = %q, want %q", tt.capture, tt.n, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run TestPreviewTail -v`
Expected: FAIL — `undefined: previewTail`.

- [ ] **Step 3: Write the implementation**

Create `cmd/claudemux-head/swpreview.go`:

```go
package main

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// The lobby's preview box: capturing the selected session's claude pane and
// drawing it below the fleet list. Everything here except swPreviewCmd is a
// pure function over strings, so the whole feature is testable without tmux.

// previewTail returns the last n lines of a captured pane, after trailing
// blank lines are dropped.
//
// The tail, not the head: a claude pane's newest turn and its input box are at
// the BOTTOM, and that is what tells you whether the session needs you. The
// trim matters because a pane parked at an idle input box ends in several
// blank rows — an untrimmed tail would spend most of the box on them. Blank is
// judged after stripping SGR, since tmux happily emits a line that is nothing
// but a color reset.
func previewTail(capture string, n int) []string {
	if n <= 0 {
		return nil
	}
	lines := strings.Split(strings.TrimRight(capture, "\n"), "\n")
	end := len(lines)
	for end > 0 && strings.TrimSpace(ansi.Strip(lines[end-1])) == "" {
		end--
	}
	if end == 0 {
		return nil
	}
	lines = lines[:end]
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/claudemux-head/ -run TestPreviewTail -v`
Expected: PASS (all 10 subtests).

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/swpreview.go cmd/claudemux-head/swpreview_test.go
git commit -m "feat(head): add previewTail for the lobby preview capture"
```

---

### Task 3: renderPreview — the bordered box

**Files:**
- Modify: `cmd/claudemux-head/swpreview.go`
- Test: `cmd/claudemux-head/swpreview_test.go`

**Interfaces:**
- Consumes: `swPad(s string, w int) string` and `truncateRunes(s string, max int) string`, both already in the package (`switchboardtui.go` and `tui.go`).
- Produces: `renderPreview(title string, lines []string, width, height int) []string` — exactly `height+2` strings (top border, `height` content rows, bottom border), each exactly `width` display cells. Returns `nil` when `width < 8` or `height < 1`.

- [ ] **Step 1: Write the failing test**

Add to `cmd/claudemux-head/swpreview_test.go` (the file already imports `strings` and `testing`; add `github.com/charmbracelet/lipgloss` and `github.com/charmbracelet/x/ansi`):

```go
func TestRenderPreviewGeometry(t *testing.T) {
	box := renderPreview("cd-receiver", []string{"one", "two"}, 40, 5)
	if len(box) != 7 {
		t.Fatalf("box has %d lines, want 7 (5 content + 2 border):\n%s", len(box), strings.Join(box, "\n"))
	}
	for i, line := range box {
		if w := lipgloss.Width(line); w != 40 {
			t.Errorf("line %d is %d cells, want 40: %q", i, w, ansi.Strip(line))
		}
	}
	if !strings.Contains(ansi.Strip(box[0]), "cd-receiver") {
		t.Errorf("top border must carry the title: %q", ansi.Strip(box[0]))
	}
	// Fewer lines than the box is tall: the remainder is blank, not missing.
	if got := strings.TrimSpace(ansi.Strip(box[4])); got != "" {
		t.Errorf("unused row %q, want blank", got)
	}
}

// A colored capture must not overflow the box or leak its color past the
// border — the two ways an embedded ANSI line breaks a TUI.
func TestRenderPreviewClipsAndResetsAnsi(t *testing.T) {
	long := "\x1b[38;5;114m" + strings.Repeat("wide ", 40)
	box := renderPreview("api", []string{long}, 30, 1)
	if len(box) != 3 {
		t.Fatalf("box has %d lines, want 3", len(box))
	}
	if w := lipgloss.Width(box[1]); w != 30 {
		t.Errorf("content row is %d cells, want 30", w)
	}
	if !strings.Contains(box[1], "\x1b[0m") {
		t.Errorf("content row must be reset-terminated so color cannot bleed: %q", box[1])
	}
}

// An over-long title cannot be allowed to push the border out.
func TestRenderPreviewTruncatesTitle(t *testing.T) {
	box := renderPreview(strings.Repeat("x", 100), nil, 20, 1)
	if w := lipgloss.Width(box[0]); w != 20 {
		t.Errorf("top border is %d cells, want 20: %q", w, ansi.Strip(box[0]))
	}
}

func TestRenderPreviewRefusesTinyBoxes(t *testing.T) {
	if got := renderPreview("api", nil, 4, 3); got != nil {
		t.Errorf("width 4 must render nothing, got %q", got)
	}
	if got := renderPreview("api", nil, 40, 0); got != nil {
		t.Errorf("height 0 must render nothing, got %q", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run TestRenderPreview -v`
Expected: FAIL — `undefined: renderPreview`.

- [ ] **Step 3: Write the implementation**

Append to `cmd/claudemux-head/swpreview.go`, and add `"github.com/charmbracelet/lipgloss"` to its imports:

```go
// swPreviewBorderStyle dims the box frame so the captured pane, which brings
// its own colors, stays the loudest thing in it.
var swPreviewBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

// ansiReset closes any color a captured line left open. Without it a truncated
// mid-color line paints the border, and everything after it, in that color.
const ansiReset = "\x1b[0m"

// renderPreview draws the preview box: a titled top border, exactly height
// content rows, and a bottom border. Every returned line is exactly width
// display cells, which is what keeps the lobby's one-row-per-line invariant
// under a payload the lobby did not generate.
//
// Returns nil for a box too small to be worth drawing — the caller renders
// nothing rather than a frame with no room inside it.
func renderPreview(title string, lines []string, width, height int) []string {
	if width < 8 || height < 1 {
		return nil
	}
	inner := width - 4 // "│ " + content + " │"
	out := make([]string, 0, height+2)
	out = append(out, previewTopBorder(title, width))
	edge := swPreviewBorderStyle.Render("│")
	for i := 0; i < height; i++ {
		content := ""
		if i < len(lines) {
			content = ansi.Truncate(lines[i], inner, "…") + ansiReset
		}
		out = append(out, edge+" "+swPad(content, inner)+" "+edge)
	}
	out = append(out, swPreviewBorderStyle.Render("└"+strings.Repeat("─", width-2)+"┘"))
	return out
}

// previewTopBorder builds "┌─ title ──────┐", clipping the title to whatever
// the width allows and dropping it entirely when nothing fits.
func previewTopBorder(title string, width int) string {
	// "┌─ " + title + " " + fill + "┐": 5 cells of frame around the title.
	if room := width - 5; room < 1 {
		title = ""
	} else {
		title = truncateRunes(title, room)
	}
	if title == "" {
		return swPreviewBorderStyle.Render("┌" + strings.Repeat("─", width-2) + "┐")
	}
	fill := width - 5 - lipgloss.Width(title)
	if fill < 0 {
		fill = 0
	}
	return swPreviewBorderStyle.Render("┌─ " + title + " " + strings.Repeat("─", fill) + "┐")
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -run TestRenderPreview -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/swpreview.go cmd/claudemux-head/swpreview_test.go
git commit -m "feat(head): render the lobby preview box"
```

---

### Task 4: computePreviewLayout — how many rows each part gets

**Files:**
- Modify: `cmd/claudemux-head/swpreview.go`
- Test: `cmd/claudemux-head/swpreview_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:

```go
type swLayout struct {
	Show     bool // draw the preview box at all
	Content  int  // preview content rows, border excluded
	ListRows int  // cap on session-list rows; 0 means uncapped
}

func computePreviewLayout(height int, hasErr bool) swLayout
```

- [ ] **Step 1: Write the failing test**

Add to `cmd/claudemux-head/swpreview_test.go`:

```go
func TestComputePreviewLayout(t *testing.T) {
	tests := []struct {
		name   string
		height int
		hasErr bool
		want   swLayout
	}{
		// avail 40 -> 40/3 = 13 content, list 40-15 = 25.
		{"a full-screen lobby", 46, false, swLayout{Show: true, Content: 13, ListRows: 25}},
		// avail 18 -> 6 content (floor), list 18-8 = 10.
		{"a small lobby hits the floor", 24, false, swLayout{Show: true, Content: 6, ListRows: 10}},
		// avail 94 -> 31 clamped to 16, list 94-18 = 76.
		{"a tall lobby hits the ceiling", 100, false, swLayout{Show: true, Content: 16, ListRows: 76}},
		// avail 10 -> 6 content, list 2: the last height that fits both.
		{"the smallest lobby with a preview", 16, false, swLayout{Show: true, Content: 6, ListRows: 2}},
		// avail 9 -> list would be 1: fleet wins, preview is dropped.
		{"too short for both", 15, false, swLayout{}},
		{"no size yet", 0, false, swLayout{}},
		// An error line costs a row: avail 39 -> 13 content, list 39-15 = 24.
		{"an error line shifts the budget", 46, true, swLayout{Show: true, Content: 13, ListRows: 24}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computePreviewLayout(tt.height, tt.hasErr); got != tt.want {
				t.Errorf("computePreviewLayout(%d, %v) = %+v, want %+v", tt.height, tt.hasErr, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run TestComputePreviewLayout -v`
Expected: FAIL — `undefined: swLayout`, `undefined: computePreviewLayout`.

- [ ] **Step 3: Write the implementation**

Append to `cmd/claudemux-head/swpreview.go`:

```go
// Preview sizing. The box claims its share BEFORE the fleet list rather than
// taking the leftovers: giving the list what it wants first would let a large
// fleet squeeze the preview out, which is exactly when a preview is most
// useful.
const (
	swPreviewMinRows = 6
	swPreviewMaxRows = 16
	// swChromeRows is what View spends on things that are neither list nor
	// preview: title, the blank under it, the blank above the box, the blank
	// above the status line, and the hints line. A tmux error line costs one
	// more.
	swChromeRows = 6
)

// swLayout is the row budget for one View pass. ListRows is a CAP, and 0 means
// uncapped — the state the lobby is in today and stays in whenever the preview
// is not drawn.
type swLayout struct {
	Show     bool
	Content  int
	ListRows int
}

// computePreviewLayout divides the pane's rows between the fleet list and the
// preview box. A pane that can show the fleet or a preview but not both shows
// the fleet: the lobby's job is ferrying clients, and the preview is decoration
// on top of that.
func computePreviewLayout(height int, hasErr bool) swLayout {
	chrome := swChromeRows
	if hasErr {
		chrome++
	}
	avail := height - chrome
	if avail < 1 {
		return swLayout{}
	}
	content := avail / 3
	if content < swPreviewMinRows {
		content = swPreviewMinRows
	}
	if content > swPreviewMaxRows {
		content = swPreviewMaxRows
	}
	// +2 for the box's own borders. Under two rows there is not even one
	// session row left, so the box is not worth its cost.
	list := avail - (content + 2)
	if list < 2 {
		return swLayout{}
	}
	return swLayout{Show: true, Content: content, ListRows: list}
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/claudemux-head/ -run TestComputePreviewLayout -v`
Expected: PASS (7 subtests).

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/swpreview.go cmd/claudemux-head/swpreview_test.go
git commit -m "feat(head): add the lobby preview row budget"
```

---

### Task 5: Capture the pane and hold the result in the model

**Files:**
- Modify: `cmd/claudemux-head/swpreview.go` (the command and message)
- Modify: `cmd/claudemux-head/switchboardtui.go` (model fields ~line 38, `Update` ~line 104)
- Test: `cmd/claudemux-head/switchboardtui_test.go`

**Interfaces:**
- Consumes: `swSession.ClaudePane` (Task 1); `swTmux(ctx, args...) (string, error)` (already in `switchboardtui.go`).
- Produces:
  - `swPreviewMsg{pane string; out string; err error}`
  - `swPreviewCmd(pane string) tea.Cmd`
  - `(swModel) selectedPane() string`
  - `(*swModel) previewCmd() tea.Cmd` — pointer receiver; sets the in-flight flag and returns `nil` when there is nothing to request.
  - Model fields `previewPane`, `previewOut string`, `previewErr`, `previewInFlight bool`. Task 6 reads `previewOut`, `previewErr`, and `selectedPane()`.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/claudemux-head/switchboardtui_test.go`. Note `swTestModel`'s sessions carry no `ClaudePane`, so set them here:

```go
// swPreviewModel is swTestModel with claude panes attached — the preview needs
// something to capture.
func swPreviewModel() swModel {
	m := swTestModel()
	m.height = 46
	m.snap.Sessions[0].ClaudePane = "%2"
	m.snap.Sessions[1].ClaudePane = "%5"
	// Sessions[2] ("scratch") deliberately keeps none.
	return m
}

func TestSwModelSelectionRequestsPreview(t *testing.T) {
	m := swPreviewModel()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if cmd == nil {
		t.Fatal("moving the selection must request a capture")
	}
	if !next.(swModel).previewInFlight {
		t.Error("a requested capture must be marked in flight")
	}
}

func TestSwModelNoPreviewWithoutClaudePane(t *testing.T) {
	m := swPreviewModel()
	m.sel = 2 // scratch: no claude pane
	if cmd := m.previewCmd(); cmd != nil {
		t.Error("a session with no claude pane must not request a capture")
	}
}

func TestSwModelPreviewInFlightGuard(t *testing.T) {
	m := swPreviewModel()
	if cmd := m.previewCmd(); cmd == nil {
		t.Fatal("the first request must fire")
	}
	if cmd := m.previewCmd(); cmd != nil {
		t.Error("a second request must be dropped while one is in flight")
	}
}

func TestSwModelStorePreview(t *testing.T) {
	m := swPreviewModel()
	m.previewInFlight = true
	next, _ := m.Update(swPreviewMsg{pane: "%2", out: "hello\n"})
	got := next.(swModel)
	if got.previewOut != "hello\n" || got.previewPane != "%2" {
		t.Errorf("preview not stored: out=%q pane=%q", got.previewOut, got.previewPane)
	}
	if got.previewInFlight {
		t.Error("a landed capture must clear the in-flight flag")
	}
	if got.previewErr {
		t.Error("a successful capture must not be flagged as an error")
	}
}

// A capture that lands after the cursor moved belongs to a session the user is
// no longer looking at; painting it would flash the wrong screen under the
// right title.
func TestSwModelDropsStalePreview(t *testing.T) {
	m := swPreviewModel()
	m.previewOut = "current"
	m.previewPane = "%2"
	m.previewInFlight = true
	next, _ := m.Update(swPreviewMsg{pane: "%99", out: "stale"})
	got := next.(swModel)
	if got.previewOut != "current" {
		t.Errorf("previewOut = %q, want the current capture kept", got.previewOut)
	}
	if got.previewInFlight {
		t.Error("even a dropped capture must clear the in-flight flag, or the preview wedges")
	}
}

func TestSwModelPreviewErrorFlagged(t *testing.T) {
	m := swPreviewModel()
	m.previewInFlight = true
	next, _ := m.Update(swPreviewMsg{pane: "%2", err: errors.New("no such pane")})
	got := next.(swModel)
	if !got.previewErr {
		t.Error("a failed capture must be flagged")
	}
	if got.lastErr != "" {
		t.Errorf("lastErr = %q: a preview failure must not claim the fleet-poll error line", got.lastErr)
	}
}
```

Add `"errors"` to that file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestSwModel(Selection|NoPreview|PreviewInFlight|StorePreview|DropsStale|PreviewError)' -v`
Expected: FAIL — `undefined: swPreviewMsg`, `m.previewCmd undefined`, `previewInFlight undefined`.

- [ ] **Step 3: Add the command and message**

Append to `cmd/claudemux-head/swpreview.go`, adding `"context"`, `"time"`, and `tea "github.com/charmbracelet/bubbletea"` to its imports:

```go
// swPreviewMsg is one finished capture. It carries the pane it came from so
// Update can drop a result whose session is no longer selected — without that
// check, a fast j/k paints a previous session's screen under the new title.
type swPreviewMsg struct {
	pane string
	out  string
	err  error
}

// swPreviewCmd captures a pane off the update loop. -e keeps the pane's SGR
// colors, which is what makes a claude pane scannable at a glance; capture-pane
// emits no cursor motion or OSC, so the result is safe to embed in a view.
func swPreviewCmd(pane string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := swTmux(ctx, "capture-pane", "-e", "-p", "-t", pane)
		return swPreviewMsg{pane: pane, out: out, err: err}
	}
}
```

- [ ] **Step 4: Add the model fields and helpers**

In `cmd/claudemux-head/switchboardtui.go`, add to `swModel` after `standby`:

```go
	standby bool
	// The preview of the selected session's claude pane. previewPane records
	// which pane previewOut came from; previewInFlight serializes captures so
	// a slow tmux cannot queue them up behind each other.
	previewPane     string
	previewOut      string
	previewErr      bool
	previewInFlight bool
```

And after `newSwModel`:

```go
// selectedPane returns the claude pane of the selected session — "" when there
// is no selection, or when that session has no claude pane to capture.
func (m swModel) selectedPane() string {
	if m.sel < 0 || m.sel >= len(m.snap.Sessions) {
		return ""
	}
	return m.snap.Sessions[m.sel].ClaudePane
}

// previewCmd requests a capture of the current selection, returning nil when
// there is nothing to capture or one is already running. Moving off the pane
// the held capture came from clears it, so the box never shows one session's
// screen under another's title while the new capture is in flight.
func (m *swModel) previewCmd() tea.Cmd {
	pane := m.selectedPane()
	if pane != m.previewPane {
		m.previewOut, m.previewPane, m.previewErr = "", "", false
	}
	if pane == "" || m.previewInFlight {
		return nil
	}
	m.previewInFlight = true
	return swPreviewCmd(pane)
}
```

- [ ] **Step 5: Wire it into Update**

In `cmd/claudemux-head/switchboardtui.go`, in the `swSnapshotMsg` case, replace the tail of the success path:

```go
		// Refresh the preview on the same beat as the fleet. tea.Batch drops
		// nil commands, so this is a no-op when there is nothing to capture.
		pv := m.previewCmd()
		if !m.standby {
			if act, ok := m.cond.step(m.snap); ok {
				return m, tea.Batch(swNextTick(), swSwitchCmd(act.Client, act.Target), pv)
			}
		}
		return m, tea.Batch(swNextTick(), pv)
```

Add the message case alongside `swSnapshotMsg`:

```go
	case swPreviewMsg:
		// Always clear the flag, stale or not: a dropped result that left the
		// flag set would wedge the preview for the rest of the session.
		m.previewInFlight = false
		if msg.pane != m.selectedPane() {
			return m, nil
		}
		m.previewPane = msg.pane
		m.previewErr = msg.err != nil
		m.previewOut = msg.out
		return m, nil
```

And in the key handler, make `j`/`k` request a capture immediately — waiting for the next poll would lag the cursor by up to a second, which reads as a broken UI rather than a slow one:

```go
		case "j", "down":
			if m.sel < len(m.snap.Sessions)-1 {
				m.sel++
				return m, m.previewCmd()
			}
		case "k", "up":
			if m.sel > 0 {
				m.sel--
				return m, m.previewCmd()
			}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -v 2>&1 | tail -20`
Expected: PASS, including the pre-existing selection, standby, and conductor tests.

- [ ] **Step 7: Commit**

```bash
git add cmd/claudemux-head/swpreview.go cmd/claudemux-head/switchboardtui.go cmd/claudemux-head/switchboardtui_test.go
git commit -m "feat(head): capture the selected session's pane for the lobby preview"
```

---

### Task 6: Draw the preview and cap the fleet list

**Files:**
- Modify: `cmd/claudemux-head/switchboardtui.go` (`View` ~line 163)
- Test: `cmd/claudemux-head/switchboardtui_test.go`

**Interfaces:**
- Consumes: `computePreviewLayout` (Task 4), `renderPreview` and `previewTail` (Tasks 2-3), `selectedPane`, `previewOut`, `previewErr` (Task 5).
- Produces: no new exported surface — this is the final wiring.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/claudemux-head/switchboardtui_test.go`:

```go
func TestSwModelViewShowsPreview(t *testing.T) {
	m := swPreviewModel()
	m.previewPane = "%2"
	m.previewOut = "● Bash(git push)\n⎿  pushed\n"
	view := ansi.Strip(m.View())
	for _, want := range []string{"● Bash(git push)", "⎿  pushed", "api"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestSwModelViewPreviewOmittedWhenShort(t *testing.T) {
	m := swPreviewModel()
	m.height = 15 // below the floor: fleet wins
	m.previewPane = "%2"
	m.previewOut = "captured content here\n"
	if view := ansi.Strip(m.View()); strings.Contains(view, "captured content here") {
		t.Errorf("a short pane must drop the preview, not squeeze it:\n%s", view)
	}
}

func TestSwModelViewPreviewPlaceholders(t *testing.T) {
	m := swPreviewModel()
	m.sel = 2 // scratch has no claude pane
	if view := ansi.Strip(m.View()); !strings.Contains(view, "no claude pane") {
		t.Errorf("a session with no claude pane needs a reason in the box:\n%s", view)
	}

	m = swPreviewModel()
	m.previewErr = true
	if view := ansi.Strip(m.View()); !strings.Contains(view, "preview unavailable") {
		t.Errorf("a failed capture needs a reason in the box:\n%s", view)
	}
}

// The preview must not be pushed off the bottom by a long fleet: the list is
// capped and says so.
func TestSwModelViewCapsListForPreview(t *testing.T) {
	m := swPreviewModel()
	m.height = 20
	var sessions []swSession
	for i := 0; i < 30; i++ {
		sessions = append(sessions, swSession{
			Name: fmt.Sprintf("sess-%02d", i), State: "Idle", Context: -1, ClaudePane: "%2",
		})
	}
	m.snap.Sessions = sessions
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "more") {
		t.Errorf("a capped list must say how many it dropped:\n%s", view)
	}
	if strings.Contains(view, "sess-29") {
		t.Errorf("the list must be capped, not rendered in full:\n%s", view)
	}
	if lines := strings.Count(view, "\n"); lines > m.height {
		t.Errorf("view is %d lines, want at most %d", lines, m.height)
	}
}

func TestSwModelViewEmptyFleetHasNoPreview(t *testing.T) {
	m := newSwModel("%9")
	m.width, m.height = 80, 46
	if view := ansi.Strip(m.View()); strings.Contains(view, "┌") {
		t.Errorf("an empty fleet must not draw a preview box:\n%s", view)
	}
}
```

Add `"fmt"` to that file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestSwModelView(ShowsPreview|PreviewOmitted|PreviewPlaceholders|CapsList|EmptyFleetHasNo)' -v`
Expected: FAIL — the preview box is never rendered, so the content, placeholder, and "more" assertions all miss.

- [ ] **Step 3: Cap the list in View**

In `cmd/claudemux-head/switchboardtui.go`, at the top of `View` after the title block, compute the layout and the row budget. The list loop currently writes each session unconditionally; give it a budget first:

```go
	lay := computePreviewLayout(m.height, m.lastErr != "")

	// Rows each session wants: 2 when it has a detail line, else 1. Only when
	// the total overflows the cap is anything dropped — and then one row is
	// held back for the "+N more" line that says so.
	budget := lay.ListRows
	if budget > 0 {
		want := 0
		for _, sess := range m.snap.Sessions {
			want += swSessionRows(sess)
		}
		if want <= budget {
			budget = 0 // everything fits; no cap needed
		} else {
			budget--
		}
	}
```

Add the helper next to `View`:

```go
// swSessionRows is how many lines a session's row occupies: two when it has a
// summary or prompt to show under it, one when it has neither. Kept next to
// the detail logic in View, which must agree with it.
func swSessionRows(sess swSession) int {
	if sess.Summary != "" || sess.Prompt != "" {
		return 2
	}
	return 1
}
```

Then in the session loop, stop when the budget is spent, counting as you go:

```go
	used := 0
	shown := 0
	for i, sess := range m.snap.Sessions {
		if budget > 0 && used+swSessionRows(sess) > budget {
			break
		}
		// ... existing row rendering, unchanged ...
		used += swSessionRows(sess)
		shown++
	}
	if shown < len(m.snap.Sessions) {
		fmt.Fprintf(&b, "    %s\n",
			swStatusStyle.Render(fmt.Sprintf("… +%d more", len(m.snap.Sessions)-shown)))
	}
```

- [ ] **Step 4: Draw the box**

Still in `View`, after the session loop and before the status line:

```go
	if lay.Show && len(m.snap.Sessions) > 0 {
		b.WriteString("\n")
		var lines []string
		switch {
		case m.selectedPane() == "":
			lines = []string{swStatusStyle.Render("no claude pane")}
		case m.previewErr:
			lines = []string{swStatusStyle.Render("preview unavailable")}
		default:
			lines = previewTail(m.previewOut, lay.Content)
		}
		for _, l := range renderPreview(m.snap.Sessions[m.sel].Name, lines, m.width, lay.Content) {
			b.WriteString(l + "\n")
		}
	}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -v 2>&1 | tail -20`
Expected: PASS — the new View tests and every pre-existing switchboard test.

- [ ] **Step 6: Verify the whole package and formatting**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: no output from `gofmt -l`, `ok github.com/mquinnv/claudemux/cmd/claudemux-head`.

- [ ] **Step 7: See it for real**

```bash
go install ./cmd/claudemux-head
tmux respawn-pane -k -t "$(tmux list-panes -a -F '#{pane_id} #{session_name}' | awk '$2=="switchboard"{print $1; exit}')" 'exec claudemux-head switchboard'
tmux capture-pane -p -t "$(tmux list-panes -a -F '#{pane_id} #{session_name}' | awk '$2=="switchboard"{print $1; exit}')"
```

Expected: the fleet list, then a bordered box titled with the selected session's name holding the tail of that session's claude pane. Confirm `j`/`k` changes the box's title and contents.

- [ ] **Step 8: Commit**

```bash
git add cmd/claudemux-head/switchboardtui.go cmd/claudemux-head/switchboardtui_test.go
git commit -m "feat(head): draw the lobby preview box and cap the fleet list"
```

---

## Self-Review

**Spec coverage:**

| Spec section | Task |
|---|---|
| Bottom, full-width placement | 6 |
| Sizing arithmetic, [6,16] clamp, omit-when-short | 4, 6 |
| List row cap with `… +N more` | 6 |
| Two trigger points (poll + selection change) | 5 |
| In-flight flag | 5 |
| Stale-drop by pane id | 5 |
| `ClaudePane` on the snapshot, claude-over-node preference | 1 |
| `previewTail` (trailing-blank trim, last N) | 2 |
| `ansi.Truncate` + per-line reset | 3 |
| Failure modes: capture failed, no claude pane, empty fleet, too short | 6 |
| `lastErr` untouched by preview failures | 5 |
| No new keys | none needed — no task adds a binding |

**Type consistency:** `swLayout{Show, Content, ListRows}` is defined in Task 4 and consumed in Task 6 with those exact names. `swPreviewMsg{pane, out, err}` is defined in Task 5 and constructed with those names in its own tests. `renderPreview(title, lines, width, height)` is defined in Task 3 and called with that argument order in Task 6. `previewTail(capture, n)` likewise. `swSession.ClaudePane` is defined in Task 1 and read in Tasks 5 and 6.
