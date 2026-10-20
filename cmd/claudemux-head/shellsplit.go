package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"
)

// defaultShellSplitSize is how wide `t`'s shell pane is when neither the
// project nor config.yml says otherwise — the same 30% bin/claudemux gives the
// shell-right layout's shell, so a pane opened later matches one opened at
// launch.
const defaultShellSplitSize = "30%"

// shellSplitMsg reports how a `t` split went, for the chip.
type shellSplitMsg struct{ note string }

// shellSplitSize resolves the width of `t`'s shell pane with bin/claudemux's
// shell_size_for precedence: the project's .claudemux.yml shell_size, then
// launch.shell_size, then the default. An illegal value at either level is
// skipped rather than handed to tmux, where it would fail the split.
func shellSplitSize(configPath, launchSize string) string {
	if size := projectDeclaredShellSize(configPath); legalShellSize.MatchString(size) {
		return size
	}
	if legalShellSize.MatchString(launchSize) {
		return launchSize
	}
	return defaultShellSplitSize
}

// projectDeclaredShellSize reads shell_size from a project config file, "" when
// the file is missing, unparseable or silent.
func projectDeclaredShellSize(configPath string) string {
	b, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	var meta struct {
		ShellSize string `yaml:"shell_size"`
	}
	if err := yaml.Unmarshal(b, &meta); err != nil {
		return ""
	}
	return strings.TrimSpace(meta.ShellSize)
}

// shellSplitArgs splits a shell off the right of pane, starting in dir. No
// command: the pane runs the user's default shell, and tmux focuses it. An
// empty dir omits -c, leaving tmux's own default.
func shellSplitArgs(pane, size, dir string) []string {
	args := []string{"split-window", "-h", "-l", size, "-t", pane}
	if dir != "" {
		args = append(args, "-c", dir)
	}
	return args
}

// splitShellCmd opens a shell pane to the right of the claude pane. The pane is
// resolved the way restartClaudeCmd resolves it: the pane map first, the last
// pane the poll saw as a fallback.
func splitShellCmd(selfPane, paneDir, lastPane, size string) tea.Cmd {
	return func() tea.Msg {
		pane := lastPane
		if selfPane != "" {
			if _, _, p, ok := mappedTranscript(selfPane, paneDir); ok && p != "" {
				pane = p
			}
		}
		if pane == "" {
			return shellSplitMsg{note: "no claude pane"}
		}
		// The claude pane's current path, so the shell lands wherever claude
		// is working now (a worktree it entered mid-session included) rather
		// than where the head was launched. Read here rather than passed as
		// `-c '#{pane_current_path}'`: split-window expands that format
		// against the ACTIVE pane — the head, since `t` was pressed in it —
		// not against -t.
		dirCtx, cancelDir := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		out, _ := exec.CommandContext(dirCtx, "tmux", "display-message", "-p", "-t", pane, "#{pane_current_path}").Output()
		cancelDir()
		dir := strings.TrimSpace(string(out))
		ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		defer cancel()
		if err := exec.CommandContext(ctx, "tmux", shellSplitArgs(pane, size, dir)...).Run(); err != nil {
			teardownLogf("shell-split pane=%s size=%s err=%v", pane, size, err)
			return shellSplitMsg{note: "shell split failed"}
		}
		return shellSplitMsg{}
	}
}

// shellSplitChip shows a failed split for teardownNoteTTL. A split that worked
// needs no chip: the new pane is the feedback, and it has the focus.
func shellSplitChip(note string, noteAt, now time.Time) string {
	if note != "" && now.Sub(noteAt) < teardownNoteTTL {
		return "▌ " + note
	}
	return ""
}
