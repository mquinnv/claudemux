# claudemux Rename + Zero-Dependency Install — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename the package to `claudemux` and make it installable with no Go toolchain via three channels (Homebrew, `curl | sh`, npm) that all consume one set of GitHub Release artifacts, with the Claude Code hook registered automatically on every channel.

**Architecture:** A GitHub Actions workflow cross-compiles `claudemux-head` on tag and publishes per-platform tarballs containing the Go binary and the three bash scripts **as siblings**. All three install channels unpack that same tarball. Hook registration is a new Go subcommand (`claudemux-head hook ensure`) rather than a `jq` script, because it read-modify-writes the user's live Claude Code config and must preserve unknown fields; it is invoked both by the installers and by the `claudemux` launcher at startup (where it is a silent no-op), which is the only way Homebrew users get a hook at all.

**Tech Stack:** Go 1.26, bash, POSIX sh, GitHub Actions, Homebrew formula (Ruby), npm.

## Prerequisite (do this FIRST — before Task 1)

The GitHub repo must be renamed **before** Task 4 pushes a tag, or the release lands under the old name and the Go module path (`github.com/mquinnv/claudemux`, set in Task 1) will not resolve.

```bash
gh repo rename claudemux -R mquinnv/claude-env       # GitHub redirects the old URL
git remote set-url origin git@github.com:mquinnv/claudemux.git
git remote -v
```

## Global Constraints

- Every name changes together. Old → new: repo/module `claude-env` → `claudemux`; Go dir `cmd/claude-head/` → `cmd/claudemux-head/`; launcher `bin/claude-env` → `bin/claudemux`; TUI binary `claude-head` → `claudemux-head`; config dir `~/.config/claude-env/` → `~/.config/claudemux/`; env var `CLAUDE_HEAD_ENV` → `CLAUDEMUX_ENV`; hook script `hooks/claude-head-map.sh` → `hooks/claudemux-map.sh`; pane-map dir `~/.claude/claude-head/panes` → `~/.claude/claudemux/panes`.
- **The hook script and `panemap.go` must agree on the pane-map dir.** If they diverge, `claudemux-head` silently falls back to most-recently-active-session detection — wrong whenever two sessions share a project, with no error message.
- **All four installed files must be siblings in one directory** on every channel: `claudemux-head`, `claudemux`, `project-color-resolve.sh`, `claudemux-map.sh`. `claudemux` finds the resolver via `dirname "$(readlink -f "$0")"`, and `claudemux-head` finds the hook script the same way.
- `settings.json` merging must preserve every unrelated key. The reference machine's file has **19 top-level keys** and pre-existing hooks on both target events.
- Config dir resolution stays hand-rolled. **Never `os.UserConfigDir()`** — it returns `~/Library/Application Support` on macOS.
- No Windows. The tool requires tmux and bash.
- Every Go task ends green: `go build ./... && go vet ./... && go test ./...`
- Every bash/sh task ends clean: `bash -n` (or `sh -n`) plus `shellcheck`.

---

### Task 1: Rename everything to claudemux

**Files:**
- Move: `cmd/claude-head/` → `cmd/claudemux-head/` (all `.go` files)
- Move: `bin/claude-env` → `bin/claudemux`
- Move: `hooks/claude-head-map.sh` → `hooks/claudemux-map.sh`
- Modify: `go.mod`, `cmd/claudemux-head/config.go`, `cmd/claudemux-head/env.go`, `cmd/claudemux-head/panemap.go`, `cmd/claudemux-head/main.go`, `cmd/claudemux-head/configget.go`, all `*_test.go`, `bin/claudemux`, `hooks/claudemux-map.sh`, `.gitignore`, `.project.yml.example`, `README.md`

**Interfaces:**
- Consumes: nothing.
- Produces: the renamed tree. Every later task assumes these names.

- [ ] **Step 1: Move the files with git mv**

```bash
cd "$(git rev-parse --show-toplevel)"
git mv cmd/claude-head cmd/claudemux-head
git mv bin/claude-env bin/claudemux
git mv hooks/claude-head-map.sh hooks/claudemux-map.sh
go mod edit -module github.com/mquinnv/claudemux
```

- [ ] **Step 2: Rewrite the identifiers**

Order matters: rewrite the longest/most specific strings first, or `claude-head` will partially match inside `claude-head-map.sh` and corrupt it.

```bash
FILES=$(git ls-files '*.go' '*.sh' '*.md' '*.yml' '*.example' 'bin/claudemux' '.gitignore')
sed -i '' \
  -e 's#claude-head-map\.sh#claudemux-map.sh#g' \
  -e 's#CLAUDE_HEAD_ENV#CLAUDEMUX_ENV#g' \
  -e 's#\.claude/claude-head/panes#.claude/claudemux/panes#g' \
  -e 's#"claude-head", "panes"#"claudemux", "panes"#g' \
  -e 's#github\.com/mquinnv/claude-env#github.com/mquinnv/claudemux#g' \
  -e 's#cmd/claude-head#cmd/claudemux-head#g' \
  -e 's#claude-head#claudemux-head#g' \
  -e 's#claude-env#claudemux#g' \
  $FILES
```

Then read the diff. Two known hazards to check by eye:

1. `claudemux-head-map.sh` — if any string became this, the ordering above failed. It must be `claudemux-map.sh`.
2. `~/.config/claudemux/` must appear, never `~/.config/claudemux-head/`.

- [ ] **Step 3: Fix `.gitignore`'s anchored build-artifact pattern**

The root build artifact is now `claudemux-head`, and the source dir is `cmd/claudemux-head/`. A bare pattern would ignore the source directory.

```gitignore
# Build output. Anchored with a leading slash on purpose: a bare pattern also
# matches the cmd/claudemux-head/ SOURCE directory, silently ignoring every file
# a contributor adds there.
/claudemux-head
/claudemux
```

Verify:

```bash
echo 'package main' > cmd/claudemux-head/zz.go
git check-ignore -v cmd/claudemux-head/zz.go && echo "BAD: source ignored" || echo "OK: source tracked"
rm cmd/claudemux-head/zz.go
```
Expected: `OK: source tracked`.

- [ ] **Step 4: Rename the usage string in `main.go`**

`main.go` prints a usage line naming the binary. It must say `claudemux-head`, not the old name:

```go
		fmt.Fprintln(os.Stderr, "usage: claudemux-head config get <dotted.path>")
```

- [ ] **Step 5: Run the rename gate**

```bash
git ls-files | xargs grep -rniE 'claude-head|claude-env|CLAUDE_HEAD_ENV' && echo "STALE NAMES REMAIN" || echo "CLEAN"
```
Expected: `CLEAN`. (`claudemux-head` contains neither substring, so any hit is a genuine miss.)

- [ ] **Step 6: Verify green**

```bash
go build ./... && go vet ./... && go test ./... && bash -n bin/claudemux && bash -n hooks/claudemux-map.sh && shellcheck bin/claudemux hooks/claudemux-map.sh
```
Expected: tests PASS. `shellcheck` may report SC2032/SC2033 on `bin/claudemux` (the local `attach` function vs `tmux attach`) — that is a known false positive; ignore it and fix nothing else.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "refactor!: rename package to claudemux"
```

---

### Task 2: `claudemux-head hook ensure`

**Files:**
- Create: `cmd/claudemux-head/hook.go`
- Create: `cmd/claudemux-head/hook_test.go`
- Modify: `cmd/claudemux-head/main.go` (dispatch `hook` alongside `config`)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `func runHookEnsure(args []string, stdout, stderr io.Writer) int` — process exit code.
  - `func hookScriptSource() (string, error)` — path to `claudemux-map.sh` shipped beside the running binary.
  - Installed hook lives at a **stable, channel-independent** path: `~/.claude/hooks/claudemux-map.sh`.

**Why Go and not `jq`:** this read-modify-writes the user's live Claude Code config. The reference machine's `settings.json` has 19 top-level keys (`permissions`, `model`, `statusLine`, `enabledPlugins`, …) and pre-existing hooks on both target events. A `jq` one-liner is exactly the tool that quietly drops unknown fields.

**Exit codes (contract with `install.sh` and the launcher):**

| Code | Meaning |
|---|---|
| 0 | Hook present already (no write), or installed successfully |
| 2 | Usage error |
| 3 | `settings.json` exists but does not parse — **nothing written** |
| 4 | I/O failure (cannot read/write/copy) |

- [ ] **Step 1: Write the failing tests**

Create `cmd/claudemux-head/hook_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeSettings puts contents at $HOME/.claude/settings.json and returns its path.
func writeSettings(t *testing.T, contents string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, ".claude", "settings.json")
	if contents != "" {
		if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", dir)
	return p
}

// stubScript creates a fake claudemux-map.sh for --script to point at.
func stubScript(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "claudemux-map.sh")
	if err := os.WriteFile(p, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("settings.json is not valid JSON after hook ensure: %v", err)
	}
	return m
}

// hookCommands returns every command string registered on event.
func hookCommands(t *testing.T, settings map[string]any, event string) []string {
	t.Helper()
	var out []string
	hooks, _ := settings["hooks"].(map[string]any)
	groups, _ := hooks[event].([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		inner, _ := gm["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if c, ok := hm["command"].(string); ok {
				out = append(out, c)
			}
		}
	}
	return out
}

func TestHookEnsureFreshInstall(t *testing.T) {
	p := writeSettings(t, "") // no settings.json at all
	script := stubScript(t)

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure([]string{"--script", script}, &stdout, &stderr); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0; stderr=%q", code, stderr.String())
	}

	s := readSettings(t, p)
	for _, ev := range []string{"SessionStart", "UserPromptSubmit"} {
		cmds := hookCommands(t, s, ev)
		if len(cmds) != 1 {
			t.Fatalf("%s: got %d commands, want 1: %v", ev, len(cmds), cmds)
		}
		if filepath.Base(cmds[0]) != "claudemux-map.sh" {
			t.Errorf("%s: command = %q, want it to end in claudemux-map.sh", ev, cmds[0])
		}
	}
}

// The hook script must be copied to a STABLE path, not referenced where it
// happened to be installed — a Homebrew libexec path would break on upgrade.
func TestHookEnsureCopiesScriptToStablePath(t *testing.T) {
	writeSettings(t, "")
	script := stubScript(t)

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure([]string{"--script", script}, &stdout, &stderr); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0", code)
	}

	home, _ := os.UserHomeDir()
	installed := filepath.Join(home, ".claude", "hooks", "claudemux-map.sh")
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatalf("hook script not copied to %s: %v", installed, err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed hook is not executable (mode %v) — Claude Code cannot run it", info.Mode().Perm())
	}
}

// Running twice must not duplicate the entries, and must not rewrite the file.
func TestHookEnsureIsIdempotent(t *testing.T) {
	p := writeSettings(t, "")
	script := stubScript(t)

	var b1, b2 bytes.Buffer
	runHookEnsure([]string{"--script", script}, &b1, &b1)
	first, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	if code := runHookEnsure([]string{"--script", script}, &b2, &b2); code != 0 {
		t.Fatalf("second runHookEnsure() = %d, want 0", code)
	}
	second, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(first, second) {
		t.Error("settings.json changed on the second run — hook ensure must be a no-op when already present")
	}
	s := readSettings(t, p)
	if got := len(hookCommands(t, s, "SessionStart")); got != 1 {
		t.Errorf("SessionStart has %d commands after two runs, want 1 — the entry was duplicated", got)
	}
}

// THE most important test: a real user's settings.json is full of unrelated
// config and other people's hooks. None of it may be lost.
func TestHookEnsurePreservesUnrelatedKeysAndHooks(t *testing.T) {
	orig := `{
  "model": "opus",
  "permissions": {"allow": ["Bash(ls:*)"]},
  "theme": "dark",
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "/other/tool.sh"}]}
    ],
    "PreToolUse": [
      {"hooks": [{"type": "command", "command": "/unrelated/event.sh"}]}
    ]
  }
}`
	p := writeSettings(t, orig)
	script := stubScript(t)

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure([]string{"--script", script}, &stdout, &stderr); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0; stderr=%q", code, stderr.String())
	}

	s := readSettings(t, p)

	if s["model"] != "opus" {
		t.Errorf("model = %v, want \"opus\" — an unrelated top-level key was lost", s["model"])
	}
	if s["theme"] != "dark" {
		t.Errorf("theme = %v, want \"dark\" — an unrelated top-level key was lost", s["theme"])
	}
	if _, ok := s["permissions"]; !ok {
		t.Error("permissions key was dropped — this is the user's live security config")
	}

	// Another tool's SessionStart hook must survive alongside ours.
	cmds := hookCommands(t, s, "SessionStart")
	var sawOther, sawOurs bool
	for _, c := range cmds {
		if c == "/other/tool.sh" {
			sawOther = true
		}
		if filepath.Base(c) == "claudemux-map.sh" {
			sawOurs = true
		}
	}
	if !sawOther {
		t.Error("another tool's SessionStart hook was clobbered")
	}
	if !sawOurs {
		t.Error("our hook was not registered on SessionStart")
	}

	// An unrelated EVENT must survive untouched.
	if got := hookCommands(t, s, "PreToolUse"); len(got) != 1 || got[0] != "/unrelated/event.sh" {
		t.Errorf("PreToolUse = %v, want the unrelated hook preserved", got)
	}
}

// A settings.json we cannot parse must be left ALONE. Replacing a corrupt but
// hand-recoverable config with our own is the worst possible outcome.
func TestHookEnsureRefusesMalformedSettings(t *testing.T) {
	orig := `{"model": "opus", TRUNCATED`
	p := writeSettings(t, orig)
	script := stubScript(t)

	var stdout, stderr bytes.Buffer
	code := runHookEnsure([]string{"--script", script}, &stdout, &stderr)

	if code != 3 {
		t.Errorf("runHookEnsure() = %d, want 3 for a malformed settings.json", code)
	}
	if stderr.String() == "" {
		t.Error("stderr empty, want the parse error reported")
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != orig {
		t.Error("settings.json was modified despite being unparseable — it must be left exactly as found")
	}
}

// A backup must exist before we overwrite a file that had content.
func TestHookEnsureBacksUpBeforeWriting(t *testing.T) {
	p := writeSettings(t, `{"model": "opus"}`)
	script := stubScript(t)

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure([]string{"--script", script}, &stdout, &stderr); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0", code)
	}

	matches, err := filepath.Glob(p + ".bak-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("found %d backups, want exactly 1", len(matches))
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"model": "opus"}` {
		t.Errorf("backup = %q, want the ORIGINAL contents", string(b))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestHookEnsure -v`
Expected: FAIL — `undefined: runHookEnsure`.

- [ ] **Step 3: Write the implementation**

Create `cmd/claudemux-head/hook.go`:

```go
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// hookScriptName is the hook's filename, identical wherever it is installed.
const hookScriptName = "claudemux-map.sh"

// hookEvents are the two Claude Code events the pane map depends on.
// SessionStart records the mapping when a pane first opens; UserPromptSubmit
// keeps it current across /clear, resume, and compaction, which rotate the
// transcript file underneath a live session. Registering only one leaves the
// map stale in exactly the cases users notice.
var hookEvents = []string{"SessionStart", "UserPromptSubmit"}

// hookScriptSource finds the claudemux-map.sh that shipped with this binary:
// every install channel lays the binary and the scripts down as siblings.
// Symlinks are resolved first because Homebrew puts the real files in libexec
// and symlinks only the binaries onto PATH.
func hookScriptSource() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(resolved), hookScriptName), nil
}

// runHookEnsure implements `claudemux-head hook ensure`.
//
// Exit codes are a contract with install.sh and with bin/claudemux, which calls
// this on every launch:
//
//	0 — already present (no write), or installed
//	2 — usage error
//	3 — settings.json exists but does not parse; NOTHING is written
//	4 — I/O failure
func runHookEnsure(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hook ensure", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scriptFlag := fs.String("script", "", "path to claudemux-map.sh (defaults to the copy beside this binary)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	src := *scriptFlag
	if src == "" {
		var err error
		if src, err = hookScriptSource(); err != nil {
			fmt.Fprintf(stderr, "claudemux: locating %s: %v\n", hookScriptName, err)
			return 4
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "claudemux: resolving home dir: %v\n", err)
		return 4
	}
	claudeDir := filepath.Join(home, ".claude")
	hooksDir := filepath.Join(claudeDir, "hooks")
	settingsPath := filepath.Join(claudeDir, "settings.json")

	// The registered command points HERE, not at wherever the package was
	// installed: a Homebrew libexec path would be baked into settings.json and
	// break on the next upgrade.
	dst := filepath.Join(hooksDir, hookScriptName)

	// Read settings BEFORE copying anything, so a malformed file leaves the
	// whole operation a no-op rather than a half-done one.
	settings := map[string]any{}
	existing, err := os.ReadFile(settingsPath)
	switch {
	case err == nil:
		if err := json.Unmarshal(existing, &settings); err != nil {
			fmt.Fprintf(stderr, "claudemux: %s does not parse (%v); refusing to touch it\n", settingsPath, err)
			return 3
		}
	case errors.Is(err, os.ErrNotExist):
		existing = nil
	default:
		fmt.Fprintf(stderr, "claudemux: reading %s: %v\n", settingsPath, err)
		return 4
	}

	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "claudemux: creating %s: %v\n", hooksDir, err)
		return 4
	}
	if err := copyExecutable(src, dst); err != nil {
		fmt.Fprintf(stderr, "claudemux: installing hook script: %v\n", err)
		return 4
	}

	if !addHookEntries(settings, dst) {
		return 0 // already registered on both events: no write, stay silent
	}

	if existing != nil {
		backup := settingsPath + ".bak-" + strconv.FormatInt(time.Now().Unix(), 10)
		if err := os.WriteFile(backup, existing, 0o644); err != nil {
			fmt.Fprintf(stderr, "claudemux: writing backup %s: %v\n", backup, err)
			return 4
		}
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "claudemux: encoding settings: %v\n", err)
		return 4
	}
	out = append(out, '\n')
	if err := os.WriteFile(settingsPath, out, 0o644); err != nil {
		fmt.Fprintf(stderr, "claudemux: writing %s: %v\n", settingsPath, err)
		return 4
	}

	fmt.Fprintf(stdout, "claudemux: registered the pane-map hook in %s\n", settingsPath)
	return 0
}

// addHookEntries adds our command to any hookEvents that lack it, mutating
// settings in place. Reports whether anything changed.
//
// It walks the generic map rather than a typed struct so that every key we do
// not model — the user's permissions, model, statusLine, other tools' hooks —
// round-trips untouched.
func addHookEntries(settings map[string]any, command string) bool {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	changed := false
	for _, event := range hookEvents {
		groups, _ := hooks[event].([]any)
		if hasHookCommand(groups, command) {
			continue
		}
		// Append; never replace. Another tool's hook on this event must survive.
		groups = append(groups, map[string]any{
			"hooks": []any{
				map[string]any{"type": "command", "command": command},
			},
		})
		hooks[event] = groups
		changed = true
	}

	if changed {
		settings["hooks"] = hooks
	}
	return changed
}

func hasHookCommand(groups []any, command string) bool {
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		inner, _ := gm["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if c, _ := hm["command"].(string); c == command {
				return true
			}
		}
	}
	return false
}

func copyExecutable(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o755)
}
```

- [ ] **Step 4: Dispatch the subcommand in `main.go`**

Add beside the existing `config` dispatch, before `flag.Parse()`:

```go
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		if len(os.Args) > 2 && os.Args[2] == "ensure" {
			os.Exit(runHookEnsure(os.Args[3:], os.Stdout, os.Stderr))
		}
		fmt.Fprintln(os.Stderr, "usage: claudemux-head hook ensure [--script <path>]")
		os.Exit(2)
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./... -run TestHookEnsure -v`
Expected: PASS (6 tests).

- [ ] **Step 6: Verify green and commit**

```bash
go build ./... && go vet ./... && go test ./...
git add cmd/claudemux-head/hook.go cmd/claudemux-head/hook_test.go cmd/claudemux-head/main.go
git commit -m "feat(hook): add 'hook ensure' to register the pane-map hook safely"
```

---

### Task 3: The launcher ensures the hook at startup

**Files:**
- Modify: `bin/claudemux`

**Interfaces:**
- Consumes: `claudemux-head hook ensure` (Task 2), exit codes 0/2/3/4.
- Produces: nothing new.

**Why:** Homebrew formulas must not write to `~/.claude`, so a brew user would otherwise never get a hook — and would silently get wrong pane binding with no error. This startup check is the only mechanism that makes "no manual hook entries" true on all three channels.

- [ ] **Step 1: Add the call**

In `bin/claudemux`, immediately after the option parsing (`shift $((OPTIND - 1))`), add:

```bash
# Register the Claude Code pane-map hook if it is not already there.
#
# This is a no-op read when the hook is present, which is the common case. It
# lives here rather than only in the installers because Homebrew formulas must
# not write to ~/.claude — without this, every brew user would silently run with
# no hook and get wrong pane binding.
#
# Never fatal: a missing or unhappy hook costs accurate pane binding, not the
# session. Exit 3 (a settings.json that does not parse) is the one case worth
# surfacing, because it means we deliberately touched nothing.
ensure_hook() {
  command -v claudemux-head >/dev/null 2>&1 || return 0
  local rc=0
  claudemux-head hook ensure >/dev/null 2>/tmp/claudemux-hook.$$ || rc=$?
  if [ "$rc" -eq 3 ]; then
    cat /tmp/claudemux-hook.$$ >&2
  fi
  rm -f /tmp/claudemux-hook.$$
  return 0
}
ensure_hook
```

- [ ] **Step 2: Verify it is a no-op when already registered**

```bash
go build -o /tmp/cmbin/claudemux-head ./cmd/claudemux-head
cp bin/claudemux bin/project-color-resolve.sh hooks/claudemux-map.sh /tmp/cmbin/
export PATH="/tmp/cmbin:$PATH"
H=$(mktemp -d); HOME="$H" claudemux-head hook ensure   # first: installs
BEFORE=$(md5 -q "$H/.claude/settings.json")
HOME="$H" claudemux-head hook ensure                    # second: must be silent no-op
AFTER=$(md5 -q "$H/.claude/settings.json")
[ "$BEFORE" = "$AFTER" ] && echo "IDEMPOTENT" || echo "BAD: file changed on second run"
ls "$H/.claude/settings.json".bak-* 2>/dev/null && echo "BAD: backup written on no-op" || echo "no spurious backup"
```
Expected: `IDEMPOTENT`, `no spurious backup`.

- [ ] **Step 3: Verify a malformed settings.json is surfaced, not swallowed**

```bash
H=$(mktemp -d); mkdir -p "$H/.claude"; printf '{"model": TRUNCATED' > "$H/.claude/settings.json"
HOME="$H" claudemux-head hook ensure; echo "exit=$?"
cat "$H/.claude/settings.json"
```
Expected: exit `3`, a parse error on stderr, and the file **unchanged**.

- [ ] **Step 4: Lint and commit**

```bash
bash -n bin/claudemux && shellcheck bin/claudemux
git add bin/claudemux
git commit -m "feat(claudemux): ensure the pane-map hook at launch"
```

---

### Task 4: Release workflow — cross-compiled tarballs

**Files:**
- Create: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: nothing.
- Produces: on tag `v*`, a GitHub Release carrying `claudemux_<version>_<os>_<arch>.tar.gz` for `darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`, plus `SHA256SUMS`. Tasks 5–7 all consume these.

**Tarball layout — all four files at the archive root, as siblings:**

```
claudemux-head
claudemux
project-color-resolve.sh
claudemux-map.sh
LICENSE
README.md
```

- [ ] **Step 1: Write the workflow**

Create `.github/workflows/release.yml`:

```yaml
name: release

on:
  push:
    tags: ['v*']

permissions:
  contents: write

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.26'

      - name: Build every platform
        env:
          CGO_ENABLED: '0'
        run: |
          set -euo pipefail
          version="${GITHUB_REF_NAME#v}"
          mkdir -p dist

          # No Windows: the tool requires tmux and bash.
          for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
            os="${target%/*}"; arch="${target#*/}"
            stage="$(mktemp -d)"

            GOOS="$os" GOARCH="$arch" go build -trimpath \
              -ldflags "-s -w -X main.version=${version}" \
              -o "$stage/claudemux-head" ./cmd/claudemux-head

            # All four files land as SIBLINGS: claudemux finds the colour
            # resolver, and claudemux-head finds the hook script, by looking
            # next to themselves.
            cp bin/claudemux bin/project-color-resolve.sh "$stage/"
            cp hooks/claudemux-map.sh "$stage/"
            cp LICENSE README.md "$stage/"
            chmod +x "$stage/claudemux" "$stage/project-color-resolve.sh" "$stage/claudemux-map.sh"

            tar -czf "dist/claudemux_${version}_${os}_${arch}.tar.gz" -C "$stage" .
          done

          cd dist && shasum -a 256 ./*.tar.gz > SHA256SUMS
          cat SHA256SUMS

      - name: Publish the release
        env:
          GH_TOKEN: ${{ github.token }}
        run: gh release create "$GITHUB_REF_NAME" dist/* --generate-notes
```

- [ ] **Step 2: Add the `version` variable the ldflags set**

`-X main.version` requires the symbol to exist. In `cmd/claudemux-head/main.go`, above `func main()`:

```go
// version is stamped by the release workflow's -ldflags. "dev" for local builds.
var version = "dev"
```

And handle `claudemux-head version` in the subcommand dispatch, before `flag.Parse()`:

```go
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		os.Exit(0)
	}
```

- [ ] **Step 3: Verify the build matrix locally before tagging**

Do not discover a cross-compile failure inside CI:

```bash
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  os="${target%/*}"; arch="${target#*/}"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -o "/tmp/cm-$os-$arch" ./cmd/claudemux-head && echo "  OK $target" || echo "  FAIL $target"
done
```
Expected: four `OK` lines.

- [ ] **Step 4: Verify the version stamp works**

```bash
go build -ldflags "-X main.version=9.9.9" -o /tmp/cmv ./cmd/claudemux-head && /tmp/cmv version
```
Expected: `9.9.9`.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/release.yml cmd/claudemux-head/main.go
git commit -m "ci: build and publish per-platform release tarballs on tag"
```

- [ ] **Step 6: Tag and cut the first release**

The remaining tasks need real published artifacts to test against.

```bash
git push -u origin HEAD
git tag v0.1.0 && git push origin v0.1.0
gh run watch                      # wait for the workflow
gh release view v0.1.0 --json assets --jq '.assets[].name'
```
Expected: four `.tar.gz` assets plus `SHA256SUMS`.

---

### Task 5: `install.sh`

**Files:**
- Create: `install.sh`

**Interfaces:**
- Consumes: the release assets from Task 4; `claudemux-head hook ensure` from Task 2.
- Produces: an installed, hooked-up claudemux at `$CLAUDEMUX_PREFIX` (default `~/.local/bin`).

- [ ] **Step 1: Write the installer**

Create `install.sh` (POSIX `sh`, not bash — it runs before we control anything):

```sh
#!/bin/sh
# claudemux installer.
#   curl -fsSL https://raw.githubusercontent.com/mquinnv/claudemux/main/install.sh | sh
#
# Env:
#   CLAUDEMUX_PREFIX   install dir (default ~/.local/bin)
#   CLAUDEMUX_VERSION  version to install (default: latest release)
set -eu

REPO="mquinnv/claudemux"
PREFIX="${CLAUDEMUX_PREFIX:-$HOME/.local/bin}"

die() { echo "claudemux: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"; }

need curl
need tar
need uname

os="$(uname -s)"
case "$os" in
  Darwin) os=darwin ;;
  Linux)  os=linux ;;
  *)      die "unsupported OS: $os (claudemux needs tmux and bash; Windows is not supported)" ;;
esac

arch="$(uname -m)"
case "$arch" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|amd64)  arch=amd64 ;;
  *)             die "unsupported architecture: $arch" ;;
esac

version="${CLAUDEMUX_VERSION:-}"
if [ -z "$version" ]; then
  version="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
  [ -n "$version" ] || die "could not determine the latest release"
fi
v="${version#v}"

tarball="claudemux_${v}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$version"

tmp="$(mktemp -d)"
# shellcheck disable=SC2064  # $tmp must expand NOW, not at trap time
trap "rm -rf '$tmp'" EXIT INT TERM

echo "claudemux: downloading $tarball ($version)"
curl -fsSL "$base/$tarball"    -o "$tmp/$tarball" || die "download failed: $base/$tarball"
curl -fsSL "$base/SHA256SUMS"  -o "$tmp/SHA256SUMS" || die "could not fetch SHA256SUMS"

# Verify before unpacking. An installer piped from the network into a shell that
# skips this is a supply-chain hole.
expected="$(sed -n "s#^\([a-f0-9]\{64\}\)  \./${tarball}\$#\1#p" "$tmp/SHA256SUMS")"
[ -n "$expected" ] || die "$tarball is not listed in SHA256SUMS"

if command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "$tmp/$tarball" | cut -d' ' -f1)"
elif command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$tarball" | cut -d' ' -f1)"
else
  die "need shasum or sha256sum to verify the download"
fi

[ "$actual" = "$expected" ] || die "checksum MISMATCH for $tarball
  expected $expected
  actual   $actual
Refusing to install."

echo "claudemux: checksum OK"

mkdir -p "$PREFIX"
tar -xzf "$tmp/$tarball" -C "$tmp"

# All four files stay siblings: claudemux resolves project-color-resolve.sh, and
# claudemux-head resolves claudemux-map.sh, by looking next to themselves.
for f in claudemux-head claudemux project-color-resolve.sh claudemux-map.sh; do
  install -m 0755 "$tmp/$f" "$PREFIX/$f"
done

echo "claudemux: installed to $PREFIX"

# Register the Claude Code hook so nobody hand-edits settings.json.
"$PREFIX/claudemux-head" hook ensure || echo "claudemux: could not register the hook automatically" >&2

case ":$PATH:" in
  *":$PREFIX:"*) ;;
  *) echo "claudemux: WARNING — $PREFIX is not on your PATH. Add it:"
     echo "    export PATH=\"$PREFIX:\$PATH\"" ;;
esac

echo "claudemux: done. Run: claudemux <project-dir>"
```

- [ ] **Step 2: Lint**

```bash
sh -n install.sh && shellcheck -s sh install.sh
```
Expected: clean.

- [ ] **Step 3: Install for real into a scratch HOME**

```bash
S=$(mktemp -d)
env HOME="$S" CLAUDEMUX_PREFIX="$S/bin" sh install.sh
ls "$S/bin"
```
Expected: all four files present in `$S/bin`.

- [ ] **Step 4: Verify the hook registered and the siblings resolve**

```bash
python3 -c "
import json;d=json.load(open('$S/.claude/settings.json'))
for e in ('SessionStart','UserPromptSubmit'):
    cmds=[h['command'] for g in d['hooks'][e] for h in g['hooks']]
    print(e, cmds)
"
"$S/bin/claudemux-head" version
```
Expected: both events list a `claudemux-map.sh` command; version prints.

- [ ] **Step 5: Verify a bad checksum ABORTS the install**

This is the security property the installer exists to have. Prove it, and prove it in a way that cannot pass for the wrong reason.

Stub `shasum` so the *computed* hash never matches the published one. This exercises exactly the comparison branch, deterministically:

```bash
S2=$(mktemp -d); FAKE=$(mktemp -d)
cat > "$FAKE/shasum" <<'EOF'
#!/bin/sh
# Always report a hash that cannot match SHA256SUMS.
echo "0000000000000000000000000000000000000000000000000000000000000000  -"
EOF
chmod +x "$FAKE/shasum"

if env PATH="$FAKE:$PATH" HOME="$S2" CLAUDEMUX_PREFIX="$S2/bin" sh install.sh 2>&1 | tee /tmp/badsum.log; then
  echo "BAD: install SUCCEEDED despite a checksum mismatch"
else
  grep -q "checksum MISMATCH" /tmp/badsum.log \
    && echo "GOOD: aborted on checksum mismatch" \
    || echo "BAD: aborted, but NOT because of the checksum — check why"
fi
ls "$S2/bin" 2>/dev/null && echo "BAD: files were installed anyway" || echo "GOOD: nothing installed"
```

Expected: `GOOD: aborted on checksum mismatch` **and** `GOOD: nothing installed`.

If it prints `BAD` in either place, verification is not wired up — stop and fix it before going further. Note the `sha256sum` branch needs the same treatment on Linux; stub whichever one `install.sh` picks on the host you test on.

- [ ] **Step 6: Commit**

```bash
git add install.sh
git commit -m "feat(install): add checksum-verifying curl|sh installer"
```

---

### Task 6: Homebrew formula

**Files:**
- Create: `Formula/claudemux.rb` in the **separate** repo `mquinnv/homebrew-tap` (which already contains `Formula/warpclip.rb`)

**Interfaces:**
- Consumes: the `darwin`/`linux` tarballs and `SHA256SUMS` from Task 4.
- Produces: `brew install mquinnv/tap/claudemux`.

**Why this channel matters:** it is the only one that can *declare* `tmux`, `jq`, and `git` as real dependencies rather than documenting them.

- [ ] **Step 1: Collect the release checksums**

```bash
gh release download v0.1.0 -R mquinnv/claudemux -p SHA256SUMS -O - | sed 's#\./##'
```
Record the four sums; the formula needs them verbatim.

- [ ] **Step 2: Write the formula**

Clone the tap and create `Formula/claudemux.rb`. Substitute the real version and the four sums from Step 1:

```ruby
class Claudemux < Formula
  desc "tmux workspace for Claude Code sessions, with a live status pane"
  homepage "https://github.com/mquinnv/claudemux"
  version "0.1.0"
  license "MIT"

  depends_on "git"
  depends_on "jq"
  depends_on "tmux"

  on_macos do
    on_arm do
      url "https://github.com/mquinnv/claudemux/releases/download/v0.1.0/claudemux_0.1.0_darwin_arm64.tar.gz"
      sha256 "PUT_DARWIN_ARM64_SUM_HERE"
    end
    on_intel do
      url "https://github.com/mquinnv/claudemux/releases/download/v0.1.0/claudemux_0.1.0_darwin_amd64.tar.gz"
      sha256 "PUT_DARWIN_AMD64_SUM_HERE"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/mquinnv/claudemux/releases/download/v0.1.0/claudemux_0.1.0_linux_arm64.tar.gz"
      sha256 "PUT_LINUX_ARM64_SUM_HERE"
    end
    on_intel do
      url "https://github.com/mquinnv/claudemux/releases/download/v0.1.0/claudemux_0.1.0_linux_amd64.tar.gz"
      sha256 "PUT_LINUX_AMD64_SUM_HERE"
    end
  end

  def install
    # All four files must stay SIBLINGS: claudemux locates
    # project-color-resolve.sh, and claudemux-head locates claudemux-map.sh, by
    # looking next to their own resolved path. Keep the real files together in
    # libexec and put only symlinks on PATH.
    libexec.install "claudemux-head", "claudemux",
                    "project-color-resolve.sh", "claudemux-map.sh"
    bin.install_symlink libexec/"claudemux-head"
    bin.install_symlink libexec/"claudemux"
  end

  def caveats
    <<~EOS
      claudemux registers its Claude Code pane-map hook automatically the first
      time you run it. No manual edits to ~/.claude/settings.json are needed.

      Get started:
        claudemux ~/path/to/project
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/claudemux-head version")
  end
end
```

- [ ] **Step 3: Install it for real and prove the siblings survived**

The symlink-into-`libexec` layout is the one thing that can silently break here.

```bash
brew install --formula ./Formula/claudemux.rb
claudemux-head version
# claudemux-head must resolve its own symlink back to libexec and find the hook
# script beside it:
H=$(mktemp -d); HOME="$H" claudemux-head hook ensure && echo "HOOK OK"
ls "$(brew --prefix claudemux)/libexec"
```
Expected: version prints, `HOOK OK`, and `libexec` lists all four files.

- [ ] **Step 4: Run brew's own audit**

```bash
brew audit --strict --formula ./Formula/claudemux.rb || true
brew test claudemux
```
Fix any **error**. Style nits from `--strict` on a tap are advisory.

- [ ] **Step 5: Commit to the tap repo**

```bash
git add Formula/claudemux.rb
git commit -m "claudemux 0.1.0"
git push
```

- [ ] **Step 6: Verify the published tap end-to-end**

```bash
brew uninstall claudemux
brew install mquinnv/tap/claudemux
claudemux-head version
```
Expected: installs from the tap and prints the version.

---

### Task 7: npm package

**Files:**
- Create: `npm/package.json`
- Create: `npm/install.js`
- Create: `npm/README.md`

**Interfaces:**
- Consumes: the release tarballs from Task 4.
- Produces: `npx claudemux` and `npm i -g claudemux`.

- [ ] **Step 1: Write `npm/package.json`**

```json
{
  "name": "claudemux",
  "version": "0.1.0",
  "description": "tmux workspace for Claude Code sessions, with a live status pane",
  "homepage": "https://github.com/mquinnv/claudemux",
  "license": "MIT",
  "repository": { "type": "git", "url": "https://github.com/mquinnv/claudemux.git" },
  "os": ["darwin", "linux"],
  "cpu": ["x64", "arm64"],
  "bin": {
    "claudemux": "vendor/claudemux",
    "claudemux-head": "vendor/claudemux-head"
  },
  "scripts": {
    "postinstall": "node install.js"
  },
  "files": ["install.js", "vendor/.keep", "README.md"]
}
```

- [ ] **Step 2: Write `npm/install.js`**

It downloads the same tarball the other channels use, into `vendor/`, keeping all four files siblings.

```js
#!/usr/bin/env node
// Fetch the claudemux release tarball for this platform into vendor/.
// The four files must land as SIBLINGS: claudemux resolves
// project-color-resolve.sh, and claudemux-head resolves claudemux-map.sh, by
// looking next to their own path.
const { execFileSync } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");
const os = require("node:os");

const REPO = "mquinnv/claudemux";
const version = require("./package.json").version;

const platform = { darwin: "darwin", linux: "linux" }[process.platform];
const arch = { x64: "amd64", arm64: "arm64" }[process.arch];
if (!platform || !arch) {
  console.error(
    `claudemux: unsupported platform ${process.platform}/${process.arch}. ` +
      `It needs tmux and bash; Windows is not supported.`,
  );
  process.exit(1);
}

const tarball = `claudemux_${version}_${platform}_${arch}.tar.gz`;
const url = `https://github.com/${REPO}/releases/download/v${version}/${tarball}`;
const vendor = path.join(__dirname, "vendor");
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "claudemux-"));

try {
  fs.mkdirSync(vendor, { recursive: true });
  execFileSync("curl", ["-fsSL", url, "-o", path.join(tmp, tarball)], { stdio: "inherit" });
  execFileSync("tar", ["-xzf", path.join(tmp, tarball), "-C", vendor], { stdio: "inherit" });
  for (const f of ["claudemux-head", "claudemux", "project-color-resolve.sh", "claudemux-map.sh"]) {
    fs.chmodSync(path.join(vendor, f), 0o755);
  }
  // Register the Claude Code hook so nobody hand-edits settings.json.
  execFileSync(path.join(vendor, "claudemux-head"), ["hook", "ensure"], { stdio: "inherit" });
} catch (err) {
  console.error(`claudemux: install failed: ${err.message}`);
  console.error(`claudemux: you can install manually from https://github.com/${REPO}`);
  process.exit(1);
} finally {
  fs.rmSync(tmp, { recursive: true, force: true });
}
```

- [ ] **Step 3: Create the vendor placeholder**

npm will not ship an empty directory.

```bash
mkdir -p npm/vendor && touch npm/vendor/.keep
printf 'vendor/*\n!vendor/.keep\n' > npm/.npmignore
```

- [ ] **Step 4: Test the package without publishing**

```bash
cd npm
npm pack                                   # -> claudemux-0.1.0.tgz
T=$(mktemp -d); cd "$T"
npm init -y >/dev/null
npm install /path/to/npm/claudemux-0.1.0.tgz
./node_modules/.bin/claudemux-head version
ls node_modules/claudemux/vendor
```
Expected: version prints; `vendor/` lists all four files as siblings.

- [ ] **Step 5: Commit**

```bash
git add npm/
git commit -m "feat(npm): publish claudemux via npm with a postinstall fetch"
```

- [ ] **Step 6: Publish (requires interactive `npm login` — ask Michael)**

`npm publish` needs an authenticated session this agent cannot create. Surface this rather than attempting it:

```bash
npm whoami || echo "Run: npm login   (interactive; Michael must do this)"
# then, once logged in:
cd npm && npm publish --access public
```

---

### Task 8: README rewrite

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: everything above.
- Produces: the published documentation.

- [ ] **Step 1: Delete the manual hook section entirely**

Remove the `~/.claude/settings.json` JSON block and the "Register it on both SessionStart and UserPromptSubmit" instructions. They are obsolete: every channel now registers the hook, and `claudemux` re-checks at launch.

Replace with a short section explaining **what** the hook does and that it is automatic:

```markdown
## The pane-map hook

`claudemux-head` follows the transcript of the `claude` process in its *sibling*
pane. A small Claude Code hook records which session lives in which tmux pane so
it can do that.

**You do not need to install or configure this.** Every install channel registers
it, and `claudemux` re-checks at launch, so it is repaired automatically if it
goes missing. It is written to `~/.claude/settings.json` — existing hooks and
settings are preserved, and a backup is taken before any change.

Without the hook, `claudemux-head` falls back to picking whichever transcript in
the project directory changed most recently. That is wrong as soon as you have two
Claude Code sessions open on the same project.
```

- [ ] **Step 2: Rewrite Install with the three channels, brew first**

```markdown
## Install

**Homebrew** (recommended — it installs `tmux`, `jq`, and `git` for you):

```bash
brew install mquinnv/tap/claudemux
```

**Shell** (no dependencies beyond `curl` and `tar`):

```bash
curl -fsSL https://raw.githubusercontent.com/mquinnv/claudemux/main/install.sh | sh
```

Installs to `~/.local/bin` (override with `CLAUDEMUX_PREFIX`). Verifies the
release checksum before installing.

**npm:**

```bash
npx claudemux ~/path/to/project     # or: npm i -g claudemux
```

**From source** (needs Go):

```bash
go install github.com/mquinnv/claudemux/cmd/claudemux-head@latest
```
Note this installs only the TUI. Clone the repo for the `claudemux` launcher, and
keep `claudemux` and `project-color-resolve.sh` in the same directory.
```

- [ ] **Step 3: Update the dependency table**

Keep it, but note Homebrew enforces it. Rename every command in it (`claude-env` → `claudemux`, `claude-head` → `claudemux-head`).

- [ ] **Step 4: Rename every remaining path and command**

`~/.config/claude-env/` → `~/.config/claudemux/`; `CLAUDE_HEAD_ENV` → `CLAUDEMUX_ENV`; the title and intro to `claudemux`.

- [ ] **Step 5: Execute every command the README now contains**

A README whose first command fails is worse than none. In a scratch `HOME`, run the `curl | sh` line (against the real release), the config snippets, and `claudemux-head version`. Fix the README where it is wrong.

```bash
S=$(mktemp -d)
env HOME="$S" CLAUDEMUX_PREFIX="$S/bin" sh -c 'curl -fsSL https://raw.githubusercontent.com/mquinnv/claudemux/main/install.sh | sh'
"$S/bin/claudemux-head" version
```
Expected: installs and prints the version.

- [ ] **Step 6: Rename gate + commit**

```bash
git ls-files | xargs grep -rniE 'claude-head|claude-env|CLAUDE_HEAD_ENV' && echo "STALE" || echo "CLEAN"
git add README.md
git commit -m "docs: rewrite install for brew/curl/npm, drop the manual hook step"
```

---

### Task 9: Migrate Michael's machine

**Files:** none in the repo. This mutates the reference machine.

**Interfaces:**
- Consumes: a published release (Task 4) and `hook ensure` (Task 2).
- Produces: a working claudemux install where claude-env used to be.

**Why it is a task:** the rename moved the config dir, the binary names, and the hook out from under a live, working setup. Every one of these is a silent breakage if missed.

- [ ] **Step 1: Move the config, and the 1Password FIFO mount**

```bash
mkdir -p ~/.config/claudemux
cp ~/.config/claude-env/config.yml ~/.config/claudemux/config.yml
```

The env file at `~/.config/claude-env/env` is a **1Password-mounted FIFO**, not a regular file — it cannot be copied. Two options; the first is cleaner:

1. Re-point the 1Password Environment mount at `~/.config/claudemux/env`.
2. Or leave the mount where it is and point config at it:
   ```yaml
   summary:
     api_key_file: ~/.config/claude-env/env
   ```

Verify whichever you choose:

```bash
P=$(claudemux-head config get summary.api_key_file); echo "$P"
[ -p "$P" ] && echo "FIFO OK" || echo "NOT A FIFO — the summarizer will get no key"
```

- [ ] **Step 2: Replace the launcher symlink and the stale binary**

```bash
rm -f ~/.local/bin/claude-env ~/go/bin/claude-head
ln -sf "$(git rev-parse --show-toplevel)/bin/claudemux" ~/.local/bin/claudemux
go install github.com/mquinnv/claudemux/cmd/claudemux-head@latest
which claudemux claudemux-head
```

- [ ] **Step 3: Update the fish function**

`~/.config/fish/functions/claude-env.fish` wraps the launcher so the terminal tab dies with the session. Recreate it as `claudemux.fish` and delete the old one:

```bash
cat > ~/.config/fish/functions/claudemux.fish <<'EOF'
function claudemux --wraps claudemux --description 'exec the claudemux tmux launcher so the tab dies with the session'
    # `exec` replaces this interactive fish with the launcher (which in turn
    # execs `tmux attach`). With no surviving parent shell, :kill-session closes
    # the terminal tab on its own.
    exec command claudemux $argv
end
EOF
rm -f ~/.config/fish/functions/claude-env.fish
```

- [ ] **Step 4: Swap the hook, and remove the OLD one**

`hook ensure` adds the new entry but will **not** remove the old `claude-head-map.sh` entries — left alone, the old hook keeps running and writing a pane map nobody reads.

```bash
claudemux-head hook ensure
python3 - <<'EOF'
import json, pathlib, time
p = pathlib.Path.home() / ".claude" / "settings.json"
d = json.loads(p.read_text())
(p.parent / f"settings.json.bak-premigrate-{int(time.time())}").write_text(json.dumps(d, indent=2))
for ev, groups in d.get("hooks", {}).items():
    for g in groups:
        g["hooks"] = [h for h in g.get("hooks", []) if "claude-head-map.sh" not in h.get("command", "")]
    d["hooks"][ev] = [g for g in groups if g.get("hooks")]
p.write_text(json.dumps(d, indent=2) + "\n")
print("removed claude-head-map.sh entries")
EOF
rm -f ~/.claude/hooks/claude-head-map.sh
```

Verify only the new hook remains:

```bash
python3 -c "
import json,pathlib
d=json.loads((pathlib.Path.home()/'.claude'/'settings.json').read_text())
for e in ('SessionStart','UserPromptSubmit'):
    print(e, [h['command'] for g in d['hooks'][e] for h in g['hooks']])
"
```
Expected: each event lists `claudemux-map.sh` and **no** `claude-head-map.sh`.

- [ ] **Step 5: Prove it live — this is the only test that matters**

Tests cannot reach the FIFO-contention path that caused a real incident. Launch a pane and confirm the summary lines populate:

```bash
claudemux ~/Projects/claudemux
```
Expected: the head pane renders, and within ~20s the topic/now summary lines populate (not the raw-prompt fallback). If they stay on the fallback, the summarizer got no key — re-check Step 1.

---

## Post-plan: what Michael must do by hand

1. **`npm login`** — interactive; an agent cannot authenticate. Required before Task 7 Step 6 can publish. Everything else in Task 7 (build, pack, local install test) works without it.
2. **The 1Password Environment mount** (Task 9 Step 1) — re-pointing it at `~/.config/claudemux/env` happens in the 1Password app, not on disk.
3. **The live launch check** (Task 9 Step 5) — no automated test can reach the FIFO-contention path that caused a real incident. It needs a real pane.

(The GitHub repo rename has moved to the **Prerequisite** section at the top — it must happen before Task 1, not after.)
