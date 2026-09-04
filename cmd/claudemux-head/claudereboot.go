package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Restarting claude from the head: `c` arms, then `c` resumes the session the
// head is following (`claude --resume <id>`) or `n` starts a fresh one. Either
// way the claude pane is respawned with the command bin/claudemux launched it
// with, so the permission mode, project name, color prompt and worktree
// marker all come back exactly as they were.
//
// This is a respawn-pane -k: claude is killed, not asked to exit. The
// transcript is written as the session goes, so a resume after a kill loses
// nothing that had landed — and a kill is the only thing that works on the
// hung claude that is the usual reason to want this.

// claudeCmdOption is the tmux pane option bin/claudemux sets on the claude
// pane at launch, holding the command verbatim. The pane option, not the
// session's: a `-n` clone gets its own pane and its own command.
const claudeCmdOption = "@claudemux_claude_cmd"

// claudeRestartMsg reports the outcome of a respawn. note is rendered on the
// chip either way — a success says what was started, a failure says why not.
type claudeRestartMsg struct{ note string }

// claudeCommandFromStart recovers the launcher's command from tmux's
// #{pane_start_command}, for panes launched before the launcher recorded it
// as an option.
//
// tmux stores a pane's argv and prints it back re-quoted by its own
// stringifier: the launcher's single `exec <cmd>` argument comes back wrapped
// in double quotes with backslashes and quotes escaped (`/color\ purple`
// becomes `/color\\ purple`), or in single quotes when nothing inside needed
// escaping. The `exec ` prefix is run_in_pane's, not the command's. A pane
// still held by `claudemux-head boot` reports the holder; the command it was
// handed after `--` is what actually runs, so that is what a restart wants.
func claudeCommandFromStart(start string) string {
	s := strings.TrimSpace(start)
	if len(s) >= 2 {
		switch {
		case s[0] == '"' && s[len(s)-1] == '"':
			s = strings.NewReplacer(`\\`, `\`, `\"`, `"`).Replace(s[1 : len(s)-1])
		case s[0] == '\'' && s[len(s)-1] == '\'':
			s = s[1 : len(s)-1]
		}
	}
	s = strings.TrimPrefix(s, "exec ")
	if fields := strings.Fields(s); len(fields) >= 2 &&
		filepath.Base(fields[0]) == "claudemux-head" && fields[1] == "boot" {
		if _, after, ok := strings.Cut(s, " -- "); ok {
			s = after
		}
	}
	return strings.TrimSpace(s)
}

// resumeIDOK reports whether id is safe to splice into the respawn line
// unquoted. Session ids are UUIDs; anything outside that alphabet is not one.
func resumeIDOK(id string) bool {
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// respawnArgs builds the tmux call that replaces pane's program with cmd,
// resuming resumeID when it is non-empty.
//
// -k kills whatever is running there first; without it tmux refuses to
// respawn a live pane, and a hung claude is exactly the case this exists for.
// No -c: the pane keeps the working directory it was created with — the
// launch dir — the same choice bin/claudemux's run_in_pane makes. `exec`
// for the same reason it is there too: pane_current_command has to name
// claude, not the shell tmux wraps the command in.
//
// --resume goes LAST. The launcher's command may end in a positional prompt
// (`/color X`), and flags after it are still flags; the flag is never inserted
// in front of that prompt, where an optional-value flag could swallow it.
func respawnArgs(pane, cmd, resumeID string) ([]string, bool) {
	if pane == "" || cmd == "" || !resumeIDOK(resumeID) {
		return nil, false
	}
	line := "exec " + cmd
	if resumeID != "" {
		line += " --resume " + resumeID
	}
	return []string{"respawn-pane", "-k", "-t", pane, line}, true
}

// claudeRestartChip renders the restart ladder's chip: the armed prompt,
// naming the keys that commit, or a recent outcome note. The note shares
// teardownNoteTTL so the two ladders' notes linger for the same time.
func claudeRestartChip(armed, canResume bool, note string, noteAt, now time.Time) string {
	if armed {
		if canResume {
			return "↻ restart claude? c resume · n new"
		}
		return "↻ restart claude? n new"
	}
	if note != "" && now.Sub(noteAt) < teardownNoteTTL {
		return "↻ " + note
	}
	return ""
}

// readClaudeCommand returns the command to respawn pane with: the launcher's
// recorded option, or, for a pane launched before the launcher recorded one,
// the command tmux itself remembers starting the pane with.
func readClaudeCommand(pane string) string {
	optCtx, cancelOpt := context.WithTimeout(context.Background(), teardownTmuxTimeout)
	out, err := exec.CommandContext(optCtx, "tmux", "show-option", "-t", pane, "-pqv", claudeCmdOption).Output()
	cancelOpt()
	if err == nil {
		if cmd := strings.TrimSpace(string(out)); cmd != "" {
			return cmd
		}
	}
	startCtx, cancelStart := context.WithTimeout(context.Background(), teardownTmuxTimeout)
	defer cancelStart()
	out, err = exec.CommandContext(startCtx, "tmux", "display-message", "-p", "-t", pane, "#{pane_start_command}").Output()
	if err != nil {
		return ""
	}
	return claudeCommandFromStart(string(out))
}

// restartClaudeCmd respawns the session's claude pane, resuming resumeID
// when it is non-empty.
//
// The pane is resolved at fire time, the way teardownSendCmd does, so it is
// the pane whose transcript the head follows now. lastPane is the fallback
// for the one case that resolution cannot cover: a claude that has already
// exited. listPanes drops dead panes, so a crashed claude — kept on screen by
// remain-on-exit=failed — has no live candidate, but its pane is still there
// to respawn into, and the head's last sighting of it is the right one.
func restartClaudeCmd(selfPane, paneDir, lastPane, resumeID string) tea.Cmd {
	return func() tea.Msg {
		pane := lastPane
		if selfPane != "" {
			if _, _, p, ok := mappedTranscript(selfPane, paneDir); ok && p != "" {
				pane = p
			}
		}
		if pane == "" {
			return claudeRestartMsg{note: "no claude pane"}
		}
		cmd := readClaudeCommand(pane)
		if cmd == "" {
			return claudeRestartMsg{note: "no launch command for the claude pane"}
		}
		args, ok := respawnArgs(pane, cmd, resumeID)
		if !ok {
			return claudeRestartMsg{note: "restart refused"}
		}
		teardownLogf("claude-restart pane=%s resume=%q cmd=%q", pane, resumeID, cmd)
		ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		defer cancel()
		if err := exec.CommandContext(ctx, "tmux", args...).Run(); err != nil {
			return claudeRestartMsg{note: "restart failed"}
		}
		if resumeID != "" {
			return claudeRestartMsg{note: "resuming claude…"}
		}
		return claudeRestartMsg{note: "restarting claude…"}
	}
}
