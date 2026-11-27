# Multi-account sessions

Date: 2026-11-27
Status: draft for review

## Why

Michael has two Claude Max plans: one on ameriglide.com and one on
bespokestairlifts.co.uk. Today a single login is shared by every session on
the machine: one keychain item, `Claude Code-credentials`, plus `oauthAccount`
in `~/.claude.json`. Running `/login` with the other account swaps the
credentials for **every** live session. Those sessions then fail, because the
thinking blocks already in their history were signed by the previous account,
and the new one rejects them.

Account selection has to be **explicit and fixed for the life of each
session**. The split also lets the Bespoke-heavy repos (homelife,
homelife-salesforce, scala3d) draw on Bespoke's budget instead of AmeriGlide's.
Keeping the two accounts' usage strictly separate is not a goal in itself. The
switchboard meters, though, have to show each account on its own, or they
mean nothing.

## Goals

- Each claudemux session runs on exactly one named Claude account, chosen at
  launch.
- The account is chosen automatically per project, with an explicit
  per-launch override.
- The account stays the same when a session is respawned, resumed, restarted
  or restored after a reboot, and when it is handed off to another session.
- The lobby and the head show which account a session is on.
- The switchboard meters (5-hour and weekly gauges, burn rate, prepaid
  credits) are tracked and shown per account.
- Both accounts still share settings, plugins, skills, hooks, rules, memory
  and transcripts, so the setup is maintained in one place.

## Non-goals

- Routing work to whichever account has budget left. That's a follow-up. The
  per-account meters are its prerequisite.
- Moving a running session from one account to another. It can't be done
  safely, because the signed thinking blocks belong to the first account.
- More than a handful of accounts, or API-key accounts. Nothing should rule
  them out, but they are not designed for.
- Linux/Windows credential stores. claudemux is macOS-first; nothing here
  should break other platforms, but only macOS gets tested.

## Anti-goals

- A session silently ends up on a different account from the one it started
  on, for example after a reboot restore or `--resume`. That is exactly the
  bug this fixes.
- Meters that mix the two accounts, or flip back and forth between them.
- Two copies of settings/plugins/skills that drift apart.
- Hand-maintained setup that breaks without anyone noticing. A broken symlink
  must be detected and reported, never silently ignored.

## What we learned (2026-11-27 experiment, Claude Code 2.1.332)

- Credentials live in the keychain item `Claude Code-credentials`. When
  `CLAUDE_CONFIG_DIR` (or `CLAUDE_SECURESTORAGE_CONFIG_DIR`) is set, the item
  becomes `Claude Code-credentials-<first 8 hex of sha256(dir)>`.
  `cmd/claudemux-head/credits.go:creditToken` already computes this.
- Setting only `CLAUDE_SECURESTORAGE_CONFIG_DIR` does split the token: the new
  store starts logged out, and `/login` there writes only the hashed item. But
  `/login` also rewrote `oauthAccount` in the **shared** `~/.claude.json`.
  `claude auth status`, `/status` and claudemux's credit lookup all read the
  account from that file. And because of `profileFetchedAt`, every refresh
  would keep flipping it between the two accounts. **Rejected.**
- MCP OAuth tokens (`mcpOAuth`) also live in the credential store, so each
  account needs its own `/mcp` logins whichever way we go.

## Approaches

1. **One full `CLAUDE_CONFIG_DIR` per extra account, with symlinks for the
   shared parts. (Chosen.)** Each extra account gets its own
   `.claude.json` and keychain item, so the account record can't be
   overwritten by the other account. The shared parts are symlinked back to
   `~/.claude`, so transcripts, plugins and settings stay in one place. Cost:
   claudemux has to build and check the symlinks, and keep user-level MCP
   servers in sync.
2. **Separate credential store only.** Simplest, but ruled out by the
   experiment above.
3. **Two fully separate config directories, nothing shared.** No symlink code,
   but transcripts split into two trees (the heads, resume and summaries would
   all need to search both), and plugins and settings would drift. Rejected.

Michael picked approach 1.

## Design

### Accounts

- AmeriGlide is the **default account** (used unless a project or `-a` says
  otherwise), but its login lives in `~/.claude-accounts/ameriglide`.
  Sessions on it are launched with
  `CLAUDE_CONFIG_DIR=~/.claude-accounts/ameriglide`. That folder already
  exists and is logged in, from the 2026-11-27 experiment.
- Bespoke keeps the login it already has in `~/.claude` and gets no
  `config_dir`. *Why (Michael, 2026-11-27):* switching the `~/.claude` login
  would immediately break every session open at that moment, while this way
  nothing breaks. The only cost is that Claude Code started outside claudemux
  uses Bespoke, and Michael never runs Claude Code outside claudemux.
- "Default account" (a config setting) and "the account in `~/.claude`" (a
  folder) are separate ideas, and the code must not mix them up.
- Accounts are declared in `~/.config/claudemux/config.yml`:

```yaml
claude:
  default_account: ameriglide
  accounts:
    ameriglide:
      config_dir: ~/.claude-accounts/ameriglide
      color: blue          # optional badge colour; claudemux picks one if unset
    bespoke: {}            # no config_dir = ~/.claude
```

  An unknown account name anywhere (project file, `-a`, a session record) is
  an error that names the account. It never falls back to the default
  silently.

### Choosing the account

Precedence, highest first:

1. `claudemux -a <account>` (long form `--account`) on the command line.
2. `claude_account: <name>` in the project's `.claudemux.yml`, or the legacy
   `.project.yml`. This is read through the existing `project_field` helper,
   the same way `op_account` is.
3. `claude.default_account`.

There is deliberately **no mapping by GitHub org**. scala3d and
homelife-salesforce are in the `ameriglide` org but belong on Bespoke, and
homelife has no remote at all. A per-project field is the reliable switch.
All three already have a `.project.yml`.

### Fixing the account to the session

- The launcher resolves the account once. The account applies to the **whole
  tmux session**, not just the claude process. It is set with
  `tmux set-environment` on the session, so the shell pane, hooks and
  everything started from them get it too. It is also added to the start of
  `claude_cmd` (next to `worktree_env`, in `bin/claudemux` around line 1033),
  so respawns don't depend on what the tmux environment holds. *(My call:
  otherwise `claude auth status` or a handoff run from the shell pane would
  silently use the wrong account.)*
- An account with no `config_dir` actively **unsets** `CLAUDE_CONFIG_DIR`, in
  both the tmux environment and the command. Otherwise a launch or handoff
  started from inside a session on another account would pass that account's
  folder on.
- The head pane gets the session's config dir passed explicitly, for its own
  usage and credit reads.
- It records the account name as the tmux option `@claudemux_account`, on both
  the session and the claude pane. Pane respawns reuse `@claudemux_claude_cmd`,
  which already carries the env prefix.
- The head's `sessionRecord` gets a new `account` field. Reboot restore
  (`swrestore.go:restoreArgs`) passes `-a <account>` when it relaunches.
- **Resume rules** *(my call)*:
  - Any resume (`-r <id>`, including one typed by hand) uses the account in
    the saved record for that session ID.
  - A record with no account (written before this change) means the session
    ran on whatever login `~/.claude` held at the time. It resumes with no
    `config_dir`. In practice that is Bespoke's store, which was the login in
    use at migration time.
  - A session ID with no record at all falls back to the normal precedence
    rules.
  - `-a X` that contradicts the record's account is an error naming both
    accounts. Nothing is switched silently.
- `--resume` paths (lost sessions, restart) also pass `-a` from the record or
  the tmux option, never a fresh resolution. If a project's `claude_account`
  has changed since the session started, the session keeps its original
  account.
- Handoffs (`claudemux -n -d -w <dir>`, and the conductor) resolve the
  **target** project's account through the normal precedence. A handoff into
  scala3d from an AmeriGlide session lands on Bespoke.
- Subagents run inside their parent's claude process, so they inherit its
  account automatically.

### The account directory

A new command, `claudemux account setup <name>` (provided by the head binary,
like the other subcommands), creates or repairs an account's `config_dir`:

- **Symlinked** to `~/.claude`: `projects/`, `settings.json`, `CLAUDE.md`,
  `rules/`, `skills/`, `agents/`, `commands/`, `plugins/`, `hooks/`,
  `claudemux/`, `memory.jsonl`, `team.json`, `team-kernel.*.md`, `plans/`,
  `specs/`, `scripts/`, `file-history/`, `history.jsonl`, `todos/` (if
  present), and `session-env/`. The builder confirms the final list against
  what's actually in `~/.claude` and what Claude Code writes. The rule:
  anything that describes the *user or machine* is shared; anything that
  describes the *login* is not.
- **Per account, not linked:** `.claude.json` (holds `oauthAccount`, project
  trust and onboarding state), the keychain item, `settings.local.json` only
  if Claude Code writes login-specific state there (builder to check),
  `statsig/`, `telemetry/` and other caches.
- `claudemux account check` (also run by `claudemux doctor`, and by the
  lobby at startup) reports missing or broken symlinks, and anything that
  should be a symlink but has become a real file or folder. Claude Code may
  replace a symlink with a real file when it writes atomically. The builder
  verifies which files Claude Code writes that way. For those, setup links
  the parent folder instead, or check offers to repair them.
- **MCP servers** *(my call)*: the account in `~/.claude` is the source.
  Its **user-level** `mcpServers` are synced into each other account's
  `.claude.json` at launch. Per-project `mcpServers` are left alone.
  - The sync is a merge by server name, and the source wins on a name clash.
  - It records which names it synced (in claudemux's own state, not in
    `.claude.json`), so a server removed from the source is removed only
    where it was synced to. A server added directly to an account is never
    touched.
  - It is done in Go: reread the file right before writing, change only that
    key, write atomically by rename. A live claude rewriting the same file at
    the same instant can still win the race; the next launch syncs again. MCP *logins* stay per account;
  the first launch on a new account tells the user to run `/mcp`.
- **First login:** `claudemux account login bespoke` opens a throwaway
  claude on that account so the user can run `/login`. Or a normal launch
  shows Claude Code's own "not logged in" screen.

### Per-account meters

Today the statusline command writes `~/.claude/claudemux/rate-limits.json`,
the usage poll writes `usage.json`, and credits write `credits-v2-<org>.json`.
`~/.claude/claudemux/` is shared through the symlinks, so each file has to be
**keyed by account**:

- The statusline writer runs inside the session's claude, which has the
  session's `CLAUDE_CONFIG_DIR` in its environment. It maps that directory to
  an account name: no `CLAUDE_CONFIG_DIR`, or `~/.claude` → the default
  account; otherwise the account whose `config_dir` matches. It writes
  `rate-limits-<account>.json`. For the default account it keeps writing the
  current `rate-limits.json` too, so heads that haven't been upgraded don't go
  blank.
- The usage poll (`usage.go:fetchPlanUsage`, which starts a claude process)
  runs once per account, with that account's `CLAUDE_CONFIG_DIR`, writing
  `usage-<account>.json`, each with its own lock and back-off marker.
- Credits (`credits.go:creditProfile`) read the org from the account's own
  `.claude.json` and the token from its own keychain item. This mostly works
  already once `CLAUDE_CONFIG_DIR` is passed. The cache is already keyed by
  org.
- **Lobby:** one row of meters per account that has a live session. A row
  for an account with no live session is hidden, or dimmed if its data is
  less than a day old (builder's call, keep it simple). Each row starts with
  the account badge.
- **Head:** the meter strip shows *its own session's* account. The account
  name is shown only when more than one account is configured.
- **Web status page:** the same per-account rows.
- Burn-rate history, which the head and lobby keep in memory, is kept per
  account.

### Badges

The lobby session list and the head show a small account badge (name plus
colour) when more than one account is configured. With only one account,
nothing changes on screen.

## Migration (one time, done with Michael)

1. `claudemux account setup ameriglide` turns the existing
   `~/.claude-accounts/ameriglide` (logged in during the experiment, keychain
   item `Claude Code-credentials-7a2796e2`) into a full account folder: it
   adds the symlinks and its own `.claude.json`. It must keep the
   `oauthAccount` there, or carry it over.
2. Fix `oauthAccount` in `~/.claude.json`, which the experiment overwrote
   with AmeriGlide. Run `/login` as Bespoke in a `~/.claude` session, or let
   setup rewrite it from the profile.
3. Add `claude_account: bespoke` to the `.project.yml` of homelife,
   homelife-salesforce and scala3d.
4. Nothing open has to be restarted. Sessions that are open keep running on
   the `~/.claude` (Bespoke) login. Sessions in AmeriGlide repos move to the
   AmeriGlide account the next time they are started fresh. They must not be
   resumed onto it: a record with no account resumes on `~/.claude`, by the
   resume rules above.

## Testing

- Unit: account resolution precedence; unknown-account errors; mapping a
  config dir to an account (with `~` expansion and trailing slashes); session
  record round trip with `account`; `restoreArgs` includes `-a`; per-account
  cache paths; statusline writes the right file for each environment.
- Launcher (bash tests where they exist): `claude_cmd` gets the env prefix
  only for accounts with a `config_dir`; `@claudemux_account` is set.
- `account setup`/`check` against a temporary HOME: creates links, is safe
  to re-run, detects a link that has become a real file.
- Live: launch scala3d → `claude auth status` in its pane shows Bespoke;
  launch claudemux itself → it shows AmeriGlide; each is unaffected by the
  other; `/login` in neither flips the
  other; the lobby shows two meter rows; reboot restore brings scala3d back
  on Bespoke.

## Left to the builder

- The exact symlink list, and which Claude Code files get replaced instead of
  written through a link.
- Whether `settings.local.json` holds login-specific state.
- Hiding vs dimming meter rows for idle accounts.
- Badge colour defaults.
- Where in the head's subcommand tree `account` lives.
