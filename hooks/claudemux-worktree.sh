#!/usr/bin/env bash
# Claude Code hook (UserPromptSubmit): ask the model to create a worktree named
# after the task, for sessions bin/claudemux marked as wanting one.
#
# claudemux no longer passes `claude --worktree` — that named the worktree
# during startup, before any prompt existed to name it from, which is how every
# worktree ended up called "lovely-wandering-lovelace". Instead the launcher
# exports CLAUDEMUX_WORKTREE_PENDING and this hook asks the model to make the
# worktree itself, on its first response, when the task IS known.
#
# UNLIKE claudemux-map.sh, this hook SPEAKS on stdout: UserPromptSubmit stdout
# is injected into the model's context, which is the whole delivery mechanism.
# That is why the two live in separate files — every path here that is not the
# one matching case must still print nothing.
set -euo pipefail

# No marker: this session was never promised a worktree (-W, a feature branch,
# a non-repo). Nothing to say, on this or any later prompt.
[ -n "${CLAUDEMUX_WORKTREE_PENDING:-}" ] || exit 0

payload="$(cat)"
cwd="$(printf '%s' "$payload" | jq -r '.cwd // empty' 2>/dev/null || true)"
[ -n "$cwd" ] || exit 0

# Already inside a worktree: the model made one on an earlier prompt (or the
# user entered one by hand). This is what makes the hook self-limiting — no
# state file, no once-per-session bookkeeping.
case "$cwd" in
  */.claude/worktrees/*) exit 0 ;;
esac

cat <<'INSTRUCTION'
<claudemux-worktree>
This session is configured to work in a git worktree, and is not in one yet.
Before any other tool call, call EnterWorktree with a `name` derived from the
user's request above: 2-5 words, lowercase, dash-separated, naming the durable
goal rather than the first step (e.g. "rename-worktrees-on-topic"). Use only
letters, digits, dots, underscores and dashes, up to 64 characters total. Then carry on with the request.
</claudemux-worktree>
INSTRUCTION
