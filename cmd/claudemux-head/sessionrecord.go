package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Session records: the heartbeat that lets the lobby restore a fleet after a
// reboot.
//
// Nothing tmux or Claude Code keeps survives a reboot in a form that says
// "these sessions were alive when the machine went down": the pane map keeps
// files for panes closed days ago, has no tmux session name, and is keyed by
// pane numbers the next tmux server reuses. So every head writes one record
// for its own session, refreshed every 30s, and the lobby reads the set back
// after a reboot — see lostsessions.go for how the dead cluster is picked out.
// See docs/superpowers/specs/2026-09-19-reboot-restore-design.md.

// sessionRecordMaxAge bounds how long a record for a session that went away
// on its own is kept. Only records inside the 10-minute cluster before a
// reboot are ever offered; this just keeps the directory from growing.
const sessionRecordMaxAge = 7 * 24 * time.Hour

// sessionRecord is one head's heartbeat. The JSON form is an on-disk
// interface read by the lobby, possibly a newer or older binary than the
// head that wrote it — add fields, never rename them.
type sessionRecord struct {
	SessionName string `json:"session_name"`
	LaunchDir   string `json:"launch_dir"`
	SessionID   string `json:"session_id"`
	ClaudeCwd   string `json:"claude_cwd"`
	State       string `json:"state"`
	Topic       string `json:"topic,omitempty"`
	LastSeen    int64  `json:"last_seen"`
}

// loadedRecord pairs a record with the file it came from, so the lobby can
// archive exactly the files it offered.
type loadedRecord struct {
	Path string
	Rec  sessionRecord
}

// sessionRecordDir is ~/.claude/claudemux/sessions, "" when the home dir
// can't be resolved (every caller treats "" as "no records").
func sessionRecordDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "claudemux", "sessions")
}

// sessionRecordFile is the record path for a tmux session name. tmux names
// can hold '/', which would be a subdirectory here, and a leading '.' would
// hide the file; both are replaced. The real name lives inside the record,
// so the filename only has to be unique and safe, not reversible.
func sessionRecordFile(dir, name string) string {
	safe := strings.ReplaceAll(name, "/", "_")
	safe = strings.ReplaceAll(safe, "\x00", "_")
	if safe == "" || strings.HasPrefix(safe, ".") {
		safe = "_" + strings.TrimPrefix(safe, ".")
	}
	return filepath.Join(dir, safe+".json")
}

// writeSessionRecord writes r atomically (temp file + rename, the same
// pattern as hooks/claudemux-map.sh) so a reader never sees half a record —
// and a reboot mid-write leaves the previous record, not a truncated one.
func writeSessionRecord(dir string, r sessionRecord) error {
	if dir == "" {
		return fmt.Errorf("no session record dir")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	path := sessionRecordFile(dir, r.SessionName)
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// readSessionRecords loads every top-level *.json record in dir. Archive
// subdirectories, temp files, unparseable files and records with no session
// name or id are skipped — the last two left in place so they can be looked
// at, never archived or offered.
func readSessionRecords(dir string) []loadedRecord {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []loadedRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var r sessionRecord
		if json.Unmarshal(data, &r) != nil || r.SessionName == "" || r.SessionID == "" {
			continue
		}
		out = append(out, loadedRecord{Path: path, Rec: r})
	}
	return out
}

// pruneSessionRecords deletes top-level records (and stray temp files) whose
// file was last written more than maxAge ago. By mtime rather than LastSeen
// so a corrupt file is also eventually cleared.
func pruneSessionRecords(dir string, now time.Time, maxAge time.Duration) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.Contains(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > maxAge {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// archiveSessionRecords moves the offered records into restored-<cutoff>/,
// so the offer is made once per boot and what was offered stays inspectable.
// A move that fails is reported but does not stop the rest.
func archiveSessionRecords(dir string, cutoff int64, paths []string) error {
	dest := filepath.Join(dir, fmt.Sprintf("restored-%d", cutoff))
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	var firstErr error
	for _, p := range paths {
		if err := os.Rename(p, filepath.Join(dest, filepath.Base(p))); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// removeSessionRecord deletes a session's record — called when a session is
// ended on purpose, so it is never offered back. Missing is fine.
func removeSessionRecord(dir, name string) {
	if dir == "" || name == "" {
		return
	}
	_ = os.Remove(sessionRecordFile(dir, name))
}
