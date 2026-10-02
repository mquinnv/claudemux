#!/usr/bin/env bash
# Claude Code hook (PreToolUse + PostToolUse + UserPromptSubmit): track pending
# AskUserQuestion calls so claudemux-head can tell "asking the human" apart
# from Idle/Thinking. Claude Code does NOT flush the assistant tool_use event
# to the transcript while a question is on screen — it lands only when the
# question is answered — so the transcript alone reads as Idle or Thinking for
# the whole time the session is blocked on the user. This hook is the only
# signal that fires at ask time.
#
# PreToolUse on AskUserQuestion writes a marker keyed by session_id;
# PostToolUse on AskUserQuestion (the answer arrived) removes it, and so does
# UserPromptSubmit (a new prompt means no question is pending — this covers an
# Esc'd question, where PostToolUse never fires). The head additionally
# ignores markers older than the transcript's newest conversation event, so a
# marker this script failed to remove cannot wedge the state.
#
# hook.go registers PreToolUse/PostToolUse with a matcher on AskUserQuestion,
# so this process only spawns for that tool to begin with; the tool-name
# check below is defense in depth (e.g. a hand-edited settings.json), not the
# primary filter.
#
# PermissionRequest covers the other dialog that blocks on the human: "Do you
# want to proceed?" for any tool. There the tool_use IS already in the
# transcript and reads as a tool that is running. The event fires only when a
# dialog is about to open, so it needs no matcher. It writes a second marker,
# <session_id>.permit.json, naming the tool. Nothing here removes that marker
# when the dialog is answered — no hook fires on approval — so the head
# confirms against the claude pane that the dialog is still up (permit.go);
# the marker only tells it when to look. UserPromptSubmit clears it.
#
# MUST stay silent on stdout: UserPromptSubmit stdout is injected into the
# model's context, and JSON on PermissionRequest's stdout can answer the
# dialog.
set -euo pipefail

# No sibling pane can be reading this session's marker outside tmux — same
# reasoning, and same line position, as claudemux-map.sh.
[ -n "${TMUX_PANE:-}" ] || exit 0

dir="$HOME/.claude/claudemux/asking"

payload="$(cat 2>/dev/null || true)"
[ -n "$payload" ] || exit 0

# One jq call instead of three: this hook still runs on every AskUserQuestion
# PreToolUse/PostToolUse plus every UserPromptSubmit, and each invocation was
# a separate process spawn. `|| true` covers both "not valid JSON" and "jq is
# not on PATH" — either way $fields comes back empty and every check below
# exits 0 without acting.
#
# Joined with \x1f (ASCII unit separator), not @tsv/tab: tool_name is absent
# on UserPromptSubmit, so the middle field is empty, and `read` with IFS set
# to a whitespace character (tab, like space and newline) collapses adjacent
# delimiters and eats that empty field — event="UserPromptSubmit",
# tool="sess-1", session_id="" instead of the intended split. \x1f is not
# IFS whitespace, so empty fields survive.
fields="$(printf '%s' "$payload" | jq -r '[.hook_event_name // "", .tool_name // "", .session_id // ""] | join("\u001f")' 2>/dev/null || true)"
[ -n "$fields" ] || exit 0
IFS=$'\x1f' read -r event tool session_id <<<"$fields"

[ -n "$session_id" ] || exit 0
# session_id becomes a filename; refuse anything that could escape the dir.
case "$session_id" in
*/* | .*) exit 0 ;;
esac

f="$dir/$session_id.json"
permit="$dir/$session_id.permit.json"

case "$event" in
PreToolUse)
    [ "$tool" = "AskUserQuestion" ] || exit 0
    mkdir -p "$dir"
    tmp="$f.tmp.$$"
    # jq -n instead of string interpolation: a session_id containing a quote
    # or backslash would otherwise land in the file as invalid JSON.
    jq -n --arg sid "$session_id" '{session_id: $sid}' > "$tmp"
    mv "$tmp" "$f"
    ;;
PostToolUse)
    [ "$tool" = "AskUserQuestion" ] || exit 0
    rm -f "$f"
    ;;
PermissionRequest)
    # AskUserQuestion has its own marker above; one dialog, one name.
    [ "$tool" != "AskUserQuestion" ] || exit 0
    mkdir -p "$dir"
    tmp="$permit.tmp.$$"
    jq -n --arg sid "$session_id" --arg tool "$tool" '{session_id: $sid, tool_name: $tool}' > "$tmp"
    mv "$tmp" "$permit"
    ;;
UserPromptSubmit)
    rm -f "$f" "$permit"
    ;;
esac

# Prune markers (and orphaned .tmp.$$ files from a crashed run) for sessions
# that stopped writing long ago, same policy as the pane map.
find "$dir" -name '*.json*' -mtime +7 -delete 2>/dev/null || true
exit 0
