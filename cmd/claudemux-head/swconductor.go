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
// within one sitting. It is the outer bound only: releaseSnoozes lets every
// snooze go the moment nothing else is left to conduct to.
const swSnoozeTTL = 10 * time.Minute

type conductor struct {
	phase    swPhase
	client   string
	escortee string
	// snoozed maps session -> the waiting episode the user deliberately
	// walked away from, and when. That episode does not re-queue while any
	// other session is waiting to be conducted to; a new episode (different
	// Since), the TTL, or the queue running dry (releaseSnoozes) un-snoozes
	// it. Without this, skipping an Idle session would bounce the client
	// straight back to it from the lobby while others were waiting.
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
	// pausedCurDeferred is pausedCur's defer mark as of the last tick, so a
	// defer pressed WHILE paused here (false → true) is distinguishable from
	// a session the user deliberately walked into knowing it was deferred.
	// Only the first means "take me on".
	pausedCurDeferred bool
}

func newConductor() conductor {
	return conductor{snoozed: map[string]swSnooze{}}
}

// waitingQueue lists the waiting sessions that may collect the human,
// oldest Since first (name as tiebreak so equal timestamps still order
// deterministically).
//
// Deferred sessions are not among them at all. "Last in line" turned out to
// be no protection: the queue's other exclusion — snooze — is a filter, not
// a demotion, so a fleet whose normal waiters had all been walked away from
// left the deferred ones as the entire queue, and deferring a session became
// a reason to be sent to it. A defer now means what the user means by it:
// the conductor never drives them here. The session stays loud on the lobby
// (badge, blocker, hue) and one keystroke away, so the mark still costs
// visibility rather than buying it.
func (s swSnapshot) waitingQueue(snoozed map[string]swSnooze, now time.Time) []swSession {
	var q []swSession
	for _, sess := range s.Sessions {
		if !isWaiting(sess.State) || sess.Deferred {
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

// releaseSnoozes lets every snooze go when the snooze filter is all that
// stands between the conductor and an empty queue: every session is busy,
// deferred, or snoozed. A snooze is an anti-bounce, not a veto — it says
// "someone else first", and once there is no one else the skipped sessions
// are conducted through again, oldest first. The veto is defer: a user who
// wants to stay out of a session marks it, and the release never re-queues a
// deferred session (the unfiltered queue excludes them). Returns the queue
// to dispatch from, which is the released one when a release happened.
func (c *conductor) releaseSnoozes(s swSnapshot, now time.Time, queue []swSession) []swSession {
	if len(queue) > 0 || len(c.snoozed) == 0 {
		return queue
	}
	released := s.waitingQueue(nil, now)
	if len(released) == 0 {
		return queue
	}
	c.snoozed = map[string]swSnooze{}
	return released
}

// clearPaused forgets the paused-session observation; called on every path
// that leaves swPaused so a later pause at the same session cannot inherit a
// stale hand-back.
func (c *conductor) clearPaused() {
	c.pausedCur = ""
	c.pausedCurWaiting = false
	c.pausedCurDeferred = false
	c.pausedHandedBack = false
}

// soleSession reports whether name is the entire fleet — the only claudemux
// session the lobby can see. Conducting away from one is pure churn: the
// destination lobby lists nothing but the session being left.
func soleSession(s swSnapshot, name string) bool {
	return len(s.Sessions) == 1 && s.Sessions[0].Name == name
}

// holdingSole reports whether the conductor is sitting on the fleet's only
// session with its turn over — the state step() enters instead of returning
// the client to a lobby with nothing in it. Derived rather than stored so
// there is one definition of the hold, and so it goes away the instant the
// fleet grows or the session starts waiting again.
func (c *conductor) holdingSole(s swSnapshot) bool {
	if c.phase != swEscorting || !soleSession(s, c.escortee) {
		return false
	}
	return !isWaiting(s.Sessions[0].State)
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
	queue := c.releaseSnoozes(s, now, s.waitingQueue(c.snoozed, now))

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
		// A deferred escortee is done with the human exactly as a resolved one
		// is. The escort only ever starts on a non-deferred session (the queue
		// excludes them), so the mark appearing here means the user pressed `d`
		// while sitting in this very session — "I am blocked, take me on" — and
		// leaving them parked in the session they just marked blocked is the
		// one thing the key must not do.
		sess, ok := s.session(c.escortee)
		deferredHere := ok && sess.Deferred
		// Starting is not a hand-back. `/clear` rotates the session id, and
		// Claude Code fires SessionStart with the new id a beat before it
		// creates the new transcript, so the head sits on the waiting
		// placeholder for a poll or two and publishes Starting. The human is
		// at the prompt they just cleared — nothing has been handed to
		// Claude — so hold exactly as for a waiting escortee. Only a real
		// prompt (Thinking, Tool, …) moves them on. A defer pressed while
		// booting still wins, as everywhere else.
		if ok && isBooting(sess.State) && !deferredHere {
			return swAction{}, false
		}
		if !ok || !isWaiting(sess.State) || deferredHere {
			if len(queue) > 0 {
				c.escortee = queue[0].Name
				return swAction{Client: c.client, Target: c.escortee}, true
			}
			// Nowhere else to be: the escortee is the whole fleet, so the
			// lobby would show one row for the session the client is already
			// sitting in. Hold instead of bouncing the user out of the work
			// they just handed back. The escortee is deliberately KEPT — it is
			// what the walk-away branch above compares against, so a manual
			// return to the lobby still parks, and the next session to start
			// waiting still comes through this same branch and collects them.
			//
			// A defer overrides the hold: a one-row lobby is a poor
			// destination, but it is the destination the user asked for, and
			// holding would pin them to the session they just declared blocked.
			if soleSession(s, c.escortee) && !deferredHere {
				return swAction{}, false
			}
			c.escortee = ""
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
		curDeferred := ok && sess.Deferred
		if cur != c.pausedCur {
			// First look at this spot (fresh pause, or the user moved
			// again): observation restarts, hand-back forgotten. A session
			// that was ALREADY deferred when the user walked into it is
			// recorded as such and never reads as a fresh defer below —
			// jumping into a deferred session to unblock it must not get you
			// yanked straight back out.
			c.pausedCur, c.pausedCurWaiting, c.pausedCurDeferred, c.pausedHandedBack = cur, curWaiting, curDeferred, false
			break
		}
		// The user deferred the session they are sitting in. Same meaning as
		// the escorting branch's defer, and the same answer: move them on,
		// whatever the phase says about who put them here. Edge-triggered, so
		// it fires once per defer rather than every tick the mark is set.
		freshDefer := curDeferred && !c.pausedCurDeferred
		c.pausedCurDeferred = curDeferred
		if freshDefer {
			c.clearPaused()
			if len(queue) > 0 {
				c.phase = swEscorting
				c.escortee = queue[0].Name
				return swAction{Client: c.client, Target: c.escortee}, true
			}
			c.phase = swParked
			return swAction{Client: c.client, Target: s.Lobby}, true
		}
		// A Starting tick is no observation at all: the session under the
		// user is between transcripts (see the escorting branch), neither
		// waiting nor handed back. Skip it rather than record it, so the
		// waiting seen before a `/clear` still pairs with the prompt typed
		// after it, and the clear itself never reads as the hand-back edge.
		if ok && isBooting(sess.State) {
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
	z, d := 0, 0
	for _, sess := range s.Sessions {
		if c.isSnoozed(sess, now) {
			z++
		}
		if sess.Deferred && isWaiting(sess.State) {
			d++
		}
	}
	if z > 0 {
		suffix = fmt.Sprintf(" · %d snoozed", z)
	}
	if d > 0 {
		suffix += fmt.Sprintf(" · %d deferred", d)
	}
	switch c.phase {
	case swPaused:
		return "paused — you navigated away; finish there or return here to resume"
	case swEscorting:
		if c.holdingSole(s) {
			return "holding — only session in the fleet"
		}
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
