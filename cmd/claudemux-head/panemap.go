package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// paneMap mirrors the JSON written by hooks/claudemux-map.sh.
type paneMap struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
}

// paneMapDir returns the directory where the Claude Code hook records
// pane → transcript mappings. Empty when the home dir can't be resolved.
func paneMapDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "claudemux", "panes")
}

// claudePaneCandidates scans `tmux list-panes` output ("%pane @window command"
// per line) and returns every pane, excluding self, running claude (matched
// exactly as "claude", or "node" for runtimes that report the shim, so
// "claudemux-head" panes never match). Candidates are ordered by preference:
// same-window claude, same-window node, other-window claude, other-window
// node; ordering within a group is stable (listing order). self's window is
// found by locating self's own line in the listing; if self isn't present,
// every candidate is treated as other-window.
func claudePaneCandidates(listing, self string) []string {
	selfWindow := ""
	haveSelfWindow := false
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if fields[0] == self {
			selfWindow = fields[1]
			haveSelfWindow = true
			break
		}
	}

	var groups [4][]string // 0: same-window claude, 1: same-window node, 2: other-window claude, 3: other-window node
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == self {
			continue
		}
		pane, window, command := fields[0], fields[1], fields[2]
		if command != "claude" && command != "node" {
			continue
		}
		sameWindow := haveSelfWindow && window == selfWindow
		switch {
		case sameWindow && command == "claude":
			groups[0] = append(groups[0], pane)
		case sameWindow && command == "node":
			groups[1] = append(groups[1], pane)
		case command == "claude":
			groups[2] = append(groups[2], pane)
		default:
			groups[3] = append(groups[3], pane)
		}
	}

	var candidates []string
	for _, g := range groups {
		candidates = append(candidates, g...)
	}
	return candidates
}

// claudePaneCandidatesLive lists the claude/node panes in the same tmux
// session as self (a pane id like "%35"), ordered by preference (see
// claudePaneCandidates), for the caller to try in turn. Returns nil outside
// tmux or on error.
func claudePaneCandidatesLive(self string) []string {
	if self == "" {
		return nil
	}
	// The poll loop calls this every second; a wedged tmux server must never
	// hang it. Bound the subprocess with a hard deadline and treat a timeout
	// like any other failure to shell out.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "list-panes", "-s", "-t", self,
		"-F", "#{pane_id} #{window_id} #{pane_current_command}").Output()
	if err != nil {
		return nil
	}
	return claudePaneCandidates(string(out), self)
}

// readPaneMap returns the transcript path and live cwd recorded for paneID.
// ok requires the map file to parse and the transcript to exist on disk —
// this check rejects maps whose transcript was deleted (transcripts
// otherwise persist indefinitely, so the check isn't about tmux server
// restarts). Staleness from a recycled pane id is instead bounded by the
// hook's SessionStart/UserPromptSubmit rewrites and its 7-day prune of
// unwritten map files. cwd is "" for map files written by an older hook
// version that didn't record it — that alone does not affect ok.
func readPaneMap(dir, paneID string) (string, string, bool) {
	if dir == "" || paneID == "" {
		return "", "", false
	}
	name := strings.TrimPrefix(paneID, "%") + ".json"
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", "", false
	}
	var m paneMap
	if err := json.Unmarshal(data, &m); err != nil || m.TranscriptPath == "" {
		return "", "", false
	}
	if _, err := os.Stat(m.TranscriptPath); err != nil {
		return "", "", false
	}
	return m.TranscriptPath, m.Cwd, true
}

// mappedTranscript composes candidate-pane discovery with the map lookup:
// it tries each claude/node pane candidate in preference order and returns
// the transcript path and live cwd for the first one the hook has recorded
// a map for. ok is false if no candidate has a usable map.
func mappedTranscript(selfPane, dir string) (string, string, bool) {
	for _, pane := range claudePaneCandidatesLive(selfPane) {
		if transcript, cwd, ok := readPaneMap(dir, pane); ok {
			return transcript, cwd, true
		}
	}
	return "", "", false
}
