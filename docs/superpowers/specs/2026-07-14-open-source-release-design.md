# claude-head: open-source release — design

**Date:** 2026-07-14
**Status:** approved

## Goal

Publish `claude-head` as an open-source project under `github.com/mquinnv/claude-head` (MIT). Four workstreams: relocate configuration to an XDG-compliant path, bring the `claude-env` tmux launcher into the repo, strip machine- and employer-specific references from tracked files, and write a README that lets a stranger install and run both.

## Scope: two programs, one repo

`claude-env` (currently an untracked bash script at `~/.local/bin/claude-env`) is the tmux launcher that *creates* the claude-head pane. The two are useless apart: claude-env exists to lay out a session containing claude-head, and claude-head is documented as "run it in a tmux pane" that only claude-env knows how to build. They ship together.

Repo layout after this work:

```
claude-head/
  *.go                       # the TUI (package main)
  bin/claude-env             # the tmux launcher (bash)
  bin/project-color-resolve.sh   # vendored from ~/.tmux/, sourced by claude-env
  hooks/claude-head-map.sh   # existing Claude Code hook
  README.md  LICENSE
```

## 1. Configuration

Configuration splits by sensitivity. Non-secret settings live in YAML; the API key stays in an env file, because the env file is served in production by a 1Password-mounted FIFO and a FIFO cannot serve a structured config format.

### 1.1 `config.yml`

A single `configDir()` resolves claude-head's config directory, per XDG: `$XDG_CONFIG_HOME/claude-head` when that variable is set and non-empty, otherwise `~/.config/claude-head`. `XDG_CONFIG_HOME` *replaces* the default — it is not a first entry in a search path.

`configDir()` is hand-rolled and must **not** use `os.UserConfigDir()`, which returns `~/Library/Application Support` on macOS — the wrong place, and wrong on the primary development machine.

The config file is `configDir()/config.yml`.

```yaml
summary:
  enabled: true             # default true
  model: claude-haiku-4-5   # default
  min_interval: 20s         # default; Go duration string

# Consumed by bin/claude-env, not by the TUI. Ships EMPTY.
onepassword:
  default_account: ""       # used when no org match; empty = let `op` pick
  accounts: {}              # GitHub org -> 1Password account, e.g.
                            #   acme: acme.1password.com
```

A new `config.go` owns a `Config` struct and a `loadConfig()` that returns it. `main.go` calls `loadConfig()` once at startup and threads the result into `newModel`.

### 1.1.1 `claude-head config get <dotted.path>`

`claude-env` is bash and needs to read `onepassword.accounts.<org>` — a *nested* lookup its existing flat-`sed` parser cannot do. Rather than teach bash to parse YAML (or take a `yq` dependency), the Go binary grows a subcommand:

```
claude-head config get onepassword.accounts.acme   # -> acme.1password.com
claude-head config get onepassword.default_account # -> (empty)
```

Exit-code contract, relied on by `claude-env`:

| Code | Meaning | Output |
|---|---|---|
| 0 | Path resolved to a scalar | Value on stdout |
| 1 | Absent key | **Nothing** on stdout or stderr — an org the user has not configured is normal, not an error worth printing |
| 2 | Usage error (wrong arg count) | Usage on stderr |
| 3 | `config.yml` exists but does not parse | Parse error on stderr |

3 is distinct from 1 on purpose. Callers wrap this in `$(... || true)` so an absent key does not trip `set -e`; without a separate code, a genuinely broken config would be laundered into silence and surface as a mysterious secrets-less launch rather than an error.

A path that resolves to a *map* rather than a scalar is exit 1 — there is no single printable value.

This keeps exactly one YAML parser in the project. `claude-env` already hard-depends on the `claude-head` binary being on `PATH` (it launches it), so this adds no new dependency.

Invocation shape: the existing `-session` flag stays as-is for the default (TUI) mode; `config` is a bare first-arg subcommand checked before `flag.Parse()`.

Error handling:

- **File absent** → not an error. Return defaults. This is the common case for a fresh install and must stay silent.
- **File present but unparseable** (bad YAML, bad duration string, unknown key) → fatal. Print the path and the parse error to stderr, exit non-zero. Running on defaults after a typo would silently ignore what the user asked for, which is worse than refusing to start.
- **Partial file** → absent keys take their defaults. Setting only `summary.model` must not reset `summary.enabled` to the zero value; defaults are populated into the struct *before* unmarshalling so YAML overwrites only what it names.

`summary.enabled: false` disables the summarizer outright: no client is constructed even when an API key is present.

Dependency: `gopkg.in/yaml.v3`.

`min_interval` needs a custom type. `yaml.v3` does **not** decode `"20s"` into a `time.Duration` — a `Duration` is an `int64`, so it would reject the string. The config defines a `Duration` wrapper implementing `UnmarshalYAML` (via `time.ParseDuration`) and `MarshalYAML` (via `String()`); the marshal half is needed by `config get`, which round-trips the struct through YAML to resolve dotted paths.

### 1.2 Env file (secrets)

`env.go` keeps its existing timeout, retry, and FIFO-contention logic verbatim. Only the path list changes.

`claudeHeadEnvPaths()` returns, highest precedence first:

1. `$CLAUDE_HEAD_ENV` — when set, this is the *only* path consulted (existing behavior; it must beat the default outright, not merely be tried first)
2. `configDir()/env` — i.e. `$XDG_CONFIG_HOME/claude-head/env` or `~/.config/claude-head/env`

The `~/Projects/claude-head/.env` fallback is **deleted** — it is hardcoded to one developer's machine.

Note the goroutine-leak arithmetic in `readEnvFileValue`'s comment ("worst case is 4 parked goroutines") assumes two default paths. Dropping to one default path makes the worst case 2. The comment must be corrected, not left to rot.

Value precedence is unchanged: the process environment beats the env file (`configValue` in `summary.go`).

Recognized keys: `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`.

## 2. Sanitization

### 2.1 Files removed from the repo

| Path | Why |
|---|---|
| `.project.yml` | This repo's *own* instance of claude-env's per-project config. Contains `op_env: z2flv6tntbjgjxs7ooys3o3s6i`, a live 1Password environment UUID. The **format** is documented in the README; this **file** is one user's values and must not ship. |
| `docs/superpowers/` (7 tracked files) | Process artifacts. Contain `/Users/michael/...` paths, internal ticket names, and describe a predecessor tool ("plan-monitor"). |

Both are deleted from tracking and added to `.gitignore`, alongside `.superpowers/` (already untracked) and `.claude/settings.local.json` (untracked but unignored — one `git add -A` from being committed).

`.project.yml` being gitignored is deliberate and slightly unusual: it is a config file users are *encouraged* to create, but every checkout's copy is personal (it names a 1Password Environment UUID). The README documents the format and ships a `.project.yml.example`. Gitignoring it also protects every *downstream* user of claude-env from committing their own `op_env` — the same trap this repo fell into.

The hook-installation instructions currently buried in `docs/superpowers/plans/2026-07-02-pane-session-binding.md` are the one piece of that tree worth keeping; they move into the README.

### 2.2 Test fixtures rewritten

Identifiers are replaced consistently across `tui_test.go`, `session_test.go`, and `events_test.go`:

- `/Users/michael/Projects/remix` → `/Users/alice/Projects/webapp`
- `crm-427-dataloader`, `crm-427` → `feature-branch`
- `/ameriglide-core:pickup` → `/myplugin:deploy`

These are cosmetic — no assertion semantics change. The encoded-path forms (`-Users-michael-Projects-remix--claude-worktrees-...`) must be rewritten to match their decoded counterparts or the path-encoding tests break.

### 2.3 `claude-env` (new to the repo)

The script moves from `~/.local/bin/claude-env` to `bin/claude-env`, with these changes and no others — the tmux layout logic, pane-pinning `window-resized` hook, zoxide resolution, and `-n` flag are all correct and stay verbatim.

**a. The org→1Password-account map is removed.** Today `op_account_for()` hardcodes:

```bash
case "$org" in
  ameriglide|phenixcrm) echo "ameriglide.1password.com" ;;
  inetalliance)         echo "team-iai.1password.com" ;;
  *)                    echo "whiteleaf.1password.com" ;;
esac
```

That is one employer's org structure. It is replaced by a lookup against the user's own config, with the same precedence (explicit `op_account` in `.project.yml` still wins):

```bash
op_account_for() {
  local work_dir="$1" acct origin org
  acct="$(project_field "$work_dir/.project.yml" op_account)"
  if [ -n "$acct" ]; then echo "$acct"; return 0; fi

  origin="$(git -C "$work_dir" remote get-url origin 2>/dev/null || true)"
  org="$(sed -n 's#.*github\.com[:/]\([^/]*\)/.*#\1#p' <<< "$origin")"
  if [ -n "$org" ]; then
    acct="$(claude-head config get "onepassword.accounts.$org" 2>/dev/null || true)"
    if [ -n "$acct" ]; then echo "$acct"; return 0; fi
  fi
  claude-head config get onepassword.default_account 2>/dev/null || true
}
```

An empty result is valid: `inject_op_env` then calls `op environment read "$env_id"` with **no** `--account` flag and lets `op` resolve its own default account. Michael's four entries move to his personal `~/.config/claude-head/config.yml` and out of the repo.

**b. `project-color-resolve.sh` is vendored.** claude-env currently sources `$HOME/.tmux/project-color-resolve.sh`, which exists on one machine. The file (52 lines, generic, no personal data) is copied to `bin/project-color-resolve.sh` and sourced relative to the script's own location:

```bash
resolver="$(dirname "$(readlink -f "$0")")/project-color-resolve.sh"
```

The existing graceful-degradation guard (`[ -f "$resolver" ] || return 0`) stays, so a broken install loses colors rather than failing to launch.

**c. iTerm2 tab coloring stays but is documented as iTerm2-only.** `apply_tab_color` writes iTerm2 proprietary OSC 6 sequences. Other terminals ignore unknown OSC sequences, so this is harmless elsewhere — no code change, just a README note. Removing it would cost Michael a feature he uses for no portability gain.

**d. `claude --permission-mode auto` stays.** It is a real default, not a personal one, and users who dislike it can edit the script.

### 2.4 Comments

`env.go` and `summary.go` reference "1Password-mounted FIFO" in explanatory comments. These stay: they document a real constraint the retry logic exists to satisfy, and 1Password is a public product, not an internal detail. The comments are rephrased to present it as *an example* of a FIFO-backed secret source rather than *the* one.

## 3. Release scaffolding

- **`go.mod`**: module path `claude-head` → `github.com/mquinnv/claude-head`. No import rewrites needed — the project is a single `package main` with no internal imports.
- **`LICENSE`**: MIT, copyright Michael Ventura.
- **`.project.yml.example`**: the documented format, with placeholder values and a comment that `.project.yml` is gitignored on purpose.
- **`README.md`**, covering:
  - What it is: a tmux status-bar "head" for Claude Code sessions, plus `claude-env`, the launcher that builds the session it lives in.
  - The pane model, with an ASCII diagram of the layout claude-env creates (head 4 rows top-left, claude below it, shell right at 30%).
  - Install: `go install github.com/mquinnv/claude-head@latest` for the binary; copy or symlink `bin/claude-env` and `bin/project-color-resolve.sh` onto `PATH` together (they must stay siblings — claude-env sources the resolver by relative path).
  - Dependencies and what degrades without each: **tmux** (required), **jq** (required by the hook), **git** (org inference), **zoxide** (optional — directory-query resolution), **`op`** (optional — 1Password Environments), **iTerm2** (optional — tab coloring; other terminals ignore the escape sequences).
  - The Claude Code hook setup, rescued from `docs/superpowers/plans/2026-07-02-pane-session-binding.md` (§2.1): symlink `hooks/claude-head-map.sh` into `~/.claude/hooks/` and register it on `SessionStart` + `UserPromptSubmit` in `~/.claude/settings.json`. Without it, claude-head cannot bind a pane to its sibling's transcript and falls back to most-recently-active-session detection.
  - Configuration reference: `config.yml` (all keys, defaults) and the env file.
  - `.project.yml` reference: `color`, `name`, `op_env`, `op_account` — and an explicit warning that `op_env` is an ID you should not commit.
  - An explicit note that summaries call the Anthropic API **against the user's own key and are billed to them**, roughly one Haiku call per 20s of active session, and that `summary.enabled: false` turns it off.
  - The **secret managers / 1Password** section (below).

### 3.1 README: secret managers (1Password)

A dedicated README section explaining that the env file is read as a *stream*, not required to be a regular file — so it can be a FIFO mounted by a secret manager, and the API key never lands on disk. It documents:

- The plain-file setup (`~/.config/claude-head/env`, mode 0600) as the baseline everyone can use.
- The 1Password setup: create an Environment in 1Password, mount it to `~/.config/claude-head/env`, and claude-head reads it per-launch. Alternatively `op run -- claude-head`, since the process environment beats the env file.
- Why the retry logic exists, in one sentence a user can act on: a FIFO serves one reader at a time, so several panes launching at once race on the open; claude-head retries with backoff, and losers succeed milliseconds later. This is the behavior to expect, not a bug to report.
- The failure mode: if the secret agent is locked or absent, opening the FIFO blocks. claude-head times out after 2s and runs without summaries rather than hanging the TUI.

This section is written to generalize — 1Password is the worked example, but any manager that can mount a FIFO or wrap the process (`env`-injecting CLIs) works the same way.

## 4. Testing

New `config_test.go`:

- missing file → defaults (`enabled: true`, `claude-haiku-4-5`, `20s`)
- partial file → named keys override, absent keys keep defaults (a file setting only `summary.model` must leave `enabled` true)
- malformed YAML → error
- invalid duration string → error
- unknown key → error (`KnownFields(true)`)
- `XDG_CONFIG_HOME` set → that path wins over `~/.config`

New `configget_test.go` (the `config get` subcommand):

- `onepassword.accounts.acme` on a config defining it → prints the account, exit 0
- absent path → prints nothing, exit 1
- path resolving to a *map* rather than a scalar → prints nothing, exit 1
- missing config file → prints nothing, exit 1 (not a crash — claude-env calls this on every launch)

Updated `env_test.go`:

- `claudeHeadEnvPaths()` with `CLAUDE_HEAD_ENV` set returns exactly that one path (existing test, unchanged)
- `claudeHeadEnvPaths()` without it returns the XDG path then the `~/.config` path, and no `~/Projects` path

`newModel` tests in `tui_test.go` gain a case asserting `summary.enabled: false` yields a nil summarizer even when `ANTHROPIC_API_KEY` is set.

`bin/claude-env` gets no automated tests — it is an interactive tmux launcher and the project has no bash test harness. It is verified by `bash -n` (syntax), `shellcheck`, and one manual launch. Adding a bats harness for one script is not worth it.

## Out of scope

- Configurable colors or tick interval (YAGNI — no demand, and the status bar is 1–3 lines).
- CI, release automation, packaging (Homebrew, etc.).
- Renaming the binary or changing the TUI.
- A bash test harness for `claude-env`.
- Porting `claude-env` off tmux, or supporting terminals other than iTerm2 for tab color (both degrade gracefully as-is).
