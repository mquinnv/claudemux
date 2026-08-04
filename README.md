# claudemux

A tmux workspace for [Claude Code](https://claude.com/claude-code) sessions. Two pieces
that ship together:

- **`claudemux`** — the launcher. Point it at a project and it builds a tmux session for
  it: `claude`, a shell, and a status pane above them.
- **`claudemux-head`** — that status pane. It tails the session's JSONL transcript and
  renders a compact bar showing session state (idle / working / waiting on you), token
  and rate-limit meters, the session's first and most recent prompt, and an
  LLM-generated one-line summary of what the session is doing right now.

You run `claudemux`; `claudemux-head` is what it draws.

## The pane model

`claudemux some-project` creates a tmux session laid out like this:

```
┌────────────────────────────┬──────────┐
│ claudemux-head   (4 rows)  │          │
├────────────────────────────┤  shell   │
│                            │  (30%)   │
│ claude                     │          │
│                            │          │
└────────────────────────────┴──────────┘
```

`claudemux-head` gets a fixed 4-row pane at the top left (it re-pins itself to 4 rows on
every resize), `claude` runs below it, and a shell takes the right 30% of the window.

The head and `claude` panes **run their program directly** — they are not shells with a
command typed into them, so a new session never shows a prompt or an echoed command. Two
consequences: exiting `claude` closes its pane instead of dropping you at a prompt (the
shell pane is there for that), and a pane whose program *fails* is kept on screen with its
error rather than vanishing.

In a project with an `op_env`, the `claude` pane is held by a waiting screen while
1Password decrypts the environment — a read takes around 25 seconds — and is then replaced
by `claude` itself, with the secrets in its environment. Pressing any key skips the wait
and starts `claude` immediately, *without* those secrets.

## Install

**Homebrew** (recommended — it installs `tmux`, `jq`, and `git` for you):

```bash
brew install mquinnv/tap/claudemux
```

**Shell** (no dependencies beyond `curl` and `tar`):

```bash
curl -fsSL https://raw.githubusercontent.com/mquinnv/claudemux/main/install.sh | sh
```

Installs to `~/.local/bin` (override with `CLAUDEMUX_PREFIX`). Verifies the release
checksum before installing.

**From source** (needs Go):

```bash
go install github.com/mquinnv/claudemux/cmd/claudemux-head@latest
```

That puts `claudemux-head` on your `PATH` (assuming `$(go env GOPATH)/bin` is on it), but
**only the status-pane binary** — the Go module doesn't include the `claudemux` launcher
or its color resolver. Clone the repo and symlink those separately:

```bash
git clone https://github.com/mquinnv/claudemux
mkdir -p ~/.local/bin
ln -s "$PWD/claudemux/bin/claudemux"               ~/.local/bin/claudemux
ln -s "$PWD/claudemux/bin/project-color-resolve.sh" ~/.local/bin/project-color-resolve.sh
```

`~/.local/bin` must be on your `PATH` — it does not exist by default on macOS, and
isn't on `PATH` in a default shell. Add it if it isn't there:

```bash
export PATH="$HOME/.local/bin:$PATH"   # in ~/.zshrc, ~/.bashrc, or config.fish
```

**`claudemux` and `project-color-resolve.sh` must remain siblings.** `claudemux`
sources the resolver by a path relative to its own location (`dirname` of its own
resolved path), not via `PATH`. Symlinking only one of them, or symlinking them into
different directories, breaks project-color resolution silently (it just no-ops).

## Dependencies

Homebrew installs and pins `tmux`, `jq`, and `git` for you. On the other channels
they're your responsibility:

| Tool | Required by | Without it |
|---|---|---|
| `tmux` | `claudemux` | `claudemux` cannot run at all |
| `claudemux-head` on `PATH` | `claudemux` | the head pane dies at "command not found" and is left on screen saying so; an `op_env` session shows that in place of its waiting screen, then still starts `claude` once the secrets land |
| `jq` | `hooks/claudemux-map.sh` | the hook exits silently; see below |
| `git` | `claudemux` (1Password org inference) | `op_account` in `.project.yml` or `onepassword.default_account` still work |
| `zoxide` | `claudemux` (fuzzy directory resolution) | `claudemux <query>` only works for literal directories, not `z`-style queries |
| `op` (1Password CLI) | `claudemux` (`op_env` injection) | sessions launch without injected secrets |
| iTerm2 | `claudemux` (tab coloring) | other terminals silently ignore the OSC escape sequences |

## The pane-map hook

`claudemux-head` follows the transcript of the `claude` process in its *sibling* pane. A
small Claude Code hook records which session lives in which tmux pane so it can do that.

**You do not need to install or configure this.** Every install channel registers it, and
`claudemux` re-checks at launch, so it is repaired automatically if it goes missing. It is
written to `~/.claude/settings.json` — existing hooks and settings are preserved, and a
backup is taken before any change.

Without the hook, `claudemux-head` falls back to picking whichever transcript in the
project directory changed most recently. That is wrong as soon as you have two Claude
Code sessions open on the same project.

## Configuration

`claudemux-head` reads `config.yml` from `$XDG_CONFIG_HOME/claudemux/` (default
`~/.config/claudemux/`). Every key, with its default:

```yaml
summary:
  enabled: true
  tab_title: true
  model: claude-haiku-4-5
  min_interval: 20s
  api_key_file: ~/.config/claudemux/env

onepassword:
  default_account: ""
  accounts: {}

launch:
  auto_worktree: false

teardown:
  command: /done
```

- `summary.enabled` — turn the LLM summary off entirely (see **Billing** below).
- `summary.model` — the Anthropic model used for summaries.
- `summary.api_key_file` — where the Anthropic credential is read from. A *path*, not
  the key itself: the file is read as a stream, so it can be a FIFO your secret manager
  mounts and the key never touches disk (see **Secrets** below). Point it anywhere;
  `~` expands.
  If the key can't be read at startup (a locked 1Password FIFO, say), the head
  keeps re-trying about once a minute for two hours and enables summaries as
  soon as a read succeeds.
- `summary.min_interval` — minimum time between summary calls for an *active* session.
  This is the only thing bounding what an active session costs you, so treat it as a
  spending control. `0` is legal and means *no floor* — every turn may fire a call.
  A negative value is rejected at startup (it would remove the limit, not set one).
  A summarize call that fails outright (API error, no reply) is retried on its
  own fixed 30s floor while the pane has no summary yet, independent of this
  setting.
- `summary.tab_title` — rename each session's tmux window (and thus the terminal
  tab) to the short Haiku `tab` label, so a row of tabs reads like a list of what
  each session is doing. Default `true`. Set `false` to keep the status-pane
  summary but leave the window/tab untouched. Independent of `summary.enabled`.
- `onepassword.default_account` / `onepassword.accounts` — consumed by `claudemux`, not
  by the TUI itself, to pick a 1Password account when injecting an `op_env`. Ships empty;
  see `.project.yml` below.
- `launch.auto_worktree` — consumed by `claudemux`, not the TUI. When `true`, a launch
  in a git repo's main checkout **on its default branch** passes `--worktree` to
  `claude`, so the session works in an isolated worktree and the checkout stays
  pristine. Feature branches, detached HEADs, existing worktrees, and non-repos are
  left alone. Override per launch with `claudemux -w` (force) / `-W` (skip), or per
  project with `worktree: true|false` in `.project.yml`. Default `false`. `-w`/`-W`
  (and the config/`.project.yml` toggles) only take effect on newly created sessions —
  `claudemux -w <existing-session>` attaches without a worktree, silently ignoring
  `-w`, the same way name/color only apply at creation. Combine with `-n` to force a
  new session if you need `-w`/`-W` to take effect.
- `teardown.command` — the wrap-up command the status pane types into the `claude`
  pane when you press `x` (see **Tearing down a session** below). Default `/done`.
  Set it to `""` to skip that step, making `x` a gated exit-and-kill.

**An unknown key in `config.yml` is a startup error, not a silent no-op.** A typo like
`sumary:` fails loudly at launch instead of quietly behaving as if you'd written nothing.
A missing file is fine — that's just defaults.

## `.project.yml`

`claudemux` reads an optional `.project.yml` from the root of the project directory you
launch it in. See [`.project.yml.example`](.project.yml.example) for the full format:

```yaml
color: blue          # tmux status-bar / iTerm2 tab color
name: my-project      # passed to `claude -n`
worktree: true        # opt this project in/out of auto --worktree (optional)
op_env: abcdefghijklmnopqrstuvwxyz  # 1Password Environment ID (optional)
op_account: my.1password.com        # 1Password account for op_env (optional)
```

**`.project.yml` is gitignored by this repo on purpose, and `op_env` should not be
committed** — it's an identifier that points at your secrets. Copy the example instead
of tracking the real file:

```bash
cp .project.yml.example .project.yml
```

## Appearance: project colors

The `color:` field in `.project.yml` drives two things at once, so a session is
visually identifiable at a glance:

- **The tmux status bar and active-pane border** for the session are tinted to that
  color (the foreground auto-picks black or white for contrast).
- **The terminal tab**, via iTerm2's tab-color escape sequence, is tinted to match — so
  the tab in your terminal and the session inside it share a color.

`color:` accepts a named color (`red blue green yellow purple orange pink cyan`) or a
`#rrggbb` hex value. With no `color:`, claudemux leaves tmux and the tab at their
defaults.

The tab coloring is **iTerm2-specific**. Other terminals silently ignore the escape
sequence — you still get the tmux status-bar color, just not the tab tint. Nothing
breaks; the color simply doesn't appear on the tab.

### Tab titles

The status pane's summarizer also produces a short 2–4 word label for the
session, and claudemux renames the tmux window to it — which the terminal shows
as the tab title. As the session's focus settles, the tab goes from the launch
default to something like `crm bundling`. Because the title comes from the tmux
window name, it also appears as the window label in the tmux status bar.

This needs no tmux configuration — claudemux sets `set-titles` itself. It applies
only inside tmux, and only while summaries are on; turn it off with
`summary.tab_title: false`. Outside tmux there is nothing to rename.

**Pinning the tab.** Click the status pane and press `r` to put the tab back the
way it launched: the window name returns to the project's `name:` (or the
session name), and the tmux status bar, active pane border, and iTerm2 tab color
are repainted from `color:`. The tab then stays put — summaries keep running,
but they stop renaming the window — and the status pane shows `⬚ pinned`. Press
`r` again to hand control back; the current label is re-applied straight away.

Sessions cloned with `-n` share one `.project.yml`, so `remix-2` restores to
`Remix 2` rather than colliding with `remix`'s `Remix`.

### Tearing down a session

When the work is finished, click the status pane and press `x`. It runs the whole
wrap-up in order:

1. The first press types `teardown.command` (`/done` by default) into the `claude`
   pane and submits it. Answer whatever it asks exactly as you would have by hand.
   The status pane shows `⏻ wrapping up…`.
2. Once the wrap-up has actually reached `claude`, the turn has ended, **and** the
   session's worktree is gone, the pane shows `⏻ press x to tear down`. If the wrap-up
   bailed — uncommitted work, unpushed commits, you declined — the worktree is still
   there, so the gate never opens and the pane says `⏻ worktree still present` instead.
3. The second press sends `/exit`, shows `⏻ exiting claude…` while it waits for
   `claude` to actually be gone, and then kills the tmux session.

The worktree the gate watches is the one **the session's working directory is in** — the
cwd from its transcript, which is where `claudemux -w` (or `launch.auto_worktree`, or
`worktree: true`) put it. That is not where the status pane itself was started, so the
check holds for auto-worktree sessions, which are the common case.

It follows the cwd, though, not the work. A session whose cwd stays in the main checkout
while it drives a worktree by explicit path (`git -C <worktree> …`) gets **no** worktree
verification — the gate falls back to turn-end alone, even though the status line may
show a worktree chip for it. The chip tracks where the commands are going; the gate
tracks where the session is sitting.

`esc` cancels a teardown in progress. **It does not undo anything** — by then the
wrap-up command has already run; cancelling only stops the status pane from driving
the rest.

Nothing here is silent. Every abort names its reason on the status line —
`⏻ wrap-up didn't submit` (the command never reached `claude`),
`⏻ claude didn't exit` (it was still running after 15 seconds, so the session was
left alive), `⏻ no claude pane`, `⏻ session rotated` (the pane re-bound to a different
session mid-teardown, so what was armed no longer applies).

Sessions whose cwd isn't in a worktree have no deletion to verify, so the gate opens as
soon as the wrap-up turn ends.

Outside tmux `x` does nothing at all: there is no pane to type into and no session to
kill.

## tmux notes

**claudemux needs no `~/.tmux.conf`.** It sets everything it depends on per session at
launch — the status-bar style, the split layout, and a `window-resized` hook that
re-pins the `claudemux-head` pane to exactly 4 rows on every resize (including on
attach). That hook exists specifically to cope with tmux's default `window-size latest`
behavior, which otherwise redistributes pane heights when a client attaches and would
shrink the head pane below 4 rows, clipping the status line. So the tool works out of the
box regardless of your tmux configuration; it doesn't read, require, or recommend any
particular settings.

The one hard requirement is that `tmux` is installed and on `PATH` (Homebrew installs it
for you; see [Dependencies](#dependencies)).

## Billing

Summaries are **calls to the Anthropic API made with your own `ANTHROPIC_API_KEY`,
billed to your account.** Roughly one Haiku call per `summary.min_interval` (20s by
default) of *active* session — none while the session is idle.

- Set `summary.enabled: false` in `config.yml` to turn this off entirely.
- If no API key is configured at all, the feature is simply off: `claudemux-head` falls
  back to showing the raw first/last prompt instead of a generated summary. Nothing is
  billed.
- **If you already have `ANTHROPIC_API_KEY` exported in your shell for other tools,
  claudemux-head will pick it up and start billing you without any separate opt-in.** Set
  `summary.enabled: false` if you don't want that.

## Secrets and secret managers

Two keys are recognized: `ANTHROPIC_API_KEY` and `ANTHROPIC_BASE_URL` (the latter for
users behind an Anthropic-compatible gateway). They are read from a plain `KEY=value`
env file, **separate from `config.yml`**, at the path `summary.api_key_file` names
(default `~/.config/claudemux/env`).

**Why a second file, rather than a key in `config.yml`?** Because the secret is read as a
byte *stream*, which lets it be a FIFO your secret manager mounts — so the key never
lands on disk. Secret managers serve dotenv, not YAML; an API key written as a YAML
scalar would have to be a real file in plaintext. `config.yml` therefore *names* the
secret's source instead of containing it.

Precedence, highest first:

1. The **process environment** — always wins. This is what makes `op run` work with no
   file at all.
2. `CLAUDEMUX_ENV`, if set — overrides the configured path *outright*; nothing else is
   consulted.
3. `summary.api_key_file` from `config.yml` (default `~/.config/claudemux/env`).

Three ways to provide the key:

**1. Plain file** — the baseline:

```bash
mkdir -p ~/.config/claudemux
cat > ~/.config/claudemux/env <<'EOF'
ANTHROPIC_API_KEY=sk-ant-...
EOF
chmod 0600 ~/.config/claudemux/env
```

One `KEY=value` per line, `#` comments allowed, no quoting.

**2. `op run`** — no file at all, since the process environment beats the file:

```bash
op run -- claudemux-head
```

**3. Mounted FIFO** (e.g. a 1Password Environment) — mount the Environment so it serves
its contents as a named pipe. `claudemux-head` reads the file as a byte stream, not as a
regular file, so the key never has to land on disk in plaintext.

If your secret manager already mounts the pipe somewhere else, point at it rather than
moving the mount:

```yaml
summary:
  api_key_file: ~/.config/some-other-place/env
```

Two behaviors are expected with a FIFO-backed env file and are not bugs:

- **Contention is expected.** A FIFO serves one reader at a time. If several
  `claudemux-head` panes start at once, they race on the open and the losers see an empty
  pipe. `claudemux-head` retries with backoff (50ms, 150ms, 400ms) and the losers succeed
  milliseconds later.
- **A locked agent degrades, it does not hang.** Opening a FIFO with no writer on the
  other end blocks forever. `claudemux-head` times out after 2 seconds and runs *without*
  summaries rather than freezing the TUI. The retries above do **not** multiply that
  wait: a read that times out means there is no writer at all, which retrying cannot
  fix, so it stops immediately. Only an empty-but-prompt read — real contention — is
  retried. Worst case is one 2-second timeout, not four.

## License

MIT — see [LICENSE](LICENSE).
