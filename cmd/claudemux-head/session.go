package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	return filepath.Join(projectDir, waitingTranscriptName)
}

// waitingTranscriptName is the placeholder's file name. Claude Code names
// transcripts by session UUID, so no real transcript can collide with it.
const waitingTranscriptName = "waiting-for-first-session.jsonl"

// isWaitingTranscript reports whether path is a waiting placeholder rather
// than a real transcript — switchSession re-enters waiting mode on one.
func isWaitingTranscript(path string) bool {
	return filepath.Base(path) == waitingTranscriptName
}

// launchWaits decides whether a head starts in waiting mode instead of
// binding to the project dir's newest transcript.
//
// bin/claudemux always launches a FRESH claude session next to the head, so
// in any project that has been used before, the newest transcript on disk is
// the previous session's — never the sibling pane's. Binding to it showed
// that session's prompt, model and context gauge (and summarized it) until
// the new session wrote its first line, which Claude Code does only at the
// first prompt. Inside tmux the pane map is what binds the head to its
// sibling, so a following head waits for it; a head restarted next to a live
// session (`R`) is bound by the map on its very first poll. A pinned head
// (--session) bound unconditionally and never followed; outside tmux there is
// no map to wait for, and the MRA glob is all there is.
func launchWaits(followActive bool, selfPane string) bool {
	return followActive && selfPane != ""
}

// followTarget is pollData's follow-active rotation decision, pure so it can
// be tested: given what the pane map said (mapped, and whether any claude
// pane was seen at all), the current binding and its waiting anchor, return
// the path to adopt and the route that chose it ("mapped", "mru-fallback",
// "continued-in"), or "" to keep the current binding.
func followTarget(mapped string, haveClaudePane bool, jsonlPath string, waitingSince time.Time, superseded map[string]string, projectsDir string) (string, string) {
	if haveClaudePane {
		// mapped is "" when the pane's live cwd is known but its transcript
		// isn't yet — keep the current binding then rather than adopting an
		// empty path. A mapped file that the harness has since continued
		// elsewhere resolves to its successor: the pane map is not rewritten
		// by a park.
		if next := resolveActiveTranscript(mapped, jsonlPath, superseded, projectsDir); next != "" {
			return next, "mapped"
		}
		// An unmapped pane while waiting: the hook has not written a map
		// for it (SessionStart still pending, or the hook missing). The
		// newer-than-the-wait fallback is safe here for the same reason it
		// is safe with no pane at all — a transcript written after the wait
		// began can only be the session being waited for — and without it
		// a hook-less head would sit on Starting forever.
		if mapped == "" && !waitingSince.IsZero() {
			if mra := followFallback(jsonlPath, waitingSince); mra != "" {
				return mra, "mru-fallback"
			}
		}
	} else if mra := followFallback(jsonlPath, waitingSince); mra != "" {
		// "No claude pane at all" — a wedged or slow tmux (listPanes has a
		// 2s deadline) looks identical to a genuinely absent pane here, and
		// this fallback then adopts whichever transcript in the dir was
		// touched last (while waiting, only one touched since the wait
		// began — see followFallback).
		return mra, "mru-fallback"
	}
	// The current binding itself may have been continued elsewhere with no
	// pane map to say so (no claude pane found, or the map still naming this
	// very file).
	if next := resolveActiveTranscript("", jsonlPath, superseded, projectsDir); next != "" {
		return next, "continued-in"
	}
	return "", ""
}

// followFallback is followTarget's rotation when the pane map has nothing to
// say: the most-recently-modified transcript in the bound file's dir, or ""
// when that is the bound file itself (or the dir holds none).
//
// waitingSince narrows it while waiting: a transcript modified before the
// wait began is not the session being waited for. At launch the claude pane
// is briefly not a candidate — it is still a shell, or the `boot` holder on
// an op_env project — and without this the fallback re-adopted the previous
// session's transcript on the head's first poll, undoing launchWaits. A
// brand-new project has nothing older to adopt, so it is unaffected. Zero
// waitingSince is the plain MRA rotation.
func followFallback(jsonlPath string, waitingSince time.Time) string {
	mra, ok := mostRecentlyActiveSession(filepath.Dir(jsonlPath))
	if !ok || mra == jsonlPath {
		return ""
	}
	if !waitingSince.IsZero() {
		fi, err := os.Stat(mra)
		if err != nil || fi.ModTime().Before(waitingSince) {
			return ""
		}
	}
	return mra
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
