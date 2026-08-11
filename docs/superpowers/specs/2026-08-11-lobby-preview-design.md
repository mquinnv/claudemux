# Lobby pane preview design

2026-08-11

## Purpose

Give the switchboard lobby a live preview of the selected session, the way tmux's own
`choose-tree -Zs` previews the session under the cursor. Today the lobby tells you a
session's state, context, topic, and summary — all of it derived. The preview shows the
thing itself: what that session's claude pane looks like right now, so you can decide
whether to jump without jumping.

## Scope

- Preview of **one** session at a time: the one under the lobby's selection cursor.
- Captures the session's **claude pane**, not its head or shell pane.
- Always on. No new keys, no toggle, no mode.
- Read-only: `capture-pane` never touches the previewed session.

Out of scope: previewing non-claudemux tmux sessions (the lobby doesn't list them),
scrolling within the preview, and previewing more than one session at once.

## Layout

The preview is a bordered box below the fleet list, spanning the full pane width:

```
claudemux switchboard                                    ← title, 2 rows

 ● beejax-platform          Idle       5:28  ███░░  57%  cd-receiver roll fix
    push pr #95 to github, 12 commits ready for review
 ● cd-receiver              Idle     24h42m  █░░░░  28%  beejax cd-receiver review
    generating fresh handoff document for review session
   claudemux                Thinking   1:03  ████░  75%  context meters alignment
    reviewing tmux switcher interface design
                                                         ← list, 1-2 rows per session

┌─ cd-receiver ────────────────────────────────────────────────────────────────┐
│ ● Bash(git push origin HEAD)                                                 │
│   ⎿  To github.com:mquinnv/beejax-platform.git                               │
│      * [new branch]  cd-receiver-fix -> cd-receiver-fix                      │
│                                                                              │
│ Pushed. Want me to open the PR?                                              │
│                                                                              │
│ > _                                                                          │
└──────────────────────────────────────────────────────────────────────────────┘

escorting → cd-receiver · 2 waiting                      ← status + hints, 3 rows
space conduct/standby · j/k select · enter jump · q quit
```

Full width rather than a right-hand split: a claude pane is 119 columns in the standard
claudemux layout, and the lobby is typically ~171. A bottom box renders those lines at
native width; a side-by-side split would leave ~85 columns and clip the right edge of
every line of tool output.

### Sizing

The preview is allocated **before** the list, not from its leftovers. Giving the list
what it wants first would mean a large fleet silently squeezes the preview out —
exactly when a preview is most useful — so the box claims its share and the list is
capped to fit around it.

All heights below are **content rows, excluding the box border**, which costs 2 more.

```
chrome  = 5                                  // title + blank, blank + status + hints
                                             // 6 when a tmux error line is showing
avail   = m.height - chrome
preview = clamp(avail / 3, 6, 16)
list    = avail - (preview + 2)
```

- When `list` would fall below 2 rows — one session — the preview is **omitted
  entirely** and the lobby renders exactly as it does today, uncapped. A pane that can
  show either the fleet or a preview, but not both, should show the fleet.
- Otherwise the fleet list is capped to `list` rows, with a `… +N more` line when
  sessions are dropped.

The row cap is new. Today the lobby renders every session and lets tmux clip whatever
overflows; with a preview at the bottom, that overflow would silently push the preview
off-screen. Capping the list keeps the preview visible and states the truncation
instead of hiding it. The cap applies only when a preview is being drawn.

## Data flow

The preview is a second Bubble Tea command, independent of the fleet poll:

```
swTickMsg ──▶ swPollCmd ──▶ swSnapshotMsg ──┐
                                            ├─▶ swPreviewCmd(pane) ──▶ swPreviewMsg
j/k selection change ───────────────────────┘
```

`swPreviewCmd` runs a single command with the same 2-second context bound the other
three tmux calls use:

```
tmux capture-pane -e -p -t <pane_id>
```

It is issued on two events:

1. **A landed snapshot** — keeps the preview roughly a second fresh, in step with the
   fleet list.
2. **A selection change (`j`/`k`)** — without this the preview lags a full poll behind
   the cursor, which reads as the UI being broken rather than merely slow.

Two guards, both mirroring patterns already in this codebase:

- **In-flight flag.** At most one capture outstanding, the same shape as the head's
  summarize in-flight flag. A request arriving while one is running is dropped; the
  next snapshot re-requests. Captures can never queue up behind a slow tmux.
- **Stale-drop.** `swPreviewMsg` carries the pane id it captured. If that no longer
  matches the selected session's pane when it lands, it is discarded rather than
  painted — otherwise a fast `j j j` flashes previous sessions' screens.

## Finding the claude pane

`swSession` gains a `ClaudePane string` field, populated by `buildSwSnapshot`.

The snapshot already lists every pane with `#{pane_current_command}` — that is how it
identifies `claudemux-head` panes — and simply discards the rest. The claude pane is
matched with the rule `panemap.go:claudePaneCandidates` already uses: a
`pane_current_command` of `claude`, or `node` for runtimes that report the shim.
First match in listing order wins; the field is empty when a session has none.

Deliberately not the session's *active* pane: a session left focused on its shell would
preview a fish prompt, and one left on its head pane would preview the same four rows
the lobby row already summarizes.

## Rendering the capture

`capture-pane -e` emits **only** SGR sequences (`ESC[38;5;NNNm`, `ESC[0m`, `ESC[9m`) —
verified against a live claude pane. No cursor motion, no OSC. That makes it safe to
embed in a rendered view and correctly clippable by `ansi.Truncate`, which `clipLine`
already uses.

Two pure steps:

1. `previewTail(capture string, n int) []string` — split, drop trailing blank lines,
   return the last `n`. The tail is the useful end of a claude pane: the newest turn and
   the input box. Trailing blanks are trimmed first because a pane with an idle input
   box ends in several of them, and an untrimmed tail would show mostly empty rows.
2. Per line: `ansi.Truncate` to the box's inner width, then append an explicit SGR
   reset. The reset is what stops a color opened mid-line from bleeding into the
   border and everything after it.

Colors are kept rather than stripped: they are what make a claude pane scannable at a
glance — which tool ran, what failed, whether it is waiting.

## Failure modes

| Case | Behavior |
|---|---|
| capture fails (session or pane died mid-tick) | box keeps its frame, shows a dim `preview unavailable`; `lastErr` is untouched — that line belongs to the fleet poll |
| session has no claude pane | box shows a dim `no claude pane` |
| empty fleet | no box at all |
| pane too short (see Sizing) | no box; today's layout, uncapped and unchanged |
| capture times out | same as a failure; the next poll retries |

A failed preview never disturbs the fleet list or the conductor. The lobby's job is
ferrying clients; the preview is decoration on top of it and must not be able to break
it.

## Testing

Every piece is a pure function over strings, matching how `conductor` and
`buildSwSnapshot` are already tested — no tmux required.

- **`buildSwSnapshot` records `ClaudePane`** — fixtures for the normal case, the
  `node` shim, a session with no claude pane, and a session with several (first wins).
- **`previewTail`** — trailing-blank trimming, fewer lines than requested, exactly
  enough, more than enough, all-blank input.
- **`renderPreview`** — returns exactly the requested height; every line measures at or
  under the box width even when fed SGR-laden input; every line ends reset-terminated.
- **Model** — a selection change requests a capture; a `swPreviewMsg` for a pane that is
  no longer selected is ignored; the in-flight guard prevents overlapping captures.
- **View** — the box is omitted below the height floor; `… +N more` appears when the
  list is capped; the fleet list is unchanged when the preview is omitted.

## What this does not change

- No new key bindings. `prefix+S` still jumps to the lobby; `space`, `j`/`k`, `enter`,
  and `q` behave exactly as they do now.
- The conductor is untouched. Arriving at the lobby still dispatches you to the oldest
  waiting session; the preview does not pause, delay, or otherwise interact with that.
- Session rows keep their fixed column grid (`swNameColW`, `swStateColW`, `swAgeColW`,
  `swCtxColW`); the preview sits below them and does not touch that layout.
