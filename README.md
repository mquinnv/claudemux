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

## The switchboard

`claudemux switch` opens a **switchboard**: a full-screen lobby session that watches
every claudemux session and automatically carries your tmux client to whichever one
is waiting on input — Claude's turn ended, or it asked you a question — oldest first.
Answer, and it moves you to the next waiting session; when nothing waits, you're
returned to the lobby.

It never fights you for the client: switch away manually and it pauses until you
come back to the lobby. A session you deliberately walk away from isn't re-queued
until it starts waiting again for a new reason.

The iTerm2 tab color follows you as you're carried between sessions — each
session paints its project color, and sessions without one (the lobby included)
reset the tab to the terminal default.

To **skip** a session you don't want to answer right now, jump back to the lobby —
`claudemux switch` binds `prefix + S` to do exactly that (unless you've bound
`prefix + S` yourself, in which case it's left alone). The skipped session is
snoozed and the conductor carries you to the next waiting one.

The lobby is a dispatch point: whenever you're parked there and something waits,
you get carried to it. To sit and watch the fleet instead, press `Space` — it
toggles **standby**, which keeps the states live but never dispatches, until you
press `Space` again.

Each session gets two lines: state, timer, a context-usage meter, and its
live Haiku-generated topic on the first, and a dimmer second line underneath
with the running summary and last prompt (`summary · prompt`, either half
omitted if empty, the whole line omitted if both are).

Keys in the lobby: `Space` toggles conducting/standby, `j`/`k` select, `Enter`
jumps to a session (and pauses conducting), `q` quits.

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
| `jq` | `hooks/claudemux-map.sh`, `hooks/claudemux-worktree.sh` | the hook exits silently; see below |
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

A second hook, `hooks/claudemux-worktree.sh`, ships and is registered the same way —
see `launch.auto_worktree` below for what it does. `claudemux-head hook ensure` installs
and repairs both scripts together.

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
  in a git repo's main checkout **on its default branch** marks the session as wanting a
  worktree rather than creating one at launch: `claudemux` prefixes both the `claude`
  command and the head pane's command with `env CLAUDEMUX_WORKTREE_PENDING=1`. The
  worktree itself doesn't exist yet at that point — `hooks/claudemux-worktree.sh` (a
  `UserPromptSubmit` hook) asks the model, on the session's first prompt, to call
  `EnterWorktree` with a name derived from the task: 2-5 words, lowercase,
  dash-separated. That's why worktrees are now named things like
  `rename-worktrees-on-topic` instead of `lovely-wandering-lovelace` — the model, not
  claudemux, is naming them, and it has the task in front of it. The worktree therefore
  appears during the session's first response, not at launch: a session that's opened
  and never prompted gets none. If the model skips the call, the status pane shows
  `⚠ no worktree` once the first turn ends, and the hook asks again on the next prompt —
  but **at most twice per session**, so declining it (or a `EnterWorktree` that refuses on
  a dirty tree) doesn't nag you for the rest of the session. Feature branches, detached HEADs, existing
  worktrees, and non-repos are left alone. Override per launch with `claudemux -w`
  (mark the session regardless of config or repo state) / `-W` (never mark), or per
  project with `worktree: true|false` in `.project.yml`. Default `false`. `-w`/`-W`
  (and the config/`.project.yml` toggles) only take effect on newly created sessions —
  `claudemux -w <existing-session>` attaches without marking it, silently ignoring
  `-w`, the same way name/color only apply at creation. Combine with `-n` to force a
  new session if you need `-w`/`-W` to take effect.
- `teardown.command` — the wrap-up command the status pane types into the `claude`
  pane when you press `x`, and the one it watches for when you type it there yourself
  (see **Tearing down a session** below). Default `/done`. Set it to `""` to skip that
  step, making `x` a gated exit-and-kill — with nothing left to watch for either.

**An unknown key in `config.yml` is a startup error, not a silent no-op.** A typo like
`sumary:` fails loudly at launch instead of quietly behaving as if you'd written nothing.
A missing file is fine — that's just defaults.

## `.project.yml`

`claudemux` reads an optional `.project.yml` from the root of the project directory you
launch it in. See [`.project.yml.example`](.project.yml.example) for the full format:

```yaml
color: blue          # tmux status-bar / iTerm2 tab color
name: my-project      # passed to `claude -n`
worktree: true        # opt this project in/out of the auto-worktree marker (optional)
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

**Worktree names.** When the head *observes* a session move from outside a worktree
into one — the signature of `hooks/claudemux-worktree.sh` having just created one — the
tab takes that worktree's name, dashes rendered as spaces, ahead of any Haiku label. It
holds until the summarizer's topic replaces an already-established one, at which point
Haiku's label takes over the tab for good. A session already sitting in a worktree when
the head started never counted as a transition, so it skips straight to Haiku's label.

**Pinning the tab.** Click the status pane and press `r` to put the tab back the
way it launched: the window name returns to the project's `name:` (or the
session name), and the tmux status bar, active pane border, and iTerm2 tab color
are repainted from `color:`. The tab then stays put — summaries keep running,
but they stop renaming the window — and the status pane shows `⬚ pinned`. Press
`r` again to hand control back; the current label is re-applied straight away.

Sessions cloned with `-n` share one `.project.yml`, so `remix-2` restores to
`Remix 2` rather than colliding with `remix`'s `Remix`.

**Refreshing the summary.** Summaries normally land on their own, when a turn
ends, and no more often than `summary.min_interval`. Click the status pane and
press `s` to run one now — the interval is skipped, because a refresh you asked
for should not quietly do nothing just because an edge fired seconds ago. While
the call is out the pane shows `⟳ summarizing`; an armed teardown takes that
slot back, since it is the one that needs an answer. `s` does nothing when there
is no API key, and a second press while a call is in flight is ignored rather
than billed twice. A pinned tab stays pinned: the summary and topic refresh, the
window name does not.

**Restarting the status pane.** Press `R` (capital) to restart it in place. The
process re-execs the `claudemux-head` binary as it stands on disk right now, so
this is how you pick up a rebuild or an edited `config.yml` without touching the
session — the pane keeps its size and position, and `claude` is never disturbed.

`q`, `ctrl+c` and `esc` quit instead, and quitting **closes the pane**: the head
is the pane's program, and a clean exit is not the failure that
`remain-on-exit` keeps a pane open for. Nothing else is harmed — `claude` and
the shell pane carry on — but the only way back is a new session, which is what
`R` exists to avoid. An armed teardown does not survive a restart; it disarms,
the same as it does across any other head restart.

### Tearing down a session

When the work is finished, click the status pane and press `x`. It runs the whole
wrap-up in order:

1. The first press types `teardown.command` (`/done` by default) into the `claude`
   pane and submits it. Answer whatever it asks exactly as you would have by hand.
   The status pane shows `⏻ wrapping up…`.

   You can also just **type the wrap-up command in the `claude` pane yourself** — the
   status pane notices and arms the same watch, showing `⏻ watching your wrap-up…`.
   Nothing is re-typed, so the wrap-up runs once; the rest of the sequence is identical,
   and step 3 still needs a real key press. This is what stops a hand-typed `/done` from
   ending with the session sitting in a deleted worktree, and stops the `x` you would have
   pressed afterwards from running the whole wrap-up a second time.

   It matches the command's own name, so `/done`, `/ameriglide-core:done` and any other
   plugin-qualified spelling all count (Claude Code rewrites slash commands, so the
   transcript rarely holds the bare form). `/done-something` and `/undone` are different
   commands and are ignored. Typing it arms the watch in **every** session, worktree or
   not — what differs between the two is the evidence the ready gate demands before it
   will offer the second press (see step 2).
2. Once the wrap-up has actually reached `claude` and the turn has ended, the pane checks
   whether it's safe to offer the kill, and what "safe" means depends on the session:
   - **Working in a worktree**: the session's worktree must be gone. If the wrap-up
     bailed — uncommitted work, unpushed commits, you declined — the worktree is still
     there, so the gate never opens and the pane says `⏻ worktree still present` instead.
   - **Not in a worktree**: there's no worktree deletion to check, so the gate instead
     holds the wrap-up to its own success bar — a clean tree and nothing unpushed. A
     dirty or unpushed tree blocks with the reason named right in the chip:
     `⏻ blocked (dirty tree)`, `⏻ blocked (unpushed)`, `⏻ blocked (no upstream)`.

   Either way, once the gate opens the pane shows `⏻ press x to tear down`. `esc`
   dismisses a blocked or ready teardown the same as any other in-progress one (see
   below). A ready gate is also re-checked against anything typed afterwards: if you
   keep working past the point it opened, the next prompt that isn't the wrap-up command
   itself drops the teardown back to idle instead of trusting evidence that may no longer
   be true — the pane says `⏻ session resumed`.
3. The second press — `x` at the `⏻ press x to tear down` chip — sends `/exit`, shows
   `⏻ exiting claude…` while it waits for `claude` to actually be gone, and then kills
   the tmux session.

The worktree the gate watches is the one **the session's working directory is in** — the
cwd from its transcript. `claudemux -w` (or `launch.auto_worktree`, or `worktree: true`
in `.project.yml`) only marks the session as wanting one; it's the model, prompted by
`hooks/claudemux-worktree.sh`, that actually calls `EnterWorktree` and moves the
session's cwd there, typically during its first response. The gate reads that cwd
regardless of when the move happened, so it holds just as well for a worktree entered
mid-session as it would have for one that existed at launch. That is not where the
status pane itself was started, so the check holds for the common case of a
session that ends up working in a worktree.

It follows the cwd, though, not the work. A session whose cwd stays in the main checkout
while it drives a worktree by explicit path (`git -C <worktree> …`) gets **no** worktree
verification — the gate falls back to the non-worktree bar (clean tree, nothing
unpushed) against the main checkout, even though the status line may show a worktree
chip for it. The chip tracks where the commands are going; the gate tracks where the
session is sitting.

`esc` cancels a teardown in progress. **It does not undo anything** — by then the
wrap-up command has already run; cancelling only stops the status pane from driving
the rest.

Nothing here is silent. Every abort names its reason on the status line —
`⏻ wrap-up didn't submit` (the command never reached `claude`),
`⏻ claude didn't exit` (it was still running after 15 seconds, so the session was
left alive), `⏻ no claude pane`, `⏻ session rotated` (the pane re-bound to a different
session mid-teardown, so what was armed no longer applies), `⏻ session resumed` (new
work landed after the ready gate opened, so it was withdrawn rather than trusted).

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
