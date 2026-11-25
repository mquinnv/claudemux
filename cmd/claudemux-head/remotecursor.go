package main

import "time"

// remoteCursor is the glasses' own place in the conductor's waiting queue.
// The lobby's conductor drives a tmux client through that queue; the glasses
// drive nothing, so they keep a cursor of their own: one pinned session, plus
// the sessions the wearer skipped. It is pure state; the caller holds the lock.
type remoteCursor struct {
	pinned string    // session currently shown, "" when none
	since  time.Time // the pinned session's waiting episode when it was pinned
	opened bool      // pinned from the fleet list: kept while the session exists, until answered or deferred
	skips  map[string]swSnooze
}

// pick returns the session the glasses should show and its 1-based position in
// the queue (0 when the session is not queued, as for an opened one that is
// not waiting). It keeps the pinned session while that session is still in the
// queue with the same Since (or, for an opened one, still exists); otherwise
// it pins the queue head. ok is false when there is nothing to show.
func (c *remoteCursor) pick(sessions []swSession, now time.Time) (sess swSession, pos, total int, ok bool) {
	snap := swSnapshot{Sessions: sessions}
	c.prune(snap, now)
	queue := snap.waitingQueue(c.skips, now)
	// As in the conductor, a skip says "someone else first": once nobody
	// else is left, every skip is released.
	if len(queue) == 0 && len(c.skips) > 0 {
		if released := snap.waitingQueue(nil, now); len(released) > 0 {
			c.skips = nil
			queue = released
		}
	}
	index := func(name string) int {
		for i, q := range queue {
			if q.Name == name {
				return i
			}
		}
		return -1
	}
	if c.pinned != "" {
		if c.opened {
			if s, found := snap.session(c.pinned); found {
				return s, index(s.Name) + 1, len(queue), true
			}
		} else if i := index(c.pinned); i >= 0 && queue[i].Since.Equal(c.since) {
			return queue[i], i + 1, len(queue), true
		}
	}
	if len(queue) == 0 {
		c.pinned, c.since, c.opened = "", time.Time{}, false
		return swSession{}, 0, 0, false
	}
	c.pinned, c.since, c.opened = queue[0].Name, queue[0].Since, false
	return queue[0], 1, len(queue), true
}

// skip walks away from a waiting episode, keyed by name and its Since, so a
// new episode of the same session is not hidden. The next pick moves on.
func (c *remoteCursor) skip(name string, since, now time.Time) {
	if c.skips == nil {
		c.skips = map[string]swSnooze{}
	}
	c.skips[name] = swSnooze{since: since, at: now}
	if c.pinned == name {
		c.pinned, c.since, c.opened = "", time.Time{}, false
	}
}

// open pins a session chosen from the fleet list, waiting or not.
func (c *remoteCursor) open(name string) {
	c.pinned, c.since, c.opened = name, time.Time{}, true
}

// release unpins name if it was opened from the fleet list, so the next pick
// goes back to the queue. Called after an answer or a defer took effect.
func (c *remoteCursor) release(name string) {
	if c.opened && c.pinned == name {
		c.pinned, c.since, c.opened = "", time.Time{}, false
	}
}

// prune drops skips whose episode ended or whose TTL expired, as the
// conductor's pruneSnoozes does.
func (c *remoteCursor) prune(s swSnapshot, now time.Time) {
	for name, sn := range c.skips {
		sess, ok := s.session(name)
		if !ok || !isWaiting(sess.State) || !sess.Since.Equal(sn.since) || now.Sub(sn.at) >= swSnoozeTTL {
			delete(c.skips, name)
		}
	}
}
