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

// listPanes returns the raw `tmux list-panes` output for self's session, one
// pane per line as "#{pane_id} #{window_id} #{pane_current_command}
// #{pane_current_path}". pane_current_path is last because it is the only field
// that can contain spaces. Empty string outside tmux or on error.
//
// Dead panes are filtered out by tmux itself. bin/claudemux sets
// remain-on-exit=failed on the claude pane, so a claude that exited non-zero
// leaves its pane on screen with the error — and tmux keeps reporting "claude"
// as that pane's pane_current_command. Without the filter the head would bind
// to a corpse, and a teardown's wait for claude to be gone would never finish.
// Excluding dead panes restores exactly the pre-command-pane semantics: a pane
// that is no longer running claude is not a candidate.
func listPanes(self string) string {
	if self == "" {
		return ""
	}
	// The poll loop calls this every second; a wedged tmux server must never
	// hang it. Bound the subprocess with a hard deadline and treat a timeout
	// like any other failure to shell out.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "list-panes", "-s", "-t", self,
		"-f", "#{==:#{pane_dead},0}",
		"-F", "#{pane_id} #{window_id} #{pane_current_command} #{pane_current_path}").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// panePaths maps each pane id to its live cwd (pane_current_path) from a
// listPanes listing. The path is everything after the first three space-
// separated fields — every earlier field is space-free — so a cwd containing
// spaces survives intact. Lines without a path (fewer than four fields) are
// skipped.
func panePaths(listing string) map[string]string {
	paths := map[string]string{}
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.SplitN(line, " ", 4)
		if len(fields) < 4 || fields[0] == "" {
			continue
		}
		paths[fields[0]] = fields[3]
	}
	return paths
}

// claudeProjectsPath is the dir under which Claude Code writes per-project
// transcript folders. Empty when the home dir can't be resolved.
func claudeProjectsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
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

// readPaneSession returns the session id the hook recorded for paneID. Unlike
// readPaneMap it does NOT require the recorded transcript to still exist: only
// the map's cwd and transcript_path go stale when an autonomous session cd's
// into a worktree (the hook rewrites the map only on SessionStart /
// UserPromptSubmit), while the session id is stable across that move. It stays a
// valid key for locating the session's current transcript. ok is false when the
// map file is absent, unparseable, or carries no session id.
func readPaneSession(dir, paneID string) (string, bool) {
	if dir == "" || paneID == "" {
		return "", false
	}
	name := strings.TrimPrefix(paneID, "%") + ".json"
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", false
	}
	var m paneMap
	if err := json.Unmarshal(data, &m); err != nil || m.SessionID == "" {
		return "", false
	}
	return m.SessionID, true
}

// mappedTranscript resolves the sibling claude pane's live cwd and current
// transcript. It prefers tmux's pane_current_path — always current — over the
// hook-recorded cwd, which lags until the next SessionStart/UserPromptSubmit and
// so goes stale the moment an autonomous session cd's into a worktree. The
// transcript is found by the pane's stable session id (via transcriptForSession)
// rather than the map's recorded transcript_path, which dangles across the same
// move. It walks candidates in preference order and binds to the first with a
// recorded session id, so cwd and transcript always describe the same session.
//
//   - ok is false only when no claude/node candidate pane exists at all.
//   - cwd is the pane's live path and drives the worktree chip; it is
//     authoritative even when no session id or transcript is known.
//   - transcript is "" when no candidate has a recorded session id yet, or the
//     session's transcript can't be found on disk — the caller keeps its
//     current binding in that case.
//   - pane is the tmux pane id of the claude pane selected, or "" when ok is
//     false. Teardown types into this pane, so it must be the same one whose
//     transcript is followed.
func mappedTranscript(selfPane, dir string) (string, string, string, bool) {
	listing := listPanes(selfPane)
	if listing == "" {
		return "", "", "", false
	}
	candidates := claudePaneCandidates(listing, selfPane)
	if len(candidates) == 0 {
		return "", "", "", false
	}
	paths := panePaths(listing)
	for _, pane := range candidates {
		if sid, ok := readPaneSession(dir, pane); ok {
			transcript, _ := transcriptForSession(claudeProjectsPath(), sid)
			return transcript, paths[pane], pane, true
		}
	}
	// No candidate has a recorded session id yet. Still report the preferred
	// pane's live cwd so the worktree chip tracks reality; there's no
	// transcript to follow until the hook writes a map for it. The pane id is
	// reported regardless — a teardown can type into a pane whose transcript
	// is not yet known.
	return "", paths[candidates[0]], candidates[0], true
}
