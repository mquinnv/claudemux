# Detect launches from the harness's own fields, not from text

## Why

Detection has now been through three shapes. Text patterns alone were defeated by any
document quoting a payload. Gating on tool identity closed that (0 false positives across
324 real transcripts) but left two problems:

- the agent leg still requires an `Async agent launched` substring, so a foreground `Agent`
  result that quotes the payload — this repo's own spec or plan, say — still registers a
  phantom launch;
- ~90 genuine launches are missed, because Claude Code auto-backgrounds a `Bash` that
  overruns its timeout (no `run_in_background` flag), and because timeout- and
  user-backgrounded launches use different wordings that match no pattern.

A reviewer established, against every transcript on this machine, that the harness already
writes the answer as structured data on the tool_result entry — a sibling of `message`, not
inside it:

| Signal | Occurrences | Genuine launches | False positives |
|---|---|---|---|
| `toolUseResult.backgroundTaskId` | 821 | 821 | 0 (incl. 26 foreground greps quoting the sentence) |
| `toolUseResult.isAsync` + `agentId` | 1555 | 1555 | 0 |

A command's stdout lands in `toolUseResult.stdout`; it cannot inject a sibling key. This is
structural in a way no text rule can be.

## The change

Detect a launch from `toolUseResult`. When you are done, **both id regexes, the
`Async agent launched` marker constant, the tool-identity gate, and the pending
tool_use map should all be gone** — the correlation machinery exists only to compensate for
text detection, and it has no job once the harness's own field is the signal. Deleting it
also removes the bounded-map concern entirely.

Expected shape of the result:

- background shell → `toolUseResult.backgroundTaskId` is a non-empty string; that string is
  the task id, and it is the same id the completion notification carries as `<task-id>`.
- async agent → `toolUseResult.isAsync == true` and `toolUseResult.agentId` is a non-empty
  string; that string is the id.

**Verify all three field names, their casing, and their value types against real
transcripts before writing the parser** — do not trust this brief's spelling. Report what
you found and the files you checked.

**`toolUseResult` is not always an object.** Some tools write a plain string there. Parse it
as `json.RawMessage` and attempt the object decode, ignoring failure: a non-object must
leave the event unchanged, never fail `parseEvent` or drop the event.

## Where it goes

`events.go` — `parseEvent`'s `raw` struct gains `ToolUseResult json.RawMessage`. Populate
two new `Event` fields (name them as you see fit) holding the background task id and the
async agent id, empty when absent. This is a sibling of `message`, so it is read at the top
level exactly like the `queue-operation` `content` field already is.

`bgwork.go` — `bgLaunches` becomes a plain function of the event's new fields. `bgTracker`
loses the pending map and its expiry; `observe` keeps the task map, the completion handling,
and the genuine-prompt clearing, all unchanged. `bgCompletions` is untouched — notification
recognition has always been structural and has never been the problem.

## Tests

Keep every existing test that still applies; delete only what tests deleted machinery.

1. **The committed fixtures still register.** `cmd/claudemux-head/testdata/launch-{shell,agent}.jsonl`
   are real transcript lines. They must keep registering, now via the new signal. If a
   fixture lacks the `toolUseResult` fields, replace it with a real line that has them and
   say so in your report.
2. **Newly recovered launches now register** — these are the misses this round fixes, and
   each needs a fixture:
   - an auto-backgrounded `Bash`: no `run_in_background` in the input, launch sentence in
     the result, `backgroundTaskId` present
   - the timeout wording (`…did not complete within its Ns timeout and was moved to the
     background (ID: …)`)
   - the user-backgrounded wording (`…was manually backgrounded by user with ID: …`)
3. **Every quoting shape is inert under EVERY carrier, including `Agent`.** This closes the
   residual: build the shapes from the repo's own docs at run time as the current tests do,
   and assert inertness when carried by a `Read`, an unrecorded id, a foreground `Bash`,
   AND an `Agent` tool_use. The `Agent` carrier is the case that fires today — it must fail
   before your change and pass after.
4. **An ordinary Bash result does not register** — `toolUseResult` present, no
   `backgroundTaskId`.
5. **A malformed or string-valued `toolUseResult` is harmless** — no launch, no parse error,
   event otherwise intact.

## Privacy — read before adding fixtures

claudemux is a PUBLIC repository. Fixtures are real transcript lines and may carry command
text, file paths, and prompt content from the user's work at his employer. Prefer lines from
`claudemux`/`mquinnv` project transcripts. If the only real example of a shape lives in a
work-project transcript, do NOT commit it verbatim: keep the structural fields
(`toolUseResult`, tool name, input keys, ids) and replace command text, prompts, and absolute
paths with neutral placeholders, and note in a comment that the fixture is derived from a
real line with content redacted. Flag in your report anything you were unsure about.

## Evidence order

Write the tests first. Run them against the current code and capture the output: #3's
`Agent` carrier and all of #2 must FAIL, and the rest should pass. Then make the change and
re-run. Report both runs.

## Also update the spec

`docs/superpowers/specs/2026-08-11-background-work-state-design.md`, "Detection rules":

- Replace the rules with the `toolUseResult` signal and the field names you verified.
- **Delete the claim that the auto-backgrounded Bash case is structurally
  indistinguishable.** It is false — that sentence is what would send the next reader back
  down the text path.
- Keep a short history: text-only was defeated by quoted payloads; tool-identity gating
  fixed that but still needed a substring for agents and missed auto-backgrounded shells;
  the harness's own fields are the end state. Say plainly what the remaining limitation is
  — if Claude Code stops writing these fields, detection reverts to reporting Idle, which
  is the pre-branch bug and degrades gracefully.

## Constraints

- No new module dependencies.
- `go build ./... && go vet ./... && go test ./cmd/claudemux-head/` green; `gofmt -l .` empty.
- Out of scope, do not touch: `isWaiting` (switchboard.go), `swAgeColW`, `switchSession`'s
  seeding behavior.
- Comments explain WHY at the density of the surrounding code.
- `TestEnvFileValueRetriesTransientlyEmptyFIFO` is a known pre-existing flake (~20-25% of
  runs, 30s when it fails, untouched by this branch). Ignore it.
