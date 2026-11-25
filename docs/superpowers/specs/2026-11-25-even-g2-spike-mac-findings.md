# Even G2 ring remote — Mac spike findings (dialogs, keys, hooks)

Spike items 2 and 3 of `2026-11-25-even-g2-ring-remote-design.md`, section 0.
Everything here was observed on 2026-11-25 in one scratch session; nothing is
taken from documentation.

- **Claude Code:** `2.1.331 (Claude Code)`, model shown as Opus 5.5.
- **tmux:** 3.8, private server (`tmux -L g2spike`), pane 120x40.
- **Session settings that affect the screen:** the user's global config has
  vim editing on (the footer shows `-- INSERT --`) and starts in auto mode.
  Permission and plan dialogs were captured in manual mode (footer
  `⏸ manual mode on`; hook `permission_mode: "default"`) and plan mode.
- **Keys** are written as `tmux send-keys` arguments. A digit was sent as
  `tmux send-keys -t <pane> 2` (no `-l`, no Enter).

Fixtures, redacted, in `cmd/claudemux-head/testdata/`:
`dialog_ask_single.txt`, `dialog_ask_multi.txt`,
`dialog_ask_two_questions.txt`, `dialog_permit_bash.txt`,
`dialog_permit_edit.txt`, `dialog_plan.txt`, and an `_ansi` copy of each
(`capture-pane -p -e`). Each is the 40-row pane while the dialog was up,
before any key was sent.

Redaction applied to the fixtures: header path → `~/project`, account tier
dropped, session URL → `session_X`, hook banner org names dropped (same as
`prompt_asking.txt`), `file://` link targets → `/tmp/proj/...`, plan-file
UUID zeroed. In `dialog_permit_bash*.txt` the directory in option 2 is a
**same-length** placeholder so the real two-line wrap of that row is kept.

## Summary

| Dialog | Digit N submits at once? | Minimal keys for option N | Escape | Footer has "Esc to cancel"? |
|---|---|---|---|---|
| AskUserQuestion, one single-select question | Yes, for the tool's own options | `N` | Declines; turn ends | Yes |
| Permission (Bash, Edit, Write) | Yes | `N` | Rejects the tool call; turn ends | Yes |
| Plan approval | Yes for 1 and 2; 3 only focuses a text row | `N` (1 or 2) | Rejects the plan; turn ends, stays in plan mode | **No** |

Sending Enter after the digit is wrong: the dialog is already gone and Enter
lands in the empty prompt.

## A. AskUserQuestion — one question, single-select

Fixture: `dialog_ask_single.txt`.

```
────────────────────────────────────────────── (full-width rule)
 ☐ Colour

Which colour do you choose?

❯ 1. Red
     Choose red
  2. Green
     Choose green
  3. Blue
     Choose blue
  4. Type something.
────────────────────────────────────────────── (full-width rule)
  5. Chat about this

Enter to select · ↑/↓ to navigate · Esc to cancel
```

| Item | Observed |
|---|---|
| Recognise | Last non-blank line is `Enter to select · ↑/↓ to navigate · Esc to cancel`. Header line is ` ☐ <header>` with **no** `←`/`→` and no `✔ Submit`. |
| Option rows | `N. <label>` at column 2 (cursor row has `❯` at column 0), description on the next line indented 5. Tool options are 1..K in the order of `tool_input.questions[0].options`. |
| Extra rows | `K+1. Type something.` (free text) directly after the last option; then a rule; then `K+2. Chat about this`. With 3 options these were 4 and 5; with 2 options (existing `prompt_asking.txt`) 3 and 4. |
| Choose option N (1..K) | `N`. Submits immediately. Confirmed: `2` → transcript line `· Which colour do you choose? → Green`, model replied "You picked Green", and PostToolUse carried `"answers": {"Which colour do you choose?": "Green"}`. |
| Alternative | `Down` × (N−1), then `Enter`. Confirmed: `Down Down Enter` → Blue. `Down` alone only moves `❯`. |
| Digit K+1 | Does **not** submit. It moves the cursor to `Type something.` and the footer becomes `-- INSERT -- · Enter to select · ↑/↓ to navigate · ctrl+g to edit in Vim`. |
| Digit K+2 | Not tried. |
| Escape | From an option row: one `Escape` closes the dialog. Transcript shows `Declined to answer Claude's questions:`, the turn ends, the prompt is empty. From the `Type something.` row (with vim editing on): the first `Escape` only leaves insert mode (footer regains `· Esc to cancel`); a second `Escape` declines. |
| ANSI cue | In the `_ansi` capture the free-text row `4. Type something.` is all grey (`38;5;246`); real options are default colour. |

## B. AskUserQuestion — shapes to refuse

Captured only; keys not mapped.

**multiSelect: true** (`dialog_ask_multi.txt`):

```
←  ☐ Toppings  ✔ Submit  →

Which toppings do you want?

❯ 1. [ ] Cheese
         Add cheese
  2. [ ] Olives
         Add olives
  3. [ ] Type something
     Submit
────────────────
  4. Chat about this

Enter to select · ↑/↓ to navigate · Esc to cancel
```

**Two questions in one call** (`dialog_ask_two_questions.txt`):

```
←  ☐ Size  ☐ Shape  ✔ Submit  →

Which size do you want?

❯ 1. Small
     Small size
  2. Large
     Large size
  3. Type something.
────────────────
  4. Chat about this

Enter to select · Tab/Arrow keys to navigate · Esc to cancel
```

How to tell them from the answerable case, from the pane alone:

- Both have a tab bar: the header line starts with `←` and contains
  `✔ Submit` and ends with `→`. The single-question single-select dialog has
  neither.
- multiSelect rows carry `[ ]` after the number, and there is a bare
  `Submit` row with no number.
- The two-question footer says `Tab/Arrow keys to navigate` instead of
  `↑/↓ to navigate`.

The hook payload is the more reliable test: `len(questions) == 1 &&
!questions[0].multiSelect`.

Escape closed both in one press.

## C. Permission prompts

Mode: manual (`⏸ manual mode on`). In auto mode none of these prompt.
`curl -sI https://example.com` did not prompt (already allowed by the
user's own rules).

**Bash** (`dialog_permit_bash.txt`, command `touch spike-marker.txt`; the
same list appeared for `rm spike-marker.txt`):

```
────────────────────────────────────────────── (full-width rule)
 Bash command
 Tip: auto mode handles these prompts for you — choose "switch to auto mode" below
 Create empty marker file
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 touch spike-marker.txt
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and always allow access to <cwd, long enough here to wrap> from this project
   3. Yes, and switch to auto mode · auto mode handles these prompts for you
   4. No

 Esc to cancel · Tab to amend
```

**Edit** (`dialog_permit_edit.txt`):

```
 Edit file
 notes.txt
╌╌╌╌
 1 -hello
 1 +goodbye
╌╌╌╌
 Do you want to make this edit to notes.txt?
 ❯ 1. Yes
   2. Yes, and switch to accept edits (auto-approve file edits and common file commands) for this session (shift+tab)
   3. No

 Esc to cancel · Tab to amend
```

**Write** of a new file (captured, not kept as a fixture): title
`Create file`, question `Do you want to create new.txt?`, the same three
choices as Edit.

| Item | Observed |
|---|---|
| Recognise | Last non-blank line is ` Esc to cancel · Tab to amend`. A question line (`Do you want to proceed?`, `Do you want to make this edit to <file>?`, `Do you want to create <file>?`) sits directly above the numbered rows. Title line under the top rule: `Bash command`, `Edit file`, `Create file`. |
| Option rows | `N. <text>` at column 3; cursor row has `❯` at column 1. All rows are one indent deeper than AskUserQuestion's. |
| Bash choices, verbatim | `1. Yes` / `2. Yes, and always allow access to <dir> from this project` / `3. Yes, and switch to auto mode · auto mode handles these prompts for you` / `4. No` |
| Edit and Write choices, verbatim | `1. Yes` / `2. Yes, and switch to accept edits (auto-approve file edits and common file commands) for this session (shift+tab)` / `3. No` |
| "Don't ask again" choice | Bash: option 2 (a directory grant, for these two file commands) and option 3 (changes the session to auto mode). Edit/Write: option 2 (changes the session to accept-edits). No choice was worded "don't ask again". |
| Free-text choice | None is shown by default. `Tab` on the focused row turns it into a text row: on row 1 it reads `1. Yes, and tell Claude what to do next`; on the last row `4. No, and tell Claude what to do differently`. The footer then drops `· Tab to amend` and reads ` Esc to cancel`. Moving the cursor off the row restores the plain label. |
| Choose option N | `N`. Submits immediately. Confirmed: `1` on Bash ran the command (file created); `3` on Edit gave `Rejected update to notes.txt` and the file was unchanged; `1` on Write created the file. Bash options 2, 3 and Edit option 2 were not pressed (they change session state). |
| Escape | One `Escape` rejects the call: transcript `Interrupted · What should Claude do instead?`, turn ends, empty prompt. Also one press from the amended (`No, and tell Claude…`) row. |
| Wrapping | A long option wraps onto a continuation line indented 6 with no number (option 2 in the Bash fixture). The wrap is hard, mid-token: `…-970b` / `-b3963…`. |
| Count varies | Bash had 4 choices, Edit/Write 3. "No" is the last row in all three, not a fixed number. |

## D. Plan approval

Fixture: `dialog_plan.txt`. Session put in plan mode with `BTab` (the cycle
observed: auto → manual → accept edits → plan → auto; footer text
`⏸ plan mode on (shift+tab to cycle)`).

```
  ──────────────────────────────────────────── (rule, indented 2)
   Ready to code?

   Here is Claude's plan:
  ╌╌╌╌
   <plan text>
  ╌╌╌╌
   (blank rows: the dialog filled the whole 40-row pane)
  ────────────────────────────────────────────
   Claude has written up a plan and is ready to execute. Would you like to proceed?

   ❯ 1. Yes, and use auto mode
     2. Yes, manually approve edits
     3. Tell Claude what to change
        shift+tab to approve with this feedback

   ctrl+g to edit in Vim · ~/.claude/plans/<slug>-<session-id>.md
```

| Item | Observed |
|---|---|
| Recognise | `Ready to code?` near the top and `Claude has written up a plan and is ready to execute. Would you like to proceed?` above the numbered rows. Last non-blank line starts `ctrl+g to edit in Vim · ` followed by the plan file path. |
| "Esc to cancel" in the footer | **Not present.** `permissionDialogUp` returns false for this capture. |
| Choices, verbatim | `1. Yes, and use auto mode` / `2. Yes, manually approve edits` / `3. Tell Claude what to change` (with a hint line under it: `shift+tab to approve with this feedback`) |
| Option rows | `N. <text>` at column 5; cursor `❯` at column 3. The whole dialog is indented 2 more than a permission dialog. |
| Choose option N | `2` submitted immediately: mode became manual (`⏸ manual mode on`), PostToolUse fired, the model continued. `1` was not pressed (it would switch the session to auto mode); by its position and styling it is an ordinary choice. |
| Digit 3 | Does not submit. Moves the cursor to the text row. |
| Free-text choice | Option 3. Grey in the `_ansi` capture, like `Type something.` |
| Escape | One `Escape` rejects, from row 1 and from row 3 alike: transcript `Rejected Claude's plan:` with the plan boxed, turn ends, session stays in plan mode. |
| Layout | With a short plan the dialog still takes the full pane height, with a block of blank rows between the plan and the question. The choices were on the last 8 rows. |
| First choice depends on the session | Option 1 names auto mode because this account has auto mode. Read the rows; do not assume the labels. |

## E. Idle, for comparison

Bottom of the pane when a turn has finished (not kept as a fixture; the
existing `prompt_empty.txt` covers it):

```
✻ Cogitated for 3s · done 1:51 PM
                                         Update available · run brew upgrade claude-code@latest
──────────────────────────────────────────────────────────────
❯ 
──────────────────────────────────────────────────────────────

  -- INSERT -- ⏵⏵ auto mode on (shift+tab to cycle)
```

The prompt box (`❯ ` between two rules) and the mode footer are absent in
all three dialogs. After a turn the prompt sometimes holds a dim suggested
next prompt instead of being empty.

## Folder-trust dialog (seen at startup)

Not numbered; default is the refusing choice.

```
 ❯ No, exit
   Yes, I trust this folder

 Enter to confirm · Esc to cancel
```

`Down` then `Enter` accepted it. A digit was not tried.

## Hooks

Project hooks logged PreToolUse and PostToolUse for
`AskUserQuestion|ExitPlanMode` only, plus PermissionRequest, Notification,
UserPromptSubmit and Stop for everything. So Bash/Edit/Write rows below
show no PreToolUse/PostToolUse because none was registered, not because
none fired.

**Direct answers to the spike questions**

- `PermissionRequest` **fires for `ExitPlanMode`**, with
  `permission_mode: "plan"` and the whole plan in `tool_input.plan`.
- `PermissionRequest` **also fires for `AskUserQuestion`**, right after its
  PreToolUse, with the same `tool_input`. A hook that treats every
  PermissionRequest as "permission dialog up" will count question dialogs
  too.
- `Notification` with `notification_type: "permission_prompt"` fired for all
  three dialog kinds, but only when the dialog stayed up: it was logged for
  dialogs left up about 6s or more and was absent for the ones answered
  within about 4s. The payloads carry no timestamp, so the exact delay was
  not measured.

**Order per experiment**

| Experiment | While the dialog was up | After the answer |
|---|---|---|
| A, answered with `2` | UserPromptSubmit → PreToolUse(AskUserQuestion) → PermissionRequest(AskUserQuestion) | PostToolUse(AskUserQuestion) → Stop |
| A, left up ~12s, then `Escape` | UserPromptSubmit → PreToolUse → PermissionRequest → Notification | nothing: no PostToolUse, no Stop |
| A, `Down Down Enter` | UserPromptSubmit → PreToolUse → PermissionRequest | PostToolUse → Stop |
| B, multi-select, `Escape` | UserPromptSubmit → PreToolUse → PermissionRequest | nothing |
| B, two questions, `Escape` | UserPromptSubmit → PreToolUse → PermissionRequest | nothing |
| C, Bash, left up ~8s, then `1` | UserPromptSubmit → PermissionRequest(Bash) → Notification | Stop |
| C, Bash, `Escape` | UserPromptSubmit → PermissionRequest(Bash) | nothing |
| C, Edit, `3` (No) | UserPromptSubmit → PermissionRequest(Edit) | nothing |
| C, Write, `1` | UserPromptSubmit → PermissionRequest(Write) | Stop |
| D, left up ~6s, then `3`, `Escape` | UserPromptSubmit → PreToolUse(ExitPlanMode) → PermissionRequest(ExitPlanMode) → Notification | nothing |
| D, `Escape` from row 1 | UserPromptSubmit → PreToolUse → PermissionRequest | nothing |
| D, `2` | UserPromptSubmit → PreToolUse → PermissionRequest | PostToolUse(ExitPlanMode, `permission_mode: "default"`) → Stop |

Every refusal (Escape, or "No" by digit) ended the turn with **no Stop hook
and no PostToolUse**. The next event from the session was the following
UserPromptSubmit.

**Payloads** (ids and paths replaced; structure and key order as logged)

PreToolUse, AskUserQuestion:

```json
{"session_id": "00000000-0000-4000-8000-000000000001",
 "transcript_path": "/home/user/.claude/projects/-tmp-proj/00000000-0000-4000-8000-000000000001.jsonl",
 "cwd": "/tmp/proj",
 "scratchpad_dir": "/tmp/scratch/00000000-0000-4000-8000-000000000001/scratchpad",
 "prompt_id": "00000000-0000-4000-8000-000000000002",
 "permission_mode": "auto",
 "effort": {"level": "medium"},
 "hook_event_name": "PreToolUse",
 "tool_name": "AskUserQuestion",
 "tool_input": {"questions": [{"question": "Which colour do you choose?", "header": "Colour",
   "options": [{"label": "Red", "description": "Choose red"},
               {"label": "Green", "description": "Choose green"},
               {"label": "Blue", "description": "Choose blue"}],
   "multiSelect": false}]},
 "tool_use_id": "toolu_000000000000000000000001"}
```

`tool_input` holds only the tool's own options. "Type something." and "Chat
about this" are added by the UI and are not in the payload.

PermissionRequest for the same call is identical except
`hook_event_name: "PermissionRequest"` and no `tool_use_id`.

PostToolUse, AskUserQuestion (after `2`), the parts that differ:

```json
{"hook_event_name": "PostToolUse", "tool_name": "AskUserQuestion",
 "tool_input": {"questions": [...], "answers": {"Which colour do you choose?": "Green"}, "annotations": {}},
 "tool_response": {"questions": [...], "answers": {"Which colour do you choose?": "Green"}, "annotations": {}},
 "tool_use_id": "toolu_000000000000000000000001", "duration_ms": 0}
```

PermissionRequest, Bash:

```json
{"session_id": "...", "transcript_path": "...", "cwd": "/tmp/proj", "scratchpad_dir": "...",
 "prompt_id": "...", "permission_mode": "default", "effort": {"level": "medium"},
 "hook_event_name": "PermissionRequest",
 "tool_name": "Bash",
 "tool_input": {"command": "touch spike-marker.txt", "description": "Create empty marker file"},
 "permission_suggestions": [
   {"type": "addDirectories", "directories": ["/tmp/proj"], "destination": "session"},
   {"type": "setMode", "mode": "acceptEdits", "destination": "session"}]}
```

PermissionRequest, ExitPlanMode:

```json
{"session_id": "...", "transcript_path": "...", "cwd": "/tmp/proj", "scratchpad_dir": "...",
 "prompt_id": "...", "permission_mode": "plan", "effort": {"level": "medium"},
 "hook_event_name": "PermissionRequest",
 "tool_name": "ExitPlanMode",
 "tool_input": {"plan": "# Add one line to README.md\n\n1. Enter a worktree (`readme-add-line`), then append a single line to the end of `README.md` (create the file if it doesn't exist).\n2. Verify with `tail -n 1 README.md` and `git diff README.md`, which should show exactly one added line.\n",
                "planFilePath": "/home/user/.claude/plans/structured-whistling-yeti-of-00000000-0000-4000-8000-000000000001.md"}}
```

PreToolUse for ExitPlanMode has the same `tool_input` plus `tool_use_id`.
PostToolUse for ExitPlanMode has `"tool_input": {}` and
`"tool_response": {"plan": "...", "isAgent": false, "filePath": "..."}`.

Notification, all of them:

```json
{"session_id": "...", "transcript_path": "...", "cwd": "/tmp/proj", "scratchpad_dir": "...",
 "prompt_id": "...", "hook_event_name": "Notification",
 "message": "Claude needs your permission", "notification_type": "permission_prompt"}
```

The message was `Claude needs your permission` for AskUserQuestion and for
Bash, and `Claude Code needs your approval for the plan` for ExitPlanMode.
`notification_type` was `permission_prompt` in all three. Notification
payloads have no `permission_mode` and no tool name.

## Surprising

1. **The plan dialog has no "Esc to cancel" footer.** The existing
   `permissionDialogUp` test does not see it. Its last line is
   `ctrl+g to edit in Vim · <plan path>`.
2. **PermissionRequest fires for AskUserQuestion**, so a question dialog
   also looks like a permission request to hooks.
3. **Refusing never fires Stop.** Escape, and "No" by digit, end the turn
   silently for all three dialogs.
4. **Digits on free-text rows only move the cursor** (`4` on
   `Type something.`, `3` on `Tell Claude what to change`). A sender that
   presses such a digit leaves the dialog up with a text field focused.
5. **Escape is two presses from AskUserQuestion's text row** when vim
   editing is on (first press leaves insert mode). One press everywhere
   else, including the permission dialog's amended row and the plan
   dialog's text row.
6. **Permission choices are not what older versions showed.** There is no
   "don't ask again for `<command>`" row and no default "No, and tell
   Claude what to do differently" row. "No" is plain, and the tell-Claude
   variants appear only after `Tab`.
7. **The dialog's choices and the hook's `permission_suggestions`
   disagree** for Bash: the pane offered "switch to auto mode", the payload
   suggested `setMode: acceptEdits`. The pane is the only source for what
   the numbers mean.
8. **A Bash choice wrapped** across two rows at 120 columns. A parser has
   to join continuation lines (indented, no `N.`) to the row above.
9. **A `Tip:` line** sits inside the Bash dialog between the title and the
   description. It is not part of the command.
10. **The trust dialog defaults to "No, exit"** and has no numbers.

## Not observed

- Digit `K+2` ("Chat about this") on AskUserQuestion: not pressed.
- Bash options 2 and 3, Edit/Write option 2, plan option 1: not pressed,
  because each changes session permissions or mode. Their numbering and
  wording are from the capture only.
- A Bash prompt whose option 2 is a command-prefix rule ("always allow
  `<cmd>`"): not seen. Both commands that prompted (`touch`, `rm`) got the
  directory grant. `curl -sI https://example.com` was tried and ran without
  a prompt.
- MCP tool and WebFetch permission dialogs: not tried.
- The exact delay before `Notification` fires: not measured.
- Behaviour with vim editing off: not tried (the user's config has it on).
- Pane sizes other than 120x40: not tried.
