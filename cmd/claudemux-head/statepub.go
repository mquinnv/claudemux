package main

import (
	"context"
	"os/exec"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Session user options the head publishes for external tooling (the
// switchboard). @claudemux_state holds the machine form of the current state
// (statePublishValue); @claudemux_state_since its start as unix seconds.
const (
	statePublishOption      = "@claudemux_state"
	statePublishSinceOption = "@claudemux_state_since"
)

// statePublishValue is the machine form of s for the @claudemux_state option:
// the kind name, with tool states carrying the tool ("Tool:AskUserQuestion").
// Consumers key on exact values — see isWaiting — so this string is an
// interface, not display text; Label() stays free to change independently.
func statePublishValue(s State) string {
	switch s.Kind {
	case StateIdle:
		return "Idle"
	case StateThinking:
		return "Thinking"
	case StateTool:
		return "Tool:" + s.ToolName
	case StateAwaiting:
		return "Awaiting"
	case StateError:
		return "Error"
	case StateCompacting:
		return "Compacting"
	}
	return ""
}

// statePublishArgs builds one tmux invocation setting both options. `;` is a
// single argv element: tmux treats it as a command separator, so both options
// land in one subprocess. `-t` takes the pane id; tmux resolves the owning
// session for session-scoped options. ok=false outside tmux (selfPane empty)
// or with nothing to say.
func statePublishArgs(selfPane, value string, since time.Time) ([]string, bool) {
	if selfPane == "" || value == "" {
		return nil, false
	}
	return []string{
		"set-option", "-t", selfPane, statePublishOption, value,
		";",
		"set-option", "-t", selfPane, statePublishSinceOption, strconv.FormatInt(since.Unix(), 10),
	}, true
}

// publishStateCmd returns a tea.Cmd publishing s, or nil when there is nothing
// to do. Fire-and-forget with a hard deadline, like renameTabCmd: a wedged
// tmux must never block the TUI, and a failed publish just leaves the previous
// value for the next transition to overwrite.
func publishStateCmd(selfPane string, s State, now time.Time) tea.Cmd {
	since := s.Since
	if since.IsZero() {
		since = now
	}
	args, ok := statePublishArgs(selfPane, statePublishValue(s), since)
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

// maybePublishState returns a cmd publishing the current state when its
// machine form changed since the last publish, nil otherwise. Cheap enough to
// call every poll. Outside tmux the cmd is nil every time — publishedState
// still updates, which is harmless: there is no consumer without tmux.
func (m *model) maybePublishState(now time.Time) tea.Cmd {
	v := statePublishValue(m.state)
	if v == m.publishedState {
		return nil
	}
	m.publishedState = v
	return publishStateCmd(m.selfPane, m.state, now)
}
