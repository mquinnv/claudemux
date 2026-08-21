# Self-computed usage meters: dropping abtop, adding per-model windows

**Date:** 2026-08-21
**Status:** Approved by user, ready for implementation planning

## Problem

The head's `5h` and `wk` gauges read `~/.claude/abtop-rate-limits.json`
(`budget.go:173`). That file is not produced by abtop the program — nothing in
claudemux ever runs abtop. It is produced by `~/.claude/abtop-statusline.sh`, a
bash+python shim that abtop's `--setup` installs into the `statusLine` slot of
`~/.claude/settings.json`. The shim reads the JSON Claude Code feeds every
statusline command, plucks `.rate_limits`, and writes the cache file.

So the dependency is on a third party owning a settings slot we could own
ourselves, and on a file whose schema we do not control. Two consequences:

- An abtop uninstall, or an abtop that renames its cache, silently blanks the
  meters. Nothing in claudemux would report why.
- We are limited to whatever abtop chooses to forward.

Separately, Max plans carry per-model weekly windows — including Fable — that
the meters do not show at all.

## What the statusline can and cannot provide

Claude Code 2.1.238 builds the statusline payload's rate-limit object by
narrowing to exactly two windows:

```js
let k = b3n();                                    // full header-derived state
let A = { ...k.five_hour && {five_hour: {…}},
          ...k.seven_day && {seven_day: {…}} };   // only these two survive
… ...(A.five_hour || A.seven_day) && {rate_limits: A}
```

Hook payloads carry nothing: every hook input is `c_(session, …)` plus
event-specific fields, and that base has no rate-limit data.

**A statusline command can therefore never produce a per-model meter.** The
abtop removal and the Fable meter are two different problems with two different
sources, and the design keeps them on two separate paths.

## Where per-model windows come from

`GET /api/oauth/usage`, gated on the OAuth account:

```js
function gs(){ if(!$w()) return !1; return OZ(ua()?.scopes) }
async function s5e(){ … if(!gs()||!OM()) return {}; … fs.get("/api/oauth/usage") }
```

This is an OAuth-account endpoint, not a platform-API one. The summarizer's
`sk-ant-…` key (`summary.go:175`) cannot reach it: Claude Code's own schema
documents `rate_limits_available` as *"False when plan rate limits do not apply
(API key, Bedrock, Vertex, or missing profile scope)"*, and a platform key is a
different billing entity from the subscription whose windows we want.

Rather than hold an OAuth credential ourselves, we let Claude Code hold it. Its
SDK control protocol exposes `get_usage`, handled CLI-side by
`collectUsageData()`, which performs the fetch and returns the structure. It is
reachable from any process that speaks the protocol over stdio.

### Verified behavior

Probed live against 2.1.238 on 2026-08-21. Sending `initialize` then
`get_usage` on stdin to
`claude -p --input-format stream-json --output-format stream-json`, with **no
user message**, returns a `control_response` in ~2.2s carrying
`subscription_type: "max"`, `rate_limits_available: true`, and:

```jsonc
"rate_limits": {
  "five_hour": { "utilization": 12, "resets_at": "<ISO 8601>" },
  "seven_day": { "utilization": 62, "resets_at": "<ISO 8601>" },
  "seven_day_opus": null, "seven_day_sonnet": null,   // named keys are null
  "limits": [
    { "kind": "session",       "percent": 12, "scope": null,  "is_active": false },
    { "kind": "weekly_all",    "percent": 62, "scope": null,  "is_active": true  },
    { "kind": "weekly_scoped", "percent": 26, "resets_at": "<ISO 8601>",
      "scope": { "model": { "id": null, "display_name": "Fable" } },
      "is_active": false }
  ],
  "extra_usage": { … }, "spend": { … }
}
```

Two facts this establishes:

- `total_cost_usd: 0` and `model_usage: {}`. No inference runs; the call spends
  no tokens and consumes none of the limit it reports.
- The per-model row lives in `limits[]`, **not** in the named `seven_day_opus`
  / `seven_day_sonnet` keys, which were `null` while `limits[]` was populated.
  The parser must read the array.

### Measured spawn cost

| spawn | wall | SessionStart hooks fired | `rate_limits_available` |
|---|---|---|---|
| default | 2.23s | 6 | `true` |
| `--no-session-persistence --strict-mcp-config --settings '{"hooks":{}}'` | 2.14s | 6 | `true` |
| `CLAUDE_CONFIG_DIR=<scratch>` | 0.33s | 0 | **`false`** |

`--settings '{"hooks":{}}'` does not suppress hooks — settings merge
additively. Isolating the config dir does suppress them, but the OAuth profile
lives in that directory, so it also destroys the answer. `--bare` is out for the
same reason: it never reads keychain or OAuth by design.

**A poll therefore costs ~2.2s and fires the user's `SessionStart` hooks.** That
cost is what sets the cadence in §2; it is not a detail to be tuned away later.

## Design

### 1. Push path — our own statusline command

`hook ensure` (`cmd/claudemux-head/hook.go`) already validates, copies, and
registers shipped scripts, and `bin/claudemux` calls it on every launch. This
extends that machinery by one artifact and one settings key.

The command is a **`claudemux-head statusline` subcommand**, not a shell script.
abtop's shim spawns bash and python3 on every statusline render; ours is a Go
binary already present. It reads the payload on stdin, writes
`~/.claude/claudemux/rate-limits.json`, and prints nothing — the same visible
result as today, since abtop's shim also prints nothing.

**Claiming rule.** `statusLine` is a single exclusive string, unlike
`hooks.<event>[]` which is an append-only list. `hook ensure` claims it only
when it is absent, or when its command's basename is `abtop-statusline.sh`, or
when it is already ours. Any other value is left untouched and reported on
stderr; the meters then simply stay unavailable. We do not silently replace a
statusline someone built.

One consequence to state plainly: taking the slot from `abtop-statusline.sh`
stops `~/.claude/abtop-rate-limits.json` being refreshed, so a user who still
runs the abtop TUI separately would find it stale. That is the intended effect
of removing the dependency, not an oversight — but it is why the claim is
narrowed to abtop's own basename and announced rather than performed silently.

**Cache file.** `~/.claude/claudemux/rate-limits.json`, alongside the existing
`panes/`, `asking/`, and `worktree-asks/` directories. `budget.go` reads it,
keeps honoring `CLAUDEMUX_RATE_LIMITS_PATH`, and falls back to the abtop path
for one release so an upgrade that lands between `hook ensure` running and the
next statusline render does not blank the gauges.

### 2. Pull path — `get_usage` poller

**Single-flight, shared cache.** `~/.claude/claudemux/usage.json`, TTL-gated
under a lock file. Every head pane reads the cache; only the pane that finds it
stale and wins the lock spawns. Ten panes cause one spawn, not ten.

**TTL: 15 minutes.** Justified by §"Measured spawn cost": each poll costs 2.2s
and six hook processes, including whatever network calls the user's hooks make.
Weekly windows move slowly enough that 15 minutes costs nothing in freshness,
and the fast-moving 5h meter does not depend on this path at all — it rides the
free push path. This split is the entire reason the design carries two sources.

**Invocation.**

```
claude -p --input-format stream-json --output-format stream-json \
       --no-session-persistence --strict-mcp-config --mcp-config '{"mcpServers":{}}'
```

with `TMUX_PANE` **unset in the child environment**. `claudemux-map.sh` keys its
pane-map file on `$TMUX_PANE` and exits immediately when it is absent; without
this, a poll spawned from a head pane writes a phantom session into
`panes/<head-pane>.json`. Unsetting one variable is cheaper and needs no
coordination with the hook script.

Send `initialize`, then `get_usage`; read the matching `control_response`; close
stdin. Enforce a hard timeout and kill the child on expiry — this runs in a poll
loop and must not wedge a pane.

**Parsing.** Walk `rate_limits.limits[]` for entries with
`kind == "weekly_scoped"` and a `scope.model.display_name`; each becomes one
meter. Do not read `seven_day_opus` / `seven_day_sonnet`. No plan check is
needed — the server emits these rows only where they apply, so rendering
whatever comes back is already correct for Pro, Max, and Team.

**Self-disabling.** When a response reports `rate_limits_available: false` (API
key, Bedrock, Vertex, missing scope), record that and stop polling. Users on
those setups pay zero spawns.

### 3. Rendering

`rateGauges()` (`tui.go:1676`) is already the model-independent seam shared by
the head (`tui.go:1670`) and the switchboard (`switchboardtui.go:712`), so both
panels pick this up from one change.

Model rows render after `wk`, in the established shape — bar, percent, reset:

```
5h ▇▇▁▁▁▁ 12%→7:10p · wk ▇▇▇▇▁▁ 62%→Mon · fable ▇▇▁▁▁▁ 26%→Mon · empty in 3h12m
```

The drop order for narrow panes extends the existing fixed order and stays
fixed: **eta, then model rows, then `wk`, then `5h`.** The 5h window is the one
a user acts on, so it is the last thing to go.

### 4. Failure posture

Claude Code's own schema marks `get_usage` *"Experimental — the response shape
may change."* Every pull-path failure — spawn error, timeout, protocol change,
unparseable row, `limits[]` absent — degrades to **no model rows**, never to an
error state and never to a blank panel. The `5h` and `wk` gauges must remain
correct with the pull path completely broken. Enforcing that is what keeps an
experimental dependency acceptable.

### 5. Testing

- **Statusline subcommand:** table tests feeding recorded payloads on stdin,
  including one with `rate_limits` absent (non-subscriber) and one with only
  `five_hour` present.
- **`hook ensure` claiming:** the four `statusLine` cases — absent, abtop's,
  a third party's, already ours — asserting the third is left byte-identical.
- **`get_usage` client:** run against a fake `claude` that speaks the control
  protocol, with the binary path injectable. No network and no real spawn in the
  suite. Cases: valid response, `rate_limits_available: false`, malformed
  `limits[]`, timeout, non-zero exit.
- **Parsing:** the live payload above, checked in as a fixture.
- **Live verification:** in a real status pane, not inside a worktree. A
  worktree-hosted pane has previously passed while the shipped feature was
  broken.

## Out of scope

- Rendering `extra_usage` / `spend`. The response carries them; no meter is
  specified for them here.
- Any use of `behaviors` (the local transcript scan `get_usage` also returns).
- Removing the `CLAUDEMUX_RATE_LIMITS_PATH` override.
