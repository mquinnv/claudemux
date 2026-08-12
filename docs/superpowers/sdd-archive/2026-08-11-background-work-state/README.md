# Background-work state — execution archive

2026-08-11

The working record of the branch that added `StateBackground`. Kept because the
analysis in it is not reproducible from the diffs: several of these findings came
from replaying every Claude Code transcript on one machine, and the numbers are
the evidence for design choices the code itself can only assert.

| File | What it holds |
|---|---|
| `progress.md` | The ledger: every task, review, fix round, ruling, and the two times the merge blocker survived a fix. Read this first. |
| `final-fix-report.md` | The wave answering the first whole-branch review. |
| `structural-fix-brief.md` / `-report.md` | Round three: gating detection on tool identity instead of text. |
| `harness-signal-brief.md` / `-report.md` | Round four, the shipped design: reading the harness's own `toolUseResult` fields. |

Not archived: the `review-*.diff` packages (~360 KB, all reproducible with
`git diff`), and the five per-task briefs and reports, which are routine.

## The one thing worth carrying forward

Detection of "this session launched background work" was attempted three times by
matching text in a `tool_result`, and failed three times. Text in a tool result
cannot distinguish *a launch happened* from *text about a launch* — a session that
merely reads or greps a document quoting a launch payload looks identical to one
that launched something. Each round narrowed the patterns, passed a green test
suite, and was still broken:

1. unanchored patterns — a Read of this repo's own spec registered two launches;
2. anchored to line start — `grep` on a single file emits no path prefix, so the
   quoted line landed at byte 0 and still matched;
3. gated on tool identity — closed the read/grep class entirely (0 false positives
   over 324 transcripts), but the agent leg still needed a substring, so a
   foreground `Agent` result quoting the payload fired.

What works is not a better pattern. The harness already writes the answer as
structured data next to the message — `toolUseResult.backgroundTaskId`, and
`toolUseResult.isAsync` with `agentId`. A command's stdout lands in
`toolUseResult.stdout`; it cannot fabricate a sibling key. Measured over every
main-session transcript on the development machine: 2079 launches, precision
2079/2079, recall 2079/2079.

If a future reader is tempted to reintroduce a text rule here — for a launch
wording the fields do not cover, say — this is the history that argues against it.
The failure mode is not a missed launch (that degrades to the pre-branch bug and
is visible); it is a session silently marked busy, which hides it from the
switchboard's conductor for up to 30 minutes with nothing on screen to say why.
