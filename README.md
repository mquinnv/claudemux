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
┌────────────────────────────────────────┐
│ claudemux-head                (5 rows) │
├─────────────────────────────┬──────────┤
│                             │          │
│ claude                      │  shell   │
│                             │  (30%)   │
└─────────────────────────────┴──────────┘
```

`claudemux-head` gets a fixed-height pane across the FULL width of the top (it re-pins
itself on every resize), `claude` runs below it, and a shell takes the right 30%
of the remaining width, spanning that remaining height — it sits beside `claude`, not
beside the head.

That 30% is the default, not a fixed quantity: `launch.shell_size` resizes the shell pane
and `launch.shell_command` gives it something to run instead of a bare prompt — both
settable per project. See **Sizing the shell pane** and **Running something in the shell
pane** below.

The head's height is not a setting of its own — it is however many rows its **context
rows** need. Four are always there (state line, meters, and the `topic`/`now` pair);
`head.show_last` adds the newest prompt below them and ships **on**, `head.show_first`
adds the session's opening prompt at the bottom and ships **off**. So a default session
gets 5 rows, and turning `show_first` on gets you 6 — the row and the line to draw it in
arrive together, with nothing to keep in step by hand. See **Configuration** below.

The head and `claude` panes **run their program directly** — they are not shells with a
command typed into them, so a new session never shows a prompt or an echoed command. Two
consequences: exiting `claude` closes its pane instead of dropping you at a prompt (the
shell pane is there for that), and a pane whose program *fails* is kept on screen with its
error rather than vanishing.

In a project with an `op_env`, the `claude` pane is held by a waiting screen while
1Password decrypts the environment — a read takes around 25 seconds — and is then replaced
by `claude` itself, with the secrets in its environment. Pressing any key skips the wait
and starts `claude` immediately, *without* those secrets.

Alongside the variables, the session gets `CLAUDEMUX_OP_ENV` set to the injected
Environment's id. A project whose scripts wrap commands in
`op run --environment <id> -- …` can check it to skip `op` — and its per-call 1Password
authorization prompt — when that exact environment is already loaded:

```sh
[ "$CLAUDEMUX_OP_ENV" = "<id>" ] && exec "$@"
exec op run --environment "<id>" -- "$@"
```

### Layouts

The arrangement above — `shell-right` — is the default of four layouts. Pick one with
`claudemux setup`, an interactive picker that shows all four as diagrams and writes the
choice to `launch.layout` in `config.yml`:

- **`shell-right`** (default) — head across the top, `claude` below it, shell to the right.
- **`no-shell`** — head across the top, `claude` below it, no shell pane at all.
- **`shell-bottom`** — head across the top, `claude` in the middle, shell full-width at
  the bottom.
- **`head-bottom`** — `claude` and the shell side by side on top, head full-width at the
  bottom.

The first time you launch `claudemux` — bare, or pointed at a directory — with no
layout chosen yet, `setup` runs automatically before the session opens. Cancelling it
(or a save failure) isn't fatal: the launch continues on `shell-right`, and you can run
`claudemux setup` again whenever you want to change it.

### Sizing the shell pane

`launch.shell_size` in `config.yml` sets how much of the split the shell pane takes —
`"40%"` of the space being divided, or `"80"` for an absolute count of columns or rows.
Unset means the layout's own default: 30% beside `claude`, 20% below it.

One setting, because there is one thing being sized. It's a *width* in `shell-right` and
`head-bottom` and a *height* in `shell-bottom` — the layout already decides which
dimension "how big is the shell pane" means, so a second key would always leave one of
the two inert. `no-shell` ignores it.

A project overrides it with `shell_size:` in its `.claudemux.yml`. A value that isn't a
positive count with an optional `%` is rejected: in `config.yml` at startup, by name, the
way every other bad key is; in a project file — which never passes through that check —
it's ignored, and the next source down applies, so one project's typo can't throw away
your global setting.

### Running something in the shell pane

`launch.shell_command` gives the shell pane a command to run at launch instead of coming
up at a bare prompt — a dashboard, a log tail, a watcher. It's a command *line*, so flags,
quoting and substitutions are yours to write:

```yaml
launch:
  shell_size: 40%
  shell_command: gh-hud
```

Set it per project with `shell_command:` in `.claudemux.yml`, which is where a
project-scoped invocation belongs:

```yaml
shell_command: gh-hud --repo mquinnv/claudemux
```

**The pane is still your shell.** The command runs, and when it exits — you quit it, it
finishes, it was never installed — you're left at a normal prompt in the project
directory, with whatever the command printed above it. Ctrl-C quits the command, not the
pane; the pane closes when you exit that shell, exactly as a shell pane always has.
Nothing is typed into a prompt to make this happen, so there's no echoed command to watch
scroll past at launch.

The command is parsed by `sh`, not by your login shell, so write it in POSIX syntax even
if you live in fish. And in a project with an `op_env`, it starts *after* the 1Password
unlock rather than at launch — around 25 seconds in, alongside `claude` — so that it comes
up with the secrets in its environment rather than without them.

## The switchboard

Run `claudemux` with no arguments to open a **switchboard** (`claudemux switch` does the
same thing, explicitly): a full-screen lobby session that watches
every claudemux session and automatically carries your tmux client to whichever one
is waiting on input — Claude's turn ended, or it asked you a question — oldest first
among the non-deferred, then oldest first among any deferred sessions (see **Deferring
a session** below).
Answer, and it moves you to the next waiting session; when nothing waits, you're
returned to the lobby — unless the session you're in is the only one there is, in
which case you stay put. A lobby whose entire fleet is the session you were just
carried out of has nothing to show you, so the conductor holds instead. The moment
a second session starts waiting, it collects you as usual.

It never fights you for the client: switch away manually and it pauses until you
come back to the lobby. A session you deliberately walk away from isn't re-queued
until it starts waiting again for a new reason.

Every escorted arrival is announced: a small popup pulls in a locomotive and
introduces the session you just landed in — what it's working on, its name, and
which model is driving at what context — closing after two seconds or on your
first keypress, so a switch is never a silent teleport.

```
      o  O
     o
    o       _____
   .][__n_n_|DD[  ====____
  >(________|__|_[_______]|
  _/oo OOOOO oo`  ooo  ooo
▌ Fix idle detection in background agents
remix-2 · escorted by claudemux
opus 4.7 · 42% context
```

The topic and the session name wear the project's color, so you recognize where
you've landed before you've read a word of it. Facts the session hasn't
published yet are left out rather than shown blank. Manual jumps (`Enter` on the
lobby) skip the banner — you chose where you were going.

The iTerm2 tab color follows you as you're carried between sessions — each
session paints its project color, and sessions without one (the lobby included)
reset the tab to the terminal default.

To **skip** a session you don't want to answer right now, jump back to the lobby —
`claudemux switch` binds `prefix + S` to do exactly that (unless you've bound
`prefix + S` yourself, in which case it's left alone). The skipped session is
snoozed and the conductor carries you to the next waiting one.

**Deferring a session** is different from a snooze: it says this session is waiting on
something outside claudemux — a review, a build, another person — and should sit at the
back of the queue until nothing else needs you. Press `d` on the selected row in the
lobby, or `d` from inside the session's own status pane, to toggle it. A deferred
session still shows up when it's genuinely the only thing waiting — with a small fleet
it may be the only session in the queue at all — but any non-deferred session that
starts waiting jumps ahead of it, and a busy session never blocks it either way. A
deferred row gets a `◆ ` marker and a ` DEFER ` badge in the lobby, and the session's own
status pane shows a `◆ defer` chip. Unlike a snooze, defer never clears itself — the
blocker is yours to resolve, not claudemux's to guess at — so it stays set until you
press `d` again.

The lobby is a dispatch point: whenever you're parked there and something waits,
you get carried to it. To sit and watch the fleet instead, press `Space` — it
toggles **standby**, which keeps the states live but never dispatches, until you
press `Space` again.

Under the title, the lobby shows the same account budget meters as the head:
the 5-hour and weekly rate-limit gauges with their reset times (and an
"empty in X" projection when usage is climbing), so you can see the account's
headroom without jumping into a session.

Each session gets two lines: state, timer, a context-usage meter, and its
live Haiku-generated topic on the first, and a dimmer second line underneath
with the running summary and last prompt (`summary · prompt`, either half
omitted if empty, the whole line omitted if both are).

Each session's **name is tinted with its project color** — the same `color:` that
paints its tmux status bar and terminal tab — so a fleet spanning several projects
sorts itself visually without you reading a single name. Only the name is tinted:
the state, timer, context and model columns keep their own meanings in color.
Sessions whose project declares no color render plain, and the selected row's
highlight always wins over the tint. Heads started before this existed publish no
color and render plain too, so a mixed-version fleet degrades one row at a time.

Below the fleet list, a bordered box previews the selected session's `claude`
pane — the same thing `tmux choose-tree -Zs` does for the session under the
cursor, so you can see what's actually happening before you jump to it. It
follows the selection (`j`/`k` refreshes it immediately, not just on the next
poll) and is read-only — nothing about previewing a session touches it. On a
pane too short to show both the box and the fleet, the box is dropped and the
lobby renders exactly as it would with no preview at all.

Press `n` to start a **new session** without leaving the lobby: the status
line becomes a prompt, you type a project directory, zoxide query, or project
name — the same thing you'd pass to `claudemux` on the command line — and `Enter` runs
the equivalent of `claudemux -n` on it. A leading `~` expands here even though
no shell is involved. The new session joins the list and
you're switched straight to it; `Esc` cancels. While the prompt is open (or
the launch is in flight) the conductor holds off dispatching you, so being
carried away mid-keystroke isn't a thing. Under the hood this uses
`claudemux -d`, which creates the session and prints its name instead of
attaching.

`Space` also works from any session's status pane — see *Toggling conduct mode*
below — so you can stop the conductor without coming back here first.

Keys in the lobby: `Space` toggles conducting/standby, `j`/`k` select, `Enter`
jumps to a session (and pauses conducting), `d` toggles defer on the selected
row, `Esc` returns to the session you came from (tmux's per-client last
session), `n` starts a new session, `q` quits.

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
| `jq` | `hooks/claudemux-map.sh`, `hooks/claudemux-worktree.sh`, `hooks/claudemux-ask.sh` | the hook exits silently; see below |
| `git` | `claudemux` (1Password org inference) | `op_account` in `.claudemux.yml` or `onepassword.default_account` still work |
| `zoxide` | `claudemux` (fuzzy directory resolution) | `claudemux <query>` resolves literal directories and `launch.project_dirs` matches, but not `z`-style frecency queries |
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
see `launch.auto_worktree` below for what it does.

A third hook, `hooks/claudemux-ask.sh`, tracks pending `AskUserQuestion` calls. Claude
Code does not write the question to the transcript until it is answered, so without this
hook a session sitting on a multiple-choice question reads as **Idle** or **Thinking** —
exactly the wrong verdict for the one session that most needs your attention. With the
hook, the head shows **Asking**, and the switchboard marks the session as waiting (dot,
highlight, auto-escort) just like an idle one.

Two caveats. An Esc'd question can keep reading as **Asking** until you send the next
prompt — nothing flushes to the transcript on Esc, so there's no signal available to clear
the marker any sooner; this is inherent to what Claude Code exposes, not a bug in the
hook. And a session that was already running when this hook was installed or upgraded
won't pick it up until its next start — hooks are snapshotted at session startup, so a
live session keeps running whatever it started with.

**Unsure** is Idle the head no longer trusts. Background work is counted from the
transcript and expires on a timer when nothing reports it finished; when that timer,
not a completion, is what emptied the count, the head shows **Unsure N** with an amber
dot and publishes `Unsure:N`. The switchboard does not treat it as waiting, so the
conductor will not escort you into a session whose pane may still say "2 shells still
running". It clears on your next prompt or Claude's next turn.

`claudemux-head hook ensure` installs and repairs all three scripts together.

## The statusline command and the account meters

The `5h` and `wk` gauges in the head and the switchboard come from Claude Code itself.
Claude Code hands its **statusline command** a JSON payload on every render, and that
payload carries the account's rate-limit windows — so claudemux ships one:
`claudemux-head statusline`, installed alongside the hooks as
`~/.claude/hooks/claudemux-statusline` and registered in the `statusLine` key of
`~/.claude/settings.json`. It prints nothing (whatever a statusline command prints
*becomes* your status line) and writes `~/.claude/claudemux/rate-limits.json`, which the
head panes read.

**`statusLine` is a single exclusive setting, so claiming it takes it from whoever had
it.** `claudemux-head hook ensure` therefore claims it only when it is *empty*, when it
is *already ours*, or when it is `abtop-statusline.sh` — the shim these meters used to
depend on. **Any other statusline you configured is left byte-for-byte alone**, a line
saying so is printed to stderr, and the account meters simply stay blank. Nothing here
will silently replace a status line you built.

Two consequences worth stating plainly:

- If you were using **abtop**'s statusline shim, claudemux takes the slot from it and
  `~/.claude/abtop-rate-limits.json` stops being refreshed. Running heads migrate to our
  cache on their own within a second; abtop's own TUI, if you still run it, would go
  stale. Removing that dependency is the point of the change, not a side effect — which
  is why the claim is narrowed to abtop's exact filename and announced rather than done
  quietly.
- If you keep your own statusline, everything else in claudemux works; only the account
  meters (`5h`, `wk`, and the per-model rows below) are unavailable.

`CLAUDEMUX_RATE_LIMITS_PATH` overrides the cache path for both the writer and the readers.

### Per-model weekly meters, and the `claude` process behind them

Max plans carry per-model weekly windows (a `fable` gauge, for example) that the
statusline payload does not include — Claude Code narrows it to the 5-hour and 7-day
windows before handing it over. The only place those rows exist is Claude Code's own
`get_usage` control request, so **claudemux runs a short-lived `claude` process to ask
for them**:

```
claude -p --input-format stream-json --output-format stream-json \
       --no-session-persistence --strict-mcp-config --mcp-config '{"mcpServers":{}}'
```

What that costs you, stated exactly:

- **~2.2 seconds, and it fires your `SessionStart` hooks** — the same ones any `claude`
  start fires. Suppressing them means isolating `CLAUDE_CONFIG_DIR`, which also loses the
  OAuth profile the answer depends on, so it is not an option.
- **No tokens and no inference.** No user message is ever sent; the probe that
  established this returned `total_cost_usd: 0` and an empty `model_usage`. It does not
  consume any of the limit it reports.
- **At most one spawn per 15 minutes for the whole machine**, not per pane: the answer
  goes in `~/.claude/claudemux/usage.json` behind a lock, and every other head and the
  switchboard read that file.

It also stops asking when there is nothing to be gained, and starts again by itself if
that changes:

- No account meters rendering at all (you kept your own statusline) — **no spawns**.
- `rate_limits_available: false`, i.e. that pane runs on an API key, Bedrock, or Vertex,
  or its profile lacks the scope — that pane stops asking. The verdict is about that
  pane's credentials, so it is never written to the shared cache and never affects the
  others.
- A plan with no per-model window at all — backs off to one attempt every few hours
  rather than one every 15 minutes.

`CLAUDEMUX_USAGE_CACHE_PATH` overrides the cache path, and `CLAUDEMUX_CLAUDE_BIN` points
the poller at a specific `claude` binary. If the whole thing is unavailable or broken, the
`5h` and `wk` gauges are unaffected: the per-model rows are strictly additive.


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

head:
  show_last: true
  show_first: false

onepassword:
  default_account: ""
  accounts: {}

launch:
  auto_worktree: false
  layout: ""
  shell_size: ""
  shell_command: ""
  project_dirs: []

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
- `head.show_last` / `head.show_first` — the raw-prompt context rows the status pane
  draws under its `topic`/`now` pair, top to bottom:

  ```
  ● idle 3m   sonnet-5   ⟳ summarizing         ← state line   (always)
  ctx ███░░ 34%   5h ██░ 21%   wk █░ 12%       ← meters       (always)
  topic ❯ head shows last/first prompt rows    ← topic        (always)
  now   ❯ editing cmd/claudemux-head/tui.go    ← now          (always)
  last  ❯ go test ./...                        ← head.show_last   (default on)
  first ❯ add last/first rows to the head      ← head.show_first  (default off)
  ```

  `last` is next to the summary because it is the row that keeps moving — what you
  actually typed, beside the summarizer's account of it — and `first` is at the bottom
  because past the first few turns it has stopped changing. `show_first` ships off for
  that reason; turn it on for long sessions that drift, where *what was I actually asked
  for* stops being obvious from the topic.

  **These also size the pane.** There is no separate height key: `claudemux` asks the head
  for `head.rows` at launch (4, plus one per enabled row) and pins the pane to it on every
  resize, so a row you turn on always has a line to draw in. `head.rows` is derived, not
  writable — `head: rows:` in `config.yml` is an unknown-key startup error like any other
  typo. A session already running keeps the height it launched with; the new one takes
  effect on the next `claudemux` launch, the same as `launch.layout` and
  `launch.shell_size`.

  Both are ignored while the pane is on its keyless fallback — before the first summary
  lands, or with no API key at all, the two rows under the meters already *are*
  `first`/`last`, and a toggle would only print the same prompt twice.
- `onepassword.default_account` / `onepassword.accounts` — consumed by `claudemux`, not
  by the TUI itself, to pick a 1Password account when injecting an `op_env`. Ships empty;
  see `.claudemux.yml` below.
- `launch.layout` — consumed by `claudemux`, not the TUI. Picks the pane arrangement for
  new sessions: `shell-right` (default), `no-shell`, `shell-bottom`, or `head-bottom` —
  see **Layouts** above. Empty (never chosen) behaves as `shell-right`. Set it with
  `claudemux setup`, the interactive picker, rather than hand-editing this file.
- `launch.shell_size` — consumed by `claudemux`, not the TUI. How much of the split the
  shell pane takes: a percentage (`"40%"`) or a column/row count (`"80"`). Empty means the
  layout's default — 30% beside `claude`, 20% below it. Overridden per project with
  `shell_size:` in `.claudemux.yml`. A value that is neither is a startup error here; in a
  project file it is ignored in favor of the next source down. See **Sizing the shell
  pane** above.
- `launch.shell_command` — consumed by `claudemux`, not the TUI. A command line the shell
  pane runs at launch instead of coming up at a bare prompt. The pane drops to a normal
  shell when the command exits, so a command that isn't installed costs an error message
  rather than the pane. Overridden per project with `shell_command:` in `.claudemux.yml`.
  Empty (the default) is a plain shell. See **Running something in the shell pane** above.
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
  project with `worktree: true|false` in `.claudemux.yml`. Default `false`. `-w`/`-W`
  (and the config/`.claudemux.yml` toggles) only take effect on newly created sessions —
  `claudemux -w <existing-session>` attaches without marking it, silently ignoring
  `-w`, the same way name/color only apply at creation. Combine with `-n` to force a
  new session if you need `-w`/`-W` to take effect.
- `launch.project_dirs` — consumed by `claudemux`, not the TUI. The roots to search by
  name when a launch query is neither a real directory nor a zoxide hit — the fallback
  that keeps `claudemux <name>` (and the switchboard's `n` key) from simply failing on a
  repo you have never opened from the shell. Ships empty, which leaves resolution exactly
  as it was: directory, then zoxide, then nothing.

  ```yaml
  launch:
    project_dirs:
      - ~/Projects
      - ~/work
  ```

  A leading `~` expands. Each root is searched two levels deep — `<root>/<project>` and
  `<root>/<org>/<project>`, the layout you end up with once repos are grouped by GitHub
  org — and dotted directories are skipped, so a project's own `.claude/worktrees` never
  turns up as a launch target. A directory name matches the query exactly, by prefix, or
  by substring, in that order of preference: *every* exact hit beats *every* prefix hit,
  wherever it sits. Within one of those tiers the shallower path wins, then the earlier
  root, then alphabetical — so a query always resolves to the same project, and the order
  you list your roots in is the order they win ties. Nothing is ever ambiguous enough to
  prompt you: it either launches one project or reports that nothing matched.
- `teardown.command` — the wrap-up command the status pane types into the `claude`
  pane when you press `x`, and the one it watches for when you type it there yourself
  (see **Tearing down a session** below). Default `/done`. Set it to `""` to skip that
  step, making `x` a gated exit-and-kill — with nothing left to watch for either.

**An unknown key in `config.yml` is a startup error, not a silent no-op.** A typo like
`sumary:` fails loudly at launch instead of quietly behaving as if you'd written nothing.
A missing file is fine — that's just defaults.

## `.claudemux.yml`

`claudemux` reads an optional `.claudemux.yml` from the root of the project directory you
launch it in. See [`.claudemux.yml.example`](.claudemux.yml.example) for the full format:

```yaml
color: blue          # tmux status-bar / iTerm2 tab color
name: my-project      # passed to `claude -n`
worktree: true        # opt this project in/out of the auto-worktree marker (optional)
shell_size: 40%       # how big this project's shell pane is (optional)
shell_command: gh-hud --repo mquinnv/claudemux  # run it in that pane (optional)
op_env: abcdefghijklmnopqrstuvwxyz  # 1Password Environment ID (optional)
op_account: my.1password.com        # 1Password account for op_env (optional)
```

`shell_size` and `shell_command` override `launch.shell_size` / `launch.shell_command`
from `config.yml`, which is what makes a project-scoped invocation — a dashboard pointed
at *this* repo — a property of the repo rather than of your global config.

**`.claudemux.yml` is gitignored by this repo on purpose, and `op_env` should not be
committed** — it's an identifier that points at your secrets. Copy the example instead
of tracking the real file:

```bash
cp .claudemux.yml.example .claudemux.yml
```

**Formerly `.project.yml`.** That name is still read when a directory has no
`.claudemux.yml`, so existing projects keep working untouched and there is no migration
step — rename yours whenever you get to it. Both names are gitignored. When a directory
somehow has both, `.claudemux.yml` wins; when they sit at different depths, the nearer
file wins regardless of name, so a subdirectory that carries its own config is never
overruled from above.

## Appearance: project colors

The `color:` field in `.claudemux.yml` drives two things at once, so a session is
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
session, and claudemux renames the tmux window to it. As the session's focus
settles, the label goes from the launch default to something like `crm
bundling`. Because it is the tmux window name, it also appears as the window
label in the tmux status bar.

The terminal's tab and titlebar show that label behind the project it belongs
to — `crm · crm bundling` — so a window announces its project from the moment it
opens, before any label has landed, and two sessions that settle on the same
topic stay tellable apart. The project is the `.claudemux.yml` `name:` when one
is declared and the session name otherwise, the same pair the `r` reset uses
below. Only the titlebar carries the prefix: the tmux status bar keeps showing
the bare label, since the session name already sits a column to its left.

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

Sessions cloned with `-n` share one `.claudemux.yml`, so `remix-2` restores to
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

**Toggling conduct mode.** While the conductor is live, the status pane shows a
`⏵ conduct` chip — a standing notice that you may be carried elsewhere when this
session finishes. Click the pane and press `Space` to turn conducting off (and
again to turn it back on), exactly as `Space` does in the lobby: the head hands
the request to the lobby, which flips the mode for the whole fleet. The chip
answers the keypress immediately and settles on what the lobby actually did
within a couple of seconds. With no lobby running there is nothing to conduct,
so the chip is absent and `Space` does nothing.

**Deferring from the status pane.** Press `d` to toggle defer on this session —
the same mark the lobby's `d` sets on the selected row (see **Deferring a
session** above). The pane shows a `◆ defer` chip while it's set, and the mark
persists on the tmux session itself, so it survives a head restart and is
visible to the lobby with no head running at all.

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

**Restarting `claude`.** Press `c` to restart the `claude` pane itself — for a
`claude` that has hung, crashed, or needs to pick up a new build or an edited
settings file. The first press arms: the status pane shows
`↻ restart claude? c resume · n new`. Then:

- `c` again resumes the session the pane is following — `claude --resume <id>` —
  so the conversation carries on where it left off.
- `n` starts a new session in the same pane instead.
- `esc` cancels.

Either way the pane is respawned with the exact command `claudemux` launched it
with, so the permission mode, project name, color and worktree marker all come
back as they were. This is a kill, not a polite exit: the running `claude` is
terminated on the spot, which is the only thing that works on a hung one. Nothing
that had reached the transcript is lost, and a `claude` that already exited (its
pane held on screen with the error) is restarted in place the same way.

The status pane follows along on its own — it rebinds to the resumed or new
session as soon as `claude` announces it. While a session has no transcript yet
(`c` right after launch) there is nothing to resume, so the chip offers only
`n new`. `c` does nothing while a teardown is in flight, and `x`/`X` do nothing
while a restart is armed; outside tmux `c` does nothing at all.

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
in `.claudemux.yml`) only marks the session as wanting one; it's the model, prompted by
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

**Tearing down without a wrap-up.** Press `X` (capital) twice. The first press arms —
`⏻ kill session? press X` — and the second sends `/exit`, waits for `claude` to be
gone, and kills the tmux session. Nothing is typed into the `claude` pane, no wrap-up
runs, and no gate has to open.

This is the escape hatch for when the gated ladder can't get its evidence. The `x`
ladder only offers the kill once it can *prove* the wrap-up succeeded, and that proof
is destroyed by things that have nothing to do with whether the work is finished: a
transcript rotation mid-wrap-up withdraws the watch, a `/done` that tidies up but
leaves the branch unpushed never opens the gate, and a session with no worktree and no
upstream can't open it at all. Re-running a wrap-up that has already run, to coax a
chip out of the status pane, is the wrong fix — `X` says "I've decided this session is
finished" and needs nothing but the two presses.

The two ladders never cross. `X` does nothing while an `x` teardown is in flight, and
`x` does nothing while `X` is armed, so the key that skips wrap-ups can never commit
one that's still running. `esc` cancels either, and an `X` arm is withdrawn the moment
any new prompt lands in the session — including the wrap-up command itself, which is
exactly when a one-keystroke kill should stop being armed. Outside tmux `X` does
nothing, same as `x`.

## tmux notes

**claudemux needs no `~/.tmux.conf`.** It sets everything it depends on per session at
launch — the status-bar style, the split layout, and a `window-resized` hook that
re-pins the `claudemux-head` pane to exactly `head.rows` rows on every resize (including
on attach). That hook exists specifically to cope with tmux's default `window-size latest`
behavior, which otherwise redistributes pane heights when a client attaches and would
shrink the head pane, clipping its bottom rows. So the tool works out of the
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

Summaries are the only thing claudemux spends. The `claude` process behind the per-model
weekly meters sends no message and runs no inference — no tokens, nothing billed, and
none of the limit it reports consumed — though it does cost ~2.2s and one firing of your
`SessionStart` hooks per poll. See **The statusline command and the account meters**.

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
