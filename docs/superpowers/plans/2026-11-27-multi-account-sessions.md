# Multi-account sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Each claudemux session runs on one named Claude account, chosen at launch, fixed for the session's life, and shown in the head and lobby, with switchboard meters tracked per account.

**Architecture:** Accounts are declared in `config.yml` under `claude:`. Every decision lives in Go so it can be tested: which account, and with what config dir. The bash launcher only asks `claudemux-head account resolve` and applies the answer. It sets the tmux session environment, prefixes `claude_cmd` and sets `@claudemux_account`. Non-`~/.claude` accounts get a `CLAUDE_CONFIG_DIR` folder whose shared parts are symlinks into `~/.claude`. Meter caches are keyed by account name.

**Tech Stack:** Go (cmd/claudemux-head, bubbletea TUI, yaml.v3), bash (bin/claudemux), tmux.

**Spec:** `docs/superpowers/specs/2026-11-27-multi-account-sessions-design.md`

## Global Constraints

- Config key: `claude.default_account` (string), `claude.accounts.<name>.config_dir` (string, `~` expanded, optional), `claude.accounts.<name>.color` (string, optional). Unknown keys are already a startup error (`KnownFields(true)`). Keep it that way.
- **No `claude:` block at all = exactly today's behaviour:** one implicit account named `default` with no config dir, no badges, the same file names.
- An account with no `config_dir` means `~/.claude`. Launches on it **unset** `CLAUDE_CONFIG_DIR`. "Default account" (`claude.default_account`) and "the account in `~/.claude`" are different ideas. Never conflate them.
- Unknown account name (in `-a`, `claude_account`, a record or `default_account`) is an error that names it. Never fall back silently.
- Account names: `^[a-z0-9][a-z0-9_-]{0,31}$` (they go into file names and tmux options).
- Per-project field: `claude_account` in `.claudemux.yml` / legacy `.project.yml`, read like `op_account`.
- tmux option name: `@claudemux_account`, set on the session and on the claude pane.
- Cache files under `~/.claude/claudemux/`: `rate-limits-<account>.json`, `usage-<account>.json` (+ its `.lock` / `.failed`). Credits stay keyed by org as today. When more than one account is configured, the default account *also* keeps writing the legacy `rate-limits.json` / `usage.json`, so heads that haven't been upgraded don't go blank. With a single implicit account, only the legacy names are used.
- Badges/account names appear on screen only when more than one account is configured.
- macOS-first. Do not break Linux builds (`go build ./...` and `go test ./...` must pass).
- Commit messages end with the two attribution lines from the session (`Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` and the `Claude-Session:` line).

## Review Focus

1. **A pane respawn or `c` restart after the tmux server's environment was lost** still runs on the session's account, because the env prefix travels inside `@claudemux_claude_cmd`. Tested in Task 3 (`claude_cmd` string assertions).
2. **A handoff launched from inside a non-`~/.claude` session into a `~/.claude`-account project** must not inherit `CLAUDE_CONFIG_DIR`. Tested in Task 2 (`resolve` output says "unset") and Task 3 (prefix is `env -u CLAUDE_CONFIG_DIR`).
3. **`config_dir` written with `~`, a trailing slash or a symlinked path** still maps back to the right account for the statusline writer. Tested in Task 1 (`accountForConfigDir` table).
4. **Claude Code replacing a symlinked file with a real file** (an atomic rename write) is detected by `account check`, not ignored. Tested in Task 5.
5. **An account whose usage poll keeps failing** does not stop the other account's poll, and does not blank the other account's row. Tested in Task 7 (separate lock/backoff paths) and Task 9 (row per account).

---

### Task 1: Account config model

**Files:**
- Create: `cmd/claudemux-head/accounts.go`, `cmd/claudemux-head/accounts_test.go`
- Modify: `cmd/claudemux-head/config.go` (add `Claude ClaudeConfig \`yaml:"claude"\`` to `Config`, and validation in `loadConfig`)

**Interfaces:**
- Produces:
  - `type ClaudeAccount struct { ConfigDir string \`yaml:"config_dir"\`; Color string \`yaml:"color"\` }`
  - `type ClaudeConfig struct { DefaultAccount string \`yaml:"default_account"\`; Accounts map[string]ClaudeAccount \`yaml:"accounts"\` }`
  - `const implicitAccount = "default"`
  - `func (c ClaudeConfig) Names() []string`: sorted. Returns `[implicitAccount]` when `Accounts` is empty.
  - `func (c ClaudeConfig) Multi() bool`: more than one account.
  - `func (c ClaudeConfig) Default() string`: `DefaultAccount`, or `implicitAccount` when there is no block.
  - `func (c ClaudeConfig) ConfigDir(name string) (dir string, err error)`: expanded absolute path, `""` for the `~/.claude` account. Errors on an unknown name.
  - `func (c ClaudeConfig) ForConfigDir(envDir string) (name string, ok bool)`: maps `$CLAUDE_CONFIG_DIR` (may be `""`) to an account. `""`, `~/.claude` or `~/.claude/` → the account with no `config_dir`. Compare after `~` expansion, `filepath.Clean` and `filepath.EvalSymlinks` (falling back to Clean if EvalSymlinks fails).
  - `func (c ClaudeConfig) Validate() error`: default exists; names match the regex; at most one account without `config_dir`; no two accounts share a `config_dir`.

- [ ] **Step 1: Write failing tests** in `accounts_test.go`:
  - `TestClaudeConfigImplicit`: zero value → `Names()==["default"]`, `Multi()==false`, `Default()=="default"`, `ConfigDir("default")==""`.
  - `TestClaudeConfigConfigDirExpandsHome`: `config_dir: ~/.claude-accounts/ameriglide` with `HOME=/h` (use `t.Setenv`) → `/h/.claude-accounts/ameriglide`.
  - `TestClaudeConfigUnknownAccount`: `ConfigDir("nope")` error message contains `nope`.
  - `TestForConfigDir`: table. `""`→bespoke; `/h/.claude`→bespoke; `/h/.claude/`→bespoke; `/h/.claude-accounts/ameriglide/`→ameriglide; `/elsewhere`→`("",false)`. Config: `{default: ameriglide, ameriglide:{config_dir:~/.claude-accounts/ameriglide}, bespoke:{}}`.
  - `TestForConfigDirSymlink`: temp dir with a symlink to the account dir → resolves to that account.
  - `TestClaudeConfigValidate`: missing default, bad name `Bad Name`, two accounts without `config_dir`, and a duplicate `config_dir` each error. The good config passes.
  - In `config_test.go`, `TestLoadConfigClaudeBlock` parses the YAML above through `loadConfig` (same harness as existing config tests), and an unknown key `claude.accounts.x.bogus` fails.
- [ ] **Step 2: Run** `go test ./cmd/claudemux-head -run 'ClaudeConfig|ForConfigDir|LoadConfigClaude'`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement** the types and methods above in `accounts.go`. Call `cfg.Claude.Validate()` from `loadConfig` after decoding, and wrap its error with `config.yml: claude:`.
- [ ] **Step 4: Run** the same tests, then `go test ./cmd/claudemux-head`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(head): claude accounts config`.

### Task 2: `claudemux-head account resolve`

**Files:**
- Create: `cmd/claudemux-head/accountcmd.go`, `cmd/claudemux-head/accountcmd_test.go`
- Modify: `cmd/claudemux-head/main.go` (dispatch `os.Args[1] == "account"` the same way as `config` / `project`), `cmd/claudemux-head/headhelp.go` (help line)

**Interfaces:**
- Consumes: Task 1 `ClaudeConfig`; the existing project-field reader in `projectconfig.go` (the Go side of `project_field`; use whatever `claudemux-head project` already uses to read `op_account`); the existing session-record readers in `sessionrecord.go`, to find a record by session ID.
- Produces:
  - `type accountChoice struct { Name, ConfigDir string; Source string }`. Source is one of `flag|record|project|default|legacy-record`.
  - `func resolveAccount(cfg ClaudeConfig, dir, flag, resumeID string, recordAccount func(id string) (acct string, found bool), projectAccount func(dir string) string) (accountChoice, error)`
  - CLI: `claudemux-head account resolve --dir D [--account X] [--resume ID]` prints one line, `NAME<TAB>CONFIG_DIR<TAB>SOURCE`. CONFIG_DIR is empty for the `~/.claude` account. Exit 1 with the error on stderr.

Rules (from the spec, exactly):
1. If `resumeID` is non-empty and `recordAccount` finds a record:
   - its account if set: `record`;
   - else the account with no `config_dir`: `legacy-record`. If no such account exists, that is an error.
   - If `flag` is also set and differs, return an error naming both: `session <id> ran on <rec>; -a <flag> would switch it`.
2. Else if `flag` is set: `flag`.
3. Else if `projectAccount(dir)` is non-empty: `project`.
4. Else `cfg.Default()`: `default`.
Every name passes through `cfg.ConfigDir` (so an unknown name errors).

- [ ] **Step 1: Write failing tests**, table-driven `TestResolveAccount`, with config `{default: ameriglide, ameriglide:{config_dir:/a}, bespoke:{}}`:
  - nothing set → `ameriglide,/a,default`
  - project=bespoke → `bespoke,"",project`
  - flag=bespoke, project=ameriglide → `bespoke,"",flag`
  - resume id found with account bespoke, project=ameriglide → `bespoke,"",record`
  - resume id found with empty account → `bespoke,"",legacy-record`
  - resume id found=bespoke, flag=ameriglide → error containing `bespoke` and `ameriglide`
  - resume id not found, project=bespoke → `bespoke,"",project`
  - project=`nope` → error containing `nope`
  - implicit config (no block), nothing set → `default,"",default`
  - `TestAccountResolveCLI`: runs the subcommand function with args and a temp HOME/config. Asserts the exact tab-separated stdout line, and exit code 1 on an unknown `--account`.
- [ ] **Step 2: Run** `go test ./cmd/claudemux-head -run 'ResolveAccount|AccountResolveCLI'`. Expected: FAIL.
- [ ] **Step 3: Implement** `resolveAccount` and the CLI wiring. The production `recordAccount` scans session records for a matching `SessionID` and returns its `Account` field (added in Task 4; until then, read the JSON key `account` from the record and treat a missing key as empty).
- [ ] **Step 4: Run** the tests, then the whole package. Expected: PASS.
- [ ] **Step 5: Commit** `feat(head): account resolve subcommand`.

### Task 3: Launcher applies the account

**Files:**
- Modify: `bin/claudemux` (getopts at line ~71, usage line, the `claude_cmd` assembly around lines 979-1033, the pane-option block around line 1130, the head-pane launch)
- Create: `cmd/claudemux-head/launcher_account_test.go`. A Go test that sources the launcher's pure helper function under `bash` (see Step 1). If sourcing the script is impractical, test the helper by running `bash -c 'source <(sed -n "/^account_env_prefix()/,/^}/p" bin/claudemux); account_env_prefix ...'`.

**Interfaces:**
- Consumes: Task 2 CLI output.
- Produces:
  - bash flag `-a <account>`. Usage string: `claudemux [-n] [-d] [-w|-W] [-a account] [-N name] [-r session-id] [-C dir] [...]`
  - bash function `account_env_prefix CONFIG_DIR` prints `CLAUDE_CONFIG_DIR=<%q dir> ` when the dir is set, else `env -u CLAUDE_CONFIG_DIR `
  - tmux: `set-option -t <session> @claudemux_account <name>`, the same on the claude pane (`-p`); `set-environment -t <session> CLAUDE_CONFIG_DIR <dir>`, or `set-environment -t <session> -u CLAUDE_CONFIG_DIR`

- [ ] **Step 1: Write failing test** `TestAccountEnvPrefix`: `account_env_prefix "/h/.claude-accounts/ameriglide"` → `CLAUDE_CONFIG_DIR=/h/.claude-accounts/ameriglide `; `account_env_prefix ""` → `env -u CLAUDE_CONFIG_DIR `; a dir with a space is `%q`-quoted.
- [ ] **Step 2: Run** `go test ./cmd/claudemux-head -run AccountEnvPrefix`. Expected: FAIL (function missing).
- [ ] **Step 3: Implement** in `bin/claudemux`:
  - add `a:` to getopts → `ACCOUNT_FLAG`
  - after `work_dir` is known and before `claude_cmd` is built: `IFS=$'\t' read -r ACCOUNT ACCOUNT_DIR ACCOUNT_SRC < <(claudemux-head account resolve --dir "$work_dir" ${ACCOUNT_FLAG:+--account "$ACCOUNT_FLAG"} ${RESUME_ID:+--resume "$RESUME_ID"})`. On failure, print the error and `exit 1`, like the other fatal launcher checks.
  - `claude_cmd="$(account_env_prefix "$ACCOUNT_DIR")${worktree_env}${claude_cmd}"`
  - set the tmux options and the session environment above, right after the session is created (beside `@claudemux_claude_cmd`)
  - start the head pane with the same `account_env_prefix`, so its usage and credit reads see the account
  - when `ACCOUNT_DIR` is non-empty, run `claudemux-head account sync-mcp "$ACCOUNT"` before launching claude (the subcommand comes in Task 6; until then, guard with `|| true` so a missing subcommand doesn't block the launch)
- [ ] **Step 4: Run** the test; then manual check: `bin/claudemux -d -a bespoke -N acct-smoke ~/Projects/claudemux`, then `tmux show -t acct-smoke -v @claudemux_account` → `bespoke`, `tmux show-environment -t acct-smoke CLAUDE_CONFIG_DIR` → `-CLAUDE_CONFIG_DIR`; then kill the session.
- [ ] **Step 5: Commit** `feat(launcher): -a and per-project claude account`.

### Task 4: Session record, restore and resume keep the account

**Files:**
- Modify: `cmd/claudemux-head/sessionrecord.go` (field), `headrecord.go` / the `writeRecordCmd` that reads tmux (read `@claudemux_account`), `swrestore.go:restoreArgs`, `lostsessions.go` (any other resume argv builder; grep for `"-r"`)
- Test: `sessionrecord_test.go`, `swrestore_test.go`, `lostsessions_test.go`

**Interfaces:**
- Produces: `sessionRecord.Account string \`json:"account,omitempty"\``. `restoreArgs` emits `"-a", r.Account` before `"--"` when `r.Account != ""`.

- [ ] **Step 1: Write failing tests:**
  - `TestSessionRecordAccountRoundTrip`: marshal/unmarshal keeps `account`, and an old JSON with no key → `""`.
  - `TestRestoreArgsAccount`: record with `Account:"bespoke"` → args contain `-a bespoke` before `--`; with `""` → no `-a`.
  - `TestWriteRecordReadsAccountOption`: use whatever seam `writeRecordCmd` already has for tmux output. The record carries the option value.
- [ ] **Step 2: Run** them. Expected: FAIL.
- [ ] **Step 3: Implement.** The head reads `@claudemux_account` alongside session name and launch dir in `writeRecordCmd` (same tmux call, one more format field).
- [ ] **Step 4: Run** the package tests. Expected: PASS.
- [ ] **Step 5: Commit** `feat(head): records and restore carry the claude account`.

### Task 5: `account setup` / `account check` / `account login`

**Files:**
- Create: `cmd/claudemux-head/accountdir.go`, `cmd/claudemux-head/accountdir_test.go`
- Modify: `accountcmd.go` (subcommands), `headhelp.go`; the lobby startup (`switchboard.go` or `switchboardtui.go` init) shows a one-line warning when `check` reports problems for any configured account

**Interfaces:**
- Consumes: Task 1.
- Produces:
  - `var sharedClaudeEntries = []string{...}`: the names under `~/.claude` that are symlinked. Start from the spec's list: `projects`, `settings.json`, `CLAUDE.md`, `rules`, `skills`, `agents`, `commands`, `plugins`, `hooks`, `claudemux`, `memory.jsonl`, `team.json`, `plans`, `specs`, `scripts`, `file-history`, `history.jsonl`, `todos`, `session-env`, `shell-snapshots`, plus any `team-kernel.*.md` glob match. Only link entries that exist in `~/.claude`.
  - `type dirProblem struct { Entry, Want, Got string }`. Got is one of `missing|real-file|real-dir|wrong-target`.
  - `func checkAccountDir(home, dir string) ([]dirProblem, error)`
  - `func setupAccountDir(home, dir string) (created []string, err error)`. Idempotent. Creates `dir`, then each missing link. It **never** overwrites a real file or folder at a link's place: it returns a problem instead, and the CLI prints how to move it. It never touches `.claude.json` if one exists.
  - CLI: `account setup <name>`, `account check [name]` (exit 1 if there are problems), `account login <name>` (execs `claude` with `CLAUDE_CONFIG_DIR` set, in `$TMPDIR`, printing "run /login, then /exit"), `account list` (name, dir or `~/.claude`, default marker).
- [ ] **Step 1: Investigate (no code)**, then record the findings as a comment above `sharedClaudeEntries`: with a throwaway `CLAUDE_CONFIG_DIR` that symlinks `settings.json` to a temp file, run `claude -p hi` and `claude config set` (or toggle a setting). Does the link survive, or does Claude Code replace it with a real file? Check the same for `history.jsonl`. Check whether `settings.local.json` holds anything login-specific. Any entry that gets replaced must be handled by linking its parent folder or by `check`'s repair hint. Adjust the list.
- [ ] **Step 2: Write failing tests** with temp dirs as HOME:
  - `TestSetupAccountDirCreatesLinks`: links exist and point to `home/.claude/<entry>`; entries missing in `~/.claude` are skipped.
  - `TestSetupAccountDirIdempotent`: a second run creates nothing.
  - `TestSetupAccountDirKeepsClaudeJSON`: a pre-existing `.claude.json` is byte-identical afterwards.
  - `TestCheckAccountDirDetectsRealFile`: replace a link with a real file → `dirProblem{Entry:"settings.json", Got:"real-file"}`.
  - `TestCheckAccountDirWrongTarget`.
- [ ] **Step 3: Run.** Expected: FAIL.
- [ ] **Step 4: Implement**, plus the lobby warning line (`⚠ account bespoke: 2 setup problems — run claudemux-head account check`), using the existing lobby notice mechanism.
- [ ] **Step 5: Run** the package tests. Expected: PASS.
- [ ] **Step 6: Commit** `feat(head): account setup/check/login/list`.

### Task 6: MCP server sync

**Files:**
- Create: `cmd/claudemux-head/mcpsync.go`, `cmd/claudemux-head/mcpsync_test.go`
- Modify: `accountcmd.go` (`account sync-mcp <name>`)

**Interfaces:**
- Produces: `func syncMCP(srcJSON, dstJSON, statePath string) (added, removed []string, err error)`
  - source = `~/.claude.json` (the `~/.claude` account's), dst = `<config_dir>/.claude.json`, state = `~/.claude/claudemux/mcp-sync-<account>.json` (a sorted list of names synced last time)
  - merge top-level `mcpServers` by name; source wins on a clash; names in state that are no longer in the source are removed from dst; names only in dst and never synced are kept
  - read dst, change only `mcpServers`, preserve every other key byte-for-byte as JSON values (decode into `map[string]json.RawMessage`), write to a temp file in the same dir, then rename
  - a missing dst file → create `{"mcpServers":{...}}`; a missing source → no-op
  - per-project `projects.*.mcpServers` untouched

- [ ] **Step 1: Write failing tests**: `TestSyncMCPAddsAndSourceWins`, `TestSyncMCPRemovesOnlySynced`, `TestSyncMCPPreservesOtherKeys` (dst has `oauthAccount` and `projects`; both are unchanged after the sync), `TestSyncMCPMissingDst`, `TestSyncMCPLeavesProjectServers`.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement.** Remove the `|| true` guard from Task 3 only if a sync failure should block launch. It shouldn't: keep the guard, but print the error to stderr.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(head): sync user MCP servers into account config dirs`.

### Task 7: Per-account meter caches (writers)

**Files:**
- Modify: `cmd/claudemux-head/statusline.go` (`defaultStatuslineCachePath`, `runStatusline`), `budget.go` (`defaultRateLimitsPath`), `usage.go` (`defaultUsageCachePath`, `fetchPlanUsage`, `refreshUsageCache`), `credits.go` (`creditProfile`)
- Test: `statusline_test.go`, `usage_test.go`, `credits_test.go`, `budget_test.go`

**Interfaces:**
- Consumes: Task 1 (`ForConfigDir`, `Multi`, `Default`, `ConfigDir`).
- Produces:
  - `func rateLimitsPathFor(account string, multi bool) string`. Returns the legacy `rate-limits.json` when `!multi`, else `rate-limits-<account>.json`.
  - `func usageCachePathFor(account string, multi bool) string`. Same pattern for `usage.json`.
  - `runStatusline` determines the account with `cfg.Claude.ForConfigDir(os.Getenv("CLAUDE_CONFIG_DIR"))` and writes `rateLimitsPathFor(acct, multi)`. If `multi` and acct is the default, it also writes the legacy path. If it can't map the dir, it writes nothing and logs once via debuglog. **Never guess.**
  - `fetchPlanUsage(ctx, claudeBin, configDir string, now)` spawns claude with `CLAUDE_CONFIG_DIR=configDir`, or with it removed (`filterEnv`) when `""`. `refreshUsageCache(ctx, path, claudeBin, configDir string, now)`.
  - `creditProfile(configDir string)` takes the dir instead of reading the env. It reads `<dir>/.claude.json` for a non-`~/.claude` account, and `~/.claude.json` otherwise. `creditToken`'s keychain service hashing already matches; pass it the same dir.
  - `CLAUDEMUX_RATE_LIMITS_PATH` still overrides everything (tests rely on it).

- [ ] **Step 1: Write failing tests:**
  - `TestRateLimitsPathFor`: single → `…/claudemux/rate-limits.json`; multi bespoke → `…/rate-limits-bespoke.json`.
  - `TestStatuslineWritesAccountFile`: set `CLAUDE_CONFIG_DIR` to the ameriglide dir (multi, default=ameriglide) → writes `rate-limits-ameriglide.json` and `rate-limits.json`. With it unset → writes only `rate-limits-bespoke.json`. With an unknown dir → writes nothing.
  - `TestFetchPlanUsageSetsConfigDir`: use the existing fake-claude-bin seam in `usage_test.go`, and assert the child env has / lacks `CLAUDE_CONFIG_DIR`.
  - `TestUsageLocksAreSeparate`: two accounts' lock/failed markers are distinct paths, and a failure marker on one doesn't block the other (`usageBackoffActive`).
  - `TestCreditProfileUsesAccountDir`: a temp `<dir>/.claude.json` with an org → that org is returned.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement.** Update every existing caller to pass the head's or lobby's account, using a new helper in `accounts.go`: `func sessionAccount() (name, configDir string)`, built from `$CLAUDE_CONFIG_DIR` via `ForConfigDir`, falling back to `Default()`. Task 3 starts the head pane with the account's env, so the head sees it there.
- [ ] **Step 4: Run** the whole package. Expected: PASS.
- [ ] **Step 5: Commit** `feat(head): per-account rate-limit, usage and credit caches`.

### Task 8: Head shows its own account

**Files:**
- Modify: `cmd/claudemux-head/tui.go` (model fields, init: `rateLimitsPath`, `usageCachePath`, `renderMetersLine`)
- Test: `tui_test.go`

**Interfaces:**
- Consumes: Task 7 helpers and `sessionAccount()`.
- Produces: `model.account string`, `model.accountColor string`. `renderMetersLine` prefixes an account badge when `cfg.Claude.Multi()`.

- [ ] **Step 1: Write failing tests:** `TestHeadUsesAccountCaches` (a head model built with account bespoke in multi mode has `rateLimitsPath` ending in `rate-limits-bespoke.json` and `usageCachePath` in `usage-bespoke.json`); `TestMetersLineAccountBadge` (multi → the rendered line contains `bespoke`; single → it does not, and the line is byte-identical to today's golden output).
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement.** Badge style: the account's `color`, falling back to a stable colour chosen by hashing the name over the existing palette used for project colours. Keep it to the name in a coloured fg; no new box.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(head): account badge and per-account meters in the head`.

### Task 9: Lobby and web page, per-account meter rows and session badges

**Files:**
- Modify: `cmd/claudemux-head/switchboardtui.go` (`swModel` usage/rate-limit fields → per account, `swPollCmd`, `swMetersLine`, the session list row, burn-sample history), `webfleet.go` / `webheadline.go` / `webpage.html` (per-account rows)
- Test: `switchboardtui_test.go`, the web tests that exist (`webfleet_test.go` etc.)

**Interfaces:**
- Consumes: Tasks 4 and 7 (`@claudemux_account` per session, per-account cache paths).
- Produces:
  - `swSnapshot` session rows gain `account string` (one more `#{@claudemux_account}` field appended to the `list-sessions` format in `swPollCmd`)
  - `swSnapshotMsg.rl` becomes `map[string]RateLimits` (with `rlErr map[string]error`). Usage state, burn samples and quiet-until are kept per account (`map[string]...`).
  - `func swMeterAccounts(cfg ClaudeConfig, snap swSnapshot) []string`: accounts with at least one live session, in config order (default first). In single mode → `[implicitAccount]`.
  - `swMetersLine` renders one line per account from that list, each with its badge in multi mode. The lobby header height grows by the number of extra lines.
  - Session rows show the badge in multi mode.
  - Web JSON gains `accounts: [{name, color, meters...}]`. The single-account shape stays, with the same top-level fields, so old pages work.

- [ ] **Step 1: Write failing tests:**
  - `TestSwMeterAccountsOnlyLive`: two accounts configured, only bespoke sessions live → `["bespoke"]`; both live → `["ameriglide","bespoke"]`.
  - `TestSwMetersLinePerAccount`: two rows, each showing its own percentages from its own cache. One account's `rlErr` doesn't blank the other's row.
  - `TestSwMetersLineSingleUnchanged`: single mode output identical to the current golden output.
  - `TestSwSessionRowBadge`: multi → the row contains the account name; single → it doesn't.
  - `TestSwPollParsesAccount`: the session listing parser picks up the new field. An older tmux line without it → `""`.
  - A web test: `accounts` array present in multi mode, and the legacy fields are unchanged in single mode.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement.** Idle accounts (no live session) are **hidden** (the spec leaves hide-vs-dim to the builder; hide is simpler). The usage poll spawns at most one claude per account per `usageTTL`, through the existing single-flight lock, per account path.
- [ ] **Step 4: Run** the whole package, then `go vet ./...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(lobby): per-account meter rows and session badges`.

### Task 10: Docs and the example config

**Files:**
- Modify: `README.md` (new "Multiple Claude accounts" section: config block, `claude_account:`, `-a`, `account setup/check/login/list`, resume rules, "MCP logins are per account"), the commented example config the onboard/`config` writer emits (grep for where `onepassword:` example text lives), `headhelp.go` if not done yet
- Test: any existing test that checks the example config parses (`config_test.go`); add the `claude:` block, commented out, to that example.

- [ ] **Step 1:** Update the example config text. Run `go test ./cmd/claudemux-head -run Config`. Expected: PASS.
- [ ] **Step 2:** Write the README section (short; mirror the spec's Design section in user terms).
- [ ] **Step 3:** Run `go build ./... && go test ./...`. Expected: PASS.
- [ ] **Step 4: Commit** `docs: multiple Claude accounts`.

### Task 11 (originating session, with Michael; not a subagent): migration

Not code. It's done after the branch is merged and installed (see the `/done` procedure in memory):
1. Add the `claude:` block to `~/.config/claudemux/config.yml`, with `default_account: ameriglide`, `ameriglide.config_dir: ~/.claude-accounts/ameriglide`, `bespoke: {}`. Do it **before** `go install`, because binwatch restarts the lobby.
2. `claudemux-head account setup ameriglide`, then `account check`.
3. Fix `oauthAccount` in `~/.claude.json` (`/login` as Bespoke in a `~/.claude` session).
4. Add `claude_account: bespoke` to `~/Projects/{homelife,homelife-salesforce,scala3d}/.project.yml`.
5. Live checks from the spec's Testing section: a fresh scala3d → `claude auth status` says Bespoke; a fresh claudemux → AmeriGlide; the lobby shows two meter rows; `/mcp` logins on the AmeriGlide account.
