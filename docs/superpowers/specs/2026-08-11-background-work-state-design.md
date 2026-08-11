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

**Launch** — a `tool_result` whose text matches either pattern below. Both are anchored,
not a bare substring search: an unanchored `agentId: ([A-Za-z0-9]+)` (and, briefly, an
unanchored `running in background with ID: ...`) registered a phantom launch for any tool
result that merely QUOTED a marker — a Grep hit on `agentId: agentRecord.id`, or a Read of
this repo's own docs, which quote both real payloads verbatim as worked examples.

| Pattern | Produces |
|---|---|
| `^Command running in background with ID: ([A-Za-z0-9]+)` (anchored to the ABSOLUTE start of the text, no multiline) | the background shell's task id |
| `(?m)^agentId: ([A-Za-z0-9]+)`, only when the same tool_result also contains the literal `Async agent launched` | the async agent's id |

The two anchors differ because the two payloads have different shapes. A background
shell's `tool_result.content` **is** the launch sentence — nothing precedes it — so
anchoring to the start of the whole string is exact and rejects any text where the
sentence merely appears somewhere inside a longer document (which is what a Read or Grep
of a file quoting it produces: the sentence is never the first byte of that tool_result's
content). An async agent's payload is a longer block where `agentId:` legitimately sits on
its own line after other text has already run, so it needs a per-line anchor
(`(?m)^`) instead of a whole-string one — but a per-line anchor by itself is not enough,
because a **quoted** copy of the payload (this repo's own design spec and plan doc quote
it verbatim, each on one physical markdown/source line) still starts a real line when the
file is read. The launch-sentence gate (requiring `Async agent launched` in the same
tool_result) is what tells those apart: in the raw JSON text a doc quotes, the payload's
`\n` between the launch sentence and `agentId:` is two literal characters on one physical
line, so `agentId:` never begins a true line there — but once a real tool_result has been
through `flattenText`'s `json.Unmarshal`, that escape decodes into an actual newline, and
`agentId:` genuinely starts a line only in that real, parsed data.

This trades a possible false negative — if Claude Code ever rewords either sentence,
detection silently reverts to pre-branch behavior for that kind — for immunity to a
session merely reading or grepping text that quotes one. That is the right side to err on:
a false negative degrades to the bug this feature fixes (already the pre-branch state,
recoverable by the wording drift being visible in a failing fixture), while a false
positive hides a session from the conductor for up to `bgMaxAge`, undetectably, which is
strictly worse than doing nothing.

**Completion** — a `<task-id>` extracted from a notification, recognized **structurally**:
a `queue-operation` whose top-level content, or a `user` turn whose text, *starts with*
`<task-notification>`.

Matching the literal substring `task-notification` anywhere is wrong: it appears in
ordinary prose — skill documentation quotes it — and several transcripts on this machine
contain it with no background task involved. The prefix check is the whole guard.

## Tracking

Outstanding tasks live in model state as `id → launch time`, updated incrementally from
each poll's **new** events.

Not recomputed from the event ring: `allEvents` is capped at 1000 (`tui.go:170`), and a
busy session scrolls a long-running task's launch out of the ring while it is still
running. Recomputing would silently drop it and revert to `Idle`.

### Expiry

Two rules, either of which clears an entry:

1. **A genuine user prompt clears the whole set.** If the human typed at that session,
   whatever it was tracking is moot. Reuses `genuinePrompt` (`tui.go:500`), which already
   filters meta notices and injected XML — including, usefully, the delivered
   `<task-notification>` turns themselves.
2. **A launch older than 30 minutes stops counting.** Catches the session nobody types at
   — exactly the ones the conductor exists to bring you to.

Without these, a task that never notifies (crashed, killed, head restarted mid-task)
would mark the session busy forever and the conductor would never visit it again —
turning a cosmetic bug into an unreachable session. Worst case with them is 30 minutes of
a wrong state that then self-heals.

### Session rotation

`switchSession` resets the tracker along with the rest of the derived state. A head that
starts, restarts, or rotates while a task is already outstanding never saw its launch and
will not count it. That is graceful — it degrades to today's behavior — and is not worth
solving by re-reading the whole transcript.

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

`events.go` needs two fields it does not currently expose:

1. `ToolResult.Content` is declared but tagged `json:"-"` and never populated
   (`events.go:58`). Populate it, concatenating text blocks when the content is an array
   and taking the string as-is when it is not.
2. The top-level `content` string carried by `queue-operation` events is not read at all.
   Expose it; it is where the completion signal arrives first.

Both are additive. Nothing currently reads these fields, so nothing else changes behavior.

## Testing

- **Launch extraction** — background Bash (string content), async agent (array content),
  a tool_result with neither marker, and a result whose text merely mentions background
  work in prose.
- **Completion extraction** — `queue-operation` form, `user` turn form, the same id from
  both (idempotent), and **skill prose containing `task-notification` mid-text, which must
  not register**.
- **Tracker** — launch then completion nets to empty; two launches and one completion
  leaves one; expiry at 30 minutes; a genuine user prompt clears everything; a delivered
  `<task-notification>` user turn does not count as a genuine prompt.
- **classifyState** — idle with outstanding work → `Background`; idle with none → `Idle`
  (unchanged); unresolved tool_use with outstanding work → `Tool` (unchanged); `Since` is
  the oldest launch.
- **Publishing** — `statePublishValue` renders `Background:2`; `isWaiting` returns false
  for it.

All pure functions over event slices, tested with fixtures copied from the transcripts
quoted above.

## Known limits

- **Wording drift.** If Claude Code rewords either launch string, detection silently
  reverts to today's behavior for that kind. The fixtures make it visible when the tests
  are run against a new version; nothing alerts at runtime.
- **Seeding.** Work launched before the head started or rotated is invisible (above).
- **Foreground agents are unaffected**, and deliberately so — they already classify
  correctly through the unresolved `tool_use` path.
