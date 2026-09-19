package main

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Picking out the sessions a reboot killed.
//
// A reboot stops every head at once, so their records' last_seen values
// cluster just before the cutoff (the boot, or the tmux server's start —
// whichever is later, so a `tmux kill-server` counts too). Sessions closed
// on purpose earlier stopped heartbeating earlier and fall outside the
// cluster. Records written after the cutoff belong to heads alive now.

// lostClusterWindow is how far before the newest pre-cutoff heartbeat a
// record may be and still count as dying in the same event. Heads write
// every 30s; ten minutes also absorbs a machine that slept before the
// update rebooted it, where heads stop at slightly different moments.
const lostClusterWindow = 10 * time.Minute

// lostSession is a record the lobby offers to restore.
type lostSession struct {
	loadedRecord
	// Interrupted: the session was mid-turn when it died. Only flagged in
	// the picker — nothing is sent to it on restore.
	Interrupted bool
}

// selectLost returns the records that died together before cutoff, newest
// first, and the newest pre-cutoff last_seen (0 when there is none).
// liveNames/liveIDs exclude sessions already running again — by tmux name,
// or by claude session id when the user resumed one by hand elsewhere.
func selectLost(recs []loadedRecord, cutoff int64, liveNames, liveIDs map[string]bool) ([]lostSession, int64) {
	var newest int64
	for _, r := range recs {
		if r.Rec.LastSeen < cutoff && r.Rec.LastSeen > newest {
			newest = r.Rec.LastSeen
		}
	}
	if newest == 0 {
		return nil, 0
	}
	floor := newest - int64(lostClusterWindow/time.Second)
	var lost []lostSession
	for _, r := range recs {
		ls := r.Rec.LastSeen
		if ls >= cutoff || ls < floor {
			continue
		}
		if liveNames[r.Rec.SessionName] || liveIDs[r.Rec.SessionID] {
			continue
		}
		lost = append(lost, lostSession{loadedRecord: r, Interrupted: interruptedState(r.Rec.State)})
	}
	sort.SliceStable(lost, func(i, j int) bool { return lost[i].Rec.LastSeen > lost[j].Rec.LastSeen })
	return lost, newest
}

// interruptedState reports whether a published state value (see
// statePublishValue) means claude was working when the record was written.
// An open AskUserQuestion is waiting on the user, not working.
func interruptedState(state string) bool {
	switch {
	case state == "Thinking", state == "Compacting":
		return true
	case state == "Tool:AskUserQuestion":
		return false
	case strings.HasPrefix(state, "Tool:"), strings.HasPrefix(state, "Background:"):
		return true
	}
	return false
}

// bootTimeRe anchors on the opening brace so "usec" doesn't match the "sec" substring.
var bootTimeRe = regexp.MustCompile(`\{\s*sec = (\d+)`)

// parseBootTime reads `sysctl -n kern.boottime` output:
// "{ sec = 1789750380, usec = 123456 } Thu Sep 18 13:53:00 2026".
func parseBootTime(out string) (int64, bool) {
	m := bootTimeRe.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	sec, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || sec <= 0 {
		return 0, false
	}
	return sec, true
}

// restoreCutoff is the later of the boot time and the tmux server's start.
// With neither known there is no offer: guessing a cutoff could offer to
// "restore" sessions that are merely closed.
func restoreCutoff(boot int64, bootOK bool, tmuxStart int64, tmuxOK bool) (int64, bool) {
	switch {
	case bootOK && tmuxOK:
		if tmuxStart > boot {
			return tmuxStart, true
		}
		return boot, true
	case bootOK:
		return boot, true
	case tmuxOK:
		return tmuxStart, true
	}
	return 0, false
}
