# Worktree Naming Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop `bin/claudemux` from creating randomly-named worktrees at launch; instead mark the session so a hook asks the model to create a task-named worktree on its first response, and teach the head to render that name as the tab.

**Architecture:** The launcher's worktree *decision* logic is untouched — only its action changes, from appending `--worktree` to exporting `CLAUDEMUX_WORKTREE_PENDING=1`. A new `UserPromptSubmit` hook script writes an `EnterWorktree` instruction to stdout (which Claude Code injects into the model's context) while the marker is set and the cwd is not yet a worktree. The head gains two behaviours: a tab sourced from the worktree name when it observes the session transition into one, and a `⚠ no worktree` chip when the marker is set and the first turn ended outside one.

**Tech Stack:** Go 1.x (`cmd/claudemux-head`, bubbletea), bash (`bin/claudemux`, `hooks/*.sh`), tmux, `go test`.

**Spec:** `docs/superpowers/specs/2026-08-06-worktree-naming-design.md`

## Global Constraints

- The marker environment variable is exactly `CLAUDEMUX_WORKTREE_PENDING`, set to `1`.
- The new hook script is named exactly `claudemux-worktree.sh` and lives in `hooks/`.
- `claudemux-map.sh` must remain silent on stdout — do not add output to it.
- The warning chip text is exactly `⚠ no worktree`.
- Worktree names requested from the model must satisfy `EnterWorktree`'s constraint: letters, digits, dots, underscores, dashes; 64 characters max.
- Every subprocess the head spawns carries a `context.WithTimeout`; results are discarded rather than surfaced as errors. Follow the existing discipline in `renameTabCmd` / `resetTabCmd`.
- Run `go test ./...` from the repo root before each commit. `go vet ./...` too.
- Commit messages: Conventional Commits (`feat(head):`, `fix(head):`, `docs:`), matching `git log`.

---

### Task 1: Hook script that asks for a named worktree

**Files:**
- Create: `hooks/claudemux-worktree.sh`
- Test: `cmd/claudemux-head/worktreehook_test.go` (new)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: a script at `hooks/claudemux-worktree.sh` that reads a Claude Code `UserPromptSubmit` JSON payload on stdin and writes an instruction to stdout. Task 2 registers it; Task 3's launcher sets the env var it reads.

Claude Code hooks receive a JSON payload on stdin. The fields this script needs are `cwd` (absolute path of the session's current working directory). `UserPromptSubmit` stdout is injected verbatim into the model's context — that is the delivery mechanism, and it is why every non-matching path must print nothing at all.

- [ ] **Step 1: Write the failing test**

Create `cmd/claudemux-head/worktreehook_test.go`:

```go
package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// worktreeHookPath locates hooks/claudemux-worktree.sh relative to this test
// file, so the test does not depend on the working directory or on the script
// having been installed.
func worktreeHookPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "hooks", "claudemux-worktree.sh")
}

// runWorktreeHook feeds payload to the hook with the given environment and
// returns its stdout. env entries are "K=V"; the child gets ONLY these, so a
// marker leaking in from the developer's shell cannot mask a failure.
func runWorktreeHook(t *testing.T, payload string, env ...string) string {
	t.Helper()
	cmd := exec.Command("bash", worktreeHookPath(t))
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hook exited non-zero: %v (stdout %q)", err, out)
	}
	return string(out)
}

func TestWorktreeHookSilentWithoutMarker(t *testing.T) {
	got := runWorktreeHook(t, `{"cwd":"/tmp/repo"}`)
	if got != "" {
		t.Errorf("hook spoke with no marker set: %q", got)
	}
}

func TestWorktreeHookSilentInsideWorktree(t *testing.T) {
	got := runWorktreeHook(t,
		`{"cwd":"/tmp/repo/.claude/worktrees/some-name"}`,
		"CLAUDEMUX_WORKTREE_PENDING=1")
	if got != "" {
		t.Errorf("hook spoke for a cwd already in a worktree: %q", got)
	}
}

func TestWorktreeHookSilentOnGarbagePayload(t *testing.T) {
	got := runWorktreeHook(t, `not json at all`, "CLAUDEMUX_WORKTREE_PENDING=1")
	if got != "" {
		t.Errorf("hook spoke on an unparseable payload: %q", got)
	}
}

func TestWorktreeHookAsksForWorktree(t *testing.T) {
	got := runWorktreeHook(t,
		`{"cwd":"/tmp/repo"}`,
		"CLAUDEMUX_WORKTREE_PENDING=1")
	if !strings.Contains(got, "EnterWorktree") {
		t.Errorf("instruction does not name the tool: %q", got)
	}
	// The name convention must reach the model, or it will invent its own shape.
	if !strings.Contains(got, "dash-separated") {
		t.Errorf("instruction does not state the naming convention: %q", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run TestWorktreeHook -v`
Expected: FAIL — every case errors, because `hooks/claudemux-worktree.sh` does not exist yet (`bash: ...: No such file or directory`, surfaced as a non-zero exit by `runWorktreeHook`).

- [ ] **Step 3: Write the script**

Create `hooks/claudemux-worktree.sh`:

```bash
#!/usr/bin/env bash
# Claude Code hook (UserPromptSubmit): ask the model to create a worktree named
# after the task, for sessions bin/claudemux marked as wanting one.
#
# claudemux no longer passes `claude --worktree` — that named the worktree
# during startup, before any prompt existed to name it from, which is how every
# worktree ended up called "lovely-wandering-lovelace". Instead the launcher
# exports CLAUDEMUX_WORKTREE_PENDING and this hook asks the model to make the
# worktree itself, on its first response, when the task IS known.
#
# UNLIKE claudemux-map.sh, this hook SPEAKS on stdout: UserPromptSubmit stdout
# is injected into the model's context, which is the whole delivery mechanism.
# That is why the two live in separate files — every path here that is not the
# one matching case must still print nothing.
set -euo pipefail

# No marker: this session was never promised a worktree (-W, a feature branch,
# a non-repo). Nothing to say, on this or any later prompt.
[ -n "${CLAUDEMUX_WORKTREE_PENDING:-}" ] || exit 0

payload="$(cat)"
cwd="$(printf '%s' "$payload" | jq -r '.cwd // empty' 2>/dev/null || true)"
[ -n "$cwd" ] || exit 0

# Already inside a worktree: the model made one on an earlier prompt (or the
# user entered one by hand). This is what makes the hook self-limiting — no
# state file, no once-per-session bookkeeping.
case "$cwd" in
  */.claude/worktrees/*) exit 0 ;;
esac

cat <<'INSTRUCTION'
<claudemux-worktree>
This session is configured to work in a git worktree, and is not in one yet.
Before any other tool call, call EnterWorktree with a `name` derived from the
user's request above: 2-5 words, lowercase, dash-separated, naming the durable
goal rather than the first step (e.g. "rename-worktrees-on-topic"). Use only
letters, digits, dots, underscores and dashes. Then carry on with the request.
</claudemux-worktree>
INSTRUCTION
```

- [ ] **Step 4: Make it executable and run the tests**

Run:
```bash
chmod +x hooks/claudemux-worktree.sh
go test ./cmd/claudemux-head/ -run TestWorktreeHook -v
```
Expected: PASS, all four cases.

- [ ] **Step 5: Commit**

```bash
git add hooks/claudemux-worktree.sh cmd/claudemux-head/worktreehook_test.go
git commit -m "feat(hooks): ask the model to name its own worktree"
```

---

### Task 2: Register the second hook

**Files:**
- Modify: `cmd/claudemux-head/hook.go:15-23` (constants), `:42-44` (`hookScriptSource`), `:55-137` (`runHookEnsure`), `:145-171` (`addHookEntries`)
- Test: `cmd/claudemux-head/hook_test.go`

**Interfaces:**
- Consumes: `hooks/claudemux-worktree.sh` from Task 1.
- Produces: `hookScripts` — a package-level `[]hookScript` where `type hookScript struct { name string; events []string }`. `claudemux-map.sh` on `SessionStart` + `UserPromptSubmit`; `claudemux-worktree.sh` on `UserPromptSubmit`. Nothing later depends on it.

`runHookEnsure`'s exit-code contract with `install.sh` and `bin/claudemux` does not change: 0 present-or-installed, 2 usage, 3 unparseable settings, 4 I/O. Its ordering guarantee does not change either — settings are read and validated before anything is copied, so a malformed `settings.json` leaves the whole operation a no-op.

The `--script` flag currently overrides the single script's source path. It becomes an override for `claudemux-map.sh` only; the other scripts resolve as its siblings, so `--script` effectively names the source *directory* too.

**This breaks the existing tests unless `stubScript` is fixed first.** `stubScript` writes `claudemux-map.sh` alone into a fresh `t.TempDir()`, so resolving `claudemux-worktree.sh` beside it finds nothing, `copyExecutable` fails, and `runHookEnsure` returns 4 — failing all seven existing `TestHookEnsure*` cases. Step 1 therefore updates the helper as well. A missing source script stays a hard error rather than a skip: a shipped script that failed to install is exactly the thing this code exists to catch.

- [ ] **Step 1: Update the stub helper and write the failing test**

In `cmd/claudemux-head/hook_test.go:28-35`, replace `stubScript` so it lays down every shipped script as siblings, mirroring what a real install directory looks like:

```go
// stubScript creates fake shipped scripts in one directory and returns the
// path to claudemux-map.sh, for --script to point at. Every script in
// hookScripts is written, because hook ensure resolves the others as siblings
// of the --script path — a directory holding only the map script is not a
// layout that occurs in any real install.
func stubScript(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, hs := range hookScripts {
		p := filepath.Join(dir, hs.name)
		if err := os.WriteFile(p, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, hookScriptName)
}
```

Then append the new cases:

```go
func TestHookEnsureRegistersBothScripts(t *testing.T) {
	settingsPath := writeSettings(t, "")
	script := stubScript(t)

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure([]string{"--script", script}, &stdout, &stderr); code != 0 {
		t.Fatalf("hook ensure = %d, want 0 (stderr %s)", code, stderr.String())
	}

	settings := readSettings(t, settingsPath)
	ups := hookCommands(t, settings, "UserPromptSubmit")
	if len(ups) != 2 {
		t.Fatalf("UserPromptSubmit commands = %v, want 2 entries", ups)
	}
	var sawMap, sawWorktree bool
	for _, c := range ups {
		switch filepath.Base(c) {
		case "claudemux-map.sh":
			sawMap = true
		case "claudemux-worktree.sh":
			sawWorktree = true
		}
	}
	if !sawMap || !sawWorktree {
		t.Errorf("UserPromptSubmit = %v, want both scripts", ups)
	}

	// The worktree hook has nothing to record at session start; registering it
	// on SessionStart would inject its instruction before a prompt exists.
	start := hookCommands(t, settings, "SessionStart")
	if len(start) != 1 || filepath.Base(start[0]) != "claudemux-map.sh" {
		t.Errorf("SessionStart = %v, want only claudemux-map.sh", start)
	}
}

func TestHookEnsureDoesNotDuplicateOnSecondRun(t *testing.T) {
	settingsPath := writeSettings(t, "")
	script := stubScript(t)

	var stdout, stderr bytes.Buffer
	for i := 0; i < 2; i++ {
		if code := runHookEnsure([]string{"--script", script}, &stdout, &stderr); code != 0 {
			t.Fatalf("run %d: hook ensure = %d (stderr %s)", i, code, stderr.String())
		}
	}

	settings := readSettings(t, settingsPath)
	if got := hookCommands(t, settings, "UserPromptSubmit"); len(got) != 2 {
		t.Errorf("UserPromptSubmit after two runs = %v, want 2 entries", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run TestHookEnsure -v`
Expected: FAIL to compile — `undefined: hookScripts` in the updated `stubScript`. Once Step 3 defines it, the remaining failure is `TestHookEnsureRegistersBothScripts` reporting one `UserPromptSubmit` entry instead of two, until the loop is in place.

- [ ] **Step 3: Generalize hook.go to a list of scripts**

Replace `hook.go:15-23` with:

```go
// hookScript is one script this tool installs and the Claude Code events it
// registers on.
type hookScript struct {
	name   string
	events []string
}

// hookScripts are every script claudemux installs.
//
// claudemux-map.sh records which session lives in which tmux pane. SessionStart
// records the mapping when a pane first opens; UserPromptSubmit keeps it
// current across /clear, resume, and compaction, which rotate the transcript
// file underneath a live session. Registering only one leaves the map stale in
// exactly the cases users notice.
//
// claudemux-worktree.sh asks the model to create a task-named worktree. It is
// UserPromptSubmit ONLY: at SessionStart there is no prompt yet, so there is
// nothing to name a worktree after — which is the entire problem it exists to
// fix.
var hookScripts = []hookScript{
	{name: "claudemux-map.sh", events: []string{"SessionStart", "UserPromptSubmit"}},
	{name: "claudemux-worktree.sh", events: []string{"UserPromptSubmit"}},
}

// hookScriptName is the pane-map script's filename, which `--script` overrides.
const hookScriptName = "claudemux-map.sh"
```

In `runHookEnsure`, replace the single-source resolution and the single `copyExecutable`/`addHookEntries` pair. The source directory is resolved once; `--script` overrides only the pane-map script's path:

```go
	// srcFor returns where to read a shipped script from. --script overrides
	// only claudemux-map.sh, so existing callers (and tests) that point it at a
	// stub keep working while the other scripts resolve as siblings.
	srcFor := func(name string) (string, error) {
		if name == hookScriptName && *scriptFlag != "" {
			return *scriptFlag, nil
		}
		if *scriptFlag != "" {
			return filepath.Join(filepath.Dir(*scriptFlag), name), nil
		}
		return siblingOfExecutable(name)
	}
```

Then, after the settings read and `MkdirAll` (keeping that order — settings validated before any write):

```go
	changed := false
	for _, hs := range hookScripts {
		src, err := srcFor(hs.name)
		if err != nil {
			fmt.Fprintf(stderr, "claudemux: locating %s: %v\n", hs.name, err)
			return 4
		}
		dst := filepath.Join(hooksDir, hs.name)
		if err := copyExecutable(src, dst); err != nil {
			fmt.Fprintf(stderr, "claudemux: installing %s: %v\n", hs.name, err)
			return 4
		}
		if addHookEntries(settings, dst, hs.events) {
			changed = true
		}
	}
	if !changed {
		return 0 // every script registered on every event: no write, stay silent
	}
```

Change `addHookEntries` to take the events for the script it is adding:

```go
func addHookEntries(settings map[string]any, command string, events []string) bool {
```

and iterate `events` instead of the package-level `hookEvents`. Its body is otherwise unchanged — append, never replace; walk the generic map so unmodelled keys round-trip.

Finally, update the success message, since it now covers more than the pane map:

```go
	fmt.Fprintf(stdout, "claudemux: registered claudemux hooks in %s\n", settingsPath)
```

- [ ] **Step 4: Run the tests**

Run: `go test ./cmd/claudemux-head/ -run TestHook -v && go vet ./...`
Expected: PASS, including the pre-existing `TestHookEnsure*` cases (malformed settings → 3 with nothing written, other tools' hooks preserved, backup written).

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/hook.go cmd/claudemux-head/hook_test.go
git commit -m "feat(head): register the worktree hook alongside the pane map"
```

---

### Task 3: Launcher marks instead of creating

**Files:**
- Modify: `bin/claudemux:16-27` (usage comments), `:468-475` (the `--worktree` append), `:490-496` (head pane launch)

**Interfaces:**
- Consumes: the hook from Tasks 1-2, which reads `CLAUDEMUX_WORKTREE_PENDING`.
- Produces: `CLAUDEMUX_WORKTREE_PENDING=1` in the environment of both the claude pane and the head pane, for sessions where `worktree_requested` succeeds. Task 5 reads it in the head.

`worktree_requested`, `auto_worktree_wanted`, the `-w`/`-W` precedence and `.project.yml` handling are **not** touched. Only the action changes.

This file has no test harness — it is shell, exercised by running it. Verification is manual and is spelled out in Step 3.

- [ ] **Step 1: Replace the `--worktree` append**

At `bin/claudemux:468-475`, replace this block:

```bash
  # LAST, after every positional. `--worktree [name]` takes an optional value,
  # so whatever token follows it becomes the worktree name: placed before the
  # "/color X" prompt argument, it swallowed the prompt as a (rejected) name
  # and the launch died. At the end of the command there is nothing to swallow,
  # and claude auto-names the worktree. Keep any future appends ABOVE this.
  if worktree_requested "$work_dir"; then
    claude_cmd+=" --worktree"
  fi
```

with:

```bash
  # A marked session creates its worktree on its FIRST RESPONSE, not at launch:
  # hooks/claudemux-worktree.sh sees this variable, sees a cwd that is not yet a
  # worktree, and asks the model to call EnterWorktree with a name derived from
  # the prompt. `claude --worktree` cannot do that — it names the worktree during
  # startup, before any prompt exists, which is why every worktree used to be
  # called something like "lovely-wandering-lovelace".
  #
  # The head gets it too, so it can show `⚠ no worktree` if the first turn ends
  # with the session still in the main checkout.
  #
  # This retires the ordering constraint that used to live here: `--worktree
  # [name]` takes an optional value, so appended anywhere before the "/color X"
  # prompt argument it swallowed the prompt as a (rejected) worktree name and the
  # launch died. An env prefix has no such hazard, and appends to claude_cmd are
  # no longer ordered. Keep the hazard in mind if --worktree is ever reinstated.
  local worktree_env=""
  if worktree_requested "$work_dir"; then
    worktree_env="CLAUDEMUX_WORKTREE_PENDING=1 "
  fi
  claude_cmd="${worktree_env}${claude_cmd}"
```

Declare `worktree_env` with the other locals at `bin/claudemux:452` instead if you prefer this file's convention of declaring locals up front — check the surrounding style and match it.

- [ ] **Step 2: Pass the marker to the head pane**

At `bin/claudemux:496`, replace:

```bash
  run_in_pane "$head_pane" "$(printf '%q' "$head_bin")"
```

with:

```bash
  run_in_pane "$head_pane" "${worktree_env}$(printf '%q' "$head_bin")"
```

`run_in_pane` builds `tmux respawn-pane -k -t <pane> "exec $2"`, so a `K=V ` prefix is interpreted by the shell tmux runs the command under — the same mechanism the claude pane now uses.

Note the `op_env` branch at `:507-514` wraps `$claude_cmd` after `--` in `holder_cmd`. Because the prefix is now *inside* `claude_cmd`, the holder execs it with the variable set, and nothing there needs changing. Verify this in Step 3 if the machine has a project with `op_env` set.

- [ ] **Step 3: Verify by launching**

Run, in a repo whose default branch is checked out and which has `worktree: true` in `.project.yml` (or with `launch.auto_worktree` enabled):

```bash
claudemux -n -w .
```

Expected, checked in the new session:
- The claude pane's process environment contains the marker: `ps eww $(pgrep -n claude) | tr ' ' '\n' | grep CLAUDEMUX_WORKTREE_PENDING` prints `CLAUDEMUX_WORKTREE_PENDING=1`.
- No worktree exists yet: `git worktree list` shows only the main checkout plus any pre-existing ones.
- Typing a first prompt (e.g. "add a --version flag to the launcher") causes the session to call `EnterWorktree`, after which `git worktree list` shows a new worktree with a task-derived name, on branch `worktree-<that name>`.

Then run `claudemux -n -W .` in the same repo and confirm the marker is absent and no worktree is created.

- [ ] **Step 4: Update the usage comments**

At `bin/claudemux:16-27`, the header currently describes `-w`/`-W` as controlling whether `--worktree` is passed. Rewrite that block to describe marking:

```bash
#   -w   Mark this session as wanting a worktree regardless of config or repo
#        state.
#   -W   Never mark this session, whatever the config says.
#        -w/-W (and the auto_worktree heuristic below) only affect NEWLY
#        CREATED sessions — attaching to an existing session reuses whatever
#        claude command it was launched with, same as name/color. Combine
#        with -n to force a new session if you need -w/-W to take effect.
#
# This script does not create worktrees and no longer passes `claude
# --worktree`. It decides whether a session SHOULD have one and exports
# CLAUDEMUX_WORKTREE_PENDING; hooks/claudemux-worktree.sh then asks the model to
# create one named after the task on its first response. Creating it at launch
# meant naming it before any prompt existed. With launch.auto_worktree enabled
# in config.yml (or `worktree: true` in .project.yml), a launch on a repo's
# default branch is marked automatically; feature branches, detached HEADs,
# linked worktrees, and non-repos are left alone.
#
# A session that is opened and never prompted therefore gets no worktree at all.
```

- [ ] **Step 5: Commit**

```bash
git add bin/claudemux
git commit -m "feat(launch): mark sessions for a worktree instead of creating one"
```

---

### Task 4: Tab sourced from the worktree name

**Files:**
- Modify: `cmd/claudemux-head/tui.go` — model fields near `:120-130`, `recomputeFromEvents` at `:329-345`, `tabCmdFor` at `:620-626`, `switchSession` near `:412-414`
- Test: `cmd/claudemux-head/tui_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `func tabLabel(worktreeTab, haikuTab string, haikuWins bool) string` and two model fields, `worktreeTab string` and `tabHaikuWins bool`. Task 5 adds a separate field and does not read these.

The rule: once the head observes the session move from outside a worktree into one, the tab is that worktree's name with dashes turned back into spaces. Haiku's `Tab` takes over permanently the first time the summarizer's `Topic` *changes* from an already-established non-empty value — the first summary establishing a topic is not a change.

Gating on the observed transition (rather than on "is in a worktree") is what stops a session that started already inside `lovely-wandering-lovelace` from rendering `lovely wandering lovelace` as its tab, which would be worse than today's Haiku label.

- [ ] **Step 1: Write the failing test**

Append to `cmd/claudemux-head/tui_test.go`:

```go
func TestTabLabelPrefersWorktreeUntilHaikuWins(t *testing.T) {
	tests := []struct {
		name        string
		worktreeTab string
		haikuTab    string
		haikuWins   bool
		want        string
	}{
		{"worktree name wins before any correction",
			"rename worktrees on topic", "worktree naming", false, "rename worktrees on topic"},
		{"haiku wins after a topic change",
			"rename worktrees on topic", "worktree naming", true, "worktree naming"},
		{"no worktree observed falls back to haiku",
			"", "worktree naming", false, "worktree naming"},
		{"haiku empty falls back to the worktree name",
			"rename worktrees on topic", "", true, "rename worktrees on topic"},
		{"neither yields empty",
			"", "", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tabLabel(tt.worktreeTab, tt.haikuTab, tt.haikuWins); got != tt.want {
				t.Errorf("tabLabel(%q, %q, %v) = %q, want %q",
					tt.worktreeTab, tt.haikuTab, tt.haikuWins, got, tt.want)
			}
		})
	}
}

func TestWorktreeTabSetOnlyOnTransition(t *testing.T) {
	// A session that was already in a worktree when the head started must not
	// adopt its name — it is a random one from before this feature.
	m := &model{sessionCwd: "/repo/.claude/worktrees/lovely-wandering-lovelace"}
	m.observeWorktreeTransition()
	if m.worktreeTab != "" {
		t.Errorf("adopted a pre-existing worktree name: %q", m.worktreeTab)
	}

	// A session observed OUTSIDE a worktree and then inside one did the
	// transition, so its name is task-derived and may be adopted.
	m2 := &model{sessionCwd: "/repo"}
	m2.observeWorktreeTransition()
	m2.sessionCwd = "/repo/.claude/worktrees/rename-worktrees-on-topic"
	m2.observeWorktreeTransition()
	if got, want := m2.worktreeTab, "rename worktrees on topic"; got != want {
		t.Errorf("worktreeTab = %q, want %q", got, want)
	}
}

func TestHaikuWinsOnlyOnTopicChange(t *testing.T) {
	m := &model{}
	// First summary establishes a topic — not a change.
	m.noteTopic("naming worktrees after their work")
	if m.tabHaikuWins {
		t.Error("first topic counted as a correction")
	}
	// Same topic again — still not a change.
	m.noteTopic("naming worktrees after their work")
	if m.tabHaikuWins {
		t.Error("an unchanged topic counted as a correction")
	}
	// A genuinely different topic is the correction signal.
	m.noteTopic("debugging the teardown gate")
	if !m.tabHaikuWins {
		t.Error("a changed topic did not hand the tab to haiku")
	}
	// The latch is one-way.
	m.noteTopic("naming worktrees after their work")
	if !m.tabHaikuWins {
		t.Error("latch reverted")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run 'TestTabLabel|TestWorktreeTab|TestHaikuWins' -v`
Expected: FAIL to compile — `undefined: tabLabel`, `m.observeWorktreeTransition undefined`, `m.worktreeTab undefined`, `m.noteTopic undefined`, `m.tabHaikuWins undefined`.

- [ ] **Step 3: Add the fields and functions**

Add to the model struct, beside `cmdWorktree` (around `tui.go:130`):

```go
	// worktreeTab is the tab label derived from the worktree this session
	// created for itself — its name with dashes turned back into spaces. Set
	// only when the head OBSERVES the session move from outside a worktree into
	// one, which is the signature of hooks/claudemux-worktree.sh having worked
	// and therefore of a task-derived name. A session already in a worktree at
	// startup is left alone: its name predates this feature (or was made by
	// hand), and rendering "lovely wandering lovelace" as the tab would be
	// strictly worse than the Haiku label it would replace.
	//
	// sawNonWorktreeCwd records that the session was once observed OUTSIDE a
	// worktree, which is the first half of that transition.
	worktreeTab      string
	sawNonWorktreeCwd bool

	// tabHaikuWins latches on the first time the summarizer REPLACES an
	// established topic, handing the tab to Haiku's label permanently. It
	// reuses the summary prompt's own stability rule ("KEEP IT VERBATIM unless
	// the human has genuinely changed goals") as the "materially different"
	// test, rather than inventing a string-similarity metric. A first summary
	// establishing a topic is not a change.
	tabHaikuWins bool
	// lastTopic is the topic tabHaikuWins compares against.
	lastTopic string
```

Add the three functions near `tabCmdFor` (`tui.go:620`):

```go
// tabLabel picks the window label: the name the session gave its own worktree
// until Haiku has earned the tab, then Haiku's. Either side falls back to the
// other when empty, so a missing summary or a session that never made a
// worktree still gets whatever label exists.
func tabLabel(worktreeTab, haikuTab string, haikuWins bool) string {
	if haikuWins && haikuTab != "" {
		return haikuTab
	}
	if worktreeTab != "" {
		return worktreeTab
	}
	return haikuTab
}

// observeWorktreeTransition watches sessionCwd for the session entering a
// worktree, and adopts that worktree's name as the tab when it does. See the
// worktreeTab field for why only an observed transition counts.
func (m *model) observeWorktreeTransition() {
	name := worktreeNameForCwd(m.sessionCwd)
	if name == "" {
		if m.sessionCwd != "" {
			m.sawNonWorktreeCwd = true
		}
		return
	}
	if m.sawNonWorktreeCwd && m.worktreeTab == "" {
		m.worktreeTab = strings.ReplaceAll(name, "-", " ")
	}
}

// noteTopic records a landed summary's topic and latches tabHaikuWins when it
// REPLACES an established one.
func (m *model) noteTopic(topic string) {
	if topic == "" {
		return
	}
	if m.lastTopic != "" && topic != m.lastTopic {
		m.tabHaikuWins = true
	}
	m.lastTopic = topic
}
```

Call `observeWorktreeTransition` at the end of `recomputeFromEvents` (`tui.go:345`), immediately after `m.cmdWorktree` is set — `sessionCwd` is fresh at that point:

```go
	m.cmdWorktree = commandWorktree(m.sessionCwd, m.allEvents, now)
	m.observeWorktreeTransition()
```

Call `noteTopic` in the `summaryMsg` handler, in the branch that accepts a summary (`tui.go:855-856`), BEFORE `m.summary` is overwritten:

```go
			m.noteTopic(msg.summary.Topic)
			m.summaryRetry = false
			m.summary = msg.summary
```

Change `tabCmdFor` (`tui.go:620-626`) to use the label:

```go
func (m model) tabCmdFor(s Summary) tea.Cmd {
	if !m.tabTitle || m.tabPinned {
		return nil
	}
	return renameTabCmd(m.selfPane, tabLabel(m.worktreeTab, s.Tab, m.tabHaikuWins))
}
```

In `switchSession` (`tui.go:412-414`), clear the new per-session state alongside `m.summary`:

```go
	m.summary = Summary{}
	m.worktreeTab = ""
	m.sawNonWorktreeCwd = false
	m.tabHaikuWins = false
	m.lastTopic = ""
```

- [ ] **Step 4: Run the tests**

Run: `go test ./... && go vet ./...`
Expected: PASS, including the existing tab tests in `tui_test.go` and `tabreset_test.go`.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/tui.go cmd/claudemux-head/tui_test.go
git commit -m "feat(head): take the tab from the worktree the session named"
```

---

### Task 5: The `⚠ no worktree` chip

**Files:**
- Modify: `cmd/claudemux-head/tui.go` — model field near `:253`, `newModel` near `:310-317`, `worktreeChip` at `:1386-1394`
- Test: `cmd/claudemux-head/tui_test.go`

**Interfaces:**
- Consumes: `CLAUDEMUX_WORKTREE_PENDING` from Task 3.
- Produces: `func worktreeChipText(chip string, pending, turnEnded, sawPrompt bool) string`, plus a `worktreePending bool` model field.

The chip is the whole mitigation for this design's accepted risk. Without it a skipped `EnterWorktree` call means working on the default branch in the shared checkout with nothing on screen saying so.

It shows only after the first turn has *ended* — the head has seen at least one genuine user prompt and the state is not `StateThinking`, `StateTool` or `StateCompacting`, the same test `teardownTurnEnded` applies. Requiring a prompt keeps it off a session sitting at an empty input, where nothing has been skipped yet.

- [ ] **Step 1: Write the failing test**

Append to `cmd/claudemux-head/tui_test.go`:

```go
func TestWorktreeChipTextWarnsWhenNoneAppeared(t *testing.T) {
	tests := []struct {
		name                       string
		chip                       string
		pending, ended, sawPrompt  bool
		want                       string
	}{
		{"warns once the first turn ends with no worktree",
			"", true, true, true, "⚠ no worktree"},
		{"silent before a prompt",
			"", true, true, false, ""},
		{"silent mid-turn",
			"", true, false, true, ""},
		{"silent when the session was never marked",
			"", false, true, true, ""},
		{"a worktree that appeared wins over the warning",
			"rename-worktrees-on-topic", true, true, true, "rename-worktrees-on-topic"},
		{"unmarked session with a worktree still shows it",
			"some-worktree", false, true, true, "some-worktree"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := worktreeChipText(tt.chip, tt.pending, tt.ended, tt.sawPrompt)
			if got != tt.want {
				t.Errorf("worktreeChipText(%q, %v, %v, %v) = %q, want %q",
					tt.chip, tt.pending, tt.ended, tt.sawPrompt, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run TestWorktreeChipText -v`
Expected: FAIL to compile — `undefined: worktreeChipText`.

- [ ] **Step 3: Implement**

Add the model field beside `inWorktree` (`tui.go:253`):

```go
	// worktreePending records that bin/claudemux marked this session as wanting
	// a worktree (CLAUDEMUX_WORKTREE_PENDING). The launcher no longer creates
	// one; hooks/claudemux-worktree.sh asks the model to. When the model skips
	// that call the session works on the default branch in the SHARED checkout,
	// which is exactly what the marking existed to prevent — so a marked
	// session whose first turn ended outside a worktree says so in the chip.
	worktreePending bool
```

Set it in `newModel`, beside the other startup captures (`tui.go:310-317`):

```go
	m.worktreePending = os.Getenv("CLAUDEMUX_WORKTREE_PENDING") != ""
```

Add the pure function beside `worktreeChip` (`tui.go:1386`):

```go
// worktreeChipText decides what the worktree chip slot shows. A real worktree
// always wins: the warning is only for a marked session that has not got one.
//
// sawPrompt gates the warning so a session sitting at an empty input is not
// accused of skipping anything — nothing has been asked of it yet.
func worktreeChipText(chip string, pending, turnEnded, sawPrompt bool) string {
	if chip != "" {
		return chip
	}
	if pending && turnEnded && sawPrompt {
		return "⚠ no worktree"
	}
	return ""
}
```

Rename the existing `worktreeChip` body to a helper and have `worktreeChip` compose the two. Replace `tui.go:1386-1394` with:

```go
// observedWorktree is the worktree the session is in, or is driving at arm's
// length, or "" for neither.
func (m model) observedWorktree() string {
	if m.sessionCwd != "" {
		if name := worktreeNameForCwd(m.sessionCwd); name != "" {
			return name
		}
		return m.cmdWorktree
	}
	return worktreeName(m.jsonlPath)
}

func (m model) worktreeChip() string {
	return worktreeChipText(m.observedWorktree(), m.worktreePending,
		teardownTurnEnded(m.state.Kind), m.firstPrompt != "")
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./... && go vet ./...`
Expected: PASS. Existing `worktreeChip` callers (`renderStateLine`, `renderStatusbar`, `renderTeardownChip` region) are unchanged — the signature is the same.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/tui.go cmd/claudemux-head/tui_test.go
git commit -m "feat(head): warn when a marked session never got its worktree"
```

---

### Task 6: Documentation

**Files:**
- Modify: `README.md:136` (`auto_worktree` config example), `:165-172` (`launch.auto_worktree` prose), `:287-295` (which worktree the teardown gate watches)

**Interfaces:**
- Consumes: the behaviour built in Tasks 1-5.
- Produces: nothing.

- [ ] **Step 1: Read the current text**

Run: `sed -n '130,200p;280,310p' README.md`

Note every claim that says claudemux passes `--worktree`, or that a worktree exists from launch. Those are the sentences that are now false.

- [ ] **Step 2: Rewrite the `launch.auto_worktree` section**

The section at `:165-172` currently says a marked launch "passes `--worktree` to `claude`, so the session works in an isolated worktree and the checkout stays pristine". Replace that mechanism sentence with the new one, keeping the surrounding precedence description (`-w`/`-W`, `.project.yml`, default `false`) intact:

- claudemux marks the session with `CLAUDEMUX_WORKTREE_PENDING` rather than creating a worktree.
- `hooks/claudemux-worktree.sh` asks the model, on the first prompt, to call `EnterWorktree` with a name derived from the task — which is why worktrees are now called things like `rename-worktrees-on-topic` instead of `lovely-wandering-lovelace`.
- The worktree therefore appears during the first response, not at launch. A session opened and never prompted gets none.
- If the model skips the call, the status pane shows `⚠ no worktree` once the first turn ends.

- [ ] **Step 3: Add the tab-naming behaviour**

Wherever the README describes the tab title (search: `grep -n 'tab' README.md`), record that a session which created its own worktree takes its tab from that worktree's name, dashes rendered as spaces, until the summarizer replaces the topic — after which the Haiku label takes over for good.

- [ ] **Step 4: Check the teardown section still reads true**

At `:287-295` the README says the gate watches the worktree "the session's working directory is in — the cwd from its transcript, which is where `claudemux -w` (or `launch.auto_worktree`, or `worktree: true`) put it." The parenthetical is now wrong: those mark the session, and the model puts it there. Fix the attribution. The mechanism itself is unchanged and should be stated as still holding.

- [ ] **Step 5: Verify and commit**

Run: `grep -n -- '--worktree' README.md`
Expected: no hit claims claudemux passes it at launch. A historical mention is fine if it is explicitly marked as the old behaviour.

```bash
git add README.md
git commit -m "docs: worktrees are named by the session, not the launcher"
```

---

## Verification

After Task 6, confirm end to end in a repo marked `worktree: true`:

```bash
go test ./... && go vet ./...
claudemux-head hook ensure          # exit 0; both scripts in ~/.claude/hooks
claudemux -n .
```

Then, in the new session: no worktree exists before the first prompt; a first prompt produces a worktree whose name reflects the task; the tab shows that name with spaces; `git worktree list` and `git branch` both read as English. Let the summarizer run and confirm the tab stays put while the topic holds.
