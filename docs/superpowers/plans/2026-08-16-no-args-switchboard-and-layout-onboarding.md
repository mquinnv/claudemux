# Plan: bare `claudemux` opens the switchboard + layout onboarding

Approved design (from brainstorming, 2026-08-16):

- Bare `claudemux` (no args) always opens the switchboard — creating the
  lobby session if needed, `switch-client` when already inside tmux —
  instead of today's attach-sole-session / create-in-`$PWD` behavior.
  `claudemux <dir|query>` remains the way to launch a project session.
- New persisted layout choice `launch.layout` in
  `~/.config/claudemux/config.yml`, values: `shell-right` (default),
  `no-shell`, `shell-bottom`, `head-bottom`.
- A first-run onboarding picker (`claudemux-head onboard`, a bubbletea TUI)
  shows the four layouts as ASCII diagrams and writes the choice to
  config.yml, preserving comments and existing keys. It triggers once from
  the launcher when the key is unset and stdin/stdout are a TTY;
  `claudemux setup` re-runs it anytime.
- `create_session` builds panes per the chosen layout. The head pane is
  ALWAYS full window width and pinned to 4 rows (this changes today's
  default too: the shell pane is no longer full height).

## Global Constraints

- The head pane spans the full window width in every layout and is pinned
  to exactly 4 rows; the existing `window-resized` hook
  (`resize-pane -t <head_pane> -y 4`) must keep working in all layouts.
- Layout names are exactly: `shell-right`, `no-shell`, `shell-bottom`,
  `head-bottom`. Empty string means "never chose" and behaves as
  `shell-right`. Any other value in config.yml is a validation error
  (config.yml exists-but-invalid is fatal, matching existing behavior).
- The shell pane is 30% width in `shell-right`/`head-bottom`, 20% height in
  `shell-bottom`.
- `config get` exit-code contract (0/1/2/3, see configget.go) must not
  change. `config get launch.layout` returns exit 0 with an empty line when
  unset (string zero value) — bin/claudemux relies on this to detect
  "never chose".
- config.yml writes must preserve user comments and unrelated keys, be
  atomic (temp file + rename in the same directory), and never write a
  config that loadConfig would reject.
- bin/claudemux stays `set -euo pipefail`-safe: no helper may leak a
  non-zero status that kills the launcher (see ch_config_get / ensure_hook
  for the established pattern).
- Go work is TDD: failing test first, then implementation. Run
  `go test ./...` and `go build ./...` before every commit.
- All work happens in this worktree
  (`.claude/worktrees/no-args-opens-switchboard`, branch
  `worktree-no-args-opens-switchboard`). Do not dispatch subagents; do not
  cd elsewhere.

## Task 1 — `launch.layout` config field + validation

Files: `cmd/claudemux-head/config.go`, `cmd/claudemux-head/config_test.go`
(or the existing config test file), `cmd/claudemux-head/configget_test.go`.

1. Add to `LaunchConfig`:

   ```go
   // Layout is the pane arrangement bin/claudemux builds for a new session,
   // chosen by `claudemux-head onboard` (or hand-set). Empty means the user
   // has never chosen: the launcher treats it as shell-right AND knows to
   // offer onboarding. Values: shell-right, no-shell, shell-bottom,
   // head-bottom.
   Layout string `yaml:"layout"`
   ```

2. In `Config.validate()`, reject any `Launch.Layout` outside
   {"", "shell-right", "no-shell", "shell-bottom", "head-bottom"} with an
   error naming the bad value and listing the legal ones.

3. Tests:
   - `config get launch.layout` with no config file → exit 0, empty output
     (this is the launcher's "never chose" probe).
   - With `launch:\n  layout: no-shell` in config.yml → exit 0, prints
     `no-shell`.
   - `layout: sideways` → loadConfig error mentioning `launch.layout`
     (and `config get` exit 3).

Follow the existing test style in configget_test.go (they set
XDG_CONFIG_HOME to a temp dir).

## Task 2 — comment-preserving config writer + `config set`

Files: new `cmd/claudemux-head/configset.go` + `configset_test.go`,
`cmd/claudemux-head/main.go`.

1. Implement `runConfigSet(args []string, stdout, stderr io.Writer) int`
   for `claudemux-head config set <dotted.path> <value>`:
   - Parse `~/.config/claudemux/config.yml` (respecting XDG_CONFIG_HOME,
     via the existing `configDir()`) into a `yaml.Node` tree so comments
     and formatting survive. A missing file starts from an empty document;
     create the config dir if needed (0700 dir, 0600 file is fine).
   - Walk/insert the dotted path (mapping nodes at each level; creating
     intermediate mappings that don't exist), set the leaf as a string
     scalar.
   - Before writing, marshal the mutated tree and decode it through the
     same strict path `loadConfig` uses (`KnownFields(true)` into `Config`,
     then `validate()`). If that fails, write nothing and print the error:
     `config set` must never produce a config.yml that bricks the next
     launch. This also makes `config set launch.layout sideways` fail
     loudly via Task 1's validation.
   - Atomic write: temp file in the same directory + `os.Rename`.
   - Exit codes: 0 written; 2 usage (wrong arg count); 3 config.yml exists
     but cannot be parsed, or the resulting config is invalid, or the
     write fails (message on stderr).
2. Dispatch in main.go beside `config get` (same pre-flag.Parse block),
   usage line updated to mention both verbs.
3. Tests (table-driven, temp XDG_CONFIG_HOME):
   - No config.yml → `config set launch.layout no-shell` creates the file;
     `loadConfig` then reports Layout "no-shell".
   - Existing config.yml with a comment (`# my note`) and
     `summary:\n  model: claude-haiku-4-5` → after
     `config set launch.layout head-bottom`, the comment AND
     `summary.model` survive verbatim, and layout reads back.
   - Existing `launch:\n  auto_worktree: true` → setting layout keeps
     auto_worktree true (merge into existing mapping, no duplicate
     `launch:` key).
   - `config set launch.layout sideways` → exit 3, file untouched.
   - Malformed config.yml → exit 3, file untouched.

## Task 3 — `claudemux-head onboard` picker TUI

Files: new `cmd/claudemux-head/onboard.go` + `onboard_test.go`,
`cmd/claudemux-head/main.go`.

1. `runOnboard(stderr io.Writer) int`: a small bubbletea program (style
   reference: boot.go — plain model, no altscreen needed, lipgloss for
   emphasis) that:
   - Shows the four layouts side by side (or stacked two-per-row when
     narrow) with these exact diagrams and labels, current selection
     highlighted:

     ```
     1) shell-right          2) no-shell
     ┌──────────────┐        ┌──────────────┐
     │ head         │        │ head         │
     ├────────┬─────┤        ├──────────────┤
     │ claude │shell│        │ claude       │
     │        │     │        │              │
     └────────┴─────┘        └──────────────┘

     3) shell-bottom         4) head-bottom
     ┌──────────────┐        ┌────────┬─────┐
     │ head         │        │ claude │shell│
     ├──────────────┤        │        │     │
     │ claude       │        ├────────┴─────┤
     │              │        │ head         │
     ├──────────────┤        └──────────────┘
     │ shell        │
     └──────────────┘
     ```

   - Keys: left/right/up/down/h/j/k/l and 1-4 move the selection; enter
     confirms; q/esc/ctrl+c cancels.
   - On confirm: persist via the Task 2 write path (call the same internal
     helper `runConfigSet` uses — factor a `configWrite(path, value
     string) error` if that is cleaner), print
     `claudemux: layout saved — change it anytime with: claudemux setup`
     and exit 0.
   - On cancel: write NOTHING, exit 1 with a one-line note that the
     default layout applies until `claudemux setup` is run.
   - If saving fails, print the error and exit 3.
2. Dispatch `onboard` in main.go's pre-flag.Parse block.
3. Tests: bubbletea model unit tests in the existing style (see
   boot/switchboard tests) — selection movement, number keys, enter
   produces the chosen layout name, esc cancels without a write (inject
   the writer as a func field so tests observe calls without touching
   disk).

## Task 4 — launcher: no-args → switchboard, `setup` subcommand, onboarding trigger

Files: `bin/claudemux`, `README.md` (usage sections).

1. Extract the existing `[ "${1:-}" = "switch" ]` block body
   (bin/claudemux:600-636) into a function `open_switchboard()` placed with
   the other functions; keep its behavior and comments intact (lobby
   session creation, prefix+S binding, client-session-changed tab-color
   hook, switch-client vs attach). `claudemux switch` calls it.
2. Replace the no-args branch (the attach-sole-session / create-`$PWD`
   logic at bin/claudemux:638-647) with a call to `maybe_onboard` followed
   by `open_switchboard`. Bare `claudemux` now ALWAYS means the
   switchboard. Update the file's header usage comment accordingly
   (`claudemux [-n] [-d] [-w|-W] [switch|setup|dir|query ...]`).
3. New `setup` subcommand branch (beside `switch`): runs
   `claudemux-head onboard` on the current terminal and exits with its
   status. No tmux involvement.
4. New function `maybe_onboard`: runs `claudemux-head onboard` once when
   ALL hold — `claudemux-head` is on PATH; stdin AND stdout are TTYs
   (`[ -t 0 ] && [ -t 1 ]`); `$DETACHED` is false; `config get
   launch.layout` prints empty with exit 0 (use ch_config_get so a broken
   config surfaces once and skips onboarding rather than killing the
   launch). A cancelled or failed onboard is non-fatal: the launch
   continues on the default layout. Call `maybe_onboard` from BOTH the
   no-args path and the argument path (before create_session) — but it
   must run at most once per invocation.
5. README: document the new no-args behavior, `setup`, `launch.layout`,
   and the four layouts. Keep the existing doc voice.

Constraint: `setup` and `switch` remain reserved words that shadow
directories of the same name — extend the existing collision comment
rather than duplicating it.

## Task 5 — launcher: layout-aware `create_session`

Files: `bin/claudemux`, `README.md` if its layout diagram needs updating.

1. In `create_session`, resolve the layout once:
   `ch_config_get launch.layout`; empty or anything unrecognized (defense
   only — the head validates) behaves as `shell-right`.
2. Build panes per layout. Pane creation ORDER is what makes the head full
   width without needing `split-window -f` (the vertical head split happens
   while the window still has a single column) — preserve these sequences:
   - `shell-right`: pane0 = claude; head = `split-window -v -b -l 4` from
     claude (full-width top); shell = `split-window -h -l '30%'` from
     claude.
   - `no-shell`: pane0 = claude; head = `split-window -v -b -l 4`. No shell
     pane at all.
   - `shell-bottom`: pane0 = claude; head = `split-window -v -b -l 4`;
     shell = `split-window -v -l '20%'` from claude (short, bottom).
   - `head-bottom`: pane0 = claude; head = `split-window -v -l 4` (below,
     full width); shell = `split-window -h -l '30%'` from claude.
3. Everything downstream keys off pane IDs already; make the shell-pane
   variable empty in `no-shell` and guard its uses: deferred_launch's
   shell respawn must skip an empty/missing shell pane (pane_cmd already
   returns "" for a gone pane — keep that behavior for an empty ID too,
   without tripping `set -u`).
4. The `window-resized` hook line stays exactly as-is (it targets the head
   pane by ID).
5. Update the layout comment block above the split calls
   (bin/claudemux:525-537) to describe the four layouts.
6. Verification (manual, run each): for L in shell-right no-shell
   shell-bottom head-bottom: write `launch:\n  layout: L` to a temp
   XDG_CONFIG_HOME config, `claudemux -d /tmp/somedir` (detached), then
   `tmux list-panes -t <session> -F '#{pane_id} #{pane_width}x#{pane_height} #{pane_current_command}'`
   and confirm: head pane width == window width and height 4; shell
   present/absent and on the expected side; claude pane focused. Kill the
   test sessions afterwards. Record the four outputs in the task report.

Note: `claudemux -d` prints the session name instead of attaching — use it
for these checks so no client is stolen.
