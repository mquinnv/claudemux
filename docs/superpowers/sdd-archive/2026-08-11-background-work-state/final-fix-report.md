# Final whole-branch review: fix report

Branch: `worktree-align-context-meters` (claudemux). Base HEAD at start: `b9ee17e`.
Final HEAD after fixes: `7281b88`.

Commits, in order:

1. `6d1ca2f` — fix(head): anchor background-launch markers to their launch sentence (Findings 1, 5, 6)
2. `d319866` — fix(head): clip lobby line 2/status/footer and truncate cells by width (Findings 2, 3, 4)
3. `7281b88` — fix(head): summarize on busy-to-Background too; give it the busy dot (Findings 7, 8)

All three commits individually leave `go build ./... && go vet ./... && go test ./cmd/claudemux-head/` green and `gofmt -l .` empty (verified after each commit and again at final HEAD, see "Final verification" below).

---

## Finding 1 (Important, merge blocker) — phantom launches from quoted markers

**File:** `cmd/claudemux-head/bgwork.go`

**What changed:**
- `bgShellRe` is now anchored to the start of a line: `(?m)^Command running in background with ID: ([A-Za-z0-9]+)` (was: unanchored `running in background with ID: ([A-Za-z0-9]+)`).
- `bgAgentRe` (`agentId: ([A-Za-z0-9]+)`) is unchanged as a pattern, but `bgLaunches` now only applies it to a `tool_result` whose text also contains the literal `"Async agent launched"` (new constant `bgAgentLaunchMarker`). An anchored-alone `agentId:` was not enough, because the real payload — and any doc quoting it — puts `agentId:` on its own line inside a larger block; only the launch sentence distinguishes a real launch from a quote.
- The real payloads (background shell string content, async agent array-of-text-blocks content) still match, verified against the fixtures copied from the design spec's "Event shapes" section (`docs/superpowers/specs/2026-08-11-background-work-state-design.md`).

**Covering tests:** `cmd/claudemux-head/bgwork_test.go`, `TestBgLaunches` subtests:
- `background shell` / `async agent` (unchanged positive cases — still pass, prove the fix didn't loosen the real match).
- `a Grep hit quoting agentId is not a launch` — new, fixture `src/agent.ts:42:  const opts = { agentId: agentRecord.id };`.
- `the shell marker mid-sentence, not at line start, is not a launch` — new, fixture `As documented: Command running in background with ID: someid appears mid-paragraph.`

Both new fixtures were confirmed **non-vacuous**: with `cmd/claudemux-head/bgwork.go` reverted to the pre-fix regex (test file kept at the new version), both fail:
```
bgwork_test.go:49: bgLaunches = ["agentRecord"], want none
bgwork_test.go:49: bgLaunches = ["someid"], want none
```

**Exact command run:**
```
go test ./cmd/claudemux-head/ -run 'TestBgLaunches' -v
```
**Output (post-fix):**
```
=== RUN   TestBgLaunches
=== RUN   TestBgLaunches/background_shell
=== RUN   TestBgLaunches/async_agent
=== RUN   TestBgLaunches/an_ordinary_tool_result_launches_nothing
=== RUN   TestBgLaunches/a_Grep_hit_quoting_agentId_is_not_a_launch
=== RUN   TestBgLaunches/the_shell_marker_mid-sentence,_not_at_line_start,_is_not_a_launch
=== RUN   TestBgLaunches/no_tool_results
--- PASS: TestBgLaunches (0.00s)
    --- PASS: TestBgLaunches/background_shell (0.00s)
    --- PASS: TestBgLaunches/async_agent (0.00s)
    --- PASS: TestBgLaunches/an_ordinary_tool_result_launches_nothing (0.00s)
    --- PASS: TestBgLaunches/a_Grep_hit_quoting_agentId_is_not_a_launch (0.00s)
    --- PASS: TestBgLaunches/the_shell_marker_mid-sentence,_not_at_line_start,_is_not_a_launch (0.00s)
    --- PASS: TestBgLaunches/no_tool_results (0.00s)
PASS
ok  	github.com/mquinnv/claudemux/cmd/claudemux-head	0.300s
```

---

## Finding 2 (Important) — lobby detail/status/footer lines can wrap and destroy the grid

**File:** `cmd/claudemux-head/switchboardtui.go`

**What changed:** Line 2 (the summary/prompt "detail" line) previously measured overflow with `len([]rune(detail)) > m.width-6` — a rune count — and truncated with `truncateRunes`, with no final `clipLine` pass on the assembled `"    " + detail` line. Replaced with the same pattern line 1 already used: assemble the full styled line, then `clipLine(line2, m.width)` when `m.width > 0`. The status line and the (cosmetic) key-hint footer had the same gap — no width guard at all — and now go through the identical `clipLine` guard, for consistency with line 1 and line 2.

**Covering test:** `cmd/claudemux-head/switchboardtui_test.go`, `TestSwModelViewNeverExceedsWidth` — renders a row with an overflowing name, state, topic, and summary at `m.width = 40` and asserts every line of `m.View()` measures `<= m.width` in display cells (`lipgloss.Width`).

Confirmed non-vacuous: reverting `switchboardtui.go` to pre-fix (test file kept at new version) fails on the unclipped footer:
```
switchboardtui_test.go:200: line measures 56 cells, want <= 40: "space conduct/standby · j/k select · enter jump · q quit"
```

**Exact command run:**
```
go test ./cmd/claudemux-head/ -run 'TestSwModelViewNeverExceedsWidth' -v
```
**Output (post-fix):**
```
=== RUN   TestSwModelViewNeverExceedsWidth
--- PASS: TestSwModelViewNeverExceedsWidth (0.00s)
PASS
ok  	github.com/mquinnv/claudemux/cmd/claudemux-head	0.291s
```

---

## Finding 3 (Minor) — wide runes overrun the fixed columns

**File:** `cmd/claudemux-head/switchboardtui.go`

**What changed:** `swCell` and the name-column truncation (in `swModel.View`) both truncated with `truncateRunes` (rune count) and then padded with `swPad`/manual width math (display cells). A CJK cell clipped to `w` runes could still measure up to `2w` cells, overrunning its column budget and shifting every column to its right. Both call sites now truncate with `ansi.Truncate(text, w, "…")` — the same display-width-aware, ANSI-safe truncation `clipLine` already uses — before padding.

**Covering test:** `cmd/claudemux-head/switchboardtui_test.go`, `TestSwModelViewClipsWideRuneNameToColumn` — renders a session whose name is `swNameColW+5` CJK runes and asserts the topic (which sits after the fixed name/state/age/context columns) still starts at the exact expected cell column, using `ansi.Cut` (cell-width-aware) rather than rune indexing to extract the check region.

Confirmed non-vacuous: reverting `switchboardtui.go` to pre-fix, this test fails with no row found at all (the topic gets pushed off past the expected column and the alignment breaks so badly the row can't even be located by content match in one run, and in another run mis-locates the topic text). Representative failure captured during verification:
```
=== RUN   TestSwModelViewClipsWideRuneNameToColumn
    switchboardtui_test.go:230: no row rendered:
        claudemux switchboard
         ● 囲囲囲囲囲囲囲囲囲囲囲囲囲囲囲囲囲囲囲囲囲囲囲… Idle            0:00  ██░░░  42%  wide-rune-name…
        conducting · 1 waiting
        space conduct/standby · j/k select · enter jump · q quit
--- FAIL: TestSwModelViewClipsWideRuneNameToColumn (0.00s)
```
(Note: the topic itself got truncated by the unfixed `clipLine`-on-line-1 fallback into `"wide-rune-name…"`, no longer matching the literal topic string used as the row-locator — direct, visible evidence of the overrun.)

**Exact command run:**
```
go test ./cmd/claudemux-head/ -run 'TestSwModelViewClipsWideRuneNameToColumn' -v
```
**Output (post-fix):**
```
=== RUN   TestSwModelViewClipsWideRuneNameToColumn
--- PASS: TestSwModelViewClipsWideRuneNameToColumn (0.00s)
PASS
ok  	github.com/mquinnv/claudemux/cmd/claudemux-head	0.291s
```

---

## Finding 4 (Minor) — nothing asserts lobby line 1 is clipped

**File:** `cmd/claudemux-head/switchboardtui_test.go`

**What changed:** `TestSwModelViewNeverExceedsWidth` (added for Finding 2, above) doubles as the mutation guard for line 1: it asserts every rendered line (including line 1) measures `<= m.width` in cells, for a row assembled to be far wider than the pane before clipping.

**Verified as a real mutation guard:** with `line = clipLine(line, m.width)` on line 1 removed (only that one guard removed, everything else at the post-fix state), the test fails:
```
=== RUN   TestSwModelViewNeverExceedsWidth
    switchboardtui_test.go:200: line measures 135 cells, want <= 40: " ● a-very-long-session-nam… Tool:AskUserQ…  3h0m  █████ 100%  a topic long enough to overflow the configured pane width many times over"
--- FAIL: TestSwModelViewNeverExceedsWidth (0.00s)
```
This matches the reviewer's mutation-testing method exactly (no-op the line-1 clip, confirm the suite goes red).

**Exact command run:** same as Finding 2's (`TestSwModelViewNeverExceedsWidth`), see above for post-fix passing output.

---

## Finding 5 (Minor) — the "prose is not a launch" test was vacuous

**File:** `cmd/claudemux-head/bgwork_test.go`

**What changed:** Removed the vacuous `"prose about background work is not a launch"` case (fixture `"the job is running in background somewhere, no ID here"` — contained no id at all, so it passed regardless of any guard). Replaced with the two fixtures described under Finding 1 (`a Grep hit quoting agentId is not a launch`, `the shell marker mid-sentence, not at line start, is not a launch`), both of which extract a real id under the pre-fix regex and are confirmed to fail without the fix. The positive fixtures (`background shell`, `async agent`) were kept unchanged to prove the real payloads still register.

**Covering test / command / output:** identical to Finding 1's `TestBgLaunches` run above.

---

## Finding 6 (Minor) — spec-mandated tracker cases untested

**File:** `cmd/claudemux-head/bgwork_test.go`

**What changed:** Added three new tracker tests per the design spec's testing section:
- `TestBgTrackerIdempotentAcrossCompletionForms` — the same task id retired via both the queue-operation form and the delivered user-turn form must stay retired (harmless double-retirement).
- `TestBgTrackerCompletionForUnknownIDIsHarmless` — a completion for an id that was never launched is a no-op, not a crash or a negative count.
- `TestBgTrackerSameIDLaunchedTwice` — relaunching the same id (e.g. a retried tool call) does not double-count `outstanding()`.

All three were "benign by inspection" per the finding; these tests exist to keep them that way, not because a bug was found.

**Exact command run:**
```
go test ./cmd/claudemux-head/ -run 'TestBgTracker' -v
```
**Output:**
```
=== RUN   TestBgTrackerPairsLaunchAndCompletion
--- PASS: TestBgTrackerPairsLaunchAndCompletion (0.00s)
=== RUN   TestBgTrackerCountsAndOldest
--- PASS: TestBgTrackerCountsAndOldest (0.00s)
=== RUN   TestBgTrackerExpiresStaleLaunches
--- PASS: TestBgTrackerExpiresStaleLaunches (0.00s)
=== RUN   TestBgTrackerClearedByGenuinePrompt
--- PASS: TestBgTrackerClearedByGenuinePrompt (0.00s)
=== RUN   TestBgTrackerNotificationTurnIsNotAPrompt
--- PASS: TestBgTrackerNotificationTurnIsNotAPrompt (0.00s)
=== RUN   TestBgTrackerIdempotentAcrossCompletionForms
--- PASS: TestBgTrackerIdempotentAcrossCompletionForms (0.00s)
=== RUN   TestBgTrackerCompletionForUnknownIDIsHarmless
--- PASS: TestBgTrackerCompletionForUnknownIDIsHarmless (0.00s)
=== RUN   TestBgTrackerSameIDLaunchedTwice
--- PASS: TestBgTrackerSameIDLaunchedTwice (0.00s)
PASS
ok  	github.com/mquinnv/claudemux/cmd/claudemux-head	0.230s
```

---

## Finding 7 (Minor) — StateBackground suppresses the summary refresh edge

**File:** `cmd/claudemux-head/tui.go`

**What changed:** `shouldSummarize` compared `prevKind != StateIdle && m.state.Kind == StateIdle`, so a turn that ended with background work outstanding (landing on `StateBackground`, not `StateIdle`) never crossed the edge, and the Haiku summary/topic went stale until the background work cleared. Introduced `turnEndedByIdle(kind StateKind) bool` (`kind == StateIdle || kind == StateBackground`) and rewrote the comparison as `!turnEndedByIdle(prevKind) && turnEndedByIdle(m.state.Kind)`. This makes Thinking/Tool → Background fire the edge (the actual bug fix) while Idle↔Background transitions do **not** fire an extra spurious call (both sides read as "ended" already).

Checked other sites keying on `StateIdle` for the same "turn ended" meaning: `teardownTurnEnded` (`teardown.go:204`) already falls through to `true` for every kind except `Thinking`/`Tool`/`Compacting`, so it already treats `StateBackground` as ended correctly — no change needed there, per the finding's own note. All other `StateIdle` references are the enum definition, `classifyState`/`bgOverride` internals, or `Label`/`statePublishValue` formatting switches, not "turn ended" logic.

**Covering tests:** `cmd/claudemux-head/tui_test.go`, `TestShouldSummarize` — five new subtests added to the existing table: `busy to background fires`, `tool to background fires`, `idle to background does not fire`, `background to idle does not fire`, `still background does not fire`.

**Exact command run:**
```
go test ./cmd/claudemux-head/ -run 'TestShouldSummarize' -v
```
**Output:**
```
=== RUN   TestShouldSummarize
=== RUN   TestShouldSummarize/busy_to_idle_fires
=== RUN   TestShouldSummarize/tool_to_idle_fires
=== RUN   TestShouldSummarize/still_idle_does_not_fire
=== RUN   TestShouldSummarize/going_busy_does_not_fire
=== RUN   TestShouldSummarize/a_call_already_in_flight_does_not_fire
=== RUN   TestShouldSummarize/a_burst_inside_the_minimum_interval_does_not_fire
=== RUN   TestShouldSummarize/no_summarizer_never_fires
=== RUN   TestShouldSummarize/busy_to_background_fires
=== RUN   TestShouldSummarize/tool_to_background_fires
=== RUN   TestShouldSummarize/idle_to_background_does_not_fire
=== RUN   TestShouldSummarize/background_to_idle_does_not_fire
=== RUN   TestShouldSummarize/still_background_does_not_fire
--- PASS: TestShouldSummarize (0.00s)
    (all subtests PASS)
PASS
ok  	github.com/mquinnv/claudemux/cmd/claudemux-head	0.308s
```

---

## Finding 8 (Minor) — stateDot has no StateBackground case

**File:** `cmd/claudemux-head/tui.go`

**What changed:** `stateDot` fell through to the `default` case (`dotIdle`) for `StateBackground`. Added an explicit `case StateBackground: return dotTool`, matching how `Thinking`/`Tool` render busy.

**Covering test:** `cmd/claudemux-head/tui_test.go`, `TestStateDotBackgroundIsBusy`. Note: `dotIdle` and `dotTool` render as byte-identical plain `"●"` in this non-TTY test binary (lipgloss strips all ANSI color when stdout isn't a terminal, and these are package-level vars evaluated once at program init, before any test can force a color profile) — so a direct string comparison of the real values can't distinguish them. The test swaps `dotIdle`/`dotTool` for distinguishable sentinel strings for its duration (they're ordinary mutable package vars, not consts) and restores them via `defer`, then asserts `stateDot(StateBackground) == dotTool`.

Confirmed non-vacuous: with the `StateBackground` case removed from `stateDot` (fix reverted, test kept), it fails:
```
tui_test.go:2043: stateDot(StateBackground) = "SENTINEL-IDLE", want dotTool (busy), not the idle dot
```

**Exact command run:**
```
go test ./cmd/claudemux-head/ -run 'TestStateDotBackgroundIsBusy' -v
```
**Output (post-fix):**
```
=== RUN   TestStateDotBackgroundIsBusy
--- PASS: TestStateDotBackgroundIsBusy (0.00s)
PASS
ok  	github.com/mquinnv/claudemux/cmd/claudemux-head	0.293s
```

---

## Explicitly not touched (per scope)

- Age column truncation of durations >= 100h (`swAgeColW = 6`) — accepted cosmetic, unchanged.
- `switchSession`'s tracker reset without observing seeded events — spec's documented "Seeding" limitation, unchanged.
- `isWaiting` in `switchboard.go` — still exact-matches only `"Idle"` and `"Tool:AskUserQuestion"`, unchanged.

## Final verification

Run at final HEAD (`7281b88`):

```
go build ./...      → exit 0, no output
go vet ./...         → exit 0, no output
gofmt -l .            → empty (no files listed)
go test ./cmd/claudemux-head/ -count=1 -v   → PASS, 421/421 subtests passed, 0 failed
go test ./cmd/claudemux-head/ -count=1      → ok  	github.com/mquinnv/claudemux/cmd/claudemux-head	3.533s
```

---

# Second fix round: Finding 1 survived in narrowed form

The re-review (explicitly authorized by the human partner) confirmed Findings 2-8 addressed
and mutation-verified, but found Finding 1 NOT fully addressed: `bgwork.go` gated an
UNANCHORED `agentId: ([A-Za-z0-9]+)` on the substring `"Async agent launched"` appearing
anywhere in the tool_result. Any document that QUOTES the launch payload — including this
repo's own `docs/superpowers/specs/2026-08-11-background-work-state-design.md` (line 59)
and `docs/superpowers/plans/2026-08-11-background-work-state.md` — contains both strings on
one physical line and still registered a phantom launch. The round-1 negative fixtures were
synthetic one-liners written against the fix rather than against the case the finding
actually named, which is why this slipped through.

Commit: `0f72b6d` — fix(head): close the remaining phantom-launch gap on quoted payloads.

Final HEAD after this round: `0f72b6d`.

## Step 1 — new tests written first, run against the CURRENT (pre-fix) code, confirmed to FAIL

Added to `cmd/claudemux-head/bgwork_test.go`:
- `repoDocPath(t, relPath)` — locates a file relative to the repo root via `runtime.Caller(0)`,
  the same pattern `worktreeHookPath` uses in `worktreehook_test.go`, so the test doesn't
  depend on the working directory.
- `TestBgLaunchesIgnoresQuotedPayloadsInRepoDocs` — reads the real bytes of
  `docs/superpowers/specs/2026-08-11-background-work-state-design.md` and
  `docs/superpowers/plans/2026-08-11-background-work-state.md` from disk, feeds each whole
  file as one `tool_result.Content`, and asserts `bgLaunches` returns nothing. Guarded: first
  asserts the file still contains both `"Async agent launched"` and
  `"running in background with ID:"` verbatim, `t.Fatal`ing with a clear message if not — so
  a future doc edit that drops either marker fails loudly instead of quietly turning the test
  vacuous.
- `TestBgLaunchesIgnoresGrepLineQuotingSpec` — locates the actual line in the spec file that
  contains both `"Async agent launched"` and `"agentId:"` (found by content match, not a
  hardcoded line number, so it tracks the file if it's edited), formats it as a `grep -n`-style
  single line (`path:lineno:text`), and asserts `bgLaunches` returns nothing for that one line
  by itself.

Commands run and output, against the pre-fix `bgwork.go` (i.e. before this round's regex
changes — round 1's fixes were already in place):

```
$ go build ./...
(no output, exit 0)

$ go test ./cmd/claudemux-head/ -run 'TestBgLaunchesIgnoresQuotedPayloadsInRepoDocs|TestBgLaunchesIgnoresGrepLineQuotingSpec' -v
=== RUN   TestBgLaunchesIgnoresQuotedPayloadsInRepoDocs
=== RUN   TestBgLaunchesIgnoresQuotedPayloadsInRepoDocs/docs/superpowers/specs/2026-08-11-background-work-state-design.md
    bgwork_test.go:117: bgLaunches = ["boigiwsir" "afbbf7a8f9ee52e81"], want none: reading this repo's own docs must not launch anything
=== RUN   TestBgLaunchesIgnoresQuotedPayloadsInRepoDocs/docs/superpowers/plans/2026-08-11-background-work-state.md
    bgwork_test.go:117: bgLaunches = ["afbbf7a8f9ee52e81"], want none: reading this repo's own docs must not launch anything
--- FAIL: TestBgLaunchesIgnoresQuotedPayloadsInRepoDocs (0.00s)
    --- FAIL: TestBgLaunchesIgnoresQuotedPayloadsInRepoDocs/docs/superpowers/specs/2026-08-11-background-work-state-design.md (0.00s)
    --- FAIL: TestBgLaunchesIgnoresQuotedPayloadsInRepoDocs/docs/superpowers/plans/2026-08-11-background-work-state.md (0.00s)
=== RUN   TestBgLaunchesIgnoresGrepLineQuotingSpec
    bgwork_test.go:145: bgLaunches = ["afbbf7a8f9ee52e81"], want none: a grep line quoting the spec must not launch anything
--- FAIL: TestBgLaunchesIgnoresGrepLineQuotingSpec (0.00s)
FAIL
FAIL	github.com/mquinnv/claudemux/cmd/claudemux-head	0.292s
```

This reproduces the exact scenario the coordinator named, byte for byte: the spec doc read
launches both `"boigiwsir"` (the shell marker) and `"afbbf7a8f9ee52e81"` (the agent marker);
the plan doc read launches the agent marker; the grep-line launches the agent marker.

## Step 2 — apply the regex change, confirm the new tests pass

Two changes to `cmd/claudemux-head/bgwork.go`, both discovered necessary by the test output
above (the coordinator's proposed change alone left the shell-marker half of the spec-doc
failure unexplained and unfixed — see "why two anchors, not one" below):

1. `bgAgentRe` anchored to the start of a real physical line, keeping the launch-sentence
   gate: `(?m)^agentId: ([A-Za-z0-9]+)`, only applied when the same `tool_result` also
   contains `"Async agent launched"`. This is the change the coordinator specified. It works
   because `flattenText` (`events.go:45`) calls `json.Unmarshal` on each text block, which
   decodes a JSON string's `\n` escape into a real newline character — so in a REAL
   tool_result, `agentId:` genuinely starts a physical line. In a doc that quotes the raw,
   *unparsed* JSON text (the design spec's line 59, the plan doc's Go-source fixtures), the
   `\n` between the launch sentence and `agentId:` is two literal characters on ONE physical
   line, so `agentId:` never begins a true line there and the anchor rejects it.

2. **Additional, necessary fix not covered by the coordinator's proposed change**: after
   applying only the `bgAgentRe` anchor, `TestBgLaunchesIgnoresQuotedPayloadsInRepoDocs`
   still failed for the spec doc alone, on `"boigiwsir"`:
   ```
   bgwork_test.go:117: bgLaunches = ["boigiwsir"], want none: reading this repo's own docs must not launch anything
   ```
   Cause: the spec doc quotes the shell-launch sentence as a **standalone paragraph** in a
   fenced code block (`docs/.../2026-08-11-background-work-state-design.md:51`) — a real
   physical line in the raw markdown, with a real newline before it. Round 1's
   `(?m)^Command running in background with ID: ...` anchor (start of any line) therefore
   still matched it; a per-line anchor cannot tell a doc's quoted paragraph apart from a real
   payload, because both are, structurally, "a line that starts with the sentence." Fixed by
   dropping `(?m)` and anchoring `bgShellRe` to the ABSOLUTE start of the whole tool_result
   string instead: `^Command running in background with ID: ([A-Za-z0-9]+)`. This is sound
   because a background shell's `tool_result.content` per the design spec **is** the launch
   sentence — nothing precedes it in a real payload — whereas a doc being read always has
   other text (the rest of the file) ahead of the quoted line within the same tool_result
   content, so the sentence is never byte 0 of it.

Commands run and output, after both changes:

```
$ go build ./...
(no output, exit 0)

$ go test ./cmd/claudemux-head/ -run 'TestBgLaunches' -v
=== RUN   TestBgLaunches
=== RUN   TestBgLaunches/background_shell
=== RUN   TestBgLaunches/async_agent
=== RUN   TestBgLaunches/an_ordinary_tool_result_launches_nothing
=== RUN   TestBgLaunches/a_Grep_hit_quoting_agentId_is_not_a_launch
=== RUN   TestBgLaunches/the_shell_marker_mid-sentence,_not_at_line_start,_is_not_a_launch
=== RUN   TestBgLaunches/no_tool_results
--- PASS: TestBgLaunches (0.00s)
    --- PASS: TestBgLaunches/background_shell (0.00s)
    --- PASS: TestBgLaunches/async_agent (0.00s)
    --- PASS: TestBgLaunches/an_ordinary_tool_result_launches_nothing (0.00s)
    --- PASS: TestBgLaunches/a_Grep_hit_quoting_agentId_is_not_a_launch (0.00s)
    --- PASS: TestBgLaunches/the_shell_marker_mid-sentence,_not_at_line_start,_is_not_a_launch (0.00s)
    --- PASS: TestBgLaunches/no_tool_results (0.00s)
=== RUN   TestBgLaunchesIgnoresQuotedPayloadsInRepoDocs
=== RUN   TestBgLaunchesIgnoresQuotedPayloadsInRepoDocs/docs/superpowers/specs/2026-08-11-background-work-state-design.md
=== RUN   TestBgLaunchesIgnoresQuotedPayloadsInRepoDocs/docs/superpowers/plans/2026-08-11-background-work-state.md
--- PASS: TestBgLaunchesIgnoresQuotedPayloadsInRepoDocs (0.00s)
    --- PASS: TestBgLaunchesIgnoresQuotedPayloadsInRepoDocs/docs/superpowers/specs/2026-08-11-background-work-state-design.md (0.00s)
    --- PASS: TestBgLaunchesIgnoresQuotedPayloadsInRepoDocs/docs/superpowers/plans/2026-08-11-background-work-state.md (0.00s)
=== RUN   TestBgLaunchesIgnoresGrepLineQuotingSpec
--- PASS: TestBgLaunchesIgnoresGrepLineQuotingSpec (0.00s)
PASS
ok  	github.com/mquinnv/claudemux/cmd/claudemux-head	0.291s
```

As an additional sanity check (not committed as a permanent test — outside what was asked,
but useful corroboration), a throwaway test read `bgwork_test.go` and `events_test.go`
themselves (both quote the payloads too, per the coordinator's evidence list) and confirmed
`bgLaunches` returns `[]` for both:
```
zz_manual_check_test.go:18: bgwork_test.go -> []
zz_manual_check_test.go:18: events_test.go -> []
```
(File removed after the check; not part of the committed diff.)

## Step 3 — existing positive tests still pass (both real payload shapes still register)

From the same `TestBgLaunches` run above: `background_shell` (bare-string content shape) and
`async_agent` (array-of-text-blocks content shape, matching the design spec's two verified
"Event shapes") both PASS — the real payloads still register a launch.

Full package suite, and static checks, at final HEAD `0f72b6d`:

```
$ go build ./...
(no output, exit 0)

$ go vet ./...
(no output, exit 0)

$ gofmt -l .
(no output — nothing needs formatting)

$ go test ./cmd/claudemux-head/ -count=1
ok  	github.com/mquinnv/claudemux/cmd/claudemux-head	3.349s
```

## Documentation updated

`docs/superpowers/specs/2026-08-11-background-work-state-design.md`, "Detection rules"
section (previously lines ~89-94): rewritten to document the two anchors actually shipped
(whole-string anchor for the shell marker, per-line anchor + launch-sentence gate for the
agent marker), explain *why* they differ (the two payload shapes differ structurally), and
state the false-negative/false-positive trade-off explicitly: a false negative (wording
drift) degrades to pre-branch behavior and is visible in a failing fixture; a false positive
hides a session from the conductor for up to `bgMaxAge`, undetectably — the strictly worse
failure, which is why detection is conservative rather than permissive.

## Not touched, per the coordinator's explicit scope

`isWaiting`, `swAgeColW`, and `switchSession`'s seeding behavior — untouched, as instructed.
No new module dependencies were added; `runtime`, `os`, `path/filepath`, `strings`, `fmt` used
in the new test code are all standard library, already imported elsewhere in this package.
