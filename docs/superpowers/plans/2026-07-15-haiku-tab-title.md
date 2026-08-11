# Haiku Tab Title Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generate a short Haiku tab label per session and apply it as the tmux window name (which tmux propagates to the terminal tab), gated by a `summary.tab_title` config toggle.

**Architecture:** The summarizer's tool gains a third field, `tab`. When a fresh summary lands, `claudemux-head` renames its own tmux window to that label — fire-and-forget with a hard timeout, off the render loop, like the existing `tmux list-panes` poll. The launcher enables `set-titles on` per session so the window name reaches the terminal tab. A `tab_title` config bool (default true) turns the renaming off without disabling summaries.

**Tech Stack:** Go 1.26, bubbletea, anthropic-sdk-go, bash, tmux.

## Global Constraints

- The `tab` label: **2–4 words, lowercase, ≤ 24 characters, no punctuation**, derived from the durable goal (like `topic`), not the current step.
- Renaming happens **only** inside tmux (`TMUX_PANE` set) and **only** when `summary.tab_title` is true.
- The tmux call must never block or crash the TUI: hard-timeout subprocess, off the render loop, failure ignored — identical discipline to `panemap.go`'s `claudePaneCandidatesLive`.
- `tab_title` defaults to **true** and is independent of `summary.enabled`.
- Never `os.UserConfigDir()`.
- Every task ends green: `go build ./... && go vet ./... && go test ./...` (plus `bash -n`/`shellcheck` for the launcher task).
- This is a user-facing feature → it releases as **v1.1.0** (tagging is out of plan scope; the controller cuts the release).

---

### Task 1: Summarizer emits a `tab` field

**Files:**
- Modify: `cmd/claudemux-head/summary.go` (`Summary` struct ~137, the tool schema + placeholder guard in `Summarize` ~232–268, and `summarySystemPrompt` ~88–110)
- Modify: `cmd/claudemux-head/summary_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `Summary` gains `Tab string \`json:"tab"\``. `Summarize` returns it populated, or errors if `tab` is a placeholder.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/claudemux-head/summary_test.go`. These test the pure, non-network pieces: the struct shape via JSON, the placeholder guard's coverage of `tab`, and the prompt text.

```go
func TestSummaryDecodesTabField(t *testing.T) {
	var s Summary
	if err := json.Unmarshal([]byte(`{"topic":"t","now":"n","tab":"crm bundling"}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Tab != "crm bundling" {
		t.Errorf("Tab = %q, want %q", s.Tab, "crm bundling")
	}
}

// The system prompt must instruct the model to produce `tab`, or the forced
// tool call will fill it with something unconstrained.
func TestSystemPromptDescribesTab(t *testing.T) {
	if !strings.Contains(summarySystemPrompt, "tab") {
		t.Error("summarySystemPrompt does not mention the tab field")
	}
	// The length rule is the load-bearing constraint for a narrow tab; make sure
	// it is stated so a prompt edit can't silently drop it.
	if !strings.Contains(summarySystemPrompt, "24") {
		t.Error("summarySystemPrompt does not state the tab length limit (24)")
	}
}
```

Requires `encoding/json` and `strings` in the test file — add if absent.

Note there is no unit test for the placeholder-guard-on-`tab` path here because `Summarize` needs a live client; Task 3's `tui_test.go` and manual verification cover the applied behaviour. The guard is still added in Step 3 below (it mirrors the existing `topic`/`now` guard exactly).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run 'TestSummaryDecodesTabField|TestSystemPromptDescribesTab' -v`
Expected: FAIL — `TestSystemPromptDescribesTab` fails (prompt has no "tab"/"24"); the decode test passes trivially only after the struct field exists, so it fails to compile until Step 3.

- [ ] **Step 3: Add the field, schema, prompt, and guard**

In `summary.go`, extend `Summary`:

```go
type Summary struct {
	Topic string `json:"topic"`
	Now   string `json:"now"`
	Tab   string `json:"tab"`
}
```

Add the `tab` property to the tool's `InputSchema.Properties` and to `Required`:

```go
			Properties: map[string]any{
				"topic": map[string]any{
					"type":        "string",
					"description": "What the session is for. Durable across turns.",
				},
				"now": map[string]any{
					"type":        "string",
					"description": "What the session is doing right now.",
				},
				"tab": map[string]any{
					"type":        "string",
					"description": "A 2-4 word lowercase tab label, 24 characters max, no punctuation. Same durable goal as topic, compressed.",
				},
			},
			Required: []string{"topic", "now", "tab"},
```

Extend the placeholder guard so a `<UNKNOWN>`-style `tab` rejects the whole summary (same fallback as topic/now):

```go
		if placeholderLine(out.Topic) || placeholderLine(out.Now) || placeholderLine(out.Tab) {
			return Summary{}, errors.New("summarize returned an empty or placeholder line")
		}
```

Append to `summarySystemPrompt`, after the `now` bullet and before the formatting rules:

```
- tab: a 2-4 word tab label — the shortest phrase naming the project or task.
  lowercase, 24 characters maximum, no punctuation. Derive it from the same
  durable goal as `topic` (never the current step), so the tab stays steady
  while `now` changes.
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -run 'TestSummaryDecodesTabField|TestSystemPromptDescribesTab' -v`
Expected: PASS.

- [ ] **Step 5: Full green + commit**

```bash
go build ./... && go vet ./... && go test ./...
git add cmd/claudemux-head/summary.go cmd/claudemux-head/summary_test.go
git commit -m "feat(summary): add a short Haiku-generated tab label"
```

---

### Task 2: `summary.tab_title` config toggle

**Files:**
- Modify: `cmd/claudemux-head/config.go` (`SummaryConfig` ~48, `defaultConfig` ~63)
- Modify: `cmd/claudemux-head/config_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `SummaryConfig` gains `TabTitle bool \`yaml:"tab_title"\``, defaulting to true.

**Why default true, set in `defaultConfig`:** the config is decoded INTO a struct pre-populated with defaults, so a bool that should default true MUST be set true in `defaultConfig` — a bare `bool` zero-values to false, which would silently disable the feature for everyone who doesn't mention the key.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/claudemux-head/config_test.go` (reuse the existing `writeConfig` helper):

```go
func TestTabTitleDefaultsTrue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config.yml

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Summary.TabTitle {
		t.Error("Summary.TabTitle = false, want true by default")
	}
}

func TestTabTitleCanBeDisabled(t *testing.T) {
	writeConfig(t, "summary:\n  tab_title: false\n")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Summary.TabTitle {
		t.Error("Summary.TabTitle = true, want false when set false in the file")
	}
	// Setting only tab_title must not disturb the other summary defaults.
	if !cfg.Summary.Enabled {
		t.Error("Summary.Enabled = false — a partial file zeroed an unrelated default")
	}
	if cfg.Summary.Model != "claude-haiku-4-5" {
		t.Errorf("Summary.Model = %q, want the default preserved", cfg.Summary.Model)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run 'TestTabTitle' -v`
Expected: FAIL — `TabTitle` field does not exist (compile error), then default is false.

- [ ] **Step 3: Add the field and default**

In `SummaryConfig`:

```go
type SummaryConfig struct {
	Enabled bool   `yaml:"enabled"`
	Model   string `yaml:"model"`
	// ... existing MinInterval, APIKeyFile ...
	// TabTitle renames the session's tmux window to the Haiku `tab` label, which
	// tmux propagates to the terminal tab. Default true. Independent of Enabled:
	// with Enabled true and this false, the status pane still summarizes but the
	// window/tab is left alone.
	TabTitle bool `yaml:"tab_title"`
}
```

In `defaultConfig`, add `TabTitle: true` to the `SummaryConfig` literal:

```go
		Summary: SummaryConfig{
			Enabled:     true,
			Model:       "claude-haiku-4-5",
			MinInterval: Duration{20 * time.Second},
			TabTitle:    true,
		},
```

(Leave `APIKeyFile` as it currently is in that literal.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -run 'TestTabTitle' -v`
Expected: PASS.

- [ ] **Step 5: Full green + commit**

```bash
go build ./... && go vet ./... && go test ./...
git add cmd/claudemux-head/config.go cmd/claudemux-head/config_test.go
git commit -m "feat(config): add summary.tab_title toggle (default on)"
```

---

### Task 3: Apply the tab title (rename the tmux window) + launcher set-titles

**Files:**
- Modify: `cmd/claudemux-head/tui.go` (`model` struct ~107, `newModel` ~118, the `summaryMsg` case ~446–470)
- Create: `cmd/claudemux-head/tabtitle.go`
- Modify: `cmd/claudemux-head/tui_test.go`
- Modify: `bin/claudemux` (session-creation block, near the existing `tmux set ... status-style` / `window-resized` hook, ~296)

**Interfaces:**
- Consumes: `Summary.Tab` (Task 1); `cfg.Summary.TabTitle` (Task 2); the model's existing `selfPane` field (`os.Getenv("TMUX_PANE")`, tui.go:129).
- Produces:
  - `func tabRenameArgs(selfPane, tab string) ([]string, bool)` — the tmux argv and whether to run it. `ok=false` when `selfPane` or `tab` is empty.
  - `func renameTabCmd(selfPane, tab string) tea.Cmd` — returns nil when there's nothing to do, else a `tea.Cmd` running the tmux rename with a hard timeout.
  - `model` gains `tabTitle bool`.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/claudemux-head/tui_test.go`:

```go
func TestTabRenameArgs(t *testing.T) {
	got, ok := tabRenameArgs("%3", "crm bundling")
	if !ok {
		t.Fatal("ok = false, want true for a real pane + label")
	}
	want := []string{"rename-window", "-t", "%3", "crm bundling"}
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// Outside tmux (no TMUX_PANE) there is nothing to rename.
func TestTabRenameArgsNoPane(t *testing.T) {
	if _, ok := tabRenameArgs("", "crm bundling"); ok {
		t.Error("ok = true with empty pane, want false")
	}
}

// An empty label must not rename the window to blank.
func TestTabRenameArgsEmptyLabel(t *testing.T) {
	if _, ok := tabRenameArgs("%3", ""); ok {
		t.Error("ok = true with empty label, want false")
	}
}

// renameTabCmd returns nil (no command) whenever there is nothing to do, so the
// caller can append it unconditionally.
func TestRenameTabCmdNilWhenNothingToDo(t *testing.T) {
	if renameTabCmd("", "crm bundling") != nil {
		t.Error("renameTabCmd with no pane should be nil")
	}
	if renameTabCmd("%3", "") != nil {
		t.Error("renameTabCmd with no label should be nil")
	}
}

// A landed summary renames the window only when tabTitle is on, a pane exists,
// and the label is non-empty. This drives the model's summaryMsg handler and
// asserts on the returned command's presence via the model's decision helper.
func TestSummaryTriggersRenameWhenEnabled(t *testing.T) {
	m := model{tabTitle: true, selfPane: "%3", summaryGen: 1}
	if m.tabCmdFor(Summary{Topic: "t", Now: "n", Tab: "crm bundling"}) == nil {
		t.Error("expected a rename command when tabTitle on, pane set, label present")
	}
}

func TestSummaryNoRenameWhenTabTitleOff(t *testing.T) {
	m := model{tabTitle: false, selfPane: "%3", summaryGen: 1}
	if m.tabCmdFor(Summary{Topic: "t", Now: "n", Tab: "crm bundling"}) != nil {
		t.Error("expected no rename command when tabTitle is off")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run 'TestTabRename|TestRenameTabCmd|TestSummary.*Rename|TestSummaryNoRename' -v`
Expected: FAIL — `tabRenameArgs`, `renameTabCmd`, `model.tabTitle`, and `model.tabCmdFor` are undefined.

- [ ] **Step 3: Create `tabtitle.go`**

```go
package main

import (
	"context"
	"os/exec"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// tabRenameArgs builds the `tmux` argument list to rename selfPane's window to
// tab, and reports whether it should run at all. It should not when we are not
// inside tmux (selfPane empty) or have no label — renaming a window to blank is
// worse than leaving it.
func tabRenameArgs(selfPane, tab string) ([]string, bool) {
	if selfPane == "" || tab == "" {
		return nil, false
	}
	return []string{"rename-window", "-t", selfPane, tab}, true
}

// renameTabCmd returns a tea.Cmd that renames the window, or nil when there is
// nothing to do (so callers append it unconditionally).
//
// The subprocess carries a hard deadline and its result is discarded: this runs
// off the poll loop exactly like panemap.go's tmux call, and a wedged tmux
// server must never block or crash the TUI. A failed rename simply leaves the
// previous title.
func renameTabCmd(selfPane, tab string) tea.Cmd {
	args, ok := tabRenameArgs(selfPane, tab)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "tmux", args...).Run()
		return nil
	}
}
```

- [ ] **Step 4: Thread `tabTitle` into the model and fire on summary land**

In `tui.go`, add the field beside `summarizer` (~107):

```go
	summarizer         *Summarizer
	minSummaryInterval time.Duration
	tabTitle           bool
```

In `newModel` (~140, in the struct literal), set it from config:

```go
		minSummaryInterval: cfg.Summary.MinInterval.Duration,
		tabTitle:           cfg.Summary.TabTitle,
```

Add a decision helper (put it near `canSummarize`):

```go
// tabCmdFor returns the window-rename command for a freshly landed summary, or
// nil when the tab title is disabled, we are not in tmux, or the label is empty.
func (m model) tabCmdFor(s Summary) tea.Cmd {
	if !m.tabTitle {
		return nil
	}
	return renameTabCmd(m.selfPane, s.Tab)
}
```

In the `summaryMsg` case, where the kept summary is stored (~468), return the rename command. Change:

```go
		if msg.err == nil {
			m.summary = msg.summary
		}
	}

	return m, nil
}
```

to:

```go
		if msg.err == nil {
			m.summary = msg.summary
			return m, m.tabCmdFor(msg.summary)
		}
	}

	return m, nil
}
```

(The stale-reply `return m, nil` above this block is unchanged — a stale summary must not rename the window either.)

- [ ] **Step 5: Enable set-titles in the launcher**

In `bin/claudemux`, in `create_session`, right after the `window-resized` set-hook line (~296), add:

```bash
  # Let the terminal tab track the tmux window name — claudemux-head renames the
  # window to the Haiku `tab` label as the session's focus settles. Set here so
  # the user needs no ~/.tmux.conf change.
  tmux set -t "$session_name" set-titles on
  tmux set -t "$session_name" set-titles-string '#W'
```

- [ ] **Step 6: Run the Go tests to verify they pass**

Run: `go test ./... -run 'TestTabRename|TestRenameTabCmd|TestSummary.*Rename|TestSummaryNoRename' -v`
Expected: PASS.

- [ ] **Step 7: Verify the launcher and the end-to-end rename by hand**

```bash
bash -n bin/claudemux && shellcheck bin/claudemux   # SC2032/SC2033 on `attach` is the known false positive
```

Then prove the rename actually reaches tmux (no live model needed):

```bash
tmux new-session -d -s cmtest
pane="$(tmux display-message -p -t cmtest '#{pane_id}')"
go build -o /tmp/cmtt/ct ./cmd/claudemux-head   # only to confirm it builds
tmux rename-window -t "$pane" "crm bundling"
tmux display-message -p -t cmtest '#{window_name}'   # -> crm bundling
tmux set -t cmtest set-titles on
tmux set -t cmtest set-titles-string '#W'
tmux show-options -t cmtest set-titles set-titles-string | sed 's/^/  /'
tmux kill-session -t cmtest
```
Expected: window name prints `crm bundling`; `set-titles on` and `set-titles-string "#W"` are shown.

- [ ] **Step 8: Full green + commit**

```bash
go build ./... && go vet ./... && go test ./...
git add cmd/claudemux-head/tabtitle.go cmd/claudemux-head/tui.go cmd/claudemux-head/tui_test.go bin/claudemux
git commit -m "feat(claudemux): rename the tmux window to the Haiku tab label"
```

---

### Task 4: Document the tab title

**Files:**
- Modify: `README.md` (the **Appearance: project colors** and **tmux notes** sections, and the **Configuration** key list)

**Interfaces:**
- Consumes: everything above.
- Produces: user documentation.

- [ ] **Step 1: Document the config key**

In the Configuration section's `config.yml` example block, add `tab_title: true` under `summary:`, and add a bullet to the key list:

```markdown
- `summary.tab_title` — rename each session's tmux window (and thus the terminal
  tab) to the short Haiku `tab` label, so a row of tabs reads like a list of what
  each session is doing. Default `true`. Set `false` to keep the status-pane
  summary but leave the window/tab untouched. Independent of `summary.enabled`.
```

- [ ] **Step 2: Add a paragraph to the Appearance / tmux area**

Add, near the tab-color paragraph (colors and titles are the same "identify a session at a glance" story):

```markdown
### Tab titles

The status pane's summarizer also produces a short 2–4 word label for the
session, and claudemux renames the tmux window to it — which the terminal shows
as the tab title. As the session's focus settles, the tab goes from the launch
default to something like `crm bundling`. Because the title comes from the tmux
window name, it also appears as the window label in the tmux status bar.

This needs no tmux configuration — claudemux sets `set-titles` itself. It applies
only inside tmux, and only while summaries are on; turn it off with
`summary.tab_title: false`. Outside tmux there is nothing to rename.
```

- [ ] **Step 3: Verify the config block still parses**

Building on the fact that an unknown key is a startup error, confirm the documented block is valid:

```bash
go build -o /tmp/cmdoc/claudemux-head ./cmd/claudemux-head
D=$(mktemp -d); mkdir -p "$D/claudemux"
awk '/^summary:$/,/^  accounts: \{\}$/' README.md > "$D/claudemux/config.yml" 2>/dev/null || true
# If the awk range does not capture it, hand-check: the block must contain tab_title: true
grep -q 'tab_title: true' README.md && echo "tab_title documented" || echo "MISSING tab_title in README"
XDG_CONFIG_HOME="$D" /tmp/cmdoc/claudemux-head config get summary.tab_title
```
Expected: prints `true` (or `tab_title documented` at minimum; fix the example block if the binary reports an unknown-key error).

- [ ] **Step 4: Rename gate + commit**

```bash
git ls-files | xargs grep -rniE 'claude-head|claude-env|CLAUDE_HEAD_' && echo STALE || echo CLEAN
git add README.md
git commit -m "docs: document summary.tab_title and Haiku tab labels"
```

---

## Post-plan: controller cuts the release

After all tasks pass review and merge to `main`, the controller cuts **v1.1.0**
following `RELEASING.md`: tag `v1.1.0`, let the workflow publish artifacts, then
bump the Homebrew formula (`version` + four new sums). `install.sh` needs no
change (it tracks latest).
