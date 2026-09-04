package main

// Following a parked session to its successor transcript.
//
// Claude Code can park an interactive session and continue the conversation
// in a daemon-hosted fork (observed 2026-09-04 via a "fleet" attach). The
// old transcript then ends with a `continued-in` record naming the new
// session id, and every later turn is written to <new id>.jsonl — not
// necessarily under the same project dir. Nothing updates the pane map: the
// fork runs outside the tmux pane, so the hook that writes
// ~/.claude/claudemux/panes/<n>.json never fires for it, and
// mappedTranscript keeps returning the dead file. The head then reads a
// transcript nobody writes to again and publishes Idle forever — while the
// pane is visibly working.
//
// The harness's own record is the fix: whichever path the pane map (or the
// current binding) names, resolve it through the recorded successors before
// deciding whether to rotate. followContinuation finds the successor via
// transcriptForSession, which globs projectsDir/*/<id>.jsonl across every
// project dir — deliberately, so the fork's transcript resolves even when a
// worktree move also lands it under a different project dir than the parked
// session's. The successor must exist on disk; until it does, the head
// stays on the old file (its last verdict is still the best available).

// continuationMaxHops bounds followContinuation. Real chains are one hop
// (a fork of a fork is two); the bound only guards a malformed cycle.
const continuationMaxHops = 8

// noteContinuations records, for the session whose events these are, the
// successor any continued-in record names. Returns the (possibly newly
// allocated) map; a nil input with nothing to record stays nil. A record
// naming the session itself is ignored — it would be a one-step cycle.
func noteContinuations(superseded map[string]string, sessionID string, events []Event) map[string]string {
	for _, e := range events {
		if e.ContinuedIn == "" || sessionID == "" || e.ContinuedIn == sessionID {
			continue
		}
		if superseded == nil {
			superseded = map[string]string{}
		}
		superseded[sessionID] = e.ContinuedIn
	}
	return superseded
}

// followContinuation resolves a transcript path through the recorded
// successors: while the path's session was superseded and the successor's
// transcript exists under projectsDir, step to it. Returns path unchanged
// when there is nothing to follow or the successor is not on disk yet.
func followContinuation(path string, superseded map[string]string, projectsDir string) string {
	for hops := 0; hops < continuationMaxHops; hops++ {
		next, ok := superseded[transcriptSessionID(path)]
		if !ok {
			return path
		}
		resolved, found := transcriptForSession(projectsDir, next)
		if !found {
			return path
		}
		path = resolved
	}
	return path
}

// resolveActiveTranscript is pollData's rotation decision, pure so it can be
// tested: given the pane-mapped transcript (or "" when none is known), the
// current binding, and the recorded successors, return the path to adopt or
// "" to keep the current one.
func resolveActiveTranscript(mapped, current string, superseded map[string]string, projectsDir string) string {
	if mapped != "" {
		mapped = followContinuation(mapped, superseded, projectsDir)
		if mapped != current {
			return mapped
		}
		return ""
	}
	if cur := followContinuation(current, superseded, projectsDir); cur != current {
		return cur
	}
	return ""
}
