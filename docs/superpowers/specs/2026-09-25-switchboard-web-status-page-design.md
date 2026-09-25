# Switchboard web status page — design

Date: 2026-09-25
Status: approved in chat, pending spec review

## Problem

The switchboard (`claudemux switch`) is the one place that sees the whole
fleet: every session's state, topic, running summary, last prompt, model,
context use and defer status. It is a tmux TUI on Michael's Mac, so nobody
else can see it. Teammates on the tailnet asking "what is Michael working on"
have no answer short of a Slack message.

## Goals

- Teammates on the tailnet open one URL and see the fleet the way the lobby
  shows it, updating live, with no login step.
- The page leads with a single LLM-written headline saying what the fleet is
  doing overall, so a visitor gets the gist without reading every row.
- Nothing on the page can act on a session. It is a window, not a console.
- Reachability is the tailnet's job. claudemux binds to the node's Tailscale
  address and does no auth of its own.
- Off unless configured. Michael turns it on once in his own `config.yml`.

## Non-goals

- Interaction of any kind: no defer, no jump, no notes, no typing.
- Public or LAN exposure. `web.listen` can name any address, but the default
  path binds the Tailscale interface only, and the README says so.
- HTTPS. Headscale (this tailnet's control server) does not issue certs, so
  `tailscale serve` is not relied on. Plain HTTP over WireGuard is the
  transport.
- A standalone daemon. The page is up exactly when the lobby is; a lobby that
  is closed serves nothing. That matches how the lobby is already used
  (restore-after-reboot is driven from it).
- Any build step, bundler or external asset. One embedded HTML file with a
  few lines of inline script.

## Design

### 1. Configuration

A new `web` section in `config.yml`:

```yaml
web:
  listen: tailscale:7474      # "" (default) = off
  headline_interval: 2m       # floor between headline calls; 0 = no floor
```

- `listen` accepts a normal `host:port`, or the keyword host `tailscale`
  meaning "this node's Tailscale IPv4, resolved with `tailscale ip -4` at
  lobby start". `""` disables the server. Validation rejects a value with no
  port or a non-numeric port at load time, by key name, like every other bad
  key.
- `headline_interval` follows `summary.min_interval`'s rules: a Duration,
  negative rejected, zero a legal opt-out of the floor.
- The lobby starts calling `loadConfig` (today only the session head does).
  A config that exists but fails to parse is fatal for the lobby too, for the
  reason it is fatal for the head: running on defaults silently discards what
  the user configured.

Nothing about the head changes. The head keeps publishing what it publishes.

### 2. Lifecycle

In `runSwitchboard`, after config loads and before `tea.NewProgram`:

1. If `web.listen` is empty, do nothing.
2. Resolve the address. For the `tailscale` host, run `tailscale ip -4` with
   a short timeout and take the first line. A missing binary or empty output
   is a resolve failure.
3. `net.Listen("tcp", addr)`, then serve in a goroutine with `http.Server`.
4. Bind or resolve failure is NOT fatal. The lobby runs without the server and
   the reason lands on the status line (`lastErr`), where create and defer
   failures already surface. It is not retried; the user fixes the config or
   the port and restarts the lobby.
5. On the way out, both the quit path and the re-exec restart path close the
   listener (`Shutdown` with a one-second budget, then `Close`) before
   `restartSelf` or return. Go sets `SO_REUSEADDR` on TCP listeners and
   `CLOEXEC` on their fds, so the replacement lobby rebinds the same port
   without inheriting the old socket.

The title row of the lobby shows the bound address when the server is up,
so the URL can be read off the screen.

### 3. Data flow

The HTTP handlers never touch tmux, bubbletea or the model. A small holder
type owns the data:

```go
type webFleet struct {
    mu       sync.RWMutex
    snap     swSnapshot
    rl       RateLimits
    rlOK     bool
    windows  []ModelWindow
    taken    time.Time
    headline webHeadline
}
```

- On every `swSnapshotMsg` the lobby's `Update` copies the new snapshot, the
  rate-limit state and the per-model windows into the holder under the write
  lock. This is the only writer for those fields.
- The headline worker (section 5) is the only writer for `headline`.
- Handlers read under the read lock and copy out. A request can never block
  the TUI, and the TUI can never observe a half-written response.

The holder is a field on `swModel` (a pointer, so `Update`'s value receiver
shares it) and nil when the server is off, in which case `Update` skips the
publish.

### 4. HTTP surface

Two routes, `GET` only (anything else is 405). No other paths (404).

`GET /api/fleet` returns JSON:

```json
{
  "taken_at": "2026-09-25T14:03:12-04:00",
  "headline": {
    "text": "Shipping the switchboard web page while two phenix sessions wait on review.",
    "at": "2026-09-25T14:01:40-04:00",
    "stale": false
  },
  "counts": { "sessions": 6, "waiting": 2, "deferred": 1 },
  "budget": {
    "five_hour": { "used_pct": 41, "resets_at": "2026-09-25T17:00:00-04:00" },
    "weekly":    { "used_pct": 63, "resets_at": "2026-09-29T09:00:00-04:00" }
  },
  "sessions": [
    {
      "name": "claudemux",
      "color": "#8b5cf6",
      "emoji": "🔀",
      "state": "Working",
      "state_raw": "Tool:Bash",
      "waiting": false,
      "since": "2026-09-25T14:02:50-04:00",
      "context_pct": 42,
      "model": "claude-opus-4-7",
      "topic": "web interface for sharing session status",
      "summary": "Writing the design spec for the web status page.",
      "prompt": "A",
      "deferred": false,
      "defer_reason": ""
    }
  ]
}
```

- `state` is `swStateText`'s display word; `state_raw` is the published
  option value. `waiting` is `isWaiting(state_raw)`.
- `sessions` is in lobby order: `swSortSessions` has already parked deferred
  rows at the end, and the page draws the same `deferred` rule the lobby
  does.
- `budget` is omitted when `rlOK` is false; `headline` is omitted until the
  first successful call. Absent facts are absent, not blank, like the banner.
- `context_pct` is omitted when the session has not published one (`-1`).
- `Cache-Control: no-store` on both routes.

`GET /` is one HTML page embedded with `embed`:

- Header: the headline (or, until one exists, `N sessions · M waiting`), the
  headline's age, and the two budget gauges as simple bars.
- One card per session, same facts as a lobby row. Name and emoji tinted with
  the project color. State word and time-in-state, context %, model. Topic on
  its own line; summary and last prompt below in a dimmer style, each omitted
  if empty. A deferred card carries a `DEFER` badge and its blocker, and
  deferred cards sit under a `deferred` rule.
- A footer: "read-only · reachable on the tailnet only · served by claudemux".
- Inline script fetches `/api/fleet` every three seconds and re-renders the
  cards from the JSON. A failed fetch dims the page and shows "lobby not
  reachable" until the next success. No framework, no external requests; the
  page must render with the network cut except for its own origin.
- It reads fine on a phone: single column, system font, no fixed widths.

Colors: the page has light and dark styling via `prefers-color-scheme`;
project colors are used as given for the name, with a text shadow rather
than a contrast calculation.

### 5. Fleet headline

`webHeadliner` wraps the existing `Summarizer` machinery rather than a new
client: it is built with `newSummarizer(cfg.Summary)` and is nil when that
returns nil, so `summary.enabled: false` or a missing key turns the headline
off, and the API-key/base-URL rules in `summarizerEnvOptions` apply
unchanged.

Input: for every session, `name`, `state` word, `topic`, `summary`. Prompts
are excluded deliberately: they are the rawest text in the system and the
headline does not need them. Deferred sessions are included with their
blocker, since "waiting on a review" is part of the overall picture.

Call shape: the same forced-tool pattern as `Summarize`, with a tool
`headline` whose single required string field `text` is described as one
sentence, under 140 characters, present tense, naming what is being worked
on overall and what is waiting, no preamble. A placeholder answer
(`placeholderLine`) is an error and keeps the previous headline.

When it runs: a goroutine owned by the lobby, woken by the snapshot publish
in section 3. It computes a fingerprint of the input (names, states-as-words,
topics, summaries, blockers, joined and hashed) and calls only when the
fingerprint differs from the last one it called with AND at least
`headline_interval` has passed since the last call started. A fleet where
only timers and context percentages move never triggers a call; a fleet whose
summaries change every ten seconds costs at most one call per interval.

The result is written to the holder with the time it was generated. `stale`
in the JSON is true when the fingerprint has changed since the headline was
generated, so the page can show "1 min ago" in a warning tone while a new
one is pending. A failed call logs to the debug log and leaves the previous
headline in place.

The first call fires on the first snapshot, without waiting the interval.

### 6. Failure handling

- Handler panics are recovered (`http.Server` already does this per
  connection, but the recover is made explicit and logged) and never reach
  the TUI.
- JSON encode errors return 500 with a one-line body and are logged.
- All logging goes through the existing `teardownLogf` sink, which is silent
  unless its env var points at a file, so a broken server cannot write to
  stdout and corrupt the TUI frame.
- The server sets `ReadHeaderTimeout` and `WriteTimeout` of a few seconds. A
  slow client cannot pin a goroutine.

### 7. README

A new subsection under **The switchboard**, "Sharing the fleet on the
tailnet": what the page shows, the two config keys, the `tailscale` keyword,
that it is read-only and unauthenticated by design, and the note that
`web.listen: ":7474"` would bind every interface and is not what you want on
a laptop.

## Testing

Table tests in the existing style, no network:

- `parseWebListen`: `""` → off; `tailscale:7474` → keyword + port;
  `127.0.0.1:7474` and `:7474` pass through; missing/non-numeric port
  rejected; `config.validate` rejects a negative `headline_interval`.
- `resolveWebListen` with a stubbed `tailscale ip` runner: first line taken,
  empty output and exec error both return an error naming the keyword.
- `webFleetJSON` from a fixed `swSnapshot`: field mapping, lobby order
  preserved, omitted fields (`budget`, `headline`, `context_pct`) actually
  absent, `waiting` derived from `isWaiting`.
- Headline gate: same fingerprint → no call; new fingerprint inside the
  interval → no call; new fingerprint after the interval → call; zero interval
  → every change calls; first snapshot always calls.
- Headline prompt builder: prompts absent from the text, deferred blockers
  present, session order stable.
- Headliner against a stubbed transport (same technique as the summarizer's
  tests): tool call parsed, placeholder rejected, error keeps previous text.
- `httptest` for both routes: 200 with JSON for `/api/fleet`, 200 with HTML
  for `/`, 405 for POST, 404 for `/other`, `no-store` header present.
- Lifecycle: a lobby with `web.listen` unset creates no listener; a bind
  failure sets `lastErr` and the program still runs (unit-test the helper
  that produces the status message, not bubbletea).

Manual check before calling it done: run the lobby locally with
`web.listen: 127.0.0.1:7474`, open the page in a browser, watch a session
change state and see the card follow within three seconds, and confirm the
headline appears after the first Haiku call.
