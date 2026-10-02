package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// A permission dialog ("Do you want to proceed?") blocks the session on the
// human exactly as an AskUserQuestion does, but the transcript shows it the
// opposite way round: the tool_use is already flushed, so classifyState reads
// "Tool: X" for as long as the dialog sits there — a session that looks busy
// and is in fact waiting for a keypress.
//
// Two signals together say otherwise, and neither is enough alone:
//
//   - hooks/claudemux-ask.sh writes a marker at PermissionRequest, the one
//     hook that fires when a dialog is about to open. But no hook fires when
//     it is ANSWERED: an approved tool just starts running, and its
//     PostToolUse arrives only when it finishes. On the marker alone an
//     approved ten-minute build would read as waiting for all ten minutes,
//     and the conductor would escort the human into a session that is working.
//   - the claude pane shows the dialog. But scraping it on every poll of every
//     tool call would cost a tmux spawn a second per session, and a pane can
//     show those words for other reasons.
//
// So the marker says when to look, and the pane says whether it is still true.

// permitMarker is one PermissionRequest as the hook recorded it. At is the
// marker file's mtime and doubles as the dialog's identity: every request
// restamps the file, so two dialogs never share one.
type permitMarker struct {
	At   time.Time
	Tool string
}

// readPermitMarker returns the permission marker for sessionID, zero when
// there is none. A marker whose body does not parse still counts — the instant
// is the signal, the tool name is decoration.
func readPermitMarker(dir, sessionID string) permitMarker {
	if dir == "" || sessionID == "" {
		return permitMarker{}
	}
	path := filepath.Join(dir, sessionID+".permit.json")
	fi, err := os.Stat(path)
	if err != nil {
		return permitMarker{}
	}
	mk := permitMarker{At: fi.ModTime()}
	if data, err := os.ReadFile(path); err == nil {
		var body struct {
			Tool string `json:"tool_name"`
		}
		if json.Unmarshal(data, &body) == nil {
			mk.Tool = body.Tool
		}
	}
	return mk
}

// permitProbeWindow is how long a marker is probed for a dialog that has not
// shown up. The hook runs before the dialog renders — and alongside every
// other PermissionRequest hook the user has, any of which may answer in the
// dialog's place — so the first captures can honestly miss it.
const permitProbeWindow = 30 * time.Second

// permitDialogTailLines is how many non-blank lines at the bottom of the pane
// are searched for the dialog's footer. The footer is the dialog's last line;
// the slack is for a statusline or a wrapped hint below it.
const permitDialogTailLines = 6

// permissionDialogUp reports whether a capture of the claude pane ends in a
// permission dialog. Every such dialog — Bash, Edit, MCP — closes with an
// "Esc to cancel" footer where the prompt box would otherwise be. Only the
// tail counts: the same words further up are conversation, not a dialog.
func permissionDialogUp(capture string) bool {
	lines := strings.Split(capture, "\n")
	seen := 0
	for i := len(lines) - 1; i >= 0 && seen < permitDialogTailLines; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		seen++
		if strings.Contains(strings.ToLower(line), "esc to cancel") {
			return true
		}
	}
	return false
}

// permitWatch remembers what the pane has shown for the current marker.
type permitWatch struct {
	mark    time.Time // the marker being watched, by its mtime
	since   time.Time // when this process first saw mark
	seen    bool      // the dialog has been on screen for mark
	gone    bool      // ...and has since left: answered, one way or the other
	probing bool      // a capture is in flight
}

// track rebinds the watch when the marker changed. since is this process's
// clock, not the marker's: a head that starts while a dialog has been up for
// hours must still look for it.
func (w *permitWatch) track(mk permitMarker, now time.Time) {
	if w.mark.Equal(mk.At) {
		return
	}
	*w = permitWatch{mark: mk.At, since: now, probing: w.probing}
}

// probeDue reports whether the pane should be captured for mk now. A dialog
// that is up is probed for as long as it stays — that is how its answer is
// noticed. One that never appeared is dropped after permitProbeWindow, and
// one that was answered is not probed again.
func (w *permitWatch) probeDue(mk permitMarker, pane string, now time.Time) bool {
	if mk.At.IsZero() || pane == "" {
		return false
	}
	w.track(mk, now)
	if w.probing || w.gone {
		return false
	}
	return w.seen || now.Sub(w.since) < permitProbeWindow
}

// observe records one capture's verdict for the marker stamped at. ok=false is
// a capture that failed and says nothing. A miss retires the marker only once
// the dialog has been seen — before that it may simply not have rendered yet.
func (w *permitWatch) observe(at time.Time, up, ok bool) {
	w.probing = false
	if !ok || !w.mark.Equal(at) {
		return
	}
	if up {
		w.seen = true
		return
	}
	if w.seen {
		w.gone = true
	}
}

// confirmed reports whether mk's dialog is on screen as of the last capture.
func (w *permitWatch) confirmed(mk permitMarker) bool {
	return !mk.At.IsZero() && w.mark.Equal(mk.At) && w.seen && !w.gone
}

// permitOverride upgrades s to Awaiting when mk's dialog is confirmed on
// screen. Tool is the usual verdict underneath it; Idle, Thinking and the
// background kinds cover a dialog raised before the tool_use flushed or by a
// background agent between turns. Asking, Error, Compacting and Starting are
// more specific truths and stay.
func permitOverride(s State, mk permitMarker, confirmed bool) State {
	if mk.At.IsZero() || !confirmed {
		return s
	}
	switch s.Kind {
	case StateTool, StateIdle, StateThinking, StateBackground, StateUnsure:
	default:
		return s
	}
	return State{Kind: StateAwaiting, ToolName: mk.Tool, Since: mk.At, Anchored: true}
}

// permitProbeMsg is one capture's verdict, tagged with the marker it was taken
// for so a late answer cannot be applied to the next dialog.
type permitProbeMsg struct {
	at time.Time
	up bool
	ok bool
}

// permitProbeCmd captures the claude pane and reports whether a permission
// dialog is at the bottom of it. Hard deadline, like every other tmux call the
// head makes: a wedged tmux must never block the TUI.
func permitProbeCmd(pane string, at time.Time) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := swTmux(ctx, "capture-pane", "-p", "-t", pane)
		if err != nil {
			return permitProbeMsg{at: at}
		}
		return permitProbeMsg{at: at, up: permissionDialogUp(out), ok: true}
	}
}
