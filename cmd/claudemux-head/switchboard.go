package main

import (
	"strconv"
	"strings"
	"time"
)

// The switchboard: a lobby session that ferries the attached tmux client to
// claudemux sessions waiting on input. This file is the poll side — snapshot
// types and parsers for the three tmux listing calls. The conductor that
// consumes snapshots lives in swconductor.go; the TUI in switchboardtui.go.
// Design: docs/superpowers/specs/2026-08-09-switchboard-design.md.

// swHeadCommand identifies a claudemux session: some pane runs this binary.
// pane_current_command reports the executable basename.
const swHeadCommand = "claudemux-head"

type swSession struct {
	Name    string
	State   string    // raw @claudemux_state; "" when unset (pre-publish head)
	Since   time.Time // zero when unset or unparseable
	Context int       // context %, -1 when unset or unparseable
	Topic   string    // the head's Haiku tab label (the tmux window name)
	Summary string    // the head's one-line summary (@claudemux_summary)
	Prompt  string    // the last typed prompt (@claudemux_prompt)
	// ClaudePane is the tmux pane id running claude, "" when the session has
	// none. The lobby previews this pane rather than the session's active one:
	// a session left focused on its shell would preview a shell prompt, and
	// one left on its head pane would preview the four rows the lobby row
	// already summarizes.
	ClaudePane string
}

type swSnapshot struct {
	// Sessions holds live claudemux sessions — those with a claudemux-head
	// pane — excluding the lobby itself (whose own pane also runs this
	// binary), in list-sessions order.
	Sessions []swSession
	Lobby    string            // session owning selfPane; "" if not found
	Clients  map[string]string // client name -> session it is attached to
}

// isWaiting reports whether a published state means "paused waiting on
// input": Claude's turn ended, or an AskUserQuestion is pending. Exact-match
// on statePublishValue strings — anything unknown is not waiting.
func isWaiting(state string) bool {
	return state == "Idle" || state == "Tool:AskUserQuestion"
}

func (s swSnapshot) session(name string) (swSession, bool) {
	for _, sess := range s.Sessions {
		if sess.Name == name {
			return sess, true
		}
	}
	return swSession{}, false
}

// swClaudeCommand / swClaudeShimCommand are the pane_current_command values
// that identify a claude pane. The shim is only a fallback: some runtimes
// report "node" for claude, but plenty of unrelated processes report it too
// (a dev server in a shell pane), so a real claude pane always wins. Same
// preference order claudePaneCandidates uses — see panemap.go.
const (
	swClaudeCommand     = "claude"
	swClaudeShimCommand = "node"
)

// buildSwSnapshot assembles a snapshot from the raw output of the three tmux
// calls (see swPollCmd). Malformed lines are skipped, not fatal: a snapshot
// built from whatever parsed keeps the lobby rendering through transient
// oddities. Formats (tab-separated):
//
//	sessOut:   #{session_name} #{@claudemux_state} #{@claudemux_state_since} #{@claudemux_context} #{@claudemux_summary} #{@claudemux_prompt}
//	paneOut:   #{session_name} #{pane_id} #{pane_current_command} #{window_name}
//	clientOut: #{client_name} #{client_session}
func buildSwSnapshot(sessOut, paneOut, clientOut, selfPane string) swSnapshot {
	snap := swSnapshot{Clients: map[string]string{}}

	heads := map[string]bool{}
	topics := map[string]string{}
	claudePanes := map[string]string{}
	shimPanes := map[string]string{}
	for _, line := range strings.Split(paneOut, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			continue
		}
		if f[2] == swHeadCommand {
			heads[f[0]] = true
			topics[f[0]] = f[3]
		}
		// First pane of each kind wins, so the previewed pane is stable
		// across polls even when a session has several candidates.
		switch f[2] {
		case swClaudeCommand:
			if _, ok := claudePanes[f[0]]; !ok {
				claudePanes[f[0]] = f[1]
			}
		case swClaudeShimCommand:
			if _, ok := shimPanes[f[0]]; !ok {
				shimPanes[f[0]] = f[1]
			}
		}
		if f[1] == selfPane && selfPane != "" {
			snap.Lobby = f[0]
		}
	}

	for _, line := range strings.Split(sessOut, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 6 || f[0] == "" {
			continue
		}
		if !heads[f[0]] || f[0] == snap.Lobby {
			continue
		}
		sess := swSession{Name: f[0], State: f[1], Summary: f[4], Prompt: f[5]}
		sess.Context = -1
		if ctx, err := strconv.Atoi(f[3]); err == nil {
			sess.Context = ctx
		}
		if secs, err := strconv.ParseInt(f[2], 10, 64); err == nil {
			sess.Since = time.Unix(secs, 0)
		}
		sess.Topic = topics[sess.Name]
		sess.ClaudePane = claudePanes[sess.Name]
		if sess.ClaudePane == "" {
			sess.ClaudePane = shimPanes[sess.Name]
		}
		snap.Sessions = append(snap.Sessions, sess)
	}

	for _, line := range strings.Split(clientOut, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 2 || f[0] == "" {
			continue
		}
		snap.Clients[f[0]] = f[1]
	}
	return snap
}
