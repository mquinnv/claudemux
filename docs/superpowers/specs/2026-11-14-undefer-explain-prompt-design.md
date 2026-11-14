# Un-defer asks why, and tells the session

## Intent

Clearing a defer today (`d` on a deferred session, in the lobby or the head)
unsets the mark and nothing else. The session's Claude never learns the blocker
is gone; the user has to jump there, focus the claude pane and type it out. The
moment of un-deferring is exactly when the user knows why the blocker cleared,
so that is when to capture it and hand it to the session.

## Goals

- `d` on a deferred session opens an inline prompt for why it is unblocked, on
  both surfaces, in the slot the blocker prompt already uses.
- Enter clears the defer and submits the explanation to that session's Claude,
  with the old blocker attached.
- Clearing silently stays one extra keypress (enter on an empty line).

## Non-goals

- Multi-line input, paste of images, or any editing beyond what the blocker
  prompt has (type, backspace).
- Changing `D` (edit blocker) or the defer direction of `d`.
- Waking sessions linked by a handoff automatically.

## Anti-goals

- Text landing as the answer to a permission or trust dialog.
- Losing a defer by accident: esc must leave the session deferred.

## Approaches

1. **Inline prompt where `d` was pressed (picked by Michael).** Several
   sessions can be unblocked from the lobby without leaving it, the old blocker
   rides along automatically, and it mirrors the existing blocker prompt.
   Costs: single-line input only.
2. **Jump to the session and pre-type into Claude's own input.** Full Claude
   input, but every un-defer pulls the user out of the lobby and a silent clear
   means deleting the prefill.

## Design

**Prompt.** `d` on a deferred session (lobby row, or the head's own session)
opens `◆ unblocked: ▌` in the defer hue: the lobby status line reads
`undefer <name> ◆ unblocked: …`, the head shows it in the defer chip slot.
Footer hint: `enter undefer · esc cancel`. Input rules are `deferInputKey`'s.
The same prompt serves a handoff's WAIT mark.

**Keys.**

- enter, text typed: clear the mark and reason, then send the message.
- enter, empty line: clear only, as `d` does today.
- esc: close the prompt, session stays deferred.

**Message.** `Blocker cleared (was: <recorded blocker>): <typed text>`; when no
blocker was recorded, `Blocker cleared: <typed text>`. The recorded blocker is
the one captured when the prompt opened, sanitised the usual way.

**Delivery.** Typed into the session's claude pane and submitted with the
literal-then-delayed-Enter sequence teardown already uses, factored out of
`teardownSendCmd` so both callers share it. No `C-u` first: a draft the user
left in Claude's input is kept, and the message is appended to it. The lobby
targets the row's `ClaudePane`; the head resolves its pane as teardown does. A
session that is mid-turn gets the message queued by Claude, which is fine.

**Guard.** If the session's state is a permission prompt, the folder trust
prompt, or still starting, nothing is typed into it:

- at `d`: the prompt does not open; the defer clears immediately as today.
- at enter (state changed while typing): the defer clears and a note says the
  message was not sent and why. Same note when no claude pane is known.

**Docs.** Help entries and footers on both surfaces, plus the README's defer
section, describe the new behaviour.

## Left to the builder

- Exact note wording and where the lobby shows it.
- How the shared send helper is shaped.
- Tests: prompt state transitions on both surfaces, message composition, the
  guard at both points, and the factored send keeping teardown's behaviour.

## Calls made by Claude, not Michael

Everything under Design except the inline approach itself: the message
wording, no `C-u`, WAIT marks using the same prompt, and the guard clearing
the defer rather than refusing.
