# Lobby handoff families — design

Date: 2026-10-16
Status: draft, awaiting Michael's approval

## Why

The `claudemux-handoff` skill lets one session launch peer sessions in other
repos (`claudemux -n -d -w <dir>`) and brief them over `SendMessage`. On
2026-10-16 iai-admin handed work to beejax, usm and ag. They showed up in the lobby
as unrelated rows scattered through a 12-session list. Nothing tied them to
iai-admin, and it was easy to lose track of which rows belonged to which
effort and what was still running.

## Goal

When a session hands work to peers, the lobby shows them as one **family**:
the peers are indented under the session that launched them, and the family
stays together in the list.

Picked from mockups (option A of three):

```
 ● remix-3       pr 972 utilization fix     Bash      2m
 ◆ iai-admin     phone-number rollout       Idle     14m  deferred
   ├ beejax      image-cd #15 rolling       Background 3m deferred
   ├ ● usm       mark 800-746 render paths  Idle      1m
   └ ag          cookie precedence          Thinking  0m
 ● remix-6       ca dead postbacks          Idle      6m
 ─ deferred ──────────────────────────────────────
 ◆ digiscreener  draft PR opened            Idle      2d  deferred
```

## Behaviour (decided by Michael)

- Peers render indented under their parent with tree glyphs (`├`, `└`).
- Every member keeps its own row exactly as today: marker, state, age,
  context, model, badge, and its detail line.
- **The family is placed by its liveliest member.** It stays in the active
  list while any member is active, even when the parent is deferred, which the
  handoff skill's defer step does on purpose. Formally, a family's group is the
  *minimum* `swGroupOf` over its members (active < closing < deferred). The
  family only drops under the `─ deferred` rule when every member is deferred.

## Behaviour (Claude's calls; Michael can override)

1. **How the parent is recorded.** The launcher records it with no skill
   change. When `bin/claudemux` creates a new session and `$TMUX_PANE` is set
   and that pane's `pane_current_command` is `claude` (or the `node` shim, the
   same rule as `swClaudeCommand`/`swClaudeShimCommand`), it sets the tmux
   session option `@claudemux_parent` on the new session to the calling
   session's name. This test excludes:
   - the lobby's `n` key, whose pane runs `claudemux-head`;
   - a human typing `claudemux` in a session's shell pane, whose pane runs
     zsh/fish;
   - launches from outside tmux.

   `CLAUDECODE=1` alone is **not** enough, because a tmux server started from a
   Claude Bash call can leak it into every pane. Attaching to an existing
   session (no `-n`, name already exists) never sets or changes a parent.
2. **Order within a family:** the order the peers were launched in
   (`session_created`, ascending). The family block takes the position its
   parent would have had in the group the block belongs to. Any ordering
   that keeps the stable-sort property swSortSessions documents (a group change
   moves only that block) is fine.
3. **Nesting depth:** recursive. A peer that hands off again gets its own
   indented children (`│ ├ …`). Guard against cycles by treating any session
   seen twice as top-level.
4. **Parent not found:** this covers a parent that closed, crashed, or is
   alive but fails the guard in (5). The peer renders as a normal top-level
   row, and its detail line leads with a dim `← iai-admin`. The tag is
   history ("launched from"), so it stays true even if an unrelated session
   now has that name. When the parent is live and matches, no `←` tag is
   shown; the indentation says it. A parent that is a live tmux session but
   not a lobby row (no claudemux head) counts as not found.
5. **Name reuse guard.** The launcher also stamps
   `@claudemux_parent_created` with the parent's `#{session_created}`. A
   link holds only while a live session with that name has *that exact*
   creation time. Once the parent closes, a new, unrelated `beejax-2` can't
   adopt the old one's peers, because its creation time differs. Add
   `#{session_created}`, `#{@claudemux_parent}` and
   `#{@claudemux_parent_created}` to the lobby's `list-sessions` format.
6. **Conductor unchanged.** Families are display order only. `waitingQueue`
   still sorts by `Since`, and defer, stay and closing semantics are untouched.
7. **One order everywhere.** Turning the tree into a flat list happens in the
   snapshot sort (where `swSortSessions` runs today), not in `View`. That way
   the selection, scroll window, divider placement and key actions all
   index the same flat list the screen draws. `swDividerAt` keys on the
   *family's* group, not the row's own: a deferred child inside an active
   family must not draw a `─ deferred` rule above itself.
8. **Web status page:** mirrors the lobby. A family moves as a whole into
   the section its group puts it in. So a deferred child of an active family
   appears in the **active** table, indented under its parent, and carries
   its own "deferred" badge and defer reason inline, the same way the lobby
   row keeps both. Each section's heading counts the rows visible in it.
   The overall header ("· 3 deferred") stays per session, so the two counts
   may differ. That is intended: one counts sessions, the other counts rows
   in a section. `webFleetView` rows carry a depth plus the orphan tag.
9. **Reboot restore:** `sessionRecord` gains `Parent string
   json:"parent,omitempty"` (the name only), written by the head alongside
   `Deferred`. Restore relaunches sessions one at a time, so a child may come
   back before its parent. Restore therefore relinks only *after* every
   selected session has been relaunched: for each restored session with a
   `Parent` that was also restored, it sets `@claudemux_parent` and stamps
   `@claudemux_parent_created` with the restored parent's new creation time.
   A parent that wasn't restored leaves the child an orphan with the `←` tag.
   After that, links are ordinary and the guard applies unchanged; there is
   no permanent exemption.
10. **Name column:** the indent comes out of the name column (`swNameColW`),
    which keeps the topic column aligned across every row. A deep child's
    name truncates with `…` as long names already do.

## Non-goals

- Changing the conductor's dispatch order, or making it family-aware.
- Collapsing or expanding families, or new keys for them.
- Re-parenting or unlinking by hand.
- Linking sessions that weren't launched by `claudemux` from a Claude pane
  (e.g. slack-agent's `remix-2` defer reason is not a link).
- Changing the handoff skill. It already launches with `claudemux -n -d`,
  which is all the launcher needs.

## Anti-goals

- A family split across groups again, i.e. the parent alone under the deferred rule.
- Any column drift: indentation must not shear the topic/state grid.
- The `n` key or a manual shell launch silently adopting a parent.

## Separate, already-diagnosed fix (not part of this design)

iai-admin did not defer itself because `~/go/bin/claudemux-head` (and the
`~/.claude/skills/claudemux-handoff/SKILL.md` it installs) dates from
2026-10-06, before PR #22 added the defer step. Re-running
`go install ./cmd/claudemux-head` from the main checkout ships it.

## Left to the builder

- Exact glyphs, and whether the tree lines use the dim status style.
- Where the parent lookup lives (in `buildSwSnapshot` or a helper next to
  `swSortSessions`) and the data shape (a `Parent`/`Depth`/`Prefix` on
  `swSession`).
- Launcher plumbing: where in `create_session` the two options are set.
- Tests: table tests in the existing `switchboard_test.go` /
  `switchboardtui_test.go` style; the launcher detection gets a
  `launchertargets_test.go`-style test if one is practical.
