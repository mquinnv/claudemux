# Auto-worktree on launch

**Date:** 2026-07-28
**Status:** Approved

## Purpose

Add a setting that makes `claudemux` launch Claude Code with `--worktree` automatically
when it is appropriate, so sessions started on a repo's default branch get an isolated
git worktree without the user remembering to ask for one. Claude Code's own
`-w, --worktree [name]` does the actual worktree creation; claudemux only decides
whether to pass the flag.

## Decisions (from brainstorming)

- **Trigger:** repo on its default branch. A launch on a feature branch, in a non-repo
  directory, in a detached HEAD, or inside an existing linked worktree is left alone.
- **Default:** off. New config key defaults to `false`; existing users see no behavior
  change on upgrade.
- **Overrides:** per-project `.project.yml` and a per-launch CLI flag.

## Design

### 1. Config key (`cmd/claudemux-head/config.go`)

New top-level section:

```yaml
launch:
  auto_worktree: true   # default false
```

- `LaunchConfig` struct (`AutoWorktree bool`) added to `Config`; `defaultConfig()`
  leaves it `false`. No `validate()` addition — a bool cannot be malformed.
- `claudemux-head config get launch.auto_worktree` works via the existing YAML
  round-trip in `configget.go` (bools print as `true`/`false`); no changes there.
- Strict `KnownFields` decoding means a typo under `launch:` is a startup error,
  consistent with every other key.

### 2. Overrides and precedence (`bin/claudemux`)

Highest wins:

1. **CLI flag:** `-w` forces `--worktree` for this launch (bypasses the heuristic
   entirely); `-W` suppresses it for this launch. Passing both is a usage error.
   Added to the existing `getopts` loop alongside `-n`.
2. **`.project.yml`:** `worktree: true|false` read via the existing `project_field`
   helper. `true` opts the project into auto mode (the heuristic below still
   applies); `false` exempts the project. Absent falls through to the global key.
3. **Global:** `launch.auto_worktree` read via the existing `ch_config_get` helper,
   so a malformed `config.yml` is reported once and degrades to "off", matching the
   1Password key behavior.

### 3. Appropriateness heuristic (`bin/claudemux`, new function `auto_worktree_wanted DIR`)

Used only in auto mode (project or global opt-in; `-w` skips it). All must hold,
each a single fast git subprocess on the launch path:

- `git -C DIR rev-parse --is-inside-work-tree` succeeds and prints `true`;
- the checkout is the main one, not a linked worktree:
  `git -C DIR rev-parse --git-dir` equals `git -C DIR rev-parse --git-common-dir`;
- HEAD is not detached: `git -C DIR symbolic-ref -q HEAD` succeeds;
- the current branch is the default branch: the basename of
  `git -C DIR symbolic-ref -q refs/remotes/origin/HEAD` when that ref exists,
  else a `main`/`master` fallback.

If `git` is missing entirely the function returns false (no worktree) rather than
erroring; git is already a documented requirement, this just degrades gracefully.

### 4. Applying the flag

When the decision is "yes", append ` --worktree` (no name — claude auto-names it)
to `claude_cmd` in `create_session`. Both launch paths (direct `send-keys` and the
deferred 1Password path) already consume `$claude_cmd`, so no further changes.

`claudemux-head` needs **no changes**: worktree sessions are already detected from
pane cwd (`worktree.go`, commits f0d2153 / f2f2bd1), and the transcript-staleness
failure mode with `--worktree` sessions was fixed in July 2026.

### 5. Documentation

- README: document `launch.auto_worktree` (with default) in the config section and
  the `-w`/`-W` flags in the usage header; document `worktree:` in the
  `.project.yml` section.
- `.project.yml.example`: add a commented `worktree:` line.
- `bin/claudemux` header comment currently says worktrees are "intentionally NOT
  handled here" — update it to describe the new behavior.

## Error handling

- Malformed `config.yml`: `ch_config_get` already surfaces exit 3 once; the launch
  proceeds with auto-worktree off.
- Non-repo dir with `-w`: the flag is passed anyway; claude itself reports the
  error. Explicit force means the user owns the outcome.
- Missing git: heuristic returns false; launch proceeds normally.

## Testing

- Go: config default is false; a `launch: auto_worktree: true` document parses;
  `config get launch.auto_worktree` prints `true`/`false`; unknown keys under
  `launch:` still fail.
- Bash heuristic: manual verification of the four gate cases (non-repo, feature
  branch, detached HEAD, default branch) — `bin/claudemux` has no test harness.

## Rollout (Michael's machine)

Order matters: `go install ./cmd/claudemux-head` **before** adding

```yaml
launch:
  auto_worktree: true
```

to `~/.config/claudemux/config.yml` — the currently installed binary's strict
parser would treat the new key as a startup error (exit 3) and every launch would
report a broken config until the new binary lands.
