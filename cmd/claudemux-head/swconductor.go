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

type conductor struct {
	phase    swPhase
	client   string
	escortee string
	// snoozed maps session -> the Since of a waiting episode the user
	// deliberately walked away from. That episode never re-queues; a new
	// episode (different Since) does. Without this, skipping an Idle session
	// would bounce the client straight back to it from the lobby.
	snoozed map[string]time.Time
}

func newConductor() conductor {
	return conductor{snoozed: map[string]time.Time{}}
}

// waitingQueue lists waiting, un-snoozed sessions oldest-first (name as
// tiebreak so equal timestamps still order deterministically).
func (s swSnapshot) waitingQueue(snoozed map[string]time.Time) []swSession {
	var q []swSession
	for _, sess := range s.Sessions {
		if !isWaiting(sess.State) {
			continue
		}
		if t, ok := snoozed[sess.Name]; ok && t.Equal(sess.Since) {
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
// under Go's random map order). No lobby client means nothing to drive.
func (c *conductor) resolveClient(s swSnapshot) {
	if c.client != "" {
		if _, ok := s.Clients[c.client]; ok {
			return
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
		c.client = names[0]
	}
}

// pruneSnoozes drops snoozes whose episode ended: session gone, no longer
// waiting, or waiting anew with a different Since. Keeping the map minimal
// makes state inspectable and stops unbounded growth across long runs.
func (c *conductor) pruneSnoozes(s swSnapshot) {
	for name, since := range c.snoozed {
		sess, ok := s.session(name)
		if !ok || !isWaiting(sess.State) || !sess.Since.Equal(since) {
			delete(c.snoozed, name)
		}
	}
}

// step advances the conductor by one poll. ok=true carries the single
// switch-client to issue this tick.
func (c *conductor) step(s swSnapshot) (swAction, bool) {
	c.pruneSnoozes(s)
	c.resolveClient(s)
	if c.client == "" {
		return swAction{}, false
	}
	cur := s.Clients[c.client]
	queue := s.waitingQueue(c.snoozed)

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
		if cur != c.escortee {
			// The user moved themselves. Snooze the abandoned session's
			// current episode so the lobby doesn't bounce them right back.
			if sess, ok := s.session(c.escortee); ok && isWaiting(sess.State) {
				c.snoozed[c.escortee] = sess.Since
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
		}
	}
	return swAction{}, false
}

// statusLine summarizes the conductor for the lobby's bottom row.
func (c *conductor) statusLine(s swSnapshot) string {
	n := len(s.waitingQueue(c.snoozed))
	switch c.phase {
	case swPaused:
		return "paused — you navigated away; return here to resume"
	case swEscorting:
		return fmt.Sprintf("escorting → %s · %d waiting", c.escortee, n)
	}
	return fmt.Sprintf("conducting · %d waiting", n)
}
