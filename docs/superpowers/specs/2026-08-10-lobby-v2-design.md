# Switchboard lobby v2 design

2026-08-10

## Purpose

Enrich the full-screen lobby: per session, alongside the existing name /
state chip / time-in-state / queue marker, show the context meter, the Haiku
tab topic, the head's one-line summary, and the last typed prompt
(user-approved column set).

## Data channels

Same architecture as v1 — heads publish, the lobby polls; the lobby never
re-tails transcripts.

New tmux user options published by `claudemux-head` (session-scoped, same
fire-and-forget pattern as `@claudemux_state`):

- `@claudemux_context` — context-window usage as an integer percent
  (`m.contextPct`); published when the integer changes.
- `@claudemux_summary` — the Haiku one-line summary; published on change.
- `@claudemux_prompt` — the last typed prompt (`m.lastTyped`); published on
  change.

Values are sanitized before publishing: tabs and newlines collapsed to
single spaces (the lobby's parser is line- and tab-delimited), truncated to
120 runes at a word boundary (reusing `truncateWords`). Empty values publish
as empty strings (the option is still set, keeping parse columns aligned).

The Haiku tab topic needs **no publishing**: the head already renames its
tmux window to the topic, so the lobby reads `#{window_name}` from the pane
listing it already makes (the head pane's row).

## Poll changes

- `list-sessions` format gains `#{@claudemux_context}`,
  `#{@claudemux_summary}`, `#{@claudemux_prompt}` fields (tab-separated, as
  today; parser field-count updated).
- `list-panes` format gains `#{window_name}`; the topic is captured from the
  same row that identifies the head pane.
- `swSession` gains `Context int` (-1 when unset/unparseable), `Topic`,
  `Summary`, `Prompt string`.

## Lobby layout

Two lines per session:

1. queue marker · name · state chip · time-in-state · context meter
   (`renderBar`-style gauge + `NN%`, omitted when unset) · topic (dim)
2. (dim, indented) summary — falling back to the last prompt when there is
   no summary; when both exist, summary first, ` · `, then prompt, truncated
   to the pane width.

Selected row keeps the pad-before-style reverse-video treatment on line 1.
The status and help lines are unchanged. Narrow panes truncate line 2 before
line 1 (clip, no wrapping).

## Testing

Existing style: publish-args/sanitize builders; parser tests for the new
fields (unset options → zero values, `Context: -1`); topic capture from the
pane listing; View tests asserting the new columns and the summary/prompt
fallback.

## Out of scope

- Publishing rate-limit gauges or model names.
- Scrolling for fleets taller than the pane (v1 constraint stands).
- Multi-window sessions (claudemux sessions are single-window).
