# Background work state design

2026-08-11

## Purpose

A session with outstanding background work — an async agent, a `run_in_background` shell —
is currently reported as `Idle`. `isWaiting()` treats `Idle` as "paused waiting on input",
so the switchboard's conductor ferries the user into a session that is busy and is not
waiting on them. That is a correctness bug in the switchboard's core promise, not a
cosmetic one.

This adds a state for "the main thread finished its turn, but work it launched is still
running", and keeps those sessions out of the waiting queue.

## Why the existing signal cannot work

`Event.IsSidechain` is parsed (`events.go:229`) and used to keep the worktree chip and
command sampling on the main thread. It cannot detect subagents, because current Claude
Code writes subagent turns to **separate files**:

```
~/.claude/projects/<slug>/<session-id>.jsonl                     ← the head tails this
~/.claude/projects/<slug>/<session-id>/subagents/agent-*.jsonl   ← isSidechain:true lives here
```

Every `isSidechain:true` entry on this machine is in a `subagents/` file. The head never
opens those. `IsSidechain` stays for its existing uses; it is not the signal here.

## What is and is not already correct

| Case | Main transcript | State today |
|---|---|---|
| Foreground agent | parent's `tool_use` stays unresolved | `Tool:Agent` — correct, unchanged |
| Async agent | `tool_use` resolves at launch, turn ends with assistant text | `Idle` — **the bug** |
| `run_in_background` Bash | same | `Idle` — **the bug** |

Only the sessions whose main thread has genuinely ended its turn are affected. Everything
`classifyState` gets right today it must keep getting right.

## Event shapes

All four verified against real transcripts, the last of them produced live in the current
Claude Code version while writing this spec.

### Launch — background Bash

`tool_result.content` is a bare string:

```
Command running in background with ID: boigiwsir. Output is being written to: …
```

### Launch — async agent

`tool_result.content` is an **array of text blocks**, not a string:

```json
[{"type":"text","text":"Async agent launched successfully. (…)\nagentId: afbbf7a8f9ee52e81 (internal ID …)\nThe agent is working i…"}]
```

The two content shapes are why the parser must concatenate text blocks rather than
assume a string. Note the async agent carries **no** `run_in_background` input flag —
keying on the tool input alone would miss every agent.

### Completion — at the moment the task finishes

A `queue-operation` event whose **top-level** `content` (not `message.content`) holds the
notification:

```json
{"type":"queue-operation","operation":"enqueue",
 "content":"<task-notification>\n<task-id>boigiwsir</task-id>\n<tool-use-id>toolu_01VSdCK…</tool-use-id>\n<output-file>…</output-file>\n<status>completed</status>…"}
```

### Completion — when the session next runs

The same payload again, delivered as an ordinary `user` turn whose text starts with
`<task-notification>`.

Both forms are handled. Removal from a set is idempotent, so seeing an id twice costs
nothing, and handling both means the feature survives either form going away.

The launch id and the notification's `<task-id>` are the same string, which is what makes
the pairing exact.

## Detection rules

**Launch** — read from the harness's own record, never from the result's text and never
from the tool that produced it. The transcript entry carrying a `tool_result` also carries
a top-level **`toolUseResult`** — a sibling of `message`, read exactly like the
`queue-operation` `content` field:

| Launch kind | Signal | The id |
|---|---|---|
| background shell | `toolUseResult.backgroundTaskId` is a non-empty string | that string |
| async agent | `toolUseResult.isAsync == true` | `toolUseResult.agentId` |

Verified across all 1915 transcripts under `~/.claude/projects` on this machine:

- `backgroundTaskId` — string, present and non-empty on **821** entries, **all** of them
  genuine shell backgroundings, **all** produced by a `Bash` tool_use, zero false
  positives (including the foreground greps whose output quotes the launch sentence).
- `isAsync` — bool, present on **1596** entries and true on every one of them; each pairs
  with a non-empty string `agentId`. All 1596 came from an `Agent` tool_use, zero false
  positives. A **foreground** agent writes no `isAsync` at all — its `toolUseResult` is a
  plain string — so `isAsync` is what separates the two dispatches the same tool makes.
- Every notified `<task-id>` matches either a `backgroundTaskId` or an `agentId`, so the
  launch id and the completion id are the same string.

This is structural in a way no text rule can be: a command's stdout lands *inside*
`toolUseResult.stdout` and cannot add a key beside it, so no output — no matter what it
quotes — can forge a launch.

**`toolUseResult` is frequently not an object.** 4035 entries write a bare string there
and 2728 an array. Decode it as `json.RawMessage` and attempt the object decode, ignoring
failure: a non-object leaves the event unchanged and must never fail `parseEvent` or drop
the entry, which still carries the timestamp and `tool_result` the rest of the head reads.

No correlation state is needed. There is no pending-tool_use map, no launch-capable tool
list and no id regexes: the entry that announces the launch is the entry that carries the
id, so `bgLaunches` is a plain function of one event.

### Why not text detection

It was tried twice and cannot work. No pattern distinguishes *a launch happened* from
*text about a launch*: a session that reads or greps a document quoting a launch payload
produces a tool_result containing exactly those bytes. An unanchored search fell to a Grep
hit on `agentId: agentRecord.id` and to a Read of this repo's own docs. Anchoring to the
absolute start of the text then fell to `grep` on a single file, which emits no path
prefix, so the spec's own quoted example landed at byte 0. Each round narrowed the
patterns and each was defeated by a different quoting shape — the shapes are unbounded,
so the class cannot be closed from the text side. Do not reintroduce it.

Gating on the identity of the tool that produced the result closed that class for shells,
but not for agents: the same `Agent` tool dispatches foreground and async agents, so it
still needed an `Async agent launched` substring — and a foreground agent that merely read
this repo's own spec reported a phantom launch. Tool identity also **missed** roughly 100
real launches, because it keyed on `input.run_in_background`: of the 821 observed shell
launches, **102** carry no such flag. Claude Code backgrounds a Bash on its own, in three
wordings, and the harness field is written for all of them:

| Wording | Count |
|---|---|
| `Command running in background with ID: …` | 755 |
| `Command did not complete within its Ns timeout and was moved to the background (ID: …)` | 64 |
| `Command was manually backgrounded by user with ID: …` | 2 |

An earlier version of this spec claimed the auto-backgrounded Bash was structurally
indistinguishable from a foreground `grep` quoting the same sentence. **That was false**,
and it is the sentence that would send the next reader back down the text path: the two
differ by `toolUseResult.backgroundTaskId`, which the harness writes for one and not the
other regardless of how the backgrounding was triggered.

**The remaining limitation** is that this depends on Claude Code continuing to write these
fields. If it stops, no launch is ever detected and every session reports `Idle` at the
end of its turn — the pre-branch bug, and the safe direction to fail. A false negative
costs the feature; a false positive would publish `Background` for a genuinely idle
session and hide it from the conductor, undetectably, for up to the applicable expiry
window — `bgShellMaxAge` for a shell, `bgAgentStallAge` past the agent's last transcript
write (or `bgAgentMaxAge` in the worst case, a wedged agent whose transcript keeps
advancing) for an agent — which is strictly worse than not having the feature at all.

**Completion** — a `<task-id>` extracted from a notification, recognized **structurally**:
a `queue-operation` whose top-level content, or a `user` turn whose text, *starts with*
`<task-notification>`.

Matching the literal substring `task-notification` anywhere is wrong: it appears in
ordinary prose — skill documentation quotes it — and several transcripts on this machine
contain it with no background task involved. The prefix check is the whole guard.

## Tracking

Outstanding tasks live in model state as `id → {launch time, kind}`, updated incrementally
from each poll's **new** events. The kind is what lets expiry apply a different regime to
shells and agents (below).

Not recomputed from the event ring: `allEvents` is capped at 1000 (`tui.go:170`), and a
busy session scrolls a long-running task's launch out of the ring while it is still
running. Recomputing would silently drop it and revert to `Idle`.

### Expiry

Retirement is by completion notification; staleness is handled by liveness/caps, not by
watching for a typed prompt. A typed prompt has no effect on the tracker: an earlier
version of `observe` cleared the whole set on any `genuinePrompt` event, on the theory
that the human looking at the session made whatever it was tracking moot. That wipe was
removed 2026-08-13 — it made a session with several agents still running read `Idle` the
instant the human typed one keystroke, which sent the conductor right back into a busy
session. Completions retire tasks reliably (harness ids plus notifications), so the wipe's
safety role was already redundant with liveness/caps below.

Expiry is **per kind**, because a flat cap cannot fit both kinds at once.
Fleet measurement 2026-08-13, across real launches: 19 of 109 outstanding tasks ran past
30 minutes, and the longest-running agent ran 11 hours. A cap short enough to self-heal a
dead background shell in one sitting is far too short for a live agent, and a cap long
enough for a live agent hides a dead shell's session from the conductor for most of a
day.

1. **Background shells keep the flat 30-minute cap.** They leave no per-task file to
   check, so age since launch is the only signal there is. 30 minutes stays deliberately
   short: a backgrounded dev server runs — and stays silent — indefinitely, and a longer
   cap would hide its session from the conductor for the whole overrun.
2. **Async agents expire by transcript liveness, not age.** The harness writes each
   agent's own transcript at `<transcript dir>/<session id>/subagents/agent-<id>.jsonl`,
   and its mtime advances while the agent runs (the longest observed gap between writes is
   a single long tool call, bounded by Bash's 10-minute cap). An agent counts as alive
   while all of the following hold:
   - its transcript file's mtime is within `bgAgentStallAge` (15 minutes) of now — past
     that, the agent is gone however it died, notification or not;
   - if the file does not exist yet, the launch is within `bgAgentSpawnGrace` (2 minutes)
     of now — the ordinary gap between the launch record and the harness creating the
     file; anything older with still no file is a launch that predates a head restart, its
     agent long gone;
   - the launch is within `bgAgentMaxAge` (24 hours) of now regardless of liveness — a
     backstop against a wedged agent that keeps writing forever.

   With no `subagentsDir` configured at all — tests only; both construction paths below set
   one from the transcript path in production — an agent falls back to the shell's flat cap
   rather than counting forever. A layout the head doesn't recognize is a different failure:
   `subagentsDir` stays non-empty but points to a directory whose agent files never appear,
   so that case never reaches the flat-cap fallback — it expires via `bgAgentSpawnGrace`
   instead, the same stat-error branch that covers the ordinary gap before the file exists.

Without expiry, a task that never notifies (crashed, killed, head restarted mid-task)
would mark the session busy forever and the conductor would never visit it again —
turning a cosmetic bug into an unreachable session. Worst case now is `bgAgentStallAge`
(agents) or `bgShellMaxAge` (shells) of a wrong state that then self-heals.

### Session rotation

Both construction paths — `newModel` at startup and `switchSession` at rotation — seed a
fresh `EventReader` with `SeedFromEnd(500)` and replay that same end-anchored window through
`m.bg.observe(seeded, now)` before anything else runs. A head that starts, restarts, or
rotates while a task is already outstanding does see its launch, as long as the launch is
still inside that window.

The replay cannot resurrect finished work: completions always postdate their launches, so a
launch inside the seed window either carries its completion inside the same window —
netting to nothing, exactly as if the tracker had been running continuously — or the task
really is still outstanding. A launch older than the seed window is still missed; that
narrower gap is what expiry and liveness exist to tolerate, the same as any other launch the
tracker never saw. Re-reading the whole transcript to close it is still not worth it.

## State

A new `StateBackground` kind, which overrides **only** `StateIdle`:

- An unresolved foreground `tool_use` still wins. The main thread actively running a tool
  is the more specific truth, and that path is already correct.
- `Thinking` needs no change; it is already not-waiting.

`State.Since` is the **oldest** outstanding launch, so the duration reads as how long work
has been out rather than restarting with each new task.

### Display

```
head statusbar    ● Working 2        1:04
lobby row         ● remix     Working 2   1:04  ████░  74%  merge queue speedup
published option  Background:2
```

`isWaiting()` exact-matches `Idle` and `Tool:AskUserQuestion` and treats everything else
as not-waiting, so the conductor stops ferrying users into these sessions with **no change
to `switchboard.go` or `swconductor.go` at all**. The published value carries the count so
the lobby needs no second option.

## Parser changes

`events.go` needs three fields it does not currently expose:

1. `ToolResult.Content` is declared but tagged `json:"-"` and never populated
   (`events.go:58`). Populate it, concatenating text blocks when the content is an array
   and taking the string as-is when it is not.
2. The top-level `content` string carried by `queue-operation` events is not read at all.
   Expose it; it is where the completion signal arrives first.
3. The top-level `toolUseResult` sibling of `message` is not read at all — this is the whole
   point of the current design. `extractLaunch` decodes it into `Event.BgTaskID` /
   `Event.BgAgentID`, ignoring the (common) case where it is not a JSON object.

All three are additive. Nothing else reads these fields, so nothing else changes behavior.

## Testing

- **Launch extraction** — background Bash (string content), async agent (array content),
  a tool_result with neither marker, and a result whose text merely mentions background
  work in prose.
- **Completion extraction** — `queue-operation` form, `user` turn form, the same id from
  both (idempotent), and **skill prose containing `task-notification` mid-text, which must
  not register**.
- **Tracker** — launch then completion nets to empty; two launches and one completion
  leaves one; shell expiry at the flat 30-minute cap; agent expiry by liveness (spawn grace
  before the transcript file exists, stall age once it does, the 24-hour hard cap regardless
  of either); a genuine user prompt does not retire anything; a delivered
  `<task-notification>` user turn does not count as a genuine prompt.
- **classifyState** — idle with outstanding work → `Background`; idle with none → `Idle`
  (unchanged); unresolved tool_use with outstanding work → `Tool` (unchanged); `Since` is
  the oldest launch.
- **Publishing** — `statePublishValue` renders `Background:2`; `isWaiting` returns false
  for it.

All pure functions over event slices, tested with fixtures copied from the transcripts
quoted above.

## Known limits

- **Field drift.** If the harness stops writing `toolUseResult.backgroundTaskId` or
  `toolUseResult.isAsync`/`agentId`, detection sees no launch and every session reports
  `Idle` at the end of its turn — the pre-branch bug, and a graceful degradation rather
  than a silent wrong answer.
- **Seeding.** Work launched before the head started or rotated is recovered when the
  launch falls inside the 500-event seed window (see Session rotation, above); a launch
  older than that window is still invisible.
- **Foreground agents are unaffected**, and deliberately so — they already classify
  correctly through the unresolved `tool_use` path.
