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
# MUST stay silent on stdout: UserPromptSubmit stdout is injected into the
# model's context.
set -euo pipefail

dir="$HOME/.claude/claudemux/asking"

payload="$(cat 2>/dev/null || true)"
[ -n "$payload" ] || exit 0

session_id="$(printf '%s' "$payload" | jq -r '.session_id | strings' 2>/dev/null || true)"
[ -n "$session_id" ] || exit 0
# session_id becomes a filename; refuse anything that could escape the dir.
case "$session_id" in
*/* | .*) exit 0 ;;
esac

event="$(printf '%s' "$payload" | jq -r '.hook_event_name | strings' 2>/dev/null || true)"
tool="$(printf '%s' "$payload" | jq -r '.tool_name | strings' 2>/dev/null || true)"

f="$dir/$session_id.json"

case "$event" in
PreToolUse)
    [ "$tool" = "AskUserQuestion" ] || exit 0
    mkdir -p "$dir"
    tmp="$f.tmp.$$"
    printf '%s\n' "{\"session_id\":\"$session_id\"}" > "$tmp"
    mv "$tmp" "$f"
    ;;
PostToolUse)
    [ "$tool" = "AskUserQuestion" ] || exit 0
    rm -f "$f"
    ;;
UserPromptSubmit)
    rm -f "$f"
    ;;
esac

# Prune markers (and orphaned .tmp.$$ files from a crashed run) for sessions
# that stopped writing long ago, same policy as the pane map.
find "$dir" -name '*.json*' -mtime +7 -delete 2>/dev/null || true
exit 0
