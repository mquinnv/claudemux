package main

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// In a folder Claude Code has never opened it asks "is this a project you
// trust?" before doing anything else — before SessionStart, so no hook fires,
// no pane map is written and no transcript exists. The session is blocked on
// the human exactly as a permission dialog blocks it, and every signal the
// head otherwise reads says only "still starting".
//
// The pane is the one place the dialog shows, so it is scraped — but only for
// a pane in precisely that condition: the head is waiting for a first
// transcript AND the hook has not yet written a map for the claude pane. In a
// trusted folder that lasts the second or two claude takes to boot; behind the
// dialog it lasts until someone answers.

// trustProbeEvery paces the captures. The dialog does not go anywhere, so
// there is nothing to gain from looking every tick.
const trustProbeEvery = 2 * time.Second

// trustDialogUp reports whether a capture of the claude pane is the folder
// trust dialog: it closes with the same "Esc to cancel" footer every dialog
// does, and offers to trust the folder.
func trustDialogUp(capture string) bool {
	return permissionDialogUp(capture) && strings.Contains(strings.ToLower(capture), "trust this folder")
}

// trustWatch is what the pane has shown while the head waits on an unmapped
// claude pane.
type trustWatch struct {
	up      bool      // the dialog was on screen at the last capture
	since   time.Time // when it was first seen
	last    time.Time // when the last capture was issued
	probing bool      // a capture is in flight
}

// probeDue reports whether the pane should be captured now. Outside the one
// condition the dialog can exist in, there is nothing to look for and any
// earlier sighting is over: the map appearing is SessionStart having run,
// which is the dialog having been answered.
func (w *trustWatch) probeDue(waiting, unmapped bool, pane string, now time.Time) bool {
	if !waiting || !unmapped || pane == "" {
		w.up, w.since = false, time.Time{}
		return false
	}
	return !w.probing && now.Sub(w.last) >= trustProbeEvery
}

// started records a capture being issued.
func (w *trustWatch) started(now time.Time) {
	w.probing, w.last = true, now
}

// observe records one capture's verdict. ok=false is a capture that failed
// and says nothing.
func (w *trustWatch) observe(up, ok bool, now time.Time) {
	w.probing = false
	if !ok {
		return
	}
	if !up {
		w.up, w.since = false, time.Time{}
		return
	}
	if !w.up {
		w.up, w.since = true, now
	}
}

// trustOverride upgrades Starting to Awaiting while the trust dialog is on
// screen, so the switchboard lists the session as waiting on the human
// (isWaiting) instead of as one that is merely booting. Only Starting is
// upgraded: a head bound to a transcript is past the dialog by definition.
func trustOverride(s State, w trustWatch) State {
	if s.Kind != StateWaiting || !w.up {
		return s
	}
	return State{Kind: StateAwaiting, ToolName: "trust folder", Since: w.since, Anchored: true}
}

// trustProbeMsg is one capture's verdict.
type trustProbeMsg struct {
	up bool
	ok bool
}

// trustProbeCmd captures the claude pane and reports whether the trust dialog
// is on it. Hard deadline, like every other tmux call the head makes.
func trustProbeCmd(pane string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := swTmux(ctx, "capture-pane", "-p", "-t", pane)
		if err != nil {
			return trustProbeMsg{}
		}
		return trustProbeMsg{up: trustDialogUp(out), ok: true}
	}
}
