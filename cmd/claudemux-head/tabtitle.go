package main

import (
	"context"
	"os/exec"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// tabTitleMaxRunes bounds the window name. The prompt asks Haiku for ≤24
// characters, but a model reply is untrusted: enforce it here so an over-long
// or multi-line label can't turn the tmux status line into a mess. tmux treats
// the label as a literal (it's a single argv element, no shell), so this is
// tidiness, not safety — but a promise the prompt makes should be kept.
const tabTitleMaxRunes = 24

// tabRenameArgs builds the `tmux` argument list to rename selfPane's window to
// tab, and reports whether it should run at all. It should not when we are not
// inside tmux (selfPane empty) or have no label — renaming a window to blank is
// worse than leaving it.
//
// The label is normalized: whitespace (including any newlines the model slipped
// in) is collapsed to single spaces, then it is truncated to tabTitleMaxRunes.
// A label that is empty after normalizing yields ok=false.
func tabRenameArgs(selfPane, tab string) ([]string, bool) {
	tab = truncateRunes(collapseWhitespace(tab), tabTitleMaxRunes)
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
