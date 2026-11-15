# A defer on another session ends when that session is out of the way

Date: 2026-11-14

## Intent

A session can be deferred because it is waiting on another claudemux session:
a handoff marks the launching session `WAIT`, and the user can defer a session
with a blocker that is really "that other session". Today nothing but the
waiting agent itself clears the mark. When the blocker finishes and the agent
never hears about it (the peer's one-shot idle notice already fired, the peer
stopped on a question answered in its own pane, the peer was closed), the
session sits in defer although nothing blocks it any more. The conductor skips
it and the user forgets it.

claudemux should notice that the blocker is out of the way, clear the mark, and
prompt the waiting agent to pick its work back up.

## Goals

- A defer linked to another session clears on its own when that session is no
  longer in the way.
- The waiting agent is nudged to resume, without the user having to visit it.
  The nudge is configurable and on by default.
- Free-text defers ("waiting on Ana", "CI") keep working exactly as they do now.

## Non-goals

- Detecting that an external blocker (a person, CI, a deploy) has cleared.
- Changing how the conductor, the lobby grouping or the badges treat a deferred
  session.
- Replacing the peer's `SendMessage` reply or `notify_when_idle`. Those stay
  the normal path; this is the net under them.

## Anti-goals

- A mark that clears while the blocker is still working, or before it ever
  started (a freshly launched peer is idle until its brief lands).
- A nudge that lands on top of the peer's own reply, so the agent is prompted
  twice for one event.
- A nudge that submits or destroys text the user had typed in the prompt.
- Clearing or overwriting a defer the link does not belong to.

## Approaches

1. **The waiting session's head watches its blocker (chosen).** The head
   already polls its own mark and the session list. It works with no lobby
   open, survives head and lobby restarts, and sees a blocker that was closed
   or killed.
2. **The lobby's conductor does it.** Same rule, but only while a lobby is
   running, so a stale mark can still sit there otherwise.
3. **The peer's Stop hook clears its parent.** Instant, but never fires for a
   peer that is closed or crashes, which is one of the cases that strands a
   session today.

Michael picked 1, asked for the nudge, and asked for it to be configurable and
on by default. He is fine with a formal link to the blocking session beside the
free-text blocker.

## Design

### The link

A deferred session may carry a link to the session it waits on, stored on the
tmux session beside the mark, in the same shape as the parent link
(`@claudemux_parent` / `@claudemux_parent_created`):

- `@claudemux_defer_on`: the blocking session's tmux name
- `@claudemux_defer_on_created`: that session's `session_created`, so a
  recycled name is never mistaken for the blocker
- `@claudemux_defer_on_seen`: set to `1` once the blocker has been seen working
  after the link was made; it lives on the session so a head restart does not
  forget it

The free-text reason stays as it is and is still what both surfaces show.
Clearing the defer unsets the link options along with the mark and the reason,
in the same tmux invocation, on every path that clears a defer today.

### Who sets it

- **Handoff.** Step 4 of the `claudemux-handoff` skill sets the link to the
  peer it just launched. My call: add a `claudemux-head defer` subcommand
  (`--wait-on <session> --reason <text>`, and `--clear`) so the skill does not
  hand-compose three tmux options and a creation time. The skill's existing
  rules stay: skip it when the user's own mark (`1`) is set, never clear a
  defer this session did not set.
- **The user's `d` prompt** (head and lobby, and `D`'s edit of an existing
  blocker). `Tab` completes the names of the other live sessions (Michael
  asked for this). My calls on how:
  - `Tab` completes what is typed so far, matched as a case-insensitive prefix
    of a session name. The session being deferred is never offered.
  - With several matches, repeated `Tab` cycles through them in the lobby's
    row order; `Shift+Tab` cycles back. Typing anything else ends the cycle
    and keeps what is shown.
  - `Tab` on an empty line cycles through every other session, starting with
    this session's handoff children, since those are the likeliest blockers.
  - No match: `Tab` does nothing.
  - On enter, a blocker that is exactly the name of another live session,
    whether completed or typed out, links the defer to it. The mark stays `1`
    and the badge stays `DEFER`. Any other text is a free-text defer with no
    link, as now.
  - The prompt shows that a link will be made (for example a marker after the
    name) so a linked defer and a free-text one are told apart before enter.

### When the blocker is out of the way

Evaluated by the waiting session's head on its normal poll, only while its
mark is set and a link exists:

- The blocker is **gone**: no live session has that name with that creation
  time.
- Or the blocker has been **seen working** since the link was made, and is now
  waiting on a human (the lobby's `isWaiting` states) and is not itself
  deferred.

A blocker that is itself deferred keeps the mark, so a chain of handoffs
unwinds from the far end.

### What happens then

1. A grace period starts (my call: about 20 seconds, a constant). It gives the
   peer's own reply or idle notice time to arrive.
2. If the waiting session starts a turn of its own during the grace period, the
   mark is cleared and no nudge is sent. The agent already heard.
3. Otherwise, when the grace period ends, the mark is cleared. If the nudge is
   enabled and the session is idle at its prompt, the head types a short
   message into the claude pane and submits it, naming the blocker, whether it
   stopped or closed, and the recorded reason, and telling the agent to check
   for the peer's report and continue.
4. With the nudge disabled, or when the session is not idle at an empty prompt
   (asking a question, a draft in the input), only the mark is cleared. The
   session then reads as waiting and the conductor brings the user to it.
5. Either way the head shows a chip saying the blocker finished or closed,
   until the session's next turn, so an idle session that just left defer
   explains itself.

If the blocker goes back to work during the grace period, the grace period is
abandoned and the mark stays.

### Configuration

A new `defer` block in the config, with `auto_nudge` (default `true`), settable
through the existing `config set` path. It gates only the typed prompt. The
automatic clear always happens.

### Restore

The reboot-restore record carries the link's session name. On restore the link
is re-made against the relaunched session of that name, with its new creation
time, when one is restored. When none is, the link is dropped and the defer
comes back as a plain one. A reboot must not read as every blocker having
closed.

### Skill text

`claudemux-handoff` step 4 uses the new command. Its closing section says that
claudemux clears the mark and prompts this session on its own if the peer's
reply never arrives, and keeps the instruction to clear the mark first when the
reply does arrive.

## Left to the builder

- How the head reads the blocker's state and defer mark (extending the lineage
  listing it already fetches is the obvious route).
- How to tell the prompt is empty before typing. The existing permit and trust
  probes already capture the claude pane. The typing itself should reuse the
  teardown flow's literal-send and separate Enter, without its `C-u`.
- The exact nudge wording and chip text.
- Whether the lobby and web page show the link. Not required.

## Testing

The clear rule, the grace-period transitions and the link parsing are pure
functions and get table-driven tests like their neighbours (`deferpub_test.go`,
`swconductor_test.go`). The typed nudge is verified by hand with two real
sessions: hand off, let the peer finish with its notice suppressed, and watch
the waiting session leave `WAIT` and resume.
