# Harness-signal detection — report

Branch tip before: `5350953`. Change committed on top of it.

## 1. Field verification

Swept **all 1915 `*.jsonl` transcripts under `~/.claude/projects`** (82 project dirs,
including subagent transcripts) with a script that, per entry, mapped `tool_result` →
producing `tool_use` name so each signal could be attributed to a tool.

The brief's spellings were all correct. Verified names, casing, types and cardinality:

| Field | Casing | Type | Present | Non-empty / true | Producing tool | False positives |
|---|---|---|---|---|---|---|
| `toolUseResult.backgroundTaskId` | as written | `string` | 821 | 821 | `Bash` ×821 | 0 |
| `toolUseResult.isAsync` | as written | `bool` | 1596 | true ×1596 (never false) | `Agent` ×1596 | 0 |
| `toolUseResult.agentId` | as written | `string` | 1596 (all alongside `isAsync:true`) | 1596 | `Agent` ×1596 | 0 |

No alternate casings of any of the three exist anywhere in the corpus. No tool other than
`Bash`/`Agent` ever writes them. The brief's counts were 821 (exact match) and 1555 for the
agent leg; I measure **1596** — the corpus has grown since the reviewer's sweep. Direction
and conclusion are unchanged.

Additional facts established, which the brief did not state and the parser depends on:

- **`toolUseResult` is a non-object more often than the brief implies.** Of 77548 entries
  carrying it: 70785 objects, **4035 bare strings**, **2728 arrays**. The array case is not
  mentioned in the brief; both are handled by the ignore-failure decode.
- **A foreground `Agent` never produces an object `toolUseResult`.** All 1625 `Agent`
  results carrying one are either `isAsync:true` objects (1596) or plain strings (29).
  There is no `isAsync:false`. So the agent leg needs no text check at all — this is the
  residual the previous round could not close.
- **Launch id == completion id, confirmed independently.** Of 2511 distinct `<task-id>`
  values in notification payloads, 1561 match an `agentId` and 764 a `backgroundTaskId`;
  the 186 unmatched are notifications whose launch is in a rotated/absent transcript.
  1561 of 1564 distinct `agentId`s were notified. So retiring by the harness id is exact
  for both kinds.
- **The `isAsync`/`agentId` pair is what recovers the agent leg**, and
  `backgroundTaskId` recovers 102 shell launches that `input.run_in_background` cannot
  see (821 total: 719 flagged, **102 with no flag**). Those 102 split by wording:
  755 standard / 64 timeout / 2 user-backgrounded across the whole 821.

**The fields are as reliable as the brief claims.** Nothing was found absent on a launch or
present on a non-launch. No stop-and-report condition was triggered.

Scripts used are in the session scratchpad (`sweep.py`, `sweep2.py`, `sweep3.py`,
`sweep4.py`); they are read-only over the transcript corpus.

## 2. What was deleted

All of the correlation machinery the brief named is gone. `cmd/claudemux-head/bgwork.go`
went from 233 to 136 lines; production code across both files is **85 added / 135 deleted**
(bgwork.go alone: 38 / 134).

Deleted from `bgwork.go`:

- `bgShellRe` (`running in background with ID: …`) — id regex
- `bgAgentRe` (`agentId: …`) — id regex
- `bgAgentLaunchMarker` (`"Async agent launched"`) — launch-sentence marker
- `bgAgentToolName` (`"Agent"`) and the whole tool-identity gate
- `bgLaunchKind`, `bgKindShell`, `bgKindAgent`, `bgLaunchKindOf` — launch-capable tool list
- `bgPending` and `bgTracker.pending` — the pending tool_use map, its persistence
  requirement and its `bgMaxAge` expiry sweep (the bounded-map concern is gone with it)
- `(*bgTracker).launches` — replaced by a free function `bgLaunches(Event) []string`

`observe` no longer inspects `e.ToolUses` at all. `outstanding` lost its pending sweep.
Untouched, as required: `bgCompletions`, `bgTaskIDRe`, `bgNotificationPrefix`, `bgMaxAge`,
the task map, completion handling, genuine-prompt clearing. `isWaiting`, `swAgeColW` and
`switchSession` were not touched.

Added: `Event.BgTaskID` / `Event.BgAgentID`, `raw.ToolUseResult json.RawMessage`, and
`extractLaunch` in `events.go`.

Verified no residue: `grep` for every deleted identifier and for `pending` /
`run_in_background` across `bgwork.go` and `events.go` returns nothing.

## 3. Tests and covering evidence

Every test builds its events as transcript **lines** run through `parseEvent`, never as
`Event` literals — the signal is a top-level sibling of `message`, so a hand-built Event
would test the test's idea of the payload rather than the parser's. That is also what let
the whole suite compile and run unchanged against the old code for the "before" capture.

| Test | Covers | Evidence |
|---|---|---|
| `TestBgTrackerRegistersRealTranscriptLaunches` | brief #1 — committed fixtures still register | `testdata/launch-{shell,agent}.jsonl`, unmodified. Both already carried the fields (`backgroundTaskId: boigiwsir`; `isAsync:true` + `agentId: a99a8221a00c2d373`), so no replacement was needed. Each is retired by its expected id, proving the id was read and not merely counted. |
| `TestBgTrackerRegistersRecoveredLaunches` | brief #2 — the ~100 recovered launches | Three new fixtures, one per real shape: `launch-shell-auto.jsonl` (auto-backgrounded, no flag, standard sentence), `launch-shell-timeout.jsonl` (`…did not complete within its 30s timeout…`, `timedOutAfterMs`), `launch-shell-user.jsonl` (`…manually backgrounded by user…`, `backgroundedByUser`). Corpus frequencies 102 / 64 / 2 of 821. |
| `TestBgRecoveredFixturesCarryNoBackgroundFlag` | keeps #2 honest | Asserts no `run_in_background` key in each fixture's tool_use input, so a future recapture cannot silently downgrade them to ordinary flagged launches while still passing. |
| `TestBgLaunchesInertUnderEveryCarrier` | brief #3 — every quoting shape × every carrier | 10 shapes (8 built at run time from this repo's own spec and plan, read from disk) × 4 carriers: `Read`, unrecorded tool_use, foreground `Bash` (with a realistic `toolUseResult` that has no `backgroundTaskId`), and **`Agent`** (foreground, `toolUseResult` a plain string as real ones are). 40 subtests. |
| `TestBgLaunchIDComesFromHarnessNotText` | the field, not the text, supplies the id | A background Bash whose result text quotes `quotedid` while the harness records `realtaskid`; only `realtaskid` retires it. Replaces `TestBgLaunchRegistersUnderRealLaunchToolUse`, which asserted text extraction. |
| `TestBgOrdinaryBashResultDoesNotRegister` | brief #4 | `toolUseResult` present and object-shaped, no `backgroundTaskId`. |
| `TestBgNonObjectToolUseResultIsHarmless` | brief #5 | 7 shapes: bare string, array of content blocks, number, `null`, wrong-typed `backgroundTaskId`, wrong-typed `isAsync`, empty-string id. Each asserts `parseEvent` still accepts the line, the timestamp and `tool_result` survive, and no launch registers. |
| `TestBgTrackerLaunchSpansPollBatches` | kept, reframed | Still valid as "a tool_use is not a launch; the result is" — it no longer guards a pending map. |
| completion/tracking tests (`TestBgCompletions`, pairs, counts+oldest, expiry, prompt clearing, notification-is-not-a-prompt, idempotence, unknown id, same id twice) | kept unchanged in substance | `bgShellLaunch` now emits real transcript lines carrying **both** the sentence and the harness field, so these exercise the same behaviour under both old and new code. |

Deleted tests, all of which tested deleted machinery: `TestBgTrackerPendingToolUseExpires`
(pending-map expiry), `TestBgLaunchesInertWhenToolCannotLaunch` (subsumed by the
every-carrier test), `TestBgForegroundBashDoesNotRegister` and
`TestBgForegroundAgentDoesNotRegister` (both now carriers in that test),
`TestBgLaunchRegistersUnderRealLaunchToolUse` (asserted text extraction under the gate).
Helpers `bgToolUseEvent`/`bgResultEvent` were replaced by line-building equivalents.

## 4. Evidence order — both runs

**Before** (new tests against unchanged `5350953` code) — `go test -run TestBg -v`:
62 subtests pass, and exactly the predicted set fails:

```
--- FAIL: TestBgTrackerRegistersRecoveredLaunches
    --- FAIL: .../launch-shell-auto.jsonl
    --- FAIL: .../launch-shell-timeout.jsonl
    --- FAIL: .../launch-shell-user.jsonl
--- FAIL: TestBgLaunchIDComesFromHarnessNotText
--- FAIL: TestBgLaunchesInertUnderEveryCarrier
    --- FAIL: agent/the design spec, read whole
    --- FAIL: agent/the plan doc, read whole
    --- FAIL: agent/a pretty-printed agent payload with real newlines
    --- FAIL: agent/a fenced code block quoting the agent example unescaped
    --- FAIL: agent/a terminal transcript containing the agent payload
    --- FAIL: agent/Go source with a raw string literal containing the agent payload
    --- FAIL: agent/a log line containing the agent payload
    --- FAIL: agent/a grep -n style prefixed line quoting the agent example
FAIL	github.com/mquinnv/claudemux/cmd/claudemux-head	0.231s
```

The `agent` carrier fails on the 8 shapes that quote the async payload and passes on the
2 shell-only shapes — that is the residual phantom launch the brief describes, reproduced.
Every other carrier (`read`, `unrecorded`, `foreground-bash`) already passed, as expected
from the tool-identity gate. Test #4 and #5 passed before, also as expected.

**After** — same command:

```
PASS: 76 subtests   FAIL: 0
ok  	github.com/mquinnv/claudemux/cmd/claudemux-head	0.232s
```

Full gates:

```
go build ./...   ok
go vet ./...     ok
gofmt -l .       (empty)
go test ./...    ok  github.com/mquinnv/claudemux/cmd/claudemux-head  3.557s
```

`TestEnvFileValueRetriesTransientlyEmptyFIFO` did not flake in these runs.

Raw captures: `before.txt` / `after.txt` in the session scratchpad.

## 5. Privacy — fixtures

`testdata/launch-{shell,agent}.jsonl` are unchanged claudemux-session lines.

The three new fixtures had to be **derived and redacted**. All three shapes
(auto-backgrounded, timeout, user-backgrounded) exist on this machine **only** inside the
user's employer's project transcripts — beejax, phenix, remix, cd-receiver, ag-admin. There
is no claudemux/mquinnv example of any of them, so committing verbatim was not an option.

Kept (the parts under test): field names, key order, value types, the harness's exact
wording, `backgroundTaskId` / `timedOutAfterMs` / `backgroundedByUser`, the absence of
`run_in_background`, `timeout` and `description` input keys, timestamps, `version`,
`attribution*` (public plugin/agent names only).

Replaced with neutral placeholders:

- **commands** — `op signin --account ameriglide.1password.com`, a `kubectl -n beejax scale
  deploy/ag` rollout, and a jar-hunting `find` became `sleep 60`, `sleep 600` and
  `find / -name '*.jar' …`; the `description` "Restore ag to 3 replicas" became "Run a long
  task"
- **`cwd`** → `/Users/michael/Projects/example`; **`gitBranch`** → `main` (was
  `chore/bjx-168-jetty-12-migration`, a worktree branch, `main`)
- **project slugs inside the task output path** → `-Users-michael-Projects-example`
- **every identifier** — `sessionId`, `session_id`, `uuid`, `parentUuid`,
  `sourceToolAssistantUUID`, `promptId`, `requestId`, `msg_*`, `toolu_*`, and the
  top-level sidechain `agentId` — replaced with deterministic synthetic values so
  cross-references inside a fixture still line up but nothing links back to a work session.

Only the opaque `backgroundTaskId` values (`bhhcrhd2d`, `bgk9vrtgb`, `b5wz612zv`) are real;
they are random harness-generated task ids carrying no content. A grep of `testdata/` for
`ameriglide|beejax|phenix|remix|jetty|kubectl|bjx|inetalliance|1password|lutra` returns
nothing.

The redaction is recorded in a comment on `TestBgTrackerRegistersRecoveredLaunches`, which
points here.

**Things I was unsure about, flagged rather than assumed:**

1. I redacted the opaque identifiers even though the already-committed fixtures keep
   theirs. Those are from a claudemux session; these would have been from work sessions,
   and a `sessionId` is a durable handle to a specific employer transcript. Redacting cost
   nothing, so I did it.
2. The three recovered fixtures are `isSidechain: true` (two of them) because that is where
   auto-backgrounding genuinely happens — inside subagents. `observe` does not filter on
   `IsSidechain`, and did not before this change either, so this is not a behaviour change.
   Noting it because it is visible in the fixtures and could read as accidental.
3. `launch-agent.jsonl` (pre-existing, untouched) contains an agent `prompt` in its
   `toolUseResult`. I re-read it; it is claudemux work and carries nothing sensitive.

## 6. Reviewer-flagged cleanup round (2026-08-11)

Four items, doc/test-only, on top of `1290b74`.

1. **`docs/…/2026-08-11-background-work-state-design.md`, Known limits.** "Wording drift"
   was stale — it said detection reverts if Claude Code rewords either launch *string*, but
   detection reads no string. Renamed to **Field drift**: names
   `toolUseResult.backgroundTaskId` and `toolUseResult.isAsync`/`agentId`, and says that if
   the harness stops writing them, every session reports `Idle` — the pre-branch bug — which
   is a graceful degradation, not a silent wrong answer.
2. **Same file, Parser changes.** Was missing the `toolUseResult` decode entirely — the
   point of the whole design — alongside `ToolResult.Content` and the `queue-operation`
   top-level `content`. Added it as item 3, naming `extractLaunch` (`events.go`) as the
   function that does the work.
3. **Reachable-vs-corpus-wide counts.** The head tails only main-session transcripts;
   `subagents/*.jsonl` is one glob level too deep for it to open. The recovered-fixture
   comment on `TestBgTrackerRegistersRecoveredLaunches` (`bgwork_test.go`) quoted corpus-wide
   figures (102/64/2 of 821) as if they were what the head sees, and double-counted inside
   them: 102 was the whole no-flag bucket, so timeout (64) and user (2) were counted again
   inside it. Corrected to reachable (main-transcript) figures, stated alongside the
   corpus-wide ones for context: auto alone is 3 of 522 reachable shell launches (36 of 821
   corpus-wide), timeout is 23 of 522, user is 2 of 522. The three reachable shapes total 28
   of 2079 main-session shell+agent launches (522 + 1557) — 1.35%. (`docs/…/design.md`
   itself already scoped its own 821/1596/1915 figures explicitly to "all 1915 transcripts
   on this machine" and does not misstate them as head input, so it was not changed for this
   item.)
4. **Untested guard.** `if res.IsAsync` in `extractLaunch` (`events.go`) survived mutation
   testing: no existing fixture has `isAsync:false` with a non-empty `agentId`, so the guard
   never ran. Added `{"an agent record that is not async", map[string]any{"isAsync": false,
   "agentId": "a1"}}` to `TestBgNonObjectToolUseResultIsHarmless`'s table (`bgwork_test.go`),
   asserting no launch registers.

   **Mutation evidence.** Removed the guard (`ev.BgAgentID = res.AgentID` unconditional, no
   `if res.IsAsync`) and reran `go test ./cmd/claudemux-head/ -run
   TestBgNonObjectToolUseResultIsHarmless -v`:

   ```
   --- FAIL: TestBgNonObjectToolUseResultIsHarmless (0.00s)
       --- FAIL: TestBgNonObjectToolUseResultIsHarmless/an_agent_record_that_is_not_async (0.00s)
           bgwork_test.go:518: outstanding = 1, want 0: an unreadable toolUseResult is not a launch
   ```

   All seven other subtests in the table still passed under the mutant — only the new case
   caught it. Restored `events.go` to the pre-mutation version (`diff` against the backup was
   empty) and reran; all tests pass.

**Gates:** `go build ./...` ok, `go vet ./...` ok, `gofmt -l .` empty,
`go test ./cmd/claudemux-head/` ok (`TestEnvFileValueRetriesTransientlyEmptyFIFO` did not
flake in this run). Diff touched exactly two files:
`cmd/claudemux-head/bgwork_test.go` and
`docs/superpowers/specs/2026-08-11-background-work-state-design.md`. No production logic
changed.
