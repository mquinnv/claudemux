# Defer Clears With Its Blocker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A defer linked to another claudemux session clears itself, and nudges the waiting agent, once that session is out of the way.

**Architecture:** A deferred session carries an optional link (tmux session options) to the session it waits on. The waiting session's own head reads the fleet listing on its normal poll, runs a pure verdict plus a small grace-period state machine, and then clears the mark and, when enabled, types a nudge into its claude pane. The handoff skill sets the link through a new `claudemux-head defer` subcommand; the `d`/`D` prompts set it when the blocker is another session's name, with `Tab` completion.

**Tech Stack:** Go, bubbletea, tmux user options. Everything lives in `cmd/claudemux-head/` (package `main`).

**Spec:** `docs/superpowers/specs/2026-11-14-defer-clears-with-blocker-design.md`

## Global Constraints

- All paths below are relative to `cmd/claudemux-head/` unless they start with `docs/` or `README.md`.
- Run tests from the repo root: `go test ./cmd/claudemux-head/ -run <Name>`; finish every task with `go build ./... && go vet ./cmd/claudemux-head/ && go test ./cmd/claudemux-head/`.
- Option names, exactly: `@claudemux_defer_on`, `@claudemux_defer_on_created`, `@claudemux_defer_on_seen` (value `1`).
- Every path that clears a defer unsets the mark, the reason and all three link options in ONE `;`-chained tmux invocation. Every path that sets a defer without a link unsets the three link options in that same invocation.
- Free-text defers behave exactly as today. A defer with no link is never auto-cleared.
- A failed or partial tmux read is never evidence that a blocker is gone. Only a successful listing that lacks the session is.
- The nudge never sends `C-u` and never types into a prompt that is not verifiably empty.
- `defer.auto_nudge` gates only the typed prompt. The automatic clear always happens.
- Grace period: `deferGrace = 20 * time.Second`.
- Follow the file's existing comment style: each new function gets a comment saying why, not what.
- Commit after each task with a conventional-commit subject and these trailers:
  ```
  Co-Authored-By: Claude Fable 5.2 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_016sp4EJSkwVaLRKpR1A9X2Z
  ```

## Review Focus

1. **A tmux read fails for one poll while a link is set.** Expected: the mark stays and no grace period starts. Pinned in Task 2 (`TestBlockerVerdictUnreadable`) and Task 5 (`TestDeferWatchSkipsFailedRead`).
2. **The blocker's name is reused by a new session** (old one closed, `phenix-2` launched again). Expected: the old blocker reads as gone, the mark clears; the newcomer is not adopted as the blocker. Pinned in Task 2 (`TestBlockerVerdictRecycledName`).
3. **The user re-defers or edits the blocker during the grace period.** Expected: the grace period belongs to the old link and is dropped; nothing is cleared or typed for the new mark. Pinned in Task 2 (`TestDeferWatchResetsOnLinkChange`).
4. **Session names that are prefixes of each other** (`phenix`, `phenix-2`) in completion and in the exact-name link. Expected: `phenix` links to `phenix`, never `phenix-2`; `Tab` cycles both. Pinned in Task 7.
5. **A session is asked to wait on itself** (`defer --wait-on` with its own name, or its own name typed in the prompt). Expected: refused by the subcommand, treated as free text by the prompt. Pinned in Tasks 6 and 7.

---

### Task 1: The link on the mark

**Files:**
- Modify: `deferpub.go`
- Test: `deferpub_test.go`

**Interfaces:**
- Produces:
  ```go
  const (
      deferOnOption        = "@claudemux_defer_on"
      deferOnCreatedOption = "@claudemux_defer_on_created"
      deferOnSeenOption    = "@claudemux_defer_on_seen"
  )
  // deferLink names the session a defer waits on. The zero value is "no link".
  type deferLink struct{ On, Created string }
  func (l deferLink) set() bool // On != "" && Created != "" && Created != "0"

  // deferMark is everything published about one session's defer.
  type deferMark struct {
      Raw, Reason string
      Link        deferLink
      Seen        bool
  }
  func deferLinkedArgs(target, value, reason string, link deferLink) []string
  func deferSeenArgs(target string) []string               // sets deferOnSeenOption to "1"
  func setDeferLinkedCmd(target, value, reason string, link deferLink) tea.Cmd
  func readDeferMark(ctx context.Context, target string) (deferMark, bool) // ok=false on a failed read
  func parseDeferMark(out string) deferMark
  ```
- `deferValueArgs` keeps its signature; `readDeferOption` is removed (its two callers move to `readDeferMark` in Task 5; until then keep it as a thin wrapper).

- [ ] **Step 1: Write the failing tests** in `deferpub_test.go`:
  - `TestDeferArgsClearUnsetsLink`: `deferArgs("%1", false, "")` contains `-u` for all five options (mark, reason, three link options) and is one argv (tmux commands joined by `";"`).
  - `TestDeferArgsPlainSetUnsetsLink`: `deferArgs("%1", true, "CI")` sets the mark and reason and unsets the three link options.
  - `TestDeferLinkedArgs`: `deferLinkedArgs("%1", deferWaitValue, "waiting on phenix-2: fix", deferLink{"phenix-2", "1763000000"})` sets mark `wait`, the reason, `@claudemux_defer_on phenix-2`, `@claudemux_defer_on_created 1763000000`, and unsets `@claudemux_defer_on_seen` (a new link starts unseen).
  - `TestDeferLinkedArgsEmptyLinkIsPlain`: a zero `deferLink` yields exactly `deferValueArgs(target, true, value, reason)`.
  - `TestDeferLinkedArgsReasonSemicolon`: a reason ending in `;` is still escaped to `\;` with a link present.
  - `TestParseDeferMark`: `"wait\twaiting on x\tphenix-2\t1763000000\t1\n"` parses to all fields; `"\t\t\t\t\n"` is the zero mark; a created of `"0"` gives `Link.set() == false`.
- [ ] **Step 2: Run** `go test ./cmd/claudemux-head/ -run 'TestDefer|TestParseDeferMark'` — expect FAIL (undefined names).
- [ ] **Step 3: Implement** the Produces block. `readDeferMark` is one `display-message -p` with the five options tab-separated, mark first, reason second (so `readDeferOption` can wrap it).
- [ ] **Step 4: Run** the same tests, then the full package — expect PASS.
- [ ] **Step 5: Commit** `feat(defer): a defer can link to the session it waits on`.

---

### Task 2: The verdict and the grace-period machine

**Files:**
- Create: `deferwatch.go`
- Test: `deferwatch_test.go`

**Interfaces:**
- Consumes: `deferLink`, `isWaiting`, `isBooting`, `isDeferValue`, `statePublishOption`, `deferOption`.
- Produces:
  ```go
  const deferGrace = 20 * time.Second

  // fleetRow is one session as the watcher and the completer see it.
  type fleetRow struct {
      Name, Created, State string
      Deferred             bool
      Parent               string // @claudemux_parent, for ordering completion
  }
  const fleetFormat = "#{session_name}\t#{session_created}\t#{@claudemux_state}\t#{@claudemux_defer}\t#{@claudemux_parent}"
  func parseFleet(listing string) []fleetRow // listing order kept; malformed lines skipped

  type blockerVerdict int
  const (
      blockerHold    blockerVerdict = iota // still in the way, or not yet seen working
      blockerWorking                        // in the way AND working right now: mark it seen
      blockerStopped                        // seen working, now waiting on a human, not deferred
      blockerGone                           // no live session with that name and creation time
  )
  // rowsOK is false when the listing could not be read: always blockerHold.
  func judgeBlocker(link deferLink, seen bool, rows []fleetRow, rowsOK bool) blockerVerdict

  type deferWatchAction int
  const (
      watchNone deferWatchAction = iota
      watchMarkSeen
      watchClear      // clear the mark, no nudge
      watchClearNudge // clear the mark and nudge
  )
  type deferWatchInput struct {
      Now          time.Time
      Mark         deferMark
      MarkOK       bool // the mark read succeeded
      Verdict      blockerVerdict
      OwnIdle      bool // this session is idle at its prompt (StateIdle)
      OwnWorking   bool // this session is mid-turn
      NudgeEnabled bool
  }
  // deferWatch is the head's memory between polls. Zero value is ready.
  type deferWatch struct {
      graceSince time.Time
      graceLink  deferLink
      graceGone  bool
  }
  func (w *deferWatch) step(in deferWatchInput) (act deferWatchAction, gone bool)
  ```

`step` rules, in order:
1. `!in.MarkOK` → `watchNone`, state untouched.
2. Mark not deferred, or link not set → reset state, `watchNone`.
3. Link differs from `graceLink` while a grace period runs → reset state, then continue.
4. Verdict `blockerWorking` → reset grace; `watchMarkSeen` if `!Mark.Seen`, else `watchNone`.
5. Verdict `blockerHold` → reset grace, `watchNone`.
6. Verdict `blockerStopped`/`blockerGone`: if no grace running → start it (`graceSince = Now`) and record whether `OwnWorking` was true at that moment, `watchNone`. Else if `Now - graceSince < deferGrace` → `watchClear` (reset) only if own was NOT working at grace start and `OwnWorking` is true now; otherwise `watchNone`. Else (grace expired) reset and return `watchClearNudge` when `NudgeEnabled && OwnIdle` and own was not working at grace start, otherwise `watchClear` (a waiter that was mid-turn at grace start is never nudged, even if idle by now). A blocker that resumes (working or hold verdict) abandons the grace (rules 4 and 5); a link change drops it (rule 3); a failed mark read leaves it untouched (rule 1). `gone` reports `blockerGone`.
   Amended after the whole-branch review: the original rule cleared at once whenever the waiter was mid-turn, which dropped the mark while a handoff's peer was between turns.

`judgeBlocker`: "working" is a non-empty state that is neither `isWaiting` nor `isBooting`. A blocker that is waiting and deferred is `blockerHold`. A blocker in `Starting` is `blockerHold`.

- [ ] **Step 1: Write the failing tests** in `deferwatch_test.go`:
  - `TestParseFleet`: three lines, one malformed, order kept, `Deferred` true for `1` and `wait`.
  - `TestJudgeBlocker` (table): working → `blockerWorking`; idle+seen → `blockerStopped`; idle+unseen → `blockerHold`; `Asking`+seen → `blockerStopped`; idle+seen+deferred → `blockerHold`; `Starting`+seen → `blockerHold`; absent → `blockerGone`.
  - `TestBlockerVerdictRecycledName`: a row with the link's name but another creation time → `blockerGone`.
  - `TestBlockerVerdictUnreadable`: `rowsOK=false` with empty rows → `blockerHold`.
  - `TestDeferWatchGraceThenNudge`: stopped at t0 → none; t0+19s → none; t0+20s, idle, nudge on → `watchClearNudge`.
  - `TestDeferWatchNudgeOff`: same, nudge off → `watchClear`.
  - `TestDeferWatchNotIdleNoNudge`: at t0+20s `OwnIdle=false`, `OwnWorking=false` (asking) → `watchClear`.
  - `TestDeferWatchOwnTurnClearsEarly`: stopped at t0, `OwnWorking` at t0+5s → `watchClear`.
  - `TestDeferWatchBlockerResumes`: stopped at t0, working at t0+5s, stopped at t0+10s → grace restarts; nothing before t0+30s.
  - `TestDeferWatchMarksSeenOnce`: working+unseen → `watchMarkSeen`; working+seen → `watchNone`.
  - `TestDeferWatchResetsOnLinkChange`: stopped on link A at t0, link B (hold) at t0+10s, link B stopped at t0+25s → none (new grace), not a clear.
  - `TestDeferWatchSkipsFailedMarkRead`: grace running, `MarkOK=false` at t0+25s → `watchNone`; next good poll → acts.
  - `TestDeferWatchGoneFlag`: `blockerGone` path returns `gone=true`.
  - `TestDeferWatchUnlinkedNever`: deferred with zero link, any verdict → `watchNone` forever.
- [ ] **Step 2: Run** `go test ./cmd/claudemux-head/ -run 'TestParseFleet|TestJudgeBlocker|TestBlockerVerdict|TestDeferWatch'` — expect FAIL.
- [ ] **Step 3: Implement** `deferwatch.go` per the rules above.
- [ ] **Step 4: Run** the same tests and the full package — expect PASS.
- [ ] **Step 5: Commit** `feat(defer): verdict and grace period for a linked blocker`.

---

### Task 3: `defer.auto_nudge` config

**Files:**
- Modify: `config.go` (struct near `TeardownConfig`, `defaultConfig`), `configset.go` / `configget.go` only if keys are enumerated there (check how `teardown.command` is reached and mirror it), `README.md` config section
- Test: `config_test.go`, `configset_test.go`

**Interfaces:**
- Produces: `Config.Defer DeferConfig` with `yaml:"defer"`; `type DeferConfig struct { AutoNudge bool `yaml:"auto_nudge"` }`; default `true`.

- [ ] **Step 1: Write the failing tests:** `TestDeferAutoNudgeDefaultsOn` (no `defer` block → true); `TestDeferAutoNudgeOff` (`defer:\n  auto_nudge: false` → false); `TestConfigSetDeferAutoNudge` (`config set defer.auto_nudge false` then `config get defer.auto_nudge` prints `false`), written the way the neighbouring set/get tests are.
- [ ] **Step 2: Run** `go test ./cmd/claudemux-head/ -run 'TestDeferAutoNudge|TestConfigSetDeferAutoNudge'` — expect FAIL.
- [ ] **Step 3: Implement.** Comment the struct: it gates the typed prompt only; the clear always happens. Add the key to the README's config reference with one sentence.
- [ ] **Step 4: Run** tests — expect PASS.
- [ ] **Step 5: Commit** `feat(config): defer.auto_nudge, on by default`.

---

### Task 4: The nudge

**Files:**
- Create: `defernudge.go`, `testdata/prompt_empty.txt`, `testdata/prompt_draft.txt`, `testdata/prompt_asking.txt`
- Test: `defernudge_test.go`

**Interfaces:**
- Consumes: `sendLiteralArgs`, `sendEnterArgs`, `teardownTmuxTimeout`, `teardownKeyDelay`, `mappedTranscript`, `swTmux`, `deferArgs`.
- Produces:
  ```go
  // promptEmpty reports whether a capture-pane of the claude pane shows its
  // input line empty. Anything it cannot recognise is false.
  func promptEmpty(capture string) bool
  func deferNudgeText(blocker string, gone bool, reason string) string
  func deferDoneChip(blocker string, gone bool) string
  // deferReleaseCmd clears selfPane's defer and, when nudge is true and the
  // prompt is empty, types text into the claude pane and submits it.
  func deferReleaseCmd(selfPane, paneDir, text string, nudge bool) tea.Cmd
  type deferReleasedMsg struct{ nudged bool }
  ```
- Exact copy:
  - stopped: `claudemux: <blocker> has stopped, so this session is no longer deferred on it (<reason>). Check for its report, then pick up where you left off.`
  - gone: `claudemux: <blocker> has closed, so this session is no longer deferred on it (<reason>). Check what it left behind, then pick up where you left off.`
  - With an empty reason, or a reason equal to the blocker's name, drop the ` (<reason>)` part.
  - chip: `◆ <blocker> finished` / `◆ <blocker> closed`, rendered with `swDeferStyle`.

- [ ] **Step 1: Capture the fixtures.** In a scratch tmux session running `claude`, run `tmux capture-pane -p -t <pane>` three times: empty prompt, a half-typed draft, an `AskUserQuestion` dialog. Save them under `testdata/` with any project-identifying text replaced. Look at how `permit_test.go` and `permissionDialogUp` recognise the pane and follow that approach for the input line.
- [ ] **Step 2: Write the failing tests:** `TestPromptEmpty` over the three fixtures (true, false, false) plus `""` → false; `TestDeferNudgeText` for the four copy cases above; `TestDeferDoneChip` (strip ANSI, compare text).
- [ ] **Step 3: Run** `go test ./cmd/claudemux-head/ -run 'TestPromptEmpty|TestDeferNudge|TestDeferDoneChip'` — expect FAIL.
- [ ] **Step 4: Implement.** `deferReleaseCmd` order: resolve the claude pane with `mappedTranscript`; when `nudge`, capture it and test `promptEmpty`; if empty, send the literal, sleep `teardownKeyDelay`, send Enter, each tmux call under its own `teardownTmuxTimeout`; then clear the mark with `deferArgs(selfPane, false, "")` regardless of how the nudge went. The mark is cleared last so a conducting lobby does not carry the user in while the text is being typed.
- [ ] **Step 5: Run** tests — expect PASS.
- [ ] **Step 6: Commit** `feat(defer): nudge text and the release command`.

---

### Task 5: Wire the watcher into the head

**Files:**
- Modify: `tui.go` (model fields near `deferRaw` at ~304, the poll at ~1150-1215, `dataMsg`, the `dataMsg` branch of `Update` at ~1702, `deferChipText` at ~2943), `headrecord.go` (`recordDeferKey`, callers of `deferRaw`), `main.go` or wherever the model is built from `Config`
- Test: `tui_test.go` (or a new `deferwire_test.go` if `tui_test.go` has no model-construction helper to reuse)

**Interfaces:**
- Consumes: Tasks 1, 2, 3, 4.
- Produces on `model`: `deferMark deferMark` (replacing `deferRaw`/`deferReason`), `fleet []fleetRow`, `fleetOK bool`, `deferWatch deferWatch`, `deferAutoNudge bool`, `deferDoneNote string`. `dataMsg` gains `deferMark deferMark`, `deferMarkOK bool`, `fleet []fleetRow`, `fleetOK bool`. New helper `readFleet(ctx) ([]fleetRow, bool)` in `deferwatch.go`.

- [ ] **Step 1: Write the failing tests:**
  - `TestDeferWatchSkipsFailedRead`: a model with a linked mark receives a `dataMsg` with `fleetOK=false` 30 seconds running → no command that clears the mark, mark unchanged.
  - `TestHeadReleasesWhenBlockerStops`: linked+seen mark, fleet shows the blocker `Idle`, model idle; feed `dataMsg`s 21 seconds apart → the second returns a command and `deferDoneNote` is `deferDoneChip("phenix-2", false)`.
  - `TestHeadDoneNoteClearsOnNextTurn`: after the release, a poll in which the model's state is mid-turn empties `deferDoneNote`.
  - `TestHeadMarksSeen`: linked+unseen mark, blocker `Thinking` → a command is returned and no release happens.
  - `TestDeferChipTextShowsDoneNote`: no mark, `deferDoneNote` set → `deferChipText()` returns it.
- [ ] **Step 2: Run** `go test ./cmd/claudemux-head/ -run 'TestDeferWatchSkipsFailedRead|TestHead|TestDeferChipTextShowsDoneNote'` — expect FAIL.
- [ ] **Step 3: Implement.**
  - Poll: replace `readDeferOption` with `readDeferMark`, add `readFleet` on the same context. Delete the `readDeferOption` wrapper.
  - `Update`: after state is recomputed for the poll, build `deferWatchInput` (`OwnIdle` is `m.state.Kind == StateIdle`; `OwnWorking` is a state that is neither waiting-on-human nor booting; reuse the head's existing turn helpers rather than inventing a classification), call `m.deferWatch.step`, and map the action: `watchMarkSeen` → a fire-and-forget tmux command from `deferSeenArgs`; `watchClear` → `deferReleaseCmd(..., false)`; `watchClearNudge` → `deferReleaseCmd(..., true)` with `deferNudgeText`. Set `deferDoneNote` on both clear actions.
  - A release in flight must not be issued twice: after a clear action, ignore the watcher until a poll shows the mark unset.
  - Update every other reader of `m.deferRaw`/`m.deferReason` to the new field.
- [ ] **Step 4: Run** tests and the full package — expect PASS.
- [ ] **Step 5: Commit** `feat(head): release a linked defer when its blocker is out of the way`.

---

### Task 6: `claudemux-head defer` and the handoff skill

**Files:**
- Create: `defercmd.go`
- Modify: `main.go` (dispatch and `knownSubcommands`), `skills/claudemux-handoff/SKILL.md` (step 4 and the closing section), `headhelp.go` if it lists subcommands
- Test: `defercmd_test.go`, `skills_test.go`, `main_test.go` (known-subcommand list)

**Interfaces:**
- Consumes: Task 1.
- Produces:
  ```go
  // tmuxRunner runs one tmux invocation and returns its stdout.
  type tmuxRunner func(args ...string) (string, error)
  func runDefer(args []string, stdout, stderr io.Writer, getenv func(string) string, tmux tmuxRunner) int
  ```
- CLI:
  - `claudemux-head defer --wait-on <session> [--reason <text>]` sets mark `wait`, the reason, and the link to `<session>` (creation time read from tmux).
  - `claudemux-head defer --clear` clears a `wait` mark.
  - Exit codes: 0 done; 0 with a line on stdout `skipped: this session is deferred by the user` when the mark is `1` (both forms leave it alone); 1 when `$TMUX_PANE` is empty, `<session>` is not a live session, or `<session>` is this pane's own session; 2 for bad usage.
  - Default reason when `--reason` is omitted: `waiting on <session>`.

- [ ] **Step 1: Write the failing tests** in `defercmd_test.go` with a fake `tmuxRunner` recording argv:
  - `TestDeferCmdWaitOn`: sets exactly `deferLinkedArgs("%7", deferWaitValue, "waiting on phenix-2: fix", deferLink{"phenix-2","1763000000"})`.
  - `TestDeferCmdRespectsUserMark`: current mark `1` → no set-option call, exit 0, stdout has `skipped`. Same for `--clear`.
  - `TestDeferCmdClear`: mark `wait` → `deferArgs("%7", false, "")`.
  - `TestDeferCmdUnknownSession`: exit 1, no set-option call.
  - `TestDeferCmdSelf`: `--wait-on` naming the pane's own session → exit 1.
  - `TestDeferCmdNoPane`: empty `TMUX_PANE` → exit 1.
  - `TestDeferCmdSessionNameWithPrefixSibling`: fleet has `phenix` and `phenix-2`; `--wait-on phenix` links `phenix`'s creation time.
  - In `main_test.go`: `defer` is a known subcommand.
  - In `skills_test.go`: the shipped handoff skill contains `claudemux-head defer --wait-on` and `claudemux-head defer --clear`, and no longer contains `@claudemux_defer wait`.
- [ ] **Step 2: Run** `go test ./cmd/claudemux-head/ -run 'TestDeferCmd|TestSkills|TestUnknownSubcommand'` — expect FAIL.
- [ ] **Step 3: Implement** `runDefer` and the dispatch. Look sessions up by exact name in a `list-sessions -F` listing, never by passing the name as a tmux target (tmux prefix-matches targets).
- [ ] **Step 4: Rewrite the skill text.** Step 4 becomes one command, `claudemux-head defer --wait-on <tmux-name> --reason "waiting on <tmux-name>: <goal in a few words>"`, keeping the "skip when the user gave this session other work" and 48-character guidance, and dropping the manual check for the user's mark (the command does it). The closing section says: claudemux clears the mark and prompts this session by itself if the peer stops or closes and no reply arrives; when the reply does arrive first, run `claudemux-head defer --clear` and then relay. Keep "Only ever clear a defer this session set."
- [ ] **Step 5: Run** tests and the full package — expect PASS.
- [ ] **Step 6: Commit** `feat(defer): claudemux-head defer, used by the handoff skill`.

---

### Task 7: Tab completion and linking in the blocker prompts

**Files:**
- Create: `defercomplete.go`
- Modify: `deferpub.go` (`deferPromptText`), `tui.go` (`deferPromptKey` ~2924, `d`/`D` at ~1533-1565), `switchboardtui.go` (defer prompt at ~1003-1021, `d`/`D` at ~1100-1130, status render at ~1613), `headhelp.go` and the lobby's help text for `d`
- Test: `defercomplete_test.go`, `tui_test.go`, `switchboardtui_test.go`

**Interfaces:**
- Consumes: `fleetRow` (Task 2), `deferLink`, `setDeferLinkedCmd` (Task 1).
- Produces:
  ```go
  // deferCandidates orders the names Tab offers for self's blocker: self's
  // handoff children first, then every other session, each group in rows'
  // order. self is never included.
  func deferCandidates(self string, rows []fleetRow) []string

  // deferCompleter is the cycling state of one blocker prompt. Zero value is
  // ready; any edit other than Tab/Shift+Tab must call reset.
  type deferCompleter struct {
      base    string // what was typed when the cycle began
      idx     int
      cycling bool
  }
  func (c *deferCompleter) next(input string, candidates []string, dir int) string // dir is +1 or -1
  func (c *deferCompleter) reset()

  // deferLinkFor resolves a finished blocker to a link: set only when input
  // is exactly the name of a live session other than self.
  func deferLinkFor(input, self string, rows []fleetRow) deferLink

  func deferPromptText(input string, linked bool) string // appends " ↳ links" before the cursor's line end when linked
  ```
- The lobby already has every session in `m.snap.Sessions`; build `[]fleetRow` from it (a small adapter in `defercomplete.go`: `fleetFromSnapshot`), using the lobby's row order. `swSession.Created` (int64, 0 when unknown) supplies the link's `Created`, formatted in base 10; a 0 leaves the row out of linking but still offers its name.

Matching: case-insensitive prefix of `base` against the candidate. `next` with no match returns `input` unchanged and does not start a cycle. On an empty `base` every candidate matches. Cycling wraps both ways.

- [ ] **Step 1: Write the failing tests** in `defercomplete_test.go`:
  - `TestDeferCandidatesChildrenFirst`: self `beejax`, rows `[phenix, beejax, beejax-platform(parent beejax), remix]` → `[beejax-platform, phenix, remix]`.
  - `TestDeferCompleterPrefix`: `"ph"` with `[phenix, phenix-2, remix]` → `phenix`, Tab again → `phenix-2`, again → `phenix`; Shift+Tab from the start → `phenix-2`.
  - `TestDeferCompleterCaseInsensitive`: `"PH"` → `phenix`.
  - `TestDeferCompleterEmpty`: `""` cycles all candidates in order.
  - `TestDeferCompleterNoMatch`: `"zz"` → `"zz"`, and a following `"ph"` Tab works.
  - `TestDeferCompleterResetOnEdit`: Tab to `phenix`, `reset`, Tab on `phenix` → `phenix` then `phenix-2` (base is now `phenix`).
  - `TestDeferLinkForExact`: `phenix` with rows `phenix`, `phenix-2` → link to `phenix` with its creation time; `phenix-2: note` → zero link; `Phenix` → zero link (names are exact); self's own name → zero link; a name not live → zero link.
- [ ] **Step 2: Write the failing prompt tests:**
  - `tui_test.go` `TestHeadDeferPromptTabCompletes`: with `m.fleet` set, `d`, type `ph`, Tab → `m.deferInput == "phenix"`; a typed rune after that resets the cycle; enter returns a command and (assert through a seam: extract `deferSubmit(input, self, rows) (reason string, link deferLink)`) the link is `phenix`'s.
  - `switchboardtui_test.go` `TestLobbyDeferPromptTabCompletes`: same on the selected row, and the row being deferred is not offered.
  - `TestDeferPromptTextLinked`: `deferPromptText("phenix", true)` contains `↳ links`; `false` does not.
- [ ] **Step 3: Run** `go test ./cmd/claudemux-head/ -run 'TestDeferCandidates|TestDeferCompleter|TestDeferLinkFor|TestHeadDeferPrompt|TestLobbyDeferPrompt|TestDeferPromptText'` — expect FAIL.
- [ ] **Step 4: Implement.** In both prompts: `tab` → `next(+1)`, `shift+tab` → `next(-1)`, every other editing key → `reset` then the existing `deferInputKey`. On enter: `deferSubmit`, then `setDeferLinkedCmd(target, deferOnValue, reason, link)` (which is the plain defer when the link is zero). `D` editing an existing `wait` mark keeps its value `wait`. Update the help lines for `d` to mention Tab.
- [ ] **Step 5: Run** tests and the full package — expect PASS.
- [ ] **Step 6: Commit** `feat(defer): tab-complete and link other sessions in the blocker prompt`.

---

### Task 8: Reboot restore keeps the link

**Files:**
- Modify: `sessionrecord.go` (struct at ~36-50), `headrecord.go` (`recordDeferKey`, `sessionRecordFor`), `swrestore.go` (`relinkArgs` ~234, `swRelinkCmd`)
- Test: `sessionrecord_test.go`, `headrecord_test.go`, `swrestore_test.go`

**Interfaces:**
- Consumes: Task 1, Task 5's `model.deferMark`.
- Produces: `sessionRecord.DeferOn string` (`json:"defer_on,omitempty"`), `sessionRecord.DeferOnSeen bool` (`json:"defer_on_seen,omitempty"`); `func deferRelinkArgs(restored []lostSession, created map[string]int64) [][]string`.

- [ ] **Step 1: Write the failing tests:**
  - `TestSessionRecordCarriesDeferLink`: a model with a linked, seen mark records `DeferOn` and `DeferOnSeen`; an unlinked defer records neither; round-trips through JSON.
  - `TestRecordDueOnLinkChange`: changing only the link makes `recordDue` true.
  - `TestDeferRelinkArgs`: child deferred on `phenix-2`, both restored, `created[phenix-2]=1763000500` → one argv setting `@claudemux_defer_on phenix-2`, `@claudemux_defer_on_created 1763000500`, and `@claudemux_defer_on_seen 1` only when `DeferOnSeen`.
  - `TestDeferRelinkArgsBlockerNotRestored`: blocker absent from `restored` → no argv (the defer stays a plain one, per the restore step that already re-set the mark without a link).
  - `TestDeferRelinkArgsNotDeferred`: a record with `DeferOn` but `Deferred=false` → no argv.
- [ ] **Step 2: Run** `go test ./cmd/claudemux-head/ -run 'TestSessionRecordCarriesDeferLink|TestRecordDueOnLinkChange|TestDeferRelink'` — expect FAIL.
- [ ] **Step 3: Implement.** `swRelinkCmd` runs `deferRelinkArgs` after `relinkArgs`, from the same `created` map.
- [ ] **Step 4: Run** tests and the full package — expect PASS.
- [ ] **Step 5: Commit** `feat(restore): a linked defer comes back linked`.

---

### Task 9: Verify with real sessions

No code. Install per the repo's dev-linked setup (`go install ./cmd/claudemux-head` then `claudemux-head hook ensure` so the rewritten skill ships), then check each line and record what was seen:

- [ ] Hand off from session A to a peer B with the skill; A shows `WAIT` and `tmux show-options -t A: | grep defer_on` shows the link.
- [ ] While B works, `@claudemux_defer_on_seen` becomes `1` on A.
- [ ] Let B finish and reply normally: A's mark clears, and A receives no extra nudge.
- [ ] Repeat with B's reply suppressed (answer B in its own pane so no notice reaches A): within about 20 seconds of B going idle A leaves `WAIT`, the nudge text is submitted, and the chip reads `◆ <B> finished`.
- [ ] Repeat and kill B's tmux session: A is released with the `closed` wording.
- [ ] Type a draft into A's prompt before B stops: the mark clears, nothing is typed, the draft is intact.
- [ ] `config set defer.auto_nudge false`, restart A's head (`R`), repeat: mark clears, nothing typed.
- [ ] `d` in the head and in the lobby: `Tab` cycles other sessions, a completed name shows `↳ links`, a free-text blocker does not and never auto-clears.
- [ ] Fix anything that fails with a failing test first, then commit.
