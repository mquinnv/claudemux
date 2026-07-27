package main

import (
	"context"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// tabTitleMaxRunes bounds the window name. The prompt asks Haiku for ≤32
// characters; the clamp sits looser so a modest overshoot lands intact instead
// of chopped ("tickets tutorial record…"). A model reply is still untrusted:
// enforce a ceiling here so an over-long or multi-line label can't turn the
// tmux status line into a mess. tmux treats the label as a literal (it's a
// single argv element, no shell), so this is tidiness, not safety.
const tabTitleMaxRunes = 40

// tabRenameArgs builds the `tmux` argument list to rename selfPane's window to
// tab, and reports whether it should run at all. It should not when we are not
// inside tmux (selfPane empty) or have no label — renaming a window to blank is
// worse than leaving it.
//
// The label is normalized: whitespace (including any newlines the model slipped
// in) is collapsed to single spaces, then it is truncated to tabTitleMaxRunes
// at a word boundary. A label that is empty after normalizing yields ok=false.
func tabRenameArgs(selfPane, tab string) ([]string, bool) {
	tab = truncateWords(collapseWhitespace(tab), tabTitleMaxRunes)
	if selfPane == "" || tab == "" {
		return nil, false
	}
	return []string{"rename-window", "-t", selfPane, tab}, true
}

// truncateWords clips s to at most max runes without cutting mid-word: the
// partial word at the cut is dropped and the elision marked with an ellipsis.
// A single word longer than max still rune-chops — there is no boundary to
// prefer over an empty label.
func truncateWords(s string, max int) string {
	if len([]rune(s)) <= max {
		return s
	}
	clipped := truncateRunes(s, max)
	clipped = strings.TrimSuffix(clipped, "…")
	if i := strings.LastIndex(clipped, " "); i > 0 {
		clipped = clipped[:i]
	}
	return strings.TrimRight(clipped, " ") + "…"
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
