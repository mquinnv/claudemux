#!/usr/bin/env bash
# Claude Code hook (SessionStart + UserPromptSubmit): record which session
# lives in which tmux pane so claudemux-head can follow its sibling pane's
# transcript. Keyed by $TMUX_PANE (inherited from the claude process).
#
# MUST stay silent on stdout: UserPromptSubmit stdout is injected into the
# model's context.
set -euo pipefail

[ -n "${TMUX_PANE:-}" ] || exit 0

dir="$HOME/.claude/claudemux/panes"
mkdir -p "$dir"

map="$(jq -c '{session_id: .session_id, transcript_path: .transcript_path, cwd: .cwd}' 2>/dev/null || true)"
[ -n "$map" ] || exit 0
# Only write when transcript_path is a non-empty string; discard jq -e's
# stdout (the matched value) since this script must stay silent on stdout.
printf '%s' "$map" | jq -e '.transcript_path | strings | length > 0' >/dev/null 2>&1 || exit 0

f="$dir/${TMUX_PANE#%}.json"
tmp="$f.tmp.$$"
printf '%s\n' "$map" > "$tmp"
mv "$tmp" "$f"

# Prune map files (and any orphaned .json.tmp.$$ files from a crashed run
# between the mktemp write and the mv) for panes that haven't written in a
# week (dead panes; pane ids restart when the tmux server restarts, so stale
# files can shadow).
find "$dir" -name '*.json*' -mtime +7 -delete 2>/dev/null || true
exit 0
