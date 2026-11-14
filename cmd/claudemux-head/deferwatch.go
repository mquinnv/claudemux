package main

import (
	"strings"
	"time"
)

// deferGrace is how long a blocker must stay out of the way before the defer
// clears. A blocker flips through Idle between turns and during a /clear, so
// acting on the first poll that sees it stopped would clear defers that are
// still earned.
const deferGrace = 20 * time.Second

// fleetRow is one session as the watcher and the completer see it.
type fleetRow struct {
	Name, Created, State string
	Deferred             bool
	Parent               string // @claudemux_parent, for ordering completion
}

// fleetFormat is the list-sessions row parseFleet reads, built from the option
// constants so a rename cannot leave the format and the writers disagreeing.
const fleetFormat = "#{session_name}\t#{session_created}\t#{" + statePublishOption + "}\t#{" +
	deferOption + "}\t#{" + parentOption + "}"

// parseFleet reads a fleetFormat listing in order. A malformed line is skipped
// rather than guessed at: a half-read row must not look like a missing one.
func parseFleet(listing string) []fleetRow {
	var rows []fleetRow
	for _, line := range strings.Split(listing, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 5 || f[0] == "" {
			continue
		}
		rows = append(rows, fleetRow{
			Name: f[0], Created: f[1], State: f[2],
			Deferred: isDeferValue(f[3]), Parent: f[4],
		})
	}
	return rows
}

// blockerVerdict is where a defer's blocker stands relative to the defer.
type blockerVerdict int

const (
	blockerHold    blockerVerdict = iota // still in the way, or not yet seen working
	blockerWorking                       // in the way AND working right now: mark it seen
	blockerStopped                       // seen working, now waiting on a human, not deferred
	blockerGone                          // no live session with that name and creation time
)

// judgeBlocker decides whether the blocker is out of the way. An unreadable
// listing always holds: a failed tmux read is not evidence the blocker is
// gone. The creation stamp is part of the identity so a recycled name is
// "gone", not "still working".
func judgeBlocker(link deferLink, seen bool, rows []fleetRow, rowsOK bool) blockerVerdict {
	if !rowsOK {
		return blockerHold
	}
	for _, r := range rows {
		if r.Name != link.On || r.Created != link.Created {
			continue
		}
		if r.State == "" || isBooting(r.State) {
			return blockerHold
		}
		if !isWaiting(r.State) {
			return blockerWorking
		}
		// Waiting. A deferred blocker is itself parked on someone else, so
		// it is still in the way; an unseen one has not started yet.
		if r.Deferred || !seen {
			return blockerHold
		}
		return blockerStopped
	}
	return blockerGone
}

// deferWatchAction is what the head should do after one poll.
type deferWatchAction int

const (
	watchNone deferWatchAction = iota
	watchMarkSeen
	watchClear      // clear the mark, no nudge
	watchClearNudge // clear the mark and nudge
)

type deferWatchInput struct {
	Now          time.Time
	Mark         deferMark
	MarkOK       bool // the mark read succeeded
	Verdict      blockerVerdict
	OwnIdle      bool // this session is idle at its prompt (StateIdle)
	OwnWorking   bool // this session is mid-turn
	NudgeEnabled bool
}

// deferWatch is the head's memory between polls. Zero value is ready.
type deferWatch struct {
	graceSince time.Time
	graceLink  deferLink
	graceGone  bool
}

// step folds one poll into the grace-period machine. It lives apart from the
// tmux plumbing so the timing rules can be tested with a fake clock.
func (w *deferWatch) step(in deferWatchInput) (act deferWatchAction, gone bool) {
	if !in.MarkOK {
		return watchNone, false
	}
	if !isDeferValue(in.Mark.Raw) || !in.Mark.Link.set() {
		*w = deferWatch{}
		return watchNone, false
	}
	if !w.graceSince.IsZero() && in.Mark.Link != w.graceLink {
		*w = deferWatch{}
	}
	switch in.Verdict {
	case blockerWorking:
		*w = deferWatch{}
		if !in.Mark.Seen {
			return watchMarkSeen, false
		}
		return watchNone, false
	case blockerStopped, blockerGone:
		gone = in.Verdict == blockerGone
		if in.OwnWorking {
			// The user already moved on; waiting out the grace would only
			// leave a stale chip on a session that is busy.
			*w = deferWatch{}
			return watchClear, gone
		}
		if w.graceSince.IsZero() {
			*w = deferWatch{graceSince: in.Now, graceLink: in.Mark.Link, graceGone: gone}
			return watchNone, gone
		}
		w.graceGone = gone
		if in.Now.Sub(w.graceSince) < deferGrace {
			return watchNone, gone
		}
		*w = deferWatch{}
		if in.NudgeEnabled && in.OwnIdle {
			return watchClearNudge, gone
		}
		return watchClear, gone
	default:
		*w = deferWatch{}
		return watchNone, false
	}
}
