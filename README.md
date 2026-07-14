# claude-head

`claude-head` is a status-bar "head" for a [Claude Code](https://claude.com/claude-code) session. It
tails the session's JSONL transcript and renders a compact bar showing:

- session state (idle / working / waiting on you)
- token and rate-limit meters
- the session's first prompt and its most recent prompt
- an LLM-generated one-line summary of what the session is doing right now

`claude-env` is a tmux launcher that builds the session `claude-head` lives in: a short
`claude-head` pane, `claude` below it, and a shell alongside for everything else.

## The pane model

`claude-env some-project` creates a tmux session laid out like this:

```
┌────────────────────────┬──────────┐
│ claude-head  (4 rows)  │          │
├────────────────────────┤  shell   │
│                        │  (30%)   │
│ claude                 │          │
│                        │          │
└────────────────────────┴──────────┘
```

`claude-head` gets a fixed 4-row pane at the top left (it re-pins itself to 4 rows on
every resize), `claude` runs below it, and a shell takes the right 30% of the window.

## Install

```bash
go install github.com/mquinnv/claude-env/cmd/claude-head@latest
```

That puts `claude-head` on your `PATH` (assuming `$(go env GOPATH)/bin` is on it). The
launcher script and its color resolver aren't part of the Go module — clone the repo and
symlink them:

```bash
git clone https://github.com/mquinnv/claude-env
mkdir -p ~/.local/bin
ln -s "$PWD/claude-env/bin/claude-env"               ~/.local/bin/claude-env
ln -s "$PWD/claude-env/bin/project-color-resolve.sh" ~/.local/bin/project-color-resolve.sh
```

`~/.local/bin` must be on your `PATH` — it does not exist by default on macOS, and
isn't on `PATH` in a default shell. Add it if it isn't there:

```bash
export PATH="$HOME/.local/bin:$PATH"   # in ~/.zshrc, ~/.bashrc, or config.fish
```

**`claude-env` and `project-color-resolve.sh` must remain siblings.** `claude-env`
sources the resolver by a path relative to its own location (`dirname` of its own
resolved path), not via `PATH`. Symlinking only one of them, or symlinking them into
different directories, breaks project-color resolution silently (it just no-ops).

## Dependencies

| Tool | Required by | Without it |
|---|---|---|
| `tmux` | `claude-env` | `claude-env` cannot run at all |
| `claude-head` on `PATH` | `claude-env` | the head pane starts but the command isn't found |
| `jq` | `hooks/claude-head-map.sh` | the hook exits silently; see below |
| `git` | `claude-env` (1Password org inference) | `op_account` in `.project.yml` or `onepassword.default_account` still work |
| `zoxide` | `claude-env` (fuzzy directory resolution) | `claude-env <query>` only works for literal directories, not `z`-style queries |
| `op` (1Password CLI) | `claude-env` (`op_env` injection) | sessions launch without injected secrets |
| iTerm2 | `claude-env` (tab coloring) | other terminals silently ignore the OSC escape sequences |

## The Claude Code hook

`claude-head` needs to know which tmux pane is running the `claude` process it should
follow — its *sibling* pane, not just "whatever session changed most recently." The hook
records that mapping.

**Without the hook, claude-head falls back to most-recently-active-session detection**,
which picks whichever `.jsonl` transcript in the project directory has the newest mtime.
That's wrong the moment you have more than one Claude Code session open on the same
project — the head pane can silently lock onto a different session than the one it sits
next to. Install the hook.

```bash
mkdir -p ~/.claude/hooks
ln -sf "$PWD/claude-env/hooks/claude-head-map.sh" ~/.claude/hooks/claude-head-map.sh
```

Register it on **both** `SessionStart` and `UserPromptSubmit` in `~/.claude/settings.json`:

```json
{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          { "type": "command", "command": "~/.claude/hooks/claude-head-map.sh" }
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [
          { "type": "command", "command": "~/.claude/hooks/claude-head-map.sh" }
        ]
      }
    ]
  }
}
```

Both events matter: `SessionStart` records the mapping when a pane first opens;
`UserPromptSubmit` keeps it current across `/clear`, `resume`, and compaction, which
rotate the transcript file underneath a live session.

The hook **must stay silent on stdout** — `UserPromptSubmit` stdout is injected directly
into the model's context, so any hook output there would leak into the conversation.
`hooks/claude-head-map.sh` already respects this; if you write your own, do too.

## Configuration

`claude-head` reads `config.yml` from `$XDG_CONFIG_HOME/claude-env/` (default
`~/.config/claude-env/`). Every key, with its default:

```yaml
summary:
  enabled: true
  model: claude-haiku-4-5
  min_interval: 20s
  api_key_file: ~/.config/claude-env/env

onepassword:
  default_account: ""
  accounts: {}
```

- `summary.enabled` — turn the LLM summary off entirely (see **Billing** below).
- `summary.model` — the Anthropic model used for summaries.
- `summary.api_key_file` — where the Anthropic credential is read from. A *path*, not
  the key itself: the file is read as a stream, so it can be a FIFO your secret manager
  mounts and the key never touches disk (see **Secrets** below). Point it anywhere;
  `~` expands.
- `summary.min_interval` — minimum time between summary calls for an *active* session.
  This is the only thing bounding what an active session costs you, so treat it as a
  spending control. `0` is legal and means *no floor* — every turn may fire a call.
  A negative value is rejected at startup (it would remove the limit, not set one).
- `onepassword.default_account` / `onepassword.accounts` — consumed by `claude-env`, not
  by the TUI itself, to pick a 1Password account when injecting an `op_env`. Ships empty;
  see `.project.yml` below.

**An unknown key in `config.yml` is a startup error, not a silent no-op.** A typo like
`sumary:` fails loudly at launch instead of quietly behaving as if you'd written nothing.
A missing file is fine — that's just defaults.

## `.project.yml`

`claude-env` reads an optional `.project.yml` from the root of the project directory you
launch it in. See [`.project.yml.example`](.project.yml.example) for the full format:

```yaml
color: blue          # tmux status-bar / iTerm2 tab color
name: my-project      # passed to `claude -n`
op_env: abcdefghijklmnopqrstuvwxyz  # 1Password Environment ID (optional)
op_account: my.1password.com        # 1Password account for op_env (optional)
```

**`.project.yml` is gitignored by this repo on purpose, and `op_env` should not be
committed** — it's an identifier that points at your secrets. Copy the example instead
of tracking the real file:

```bash
cp .project.yml.example .project.yml
```

## Billing

Summaries are **calls to the Anthropic API made with your own `ANTHROPIC_API_KEY`,
billed to your account.** Roughly one Haiku call per `summary.min_interval` (20s by
default) of *active* session — none while the session is idle.

- Set `summary.enabled: false` in `config.yml` to turn this off entirely.
- If no API key is configured at all, the feature is simply off: `claude-head` falls
  back to showing the raw first/last prompt instead of a generated summary. Nothing is
  billed.
- **If you already have `ANTHROPIC_API_KEY` exported in your shell for other tools,
  claude-head will pick it up and start billing you without any separate opt-in.** Set
  `summary.enabled: false` if you don't want that.

## Secrets and secret managers

Two keys are recognized: `ANTHROPIC_API_KEY` and `ANTHROPIC_BASE_URL` (the latter for
users behind an Anthropic-compatible gateway). They are read from a plain `KEY=value`
env file, separate from `config.yml`, at `$XDG_CONFIG_HOME/claude-env/env` (default
`~/.config/claude-env/env`). **The process environment always wins over the env file.**
`CLAUDE_HEAD_ENV` overrides the env-file path outright (nothing else is consulted when
it's set).

Three ways to provide the key:

**1. Plain file** — the baseline:

```bash
mkdir -p ~/.config/claude-head
cat > ~/.config/claude-env/env <<'EOF'
ANTHROPIC_API_KEY=sk-ant-...
EOF
chmod 0600 ~/.config/claude-env/env
```

One `KEY=value` per line, `#` comments allowed, no quoting.

**2. `op run`** — no file at all, since the process environment beats the file:

```bash
op run -- claude-head
```

**3. Mounted FIFO** (e.g. a 1Password Environment) — mount the Environment so it serves
its contents at `~/.config/claude-env/env`. `claude-head` reads the file as a byte
stream, not as a regular file, so it never has to land on disk in plaintext.

Two behaviors are expected with a FIFO-backed env file and are not bugs:

- **Contention is expected.** A FIFO serves one reader at a time. If several
  `claude-head` panes start at once, they race on the open and the losers see an empty
  pipe. `claude-head` retries with backoff (50ms, 150ms, 400ms) and the losers succeed
  milliseconds later.
- **A locked agent degrades, it does not hang.** Opening a FIFO with no writer on the
  other end blocks forever. `claude-head` times out after 2 seconds and runs *without*
  summaries rather than freezing the TUI. The retries above do **not** multiply that
  wait: a read that times out means there is no writer at all, which retrying cannot
  fix, so it stops immediately. Only an empty-but-prompt read — real contention — is
  retried. Worst case is one 2-second timeout, not four.

## License

MIT — see [LICENSE](LICENSE).
