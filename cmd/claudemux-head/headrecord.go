package main

import (
	"context"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The head's half of reboot restore: keep this session's record fresh.
// See sessionrecord.go for why the record exists.

// sessionRecordInterval is the heartbeat period. It sets how stale the
// interrupted flag can be, and must stay well under lostClusterWindow.
const sessionRecordInterval = 30 * time.Second

// recordWrittenMsg carries the tmux session name the record was written
// under, so teardown can delete exactly that file.
type recordWrittenMsg struct{ name string }

// recordDue reports whether a heartbeat should be written now: inside tmux,
// bound to a session that can be resumed, not tearing down (a write racing
// the teardown's delete would bring the record back), and either the
// interval has passed or the published state changed — so a record never
// says Idle about a session that died mid-turn a few seconds later.
func (m model) recordDue(now time.Time) bool {
	if m.selfPane == "" || m.sessionID == "" || m.teardown != teardownIdle {
		return false
	}
	if m.lastRecordAt.IsZero() || now.Sub(m.lastRecordAt) >= sessionRecordInterval {
		return true
	}
	return statePublishValue(m.state) != m.lastRecordState
}

// sessionRecordFor builds the record from what the head knows. The session
// name and launch dir come from tmux, in writeRecordCmd, off the Update loop.
// ClaudeCwd prefers the cwd the transcript last recorded (a session that
// entered a worktree resumes from there) and falls back to the head's own
// directory.
func (m model) sessionRecordFor(now time.Time) sessionRecord {
	cwd := m.sessionCwd
	if cwd == "" {
		cwd = m.workDir
	}
	return sessionRecord{
		SessionID: m.sessionID,
		ClaudeCwd: cwd,
		State:     statePublishValue(m.state),
		Topic:     m.summary.Topic,
		LastSeen:  now.Unix(),
	}
}

// parseSessionNamePath splits "#{session_name}\t#{session_path}" output.
func parseSessionNamePath(out string) (name, path string, ok bool) {
	name, path, found := strings.Cut(strings.TrimRight(out, "\n"), "\t")
	if !found || name == "" || path == "" {
		return "", "", false
	}
	return name, path, true
}

// writeRecordCmd resolves this pane's session name and launch dir and writes
// the record. Best-effort: any failure is silent (nil message) and the next
// heartbeat tries again — the display never depends on it.
func writeRecordCmd(selfPane, dir string, r sessionRecord) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "-t", selfPane,
			"#{session_name}\t#{session_path}").Output()
		if err != nil {
			return nil
		}
		name, path, ok := parseSessionNamePath(string(out))
		if !ok {
			return nil
		}
		r.SessionName, r.LaunchDir = name, path
		if writeSessionRecord(dir, r) != nil {
			return nil
		}
		return recordWrittenMsg{name: name}
	}
}
