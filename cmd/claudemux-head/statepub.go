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
	case StateBackground:
		return "Background:" + strconv.Itoa(s.BgCount)
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
// machine form changed since the last publish — or when the state's Since
// moved to a new ANCHORED value under the same machine form. The second
// clause is what keeps @claudemux_state_since honest across value-identical
// transitions (Idle -> busy blip between polls -> Idle): the conductor's
// snooze matching and queue ordering key on _since, and a pinned stale value
// starves the session. Anchored-only, because an unanchored Since is a
// now-fallback that differs every tick — republishing on it would set a tmux
// option once a second for every session with an empty transcript.
func (m *model) maybePublishState(now time.Time) tea.Cmd {
	v := statePublishValue(m.state)
	if v == m.publishedState && (!m.state.Anchored || m.state.Since.Equal(m.publishedSince)) {
		return nil
	}
	m.publishedState = v
	m.publishedSince = m.state.Since
	return publishStateCmd(m.selfPane, m.state, now)
}

// Companion options to @claudemux_state: coarse per-session facts the lobby
// renders. Values are sanitized (no tabs/newlines, bounded length) because
// the lobby's snapshot parser is line- and tab-delimited.
const (
	infoContextOption = "@claudemux_context"
	infoSummaryOption = "@claudemux_summary"
	infoPromptOption  = "@claudemux_prompt"
	infoModelOption   = "@claudemux_model"
)

// infoValueMaxRunes bounds published summary/prompt text. 120 comfortably
// fills a lobby line and keeps `tmux list-sessions` output shell-friendly.
const infoValueMaxRunes = 120

func sanitizeOptionValue(s string) string {
	return truncateWords(collapseWhitespace(s), infoValueMaxRunes)
}

// publishOptionCmd sets one session option, fire-and-forget. An empty value
// is still published: a previously-published non-empty value (a summary that
// got cleared, a prompt that scrolled out) must be overwritable back to
// empty, and skipping the call whenever value is "" would leave tmux holding
// the stale non-empty one forever.
func publishOptionCmd(selfPane, option, value string) tea.Cmd {
	if selfPane == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "tmux", "set-option", "-t", selfPane, option, value).Run()
		return nil
	}
}

// maybePublishInfo returns publish cmds for every info option whose value
// changed since its last publish — usually none, occasionally one. Change
// guards live per option so a chatty value (context) cannot force republishes
// of quiet ones (summary).
func (m *model) maybePublishInfo() []tea.Cmd {
	if m.selfPane == "" {
		return nil
	}
	var cmds []tea.Cmd
	if pct := int(m.contextPct); pct != m.publishedContext {
		m.publishedContext = pct
		cmds = append(cmds, publishOptionCmd(m.selfPane, infoContextOption, strconv.Itoa(pct)))
	}
	if s := sanitizeOptionValue(m.summary.Now); s != m.publishedSummary {
		m.publishedSummary = s
		cmds = append(cmds, publishOptionCmd(m.selfPane, infoSummaryOption, s))
	}
	if p := sanitizeOptionValue(m.lastTyped); p != m.publishedPrompt {
		m.publishedPrompt = p
		cmds = append(cmds, publishOptionCmd(m.selfPane, infoPromptOption, p))
	}
	// The raw model id, not shortModel's display form: the option is an
	// interface like @claudemux_state, and the lobby shortens at render time.
	if v := sanitizeOptionValue(m.modelName); v != m.publishedModel {
		m.publishedModel = v
		cmds = append(cmds, publishOptionCmd(m.selfPane, infoModelOption, v))
	}
	return cmds
}
