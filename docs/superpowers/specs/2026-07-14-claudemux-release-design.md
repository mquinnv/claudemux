# claudemux: rename + zero-dependency install — design

**Date:** 2026-07-14
**Status:** approved
**Supersedes naming in:** `2026-07-14-open-source-release-design.md`

## Goal

Rename the package to **claudemux** and make it installable without a Go toolchain, through three channels that share one set of release artifacts. Registering the Claude Code hook must be automatic on every channel — no user ever hand-edits `~/.claude/settings.json`.

## Context

The project shipped this morning as `github.com/mquinnv/claude-env` (public, MIT, single-commit history). It is two programs that ship together: a bash tmux launcher and a Go TUI status pane. Today it can only be installed with `go install` plus manual symlinks plus a hand-pasted JSON hook. That is three barriers too many.

`claude-env` is unusable as an npm name (taken), and `cmux` is unusable everywhere: taken on npm, already a Homebrew core formula (the 24k-star Ghostty terminal), and **`craigsc/cmux` is already "tmux for Claude Code"** — the same product under the same name. `claudemux` is free on npm, free in Homebrew core, and has no meaningful GitHub collision.

## 1. Naming

| Thing | Was | Becomes |
|---|---|---|
| Repo | `mquinnv/claude-env` | `mquinnv/claudemux` (renamed in place; GitHub redirects) |
| Go module | `github.com/mquinnv/claude-env` | `github.com/mquinnv/claudemux` |
| Go program dir | `cmd/claude-head/` | `cmd/claudemux-head/` |
| Launcher (bash) | `bin/claude-env` | `bin/claudemux` |
| TUI (Go) | `claude-head` | `claudemux-head` |
| Config dir | `~/.config/claude-env/` | `~/.config/claudemux/` |
| Env override | `CLAUDE_HEAD_ENV` | `CLAUDEMUX_ENV` |
| Hook script | `hooks/claude-head-map.sh` | `hooks/claudemux-map.sh` |
| Pane-map dir | `~/.claude/claude-head/panes` | `~/.claude/claudemux/panes` |

The hook script and the pane-map dir must change **together**: the hook writes the map and `panemap.go` reads it. If they disagree, `claudemux-head` silently falls back to most-recently-active-session detection, which is wrong whenever two sessions share a project — a failure with no error message.

`config.yml`'s schema is unchanged (`summary.*`, `onepassword.*`). The `summary.api_key_file` default becomes `~/.config/claudemux/env`.

## 2. Release artifacts

One GitHub Actions workflow, triggered on tag `v*`, is the **single source of every install channel**. No channel builds from source.

- Cross-compile `claudemux-head` with `CGO_ENABLED=0` for: `darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64`. (No Windows: the tool requires tmux and bash.)
- Per platform, produce `claudemux_<version>_<os>_<arch>.tar.gz` containing:
  ```
  claudemux-head              # the Go binary
  claudemux                   # the bash launcher
  project-color-resolve.sh    # MUST be a sibling of claudemux
  claudemux-map.sh            # the Claude Code hook
  LICENSE  README.md
  ```
- Publish `SHA256SUMS` alongside them and attach everything to the GitHub Release.

`claudemux` locates `project-color-resolve.sh` via `dirname "$(readlink -f "$0")"`. Every channel must therefore keep the two adjacent; a channel that installs only the launcher breaks project colors silently (the `[ -f "$resolver" ] || return 0` guard makes it a no-op, not an error).

## 3. Install channels

### 3.1 Homebrew (primary)

A formula `claudemux.rb` added to the existing `mquinnv/homebrew-tap` (which already carries `Formula/warpclip.rb`).

```ruby
depends_on "tmux"
depends_on "jq"
depends_on "git"
```

This is Homebrew's unique advantage: the dependency table in the README becomes *enforced* rather than documented. The formula installs `claudemux-head` and `claudemux` into `bin`, keeping `project-color-resolve.sh` beside the launcher in `libexec` and symlinking accordingly.

### 3.2 `install.sh` (canonical, zero-dependency)

`curl -fsSL https://raw.githubusercontent.com/mquinnv/claudemux/main/install.sh | sh`

- Detects OS/arch, resolves the latest release, downloads the matching tarball.
- **Verifies the SHA256 against `SHA256SUMS` and aborts on mismatch.** A `curl | sh` installer that skips checksum verification is a supply-chain hole; this one does not.
- Installs to `$CLAUDEMUX_PREFIX` (default `~/.local/bin`), creating it, and warns if it is not on `PATH`.
- Runs `claudemux-head hook ensure` (§4).
- Idempotent: re-running upgrades in place.
- POSIX `sh`, not bash — it must run before we control the environment.

### 3.3 npm

Package `claudemux` (unscoped, free). `postinstall` fetches the same tarball for the host platform; `bin` exposes `claudemux` and `claudemux-head`. `npm i -g claudemux` is a real install; `npx claudemux <dir>` runs the launcher.

Documented caveat: `--ignore-scripts` environments will not get the binary.

## 4. Hook registration — `claudemux-head hook ensure`

A new Go subcommand, sibling to the existing `config get`.

It merges two entries — `SessionStart` and `UserPromptSubmit`, both invoking the installed `claudemux-map.sh` — into `~/.claude/settings.json`.

Contract:

- **Idempotent.** Present already → no write, exit 0, silent.
- **Never clobbers.** Existing hooks on those events, and every unrelated key in the file, are preserved. Decode into `map[string]any`, mutate only the two arrays, re-encode.
- **Backs up before writing** (`settings.json.bak-<unix>`).
- **Refuses rather than destroys.** If `settings.json` exists but does not parse, print the error and exit non-zero, changing nothing. Overwriting a corrupt-but-recoverable settings file with our own would be the worst possible outcome.
- Missing file or missing `~/.claude` → create both.
- Exit codes: `0` present-or-installed, non-zero on refuse/error.

**It is Go, not bash+jq, deliberately.** Preserving unknown fields through a read-modify-write of someone's live editor config is exactly where a `jq` one-liner quietly drops data.

### 4.1 Two invocation points

1. `install.sh` and the npm `postinstall` call it directly.
2. **The `claudemux` launcher calls it at startup**, where it is a no-op read when the hook is already present.

The second is not redundant. **Homebrew cannot safely write to `~/.claude`** — formulas must not mutate user config — so without the launcher-side check, every brew user would silently run with no hook and get wrong pane binding. This is the only mechanism that makes "no manual hook entries" true on all three channels.

## 5. README

Delete the hand-pasted `settings.json` JSON block entirely; it is replaced by §4. Lead with `brew install`, then `curl | sh`, then `npx`. The dependency table stays (it is still true for the non-brew channels) but notes that Homebrew enforces it.

## 6. Testing

**Go** (`hook_test.go`) — the file being mutated is the user's live Claude Code config, so these are the highest-value tests in the change:
- fresh install (no `~/.claude`) → both events registered
- already present → byte-identical file, no backup written
- **existing unrelated hooks on the same events → preserved, ours appended**
- **unrelated top-level keys (`model`, `permissions`, …) → preserved through the round-trip**
- malformed JSON → non-zero exit, file untouched, no backup
- backup written before any modification

**`install.sh`** — `shellcheck`; install into a scratch `HOME`/`CLAUDEMUX_PREFIX` and assert both binaries land, the launcher and resolver are siblings, and the hook registers. Assert a **corrupted checksum aborts the install**.

**Formula** — `brew install --formula` from the real published tarball, then `claudemux-head config get summary.model`.

**npm** — `npm pack`, install the tarball into a temp prefix, assert both bins resolve.

**Rename regression** — a grep gate asserting no tracked file still says `claude-env`, `claude-head`, or `CLAUDE_HEAD_ENV`.

## 7. Michael's machine (migration)

Not agent-automatable end-to-end; enumerated so nothing is missed:

- 1Password Environment mount: `~/.config/claude-env/env` → `~/.config/claudemux/env`
- `~/.config/claude-env/config.yml` → `~/.config/claudemux/config.yml` (org→account map lives here, not in the repo)
- `~/.local/bin/claude-env` symlink → `claudemux`
- fish function `claude-env` → `claudemux`
- `~/.claude/settings.json`: old `claude-head-map.sh` entries replaced (the new `hook ensure` adds the new ones but will not remove the old — that is a manual delete, or the old hook keeps writing a pane map nobody reads)
- `~/go/bin/claude-head` — stale, delete

## Out of scope

- Windows support (the tool requires tmux + bash).
- Publishing to Homebrew *core* (the tap is sufficient and avoids core's notability bar).
- Auto-update / self-update.
- Migrating a user's existing `~/.config/claude-env` automatically. The project is hours old with one known user; a migration shim would be dead code by next week.
