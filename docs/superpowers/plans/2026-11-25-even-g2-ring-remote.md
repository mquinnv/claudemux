# Even G2 / R1 Ring Remote Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Answer whatever claudemux session is waiting from Even G2 glasses with the R1 ring, without speaking or typing.

**Architecture:** The lobby's web server gains token-protected `/remote/` routes. They describe the next waiting session (question, options) and accept an answer, which the lobby types into the real tmux pane after re-checking the pane still shows the same dialog. A small Even Hub web app in `glasses/` polls those routes, draws three text regions on the glasses and posts ring gestures back.

**Tech Stack:** Go (existing `cmd/claudemux-head` package, bubbletea, net/http). TypeScript, Vite, vitest, `@evenrealities/even_hub_sdk@0.0.16`, `@evenrealities/evenhub-cli@0.2.0`.

**Spec:** `docs/superpowers/specs/2026-11-25-even-g2-ring-remote-design.md`
**Spike findings (read before any dialog task):** `docs/superpowers/specs/2026-11-25-even-g2-spike-mac-findings.md`

## Global Constraints

- All Go code lives in package `main` under `cmd/claudemux-head/`; tests are plain `go test ./cmd/claudemux-head/`. No new Go dependencies.
- No test sends real keys or runs real tmux. Senders take their tmux calls as function parameters.
- Keys must never land on a different dialog than the one shown. On any doubt the sender sends nothing and reports `changed`.
- A digit answers a dialog by itself. Never send Enter after a digit.
- Rows that need typing (`Type something.`, `Chat about this`, `Tell Claude what to change`, any row the parser marks free-text) are never offered.
- `/remote/` routes exist only when `remote.enabled` is true, `web.listen` is set and `CLAUDEMUX_REMOTE_TOKEN` is non-empty. They sit behind the existing `webGuard`.
- The existing `/` and `/api/fleet` stay read-only, unauthenticated and byte-for-byte unchanged in behaviour.
- The glasses never cause a `switch-client`.
- Fixed replies default to exactly: `Yes, go ahead`, `Continue`, `No, stop and wait for me`.
- Skip uses `swSnoozeTTL` (10 minutes). Defer from the glasses uses the reason `deferred from glasses`.
- The glasses app is not built by `go install` or the release workflow.
- Fixtures follow `cmd/claudemux-head/testdata/README.md` redaction rules.

## Review Focus

1. **The dialog changes between display and tap** (another dialog replaced it, or Michael answered at the desk): no key is sent, the glasses redraw. Pinned in Task 6.
2. **An option row wrapped onto a second pane line** (the Bash fixture's option 2): the label is joined and later rows keep their numbers. Pinned in Task 4.
3. **A permission dialog with a layout the parser has not seen** (MCP, WebFetch): the session shows "answer at the desk" rather than a guessed option list. Pinned in Task 4.
4. **A draft is sitting in the prompt when a reply is tapped:** nothing is typed, the glasses say `prompt has a draft`. Pinned in Task 6.
5. **The phone wakes with an old screen and Michael taps:** the stale episode is refused with `changed` and the next poll redraws. Pinned in Tasks 10 and 12.
6. **The session ends or is renamed between poll and tap:** the answer returns `gone`, nothing is sent. Pinned in Task 10.
7. **The lobby re-execs (binwatch) while the glasses are in use:** skips and the pinned session are lost; the app must recover on the next poll with no manual step. Pinned in Task 12.

---

### Task 0: Close out the phone spike (controller with Michael, not a subagent)

The dev-load half passed on 2026-11-25: plain `http://100.64.0.15` fetches work from the dev WebView on iPhone, three text containers update in place with no flicker, and swipe/tap/double-tap/long-press all arrive from the ring.

- [ ] **Step 1:** Michael runs `npx evenhub login` in the scratch spike project. Upload `claudemux-spike.ehpk` as a private build, install it, and confirm the header still counts polls. Pass means a packaged build may whitelist `http://<tailnet-ip>:<port>`.
- [ ] **Step 2:** Michael locks the phone for two minutes with the app in front. Read the gap from `ping.log`.
- [ ] **Step 3:** Append both results to the spike findings doc and commit.
- [ ] **Step 4:** If Step 1 fails, stop and add the TLS task from the spec's fallback (`web.tls_cert`, `web.tls_key`, allowed hostname in `webGuard`) before Task 10. If Step 2 shows polling stops, nothing changes: Task 11 already draws the data age.

### Task 1: Keep the question in the ask marker

**Files:**
- Modify: `cmd/claudemux-head/hookrun.go` (`runHookAsk`, `PreToolUse` case)
- Modify: `cmd/claudemux-head/panemap.go` (new reader next to `askMarkerTime`)
- Test: `cmd/claudemux-head/askhook_test.go`

**Interfaces:**
- Produces:
  ```go
  type askOption struct{ Label, Description string }
  type askQuestion struct {
      Question, Header string
      Options          []askOption
      MultiSelect      bool
  }
  type askMarker struct {
      SessionID string        `json:"session_id"`
      Questions []askQuestion `json:"questions,omitempty"`
  }
  func readAskMarker(dir, sessionID string) (askMarker, bool)
  ```
  JSON keys mirror the hook payload: `question`, `header`, `options[].label`, `options[].description`, `multiSelect`.

- [ ] **Step 1: Failing tests** in `askhook_test.go`:
  - `TestAskHookStoresQuestions`: feed a `PreToolUse` payload whose `tool_input.questions` holds one question `Which colour do you choose?`, header `Colour`, options `Red`, `Green`, `Blue`; `readAskMarker` returns that question with three labels and `MultiSelect == false`.
  - `TestAskHookMarkerWithoutToolInput`: a payload with no `tool_input` still writes a marker; `readAskMarker` returns `ok == true` and no questions.
  - `TestReadAskMarkerMissing`: returns `ok == false`.
- [ ] **Step 2:** Run `go test ./cmd/claudemux-head/ -run 'AskHook|ReadAskMarker'`; expect failures on the undefined names.
- [ ] **Step 3:** Implement. Read `tool_input.questions` from the decoded payload tolerantly (wrong types mean "no questions", never an error). The hook stays silent on stdout.
- [ ] **Step 4:** Run the same command; expect PASS. Run the whole package.
- [ ] **Step 5:** Commit `ask hook: keep the question and its options in the marker`.

### Task 2: Plan approval counts as a dialog

Today `permissionDialogUp` looks for `esc to cancel` in the last 6 non-blank lines. The plan-approval dialog has no such footer (see `testdata/dialog_plan.txt`), so a session waiting on plan approval never publishes `Awaiting`. `PermissionRequest` does fire for `ExitPlanMode`, so the permit marker already exists.

**This changes desk behaviour:** the lobby and conductor will start treating plan approvals as waiting. Michael was told; do not gate it behind `remote.enabled`.

**Files:**
- Modify: `cmd/claudemux-head/permit.go` (`permissionDialogUp`)
- Test: `cmd/claudemux-head/permit_test.go` (create if absent; follow `askstate_test.go` style)

- [ ] **Step 1: Failing tests:**
  - `TestPermissionDialogUpPlan`: `testdata/dialog_plan.txt` returns true.
  - `TestPermissionDialogUpFixtures`: `dialog_permit_bash.txt` and `dialog_permit_edit.txt` return true; `prompt_empty.txt` (or the existing empty-prompt fixture) and `dialog_ask_single.txt` keep their current results.
- [ ] **Step 2:** Run `go test ./cmd/claudemux-head/ -run PermissionDialogUp`; plan case fails.
- [ ] **Step 3:** Extend `permissionDialogUp` to also return true when the capture's tail holds the plan dialog's stable markers from the findings doc (the numbered rows `1. Yes, and use auto mode` / `2. Yes, manually approve edits` below a rule). Match on the two `Yes,` rows together, not on one phrase.
- [ ] **Step 4:** Run; expect PASS. Run the whole package.
- [ ] **Step 5:** Commit `permit: a plan-approval dialog reads as Awaiting`.

### Task 3: Remote config and token

**Files:**
- Modify: `cmd/claudemux-head/config.go`
- Test: `cmd/claudemux-head/config_test.go`

**Interfaces:**
- Produces:
  ```go
  type RemoteConfig struct {
      Enabled bool     `yaml:"enabled"`
      Replies []string `yaml:"replies"`
      Suggest bool     `yaml:"suggest"`
  }
  // Config gains: Remote RemoteConfig `yaml:"remote"`
  const remoteTokenKey = "CLAUDEMUX_REMOTE_TOKEN"
  func remoteToken(cfg Config) string // configValue(cfg.Summary.APIKeyFile, remoteTokenKey), trimmed
  ```
  Defaults: `Enabled: false`, `Suggest: true`, `Replies` the three fixed replies from Global Constraints.

- [ ] **Step 1: Failing tests:** `TestRemoteConfigDefaults` (values above); `TestRemoteConfigRejectsEmptyReply` (a `replies` entry that is empty or over 80 runes fails `validate()` naming `remote.replies`); `TestRemoteConfigRepliesOverride` (a YAML list replaces the defaults, it does not append).
- [ ] **Step 2:** Run `go test ./cmd/claudemux-head/ -run RemoteConfig`; expect failures.
- [ ] **Step 3:** Implement. `remote.enabled` with no token or no `web.listen` is NOT a load error; Task 10 reports it on the lobby status line.
- [ ] **Step 4:** Run; PASS.
- [ ] **Step 5:** Commit `config: remote section and token key`.

### Task 4: Parse a dialog out of a pane capture

**Files:**
- Create: `cmd/claudemux-head/remotedialog.go`
- Test: `cmd/claudemux-head/remotedialog_test.go`

**Interfaces:**
- Produces:
  ```go
  type dialogKind int
  const (
      dialogNone dialogKind = iota
      dialogAsk        // AskUserQuestion, one single-select question
      dialogAskComplex // multi-select or several questions
      dialogPermit
      dialogPlan
      dialogUnknown    // a dialog is up but its layout is not recognised
  )
  type dialogRow struct {
      N        int    // the digit that selects it
      Label    string // wrapped continuation lines joined with one space
      FreeText bool   // needs typing; never offered
  }
  type paneDialog struct {
      Kind   dialogKind
      Prompt string // the question, or the tool line plus its description
      Rows   []dialogRow
  }
  func parsePaneDialog(capture string) paneDialog
  ```
  Takes the plain capture (no `-e`).

- [ ] **Step 1: Failing tests,** one per fixture:
  - `dialog_ask_single.txt`: `dialogAsk`, prompt `Which colour do you choose?`, rows 1-3 `Red`, `Green`, `Blue` not free-text, rows 4 (`Type something.`) and 5 (`Chat about this`) free-text.
  - `dialog_ask_multi.txt` and `dialog_ask_two_questions.txt`: `dialogAskComplex`.
  - `dialog_permit_bash.txt`: `dialogPermit`, four rows, row 1 `Yes`, row 4 `No`, row 2's label is the full two-line text joined, row 3 starts `Yes, and switch to auto mode`. The `Tip:` line is not a row and not in the prompt.
  - `dialog_permit_edit.txt`: `dialogPermit`, three rows, row 3 `No`.
  - `dialog_plan.txt`: `dialogPlan`, row 3 (`Tell Claude what to change`) free-text.
  - The existing empty-prompt fixture: `dialogNone`.
  - `TestParsePaneDialogUnknown`: a synthetic capture ending in `Esc to cancel` with no numbered rows gives `dialogUnknown`.
  - `TestParsePaneDialogIgnoresScrollback`: numbered lines in conversation text above the last rule are not rows.
- [ ] **Step 2:** Run `go test ./cmd/claudemux-head/ -run ParsePaneDialog`; expect failures.
- [ ] **Step 3:** Implement. Work only below the last horizontal rule that opens the dialog (`isRuleLine` exists in `defernudge.go`). A row starts at a line matching optional cursor glyph, digits, `.`, space; an indented line with no number directly after a row continues it. Decide the kind from the markers listed in the findings doc. Anything that looks like a dialog but fails those checks is `dialogUnknown`.
- [ ] **Step 4:** Run; PASS.
- [ ] **Step 5:** Commit `remote: parse dialogs from a pane capture`.

### Task 5: Build what the glasses show for one session

**Files:**
- Create: `cmd/claudemux-head/remotedetail.go`
- Test: `cmd/claudemux-head/remotedetail_test.go`

**Interfaces:**
- Consumes: `askMarker`/`readAskMarker` (Task 1), `parsePaneDialog` (Task 4), `swSession`, `promptEmpty`, `RemoteConfig`.
- Produces:
  ```go
  type remoteOption struct {
      ID        string `json:"id"`        // "key:2" or "reply:0" or "suggest:1"
      Label     string `json:"label"`
      Broad     bool   `json:"broad,omitempty"`
      Suggested bool   `json:"suggested,omitempty"`
  }
  type remoteDetail struct {
      Session string         `json:"session"`
      Title   string         `json:"title"`   // "<description or name> · <topic>"
      Kind    string         `json:"kind"`    // ask | permit | plan | idle | desk
      Body    string         `json:"body"`
      Note    string         `json:"note,omitempty"` // "answer at the desk", "prompt has a draft"
      Options []remoteOption `json:"options"`
      Episode string         `json:"episode"`
  }
  type remoteInputs struct {
      Sess      swSession
      Capture   string    // plain capture of ClaudePane
      CaptureE  string    // -e capture, for promptEmpty
      Marker    askMarker
      MarkerOK  bool
      LastText  string    // last assistant text from the transcript, "" if unknown
      Suggested []string
      Replies   []string
      VimMode   bool
  }
  func buildRemoteDetail(in remoteInputs) remoteDetail
  func remoteEpisode(since time.Time, kind, body string, labels []string) string // first 16 hex of sha256
  func broadChoice(kind dialogKind, row dialogRow, rowCount int) bool
  ```

Rules, all from the spec:
- `ask`: body is the marker's question when present, else the parsed prompt. Options are the non-free-text rows as `key:N`. If the marker's labels and the parsed rows disagree in count or order, kind is `desk`.
- `permit`, `plan`: options are non-free-text rows as `key:N`. `broadChoice` is true for any row that is neither the first (`Yes`) nor the last (`No`) on a permit dialog, and for plan row 1 (`Yes, and use auto mode`).
- `dialogAskComplex`, `dialogUnknown`: kind `desk`, note `answer at the desk`, no options.
- `idle` (state `Idle`, no dialog): body is the tail of `LastText` (last 400 runes, cut at a word boundary), falling back to the capture's last 8 non-blank lines. Options are `Suggested` as `suggest:i` (marked suggested), then `Replies` as `reply:i`. If `promptEmpty(CaptureE, VimMode)` is false: note `prompt has a draft`, no options.
- `Episode` covers `Sess.Since`, kind, body and option labels in order.

- [ ] **Step 1: Failing tests:** one per rule above, using the fixtures; plus `TestRemoteEpisodeStable` (same inputs, same value) and `TestRemoteEpisodeChangesWithLabels`; `TestBroadChoice` over the Bash fixture (rows 2 and 3 broad, 1 and 4 not), the Edit fixture (row 2 broad) and the plan fixture (row 1 broad, row 2 not).
- [ ] **Step 2:** Run `go test ./cmd/claudemux-head/ -run 'RemoteDetail|RemoteEpisode|BroadChoice'`; expect failures.
- [ ] **Step 3:** Implement as a pure function. No tmux, no file reads here.
- [ ] **Step 4:** Run; PASS.
- [ ] **Step 5:** Commit `remote: detail and episode for one waiting session`.

### Task 6: Send an answer safely

**Files:**
- Create: `cmd/claudemux-head/remotesend.go`
- Test: `cmd/claudemux-head/remotesend_test.go`

**Interfaces:**
- Consumes: `buildRemoteDetail`, `remoteDetail` (Task 5), `nudgeClaudePane`, `sendLiteralArgs` in `teardown.go`.
- Produces:
  ```go
  var (
      errRemoteChanged = errors.New("changed") // pane no longer shows that episode; nothing sent
      errRemoteStuck   = errors.New("stuck")   // key sent, dialog still up
      errRemoteDraft   = errors.New("draft")   // prompt not empty; nothing typed
      errRemoteNoOpt   = errors.New("unknown option")
  )
  type remoteIO struct {
      Rebuild func() (remoteDetail, error)   // re-capture and rebuild the detail now
      SendKey func(pane, key string) error   // tmux send-keys -t pane key
      Nudge   func(pane, text string) bool   // nudgeClaudePane
      Sleep   func(time.Duration)
  }
  func sendRemoteAnswer(pane, episode, optionID string, io remoteIO) error
  ```
  Order: `Rebuild`; if its `Episode` differs from `episode` return `errRemoteChanged`; find `optionID` or return `errRemoteNoOpt`. For `key:N` call `SendKey(pane, "N")` once, sleep 400ms, `Rebuild` again and return `errRemoteStuck` if the episode is unchanged. For `reply:i` and `suggest:i` call `Nudge` with the option's label; false returns `errRemoteDraft`.

- [ ] **Step 1: Failing tests** with a fake `remoteIO` recording calls:
  - `TestSendRemoteAnswerKey`: matching episode, `key:2` sends exactly one key `"2"` and no Enter.
  - `TestSendRemoteAnswerChanged`: differing episode sends nothing.
  - `TestSendRemoteAnswerStuck`: episode unchanged after the key returns `errRemoteStuck` and sends no second key.
  - `TestSendRemoteAnswerReply`: `reply:0` calls `Nudge` once with `Yes, go ahead`.
  - `TestSendRemoteAnswerDraft`: `Nudge` false returns `errRemoteDraft`.
  - `TestSendRemoteAnswerUnknownOption`: `key:9` on a three-row dialog sends nothing.
  - `TestSendRemoteAnswerNeverOffersFreeText`: an ID for a free-text row is `errRemoteNoOpt` (it is not in `Options`).
- [ ] **Step 2:** Run `go test ./cmd/claudemux-head/ -run SendRemoteAnswer`; expect failures.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run; PASS.
- [ ] **Step 5:** Commit `remote: answer a dialog only if the pane still shows it`.

### Task 7: Suggested replies

**Files:**
- Create: `cmd/claudemux-head/remotesuggest.go`
- Test: `cmd/claudemux-head/remotesuggest_test.go`

**Interfaces:**
- Consumes: `Summarizer` (`summary.go`); follow `Headline` in `webheadline.go` for the request shape, model and timeout.
- Produces:
  ```go
  func (s *Summarizer) SuggestReplies(ctx context.Context, lastMessage string) ([]string, error)
  func cleanSuggestions(raw string) []string // one per line; strips bullets, numbering and quotes; drops empties and lines over 60 runes; at most 3
  type suggestCache struct{ /* keyed by session name + Since */ }
  func (c *suggestCache) get(name string, since time.Time) ([]string, bool)
  func (c *suggestCache) put(name string, since time.Time, v []string)
  ```
  A nil `*Summarizer` or an error means no suggestions, never a failure of the detail.

The system prompt asks for up to three short replies the engineer would plausibly send next, one per line, each a complete instruction, no questions back, nothing that grants standing permission. Wording is the builder's.

- [ ] **Step 1: Failing tests:** `TestCleanSuggestions` (bullets, `1.` numbering, quotes, a 61-rune line dropped, four lines cut to three, blank input gives nil); `TestSuggestCache` (hit on same `Since`, miss on a later `Since`).
- [ ] **Step 2:** Run `go test ./cmd/claudemux-head/ -run 'Suggest'`; expect failures.
- [ ] **Step 3:** Implement. Test `SuggestReplies` against the fake transport the headline tests use, if one exists; otherwise test only the pure pieces.
- [ ] **Step 4:** Run; PASS.
- [ ] **Step 5:** Commit `remote: suggested replies for an idle session`.

### Task 8: The glasses' own place in the queue

**Files:**
- Create: `cmd/claudemux-head/remotecursor.go`
- Test: `cmd/claudemux-head/remotecursor_test.go`

**Interfaces:**
- Consumes: `waitingQueue` (`swconductor.go`), `swSnoozeTTL`, `isWaiting`.
- Produces:
  ```go
  type remoteCursor struct{ /* pinned name + Since; skips keyed by name with Since and time */ }
  // pick returns the session the glasses should show and its 1-based position
  // in the queue. It keeps the pinned session while that session is still in
  // the queue with the same Since; otherwise it pins the queue head.
  func (c *remoteCursor) pick(sessions []swSession, now time.Time) (sess swSession, pos, total int, ok bool)
  func (c *remoteCursor) skip(name string, since, now time.Time)
  func (c *remoteCursor) open(name string) // pin a session chosen from the fleet list, waiting or not
  ```
  The queue is `waitingQueue` with the cursor's own skips as the snooze map. As in the conductor, all skips are released when they would empty the queue.

- [ ] **Step 1: Failing tests:** `TestRemoteCursorPinsHead`; `TestRemoteCursorKeepsPinWhenNewArrives` (an older-waiting arrival changes `total`, not the session); `TestRemoteCursorMovesOnWhenAnswered` (pinned session stops waiting, next head is picked); `TestRemoteCursorSkipExpires` (after `swSnoozeTTL`); `TestRemoteCursorSkipsReleasedWhenQueueEmpty`; `TestRemoteCursorOpenNonWaiting` (opened session is returned with `pos == 0`).
- [ ] **Step 2:** Run `go test ./cmd/claudemux-head/ -run RemoteCursor`; expect failures.
- [ ] **Step 3:** Implement. Pure; the caller holds the lock.
- [ ] **Step 4:** Run; PASS.
- [ ] **Step 5:** Commit `remote: a queue cursor for the glasses`.

### Task 9: Unfiltered fleet for the remote

`webFleet.publish` applies the `hide_private_orgs` filter before storing. The remote is Michael's own view and needs every session plus pane ids.

**Files:**
- Modify: `cmd/claudemux-head/webfleet.go`
- Test: `cmd/claudemux-head/webfleet_test.go`

**Interfaces:**
- Produces: `func (w *webFleet) rawSessions() []swSession` returning the snapshot's sessions before the privacy filter, copied under the read lock.

- [ ] **Step 1: Failing test** `TestWebFleetRawSessionsUnfiltered`: publish a snapshot with one hidden session; `sessions()` omits it, `rawSessions()` includes it, and `view()` is unchanged.
- [ ] **Step 2:** Run `go test ./cmd/claudemux-head/ -run WebFleetRaw`; fails.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run; PASS.
- [ ] **Step 5:** Commit `web: keep the unfiltered snapshot for the remote routes`.

### Task 10: Remote routes and lobby wiring

**Files:**
- Create: `cmd/claudemux-head/remoteroutes.go`
- Modify: `cmd/claudemux-head/webserver.go` (`webHandler`, `startSwitchboardWeb`, `swWeb`)
- Modify: `cmd/claudemux-head/switchboardtui.go` (`runSwitchboard`, `Update`)
- Test: `cmd/claudemux-head/remoteroutes_test.go`

**Interfaces:**
- Consumes: everything from Tasks 3 and 5-9; `setDeferCmd`, `swDeferTarget`, `readPaneMap`/`paneMapDir`, `askMarkerDir`, `newEventReader`, `claudeVimMode`.
- Produces:
  ```go
  type remoteHub struct {
      fleet   *webFleet
      cfg     RemoteConfig
      token   string
      mu      sync.Mutex
      cursor  remoteCursor
      suggest suggestCache
      busy    map[string]bool          // one answer in flight per session
      gather  func(swSession) remoteInputs // captures pane, reads marker and transcript
      io      func(pane string, rebuild func() (remoteDetail, error)) remoteIO
      deferFn func(swSession, string) error // runs through tea.Program.Send
      sum     *Summarizer
  }
  func remoteHandler(h *remoteHub) http.Handler // mounted at /remote/ inside webHandler's mux
  type remoteDeferMsg struct{ sess swSession; reason string; done chan error }
  ```

Wire format:
- Every route answers `OPTIONS` with 204 and, on every response, `Access-Control-Allow-Origin: *`, `Access-Control-Allow-Headers: Authorization, Content-Type`, `Access-Control-Allow-Methods: GET, POST, OPTIONS`. `OPTIONS` needs no token.
- All other requests need `Authorization: Bearer <token>`, compared with `subtle.ConstantTimeCompare`; otherwise 401 with an empty body.
- `GET /remote/next` → `{"taken_at", "queue": {"pos", "total"}, "working": <count of non-waiting sessions>, "detail": remoteDetail | null}`. `?session=<name>` calls `cursor.open` first. Suggestions are fetched only when `cfg.Suggest`, kind is `idle` and the cache misses; the first response for an episode may omit them and a goroutine fills the cache.
- `GET /remote/fleet` → `{"sessions": [{"name", "title", "state", "waiting", "deferred"}]}` from `rawSessions()`.
- `POST /remote/answer` body `{"session", "episode", "option"}` → 200 `{"result": "sent"}`; 409 `{"result": "changed" | "stuck" | "draft" | "busy"}`; 404 `{"result": "gone"}` when the session is not in the snapshot or has no `ClaudePane`.
- `POST /remote/skip` body `{"session"}` → 200. `POST /remote/defer` body `{"session"}` → sets the defer with reason `deferred from glasses` via `remoteDeferMsg`; 200 or 404.

Lobby wiring:
- `startSwitchboardWeb` builds the hub when `cfg.Remote.Enabled`. With no token it returns no hub and an error string for the status line (`remote: CLAUDEMUX_REMOTE_TOKEN is not set`), exactly as a bad `web.listen` is surfaced.
- `runSwitchboard` gives the hub `p.Send` after `tea.NewProgram`. `Update` handles `remoteDeferMsg` by returning a `tea.Cmd` that runs `setDeferCmd(swDeferTarget(sess), true, reason)` and reports on `done`.
- Answers do not go through `Update`: `sendRemoteAnswer` runs in the request goroutine under `busy`.

- [ ] **Step 1: Failing tests** with `httptest` and a hub whose `gather`, `io` and `deferFn` are fakes:
  - `TestRemoteRequiresToken` (401 without, 401 with a wrong one, 200 with the right one; `OPTIONS` 204 with CORS headers and no token).
  - `TestRemoteRoutesAbsentWhenDisabled` (`/remote/next` is 404 when the hub is nil; `/api/fleet` still 200).
  - `TestRemoteNextShowsQueueHead` (two waiting sessions; detail is the older one, `pos` 1, `total` 2).
  - `TestRemoteNextEmptyQueue` (`detail` null, `working` counts the busy sessions).
  - `TestRemoteNextIncludesPrivateSessions` (a session hidden from `/api/fleet` appears).
  - `TestRemoteAnswerSent`, `TestRemoteAnswerStaleEpisode` (409 `changed`, no key recorded), `TestRemoteAnswerGone` (404), `TestRemoteAnswerBusy` (second concurrent answer for the same session is 409 `busy`).
  - `TestRemoteSkipMovesOn`, `TestRemoteDeferUsesReason` (`deferred from glasses`).
  - `TestRemoteGuardStillApplies` (a non-tailnet `RemoteAddr` gets 403 before auth).
- [ ] **Step 2:** Run `go test ./cmd/claudemux-head/ -run Remote`; expect failures.
- [ ] **Step 3:** Implement the handler, then the wiring.
- [ ] **Step 4:** Run the whole package; PASS. `go vet ./...` clean.
- [ ] **Step 5:** Commit `remote: token-protected routes for the glasses`.

### Task 11: Glasses app, pure pieces

**Files:**
- Create: `glasses/package.json`, `glasses/tsconfig.json`, `glasses/vite.config.ts`, `glasses/.gitignore` (`node_modules`, `dist`, `*.ehpk`)
- Create: `glasses/src/types.ts`, `glasses/src/view.ts`, `glasses/src/layout.ts`
- Test: `glasses/src/view.test.ts`, `glasses/src/layout.test.ts`

**Interfaces:**
- Produces:
  ```ts
  // types.ts mirrors Task 10's JSON: RemoteOption, RemoteDetail, NextResponse, FleetResponse
  type Row = { id: string; label: string; broad?: boolean; suggested?: boolean };
  type Screen = 'session' | 'fleet';
  type State = {
    screen: Screen; next: NextResponse | null; fleet: FleetResponse | null;
    cursor: number; confirming: string | null;   // option id awaiting a second tap
    lastOkAt: number; error: string | null; flash: string | null;
  };
  type Gesture = 'up' | 'down' | 'tap' | 'double';
  type Effect =
    | { kind: 'answer'; session: string; episode: string; option: string }
    | { kind: 'skip'; session: string } | { kind: 'defer'; session: string }
    | { kind: 'open'; session: string } | { kind: 'loadFleet' } | null;
  function rows(s: State): Row[];              // options, then {id:'defer'}, {id:'skip'}
  function reduce(s: State, g: Gesture): { state: State; effect: Effect };
  function applyNext(s: State, n: NextResponse, now: number): State; // resets cursor and confirming when the episode changes
  function render(s: State, now: number): { header: string; body: string; options: string };
  // layout.ts
  const COLS = 46;
  function wrap(text: string, cols: number, maxLines: number): string; // ends with "…" when cut
  function fit(left: string, right: string, cols: number): string;     // left padded so right is flush
  ```

Behaviour:
- `down`/`up` move the cursor within `rows`, clamped. On the fleet screen they move through sessions.
- `tap` on a broad option sets `confirming` and changes that row's text to `tap again to confirm`; a second tap on the same row emits `answer`. Moving the cursor clears `confirming`.
- `tap` on any other option emits `answer` at once. `tap` on `defer`/`skip` emits those.
- `double` on the session screen switches to the fleet screen and emits `loadFleet`; on the fleet screen it returns. `tap` on a fleet row emits `open` and returns.
- `render`: header is `fit(title, "pos/total", COLS)`; when `now - lastOkAt > 10_000` the right side is `<n>s ago`; when `error` is set the header is `lobby not reachable`. Body is `wrap(detail.body, COLS, 3)`, with `detail.note` on its own line when present. Options show at most 5 rows as a window that follows the cursor, the cursor row prefixed `> `, others two spaces, suggested rows suffixed ` *`. With no detail: header `all clear`, body `<working> working`.

- [ ] **Step 1: Failing tests** (vitest): one per bullet above, plus `applyNext keeps the cursor when the episode is unchanged`, `applyNext resets it when the episode changes`, `wrap cuts at a word and adds an ellipsis`, `the option window scrolls to keep the cursor visible`.
- [ ] **Step 2:** `cd glasses && npm install && npx vitest run`; expect failures.
- [ ] **Step 3:** Implement. No SDK imports in these files.
- [ ] **Step 4:** `npx vitest run` passes; `npx tsc --noEmit` clean.
- [ ] **Step 5:** Commit `glasses: state, gestures and text layout`.

### Task 12: Glasses app, device wiring and packaging

Start from the working spike at the scratch path named in the findings doc (`g2hello/src/main.ts`): same three text containers, same event handling.

**Files:**
- Create: `glasses/index.html`, `glasses/src/main.ts`, `glasses/src/api.ts`, `glasses/src/setup.ts`, `glasses/app.json.tmpl`, `glasses/scripts/pack.mjs`, `glasses/README.md`
- Test: `glasses/src/api.test.ts`

**Interfaces:**
- Consumes: Task 11's `reduce`, `applyNext`, `render`; Task 10's routes.
- Produces:
  ```ts
  // api.ts
  type AnswerResult = 'sent' | 'changed' | 'stuck' | 'draft' | 'busy' | 'gone' | 'unauthorized' | 'unreachable';
  function client(origin: string, token: string, f?: typeof fetch): {
    next(session?: string): Promise<NextResponse>;
    fleet(): Promise<FleetResponse>;
    answer(session: string, episode: string, option: string): Promise<AnswerResult>;
    skip(session: string): Promise<void>; defer(session: string): Promise<void>;
  };
  ```

Decisions:
- Containers: header `y=0 h=32`, body `y=32 h=96`, options `y=128 h=160`, all `x=0 w=576`; options has `isEventCapture: 1`. Update only with `textContainerUpgrade`, and only the regions whose text changed.
- Gestures: `SCROLL_BOTTOM_EVENT` → `down`, `SCROLL_TOP_EVENT` → `up`, `DOUBLE_CLICK_EVENT` → `double`, `CLICK_EVENT` or an event with `eventType` undefined → `tap`. Long press is ignored.
- Poll `next` every 2000ms; one request in flight at a time. After any effect, poll immediately.
- Answer results: `sent` flashes `sent` for one poll; `changed`, `gone`, `busy` just redraw from the next poll; `stuck` flashes `not accepted - try again`; `draft` flashes `prompt has a draft`; `unauthorized` shows `bad token - open the phone app`.
- The lobby origin is fixed at pack time: `scripts/pack.mjs` reads `CLAUDEMUX_REMOTE_ORIGIN` (for example `http://100.64.0.15:7474`), fails if unset, writes `app.json` from the template with that single origin in the network whitelist and `package_id` `net.claudemux.glasses`, runs `vite build` with the origin injected, then `evenhub pack app.json dist -o claudemux-glasses.ehpk`.
- The token is entered once on the phone page (`setup.ts`), stored with `bridge.setLocalStorage('token', …)`. The phone page also shows the last ten log lines.
- `glasses/README.md`: enabling `remote` in `config.yml`, putting `CLAUDEMUX_REMOTE_TOKEN` in the claudemux env file, packing, `evenhub login` / `create` / `upload`, installing from Me → Apps → Private builds, and promoting to Beta to launch from the glasses menu.

- [ ] **Step 1: Failing tests** for `api.ts` with a fake `fetch`: sends `Authorization: Bearer <token>`; maps 409 bodies to their result; 401 → `unauthorized`; a thrown fetch → `unreachable`; `next('x')` adds `?session=x` URL-encoded.
- [ ] **Step 2:** `npx vitest run`; expect failures.
- [ ] **Step 3:** Implement `api.ts`, then `main.ts`, `setup.ts`, the pack script and README.
- [ ] **Step 4:** `npx vitest run` and `npx tsc --noEmit` pass. `CLAUDEMUX_REMOTE_ORIGIN=http://100.64.0.15:7474 node scripts/pack.mjs` produces `claudemux-glasses.ehpk`.
- [ ] **Step 5:** Commit `glasses: device wiring, setup page and pack script`.

### Task 13: Docs, then the on-device check (controller with Michael)

**Files:**
- Modify: `README.md` (a "Glasses remote" section after the web status page section: what it does, the `remote` config block, the token, that it is up only while the lobby is open, and that plan approvals now count as waiting)

- [ ] **Step 1:** Write the README section. Commit `README: glasses remote`.
- [ ] **Step 2:** Michael's steps, in order because the lobby reads config only at start: add `remote: {enabled: true}` to `config.yml` and the token to the env file, then `go install ./cmd/claudemux-head`, then confirm the lobby restarted on the new binary (`R` in the lobby if not).
- [ ] **Step 3:** `curl -H "Authorization: Bearer $TOKEN" http://100.64.0.15:7474/remote/next` from the Mac returns the current queue; without the header it returns 401.
- [ ] **Step 4:** Pack, upload and install the private build. In a scratch session, check each on the glasses: an AskUserQuestion answered by tap; a Bash permission prompt answered `Yes`; a broad option needing two taps; a plan approval; an idle session answered with a fixed reply; a suggested reply appearing; defer; skip; double tap to the fleet list and back; a tap after answering at the desk redraws instead of sending.
- [ ] **Step 5:** Record anything that differs from the plan in the findings doc and fix it before finishing the branch.
