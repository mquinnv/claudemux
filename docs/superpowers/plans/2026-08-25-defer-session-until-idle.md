# Defer a session until nothing else is waiting

## Goal
Let the user mark a session as *deferred* — it is waiting on an external blocker
and must not be forgotten, but the conductor should only escort the client to it
when NO other (non-deferred, un-snoozed) session is waiting. Busy sessions do not
block a deferred dispatch.

## Global Constraints
- The mark is the session-scoped tmux user option `@claudemux_defer`, value `1`
  when deferred; unset (or any other value) means normal. It lives on the tmux
  session so it survives head and lobby restarts.
- The lobby reads it only through the existing `swPollCmd` tmux calls (the lobby
  never touches the filesystem). Add `#{@claudemux_defer}` as the 9th
  tab-separated field of `sessOut`; `buildSwSnapshot` must accept exactly 9
  fields now (update every test fixture that emits sessOut lines).
- `swSession` gains `Deferred bool`.
- `waitingQueue` orders non-deferred waiters first, then deferred; within each
  group oldest `Since` first, name as tiebreak (as today). Snooze semantics are
  unchanged and apply to deferred sessions identically.
- Nothing else in `conductor.step` changes.
- Toggle key is `d` on both surfaces. Lobby: toggles the SELECTED row's session.
  Head: toggles its own session (`-t selfPane`, tmux resolves the owning session).
  Set = `tmux set-option -t <target> @claudemux_defer 1`; unset =
  `tmux set-option -t <target> -u @claudemux_defer`. Fire-and-forget with the
  usual 2s deadline, like every other tmux shell-out.
- Visuals: deferred must NOT be dim — it is a session that must not be
  forgotten. Use one new color, ANSI 256 `45` (cyan; `39` is already
  `swBusyStyle`'s "Thinking" blue), shared by both surfaces:
  `swDeferStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("45"))` and
  `swBadgeDeferStyle = Bold, Foreground "232", Background "45"`.
  - Lobby row: when `sess.Deferred`, the leading marker is `swDeferStyle.Render("◆ ")`
    (replacing the waiting dot / blank — deferred is shown whether or not the
    session is currently waiting), AND a ` DEFER ` badge (`swBadgeDeferStyle`)
    is appended after the model cell on line 1. Row text/state styling is
    otherwise untouched (no dimming).
  - Lobby status line: `conductor.statusLine` appends ` · N deferred` (count of
    sessions with Deferred && isWaiting) after the existing snoozed suffix,
    only when N > 0.
  - Lobby footer help gains `d defer` after `n new`.
  - Head chip: `deferChip(raw string) string` returns
    `swDeferStyle.Render("◆ defer")` when raw == "1", else "". Rendered right
    next to `conductChip` wherever it appears (both call sites in tui.go).
- The head reads `@claudemux_defer` on its existing poll tick (same place it
  reads `readConductOption`), stored on the model as `deferRaw`, and uses that
  read to decide the direction of a `d` toggle. No optimistic/pending state:
  a one-poll lag on the chip is fine.
- All new logic is pure and unit-tested without tmux: argv builders, queue
  ordering, snapshot parsing, chips/badges, status line. Follow existing test
  style in the same package.
- Run `go build ./... && go test ./...` before every commit. Do not run
  `go install`.

## Task 1: Option, snapshot, conductor
Files: `cmd/claudemux-head/deferpub.go` (new) + test, `switchboard.go` + test,
`swconductor.go` + test, and any test fixture producing sessOut lines.
- `deferpub.go`: `const deferOption = "@claudemux_defer"`;
  `deferArgs(target string, on bool) []string` building the set/unset argv above;
  `setDeferCmd(target string, on bool) tea.Cmd` (fire-and-forget, 2s deadline);
  `readDeferOption(ctx, target string) string` using
  `tmux show-option -t <target> -qv @claudemux_defer` (trimmed; "" on error);
  `deferChip(raw string) string` per the constraints; the two lipgloss styles.
- `switchboard.go`: 9th field in the sessOut format comment and in the
  `list-sessions -F` format string inside `swPollCmd`; `Deferred = f[8] == "1"`;
  `len(f) != 9` guard. Update fixtures.
- `swconductor.go`: `waitingQueue` ordering; `statusLine` deferred suffix.
- Tests: argv for set/unset; parse Deferred true/false/absent; queue —
  deferred waits behind a younger normal waiter; deferred dispatched when it is
  the only waiter; snoozed deferred excluded; two deferred ordered oldest-first;
  statusLine shows `· 1 deferred` only when a deferred session is waiting;
  deferChip on/off.

## Task 2: Lobby toggle and rendering
Files: `switchboardtui.go` + test.
- `d` key in the main (non-creating) key switch: if a row is selected, return
  `setDeferCmd(sess.Name, !sess.Deferred)`; no-op when the list is empty.
  Guard like the other row keys.
- Row marker + DEFER badge per constraints; footer text.
- Tests: rendered row contains `◆` and `DEFER` for a deferred session and not
  for a normal one; `d` produces a cmd (or none on empty list); footer text.

## Task 3: Head toggle and chip
Files: `tui.go` + test.
- `dataMsg.deferRaw` read on the poll tick beside `conductRaw` (only when
  `selfPane != ""`), copied to `model.deferRaw` where `conductRaw` is copied.
- `d` key: `return m, setDeferCmd(m.selfPane, m.deferRaw != "1")`; no-op when
  `selfPane == ""`.
- Render `deferChip(m.deferRaw)` adjacent to `conductChip` at both call sites,
  separated the same way neighbouring chips are.
- Tests: `d` yields a cmd inside tmux and nil outside; the chip appears in the
  rendered view when `deferRaw == "1"`.
