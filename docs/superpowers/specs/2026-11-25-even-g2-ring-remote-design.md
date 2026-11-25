# Even G2 / R1 ring remote — design

Date: 2026-11-25
Status: draft, pending Michael's approval

## Problem

Michael wears Even Realities G2 glasses with an R1 ring and wants to keep his
claudemux fleet moving when he is away from the keyboard. Even's built-in
Terminal Mode does not do this well:

- It does not attach to the running tmux session. `even-terminal` resumes a
  second Claude process on the same session id through the Agent SDK, with
  `permissionMode: "default"` and a short tool allowlist. That copy asks for
  confirmation on nearly everything, cannot answer the question sitting in
  the real pane, and appends its turns to the same transcript the live
  session and claudemux read.
- Switching is the phone app's list of every Claude transcript on the
  machine. It has no idea which sessions are waiting.

## Intent

A glasses view of the fleet that works the way the conductor does: it puts
the session that needs Michael in front of him, shows what it is asking, and
lets him answer with the ring. **The primary goal is to interact without
speaking or typing.** Every answer path in this design is a ring gesture.

## Goals

- While the Even app is awake, the glasses show the session the conductor
  would take Michael to next, with what it is asking.
- One ring tap answers it, in the real tmux pane, under that session's real
  permission mode.
- After an answer the glasses move to the next waiting session on their own.
  Manual switching is available but rarely needed.
- Sessions that want free text are still answerable silently: a short list
  of replies to pick from, some fixed, some suggested from what the session
  just said.
- Off unless configured, like the web status page.

## Non-goals

- Voice dictation. Wanted later, not now, and never the main path.
- Mirroring the terminal or streaming output. This is a decision surface,
  not a screen.
- Starting, closing or restarting sessions from the glasses.
- Moving the Mac's tmux client. The glasses have their own cursor; the desk
  conductor is untouched.
- Any change to Even's Terminal Mode, or cooperation with it.
- Publishing to the Even Hub store. This is a private build for Michael.

## Anti-goals

- **A wrong answer.** Keys must never land on a different dialog than the one
  shown on the glasses. A refused answer is fine; a misdirected one is not.
- A control surface reachable without a secret. The existing page is
  unauthenticated because it is read-only; this one can act.
- Asking Michael to confirm things the session itself is not asking.

## Constraints

Even Hub platform (from hub.evenrealities.com docs, 2026-11):

- An app is HTML/JS in a WebView inside the Even phone app. The glasses only
  render containers and emit input events.
- Display is 576x288, monochrome green, 16 levels. At most 8 non-image
  containers per page. Text containers update in place without flicker
  (2,000 chars); native lists (20 items, 64 chars each) cannot be updated in
  place and rebuilding the page flickers.
- Ring and temple gestures are the same set: tap, double tap, swipe up,
  swipe down, long press and release. The OS claims tap-then-long-press.
- Network calls need the origin whitelisted in `app.json` and CORS headers
  from the server. Docs say HTTPS in production; whether a private build may
  whitelist a plain `http://` tailnet origin is not documented.
- The WebView stalls when the phone app is backgrounded. Sockets drop.
- Private builds launch from the phone app, not the glasses menu. A Beta
  build with Michael as the only tester launches from the glasses menu.

claudemux today:

- The lobby learns the fleet only from tmux options, polled every second. It
  knows *that* a session is asking, never *what*. The `ask` hook reads
  `hook_event_name`, `tool_name` and `session_id` and discards `tool_input`.
- A permission dialog is detected by scraping the pane for "esc to cancel".
  Plan approval has no handling at all.
- `nudgeClaudePane` types only into a provably empty prompt and refuses on
  any dialog. Nothing can pick a menu option.
- The web server is read-only, unauthenticated, tailnet-guarded, polled by
  the page every 3s, and has no path back into the lobby's update loop.
- The repo is Go only, with no build step beyond `go install`.

## Approaches

1. **Our own Even Hub app driven by the lobby (chosen).** The lobby gains
   authenticated remote routes; a small app shows the waiting session and
   sends ring choices back; the lobby types the answer into the real pane.
   Fixes switching and the confirmation noise, at the cost of a control
   endpoint and the repo's first non-Go code.
2. **Keep Terminal Mode, launch sessions under `even-terminal claude`.** The
   glasses would join the real session, which removes the extra
   confirmations. Switching stays Even's list, which claudemux cannot steer,
   and Even labels the sync mode experimental.
3. **Both: Terminal Mode for reading, a tiny switcher of our own.** Two apps
   to hop between on the glasses, which is the onerous part again.

Michael picked 1. It is the only one that fixes switching.

## Design

### 0. Spike first

Four facts are undocumented. The spike is the first phase of the plan, it is
throwaway, and the rest of the plan is written against its results. Each
item has a pass bar and a settled fallback, so no result stops the project.

| # | Question | Who | Pass | If it fails |
|---|---|---|---|---|
| 1 | Can a private Even Hub build `fetch` a whitelisted `http://<tailnet-ip>:port` origin from the phone? | Michael's phone and glasses, with a hello-world build | A fetch to the lobby's `/api/fleet` returns | Serve HTTPS on the tailnet: a real certificate for a public DNS name that resolves to the node's tailnet address, with `web.tls_cert` / `web.tls_key` config and that hostname added to the guard's allowed hosts. Nothing is exposed publicly. |
| 2 | What keys answer each dialog in the installed Claude Code (AskUserQuestion, permission prompt, plan approval)? | An agent, in a scratch tmux session on this Mac | A fixture and a key sequence per dialog | A dialog with no reliable key sequence is shown as "answer at the desk". |
| 3 | Does `PermissionRequest` fire for `ExitPlanMode`, and what is in the AskUserQuestion `PreToolUse` `tool_input`? | Same scratch session | Plan approval already publishes a waiting state | Plan approval is dropped from the first version. Making it a waiting state would also change what the desk conductor does, which is a separate decision. |
| 4 | Does the WebView keep polling with the phone locked and the Even app in front? Is in-place text redraw clean? | Michael's phone and glasses | Polls continue for 10 minutes locked | Accepted limit: the glasses go stale while the app sleeps and catch up on the first poll after it wakes. The header shows the age of the data when it is over 10s old. |

### 1. What the glasses show

One session at a time. Layout is three text containers (header, body,
options), all updated in place so nothing flickers. The app draws its own
cursor in the options text rather than using a native list.

Waiting session with options:

```
sonar · tierpoint-cutover           1/3
Cut DNS over before or after the
freeze?
> Before the freeze
  After the freeze
  ── defer · skip
```

Waiting session with no options (finished a turn, or asked in prose):

```
phenix · lead-dedupe                2/3
Tests pass. I can open the PR now or
tighten the matcher first.
> Open the PR                    (suggested)
  Tighten the matcher first      (suggested)
  Yes, go ahead
  Continue
  ── defer · skip
```

Nothing waiting:

```
all clear                    7 working
next likely: beejax · image-pin
```

The header is `project · topic` and position in the waiting queue. The body
is the question, or the tail of the session's last message, trimmed to fit.

The glasses stay on the session they are showing until it is answered,
deferred, skipped or stops waiting. A newer or more urgent arrival changes
the count in the header, never the session under the cursor.

### 2. Ring gestures

| Gesture | On a session | On the fleet list |
|---|---|---|
| Swipe up / down | Move the cursor through options | Move through sessions |
| Tap | Send the highlighted option | Open that session |
| Double tap | Open the fleet list | Back to the waiting session |
| Long press | Unused (reserved for voice later) | Unused |

`defer` and `skip` are ordinary rows at the bottom of every option list.
Skip snoozes the session for the glasses only, with the conductor's existing
10-minute snooze rule. Defer sets the real defer mark with the reason
"deferred from glasses", so it shows in the lobby too.

A tap on an option whose label reads as broad or lasting ("Yes, and don't
ask again", "allow all edits") asks once. Nothing marks these, so they are
recognised from the label text, and an unrecognised permission choice other
than a plain yes or no is treated as broad: the row changes to
`tap again to confirm`. No other answer is double-confirmed. *(My call.)*

### 3. Where answers come from

| The session is | Options shown | How the answer is sent |
|---|---|---|
| In AskUserQuestion, one single-select question | The question's own options | Menu keys for that option |
| In AskUserQuestion, multi-select or several questions | None: "answer at the desk" plus defer/skip | Not sent |
| At a permission prompt | The numbered choices read from the pane | That choice's key |
| At plan approval | The choices read from the pane | That choice's key |
| Idle, waiting for a prompt | Up to 3 suggested replies, then the fixed replies | Typed into the empty prompt, then Enter |

Choices that need typing ("Other", "No, and tell Claude what to do
differently") are not shown.

Multi-select and multi-question asks are left out of the first version
because a wrong partial answer is worse than no answer. *(My call.)*

**Suggested replies.** For an idle session, the lobby asks the existing
summarizer (Haiku, same key and file as summaries and the headline) for up
to three short replies Michael would plausibly give to the session's last
message. They are generated once per waiting episode, only for the session
the glasses are showing, and marked as suggested. With no API key the list
is just the fixed replies. This is what makes prose questions answerable
without typing. *(My call; say if you would rather start with fixed replies
only.)*

**Fixed replies** come from config, with these defaults: "Yes, go ahead",
"Continue", "No, stop and wait for me".

### 4. Capturing the question

- The `ask` hook, on `PreToolUse` for AskUserQuestion, also stores
  `tool_input.questions` (question, header, options with label and
  description, multiSelect) in the existing `asking/<sid>.json` marker.
  Still removed on `PostToolUse` and `UserPromptSubmit`.
- Permission and plan dialogs are parsed from a pane capture, the same
  source `permissionDialogUp` already trusts: the prompt text and its
  numbered choices.
- For an idle session the body is the last assistant text from the
  transcript, found through the pane map; if that fails, the tail of the
  pane capture.

The lobby gathers this detail only for the session the glasses are showing,
on request, not for the whole fleet every second.

### 5. Sending the answer safely

A new sender next to `nudgeClaudePane`, for dialogs:

1. The glasses send `{session, episode, choice}`. `episode` is the session's
   `Since` plus a hash of the question text and option labels it was shown.
2. The lobby re-captures the pane and rebuilds the same detail. If state,
   `Since` or hash differ, it sends nothing and returns "changed"; the
   glasses redraw.
3. It sends the keys for that choice, then confirms the dialog is gone on
   the next capture. If it is still up, it reports failure and sends nothing
   more.

Free-text replies go through `nudgeClaudePane` unchanged, so a draft
Michael left in the prompt is never overwritten; the glasses show "prompt
has a draft" and offer defer/skip.

One answer at a time per session; a second request while one is in flight is
refused.

### 6. Remote routes

Mounted in the existing web server behind the tailnet guard, under
`/remote/`, with their own bearer-token check and CORS for the Even app's
origin. They run only when `remote.enabled` is true and a token exists.

- `GET /remote/next` — the waiting queue (names, states, ages) and full
  detail for the head of it, or for `?session=`.
- `GET /remote/fleet` — every session, one line each, for the fleet list.
  Not filtered by `hide_private_orgs`: this is Michael's own view.
- `POST /remote/answer` — `{session, episode, choice}` or `{session,
  episode, reply}`.
- `POST /remote/defer`, `POST /remote/skip`.

The glasses poll `/remote/next` every 2s while the WebView is alive. Polling
rather than a socket, because sockets drop on backgrounding and the existing
page already polls.

Write actions reach the lobby through `tea.Program.Send` with a reply
channel, so they run inside the update loop like key presses do and use the
same command builders (`setDeferCmd`, the senders above).

Like the status page, the routes are up exactly when the lobby is.

### 7. Glasses queue vs the desk conductor

The glasses keep their own cursor and skip list, held in the lobby and keyed
by the token. They use `waitingQueue` unchanged, so order is the
conductor's order. Answering from the glasses makes the session stop waiting, and
the desk conductor reacts as it would to an answer typed at the desk. The
glasses never call `switch-client`.

### 8. Config and secret

```yaml
remote:
  enabled: false
  replies: ["Yes, go ahead", "Continue", "No, stop and wait for me"]
  suggest: true
```

The token is `CLAUDEMUX_REMOTE_TOKEN`, read through `configValue` from the
same env file as the API key. `remote.enabled` with no token, or with
`web.listen` empty, is reported the way a bad `web.listen` is today: the
lobby runs without the routes and says why on its status line.

### 9. The app

A new top-level `glasses/` directory: TypeScript, Vite, the
`@evenrealities/even_hub_sdk` package, packed to an `.ehpk` with the
`evenhub` CLI. It is not part of `go install` and the release workflow does
not build it.

- First launch shows a setup page in the phone WebView: lobby URL and token,
  saved to localStorage.
- All state lives in the lobby. The app renders what `/remote/next` returns
  and posts gestures back.
- When a poll fails it shows "lobby not reachable" and keeps trying.

### 10. Testing

- Go: fixtures for each dialog kind feed the parser and the sender's
  refuse-on-change logic; route tests cover auth, CORS and the stale-episode
  path. No test sends real keys.
- App: the pure pieces (cursor movement, text layout to 576x288) get unit
  tests; the rest is checked in the Even simulator and on the glasses.

## Left to the builder

- Exact key sequences per dialog (from the spike).
- How the body text is trimmed and wrapped for the display.
- The suggested-replies prompt wording and its length cap.
- Whether `/remote/next` detail is cached between polls.
- README section and the `glasses/README.md` install steps.

## Later

- Hold-to-dictate free text.
- Multi-select and multi-question asks.
- An "away" hint that nudges sessions to ask with options rather than prose.
