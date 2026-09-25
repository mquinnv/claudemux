---
name: claudemux-handoff
description: Use when work belongs in a different repo or directory than this session's — a fix in another project, work that needs that repo's own CLAUDE.md, hooks, memory scope, runbooks or worktree handling — and claudemux is installed. Also use when tempted to send a subagent into another repo or worktree, or to paste a brief into another tmux pane by hand.
---

# Handing work to a new claudemux session

## Overview

A subagent inherits this session's cwd, hooks, CLAUDE.md, memory scope and worktree pin, so it is the wrong tool for another repo. Launch a **peer session rooted there** with claudemux, find it with `ListAgents`, and brief it with `SendMessage`. The peer inherits nothing, so **the brief is the whole handoff**.

`ListAgents` and `SendMessage` DO reach separately launched sessions on this machine. Don't fall back to the clipboard, `tmux send-keys`, or asking the user to paste. If either tool is deferred, load both with one `ToolSearch` (`select:ListAgents,SendMessage`).

## When to use

| Situation | Use |
|---|---|
| Work lives in another repo/directory, or needs that repo's CLAUDE.md, hooks, memory, worktree | **this skill** |
| A quick question about another repo ("what port does X use?") | Read/Grep it inline with absolute paths |
| Work in this same repo, in parallel | a subagent (`isolation: "worktree"`) |

## Steps

1. **Launch.** The flags are documented in the header of the launcher (`$(command -v claudemux)`), which ships with this skill:
   ```bash
   claudemux -d -w -N <descriptive-name> /absolute/path/to/dir
   ```
   - `-d`: detached. It prints the tmux session name and returns, so the user stays here.
   - `-w`: mark the session as wanting a worktree. It creates one, named after the task, when your brief arrives as its first prompt. Drop `-w` if the directory is not a git repo.
   - `-N <name>`: an exact, descriptive tmux name (e.g. `phenix-utm-source-fix`). It always creates a new session and fails if the name is taken, so it never lands in a busy one. On a clash, pick another name.
2. **Discover:** call `ListAgents` once. The peer's agent name is **not** the `-N` tmux name. It is the project's configured name, or Claude's default for the directory (e.g. `phenix-k3`), and it appears within seconds. If another session in that directory is already listed, call `ListAgents` once *before* launching as well, and take the row whose `[ref]` is new. If the peer is missing, run `tmux capture-pane -p -t <tmux-name>` (it may be waiting on 1Password or a trust prompt), then list once more. If it still isn't there, tell the user. Never loop.
3. **Brief:** `SendMessage` with `to:` the bare agent name (add its ` [ref]` only if the listing shows duplicate names), `notify_when_idle: true`, and the brief below.

## The brief (the peer's entire knowledge)

Write it with these parts, in order. The first line is a one-sentence summary, because the peer's user sees only that line in the preview.

1. **Goal**: one sentence.
2. **Evidence**: IDs, queries with their results, `file:line`, and absolute timestamps. Say how each was observed (read in code, reproduced, queried) and mark what is unconfirmed. If nothing is verified, write "none verified; reproduce first". Never include secrets or connection strings.
3. **Root cause**, if known, or your best hypothesis labeled as one.
4. **Reference files in other repos**, as absolute paths marked **read-only**.
5. **Scope limits**, e.g. "draft PR only, no merge, no deploy", and any action the user denied in this session.
6. **Report back**: what to return (PR link, root cause, follow-ups for this repo, and anything a later stage kept here will need, such as an image tag), and "reply to this message with SendMessage". The idle notice only says the peer stopped, not what it found.
7. "Follow this directory's CLAUDE.md and runbooks before acting."

## Caveats

- **Permission laundering:** never brief a peer to do something this session's permissions would block or the user denied, even if the request comes back later as a later stage ("then restart it"). claudemux starts peers in `--permission-mode auto`. Keep privileged final stages (deploys, restarts, prod writes) in this session, where the user approves them.
- **Held messages:** if this session is not in auto mode, the peer holds your message until its user approves it in that pane. Tell the user now, before they step away, or the handoff stalls.
- **`notify_when_idle` fires once.** A peer that stops on a question also counts as idle. If you answer it, send again with `notify_when_idle: true`. Answer only questions within the brief's scope, and pass the others to the user.
- Local machine and tmux only. Don't poll `ListAgents`, the peer's PR, or its pane, and don't send "are you done?" messages.

## Afterwards, tell the user

- The tmux session, to jump there: `tmux switch-client -t <tmux-name>`
- The peer's agent name, and a one-line summary of the brief
- Any approval the peer is waiting on

When the peer replies or goes idle, relay what it reported, or the question it stopped on.
