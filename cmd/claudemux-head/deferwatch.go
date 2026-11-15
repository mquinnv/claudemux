package main

import (
	"context"
	"os/exec"
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

// readFleet lists every session for the head's poll, on the poll's own
// deadline. ok is false when the listing failed, which judgeBlocker treats as
// no evidence at all rather than as an empty fleet.
func readFleet(ctx context.Context) ([]fleetRow, bool) {
	out, err := exec.CommandContext(ctx, "tmux", "list-sessions", "-F", fleetFormat).Output()
	if err != nil {
		return nil, false
	}
	return parseFleet(string(out)), true
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
	Now     time.Time
	Mark    deferMark
	MarkOK  bool // the mark read succeeded
	Verdict blockerVerdict
	OwnIdle bool // this session is idle at its prompt (StateIdle)
	// OwnWorking is this session mid-turn. It ends a grace period early only
	// when the session was not working as the grace started and is now: a
	// session already mid-turn then (a handoff still writing its summary) has
	// not moved on, it just has not stopped yet.
	OwnWorking   bool
	NudgeEnabled bool
}

// deferWatch is the head's memory between polls. Zero value is ready.
type deferWatch struct {
	graceSince time.Time
	graceLink  deferLink
	// graceOwnWorking is whether this session was mid-turn when the grace
	// started. Such a session waits the grace out and is never nudged.
	graceOwnWorking bool
}

// step folds one poll into the grace-period machine. It lives apart from the
// tmux plumbing so the timing rules can be tested with a fake clock.
//
// A stopped or gone blocker always starts a grace period first. During it the
// mark clears early, without a nudge, only if this session was idle when the
// grace started and has since started a turn: the user moved on. A session
// that was already mid-turn at grace start waits the grace out and then
// clears without a nudge, since it may have just handled the reply itself;
// clearing it at once would drop the mark while a handoff's peer is merely
// between turns. A blocker that resumes, or a link change, abandons the
// grace; a failed mark read leaves it as it is.
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
		if w.graceSince.IsZero() {
			*w = deferWatch{graceSince: in.Now, graceLink: in.Mark.Link, graceOwnWorking: in.OwnWorking}
			return watchNone, gone
		}
		if in.Now.Sub(w.graceSince) < deferGrace {
			if !w.graceOwnWorking && in.OwnWorking {
				// The user already moved on; waiting out the grace would
				// only leave a stale chip on a session that is busy.
				*w = deferWatch{}
				return watchClear, gone
			}
			return watchNone, gone
		}
		ownWasWorking := w.graceOwnWorking
		*w = deferWatch{}
		if in.NudgeEnabled && in.OwnIdle && !ownWasWorking {
			return watchClearNudge, gone
		}
		return watchClear, gone
	default:
		*w = deferWatch{}
		return watchNone, false
	}
}
