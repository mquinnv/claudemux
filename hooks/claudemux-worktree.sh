#!/usr/bin/env bash
# Claude Code hook (UserPromptSubmit): ask the model to create a worktree named
# after the task, for sessions bin/claudemux marked as wanting one — and to
# create it no sooner than its first change.
#
# claudemux no longer passes `claude --worktree` — that named the worktree
# during startup, before any prompt existed to name it from, which is how every
# worktree ended up called "lovely-wandering-lovelace". Instead the launcher
# exports CLAUDEMUX_WORKTREE_PENDING and this hook asks the model to make the
# worktree itself, when the task IS known.
#
# WHEN the model makes it matters as much as what it is called. The first
# version of this instruction said "before any other tool call", and the model
# obeyed: every session got a worktree on its first response, including the
# many that only ever read code, answered a question, or investigated a bug and
# changed nothing. Those worktrees were pure cost — a branch and a directory to
# clean up afterwards. So the instruction now defers the call to the moment
# right before the first change that touches git state. Investigation happens
# in the current checkout; a session that never changes anything never gets a
# worktree.
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
# user entered one by hand). This is the cheap exit, and it covers the whole
# success path with no bookkeeping at all.
case "$cwd" in
  */.claude/worktrees/*) exit 0 ;;
esac

# The cwd check above only ends the injection when EnterWorktree SUCCEEDS.
# Every other path leaves the cwd where it was: the session is still reading
# and has not needed a worktree yet, the user answers "no, just work here", the
# tool refuses on a dirty tree, the model declines. Without a cap, this hook
# would re-inject the same instruction on every prompt for the rest of the
# session — nagging that the old `claude --worktree` never did, because a
# rejected flag failed once, loudly, at launch.
#
# So: say it at most maxAsks times per session. Two, not one, because a single
# statement is easy to lose and the second is cheap. Two is also enough: the
# instruction is a standing rule ("enter one right before your first change"),
# not a request that is consumed by being obeyed, and each injection stays in
# the transcript for the rest of the session, so a session that investigates
# for many prompts before its first edit still carries the rule when that edit
# comes. After two, the model has the rule twice and the human has watched the
# session run without a worktree; a third would only be noise.
#
# Keyed by session_id so a resumed or /clear'd session gets a fresh budget —
# that is a new task, and it deserves a new worktree. When no session_id is
# available there is nothing to key on, so fall through and ask: losing the
# feature is worse than an occasional extra ask.
max_asks=2
session="$(printf '%s' "$payload" | jq -r '.session_id // empty' 2>/dev/null || true)"
# ${HOME:-} rather than $HOME: `set -u` would abort the whole script on an unset
# HOME, and aborting is the one outcome worse than not counting.
if [ -n "$session" ] && [ -n "${HOME:-}" ]; then
  asks_dir="$HOME/.claude/claudemux/worktree-asks"
  mkdir -p "$asks_dir" 2>/dev/null || true
  # Prune counters for sessions that ended days ago, the same way the pane map
  # prunes its own files — this directory must not grow without bound.
  find "$asks_dir" -type f -mtime +7 -delete 2>/dev/null || true

  counter="$asks_dir/${session}"
  asked=0
  [ -f "$counter" ] && asked="$(cat "$counter" 2>/dev/null || echo 0)"
  case "$asked" in
    ''|*[!0-9]*) asked=0 ;;  # a corrupt counter must not disable the feature
  esac
  [ "$asked" -ge "$max_asks" ] && exit 0
  printf '%s' "$((asked + 1))" > "$counter" 2>/dev/null || true
fi

# The wording is deliberate on two points. "Do not enter one now" comes first
# because the model reads the tool name and reaches for it; the prohibition has
# to land before the tool does. And the trigger is "changes git state", not
# "edits a file": a new plan document or a commit is a change too, and a
# gitignored scratch file is not.
cat <<'INSTRUCTION'
<claudemux-worktree>
This session is configured to work in a git worktree, and is not in one yet.
Do not enter one now. Investigate, read, search, review, and answer in the
current checkout. Call EnterWorktree only immediately before your first action
that changes this repository's git state (editing a tracked file, adding a new
file such as a plan or design document, committing) -- and if the task never
changes anything, never call it. When you do, derive `name` from the user's
request: 2-5 words, lowercase, dash-separated, naming the durable goal rather
than the first step (e.g. "rename-worktrees-on-topic"). Use only letters,
digits, dots, underscores and dashes, up to 64 characters total.
</claudemux-worktree>
INSTRUCTION
