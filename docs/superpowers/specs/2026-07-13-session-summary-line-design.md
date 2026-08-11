# Session summary lines

## Problem

The pane's prompt rows show the session's first and last user prompts. The first
prompt is meant to say what the session is about, but it often says nothing at
all: a session that opens with `/clear` shows `/clear` forever.

Two distinct faults produce that:

1. **A bug.** `firstUserPrompt` (tui.go) already skips slash commands and falls
   through to the next genuine prompt. But `EventReader.firstPrompt` is captured
   once, at seed time, and never revisited. A session seeded while `/clear` was
   its only prompt takes `/clear` as the fallback and freezes it — the real
   prompt that arrives a moment later never gets a chance to replace it.

2. **A design limit.** Even with the bug fixed, a raw prompt is not a summary. The
   first prompt describes what the user asked for an hour ago; the last describes
   what they just typed. Neither says what the session is *about* or what it is
   *doing*.

## Design

Replace the `first` / `last` prompt rows with two summary rows produced by Haiku:

```
 topic  making the summary line say what the session is about
 now    wiring the haiku call behind an in-flight guard
```

- `topic` — what this session is for. Stable; changes only when the session's
  purpose genuinely moves on.
- `now` — what it is currently doing. Moves at each turn boundary.

### Rendering

`topic` and `now` take the slots `first` and `last` occupy today (tui.go `View`):

| pane height | rows |
|---|---|
| ≤ 1 | packed statusbar (unchanged) |
| 2 | state + meters (unchanged) |
| 3 | state + meters + `now` |
| ≥ 4 | state + meters + `topic` + `now` |

At height 3 the single prompt row becomes `now`, taking the slot `last` holds
today — the live line is the one worth having when only one fits.

`renderPromptLine` is reused unchanged. Both labels are five columns wide, like
`first` / `last `, and the existing clip-to-width behavior handles overflow.

### Summarizer

New `summary.go`. One call produces both lines.

- **Client:** the official `github.com/anthropics/anthropic-sdk-go`, not hand-rolled
  `net/http`. It is one dependency, and it supplies typed model constants and tool
  params instead of hand-written JSON.
- **Model:** `claude-haiku-4-5`.
- **Auth:** `ANTHROPIC_API_KEY`, read from the environment and passed to the SDK
  explicitly. Deliberately *not* the local Claude Code credentials — claude-head
  displays the subscription rate limit, so it must not consume it, and a client
  constructed with no key falls back to the local OAuth profile and would.
- **Output contract:** a forced tool call returning `{"topic": "...", "now": "..."}`.
  No prose parsing.
- **Input:** a condensed transcript — the session's genuine first prompt (which
  anchors `topic`), then the last ~30 events flattened to user text, assistant
  text, and tool names.
- **Topic stability:** the previous `topic` is passed back into each call with the
  instruction to keep it unless the session has clearly moved on. Without this
  anchor the topic re-derives from scratch every turn and jitters.
- **Cost:** a few thousand input tokens and ~40 output per call; tens of calls per
  day per pane.

### Trigger

Fire on the `busy → idle` edge detected in `recomputeFromEvents` — a turn just
completed, so there is real news — plus once at seed, so a freshly attached pane
is not blank.

Two guards:

- an in-flight bool, mirroring the existing `polling` flag, so overlapping calls
  cannot race;
- a minimum interval (~20s), so a burst of one-line turns cannot hammer the API.

The call runs as a `tea.Cmd` returning a `summaryMsg`. The TUI never blocks on
the network. This is claude-head's first outbound HTTP dependency.

### Fallback

The pane renders today's `first` / `last` prompt rows whenever a summary is
unavailable: `ANTHROPIC_API_KEY` unset, the call errored, or nothing has returned
yet. Nothing regresses on a machine without a key.

That fallback path has to actually work, so it carries the bug fix. In
`events.go`, `firstPrompt` keeps upgrading as events arrive while it still holds
a slash command or is empty, and freezes the moment a genuine non-command prompt
lands.

Summaries live in memory only. A pane restart re-derives on the next turn
boundary rather than reading a cache from disk.

### Testing

The SDK client is built with `option.WithHTTPClient`, so a fake transport stands in
for the API and the whole feature tests without touching the network:

- transcript condensing, the JSON contract, and topic anchoring;
- error and missing-key paths fall back to the raw prompt rows;
- the in-flight and minimum-interval guards, against a fake clock.

The `firstPrompt` upgrade rule gets a table test in `events_test.go`: `/clear`
then a real prompt → the real prompt wins; `/clear` alone → `/clear`; a real
prompt then `/clear` → the first one holds.
