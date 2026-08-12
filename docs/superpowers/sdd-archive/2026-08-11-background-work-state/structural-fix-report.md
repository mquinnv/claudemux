# Structural fix report: gate background-launch detection on tool identity

Base: `0f72b6d`. Working directory: `.claude/worktrees/align-context-meters`, branch `main`.

## Transcript verification of the agent-dispatch tool name (done before writing the gate)

Scanned **every** `~/.claude/projects/**/*.jsonl` (82 project dirs, including
`*/subagents/*.jsonl`), correlating each `tool_result` back to the `tool_use` block whose
`id` matches its `tool_use_id`, per file.

Results containing `Async agent launched`, grouped by the producing tool's `name`:

| count | tool `name` | payload starts with the sentence | has an `^agentId:` line |
|---|---|---|---|
| 1648 | **`Agent`** | yes | yes |
| 23 | `Read` | no | no |
| 18 | `Bash` | no | no |
| 1 | `AskUserQuestion` | no | no |

`Agent` is the only name that ever produces a real async launch — 1648 of the 1696 total
`Agent` dispatches on this machine; the other 48 are foreground agents (test #5's basis).
There is no `Task`-named dispatch tool in any transcript here: the `Task*` names that show
up (`TaskCreate` 570, `TaskUpdate` 1074, `TaskStop` 38, `TaskList` 1) are the todo-list
tools, unrelated. The `Read`/`Bash`/`AskUserQuestion` rows are precisely the false-positive
class this gate rejects. So the gate handles exactly one name, `Agent`, and that is
recorded in `bgAgentToolName` with the count in its comment.

Same correlation for the shell payload (`running in background with ID`), where "genuine"
means the payload matches `^Command running in background with ID: (id)\. Output is being
written to: \S*/tasks/\1\.output`:

| count | tool | `run_in_background == true` | genuine launch payload |
|---|---|---|---|
| 763 | `Bash` | yes | yes |
| 40 | `Bash` | **no** | yes |
| 21 | `Bash` | no | no (grep/sed/cat quoting it) |
| 35 | `Read` | no | no (reading a doc that quotes it) |
| 1 | `AskUserQuestion` | no | no |

**Finding worth flagging:** those 40 are real launches with no `run_in_background` flag —
Claude Code auto-backgrounds a foreground `Bash` that exceeds its timeout. The gate as
specified misses them (a false negative, ~5% of shell launches). It is not fixable without
reintroducing text-only detection, because those 40 are structurally identical to the 21
foreground greps that merely quote the sentence — same tool, same input shape; only the
text differs, and text is exactly what cannot be trusted. Left as specified, since a false
negative degrades to the pre-branch bug while a false positive hides a session from the
conductor for 30 minutes. Recorded in the spec's "Why not text-only detection".

Scan scripts: `scratchpad/scan.py`, `scan2.py`, `scan3.py` (not committed).

## Per requirement: what changed, and the covering test

### R1 — Decide launches from the tool, not the result text

`cmd/claudemux-head/bgwork.go`: `bgLaunchKindOf(ToolUse) (bgLaunchKind, bool)` is the gate.
`Bash` qualifies only with `Input["run_in_background"].(bool) == true` (type-asserted, per
the brief); `Agent` always qualifies as pending, and its result must additionally contain
`Async agent launched` to count as a launch. Anything else is not recorded at all.

`bgLaunches(Event)` is gone as a free function — deciding a launch now needs tracker state.
It is `func (b *bgTracker) launches(e Event) []string`, which looks each `ToolResult` up by
`ToolUseID` in the pending map, ignores it outright when absent, and otherwise applies that
kind's text rule and deletes the entry (one tool_use, one result).

Covering tests: `TestBgLaunchesInertWhenToolCannotLaunch` (20 subtests — 10 shapes × {under
a `Read`, under no recorded tool_use}), `TestBgForegroundBashDoesNotRegister`,
`TestBgForegroundAgentDoesNotRegister` (#5),
`TestBgLaunchRegistersUnderRealLaunchToolUse` (#4).

### R2 — Keep the text patterns only for the id, and relax the anchors

`bgShellRe` is now `running in background with ID: ([A-Za-z0-9]+)` and `bgAgentRe` is
`agentId: ([A-Za-z0-9]+)`; both absolute/`(?m)` anchors removed. Justified in-comment by
the 40-of-803 sweep above.

Covering test: `TestBgLaunchRegistersUnderRealLaunchToolUse` feeds a **grep-prefixed**
launch sentence (so it does not begin at byte 0) under a genuine `Bash` +
`run_in_background` tool_use and asserts it registers **and** retires under the id
`boigiwsir` extracted from that text. This is test #4 — the same bytes are asserted inert
under a `Read` in `TestBgLaunchesInertWhenToolCannotLaunch`. Both tests draw that string
from the same `bgQuotingShapes` builder, so they cannot drift apart.

### R3 — Correlation lives in `bgTracker`, persists across `observe`, and is bounded

`bgTracker` gained `pending map[string]bgPending{kind, at}`. `observe` walks `e.ToolUses`
first (recording only launch-capable ones), then completions, then `b.launches(e)`.
`outstanding` sweeps `pending` for entries past `bgMaxAge` in the same pass that expires
`tasks`. A genuine prompt still clears `tasks` and deliberately leaves `pending` alone —
commented: pending is an unresolved observation, not tracked work, and its result still
reports truthfully.

Covering tests: `TestBgTrackerLaunchSpansPollBatches` (#2 — tool_use in poll 1, result in
poll 2, with an explicit assertion that a lone tool_use is not yet a launch),
`TestBgTrackerPendingToolUseExpires` (#6 — a pending tool_use past the cap is dropped and
its late result finds nothing to resolve).

### R4 — Real payloads register, built the way production does

New `cmd/claudemux-head/testdata/launch-shell.jsonl` and `launch-agent.jsonl`: each is two
verbatim lines lifted from real transcripts under `~/.claude/projects` — the assistant
`tool_use` line and the `user` `tool_result` line — from this repo's own sessions. The test
runs each line through `parseEvent` and drives `observe`, then proves the extracted id by
retiring it with a `<task-notification>` carrying `boigiwsir` (shell) and
`a99a8221a00c2d373` (agent). The clock is read from the fixture's own timestamp so an aging
fixture never silently expires against a wall clock.

Covering test: `TestBgTrackerRegistersRealTranscriptLaunches` (#1).

### R5 — Quoting-shape tests replaced, not extended

`TestBgLaunches`, `TestBgLaunchesIgnoresQuotedPayloadsInRepoDocs`, and
`TestBgLaunchesIgnoresGrepLineQuotingSpec` are gone. In their place
`TestBgLaunchesInertWhenToolCannotLaunch` says the new thing: under a tool that cannot
launch, a quoted payload is not a near-miss, it is simply irrelevant. Ten shapes, all
required by the brief (nine plus a grep-prefixed agent line), each run twice:

1. the actual bytes of the design spec
2. the actual bytes of the plan doc
3. a bare line quoting the spec's shell example, no path prefix (the shape that fires today)
4. a pretty-printed agent payload with REAL newlines
5. a fenced code block quoting the spec's agent example unescaped
6. a terminal transcript containing the agent payload
7. Go source with a raw string literal containing it
8. a log line containing it
9. a `grep -n` style prefixed line quoting the shell example
10. a `grep -n` style prefixed line quoting the agent example

Shapes 3–10 are derived from the docs' real bytes at run time (`bgFindLine` `t.Fatal`s if
the shape disappears), not hand-written to fit the fix.

### R6 — The vacuity guard fixed

`bgReadDoc` no longer guards on bare `strings.Contains` for the two marker strings — that
prose now also lives in the Detection-rules section, so reflowing the worked examples would
have left the guard passing and the test vacuous. It now requires the doc to contain a line
that **begins** with the shell sentence, or a line carrying **both** `Async agent launched`
and `agentId:` — the shapes that can actually be mistaken for a launch — and fails loudly
otherwise.

### R7 — Callers updated

`observe`'s signature is unchanged, so `tui.go` needed no edit (`m.bg.observe`,
`m.bg.outstanding`, `newBgTracker` all still valid). Three call sites in `tui_test.go`
(`TestModelBackgroundStateFromPoll`, `TestSwitchSessionResetsBackgroundWork`,
`TestUpdateDataMsgFeedsBackgroundTracker`) used the old single-event `bgLaunchEvent`
helper; they now use `bgShellLaunch(id, ts) []Event`, which emits the tool_use event and
its result event as a real poll would. No assertion in those tests changed.

`isWaiting`, `swAgeColW`, `switchSession` seeding, `bgCompletions`, and the events parser
are untouched.

### R8 — Spec updated

`docs/superpowers/specs/2026-08-11-background-work-state-design.md`, "Detection rules":
the anchoring argument and the `\n`-escaping discussion are replaced with the structural
rule (gate table, verified `Agent` name with counts, the pending-map correlation and its
bound), plus a new "Why not text-only detection" subsection recording that it was tried
twice, why the class cannot be closed from the text side, and the auto-backgrounded-Bash
false negative the gate knowingly accepts.

## Commands run, and their output

```
$ go build ./... && go vet ./... && gofmt -l .
(no output — clean)

$ go test ./cmd/claudemux-head/
ok  	github.com/mquinnv/claudemux/cmd/claudemux-head	3.504s

$ go test ./...
ok  	github.com/mquinnv/claudemux/cmd/claudemux-head	(cached)
```

`TestEnvFileValueRetriesTransientlyEmptyFIFO` did not flake on these runs; untouched either
way.

## Evidence order

Tests were written first and run against the **unchanged** `bgwork.go` (only the three
`tui_test.go` call sites were adjusted first, since the old `bgLaunchEvent` helper no longer
existed and the package would not otherwise compile).

### Run 1 — new tests against old code (`0f72b6d` implementation)

```
$ go test ./cmd/claudemux-head/ -run 'TestBg|TestModelBackgroundStateFromPoll|TestSwitchSessionResetsBackgroundWork|TestUpdateDataMsgFeedsBackgroundTracker' -v
```

Failed (14 failing test/subtest lines):

- `TestBgLaunchesInertWhenToolCannotLaunch/{read,unrecorded}/a bare line quoting the shell example` — `outstanding = 1, want 0`
- `TestBgLaunchesInertWhenToolCannotLaunch/{read,unrecorded}/a pretty-printed agent payload with real newlines` — `outstanding = 1, want 0`
- `TestBgLaunchesInertWhenToolCannotLaunch/{read,unrecorded}/a fenced code block quoting the agent example unescaped` — `outstanding = 1, want 0`
- `TestBgLaunchesInertWhenToolCannotLaunch/{read,unrecorded}/a terminal transcript containing the agent payload` — `outstanding = 1, want 0`
- `TestBgLaunchesInertWhenToolCannotLaunch/{read,unrecorded}/Go source with a raw string literal containing the agent payload` — `outstanding = 1, want 0`
- `TestBgLaunchRegistersUnderRealLaunchToolUse` — `outstanding = 0, want 1: the same text a Read cannot launch with must launch under a background Bash`
- `TestBgForegroundBashDoesNotRegister` — `outstanding = 1, want 0: a foreground Bash quoting the sentence launched nothing`
- `TestBgTrackerPendingToolUseExpires` — `outstanding = 1, want 0: a pending tool_use past the cap must have been dropped`

Passed for the right reason: `TestBgTrackerRegistersRealTranscriptLaunches` (both kinds),
`TestBgTrackerLaunchSpansPollBatches`, `TestBgForegroundAgentDoesNotRegister`, and every
pre-existing tracker/completion test. Passed for the *wrong* reason (old code rejected them
on anchors, not on tool identity): the two whole-doc reads, the log line, and both
`grep -n` prefixed lines.

This is exactly the brief's prediction: #3's bare-line and pretty-printed shapes and #4
fail first.

### Run 2 — same tests against the new code

```
$ go test ./cmd/claudemux-head/ -run 'TestBg|TestModelBackgroundStateFromPoll|TestSwitchSessionResetsBackgroundWork|TestUpdateDataMsgFeedsBackgroundTracker' -v
...
--- PASS: TestBgTrackerRegistersRealTranscriptLaunches (0.00s)
    --- PASS: .../launch-shell.jsonl
    --- PASS: .../launch-agent.jsonl
--- PASS: TestBgTrackerLaunchSpansPollBatches
--- PASS: TestBgLaunchesInertWhenToolCannotLaunch   (all 20 subtests)
--- PASS: TestBgLaunchRegistersUnderRealLaunchToolUse
--- PASS: TestBgForegroundBashDoesNotRegister
--- PASS: TestBgForegroundAgentDoesNotRegister
--- PASS: TestBgTrackerPendingToolUseExpires
--- PASS: TestBgCompletions (4 subtests)
--- PASS: TestBgTrackerPairsLaunchAndCompletion
--- PASS: TestBgTrackerCountsAndOldest
--- PASS: TestBgTrackerExpiresStaleLaunches
--- PASS: TestBgTrackerClearedByGenuinePrompt
--- PASS: TestBgTrackerNotificationTurnIsNotAPrompt
--- PASS: TestBgTrackerIdempotentAcrossCompletionForms
--- PASS: TestBgTrackerCompletionForUnknownIDIsHarmless
--- PASS: TestBgTrackerSameIDLaunchedTwice
--- PASS: TestModelBackgroundStateFromPoll
--- PASS: TestSwitchSessionResetsBackgroundWork
--- PASS: TestUpdateDataMsgFeedsBackgroundTracker
PASS
ok  	github.com/mquinnv/claudemux/cmd/claudemux-head	0.294s
```

Zero failures.

## Constraints

No new module dependencies (`go.mod` untouched). `go build`, `go vet`, `go test`,
`gofmt -l .` all clean. Comments explain WHY at the surrounding density. No existing test
assertion changed except the quoting-shape tests the brief replaced.
