package main

import (
	"fmt"
	"sort"
	"time"
)

// The conductor decides when to move the driven tmux client. It is pure —
// step() consumes a snapshot and returns at most one switch-client action —
// so every policy in the spec is unit-testable without tmux.
type swPhase int

const (
	// swParked: the client sits on the lobby; dispatch when something waits.
	swParked swPhase = iota
	// swEscorting: the conductor moved the client to escortee; hold until
	// that session stops waiting.
	swEscorting
	// swPaused: the client is somewhere the conductor didn't put it. Never
	// fight the user — resume only when they return to the lobby.
	swPaused
)

type swAction struct {
	Client string // tmux client_name to move
	Target string // session to switch it to
}

// swSnooze records a waiting episode the user deliberately walked away from:
// which episode (the session's published Since) and when they left.
type swSnooze struct {
	since time.Time
	at    time.Time
}

// swSnoozeTTL bounds a snooze. It exists so a skip cannot become forever:
// an idle session's episode lasts until its state actually transitions —
// hours, for a session the user is done with — and an unexpiring snooze
// starves it behind sessions that were never skipped. Ten minutes keeps the
// original anti-bounce purpose (leaving a session must not ping-pong the
// client straight back) while guaranteeing every waiting session resurfaces
// within one sitting.
const swSnoozeTTL = 10 * time.Minute

type conductor struct {
	phase    swPhase
	client   string
	escortee string
	// snoozed maps session -> the waiting episode the user deliberately
	// walked away from, and when. That episode never re-queues until the
	// snooze expires; a new episode (different Since) un-snoozes it
	// immediately. Without this, skipping an Idle session would bounce the
	// client straight back to it from the lobby.
	snoozed map[string]swSnooze
	// Paused-session observation. The user navigated somewhere themselves;
	// swPaused's contract is "never fight the user" — but Michael's actual
	// signal for "done here" is handing the session back to Claude, not
	// walking to the lobby. pausedCur/pausedCurWaiting track the session
	// under the client and whether it was waiting on the last tick;
	// pausedHandedBack latches once that same session transitions
	// waiting → not-waiting under them. From then on any waiting session
	// collects the user (now, or whenever one appears). Latched rather
	// than edge-only: the next waiter may fire minutes after the
	// hand-back. Jumping into an already-busy session never latches, so
	// "go watch a busy session" stays possible.
	pausedCur        string
	pausedCurWaiting bool
	pausedHandedBack bool
}

func newConductor() conductor {
	return conductor{snoozed: map[string]swSnooze{}}
}

// waitingQueue lists waiting, un-snoozed sessions oldest-first (name as
// tiebreak so equal timestamps still order deterministically).
func (s swSnapshot) waitingQueue(snoozed map[string]swSnooze, now time.Time) []swSession {
	var q []swSession
	for _, sess := range s.Sessions {
		if !isWaiting(sess.State) {
			continue
		}
		if sn, ok := snoozed[sess.Name]; ok && sn.since.Equal(sess.Since) && now.Sub(sn.at) < swSnoozeTTL {
			continue
		}
		q = append(q, sess)
	}
	sort.SliceStable(q, func(i, j int) bool {
		if !q[i].Since.Equal(q[j].Since) {
			return q[i].Since.Before(q[j].Since)
		}
		return q[i].Name < q[j].Name
	})
	return q
}

// resolveClient keeps driving the same client while it exists, else adopts
// the lexicographically smallest client attached to the lobby (deterministic
// under Go's random map order). Returns whether the client identity changed
// (old disconnected, new one adopted). No lobby client means nothing to drive.
func (c *conductor) resolveClient(s swSnapshot) bool {
	if c.client != "" {
		if _, ok := s.Clients[c.client]; ok {
			return false
		}
		c.client = ""
	}
	names := make([]string, 0, len(s.Clients))
	for name, sess := range s.Clients {
		if sess == s.Lobby && s.Lobby != "" {
			names = append(names, name)
		}
	}
	if len(names) > 0 {
		sort.Strings(names)
		old := c.client
		c.client = names[0]
		return old != c.client
	}
	// No lobby client, but if exactly one client exists anywhere, it's
	// unambiguously ours even before it ever visits the lobby — the daemon
	// can start after the user already has claude open elsewhere. With 2+
	// off-lobby candidates there's no way to tell which is ours, so we
	// still wait for a lobby visit in that case.
	if c.client == "" && len(s.Clients) == 1 {
		for name := range s.Clients {
			c.client = name
			return true
		}
	}
	return false
}

// pruneSnoozes drops snoozes whose episode ended (session gone, no longer
// waiting, or waiting anew with a different Since) or whose TTL expired.
// Keeping the map minimal makes state inspectable and stops unbounded growth
// across long runs.
func (c *conductor) pruneSnoozes(s swSnapshot, now time.Time) {
	for name, sn := range c.snoozed {
		sess, ok := s.session(name)
		if !ok || !isWaiting(sess.State) || !sess.Since.Equal(sn.since) || now.Sub(sn.at) >= swSnoozeTTL {
			delete(c.snoozed, name)
		}
	}
}

// clearPaused forgets the paused-session observation; called on every path
// that leaves swPaused so a later pause at the same session cannot inherit a
// stale hand-back.
func (c *conductor) clearPaused() {
	c.pausedCur = ""
	c.pausedCurWaiting = false
	c.pausedHandedBack = false
}

// step advances the conductor by one poll. ok=true carries the single
// switch-client to issue this tick.
func (c *conductor) step(s swSnapshot, now time.Time) (swAction, bool) {
	c.pruneSnoozes(s, now)
	clientChanged := c.resolveClient(s)
	if c.client == "" {
		return swAction{}, false
	}
	cur := s.Clients[c.client]
	queue := s.waitingQueue(c.snoozed, now)

	switch c.phase {
	case swParked:
		if cur != s.Lobby {
			c.phase = swPaused
			return swAction{}, false
		}
		if len(queue) > 0 {
			c.phase = swEscorting
			c.escortee = queue[0].Name
			return swAction{Client: c.client, Target: c.escortee}, true
		}
	case swEscorting:
		// If the driven client disconnected and a new one was adopted, that's
		// not a walk-away (user did not move themselves). Clear escortee and
		// transition; the escortee will be re-dispatched on a following tick.
		if clientChanged {
			c.escortee = ""
			if cur == s.Lobby {
				c.phase = swParked
			} else {
				c.phase = swPaused
			}
			return swAction{}, false
		}
		if cur != c.escortee {
			// The user moved themselves with the same client. Snooze the
			// abandoned session's current episode so the lobby doesn't bounce
			// them right back.
			if sess, ok := s.session(c.escortee); ok && isWaiting(sess.State) {
				c.snoozed[c.escortee] = swSnooze{since: sess.Since, at: now}
			}
			c.escortee = ""
			if cur == s.Lobby {
				c.phase = swParked
			} else {
				c.phase = swPaused
			}
			return swAction{}, false
		}
		if sess, ok := s.session(c.escortee); !ok || !isWaiting(sess.State) {
			c.escortee = ""
			if len(queue) > 0 {
				c.escortee = queue[0].Name
				return swAction{Client: c.client, Target: c.escortee}, true
			}
			c.phase = swParked
			return swAction{Client: c.client, Target: s.Lobby}, true
		}
	case swPaused:
		if cur == s.Lobby {
			c.phase = swParked
			c.clearPaused()
			break
		}
		sess, ok := s.session(cur)
		curWaiting := ok && isWaiting(sess.State)
		if cur != c.pausedCur {
			// First look at this spot (fresh pause, or the user moved
			// again): observation restarts, hand-back forgotten.
			c.pausedCur, c.pausedCurWaiting, c.pausedHandedBack = cur, curWaiting, false
			break
		}
		if c.pausedCurWaiting && !curWaiting {
			c.pausedHandedBack = true
		}
		c.pausedCurWaiting = curWaiting
		if c.pausedHandedBack && !curWaiting && len(queue) > 0 {
			c.clearPaused()
			c.phase = swEscorting
			c.escortee = queue[0].Name
			return swAction{Client: c.client, Target: c.escortee}, true
		}
	}
	return swAction{}, false
}

// statusLine summarizes the conductor for the lobby's bottom row.
func (c *conductor) statusLine(s swSnapshot, now time.Time) string {
	n := len(s.waitingQueue(c.snoozed, now))
	// Counted live against the snapshot rather than len(c.snoozed): pruning
	// only runs inside step(), which the lobby skips while standby is on, so
	// the raw map size can be stale (TTL elapsed, or Since moved on) for a
	// render or two. This keeps the suffix exactly matching what
	// waitingQueue excluded at this instant.
	suffix := ""
	z := 0
	for _, sess := range s.Sessions {
		if c.isSnoozed(sess, now) {
			z++
		}
	}
	if z > 0 {
		suffix = fmt.Sprintf(" · %d snoozed", z)
	}
	switch c.phase {
	case swPaused:
		return "paused — you navigated away; finish there or return here to resume"
	case swEscorting:
		return fmt.Sprintf("escorting → %s · %d waiting%s", c.escortee, n, suffix)
	}
	return fmt.Sprintf("conducting · %d waiting%s", n, suffix)
}

// isSnoozed reports whether sess's current episode is snoozed right now —
// the lobby dims such rows so "waiting but deliberately skipped" is visible
// instead of looking like a conductor bug.
func (c *conductor) isSnoozed(sess swSession, now time.Time) bool {
	sn, ok := c.snoozed[sess.Name]
	return ok && sn.since.Equal(sess.Since) && now.Sub(sn.at) < swSnoozeTTL
}
