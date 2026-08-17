# testdata

The `.jsonl` fixtures here are DERIVED from real Claude Code transcript
lines, not synthetic. Real transcripts are the only source that reliably
reproduces the harness's exact wording, field names, key order and nesting —
including shapes (like an un-flagged background shell) that are easy to get
subtly wrong by hand. But this is a public repository, so every fixture is
redacted before it's committed:

- **Untouched:** harness structure, field names, key order, tool names,
  `toolUseResult` shape, usage/telemetry numbers, model names, timestamps.
- **Replaced with neutral placeholders:** commands, `cwd`, git branch,
  project/worktree slugs, and every session/message/request identifier
  (`uuid`, `sessionId`, `session_id`, `promptId`, `requestId`, message and
  `tool_use` ids, and any of the same values embedded in prose, e.g. an
  output-file path). The placeholder cwd is always
  `/Users/michael/Projects/example`; placeholder ids reuse the real ids'
  shape (`00000000-0000-4000-8000-NNNNNNNNNNNN` for UUIDs, `msg_`/`toolu_`/
  `req_` + a zero-padded counter). The same real value always maps to the
  same placeholder within a fixture (and across the two lines of a
  tool_use/tool_result pair), so relationships between fields survive
  redaction even though the values don't.
- **Exempt from replacement — kept exactly as captured:** `backgroundTaskId`,
  `agentId` and `resumedAgentId` values (and the `pin` object echoing the
  same id). They're opaque, non-identifying strings, and the tests assert
  against them by exact value (see below), so changing one means updating the
  test in the same commit.

## What the tests depend on

`bgwork_test.go` parses each fixture line with the production `parseEvent`
and asserts on the result — never a fixture read as raw bytes. A fixture
must stay a single tool_use event followed by its tool_result event
(`bgFixture` hard-asserts exactly 2 events, one `tool_use` and one
`tool_result`), and the `toolUseResult.backgroundTaskId` /
`toolUseResult.agentId` / `toolUseResult.resumedAgentId` the launch registers
under must keep matching the `wantID` hardcoded in
`TestBgTrackerRegistersRealTranscriptLaunches` and
`TestBgTrackerRegistersRecoveredLaunches`. Everything else in a fixture
(prompt text, tool input, telemetry) is free to redact or shorten, since
nothing else is asserted on.

## Adding a new fixture

Follow the same rule: capture the real line(s), then redact using the
scheme above before committing. Do not commit an unredacted transcript line,
and do not let a new fixture be held to a lighter standard than the ones
already here — this file exists so the standard is discoverable instead of
living only in a session report.
