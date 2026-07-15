package main

import (
	"context"
	"os/exec"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// tabRenameArgs builds the `tmux` argument list to rename selfPane's window to
// tab, and reports whether it should run at all. It should not when we are not
// inside tmux (selfPane empty) or have no label — renaming a window to blank is
// worse than leaving it.
func tabRenameArgs(selfPane, tab string) ([]string, bool) {
	if selfPane == "" || tab == "" {
		return nil, false
	}
	return []string{"rename-window", "-t", selfPane, tab}, true
}

// renameTabCmd returns a tea.Cmd that renames the window, or nil when there is
// nothing to do (so callers append it unconditionally).
//
// The subprocess carries a hard deadline and its result is discarded: this runs
// off the poll loop exactly like panemap.go's tmux call, and a wedged tmux
// server must never block or crash the TUI. A failed rename simply leaves the
// previous title.
func renameTabCmd(selfPane, tab string) tea.Cmd {
	args, ok := tabRenameArgs(selfPane, tab)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "tmux", args...).Run()
		return nil
	}
}
