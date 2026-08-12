# Structural fix: gate background-launch detection on tool identity

## Why this exists

`bgLaunches` currently decides that a background task was launched by matching text
patterns in a `tool_result`'s content. That cannot work, and two rounds of tightening the
patterns have not made it work: text in a tool_result cannot distinguish *a launch
happened* from *text about a launch*. A reviewer defeated the current anchored version
with nine different quoting shapes, one of which fires on this repository today:

```
$ grep "Command running in background with ID" docs/superpowers/specs/2026-08-11-background-work-state-design.md
```

`grep` with a single file argument emits no path prefix, so the spec's quoted example
lands at byte 0 and the absolute anchor matches. A false positive publishes
`Background:1`, makes `isWaiting` false, and hides a genuinely idle session from the
switchboard's conductor for up to 30 minutes — strictly worse than not having the feature.

## The fix

Decide *whether* a launch happened from the **tool that produced the result**, not from
the result's text. Keep the text patterns only to extract the **id**.

A `tool_result` carries `ToolUseID`. The `tool_use` that produced it carries `Name` and
`Input`. No amount of text inside a tool_result can fabricate a matching `tool_use` block,
so this closes the entire class of bug rather than the instances found so far.

| Launch kind | Gate (structural) | Then extract the id from the result text |
|---|---|---|
| background shell | the tool_use is `Bash` with `Input["run_in_background"] == true` | `running in background with ID: ([A-Za-z0-9]+)` |
| async agent | the tool_use is the agent-dispatch tool AND the result text contains `Async agent launched` | `agentId: ([A-Za-z0-9]+)` |

The agent case still needs its text check, for a real reason: the **same tool** is used for
foreground agents, whose result is the agent's final report rather than a launch
acknowledgement. Tool identity says "this is an agent dispatch"; the sentence says "and it
was an async one". That is a genuine semantic distinction, not a quoting workaround.

**Verify the agent tool's actual `name` against real transcripts before writing the
gate** — do not guess. Grep `~/.claude/projects/**/*.jsonl` for tool_use blocks whose
result contains `Async agent launched` and read the `name` field. Handle every name you
actually find. Note it in your report.

**Relax the anchors.** With tool identity doing the gating, the absolute/line anchors added
in the previous two rounds are no longer load-bearing, and they carry their own
false-negative risk (a sweep found 42 of 844 real shell results do not begin at byte 0).
Go back to simple unanchored patterns for id extraction. The gate is the guard now.

## Where the correlation lives

`bgTracker` gains a second map: pending tool_uses of interest, `toolUseID → kind`, where
kind is background-shell or agent-dispatch. In `observe`:

1. For each event, walk `e.ToolUses`. Record ONLY the ones that could be a launch (Bash
   with `run_in_background` true; the agent tool). Recording only interesting tool_uses is
   what keeps the map small — do not record every tool_use in the session.
2. For each `e.ToolResults`, look up `ToolResultID` in that map. If absent, the result did
   not come from a launch-capable tool: ignore it entirely, whatever its text says. If
   present, apply that kind's text rule to extract the id, then delete the map entry — a
   tool_use produces exactly one result.

The map must persist across `observe` calls: the tool_use arrives on an assistant event
and its result often arrives in a later poll's batch.

**Bound the map.** Stamp each pending entry with the event's timestamp and expire entries
older than `bgMaxAge` in the same sweep that expires outstanding tasks, so a session killed
mid-tool cannot grow it without limit. A tool_use whose result never arrives must not leak.

`Input` is `map[string]interface{}`, so `run_in_background` arrives as a JSON bool — type
assert it; a missing or non-bool value means not a background shell.

## Known limits to preserve (do not try to fix)

- A launch whose `tool_use` was never seen — head started or rotated mid-tool — is
  unrecognized, so its result is ignored and the session reads Idle. This is the spec's
  documented seeding limitation and is graceful.
- `isWaiting` (switchboard.go), `swAgeColW`, and `switchSession`'s seeding behavior are out
  of scope and must not change.

## Tests

Replace the quoting-shape tests rather than adding to them: with tool-identity gating, a
quoted payload is not a near-miss, it is simply a result from a tool that cannot launch
anything. The tests should say that.

1. **Real payloads register.** Build them the way production does: take real JSONL lines
   from `~/.claude/projects/**` for both kinds, run them through `parseEvent`, and drive
   `observe`. The tool_use event and the tool_result event are separate lines — feed both,
   in order, as a real poll would. Assert the launch is tracked.
2. **The tool_use arrives in an earlier batch than its result.** Two `observe` calls. This
   is the case a single-batch test would miss.
3. **Every quoting shape is inert when the tool cannot launch.** Take the nine shapes
   below and feed each as the content of a result whose tool_use is `Read`, and again as a
   result whose `ToolUseID` matches no recorded tool_use. Assert no launches, for all of
   them:
   - the actual bytes of `docs/superpowers/specs/2026-08-11-background-work-state-design.md`
   - the actual bytes of `docs/superpowers/plans/2026-08-11-background-work-state.md`
   - a single bare line quoting the spec's shell example (no path prefix — this is what
     `grep` on one file emits, and it is the shape that fires today)
   - a pretty-printed agent payload with REAL newlines:
     `"Async agent launched successfully.\nagentId: abc123def\n"`
   - a fenced code block quoting the spec's agent example unescaped
   - a terminal transcript containing the agent payload
   - Go source with a multi-line raw string literal containing it
   - a log line containing it
   - a `grep -n` style prefixed line
4. **The same text DOES register under a real launch tool_use.** Pair one of the above
   texts with a genuine `Bash` + `run_in_background` tool_use and assert it is tracked.
   This is what proves the gate is doing the work rather than the text being rejected.
5. **A foreground agent does not register.** The agent tool with a result that does not
   contain `Async agent launched`.
6. **The pending map is bounded.** A recorded tool_use whose result never arrives expires.

Fix the vacuity guard the previous round left behind: `bgwork_test.go` guards its
doc fixtures with bare `strings.Contains` checks for `Async agent launched` and
`running in background with ID:`, but that prose now also appears in the Detection-rules
section, so reflowing the two example lines would leave the guard passing and the test
vacuous. Guard on the SHAPES that matter instead — a line whose text begins with the
shell sentence, and a line containing both `Async agent launched` and `agentId:`.

## Evidence order

Write the tests first, run them against the current code, and capture what fails and what
passes. Some will already pass (the doc-read cases pass today for the wrong reason); the
ones that must fail first are #3's bare-line and pretty-printed shapes and #4. Then make
the change. Report both runs.

## Also update the spec

`docs/superpowers/specs/2026-08-11-background-work-state-design.md`'s "Detection rules"
section currently documents the anchoring approach and argues at length about `\n`
escaping. Replace that with the structural rule. Keep a short paragraph recording that
text-only detection was tried twice and why it cannot work — the next reader will
otherwise propose it again.

## Constraints

- No new module dependencies.
- `go build ./... && go vet ./... && go test ./cmd/claudemux-head/` green; `gofmt -l .` empty.
- Comments explain WHY at the density of the surrounding code.
- Do not alter existing test assertions except the quoting-shape tests this brief replaces.
- `TestEnvFileValueRetriesTransientlyEmptyFIFO` is a known pre-existing flake (~20-25% of
  runs, 30s when it fails, untouched by this branch). Ignore it; do not "fix" it.
