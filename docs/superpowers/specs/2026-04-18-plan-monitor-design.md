# plan-monitor Design Spec

## Overview

A Go CLI tool that displays the active plan and tasks for a Claude Code session in a live-updating terminal UI. Runs in a separate terminal pane alongside Claude Code, giving real-time visibility into what Claude is working on.

## Data Sources

All data is read from `~/.claude/` — no hooks or custom state files needed.

### Session Discovery

- **Path encoding:** Claude Code encodes project paths by replacing `/` with `-`. Example: `/Users/michael/Projects/foo` → `-Users-michael-Projects-foo`
- **Sessions index:** `~/.claude/projects/<encoded-path>/sessions-index.json` contains an array of session entries:
  ```json
  {
    "version": 1,
    "entries": [
      {
        "sessionId": "uuid",
        "projectPath": "/absolute/path",
        "modified": "ISO-8601 string",
        "fileMtime": 1769098004061,
        "firstPrompt": "...",
        "summary": "...",
        "messageCount": 42,
        "gitBranch": "main"
      }
    ]
  }
  ```
- **Resolution:** Auto-detect from cwd by default. `--session <id>` flag overrides. When auto-detecting, pick the session with the most recent `modified` field (ISO-8601 string) or `fileMtime` (epoch ms) — whichever is available.

### Tasks

- **Location:** `~/.claude/tasks/<session-id>/*.json`
- **Format:** One JSON file per task, numbered by ID:
  ```json
  {
    "id": "1",
    "subject": "Explore project context",
    "description": "Check existing files, docs, recent commits",
    "activeForm": "Exploring project context",
    "status": "completed",
    "blocks": [],
    "blockedBy": []
  }
  ```
- **Statuses:** `pending`, `in_progress`, `completed`
- **activeForm:** Optional present-tense label shown when status is `in_progress`

### Plans

- **Location:** `~/.claude/plans/<plan-name>.md`
- **Discovery:** Plans are not directly linked to sessions. Two strategies:
  1. Grep the session JSONL (`~/.claude/projects/<encoded-path>/<session-id>.jsonl`) for `EnterPlanMode` tool calls to extract the plan name
  2. Fallback: show the most recently modified `.md` file in `~/.claude/plans/`
- **Format:** Plain markdown files with a `# Title` heading

## CLI Interface

```
plan-monitor [flags]

Flags:
  --session <id>    Use a specific session ID instead of auto-detecting
  --help            Show help
```

No subcommands. Launches directly into the live TUI. `q` or `Ctrl+C` to quit.

## TUI Layout

```
┌─ plan-monitor ── /Users/michael/Projects/foo ──────────┐
│                                                         │
│  Plan: Fix PR #310 Review Issues                        │
│  ───────────────────────────────────────────────────     │
│  <rendered plan markdown, scrollable>                   │
│                                                         │
│  Tasks (3/8 complete)                                   │
│  ───────────────────────────────────────────────────     │
│  ✓ Explore project context                              │
│  ✓ Ask clarifying questions                             │
│  ✓ Propose approaches                                   │
│  ⟳ Present design                                       │
│  ○ Write design doc                                     │
│  ○ Spec self-review                                     │
│  ○ User reviews spec                                    │
│  ○ Transition to implementation                         │
│                                                         │
│  Session: 160b6227 · Updated 2s ago                     │
└─────────────────────────────────────────────────────────┘
```

### Status Icons

| Status | Icon | Notes |
|--------|------|-------|
| `completed` | `✓` | Green |
| `in_progress` | `⟳` | Yellow, shows `activeForm` text if present |
| `pending` | `○` | Dim/gray |

### Behavior

- **Refresh:** Poll task files and plan file every 1 second
- **Scrolling:** Arrow keys scroll the plan section when content exceeds terminal height
- **Resize:** Adapts to terminal size changes
- **No plan state:** If no plan is found, the plan section is hidden — only tasks are shown
- **No tasks state:** If no tasks exist, show "No active tasks"
- **No session state:** If no session is found for cwd, show an error message and exit
- **Relative time:** "Updated Xs ago" in the footer, based on the most recently modified task file

## Project Structure

```
plan-monitor/
├── main.go           # entry point, flag parsing, session resolution
├── session.go        # path encoding, sessions-index parsing, session selection
├── tasks.go          # read and parse task JSON files
├── plan.go           # plan discovery from JSONL + fallback
├── tui.go            # bubbletea model, update, view
├── go.mod
└── go.sum
```

## Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/charmbracelet/bubbletea` | TUI framework (event loop, input, terminal management) |
| `github.com/charmbracelet/lipgloss` | Terminal styling (colors, borders, layout) |
| `github.com/charmbracelet/glamour` | Markdown rendering for plan content |

## Edge Cases

- **Session JSONL is large:** Only scan for `EnterPlanMode` — don't parse the full conversation. Use line-by-line scanning, not full file read.
- **Task files appear/disappear mid-run:** Re-read the directory listing on each poll cycle. Handle missing files gracefully.
- **Multiple plans referenced in one session:** Show the most recently referenced plan.
- **Lock file in tasks dir:** Ignore `.lock` file when listing tasks.
- **sessions-index.json missing or corrupt:** Fall back to finding sessions by listing JSONL files in the project directory and using filesystem mtime.
