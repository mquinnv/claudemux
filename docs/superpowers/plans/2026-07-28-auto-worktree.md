# Auto-Worktree on Launch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `launch.auto_worktree` setting that makes `claudemux` pass `--worktree` to `claude` when launching on a repo's default branch, with `.project.yml` and CLI-flag overrides.

**Architecture:** The Go side (`cmd/claudemux-head/config.go`) only stores the new config key so `claudemux-head config get launch.auto_worktree` resolves it; all decision logic lives in `bin/claudemux` beside where `claude_cmd` is assembled. Claude Code's own `-w, --worktree [name]` flag does the actual worktree creation.

**Tech Stack:** Go (yaml.v3 config), bash launcher, git plumbing commands.

**Spec:** `docs/superpowers/specs/2026-07-28-auto-worktree-design.md`

## Global Constraints

- New config key defaults to **false**; existing users must see zero behavior change on upgrade.
- Precedence, highest wins: `-w`/`-W` CLI flag → `.project.yml` `worktree: true|false` → config.yml `launch.auto_worktree`.
- `-w` bypasses the appropriateness heuristic; auto mode (either config source) applies it.
- Every heuristic check is a single fast git subprocess — this runs on the launch path that was just optimized; add nothing slow.
- `docs/superpowers/` is gitignored in this repo — never `git add -f` the spec or this plan.
- Go tests run with: `go test ./cmd/claudemux-head/`
- The repo convention for commit messages is `type(scope): imperative summary` (see `git log`).

---

### Task 1: `launch.auto_worktree` config key (Go)

**Files:**
- Modify: `cmd/claudemux-head/config.go` (Config struct around line 44)
- Test: `cmd/claudemux-head/config_test.go`, `cmd/claudemux-head/configget_test.go`

**Interfaces:**
- Produces: `Config.Launch.AutoWorktree bool` (yaml path `launch.auto_worktree`, default `false`), resolvable via `claudemux-head config get launch.auto_worktree` → prints `true`/`false`. Task 2's shell code consumes the `config get` output.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/claudemux-head/config_test.go` (it already has a `writeConfig(t, contents)` helper that points `XDG_CONFIG_HOME` at a temp dir):

```go
func TestAutoWorktreeDefaultsFalse(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config.yml

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Launch.AutoWorktree {
		t.Error("Launch.AutoWorktree = true, want false by default — launching into a surprise worktree must be opt-in")
	}
}

func TestAutoWorktreeCanBeEnabled(t *testing.T) {
	writeConfig(t, "launch:\n  auto_worktree: true\n")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if !cfg.Launch.AutoWorktree {
		t.Error("Launch.AutoWorktree = false, want true when set in the file")
	}
	// A file naming only launch keys must not zero the summary defaults.
	if !cfg.Summary.Enabled {
		t.Error("Summary.Enabled = false — a partial file zeroed an unrelated default")
	}
}

func TestAutoWorktreeUnknownKeyUnderLaunchIsFatal(t *testing.T) {
	writeConfig(t, "launch:\n  auto_worktre: true\n") // typo: missing final e

	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() error = nil, want an error — a typo under launch: must fail loudly, not silently behave as unset")
	}
}
```

Append to `cmd/claudemux-head/configget_test.go` (match its existing style — it calls `runConfigGet(args, &stdout, &stderr)` with `bytes.Buffer`s; read the file first and mirror how the existing bool test for `summary.enabled`, if any, captures output):

```go
func TestConfigGetLaunchAutoWorktree(t *testing.T) {
	writeConfig(t, "launch:\n  auto_worktree: true\n")

	var stdout, stderr bytes.Buffer
	code := runConfigGet([]string{"launch.auto_worktree"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "true" {
		t.Errorf("config get launch.auto_worktree = %q, want %q", got, "true")
	}
}

func TestConfigGetLaunchAutoWorktreeDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config.yml

	var stdout, stderr bytes.Buffer
	code := runConfigGet([]string{"launch.auto_worktree"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "false" {
		t.Errorf("config get launch.auto_worktree = %q, want %q — the key must resolve (exit 0) even when unset, so the launcher reads a definite false rather than an absent key", got, "false")
	}
}
```

If `configget_test.go` lacks the `bytes`/`strings` imports, add them.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'AutoWorktree|ConfigGetLaunch' -v`
Expected: compile error — `cfg.Launch` undefined. That counts as the failing state.

- [ ] **Step 3: Implement**

In `cmd/claudemux-head/config.go`, add to the `Config` struct (after the `OnePassword` field):

```go
	Launch      LaunchConfig      `yaml:"launch"`
```

And below `OnePasswordConfig`, add:

```go
// LaunchConfig is consumed by bin/claudemux via `claudemux-head config get`,
// not by the TUI. AutoWorktree makes the launcher pass `--worktree` to claude
// when the launch directory is a repo's main checkout sitting on its default
// branch (the launcher owns that heuristic and its overrides — see
// bin/claudemux worktree_requested). Default false: launching into a surprise
// worktree must be something the user asked for.
type LaunchConfig struct {
	AutoWorktree bool `yaml:"auto_worktree"`
}
```

No `defaultConfig()` change (the zero value false IS the default) and no `validate()` change (a bool cannot be malformed).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/claudemux-head/`
Expected: PASS (full package, not just the new tests — the `configLookup` YAML round-trip must still marshal cleanly).

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/config.go cmd/claudemux-head/config_test.go cmd/claudemux-head/configget_test.go
git commit -m "feat(config): add launch.auto_worktree toggle (default off)"
```

---

### Task 2: Launcher decision logic (`bin/claudemux`)

**Files:**
- Modify: `bin/claudemux` — header comment (lines 3, 13–18), getopts block (lines 22–29), new functions after `ch_config_get` (after line 200), `create_session` claude_cmd assembly (around line 326)

**Interfaces:**
- Consumes: `claudemux-head config get launch.auto_worktree` (prints `true`/`false`, exit 0) via the existing `ch_config_get` helper; `project_field FILE KEY` helper for `.project.yml`.
- Produces: `claude_cmd` gains ` --worktree` when `worktree_requested "$work_dir"` succeeds. New CLI flags `-w` / `-W`.

- [ ] **Step 1: Update the header comment and usage**

Replace lines 3 and 13–18 of `bin/claudemux` so the header reads:

```bash
# Usage: claudemux [-n] [-w|-W] [dir|query ...]
```

and the Options/isolation block becomes:

```bash
# Options:
#   -n   Create a new session even if one with the same name exists
#        (appends -2, -3, ... to the session name).
#   -w   Launch claude with --worktree regardless of config or repo state.
#   -W   Never pass --worktree for this launch, whatever the config says.
#
# Worktree creation itself is Claude Code's job (`claude --worktree`); this
# script only decides whether to pass the flag. With launch.auto_worktree
# enabled in config.yml (or `worktree: true` in .project.yml), a launch on a
# repo's default branch gets --worktree automatically; feature branches,
# detached HEADs, linked worktrees, and non-repos are left alone.
```

- [ ] **Step 2: Extend getopts**

Replace the getopts block (lines 22–29) with the following. Note the `worktree_mode_set` guard: plain `WORKTREE_MODE=force` / `=skip` assignments would let `-w -W` silently take whichever came last, and the spec says conflicting flags are a usage error.

```bash
FORCE_NEW=false
WORKTREE_MODE=""   # "" = decide from config; "force"/"skip" from -w/-W
worktree_mode_set() {
  if [ -n "$WORKTREE_MODE" ] && [ "$WORKTREE_MODE" != "$1" ]; then
    echo "claudemux: -w and -W are mutually exclusive" >&2
    exit 1
  fi
  WORKTREE_MODE="$1"
}
while getopts "nwW" opt; do
  case "$opt" in
    n) FORCE_NEW=true ;;
    w) worktree_mode_set force ;;
    W) worktree_mode_set skip ;;
    *) echo "Usage: claudemux [-n] [-w|-W] [dir|query ...]" >&2; exit 1 ;;
  esac
done
shift $((OPTIND - 1))
```

- [ ] **Step 3: Add the heuristic and precedence functions**

Insert after the `ch_config_get` function (after line 200), before `op_account_for`:

```bash
# auto_worktree_wanted DIR — success when an automatic `claude --worktree` is
# appropriate for DIR: a git repo's MAIN checkout sitting on its default
# branch. Feature branches, detached HEADs, linked worktrees, and non-repos
# all say no — those are states the user chose, and a surprise worktree would
# fight them. Each check is one fast git subprocess; keep it that way, this
# runs on the launch path.
auto_worktree_wanted() {
  local dir="$1" branch default
  command -v git >/dev/null 2>&1 || return 1
  [ "$(git -C "$dir" rev-parse --is-inside-work-tree 2>/dev/null)" = "true" ] || return 1
  # A linked worktree's --git-dir sits under the main checkout's
  # .git/worktrees/<name>; only in the main checkout do the two coincide.
  [ "$(git -C "$dir" rev-parse --git-dir 2>/dev/null)" = \
    "$(git -C "$dir" rev-parse --git-common-dir 2>/dev/null)" ] || return 1
  branch="$(git -C "$dir" symbolic-ref --short -q HEAD)" || return 1 # detached
  default="$(git -C "$dir" symbolic-ref --short -q refs/remotes/origin/HEAD || true)"
  default="${default#origin/}"
  if [ -n "$default" ]; then
    [ "$branch" = "$default" ]
  else
    # No origin/HEAD (no remote, or never fetched): fall back to convention.
    [ "$branch" = "main" ] || [ "$branch" = "master" ]
  fi
}

# worktree_requested DIR — success when this launch should pass --worktree.
# Precedence, highest first: -w/-W, `worktree:` in DIR/.project.yml,
# launch.auto_worktree in config.yml. An explicit -w skips the
# appropriateness heuristic entirely — the user asked, and if DIR turns out
# not to be a repo, claude reports that itself. Both config sources are
# auto mode and go through auto_worktree_wanted.
worktree_requested() {
  local dir="$1" proj
  case "$WORKTREE_MODE" in
    force) return 0 ;;
    skip)  return 1 ;;
  esac
  proj="$(project_field "$dir/.project.yml" worktree)"
  case "$proj" in
    true)  auto_worktree_wanted "$dir"; return ;;
    false) return 1 ;;
  esac
  ch_config_get launch.auto_worktree
  [ "$CH_CONFIG_VALUE" = "true" ] || return 1
  auto_worktree_wanted "$dir"
}
```

Note: `worktree_requested` calls `project_field`, which is defined at line 160 — above the insertion point, so ordering is fine. `ch_config_get` never fails under `set -e` (it always returns 0) and reports a malformed config.yml exactly once; an empty/absent value falls through to "no worktree".

- [ ] **Step 4: Append the flag in create_session**

In `create_session`, immediately after the line `claude_cmd="claude --permission-mode auto"` (line 326) and before the `-n`/color appends, add:

```bash
  if worktree_requested "$work_dir"; then
    claude_cmd+=" --worktree"
  fi
```

- [ ] **Step 5: Syntax and lint check**

Run: `bash -n bin/claudemux && shellcheck bin/claudemux || true`
Expected: `bash -n` silent (exit 0). If shellcheck is installed, no NEW warnings versus `git stash`-comparing is overkill — just confirm any warnings it prints predate this change (the file has `# shellcheck disable` pragmas already).

- [ ] **Step 6: Functional check of the heuristic against real repos**

Extract the function into a subshell and run it against five throwaway fixtures in the scratchpad (NOT /tmp, NOT the project dir):

```bash
S=/private/tmp/claude-501/-Users-michael-Projects-claudemux/8f21899f-44c0-4a2f-a9d3-1e49dfad746b/scratchpad/wt-fixtures
mkdir -p "$S" && cd "$S"
git init -q -b main default-branch && (cd default-branch && git commit -q --allow-empty -m x)
git init -q -b main feature && (cd feature && git commit -q --allow-empty -m x && git checkout -q -b topic)
git init -q -b main detached && (cd detached && git commit -q --allow-empty -m x && git checkout -q --detach)
(cd default-branch && git worktree add -q "$S/linked" -b wt-branch)
mkdir -p not-a-repo

fn="$(sed -n '/^auto_worktree_wanted()/,/^}$/p' /Users/michael/Projects/claudemux/bin/claudemux)"
for d in default-branch feature detached linked not-a-repo; do
  bash -c "$fn; auto_worktree_wanted '$S/$d' && echo '$d: WORKTREE' || echo '$d: no'"
done
```

Expected output, exactly:

```
default-branch: WORKTREE
feature: no
detached: no
linked: no
not-a-repo: no
```

Then clean up: `rm -rf "$S"` (run `git -C "$S/default-branch" worktree prune` is unnecessary — the whole fixture tree is deleted).

- [ ] **Step 7: Commit**

```bash
git add bin/claudemux
git commit -m "feat(launcher): auto --worktree on default-branch launches, with -w/-W overrides"
```

---

### Task 3: Documentation

**Files:**
- Modify: `README.md` (Configuration section ~line 109; `.project.yml` section ~line 147)
- Modify: `.project.yml.example`

**Interfaces:**
- Consumes: names fixed by Tasks 1–2: `launch.auto_worktree`, `.project.yml` key `worktree`, flags `-w`/`-W`.

- [ ] **Step 1: README Configuration section**

In the "every key, with its default" YAML block, append after the `onepassword:` block:

```yaml
launch:
  auto_worktree: false
```

Add to the bullet list below it:

```markdown
- `launch.auto_worktree` — consumed by `claudemux`, not the TUI. When `true`, a launch
  in a git repo's main checkout **on its default branch** passes `--worktree` to
  `claude`, so the session works in an isolated worktree and the checkout stays
  pristine. Feature branches, detached HEADs, existing worktrees, and non-repos are
  left alone. Override per launch with `claudemux -w` (force) / `-W` (skip), or per
  project with `worktree: true|false` in `.project.yml`. Default `false`.
```

- [ ] **Step 2: README `.project.yml` section**

Extend the sample block:

```yaml
color: blue          # tmux status-bar / iTerm2 tab color
name: my-project      # passed to `claude -n`
worktree: true        # opt this project in/out of auto --worktree (optional)
op_env: abcdefghijklmnopqrstuvwxyz  # 1Password Environment ID (optional)
op_account: my.1password.com        # 1Password account for op_env (optional)
```

- [ ] **Step 3: `.project.yml.example`**

Append after the `name:` entry:

```yaml
# Auto-worktree override for this project. `true` opts in even when
# launch.auto_worktree is off globally (the default-branch heuristic still
# applies); `false` exempts this project. Omit to follow the global setting.
# worktree: true
```

- [ ] **Step 4: Commit**

```bash
git add README.md .project.yml.example
git commit -m "docs: document launch.auto_worktree, -w/-W, and .project.yml worktree"
```

---

### Task 4: Rollout on this machine (order matters)

**Files:**
- Modify: `~/.config/claudemux/config.yml` (NOT in the repo — machine config, no commit)

**Interfaces:**
- Consumes: the new binary's `config get launch.auto_worktree`.

- [ ] **Step 1: Install the new binary FIRST**

```bash
cd /Users/michael/Projects/claudemux && go install ./cmd/claudemux-head
```

The currently installed binary's strict `KnownFields` parser treats `launch:` as an unknown key — writing the config first would make every launch report a broken config (exit 3) until the binary lands.

- [ ] **Step 2: Verify the key resolves (still default)**

Run: `claudemux-head config get launch.auto_worktree`
Expected: `false` (exit 0).

- [ ] **Step 3: Enable it in Michael's config**

Append to `~/.config/claudemux/config.yml` (use Edit on the existing file — it has hand-written comments; do not rewrite it):

```yaml

# Launch claude with --worktree automatically when starting on a repo's
# default branch. Override per project (`worktree:` in .project.yml) or per
# launch (claudemux -w / -W).
launch:
  auto_worktree: true
```

- [ ] **Step 4: Verify end to end**

```bash
claudemux-head config get launch.auto_worktree   # expect: true
bash -n bin/claudemux                             # still syntactically clean
go test ./cmd/claudemux-head/                     # full suite green
```

Also confirm the decision path without launching tmux: run the Task 2 Step 6 `worktree_requested`-style check mentally or note that the claudemux repo itself is on `main`, so the NEXT real `claudemux` launch of any default-branch project will get `--worktree`. (This session's own tmux panes are unaffected — the change only applies to new launches.)

- [ ] **Step 5: No commit**

Machine config only. Done.
