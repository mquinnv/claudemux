# plan-monitor redesign: ambient meta-info dashboard

**Date:** 2026-04-30
**Status:** Approved by user, ready for implementation planning

## Problem

The current plan-monitor TUI shows a project header, a fully-rendered markdown plan, a task list, and a footer. The plan markdown render dominates the panel and duplicates information already accessible by opening the plan file. What's missing is real-time meta-information about the Claude Code session running in the working directory: liveness, model, context fullness, rate-limit budgets, and recent activity.

User quote framing the goal:

> "the intent is to have a panel with meta info about the claude that is running in the working directory we are in"

## Use case

The panel runs in a top-right tmux pane. Claude Code runs in the left pane; a working terminal sits below the monitor. The panel is **ambient**, not interactive — the user glances at it but does not navigate or scroll it. This rules out tabbed UIs, drill-down inspectors, and scrollable content as primary interactions. The job of the panel is to answer **"what is Claude doing right now and how is it going?"** at a glance.

## Architecture: Glance Dashboard

Single-column, top-down layout. Every line earns its keep. No plan markdown rendering. Plan markdown is deliberately removed because the file is one keystroke away in the editor and the panel's job is current state, not reference material.

### Section order (top to bottom)

1. Header
2. Status block (state line + budget line)
3. Last prompt
4. Plan summary
5. Tasks
6. Activity feed
7. Footer

### Full-panel mockup

```
▸ plan-monitor · plan-monitor (main)
● Thinking 0:42 · opus · ctx 67%
5h 38% → 4:32p · wk 12% → Mon · empty in 3h12m

You: i'd like to always have the estimated time the tokens will…

Plan: Add real-time monitor sections
  ⟳ Wire up activity feed

Tasks ▰▰▰▰▱▱▱ 4/7
  ✓ Refactor JSONL reader to incremental
  ✓ Implement status block
  ✓ Add rate-limit detection
  ✓ Wire activity feed parser
  ⟳ Render activity feed
  ○ Add error counter
  ○ Polish footer

Activity
  ⟳  3s   Agent Explore "find session detection bug"
  ✓  8s   Bash $ go test ./...
  ✓ 12s   Read tui.go:1-275
  ✓ 24s   Edit plan.go +12 -3
  ✗ 38s   Bash $ npm test (exit 1)
  ⊘ 2m    Bash $ rm -rf /tmp/x  (denied)

sess a3f2c1b8 · upd 2s · 1 error
```

## Section specs

### Header

Single line. Format: `▸ plan-monitor · <project-basename> (<git-branch>)`.

- `<project-basename>` is the last path component of cwd (not full path — saves width).
- Branch in dim color. Turns yellow if there are uncommitted changes (anything in `git status --porcelain`).

### Status block

Two lines. The most-changed and most-watched part of the panel.

**Line 1 — current state:**
```
● Thinking 0:42 · opus · ctx 67%
```

- **Dot color encodes state:**
  - green ● `Idle` — last event is assistant text; no pending tool call
  - blue ● `Thinking` — assistant turn started, no tool call yet
  - yellow ● `Tool: <name>` — tool call in flight
  - red ● `Awaiting` — tool call needs user permission (heuristic: `tool_use` event with no matching `tool_result` for >N seconds while no streaming activity)
  - red ● `Error` — last turn ended in error / hook denial
  - magenta ● `Compacting` — compaction in progress
- **Time:** duration in current state (`0:42`, `2:13`, `12m`).
- **Model:** short name from JSONL — `opus`, `sonnet`, `haiku`. If a subagent (Task) is currently active, append agent type: `opus › Explore`.
- **ctx %:** input + cached + output tokens vs. model context budget. Yellow >70%, red >85%.

**Line 2 — budgets and projection:**
```
5h 38% → 4:32p · wk 12% → Mon · empty in 3h12m
```

- **Per window:** `<window> <%> → <reset-time>` for both 5-hour rolling and weekly caps.
- **ETA-to-empty:** `empty in <duration>` — the binding window's projected exhaustion at current burn rate. Burn rate is a rolling average over the last ~15 min of activity (single-tool-call rates whipsaw and are useless). When ETA exceeds both reset times, replace with `safe until reset`. When ETA < next reset, render in red.
- **Bootstrap:** when window has < 2 min of usage data, show `measuring…` instead of bogus number.
- **Degraded mode:** if rate-limit data is unavailable for the user's plan/auth, omit line 2 entirely. ctx % on line 1 always works.

**Plan/billing detection:**
- API billing — show `· $X.XX` appended after ETA on line 2.
- Max/Pro — windows are the meaningful budget; no `$` shown.
- Detection mechanism is an investigation item; reference implementations like `claude-monitor` and `ccusage` exist and should be studied during implementation.

### Last prompt

One or two lines. The most recent `user` event in the JSONL, truncated with ellipsis. Dim/italic style.

```
You: i'd like to always have the estimated time the tokens will…
```

If the most recent user message is a tool result or system event (not a real user prompt), walk back to the last actual user prompt.

### Plan summary

Compact replacement for the current full-markdown plan render.

```
Plan: Add real-time monitor sections
  ⟳ Wire up activity feed
```

- Line 1: plan title (extracted from first markdown heading, as today).
- Line 2: the single in-progress step from the plan, if recoverable. Determined by whatever signal is currently used by superpowers plan-execution skills (TBD during implementation — fall back to omitting line 2 if no signal exists).
- Section omitted entirely if no plan is discoverable for the session.

### Tasks

Mostly as today, with two changes:

```
Tasks ▰▰▰▰▱▱▱ 4/7
  ✓ Refactor JSONL reader to incremental
  ⟳ Render activity feed
  ○ Add error counter
```

- Header replaces `Tasks (4/7 complete)` with a unicode block progress bar plus count.
- Cap visible tasks at ~10. If the list exceeds the cap, render `…and N more` instead of allowing scrolling. Always include all in-progress tasks regardless of cap.

### Activity feed

New section. Last 5–8 tool calls, **newest at top**. Each line: `<status> <age> <tool> <arg>`.

```
Activity
  ⟳  3s   Agent Explore "find session detection bug"
  ✓  8s   Bash $ go test ./...
  ✓ 12s   Read tui.go:1-275
  ✓ 24s   Edit plan.go +12 -3
  ✗ 38s   Bash $ npm test (exit 1)
  ⊘ 2m    Bash $ rm -rf /tmp/x  (denied)
```

**Status glyphs:**
- `⟳` in flight (yellow)
- `✓` succeeded (dim green)
- `✗` failed / non-zero exit (red)
- `⊘` denied by hook or user (red)

**Per-tool argument formatting:**
- `Read foo.go:10-50` — file + line range when specified
- `Edit foo.go +N -M` — diff line counts when computable from tool args
- `Bash $ <cmd>` — command, ellipsis-truncated to fit
- `Grep "<pat>" <glob>` — pattern + glob filter
- `Glob <pattern>`
- `Agent <type> "<desc>"` — agent type + description
- `Web fetch <host>` — host only
- `Skill <name>`
- `mcp <server>/<tool>` — collapsed namespace (`mcp linear/get_issue`, not `mcp__linear__get_issue`)

**Rules:**
- Truncation only ever applies to the argument column. Status, age, and tool name never truncated.
- Age column ticks live every second so the feed feels alive even when nothing new happens. Format: `<N>s`, `<N>m`, capped at `5m+` for older entries.
- Denied/errored items are kept in the feed (often the most interesting line); not filtered out.
- Subagent calls show as `Agent <type> (running)` while in flight; v1 does not recurse into the subagent's own JSONL. Could be added later if monitor evolves into an inspector.

### Footer

Single line. Drop the `q to quit` hint (panel is ambient; no need to advertise keys, though `q` still works).

```
sess a3f2c1b8 · upd 2s · 1 error
```

- Short session id (first 8 chars).
- Seconds since last data poll.
- Error/denial count for this session, shown only when > 0, in red.

## Data sourcing

### Incremental JSONL tail

The current implementation re-reads the full session JSONL on every poll. With the activity feed added, this becomes infeasible for long sessions. Replace with an incremental reader:

- Maintain a file offset between polls, per session.
- On each poll, seek to last offset, read new bytes, parse new events, update state.
- Keep a ring buffer of the last ~20 tool-call events (so adaptive height can show more than the default 5–8 without re-reading).
- On panel start, scan from EOF backwards far enough to fill the ring buffer and compute current state.

### Rate-limit data

Three possible sources, in preferred order:

1. **API response headers** logged in JSONL — if Claude Code records `anthropic-ratelimit-*` headers, read them directly.
2. **Self-tracked usage** against a hardcoded plan-cap table — count tokens since window start.
3. **External tool data** — `claude-monitor` / `ccusage` likely write state somewhere; could read theirs.

Investigate during implementation; choose the simplest source that works. The fallback for any failure is the degraded-mode display (omit line 2, keep ctx %).

### Plan / subscription detection

Probably lives in `~/.claude/.credentials.json` or similar. Investigation item. If detection fails, default to "Max-style" display (no `$`).

## Error handling

- JSONL missing or unreadable: show panel with status `● Error · session unavailable`, omit feed/tasks/plan.
- Plan file missing: omit plan section entirely.
- Tasks dir missing: omit tasks section.
- Rate-limit data missing: degraded mode (line 2 omitted).
- Git not initialized: omit branch suffix in header.
- Any single-section failure must not blank the panel — each section renders independently from its own data source.

## Testing

- Unit tests for each formatter: status line, budget line, activity-feed line, tasks header.
- Unit tests for the incremental JSONL reader: append-only growth, file rotation, partial-line at offset boundary.
- Unit tests for burn-rate calculation: empty window, single sample, rolling-average correctness.
- Unit tests for state classifier: each of the six states produces correct dot+label from a JSONL fixture.
- Snapshot test for the full panel render at a fixed terminal size with a fixture session.

## Out of scope (deliberately)

- No tabbed views, scrolling lists, or interactive navigation.
- No drill-down into subagent JSONLs.
- No plan markdown rendering.
- No keyboard shortcuts beyond the existing `q`-to-quit.
- No alerts / notifications outside the panel.

## Resolved data sources

Findings from a Task 1 investigation of `~/.claude/`, a recent session JSONL, and the locally-installed reference tools (`claude-monitor`, `abtop`). `ccusage` is not installed and was not inspected directly.

### Subscription type / plan tier

Not exposed in any human-readable file under `~/.claude/`. `~/.claude/settings.json` keys are `effortLevel`, `enabledPlugins`, `extraKnownMarketplaces`, `hooks`, `permissions`, `skipAutoPermissionPrompt`, `skipDangerousModePermissionPrompt`, `statusLine`, `voiceEnabled` — nothing about plan tier. `~/.claude/.credentials.json` is owned by the user but contained no subscription / plan / tier field exposed to the keys probe (it stores OAuth state). The CLI clearly *knows* the plan (the statusline payload includes per-window cap percentages — see below — which only makes sense if the host knows the cap), but it does not expose the plan name to the filesystem in a documented place.

**Decision:** plan-monitor will treat the plan as "unknown / Max-style" by default. Allow override via env var (e.g. `PLAN_MONITOR_PLAN=max20|max5|pro`) for users who want to drive a hardcoded cap table. We do **not** attempt to detect the tier automatically.

### Rate-limit response headers in JSONL

**Not persisted.** A grep for `anthropic-ratelimit` against a recent session JSONL returns 12 hits, but every hit is inside user/assistant *text content* (the model and user discussing rate limits in conversation). No `ratelimit-*` headers appear as structured fields on any event. Confirmed by enumerating all top-level keys across all event types — no `responseHeaders`, no `ratelimit`, no `cap` field anywhere.

So we cannot read live cap percentages by tailing the JSONL alone.

### Per-turn token usage in JSONL

**Yes, rich data.** Every `assistant` event has `message.usage` with these fields:

```
input_tokens, output_tokens,
cache_creation_input_tokens, cache_read_input_tokens,
cache_creation: { ephemeral_1h_input_tokens, ephemeral_5m_input_tokens },
server_tool_use: { web_search_requests, web_fetch_requests },
service_tier ("standard"), speed ("standard"),
inference_geo, iterations[]  (per-iteration breakdown of the same fields)
```

This is enough to *self-track* token consumption per turn. It is not enough to know what fraction of the rate-limit budget that represents (no cap, no window-start anchor).

### The Claude Code statusline hook is the real cap source

`~/.claude/abtop-statusline.sh` is a Claude Code statusline hook that receives a JSON payload on stdin from the CLI. That payload contains a `rate_limits` object with this shape:

```json
{
  "rate_limits": {
    "five_hour": { "used_percentage": 8,  "resets_at": 1777572000 },
    "seven_day": { "used_percentage": 11, "resets_at": 1777928400 }
  }
}
```

The hook writes a cached extract to `~/.claude/abtop-rate-limits.json`:

```json
{"source":"claude","updated_at":1777557186,
 "five_hour":{"used_percentage":8,"resets_at":1777572000},
 "seven_day":{"used_percentage":11,"resets_at":1777928400}}
```

This is a known, observed file on disk. It is updated whenever Claude Code refreshes the statusline (typically each turn). `abtop` reads it; we can too.

### Decision on cap detection

**Read-from-file (not self-track).** Plan-monitor will:

1. **Primary source:** poll `~/.claude/abtop-rate-limits.json` for cap percentages and reset timestamps. Display directly. The path is configurable; if abtop isn't installed, the file simply won't exist and we degrade.
2. **Optional:** install our own statusline hook that writes the same file shape under a plan-monitor-owned path (e.g. `~/.claude/plan-monitor-rate-limits.json`) so users without abtop still get cap data. Out of scope for Task 4; revisit later.
3. **Fallback (degraded):** if no rate-limit file is present and no env-var plan override is set, **omit the cap display entirely** rather than show wrong numbers. The status line still shows context% and per-turn tokens — both derivable from JSONL alone.
4. **Burn-rate / ETA:** computed from JSONL `message.usage` deltas over the last ~15 min, *projected against the cap percentage from the file*. ETA = (100 − used_percentage) / burn_rate_pct_per_min. Show the warning line only when projected exhaustion < `resets_at`.

We do **not** maintain a hardcoded `{plan: cap_tokens}` table for Task 4. It is brittle and the file already gives us the percentage directly.

### "Awaiting permission" heuristic

**Default: 15 s without a `tool_result` after the most recent `tool_use`.** When the assistant emits a `tool_use` event, the runtime either auto-approves (tool_result arrives within ~1 s) or prompts the user. A stuck `tool_use` with no matching `tool_result` for 15 s is a strong proxy for "awaiting permission". The classifier (Task 3) will own the threshold; tune in Task 12 if it false-fires.

We did **not** find a dedicated permission-prompt event type in the JSONL schema — `permissionMode` exists as a session-scoped field but does not signal individual prompts. The 15 s heuristic is the best proxy available.

### What we learned from `claude-monitor` and `abtop`

- **`claude-monitor`** (Python, installed via uv) does *not* read rate-limit headers. `claude_monitor/core/plans.py` defines a hardcoded plan table (`PRO: 19k tokens, MAX5: 88k, MAX20: 220k`, plus message and cost limits) and `data/reader.py` walks `~/.claude/projects/**/*.jsonl`, extracts `message.usage`, and counts tokens against the chosen plan's `token_limit` over a rolling window. It is the "self-track against hardcoded caps" approach we explicitly rejected — works without privileged data, but the user must pick the right plan and the caps drift over time. Useful as confirmation that JSONL `message.usage` is the right token source.
- **`abtop`** (Rust, Mach-O binary at `~/.cargo/bin/abtop`) reads the cached `abtop-rate-limits.json` file written by the statusline hook described above. The strings in the binary confirm: it deserializes a `RateLimitFile { source, five_hour: WindowInfo, seven_day: WindowInfo, updated_at }` struct with `WindowInfo { used_percentage, resets_at }`. This is the approach plan-monitor will mirror.
- **`ccusage`** is not installed locally and was not inspected. From its public reputation it is closer to the claude-monitor pattern (token accounting from JSONL); we did not need it to make a decision.

The crucial insight is that the statusline hook payload is the only place Claude Code surfaces actual cap data, and it does so on a per-turn cadence with no API calls required. That makes it the right primary source.
