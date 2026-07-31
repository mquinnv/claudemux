# Session Teardown (`press x`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a single key to `claudemux-head` that runs the project's wrap-up command in the `claude` pane, verifies the worktree is gone, then exits `claude` and kills the tmux session behind a second-press gate.

**Architecture:** A four-state machine on the bubbletea model (`idle → sent → ready → exiting`), driven by the `x` key and the existing one-second poll tick. Every decision is a pure function tested without a filesystem or a tmux server; every side effect (tmux `send-keys`, `git worktree list`, `kill-session`) lives in a `tea.Cmd` with a bounded context, exactly as `resetTabCmd` and `renameTabCmd` already do.

**Tech Stack:** Go, [bubbletea](https://github.com/charmbracelet/bubbletea), [lipgloss](https://github.com/charmbracelet/lipgloss), `gopkg.in/yaml.v3`, tmux CLI, git CLI.

**Spec:** `docs/superpowers/specs/2026-07-31-session-teardown-design.md`

## Global Constraints

- Go module is `github.com/mquinnv/claudemux`; all code in this plan is package `main` under `cmd/claudemux-head/`.
- Tests run with `go test ./...` from the repo root. Individual tests: `go test ./cmd/claudemux-head/ -run TestName -v`.
- **No subprocess may run on the `Update` loop.** Anything shelling out to `tmux` or `git` goes in a `tea.Cmd` closure with `context.WithTimeout`. Existing bound for tmux calls is `2 * time.Second`.
- **A failed side effect degrades, it never errors out.** Discard subprocess results, keep the TUI alive — the discipline in `resetTabCmd`'s doc comment.
- Unknown keys in `config.yml` are already a hard startup error (`dec.KnownFields(true)` in `loadConfig`). Any new config key must therefore also be added to the struct or every existing user's config breaks — and conversely, a new key is automatically rejected if misspelled. No extra validation needed.
- Exact user-visible strings (copy verbatim, they are asserted in tests):
  - `⏻ wrapping up…`
  - `⏻ press x to tear down`
  - `⏻ exiting claude…`
  - `⏻ worktree still present`
  - Abort notes: `wrap-up didn't submit`, `claude didn't exit`, `no claude pane`
- Timeouts (exact values): `teardownSubmitTimeout = 10 * time.Second`, `teardownExitTimeout = 15 * time.Second`, `teardownNoteTTL = 5 * time.Second`.
- Commit after every task. Conventional-commit prefixes, matching this repo's history (`feat(head):`, `fix(head):`, `docs(head):`).

## File Structure

| File | Responsibility |
|---|---|
| `cmd/claudemux-head/teardown.go` | **New.** Everything teardown: phase type, gate predicate, chip renderer, tmux argument builders, worktree-gone helpers, and the `tea.Cmd`s. |
| `cmd/claudemux-head/teardown_test.go` | **New.** Tests for all of the above. |
| `cmd/claudemux-head/config.go` | Add `TeardownConfig` and its default. |
| `cmd/claudemux-head/config_test.go` | Cover the new key. |
| `cmd/claudemux-head/panemap.go` | `mappedTranscript` also returns the claude pane id it picked. |
| `cmd/claudemux-head/tui.go` | Model fields, startup capture, `x`/`esc` key handling, tick and message transitions, chip in both status-line renderers. |
| `cmd/claudemux-head/tui_test.go` | Model/Update/View coverage. |
| `README.md` | User-facing docs. |

`teardown.go` holds the whole feature rather than being split by technical layer, matching `tabreset.go`, which likewise holds its pure builders, its helpers, and its one `tea.Cmd` together.

---

### Task 1: `teardown.command` config key

**Files:**
- Modify: `cmd/claudemux-head/config.go` (Config struct ~line 43, `defaultConfig` ~line 94)
- Test: `cmd/claudemux-head/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `type TeardownConfig struct { Command string }`, reachable as `cfg.Teardown.Command`. Default `"/done"`. Empty string is a meaningful value: skip the wrap-up command.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/claudemux-head/config_test.go`:

```go
func TestTeardownCommandDefaultsToDone(t *testing.T) {
	cfg := defaultConfig()
	if cfg.Teardown.Command != "/done" {
		t.Errorf("default teardown.command = %q, want %q", cfg.Teardown.Command, "/done")
	}
}

// An explicitly empty command is a legal opt-out (press x becomes a gated
// exit-and-kill), so it must survive decoding rather than being re-defaulted.
func TestTeardownCommandEmptyStringIsPreserved(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "claudemux"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "claudemux", "config.yml"),
		[]byte("teardown:\n  command: \"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Teardown.Command != "" {
		t.Errorf("teardown.command = %q, want empty", cfg.Teardown.Command)
	}
}

func TestTeardownCommandOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "claudemux"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "claudemux", "config.yml"),
		[]byte("teardown:\n  command: /wrapup\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Teardown.Command != "/wrapup" {
		t.Errorf("teardown.command = %q, want /wrapup", cfg.Teardown.Command)
	}
	// Untouched blocks keep their defaults — the partial-decode contract.
	if cfg.Summary.Model != "claude-haiku-4-5" {
		t.Errorf("summary.model = %q, want default", cfg.Summary.Model)
	}
}
```

If `config_test.go` does not already import `os` and `path/filepath`, add them.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run TestTeardown -v`
Expected: compile failure — `cfg.Teardown undefined (type Config has no field or method Teardown)`.

- [ ] **Step 3: Add the config struct and default**

In `cmd/claudemux-head/config.go`, add the field to `Config`:

```go
type Config struct {
	Summary     SummaryConfig     `yaml:"summary"`
	OnePassword OnePasswordConfig `yaml:"onepassword"`
	Launch      LaunchConfig      `yaml:"launch"`
	Teardown    TeardownConfig    `yaml:"teardown"`
}
```

Add the type below `LaunchConfig`:

```go
// TeardownConfig configures the `x` key in the status pane, which wraps a
// session up and kills its tmux session.
//
// Command is typed into the claude pane on the first press. It defaults to
// "/done" because that is the wrap-up slash command this tool was built
// around, but it is a *command name in someone else's TUI* — a user without
// that skill points it at their own, and "" opts out of the step entirely,
// making the key a gated exit-and-kill. Empty is therefore a real value, not
// "unset": loadConfig decodes into defaults, so an explicit `command: ""`
// overrides the default rather than being ignored.
type TeardownConfig struct {
	Command string `yaml:"command"`
}
```

In `defaultConfig()`, add the block:

```go
func defaultConfig() Config {
	return Config{
		Summary: SummaryConfig{
			Enabled:     true,
			Model:       "claude-haiku-4-5",
			MinInterval: Duration{20 * time.Second},
			TabTitle:    true,
		},
		Teardown: TeardownConfig{
			Command: "/done",
		},
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -v`
Expected: PASS, including the pre-existing config tests (the `config get` subcommand round-trips `Config` through YAML, so a new field must not break `configget_test.go`).

- [ ] **Step 5: Verify the launcher-facing accessor works**

Run: `go run ./cmd/claudemux-head config get teardown.command`
Expected: prints `/done`.

- [ ] **Step 6: Commit**

```bash
git add cmd/claudemux-head/config.go cmd/claudemux-head/config_test.go
git commit -m "feat(head): add teardown.command config key"
```

---

### Task 2: Worktree-gone helpers

**Files:**
- Create: `cmd/claudemux-head/teardown.go`
- Create: `cmd/claudemux-head/teardown_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `func worktreeListed(porcelain, path string) bool`
  - `func mainCheckoutFromCommonDir(out string) string`
  - `func mainCheckoutFor(dir string) string`
  - `func worktreeIsGone(ctx context.Context, workDir, mainCheckout string) bool`

**Background for the implementer:** a git *worktree* is a second checkout of one repo sharing its object store. `git worktree list --porcelain` prints one stanza per worktree, each starting with a `worktree <absolute path>` line. `git rev-parse --path-format=absolute --git-common-dir` prints the shared git directory (`/repo/.git`) from anywhere in the repo, including from a linked worktree — its parent is the main checkout. The wrap-up command deletes the worktree the session runs in, which is why the head must have captured these paths before they vanish.

- [ ] **Step 1: Write the failing tests**

Create `cmd/claudemux-head/teardown_test.go`:

```go
package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWorktreeListed(t *testing.T) {
	const porcelain = `worktree /Users/me/Projects/app
HEAD abc123
branch refs/heads/main

worktree /Users/me/Projects/app/.claude/worktrees/floating-harp
HEAD def456
branch refs/heads/feature
`
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"main checkout", "/Users/me/Projects/app", true},
		{"linked worktree", "/Users/me/Projects/app/.claude/worktrees/floating-harp", true},
		{"trailing slash still matches", "/Users/me/Projects/app/.claude/worktrees/floating-harp/", true},
		{"removed worktree", "/Users/me/Projects/app/.claude/worktrees/gone", false},
		{"prefix is not a match", "/Users/me/Projects/app/.claude/worktrees/floating", false},
		{"empty path", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := worktreeListed(porcelain, tt.path); got != tt.want {
				t.Errorf("worktreeListed(_, %q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// Empty output means git failed or printed nothing; nothing is listed, and the
// caller must not read that as "the worktree is gone".
func TestWorktreeListedEmptyListing(t *testing.T) {
	if worktreeListed("", "/Users/me/Projects/app") {
		t.Error("empty listing matched a path")
	}
}

func TestMainCheckoutFromCommonDir(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{"trailing newline", "/Users/me/Projects/app/.git\n", "/Users/me/Projects/app"},
		{"no newline", "/Users/me/Projects/app/.git", "/Users/me/Projects/app"},
		{"empty", "", ""},
		{"whitespace only", "  \n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mainCheckoutFromCommonDir(tt.out); got != tt.want {
				t.Errorf("mainCheckoutFromCommonDir(%q) = %q, want %q", tt.out, got, tt.want)
			}
		})
	}
}

// A deleted work dir is gone regardless of what git says — the common case,
// and the one where git can't be run from the work dir at all.
func TestWorktreeIsGoneMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "deleted")
	if !worktreeIsGone(context.Background(), missing, "") {
		t.Error("missing work dir reported as present")
	}
}

// A directory that still exists and is still a registered worktree is present.
// This exercises the real git path end to end.
func TestWorktreeIsGoneLiveWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	main := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", main}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("commit", "--allow-empty", "-m", "init")

	wt := filepath.Join(main, "wt")
	run("worktree", "add", "-b", "side", wt)

	// EvalSymlinks because t.TempDir() hands back /var/... on macOS while git
	// reports the resolved /private/var/... — the same normalization the model
	// does at startup.
	resolvedWT, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}
	resolvedMain, err := filepath.EvalSymlinks(main)
	if err != nil {
		t.Fatal(err)
	}

	if got := mainCheckoutFor(resolvedWT); got != resolvedMain {
		t.Errorf("mainCheckoutFor = %q, want %q", got, resolvedMain)
	}
	if worktreeIsGone(context.Background(), resolvedWT, resolvedMain) {
		t.Error("live worktree reported as gone")
	}

	run("worktree", "remove", "--force", wt)
	if !worktreeIsGone(context.Background(), resolvedWT, resolvedMain) {
		t.Error("removed worktree reported as present")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestWorktree|TestMainCheckout' -v`
Expected: compile failure — `undefined: worktreeListed`.

- [ ] **Step 3: Write the implementation**

Create `cmd/claudemux-head/teardown.go`:

```go
package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// worktreeListed reports whether `git worktree list --porcelain` output names
// path as a worktree of the repo. Each stanza opens with a "worktree <abs
// path>" line; every other line is ignored.
//
// Paths are compared after filepath.Clean so a trailing slash cannot cause a
// false negative. Callers pass a symlink-resolved path: git prints resolved
// paths, and on macOS a /var/... work dir is really /private/var/..., which
// would otherwise never match.
func worktreeListed(porcelain, path string) bool {
	if path == "" {
		return false
	}
	want := filepath.Clean(path)
	for _, line := range strings.Split(porcelain, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree ")
		if !ok {
			continue
		}
		if filepath.Clean(strings.TrimSpace(rest)) == want {
			return true
		}
	}
	return false
}

// mainCheckoutFromCommonDir turns `git rev-parse --git-common-dir` output into
// the main checkout's path: the common dir is <main>/.git, so its parent is the
// checkout. Empty output yields empty, never ".".
func mainCheckoutFromCommonDir(out string) string {
	common := strings.TrimSpace(out)
	if common == "" {
		return ""
	}
	return filepath.Dir(common)
}

// mainCheckoutFor resolves the main checkout of the repo containing dir, or ""
// when dir is not in a repo, git is missing, or git is too old for
// --path-format (added in 2.31; without it the output could be relative and
// unusable once dir is deleted).
//
// Called once at startup, while dir still exists: by the time a teardown needs
// it, the wrap-up command may have deleted the working directory out from under
// the process, leaving nowhere to run git from.
func mainCheckoutFor(dir string) string {
	if dir == "" {
		return ""
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse",
		"--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	return mainCheckoutFromCommonDir(string(out))
}

// worktreeIsGone reports whether the session's worktree has been torn down.
//
// Two ways to be gone, because a wrap-up may do either: the directory is
// deleted (the common case — `git worktree remove`), or the directory survives
// but git no longer registers it. Anything unclear — no main checkout captured,
// git unavailable, a stat error that isn't "not exist" — reports NOT gone. The
// gate this feeds guards a kill-session, so uncertainty must hold it shut.
func worktreeIsGone(ctx context.Context, workDir, mainCheckout string) bool {
	if workDir == "" {
		return false
	}
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		return true
	}
	if mainCheckout == "" {
		return false
	}
	out, err := exec.CommandContext(ctx, "git", "-C", mainCheckout,
		"worktree", "list", "--porcelain").Output()
	if err != nil {
		return false
	}
	return !worktreeListed(string(out), workDir)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -run 'TestWorktree|TestMainCheckout' -v`
Expected: PASS (5 test functions).

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/teardown.go cmd/claudemux-head/teardown_test.go
git commit -m "feat(head): add worktree-gone detection for teardown"
```

---

### Task 3: Phase type, ready gate, and chip renderer

**Files:**
- Modify: `cmd/claudemux-head/teardown.go`
- Test: `cmd/claudemux-head/teardown_test.go`

**Interfaces:**
- Consumes: `StateKind` and its constants from `state.go` (`StateIdle`, `StateThinking`, `StateTool`, `StateAwaiting`, `StateError`, `StateCompacting`).
- Produces:
  - `type teardownPhase int` with constants `teardownIdle`, `teardownSent`, `teardownReady`, `teardownExiting`
  - `func teardownTurnEnded(kind StateKind) bool`
  - `func teardownGateOpen(kind StateKind, inWorktree, worktreeGone bool) bool`
  - `func teardownChip(p teardownPhase, blocked bool, note string, noteAt, now time.Time) string`
  - `const teardownNoteTTL = 5 * time.Second`

- [ ] **Step 1: Write the failing tests**

Append to `cmd/claudemux-head/teardown_test.go` (add `"time"` to its imports):

```go
func TestTeardownTurnEnded(t *testing.T) {
	tests := []struct {
		kind StateKind
		want bool
	}{
		{StateIdle, true},
		{StateAwaiting, true}, // a wrap-up asking its confirmation is a real pause
		{StateError, true},
		{StateThinking, false},
		{StateTool, false},
		{StateCompacting, false},
	}
	for _, tt := range tests {
		if got := teardownTurnEnded(tt.kind); got != tt.want {
			t.Errorf("teardownTurnEnded(%v) = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestTeardownGateOpen(t *testing.T) {
	tests := []struct {
		name         string
		kind         StateKind
		inWorktree   bool
		worktreeGone bool
		want         bool
	}{
		{"worktree gone, turn ended", StateIdle, true, true, true},
		{"worktree gone but still working", StateTool, true, true, false},
		{"turn ended, worktree survives", StateIdle, true, false, false},
		{"awaiting confirmation, worktree survives", StateAwaiting, true, false, false},
		{"not a worktree, turn ended", StateIdle, false, false, true},
		{"not a worktree, still working", StateThinking, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := teardownGateOpen(tt.kind, tt.inWorktree, tt.worktreeGone)
			if got != tt.want {
				t.Errorf("teardownGateOpen(%v, %v, %v) = %v, want %v",
					tt.kind, tt.inWorktree, tt.worktreeGone, got, tt.want)
			}
		})
	}
}

func TestTeardownChip(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		phase   teardownPhase
		blocked bool
		note    string
		noteAt  time.Time
		want    string
	}{
		{"idle, nothing to say", teardownIdle, false, "", time.Time{}, ""},
		{"sent", teardownSent, false, "", time.Time{}, "⏻ wrapping up…"},
		{"sent but blocked", teardownSent, true, "", time.Time{}, "⏻ worktree still present"},
		{"ready", teardownReady, false, "", time.Time{}, "⏻ press x to tear down"},
		{"exiting", teardownExiting, false, "", time.Time{}, "⏻ exiting claude…"},
		{"fresh abort note", teardownIdle, false, "claude didn't exit", now.Add(-2 * time.Second), "⏻ claude didn't exit"},
		{"expired abort note", teardownIdle, false, "claude didn't exit", now.Add(-6 * time.Second), ""},
		{"note is ignored mid-teardown", teardownSent, false, "stale", now, "⏻ wrapping up…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := teardownChip(tt.phase, tt.blocked, tt.note, tt.noteAt, now)
			if got != tt.want {
				t.Errorf("teardownChip() = %q, want %q", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run TestTeardown -v`
Expected: compile failure — `undefined: teardownTurnEnded`.

- [ ] **Step 3: Write the implementation**

Append to `cmd/claudemux-head/teardown.go` (add `"time"` to its imports):

```go
// teardownPhase is where a session teardown has got to. It advances on the `x`
// key and on poll ticks; every phase but teardownIdle is visible in the status
// line, because a key that arms a kill-session must never be armed silently.
type teardownPhase int

const (
	teardownIdle teardownPhase = iota
	// teardownSent: the wrap-up command has been typed into the claude pane
	// (or skipped, when teardown.command is empty) and the ready gate is
	// being polled.
	teardownSent
	// teardownReady: the gate is open. The next `x` exits claude and kills
	// the session.
	teardownReady
	// teardownExiting: /exit has been sent; waiting for claude to actually be
	// gone before killing the session.
	teardownExiting
)

// teardownNoteTTL is how long an abort reason stays on the status line. Long
// enough to read after glancing away, short enough that it doesn't look like
// persistent state.
const teardownNoteTTL = 5 * time.Second

// teardownTurnEnded reports whether claude has stopped working.
//
// StateAwaiting counts as ended on purpose: the wrap-up command asking for its
// confirmation is a legitimate resting point, and the gate's second condition
// (the worktree being gone) cannot hold until that question is answered
// anyway. StateCompacting does NOT count — the session is mid-turn and about
// to keep going.
func teardownTurnEnded(kind StateKind) bool {
	switch kind {
	case StateThinking, StateTool, StateCompacting:
		return false
	}
	return true
}

// teardownGateOpen reports whether the second `x` press should be offered.
//
// For a worktree session the worktree must be gone, which is the whole signal
// that the wrap-up actually succeeded: a /done that bailed on uncommitted or
// unpushed work leaves it standing, and the gate stays shut. A session that
// was never in a worktree has no such evidence available, so it gates on the
// turn ending alone.
func teardownGateOpen(kind StateKind, inWorktree, worktreeGone bool) bool {
	if !teardownTurnEnded(kind) {
		return false
	}
	if !inWorktree {
		return true
	}
	return worktreeGone
}

// teardownChip renders the status-line chip for a teardown, or "" when there
// is nothing to show.
//
// An abort note is shown only from teardownIdle: it explains why a teardown
// stopped, so a note left over from an earlier attempt must never shadow a
// live phase.
func teardownChip(p teardownPhase, blocked bool, note string, noteAt, now time.Time) string {
	switch p {
	case teardownSent:
		if blocked {
			return "⏻ worktree still present"
		}
		return "⏻ wrapping up…"
	case teardownReady:
		return "⏻ press x to tear down"
	case teardownExiting:
		return "⏻ exiting claude…"
	}
	if note != "" && now.Sub(noteAt) < teardownNoteTTL {
		return "⏻ " + note
	}
	return ""
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -run TestTeardown -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/teardown.go cmd/claudemux-head/teardown_test.go
git commit -m "feat(head): add teardown phase, ready gate, and status chip"
```

---

### Task 4: tmux argument builders

**Files:**
- Modify: `cmd/claudemux-head/teardown.go`
- Test: `cmd/claudemux-head/teardown_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `func sendLiteralArgs(pane, text string) ([]string, bool)`
  - `func sendEnterArgs(pane string) ([]string, bool)`
  - `func killSessionArgs(session string) ([]string, bool)`

**Why the text and the Enter are separate commands:** typing `/done` in Claude Code opens a slash-command completion popup. An `Enter` arriving in the same input burst can be eaten by that popup (accepting the completion) instead of submitting the line. Sending the literal text, letting the TUI settle, then sending `Enter` as its own tmux call is the mitigation. `-l` sends the string literally so a command containing tmux key names (`Enter`, `C-c`) is typed rather than interpreted.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/claudemux-head/teardown_test.go`:

```go
func TestSendLiteralArgs(t *testing.T) {
	args, ok := sendLiteralArgs("%3", "/done")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{"send-keys", "-t", "%3", "-l", "/done"}
	if !slices.Equal(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
}

// Nothing to type, or nowhere to type it, must build no command at all —
// `send-keys -l ""` is a no-op that still costs a subprocess, and a missing
// pane target would send to whatever tmux considers current.
func TestSendLiteralArgsRefusesEmpty(t *testing.T) {
	if _, ok := sendLiteralArgs("", "/done"); ok {
		t.Error("empty pane accepted")
	}
	if _, ok := sendLiteralArgs("%3", ""); ok {
		t.Error("empty text accepted")
	}
}

func TestSendEnterArgs(t *testing.T) {
	args, ok := sendEnterArgs("%3")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{"send-keys", "-t", "%3", "Enter"}
	if !slices.Equal(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
	if _, ok := sendEnterArgs(""); ok {
		t.Error("empty pane accepted")
	}
}

func TestKillSessionArgs(t *testing.T) {
	args, ok := killSessionArgs("claudemux")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{"kill-session", "-t", "claudemux"}
	if !slices.Equal(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
	if _, ok := killSessionArgs(""); ok {
		t.Error("empty session accepted — would kill the current session")
	}
}
```

Add `"slices"` to the test file's imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestSend|TestKillSession' -v`
Expected: compile failure — `undefined: sendLiteralArgs`.

- [ ] **Step 3: Write the implementation**

Append to `cmd/claudemux-head/teardown.go`:

```go
// sendLiteralArgs builds the tmux call that types text into pane verbatim.
//
// -l sends the string literally: without it tmux parses the argument as key
// names, so a configured command containing "Enter" or "C-c" would be
// interpreted rather than typed.
//
// The Enter that submits is a SEPARATE call (sendEnterArgs) with a delay
// between them — see this file's teardownSendCmd for why.
func sendLiteralArgs(pane, text string) ([]string, bool) {
	if pane == "" || text == "" {
		return nil, false
	}
	return []string{"send-keys", "-t", pane, "-l", text}, true
}

// sendEnterArgs builds the tmux call that submits whatever is in pane's input.
func sendEnterArgs(pane string) ([]string, bool) {
	if pane == "" {
		return nil, false
	}
	return []string{"send-keys", "-t", pane, "Enter"}, true
}

// killSessionArgs builds the tmux call that ends the session.
//
// An empty session is refused rather than defaulted: `kill-session` with no -t
// kills the *current* session, so a failed lookup would still destroy
// something, just not necessarily the right thing.
func killSessionArgs(session string) ([]string, bool) {
	if session == "" {
		return nil, false
	}
	return []string{"kill-session", "-t", session}, true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -run 'TestSend|TestKillSession' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/teardown.go cmd/claudemux-head/teardown_test.go
git commit -m "feat(head): add tmux argument builders for teardown"
```

---

### Task 5: `mappedTranscript` reports the claude pane it picked

**Files:**
- Modify: `cmd/claudemux-head/panemap.go:196-216`
- Modify: `cmd/claudemux-head/tui.go:408` (the one caller)
- Test: `cmd/claudemux-head/panemap_test.go`

**Interfaces:**
- Consumes: `claudePaneCandidates(listing, self string) []string` and `panePaths(listing string) map[string]string`, both already in `panemap.go`.
- Produces: `func mappedTranscript(selfPane, dir string) (transcript, cwd, pane string, ok bool)` — a **four**-value signature; the new third return is the tmux pane id (`%3`) of the claude pane whose transcript is being followed, or `""` when no candidate exists.

**Why:** teardown must type into the *same* pane whose transcript the head reads, otherwise a two-session window could get `/done` in the wrong place. `claudePaneCandidates` already ranks panes correctly; `mappedTranscript` picks one and currently throws the id away.

**Bonus this unlocks:** "claude has exited" needs no new detection — once claude is gone that pane's `pane_current_command` is a shell, so `claudePaneCandidates` returns nothing and `ok` is false.

- [ ] **Step 1: Write the failing test**

Append to `cmd/claudemux-head/panemap_test.go`:

```go
// The pane whose transcript is followed must be reported, so a teardown types
// into that exact pane rather than re-deriving a possibly different one.
func TestMappedTranscriptReportsPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	// Outside tmux there is no self pane, so the lookup can't run at all.
	// This asserts the shape of that path: four values, all zero.
	transcript, cwd, pane, ok := mappedTranscript("", t.TempDir())
	if ok || transcript != "" || cwd != "" || pane != "" {
		t.Errorf("mappedTranscript(\"\", _) = (%q, %q, %q, %v), want all zero",
			transcript, cwd, pane, ok)
	}
}
```

Add `"os/exec"` to the test file's imports if absent.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run TestMappedTranscriptReportsPane -v`
Expected: compile failure — `assignment mismatch: 4 variables but mappedTranscript returns 3 values`.

- [ ] **Step 3: Change the signature**

In `cmd/claudemux-head/panemap.go`, update the doc comment's return list to mention `pane`, then:

```go
func mappedTranscript(selfPane, dir string) (string, string, string, bool) {
	listing := listPanes(selfPane)
	if listing == "" {
		return "", "", "", false
	}
	candidates := claudePaneCandidates(listing, selfPane)
	if len(candidates) == 0 {
		return "", "", "", false
	}
	paths := panePaths(listing)
	for _, pane := range candidates {
		if sid, ok := readPaneSession(dir, pane); ok {
			transcript, _ := transcriptForSession(claudeProjectsPath(), sid)
			return transcript, paths[pane], pane, true
		}
	}
	// No candidate has a recorded session id yet. Still report the preferred
	// pane's live cwd so the worktree chip tracks reality; there's no
	// transcript to follow until the hook writes a map for it. The pane id is
	// reported regardless — a teardown can type into a pane whose transcript
	// is not yet known.
	return "", paths[candidates[0]], candidates[0], true
}
```

Add to the doc comment above it:

```go
//   - pane is the tmux pane id of the claude pane selected, or "" when ok is
//     false. Teardown types into this pane, so it must be the same one whose
//     transcript is followed.
```

- [ ] **Step 4: Update the caller**

In `cmd/claudemux-head/tui.go`, inside `pollData` (~line 408):

```go
			if mapped, _, _, ok := mappedTranscript(selfPane, paneDir); ok {
```

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: PASS. If any other call site fails to compile, fix it the same way (`grep -rn 'mappedTranscript' cmd/`).

- [ ] **Step 6: Commit**

```bash
git add cmd/claudemux-head/panemap.go cmd/claudemux-head/panemap_test.go cmd/claudemux-head/tui.go
git commit -m "feat(head): report the claude pane id from mappedTranscript"
```

---

### Task 6: The teardown `tea.Cmd`s

**Files:**
- Modify: `cmd/claudemux-head/teardown.go`
- Test: `cmd/claudemux-head/teardown_test.go`

**Interfaces:**
- Consumes: `mappedTranscript` (4-value, Task 5), `sendLiteralArgs` / `sendEnterArgs` / `killSessionArgs` (Task 4), `worktreeIsGone` (Task 2), `tabResetTimeout`-style bounded contexts.
- Produces:
  - `type teardownSentMsg struct{ note string }` — `note` empty on success, otherwise the abort reason
  - `type teardownProbeMsg struct{ worktreeGone bool }`
  - `type claudeGoneMsg struct{ gone bool }`
  - `func teardownSendCmd(selfPane, paneDir, text string) tea.Cmd`
  - `func teardownProbeCmd(workDir, mainCheckout string) tea.Cmd`
  - `func claudeGoneCmd(selfPane, paneDir string) tea.Cmd`
  - `func killSessionCmd(selfPane string) tea.Cmd`
  - `const teardownKeyDelay = 250 * time.Millisecond`
  - `const teardownTmuxTimeout = 2 * time.Second`

- [ ] **Step 1: Write the failing tests**

Append to `cmd/claudemux-head/teardown_test.go`:

```go
// Outside tmux there is no pane to type into; the command must report that
// rather than shelling out or silently succeeding.
func TestTeardownSendCmdNoPane(t *testing.T) {
	msg := teardownSendCmd("", t.TempDir(), "/done")()
	sent, ok := msg.(teardownSentMsg)
	if !ok {
		t.Fatalf("msg = %T, want teardownSentMsg", msg)
	}
	if sent.note != "no claude pane" {
		t.Errorf("note = %q, want %q", sent.note, "no claude pane")
	}
}

// The probe reports gone for a work dir that no longer exists — no tmux, no
// repo, no session required.
func TestTeardownProbeCmdMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "deleted")
	msg := teardownProbeCmd(missing, "")()
	probe, ok := msg.(teardownProbeMsg)
	if !ok {
		t.Fatalf("msg = %T, want teardownProbeMsg", msg)
	}
	if !probe.worktreeGone {
		t.Error("worktreeGone = false, want true")
	}
}

func TestTeardownProbeCmdLiveDir(t *testing.T) {
	msg := teardownProbeCmd(t.TempDir(), "")()
	if probe := msg.(teardownProbeMsg); probe.worktreeGone {
		t.Error("worktreeGone = true for a directory that still exists")
	}
}

// Outside tmux nothing can be observed, so "gone" must be false: reporting
// gone would let the exit wait fall through to a kill-session.
func TestClaudeGoneCmdOutsideTmux(t *testing.T) {
	msg := claudeGoneCmd("", t.TempDir())()
	gone, ok := msg.(claudeGoneMsg)
	if !ok {
		t.Fatalf("msg = %T, want claudeGoneMsg", msg)
	}
	if gone.gone {
		t.Error("gone = true outside tmux")
	}
}

// A kill with no pane to resolve a session from must do nothing at all.
func TestKillSessionCmdNoPane(t *testing.T) {
	if cmd := killSessionCmd(""); cmd != nil {
		t.Error("killSessionCmd(\"\") returned a command")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestTeardownSend|TestTeardownProbe|TestClaudeGone|TestKillSession' -v`
Expected: compile failure — `undefined: teardownSendCmd`.

- [ ] **Step 3: Write the implementation**

Append to `cmd/claudemux-head/teardown.go` (add `tea "github.com/charmbracelet/bubbletea"` to imports):

```go
// teardownKeyDelay separates the literal text from the Enter that submits it.
// Claude Code opens a completion popup as a slash command is typed; an Enter
// arriving in the same burst can be consumed selecting the completion instead
// of submitting the line. A quarter second is imperceptible to the user and
// ample for the TUI to settle.
const teardownKeyDelay = 250 * time.Millisecond

// teardownTmuxTimeout bounds each tmux subprocess, matching the ceiling used
// everywhere else in this package. A wedged tmux server must never block the
// TUI.
const teardownTmuxTimeout = 2 * time.Second

// teardownSentMsg reports the outcome of typing the wrap-up command. note is
// empty on success and an abort reason otherwise — it is rendered verbatim in
// the status chip.
type teardownSentMsg struct{ note string }

// teardownProbeMsg carries one ready-gate observation.
type teardownProbeMsg struct{ worktreeGone bool }

// claudeGoneMsg reports whether any pane in this session is still running
// claude.
type claudeGoneMsg struct{ gone bool }

// teardownSendCmd types text into the session's claude pane and submits it.
//
// The pane is resolved here rather than cached on the model so it is always
// the pane whose transcript the head currently follows, even if the session
// rotated a moment ago.
func teardownSendCmd(selfPane, paneDir, text string) tea.Cmd {
	return func() tea.Msg {
		_, _, pane, ok := mappedTranscript(selfPane, paneDir)
		if !ok || pane == "" {
			return teardownSentMsg{note: "no claude pane"}
		}
		literal, ok := sendLiteralArgs(pane, text)
		if !ok {
			return teardownSentMsg{note: "wrap-up didn't submit"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		defer cancel()
		if err := exec.CommandContext(ctx, "tmux", literal...).Run(); err != nil {
			return teardownSentMsg{note: "wrap-up didn't submit"}
		}

		time.Sleep(teardownKeyDelay) // see teardownKeyDelay; this runs off the Update loop

		enter, ok := sendEnterArgs(pane)
		if !ok {
			return teardownSentMsg{note: "wrap-up didn't submit"}
		}
		if err := exec.CommandContext(ctx, "tmux", enter...).Run(); err != nil {
			return teardownSentMsg{note: "wrap-up didn't submit"}
		}
		// Success here means the keystrokes were delivered, NOT that claude
		// accepted them. The model separately watches the transcript for
		// evidence of a submitted prompt and aborts on teardownSubmitTimeout.
		return teardownSentMsg{}
	}
}

// teardownProbeCmd takes one ready-gate reading.
func teardownProbeCmd(workDir, mainCheckout string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		defer cancel()
		return teardownProbeMsg{worktreeGone: worktreeIsGone(ctx, workDir, mainCheckout)}
	}
}

// claudeGoneCmd reports whether claude has exited: no pane in the session runs
// claude any more, which is exactly what mappedTranscript failing to find a
// candidate means. Outside tmux nothing is observable, so it reports not-gone
// — the exit wait then times out rather than falling through to a kill.
func claudeGoneCmd(selfPane, paneDir string) tea.Cmd {
	return func() tea.Msg {
		if selfPane == "" {
			return claudeGoneMsg{}
		}
		_, _, pane, ok := mappedTranscript(selfPane, paneDir)
		return claudeGoneMsg{gone: !ok || pane == ""}
	}
}

// killSessionCmd ends the tmux session this pane lives in. It is the last
// thing the process does: the kill takes the head down with everything else,
// so there is no message to return and no state to render afterwards.
//
// nil when there is no pane to resolve a session from, so callers can append
// it unconditionally — the same contract as renameTabCmd.
func killSessionCmd(selfPane string) tea.Cmd {
	if selfPane == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, "tmux", "display-message",
			"-p", "-t", selfPane, "#{session_name}").Output()
		if err != nil {
			return nil
		}
		args, ok := killSessionArgs(strings.TrimSpace(string(out)))
		if !ok {
			return nil
		}
		_ = exec.CommandContext(ctx, "tmux", args...).Run()
		return nil
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -v`
Expected: PASS (whole package — the new commands must not disturb existing tests).

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/teardown.go cmd/claudemux-head/teardown_test.go
git commit -m "feat(head): add teardown commands for send, probe, exit-wait, and kill"
```

---

### Task 7: Wire the state machine into the model

**Files:**
- Modify: `cmd/claudemux-head/tui.go` (model struct ~line 93, `newModel` ~line 183, `Update` key handling ~line 531, `tickMsg` ~line 557, `dataMsg` ~line 604)
- Test: `cmd/claudemux-head/tui_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1–6, plus existing `m.state` (`State`), `m.lastPrompt` (`string`), `m.selfPane`, `m.paneDir`, `worktreeNameForCwd(cwd string) string` from `worktree.go`.
- Produces: model fields `teardown`, `teardownCmdText`, `teardownAt`, `teardownPrompt`, `teardownSubmitted`, `teardownBlocked`, `teardownProbing`, `teardownNote`, `teardownNoteAt`, `workDir`, `mainCheckout`, `inWorktree`; and `func (m model) teardownKey() (model, tea.Cmd)`.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/claudemux-head/tui_test.go`:

```go
// A model with just enough set up to exercise teardown transitions.
func teardownTestModel() model {
	return model{
		ready:           true,
		width:           120,
		height:          4,
		selfPane:        "%1",
		paneDir:         "/tmp/panemap",
		workDir:         "/tmp/wt",
		inWorktree:      true,
		teardownCmdText: "/done",
		state:           State{Kind: StateIdle},
	}
}

func TestTeardownKeyArmsFromIdle(t *testing.T) {
	m := teardownTestModel()
	got, cmd := m.teardownKey()
	if got.teardown != teardownSent {
		t.Errorf("phase = %v, want teardownSent", got.teardown)
	}
	if cmd == nil {
		t.Error("no command issued to send the wrap-up")
	}
}

// Outside tmux there is nothing to type into and nothing to kill.
func TestTeardownKeyInertOutsideTmux(t *testing.T) {
	m := teardownTestModel()
	m.selfPane = ""
	got, cmd := m.teardownKey()
	if got.teardown != teardownIdle {
		t.Errorf("phase = %v, want teardownIdle", got.teardown)
	}
	if cmd != nil {
		t.Error("command issued outside tmux")
	}
}

// An empty teardown.command still arms, but types nothing.
func TestTeardownKeyEmptyCommandSkipsSend(t *testing.T) {
	m := teardownTestModel()
	m.teardownCmdText = ""
	got, cmd := m.teardownKey()
	if got.teardown != teardownSent {
		t.Errorf("phase = %v, want teardownSent", got.teardown)
	}
	if cmd != nil {
		t.Error("command issued despite empty teardown.command")
	}
	if !got.teardownSubmitted {
		t.Error("submitted = false; nothing was sent, so nothing can be awaited")
	}
}

// A press while the gate is still shut must not advance anything.
func TestTeardownKeyIgnoredWhileSent(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownSent
	got, cmd := m.teardownKey()
	if got.teardown != teardownSent {
		t.Errorf("phase = %v, want teardownSent", got.teardown)
	}
	if cmd != nil {
		t.Error("command issued from teardownSent")
	}
}

func TestTeardownKeyFromReadyExits(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownReady
	got, cmd := m.teardownKey()
	if got.teardown != teardownExiting {
		t.Errorf("phase = %v, want teardownExiting", got.teardown)
	}
	if cmd == nil {
		t.Error("no command issued to exit claude")
	}
}

// esc cancels an armed teardown instead of quitting the head.
func TestEscCancelsTeardown(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownReady
	m.teardownBlocked = true
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := next.(model)
	if got.teardown != teardownIdle {
		t.Errorf("phase = %v, want teardownIdle", got.teardown)
	}
	if got.teardownBlocked {
		t.Error("blocked flag survived cancel")
	}
	if cmd != nil {
		t.Error("esc during teardown issued a command (quit?)")
	}
}

// With no teardown armed, esc still quits — the pre-existing binding.
func TestEscQuitsWhenIdle(t *testing.T) {
	m := teardownTestModel()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc did not quit from teardownIdle")
	}
	if msg := cmd(); msg == nil {
		t.Error("esc issued a no-op command instead of quitting")
	}
}

// The probe opening the gate promotes sent → ready.
func TestTeardownProbeOpensGate(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownSent
	m.teardownProbing = true
	next, _ := m.Update(teardownProbeMsg{worktreeGone: true})
	got := next.(model)
	if got.teardown != teardownReady {
		t.Errorf("phase = %v, want teardownReady", got.teardown)
	}
	if got.teardownProbing {
		t.Error("probing flag still held")
	}
}

// A wrap-up that bailed leaves the worktree standing: the gate stays shut and
// the pane says why.
func TestTeardownProbeBlocksWhenWorktreeSurvives(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownSent
	next, _ := m.Update(teardownProbeMsg{worktreeGone: false})
	got := next.(model)
	if got.teardown != teardownSent {
		t.Errorf("phase = %v, want teardownSent", got.teardown)
	}
	if !got.teardownBlocked {
		t.Error("blocked = false, want true")
	}
}

// Claude exiting during the wait triggers the kill.
func TestClaudeGoneTriggersKill(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownExiting
	_, cmd := m.Update(claudeGoneMsg{gone: true})
	if cmd == nil {
		t.Error("no kill command issued once claude exited")
	}
}

// Claude still alive keeps waiting.
func TestClaudeStillAliveKeepsWaiting(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownExiting
	next, cmd := m.Update(claudeGoneMsg{gone: false})
	if got := next.(model); got.teardown != teardownExiting {
		t.Errorf("phase = %v, want teardownExiting", got.teardown)
	}
	if cmd != nil {
		t.Error("kill command issued while claude was still running")
	}
}

// A wrap-up that never reaches the transcript aborts rather than hanging.
func TestTeardownSubmitTimeoutAborts(t *testing.T) {
	now := time.Now()
	m := teardownTestModel()
	m.teardown = teardownSent
	m.teardownAt = now.Add(-teardownSubmitTimeout - time.Second)
	next, _ := m.Update(tickMsg(now))
	got := next.(model)
	if got.teardown != teardownIdle {
		t.Errorf("phase = %v, want teardownIdle", got.teardown)
	}
	if got.teardownNote != "wrap-up didn't submit" {
		t.Errorf("note = %q, want %q", got.teardownNote, "wrap-up didn't submit")
	}
}

// A claude that will not exit aborts, leaving the session alive.
func TestTeardownExitTimeoutAborts(t *testing.T) {
	now := time.Now()
	m := teardownTestModel()
	m.teardown = teardownExiting
	m.teardownAt = now.Add(-teardownExitTimeout - time.Second)
	next, _ := m.Update(tickMsg(now))
	got := next.(model)
	if got.teardown != teardownIdle {
		t.Errorf("phase = %v, want teardownIdle", got.teardown)
	}
	if got.teardownNote != "claude didn't exit" {
		t.Errorf("note = %q, want %q", got.teardownNote, "claude didn't exit")
	}
}

// Evidence that the command reached claude clears the submit deadline.
func TestTeardownSubmitObservedViaBusyState(t *testing.T) {
	now := time.Now()
	m := teardownTestModel()
	m.teardown = teardownSent
	m.teardownAt = now.Add(-time.Second)
	m.state = State{Kind: StateTool}
	next, _ := m.Update(tickMsg(now))
	if got := next.(model); !got.teardownSubmitted {
		t.Error("submitted = false despite claude going busy")
	}
}

// A new prompt in the transcript is the other piece of evidence.
func TestTeardownSubmitObservedViaNewPrompt(t *testing.T) {
	now := time.Now()
	m := teardownTestModel()
	m.teardown = teardownSent
	m.teardownAt = now.Add(-time.Second)
	m.teardownPrompt = "earlier thing"
	m.lastPrompt = "/done"
	next, _ := m.Update(tickMsg(now))
	if got := next.(model); !got.teardownSubmitted {
		t.Error("submitted = false despite a new prompt landing")
	}
}

// A failed send aborts immediately with the reason the command reported.
func TestTeardownSentMsgWithNoteAborts(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownSent
	next, _ := m.Update(teardownSentMsg{note: "no claude pane"})
	got := next.(model)
	if got.teardown != teardownIdle {
		t.Errorf("phase = %v, want teardownIdle", got.teardown)
	}
	if got.teardownNote != "no claude pane" {
		t.Errorf("note = %q, want %q", got.teardownNote, "no claude pane")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestTeardown|TestEsc|TestClaude' -v`
Expected: compile failure — `unknown field workDir in struct literal`.

- [ ] **Step 3: Add the model fields**

In `cmd/claudemux-head/tui.go`, add to the `model` struct, after the `tabPinned` block:

```go
	// Teardown state: `x` wraps the session up and kills its tmux session.
	// See teardown.go. Like tabPinned, this is deliberately not persisted —
	// an armed kill that survived a head restart would be a trap.
	teardown teardownPhase
	// teardownCmdText is teardown.command from config, typed into the claude
	// pane on the first press. Empty means skip that step entirely.
	teardownCmdText string
	// teardownAt stamps the current phase, so the submit and exit deadlines
	// measure from the transition rather than from process start.
	teardownAt time.Time
	// teardownPrompt is lastPrompt as it stood when the wrap-up was sent. A
	// change in lastPrompt is one of the two signals that the keystrokes
	// actually reached claude (the other is the session going busy).
	teardownPrompt    string
	teardownSubmitted bool
	// teardownBlocked records that the turn ended with the worktree still
	// standing — a wrap-up that bailed. It drives the status chip; the gate
	// simply stays shut.
	teardownBlocked bool
	teardownProbing bool
	// teardownNote is the reason the last teardown aborted, shown for
	// teardownNoteTTL and then dropped.
	teardownNote   string
	teardownNoteAt time.Time

	// workDir is the launch directory with symlinks resolved, captured at
	// startup because the wrap-up command deletes it out from under this
	// process: once it is gone os.Getwd() fails, so it cannot be re-derived
	// at the moment it is needed. mainCheckout is captured for the same
	// reason — there is no valid cwd left to run git from.
	workDir      string
	mainCheckout string
	inWorktree   bool
```

- [ ] **Step 4: Capture the startup values in `newModel`**

In `newModel`, add to the struct literal:

```go
		teardownCmdText:    cfg.Teardown.Command,
```

and after the literal, before `m.recomputeFromEvents(time.Now())`:

```go
	// Captured while the directory still exists — see the workDir field.
	if wd, err := os.Getwd(); err == nil {
		if resolved, err := filepath.EvalSymlinks(wd); err == nil {
			wd = resolved
		}
		m.workDir = wd
		m.inWorktree = worktreeNameForCwd(wd) != ""
		m.mainCheckout = mainCheckoutFor(wd)
	}
```

Ensure `path/filepath` is imported in `tui.go` (it is, for `filepath.Dir`).

- [ ] **Step 5: Add `teardownKey` and the abort helper**

Append to `cmd/claudemux-head/tui.go`:

```go
// teardownKey advances the teardown state machine one press of `x`.
//
// Only two phases accept a press: idle arms the wrap-up, ready commits to the
// kill. The two waiting phases ignore it — a key that does nothing beats a key
// that does something surprising while the pane is mid-sequence.
func (m model) teardownKey() (model, tea.Cmd) {
	switch m.teardown {
	case teardownIdle:
		if m.selfPane == "" {
			return m, nil // not in tmux: nothing to type into, nothing to kill
		}
		m.teardown = teardownSent
		m.teardownAt = time.Now()
		m.teardownPrompt = m.lastPrompt
		m.teardownBlocked = false
		m.teardownNote = ""
		if m.teardownCmdText == "" {
			// Nothing was typed, so there is no submission to wait for; the
			// gate is all that stands between here and ready.
			m.teardownSubmitted = true
			return m, nil
		}
		m.teardownSubmitted = false
		return m, teardownSendCmd(m.selfPane, m.paneDir, m.teardownCmdText)

	case teardownReady:
		m.teardown = teardownExiting
		m.teardownAt = time.Now()
		return m, teardownSendCmd(m.selfPane, m.paneDir, "/exit")
	}
	return m, nil
}

// abortTeardown returns to idle with a reason on the status line. Nothing that
// already happened is undone — the wrap-up command has run, and only this
// program's own sequencing stops.
func (m model) abortTeardown(note string, now time.Time) model {
	m.teardown = teardownIdle
	m.teardownBlocked = false
	m.teardownProbing = false
	m.teardownSubmitted = false
	m.teardownNote = note
	m.teardownNoteAt = now
	return m
}
```

- [ ] **Step 6: Wire the keys**

In `Update`'s `tea.KeyMsg` case, replace the existing quit case and add `x`:

```go
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc":
			// esc cancels an armed teardown rather than quitting: a key that
			// arms a kill-session needs a way out, and adding a second cancel
			// key to a four-key TUI would be worse.
			if m.teardown != teardownIdle {
				m.teardown = teardownIdle
				m.teardownBlocked = false
				m.teardownProbing = false
				m.teardownNote = ""
				return m, nil
			}
			return m, tea.Quit
		case "x":
			return m.teardownKey()
		case "r":
```

(leave the `r` body unchanged.)

Note: `teardownKey` returns `(model, tea.Cmd)` while `Update` returns `(tea.Model, tea.Cmd)`; Go converts the concrete `model` to `tea.Model` automatically on return, so `return m.teardownKey()` compiles.

- [ ] **Step 7: Wire the tick**

In `Update`'s `tickMsg` case, after the existing `cmds` are assembled and before `return m, tea.Batch(cmds...)`:

```go
		now := time.Time(msg)
		switch m.teardown {
		case teardownSent:
			// Evidence the keystrokes landed: claude went busy, or a new
			// prompt appeared in the transcript.
			if !m.teardownSubmitted &&
				(!teardownTurnEnded(m.state.Kind) || m.lastPrompt != m.teardownPrompt) {
				m.teardownSubmitted = true
			}
			if !m.teardownSubmitted && now.Sub(m.teardownAt) >= teardownSubmitTimeout {
				return m.abortTeardown("wrap-up didn't submit", now), tea.Batch(cmds...)
			}
			if !m.teardownProbing {
				m.teardownProbing = true
				cmds = append(cmds, teardownProbeCmd(m.workDir, m.mainCheckout))
			}
		case teardownExiting:
			if now.Sub(m.teardownAt) >= teardownExitTimeout {
				return m.abortTeardown("claude didn't exit", now), tea.Batch(cmds...)
			}
			if !m.teardownProbing {
				m.teardownProbing = true
				cmds = append(cmds, claudeGoneCmd(m.selfPane, m.paneDir))
			}
		}
		return m, tea.Batch(cmds...)
```

Add the timeouts to `teardown.go`, beside `teardownNoteTTL`:

```go
// teardownSubmitTimeout bounds the wait for evidence that the wrap-up command
// actually reached claude. Injecting keystrokes into someone else's TUI is
// best-effort, so it is checked rather than assumed: a command left typed but
// unsubmitted aborts loudly instead of hanging in "wrapping up…" forever.
const teardownSubmitTimeout = 10 * time.Second

// teardownExitTimeout bounds the wait for claude to exit. On expiry the
// session is left ALIVE — letting claude finish on its own terms is the entire
// reason the kill comes last.
const teardownExitTimeout = 15 * time.Second
```

- [ ] **Step 8: Wire the messages**

Add three cases to `Update`, after the `summaryMsg` case:

```go
	case teardownSentMsg:
		if m.teardown != teardownSent && m.teardown != teardownExiting {
			return m, nil // cancelled while the send was in flight
		}
		if msg.note != "" {
			return m.abortTeardown(msg.note, time.Now()), nil
		}

	case teardownProbeMsg:
		m.teardownProbing = false
		if m.teardown != teardownSent {
			return m, nil
		}
		if teardownGateOpen(m.state.Kind, m.inWorktree, msg.worktreeGone) {
			m.teardown = teardownReady
			m.teardownAt = time.Now()
			m.teardownBlocked = false
			return m, nil
		}
		// The turn is over and the worktree is still there: the wrap-up
		// bailed. Say so and keep polling — the user may still be answering.
		m.teardownBlocked = m.inWorktree && teardownTurnEnded(m.state.Kind)

	case claudeGoneMsg:
		m.teardownProbing = false
		if m.teardown != teardownExiting || !msg.gone {
			return m, nil
		}
		return m, killSessionCmd(m.selfPane)
```

- [ ] **Step 9: Run the tests**

Run: `go test ./cmd/claudemux-head/ -v`
Expected: PASS, including every pre-existing test.

- [ ] **Step 10: Vet and commit**

```bash
go vet ./...
git add cmd/claudemux-head/tui.go cmd/claudemux-head/tui_test.go cmd/claudemux-head/teardown.go
git commit -m "feat(head): press x to wrap up and tear down the session"
```

---

### Task 8: Status-line chip and documentation

**Files:**
- Modify: `cmd/claudemux-head/tui.go` (`renderStatusbar` ~line 821, `renderStateLine` ~line 937)
- Modify: `README.md`
- Test: `cmd/claudemux-head/tui_test.go`

**Interfaces:**
- Consumes: `teardownChip` (Task 3), the model fields from Task 7.
- Produces: `func (m model) teardownChipText(now time.Time) string`.

**Placement rule:** the teardown chip takes the slot `⬚ pinned` occupies, and wins when both apply — it is transient and actionable, the pin is ambient. Both renderers must agree, because which one runs depends only on pane height.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/claudemux-head/tui_test.go`:

```go
func TestTeardownChipInStateLine(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownReady
	if got := renderStateLine(m, time.Now()); !strings.Contains(got, "⏻ press x to tear down") {
		t.Errorf("state line missing teardown chip:\n%s", got)
	}
}

func TestTeardownChipInStatusbar(t *testing.T) {
	m := teardownTestModel()
	m.height = 1
	m.teardown = teardownSent
	if got := renderStatusbar(m, time.Now(), ""); !strings.Contains(got, "⏻ wrapping up…") {
		t.Errorf("statusbar missing teardown chip:\n%s", got)
	}
}

// Both chips compete for one slot; the transient, actionable one wins.
func TestTeardownChipBeatsPinned(t *testing.T) {
	m := teardownTestModel()
	m.tabPinned = true
	m.teardown = teardownReady
	got := renderStateLine(m, time.Now())
	if !strings.Contains(got, "⏻ press x to tear down") {
		t.Errorf("teardown chip missing:\n%s", got)
	}
	if strings.Contains(got, "⬚ pinned") {
		t.Errorf("pin chip should have yielded to the teardown chip:\n%s", got)
	}
}

// With no teardown running the pin renders exactly as before.
func TestPinnedChipUnaffectedWhenIdle(t *testing.T) {
	m := teardownTestModel()
	m.tabPinned = true
	got := renderStateLine(m, time.Now())
	if !strings.Contains(got, "⬚ pinned") {
		t.Errorf("pin chip missing when no teardown is armed:\n%s", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestTeardownChip|TestPinnedChip' -v`
Expected: FAIL — `state line missing teardown chip`.

- [ ] **Step 3: Add the accessor**

Append to `cmd/claudemux-head/tui.go`:

```go
// teardownChipText renders this model's teardown chip, or "" when there is
// nothing to show.
func (m model) teardownChipText(now time.Time) string {
	return teardownChip(m.teardown, m.teardownBlocked, m.teardownNote, m.teardownNoteAt, now)
}
```

- [ ] **Step 4: Render it in both status lines**

In `renderStatusbar`, replace:

```go
	if m.tabPinned {
		leftParts = append(leftParts, "⬚ pinned")
	}
```

with:

```go
	// The teardown chip takes the pin's slot and wins when both apply: it is
	// transient and actionable, the pin is ambient. Both sit ahead of the
	// worktree chip for the clipping reason documented above.
	if td := m.teardownChipText(now); td != "" {
		leftParts = append(leftParts, td)
	} else if m.tabPinned {
		leftParts = append(leftParts, "⬚ pinned")
	}
```

In `renderStateLine`, replace:

```go
	if m.tabPinned {
		parts = append(parts, "⬚ pinned")
	}
```

with:

```go
	if td := m.teardownChipText(now); td != "" {
		parts = append(parts, td)
	} else if m.tabPinned {
		parts = append(parts, "⬚ pinned")
	}
```

- [ ] **Step 5: Run the full suite**

Run: `go test ./... && go vet ./...`
Expected: PASS, no vet findings.

- [ ] **Step 6: Document it in the README**

In the **Configuration** section's `config.yml` block, add after the `launch:` block:

```yaml
teardown:
  command: /done
```

Add to the bullet list below that block:

```markdown
- `teardown.command` — the wrap-up command the status pane types into the `claude`
  pane when you press `x` (see **Tearing down a session** below). Default `/done`.
  Set it to `""` to skip that step, making `x` a gated exit-and-kill.
```

Add a new subsection immediately after **Tab titles** (after the paragraph beginning
"Sessions cloned with `-n`"):

```markdown
### Tearing down a session

When the work is finished, click the status pane and press `x`. It runs the whole
wrap-up in order:

1. The first press types `teardown.command` (`/done` by default) into the `claude`
   pane and submits it. Answer whatever it asks exactly as you would have by hand.
   The status pane shows `⏻ wrapping up…`.
2. Once the turn ends **and** the session's worktree is gone, the pane shows
   `⏻ press x to tear down`. If the wrap-up bailed — uncommitted work, unpushed
   commits, you declined — the worktree is still there, so the gate never opens and
   the pane says `⏻ worktree still present` instead.
3. The second press sends `/exit`, waits for `claude` to actually be gone, and then
   kills the tmux session.

`esc` cancels a teardown in progress. **It does not undo anything** — by then the
wrap-up command has already run; cancelling only stops the status pane from driving
the rest.

Nothing here is silent. Every abort names its reason on the status line —
`⏻ wrap-up didn't submit` (the command never reached `claude`),
`⏻ claude didn't exit` (it was still running after 15 seconds, so the session was
left alive), `⏻ no claude pane`.

Sessions that aren't in a worktree have no deletion to verify, so the gate opens as
soon as the wrap-up turn ends.
```

- [ ] **Step 7: Commit**

```bash
git add cmd/claudemux-head/tui.go cmd/claudemux-head/tui_test.go README.md
git commit -m "feat(head): show the teardown chip and document press-x teardown"
```

---

### Task 9: Live verification

**Files:** none — this task changes nothing, it proves the feature works outside the test suite.

**Interfaces:**
- Consumes: the installed `claudemux` / `claudemux-head`.
- Produces: nothing.

**Why a task at all:** every previous task tested pure functions and model transitions. Not one of them proved that `send-keys -l "/done"` followed by `Enter` actually submits a slash command in Claude Code's TUI — the one part of this feature that depends on another program's input handling. That has to be seen.

- [ ] **Step 1: Build and install the head**

```bash
go build -o "$(go env GOPATH)/bin/claudemux-head" ./cmd/claudemux-head
```

- [ ] **Step 2: Prove the send works at all**

In a scratch claudemux session, from a shell (not the status pane), with `%N` being the claude pane's id (`tmux list-panes -F '#{pane_id} #{pane_current_command}'`):

```bash
tmux send-keys -t %N -l "/done"
sleep 0.25
tmux send-keys -t %N Enter
```

Expected: `/done` is submitted in the claude pane, not left sitting in the input box with a completion popup open.

**If it does not submit:** the completion popup ate the `Enter`. Fix by sending a second `Enter` in `teardownSendCmd` after another `teardownKeyDelay`, or by raising `teardownKeyDelay`. Whichever it takes, record the finding in a comment on `teardownKeyDelay` — this is the value future readers will wonder about.

- [ ] **Step 3: Drive the real key**

In a scratch session in a worktree with committed, pushed work: click the status pane, press `x`, answer the wrap-up, watch for `⏻ press x to tear down`, press `x` again.

Expected: `claude` exits, then the tmux session disappears.

- [ ] **Step 4: Prove the gate holds shut**

In a worktree with a deliberately dirty file: press `x`, let the wrap-up bail.

Expected: the pane shows `⏻ worktree still present` and the second `x` does nothing. Press `esc`; the chip clears and the session survives.

- [ ] **Step 5: Commit any fixes**

```bash
git add -A
git commit -m "fix(head): <what live testing turned up>"
```

Skip if nothing needed changing.

---

## Self-Review

**Spec coverage.** Every section of the spec maps to a task: interaction and the `x` rationale → Tasks 6–8; state machine table → Task 7; sending the wrap-up command (split send-keys, submit verification, empty-command path) → Tasks 4, 6, 7; ready gate and worktree-gone detection (including the startup capture) → Tasks 2, 3, 7; exit and kill → Tasks 6, 7; configuration → Task 1; rendering and chip precedence → Tasks 3, 8; failure-behavior table → covered by `teardownSendCmd`'s notes (Task 6), the timeouts (Task 7), the `selfPane == ""` guard (Task 7), and `worktreeIsGone`'s conservative default (Task 2); testing → each task's own tests; documentation → Task 8. Task 9 exists because the spec's riskiest claim — that injected keystrokes submit a slash command — cannot be covered by a unit test.

**Consistency.** `mappedTranscript` is four-valued from Task 5 onward and every later use matches. `teardownSendCmd` is reused verbatim for `/exit` rather than growing a near-duplicate `teardownExitCmd`; the spec named a separate command, and this plan collapses them because the two would have been identical — the interface block in Task 6 lists what actually gets written. Chip strings, timeout constants, and abort notes are identical between the Global Constraints, the implementation steps, and the assertions.
