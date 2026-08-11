# Pane-Accurate Session Binding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make each claude-head follow the exact Claude Code session running in its sibling tmux pane, instead of guessing via "most-recently-active .jsonl in the cwd-derived project dir".

**Architecture:** A Claude Code hook (SessionStart + UserPromptSubmit) writes `{session_id, transcript_path}` to `~/.claude/claude-head/panes/<pane-number>.json`, keyed by the claude process's `$TMUX_PANE`. claude-head, when running inside tmux, finds the pane in its own tmux session whose current command is `claude`, reads that pane's map file each poll, and rotates to that transcript path. The existing MRA (most-recently-active) glob remains the fallback when no map is available (not in tmux, claude not started yet, hook not fired yet). Also: skip `<synthetic>` model values in the model-name scan.

**Tech Stack:** Go (bubbletea TUI, stdlib only — no new deps), bash + jq (hook), tmux.

## Global Constraints

- Repo: `/Users/michael/Projects/claude-head`. Binary installs to `~/go/bin/claude-head` via `go build -o ~/go/bin/claude-head .`
- Go tests run with `go test ./...` from the repo root; all must pass at every commit.
- Conventional-commit messages matching repo history (`feat(...)`, `fix(...)`, `refactor(...)`).
- The hook script MUST print nothing to stdout and exit 0 in all non-error paths — UserPromptSubmit stdout is injected into the model's context.
- The hook must be a no-op when `$TMUX_PANE` is unset (headless/cron/cloud sessions).
- No new Go module dependencies.
- Map dir is `~/.claude/claude-head/panes/`; map filename is the pane id without the `%` (e.g. pane `%42` → `42.json`).

---

### Task 1: Hook script `hooks/claude-head-map.sh`

**Files:**
- Create: `hooks/claude-head-map.sh` (in repo, mode 0755)

**Interfaces:**
- Consumes: hook JSON on stdin (fields `.session_id`, `.transcript_path`), env `$TMUX_PANE`, `$HOME`.
- Produces: `~/.claude/claude-head/panes/<pane-number>.json` containing `{"session_id":"...","transcript_path":"..."}` (compact JSON, written atomically). Task 2's `readPaneMap` parses exactly this shape.

- [ ] **Step 1: Write the script**

```bash
#!/usr/bin/env bash
# Claude Code hook (SessionStart + UserPromptSubmit): record which session
# lives in which tmux pane so claude-head can follow its sibling pane's
# transcript. Keyed by $TMUX_PANE (inherited from the claude process).
#
# MUST stay silent on stdout: UserPromptSubmit stdout is injected into the
# model's context.
set -euo pipefail

[ -n "${TMUX_PANE:-}" ] || exit 0

dir="$HOME/.claude/claude-head/panes"
mkdir -p "$dir"

map="$(jq -c '{session_id: .session_id, transcript_path: .transcript_path}' 2>/dev/null || true)"
[ -n "$map" ] && [ "$map" != '{"session_id":null,"transcript_path":null}' ] || exit 0

f="$dir/${TMUX_PANE#%}.json"
tmp="$f.tmp.$$"
printf '%s\n' "$map" > "$tmp"
mv "$tmp" "$f"

# Prune map files for panes that haven't written in a week (dead panes;
# pane ids restart when the tmux server restarts, so stale files can shadow).
find "$dir" -name '*.json' -mtime +7 -delete 2>/dev/null || true
exit 0
```

- [ ] **Step 2: Make it executable and test it manually**

Run:
```bash
cd /Users/michael/Projects/claude-head
chmod +x hooks/claude-head-map.sh
echo '{"session_id":"test-123","transcript_path":"/tmp/x.jsonl","cwd":"/tmp"}' | TMUX_PANE='%99' hooks/claude-head-map.sh
cat ~/.claude/claude-head/panes/99.json
```
Expected: prints `{"session_id":"test-123","transcript_path":"/tmp/x.jsonl"}` and the script itself produced no stdout.

- [ ] **Step 3: Test the no-TMUX and bad-input paths**

Run:
```bash
echo '{"session_id":"x","transcript_path":"/tmp/y.jsonl"}' | hooks/claude-head-map.sh; echo "exit=$?"
echo 'not json' | TMUX_PANE='%98' hooks/claude-head-map.sh; echo "exit=$?"
ls ~/.claude/claude-head/panes/98.json 2>&1 || true
rm -f ~/.claude/claude-head/panes/99.json
```
Expected: both invocations print `exit=0`, no `98.json` is created, cleanup removes the test file.

- [ ] **Step 4: Commit**

```bash
git add hooks/claude-head-map.sh
git commit -m "feat(hooks): record tmux pane → session transcript map"
```

---

### Task 2: `panemap.go` — resolve the sibling claude pane's transcript

**Files:**
- Create: `panemap.go`
- Test: `panemap_test.go`

**Interfaces:**
- Consumes: map files produced by Task 1's hook.
- Produces (used by Task 3):
  - `func paneMapDir() string` — `~/.claude/claude-head/panes` ("" if home unknown)
  - `func pickClaudePane(listing, self string) (string, bool)` — pure parser
  - `func siblingClaudePane(self string) (string, bool)` — shells out to tmux
  - `func readPaneMap(dir, paneID string) (string, bool)` — returns transcript path; ok only if the file parses AND the transcript exists on disk
  - `func mappedTranscript(selfPane, dir string) (string, bool)` — the one-call composition Task 3 uses

- [ ] **Step 1: Write the failing tests**

Create `panemap_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPickClaudePane(t *testing.T) {
	listing := "%35 claude-head\n%33 claude\n%34 fish\n"
	pane, ok := pickClaudePane(listing, "%35")
	if !ok || pane != "%33" {
		t.Fatalf("got %q ok=%v, want %%33 true", pane, ok)
	}
}

func TestPickClaudePaneSkipsSelfAndNonClaude(t *testing.T) {
	// Self runs `claude-head` but the field split makes its command
	// "claude-head", which must not match; a shell pane must not match.
	if pane, ok := pickClaudePane("%1 claude-head\n%2 bash\n", "%1"); ok {
		t.Fatalf("expected no match, got %q", pane)
	}
	// node accepted (claude may report as its runtime)
	if pane, ok := pickClaudePane("%1 claude-head\n%2 node\n", "%1"); !ok || pane != "%2" {
		t.Fatalf("got %q ok=%v, want %%2 true", pane, ok)
	}
}

func TestReadPaneMap(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "abc.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `{"session_id":"abc","transcript_path":"` + transcript + `"}`
	if err := os.WriteFile(filepath.Join(dir, "42.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := readPaneMap(dir, "%42")
	if !ok || got != transcript {
		t.Fatalf("got %q ok=%v, want %q true", got, ok, transcript)
	}
}

func TestReadPaneMapRejectsMissingTranscript(t *testing.T) {
	dir := t.TempDir()
	body := `{"session_id":"abc","transcript_path":"` + filepath.Join(dir, "gone.jsonl") + `"}`
	if err := os.WriteFile(filepath.Join(dir, "7.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := readPaneMap(dir, "%7"); ok {
		t.Fatalf("expected not-ok for missing transcript, got %q", got)
	}
}

func TestReadPaneMapRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "9.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readPaneMap(dir, "%9"); ok {
		t.Fatal("expected not-ok for garbage map file")
	}
	if _, ok := readPaneMap(dir, "%404"); ok {
		t.Fatal("expected not-ok for absent map file")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run 'PickClaudePane|ReadPaneMap' -v`
Expected: FAIL — `undefined: pickClaudePane`, `undefined: readPaneMap`.

- [ ] **Step 3: Write the implementation**

Create `panemap.go`:

```go
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// paneMap mirrors the JSON written by hooks/claude-head-map.sh.
type paneMap struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
}

// paneMapDir returns the directory where the Claude Code hook records
// pane → transcript mappings. Empty when the home dir can't be resolved.
func paneMapDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "claude-head", "panes")
}

// pickClaudePane scans `tmux list-panes` output ("%id command" per line) and
// returns the first pane, excluding self, running claude. The command is
// matched exactly ("claude", or "node" for runtimes that report the shim)
// so "claude-head" panes never match.
func pickClaudePane(listing, self string) (string, bool) {
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == self {
			continue
		}
		if fields[1] == "claude" || fields[1] == "node" {
			return fields[0], true
		}
	}
	return "", false
}

// siblingClaudePane finds the claude pane in the same tmux session as self
// (a pane id like "%35"). ok is false outside tmux or when no claude pane
// exists yet.
func siblingClaudePane(self string) (string, bool) {
	if self == "" {
		return "", false
	}
	out, err := exec.Command("tmux", "list-panes", "-s", "-t", self,
		"-F", "#{pane_id} #{pane_current_command}").Output()
	if err != nil {
		return "", false
	}
	return pickClaudePane(string(out), self)
}

// readPaneMap returns the transcript path recorded for paneID. ok requires
// the map file to parse and the transcript to exist on disk — a stale map
// (recycled pane id after a tmux server restart, deleted session) is
// treated as absent so callers fall back to MRA discovery.
func readPaneMap(dir, paneID string) (string, bool) {
	if dir == "" || paneID == "" {
		return "", false
	}
	name := strings.TrimPrefix(paneID, "%") + ".json"
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", false
	}
	var m paneMap
	if err := json.Unmarshal(data, &m); err != nil || m.TranscriptPath == "" {
		return "", false
	}
	if _, err := os.Stat(m.TranscriptPath); err != nil {
		return "", false
	}
	return m.TranscriptPath, true
}

// mappedTranscript composes sibling-pane discovery with the map lookup:
// the transcript path of the claude session running next to us, if the
// hook has recorded one.
func mappedTranscript(selfPane, dir string) (string, bool) {
	pane, ok := siblingClaudePane(selfPane)
	if !ok {
		return "", false
	}
	return readPaneMap(dir, pane)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -v`
Expected: all PASS (new tests and the existing suite).

- [ ] **Step 5: Commit**

```bash
git add panemap.go panemap_test.go
git commit -m "feat: resolve sibling claude pane's transcript via hook-written pane map"
```

---

### Task 3: Wire mapped-transcript binding into the poll loop

**Files:**
- Modify: `tui.go` (model struct ~line 38, `newModel` ~line 73, `pollData` ~line 173)

**Interfaces:**
- Consumes: `mappedTranscript(selfPane, dir string) (string, bool)` and `paneMapDir() string` from Task 2; existing `mostRecentlyActiveSession` from `session.go`.
- Produces: no new exported surface; `dataMsg.activeJSONL` semantics unchanged (Task 3 is behavior-only, existing `switchSession` handles the rotation, including into other project dirs for worktrees).

- [ ] **Step 1: Add pane-binding fields to the model**

In `tui.go`, extend the model struct — after the `followActive` field (~line 47) add:

```go
	// selfPane/paneDir drive pane-accurate binding: when running inside
	// tmux, each poll prefers the transcript the SessionStart/
	// UserPromptSubmit hook recorded for the sibling claude pane over the
	// MRA glob. The glob cross-binds when two envs share a repo and goes
	// permanently stale when claude runs in a worktree (transcripts land
	// in a different encoded project dir).
	selfPane string
	paneDir  string
```

In `newModel`, add to the `model{...}` literal:

```go
		selfPane:       os.Getenv("TMUX_PANE"),
		paneDir:        paneMapDir(),
```

Add `"os"` to the imports of `tui.go`.

- [ ] **Step 2: Prefer the mapped transcript in pollData**

Replace the follow-rotation block inside `pollData` (currently:)

```go
		activeJSONL := ""
		if follow {
			if mra, ok := mostRecentlyActiveSession(filepath.Dir(jsonlPath)); ok && mra != jsonlPath {
				activeJSONL = mra
			}
		}
```

with:

```go
		activeJSONL := ""
		if follow {
			if mapped, ok := mappedTranscript(selfPane, paneDir); ok {
				if mapped != jsonlPath {
					activeJSONL = mapped
				}
			} else if mra, ok := mostRecentlyActiveSession(filepath.Dir(jsonlPath)); ok && mra != jsonlPath {
				activeJSONL = mra
			}
		}
```

and capture the two new fields alongside the existing ones at the top of `pollData`:

```go
	selfPane := m.selfPane
	paneDir := m.paneDir
```

- [ ] **Step 3: Run the full suite and vet**

Run: `go test ./... && go vet ./...`
Expected: PASS, no vet complaints. (No new unit test here: the new branch is a composition of the Task 2 units over a live tmux socket; it gets an end-to-end verification in Task 5.)

- [ ] **Step 4: Commit**

```bash
git add tui.go
git commit -m "fix(tui): follow sibling pane's mapped transcript, MRA only as fallback"
```

---

### Task 4: Skip `<synthetic>` model values

**Files:**
- Modify: `tui.go` (`recomputeFromEvents`, ~line 91)
- Test: `tui_test.go`

**Interfaces:**
- Consumes: existing `model.recomputeFromEvents(now time.Time)` and `Event.Model`.
- Produces: `m.modelName` never set to a placeholder model (any value starting with `<`).

- [ ] **Step 1: Write the failing test**

Append to `tui_test.go`:

```go
func TestRecomputeSkipsSyntheticModel(t *testing.T) {
	m := model{allEvents: []Event{
		{Type: "assistant", Model: "claude-opus-4-8"},
		{Type: "assistant", Model: "<synthetic>"},
	}}
	m.recomputeFromEvents(time.Now())
	if m.modelName != "claude-opus-4-8" {
		t.Fatalf("modelName = %q, want claude-opus-4-8", m.modelName)
	}
}
```

(If `tui_test.go` doesn't already import `time`, add it.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TestRecomputeSkipsSyntheticModel -v`
Expected: FAIL with `modelName = "<synthetic>"`.

- [ ] **Step 3: Implement the skip**

In `recomputeFromEvents`, change the model scan loop body from:

```go
	for i := len(m.allEvents) - 1; i >= 0; i-- {
		if m.allEvents[i].Model != "" {
			m.modelName = m.allEvents[i].Model
			break
		}
	}
```

to:

```go
	for i := len(m.allEvents) - 1; i >= 0; i-- {
		// Skip placeholder models like "<synthetic>" (error/bookkeeping
		// events) — show the last real API model instead.
		if mm := m.allEvents[i].Model; mm != "" && !strings.HasPrefix(mm, "<") {
			m.modelName = mm
			break
		}
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add tui.go tui_test.go
git commit -m "fix(tui): ignore <synthetic> model placeholders in model display"
```

---

### Task 6: Worktree indicator in the status bar

**Files:**
- Modify: `tui.go` (`renderStatusbar`, ~line 358 `leftParts` assembly; new helper next to `shortModel`)
- Test: `tui_test.go`

**Interfaces:**
- Consumes: `m.jsonlPath` (already on the model) — transcript paths live in `~/.claude/projects/<encoded-project-dir>/<session>.jsonl`, and a session running in a Claude worktree has an encoded dir containing the marker `--claude-worktrees-` (e.g. `-Users-michael-Projects-remix--claude-worktrees-crm-427-dataloader`).
- Produces: `func worktreeName(jsonlPath string) string` — the worktree's name (text after the marker in the encoded dir), "" for non-worktree sessions. Shown in the status bar as `⎇ <name>` between the model name and the ctx bar.

- [ ] **Step 1: Write the failing test**

Append to `tui_test.go`:

```go
func TestWorktreeName(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/Users/michael/.claude/projects/-Users-michael-Projects-remix--claude-worktrees-crm-427-dataloader/abc.jsonl", "crm-427-dataloader"},
		{"/Users/michael/.claude/projects/-Users-michael-Projects-remix/abc.jsonl", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := worktreeName(c.path); got != c.want {
			t.Errorf("worktreeName(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TestWorktreeName -v`
Expected: FAIL — `undefined: worktreeName`.

- [ ] **Step 3: Implement**

In `tui.go`, next to `shortModel`, add:

```go
// worktreeName extracts the Claude worktree name from a transcript path.
// Worktree sessions write transcripts under an encoded project dir that
// embeds the worktree location, e.g.
// ".../projects/-Users-x-repo--claude-worktrees-<name>/<session>.jsonl".
// Returns "" for non-worktree sessions.
func worktreeName(jsonlPath string) string {
	const marker = "--claude-worktrees-"
	dir := filepath.Base(filepath.Dir(jsonlPath))
	i := strings.LastIndex(dir, marker)
	if i < 0 {
		return ""
	}
	return dir[i+len(marker):]
}
```

In `renderStatusbar`, after the model-name append (`if m.modelName != "" { ... }`), add:

```go
	if wt := worktreeName(m.jsonlPath); wt != "" {
		leftParts = append(leftParts, "⎇ "+truncateRunes(wt, 24))
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... && go vet ./...`
Expected: all PASS, no vet complaints.

- [ ] **Step 5: Commit**

```bash
git add tui.go tui_test.go
git commit -m "feat(tui): show worktree name in the status bar"
```

---

### Task 7: Show genuine human prompts on the first/last lines

**Files:**
- Modify: `events.go` (raw parse struct in `parseEvent`, `Event` struct)
- Modify: `tui.go` (`firstUserPrompt` ~line 151, `lastUserPrompt` ~line 137)
- Test: `tui_test.go`, `events_test.go`

**Interfaces:**
- Consumes: existing `Event`, `cleanCommandText` (already turns `<command-name>` expansions into `/name args`).
- Produces: `Event.IsMeta bool` (parsed from the jsonl `isMeta` field); `func genuinePrompt(e Event) bool`; `firstUserPrompt` prefers non-slash-command prompts. Rationale: sessions opened via `/clear` start with an `isMeta` `<local-command-caveat>` notice and a `/clear` expansion before the first real prompt; task notifications/system reminders arrive as user turns whose text starts with `<`.

- [ ] **Step 1: Write the failing tests**

Append to `tui_test.go`:

```go
func TestFirstUserPromptSkipsMetaXMLAndCommands(t *testing.T) {
	events := []Event{
		{Type: "user", IsMeta: true, UserText: "Caveat: the messages below were generated..."},
		{Type: "user", UserText: "/clear"},
		{Type: "user", UserText: "<task-notification>...</task-notification>"},
		{Type: "user", UserText: "hey, fix the thing"},
	}
	if got := firstUserPrompt(events); got != "hey, fix the thing" {
		t.Fatalf("firstUserPrompt = %q, want the genuine prompt", got)
	}
}

func TestFirstUserPromptFallsBackToCommand(t *testing.T) {
	events := []Event{
		{Type: "user", IsMeta: true, UserText: "Caveat: ..."},
		{Type: "user", UserText: "/code-review ultra"},
	}
	if got := firstUserPrompt(events); got != "/code-review ultra" {
		t.Fatalf("firstUserPrompt = %q, want the command fallback", got)
	}
}

func TestLastUserPromptSkipsMetaAndXML(t *testing.T) {
	events := []Event{
		{Type: "user", UserText: "run the tests"},
		{Type: "user", IsMeta: true, UserText: "Caveat: ..."},
		{Type: "last-prompt", UserText: "<task-notification>done</task-notification>"},
	}
	if got := lastUserPrompt(events); got != "run the tests" {
		t.Fatalf("lastUserPrompt = %q, want the genuine prompt", got)
	}
}
```

Append to `events_test.go`:

```go
func TestParseEventIsMeta(t *testing.T) {
	line := `{"type":"user","isMeta":true,"timestamp":"2026-07-02T12:00:00Z","message":{"content":"Caveat: x"}}`
	e, ok := parseEvent(line)
	if !ok || !e.IsMeta {
		t.Fatalf("parseEvent isMeta: ok=%v IsMeta=%v, want true/true", ok, e.IsMeta)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run 'FirstUserPrompt|LastUserPromptSkips|ParseEventIsMeta' -v`
Expected: FAIL — `unknown field IsMeta` compile error (that counts as the failing state).

- [ ] **Step 3: Implement**

In `events.go`, add `IsMeta bool` to `Event` (after `Type string`), add `IsMeta bool \`json:"isMeta"\`` to the anonymous raw struct in `parseEvent`, and copy it: `ev := Event{Type: raw.Type, IsMeta: raw.IsMeta, Timestamp: raw.Timestamp, RawLine: line}`.

In `tui.go`, add next to `lastUserPrompt`:

```go
// genuinePrompt reports whether e carries text the human actually sent.
// Harness bookkeeping also arrives as user turns: isMeta caveat notices,
// injected task notifications / system reminders (text starts with "<" —
// humans don't open messages with XML). Slash commands pass; callers that
// want to de-prioritize them do so themselves.
func genuinePrompt(e Event) bool {
	if e.Type != "last-prompt" && e.Type != "user" {
		return false
	}
	if e.IsMeta || e.UserText == "" {
		return false
	}
	return !strings.HasPrefix(strings.TrimSpace(e.UserText), "<")
}
```

Replace `lastUserPrompt`'s loop body condition with `if genuinePrompt(events[i]) { return events[i].UserText }` (keep the newest-first scan; update its doc comment to mention the filtering).

Replace `firstUserPrompt` with:

```go
// firstUserPrompt returns the oldest thing the human sent — the session's
// context line. Slash commands like "/clear" say nothing about what the
// session is for, so non-command prompts win; a command is returned only
// when the session has nothing else (e.g. a window whose whole purpose is
// one slash command).
func firstUserPrompt(events []Event) string {
	fallback := ""
	for _, e := range events {
		if !genuinePrompt(e) {
			continue
		}
		if !strings.HasPrefix(e.UserText, "/") {
			return e.UserText
		}
		if fallback == "" {
			fallback = e.UserText
		}
	}
	return fallback
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... && go vet ./...`
Expected: all PASS (existing prompt tests may need their fixtures' expectations checked — if an existing test asserted that an XML/meta prompt is returned, that expectation changed intentionally with this task; update the fixture to use genuine prompt text rather than weakening the new filter).

- [ ] **Step 5: Commit**

```bash
git add events.go events_test.go tui.go tui_test.go
git commit -m "fix(tui): show genuine human prompts on first/last lines"
```

---

### Task 5: Install, register the hook, and verify end-to-end

**Files:**
- Modify: `~/.claude/settings.json` (add `hooks` key — none exists today)
- Create: symlink `~/.claude/hooks/claude-head-map.sh` → repo script
- Modify: `~/go/bin/claude-head` (rebuilt binary)

**Interfaces:**
- Consumes: everything above.
- Produces: live system. Note: hook configs are captured when a claude session starts, so already-running claude sessions will NOT write map files until restarted; heads keep MRA-fallback behavior for those. New/restarted sessions self-heal on every prompt via UserPromptSubmit.

- [ ] **Step 1: Build and install the binary**

```bash
cd /Users/michael/Projects/claude-head
go build -o ~/go/bin/claude-head .
~/go/bin/claude-head --help 2>&1 | head -2 || true
```
Expected: builds cleanly; binary runs (flag parse output or TUI error about no TTY is fine).

- [ ] **Step 2: Symlink the hook into ~/.claude/hooks**

```bash
mkdir -p ~/.claude/hooks
ln -sf /Users/michael/Projects/claude-head/hooks/claude-head-map.sh ~/.claude/hooks/claude-head-map.sh
```

- [ ] **Step 3: Register the hook in settings.json**

There is no `hooks` key today. Add one with python3 (preserves the rest of the file):

```bash
python3 - <<'EOF'
import json
p = "/Users/michael/.claude/settings.json"
s = json.load(open(p))
hook = {"type": "command", "command": "/Users/michael/.claude/hooks/claude-head-map.sh"}
s["hooks"] = {
    "SessionStart": [{"hooks": [hook]}],
    "UserPromptSubmit": [{"hooks": [hook]}],
}
json.dump(s, open(p, "w"), indent=2)
EOF
python3 -c "import json; print(json.load(open('/Users/michael/.claude/settings.json'))['hooks'])"
```
Expected: printed hooks dict with both events.

- [ ] **Step 4: End-to-end verification in a scratch tmux session**

```bash
tmux new-session -d -s headtest -c /tmp
tmux split-window -v -t headtest -c /tmp
tmux send-keys -t headtest:0.1 'claude -p "say hi" --model haiku' C-m
sleep 25
pane=$(tmux list-panes -t headtest -F '#{pane_id} #{pane_current_command}' | awk '$2=="claude"||$2=="node"{print $1}')
ls -la ~/.claude/claude-head/panes/
tmux kill-session -t headtest
```
Expected: a `panes/<n>.json` file appears whose number matches a pane id from the scratch session, containing a `/private/tmp`-encoded transcript path. (If `claude -p` doesn't fire SessionStart in print mode, run an interactive `claude` in the pane instead, wait for the prompt, then check — the file must exist before killing the session.)

- [ ] **Step 5: Restart the running claude-head panes**

For each tmux pane currently running claude-head (find with `tmux list-panes -a -F '#{pane_id} #{pane_current_command}' | awk '$2=="claude-head"'`):

```bash
for p in $(tmux list-panes -a -F '#{pane_id} #{pane_current_command}' | awk '$2=="claude-head"{print $1}'); do
  tmux send-keys -t "$p" q
  sleep 1
  tmux send-keys -t "$p" 'claude-head' C-m
done
```
Expected: each head pane restarts and renders. They will still show MRA-derived sessions until their sibling claude is restarted (hook snapshot), which is expected — do NOT restart the claude panes themselves (live user sessions).

- [ ] **Step 6: Commit the plan doc**

```bash
cd /Users/michael/Projects/claude-head
git add docs/superpowers/plans/2026-07-02-pane-session-binding.md
git commit -m "docs: pane-accurate session binding plan"
```
