package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func encodeProjectPath(absPath string) string {
	return strings.ReplaceAll(absPath, "/", "-")
}

type sessionEntry struct {
	SessionID    string `json:"sessionId"`
	ProjectPath  string `json:"projectPath"`
	Modified     string `json:"modified"`
	FirstPrompt  string `json:"firstPrompt"`
	MessageCount int    `json:"messageCount"`
	GitBranch    string `json:"gitBranch"`
}

type sessionsIndex struct {
	Version int            `json:"version"`
	Entries []sessionEntry `json:"entries"`
}

func parseSessionsIndex(data []byte) ([]sessionEntry, error) {
	var idx sessionsIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parsing sessions index: %w", err)
	}
	return idx.Entries, nil
}

// findMostRecentSession returns the session ID with the latest Modified timestamp.
// The entries slice is sorted in place. ISO 8601 UTC timestamps sort lexicographically.
func findMostRecentSession(entries []sessionEntry) string {
	if len(entries) == 0 {
		return ""
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Modified > entries[j].Modified
	})
	return entries[0].SessionID
}

// newestByModTime returns the most-recently-modified path from matches, sorting
// the slice in place. ok is false for an empty slice. A path that can't be
// stat'd sorts as not-newer, so it never wins over a readable file.
func newestByModTime(matches []string) (string, bool) {
	if len(matches) == 0 {
		return "", false
	}
	sort.Slice(matches, func(i, j int) bool {
		infoI, errI := os.Stat(matches[i])
		infoJ, errJ := os.Stat(matches[j])
		if errI != nil || errJ != nil {
			return false
		}
		return infoI.ModTime().After(infoJ.ModTime())
	})
	return matches[0], true
}

// mostRecentlyActiveSession returns the path of the most-recently-modified
// .jsonl file in dir. ok is false when dir has no session files or can't be
// read — callers should keep their current binding in that case. This is the
// "most-recently-active" (MRA) selector the live monitor uses to follow a
// session that rotates underneath it (new session, /clear, resume, compaction).
func mostRecentlyActiveSession(dir string) (string, bool) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return "", false
	}
	return newestByModTime(matches)
}

// transcriptForSession returns the newest transcript named "<sessionID>.jsonl"
// across every per-project folder under projectsDir. A session's transcript
// moves to a different encoded project dir when its cwd crosses into (or out of)
// a git worktree, so the pane map's recorded transcript_path can dangle while
// the session keeps writing under a new dir. The session id is stable across
// that move, so globbing by it recovers the live transcript without having to
// re-derive Claude Code's project-dir encoding (which drops '/', '.', and '+'
// alike to '-' and so can't be reversed). ok is false when no such file exists.
func transcriptForSession(projectsDir, sessionID string) (string, bool) {
	if projectsDir == "" || sessionID == "" {
		return "", false
	}
	matches, err := filepath.Glob(filepath.Join(projectsDir, "*", sessionID+".jsonl"))
	if err != nil {
		return "", false
	}
	return newestByModTime(matches)
}

// discoverSessionsFromJSONL finds sessions by listing .jsonl files in the project
// directory and returning the most recently modified one. This is the fallback
// when sessions-index.json doesn't exist.
func discoverSessionsFromJSONL(projectDir string) (string, error) {
	path, ok := mostRecentlyActiveSession(projectDir)
	if !ok {
		return "", fmt.Errorf("no session files found in %s", projectDir)
	}
	// Extract session ID from filename (strip directory and .jsonl extension)
	return strings.TrimSuffix(filepath.Base(path), ".jsonl"), nil
}

// waitingTranscript is the placeholder path a head binds to when its project
// has no transcript yet — a brand-new project, where claudemux launches the
// head and the claude pane in the same second and Claude Code only creates its
// .jsonl after it finishes booting. The file never exists; it anchors the
// follow-active scan to the right project dir (pollData globs the placeholder's
// dir) so the first real transcript differs from it and rotation adopts it.
// The bound model carries sessionID "" — the waiting-mode marker
// recomputeFromEvents keys StateWaiting on.
func waitingTranscript(projectDir string) string {
	return filepath.Join(projectDir, "waiting-for-first-session.jsonl")
}

func resolveSession(claudeProjectsDir string, cwd string, explicitSession string) (string, error) {
	if explicitSession != "" {
		return explicitSession, nil
	}

	encoded := encodeProjectPath(cwd)
	projectDir := filepath.Join(claudeProjectsDir, encoded)

	// Always discover from JSONL file mtimes — sessions-index.json can lag
	// arbitrarily behind reality (its `modified` timestamps are not refreshed
	// per turn), and we want the truly-active session, not the most-recently-
	// indexed one.
	return discoverSessionsFromJSONL(projectDir)
}
