package main

import (
	"context"
	"os/exec"
	"path/filepath"
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
func (m model) sessionRecordFor(now time.Time) sessionRecord {
	return sessionRecord{
		SessionID: m.sessionID,
		ClaudeCwd: claudeCwdFor(m.sessionCwd, m.workDir, m.jsonlPath),
		State:     statePublishValue(m.state),
		Topic:     m.summary.Topic,
		LastSeen:  now.Unix(),
	}
}

// claudeCwdFor picks the directory restore should pass as `-C`: the one
// whose Claude project-dir encoding matches where the transcript actually
// lives. sessionCwd is the transcript's last main-chain cwd, but a tool `cd`
// during the session can leave it inside a subdirectory of where claude was
// actually launched (e.g. `cd cmd/claudemux-head` from a worktree root) —
// claude looks up --resume ids under the project dir of ITS OWN cwd, so
// recording the deeper subdirectory makes restore fail "no conversation
// found" even though the same transcript is one level up. Walk from
// sessionCwd through its ancestors, then workDir, and use the first whose
// encoding matches jsonlPath's own directory name. workDir — the head's
// launch dir, always correct by construction — is the fallback when nothing
// matches, or there is no transcript yet to compare against.
func claudeCwdFor(sessionCwd, workDir, jsonlPath string) string {
	if sessionCwd == "" || jsonlPath == "" {
		return workDir
	}
	want := filepath.Base(filepath.Dir(jsonlPath))
	for _, dir := range append(ancestors(sessionCwd), workDir) {
		if claudeProjectDirName(dir) == want {
			return dir
		}
	}
	return workDir
}

// ancestors returns dir and each of its parents up to (not including) "/".
func ancestors(dir string) []string {
	var out []string
	for dir != "" && dir != "/" && dir != "." {
		out = append(out, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return out
}

// claudeProjectDirName reproduces Claude Code's project-dir encoding: every
// byte outside [A-Za-z0-9] becomes '-'. encodeProjectPath (session.go) only
// replaces '/' and so mismatches worktree paths, which also carry '.' from
// ".claude/worktrees/...". This is forward-only, used to test a candidate
// cwd against an already-encoded directory name — see transcriptForSession's
// comment for why the encoding isn't safe to reverse in general.
func claudeProjectDirName(absPath string) string {
	b := make([]byte, len(absPath))
	for i := 0; i < len(absPath); i++ {
		c := absPath[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b[i] = c
		default:
			b[i] = '-'
		}
	}
	return string(b)
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
