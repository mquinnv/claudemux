package main

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Tab completion and linking for the blocker prompt that `d` and `D` open in
// both the head and the lobby. A blocker that is exactly another live
// session's name links the defer to that session, so the head can release it
// when the blocker is out of the way (deferwatch.go); anything else stays the
// free-text note it always was. Completion exists so that typing an exact
// name is easy and a near-miss is visible before enter, not after.

// deferCandidates orders the names Tab offers for self's blocker: self's
// handoff children first, then every other session, each group in rows'
// order. self is never included. Children lead because a session handed work
// to them and is most likely waiting on one of them.
func deferCandidates(self string, rows []fleetRow) []string {
	if self == "" {
		return nil
	}
	var kids, rest []string
	for _, r := range rows {
		switch {
		case r.Name == self:
		case r.Parent == self:
			kids = append(kids, r.Name)
		default:
			rest = append(rest, r.Name)
		}
	}
	return append(kids, rest...)
}

// deferCompleter is the cycling state of one blocker prompt. Zero value is
// ready; any edit other than Tab/Shift+Tab must call reset.
type deferCompleter struct {
	base    string // what was typed when the cycle began
	idx     int
	cycling bool
}

// next is one Tab (dir +1) or Shift+Tab (dir -1). The cycle matches against
// the text typed before the first Tab, not the completion now in the input,
// so repeated Tabs walk every match instead of locking onto the first one.
// No match leaves the input alone and starts no cycle, so the next Tab
// matches against whatever is typed then.
func (c *deferCompleter) next(input string, candidates []string, dir int) string {
	if !c.cycling {
		c.base = input
	}
	var matches []string
	for _, cand := range candidates {
		if strings.HasPrefix(strings.ToLower(cand), strings.ToLower(c.base)) {
			matches = append(matches, cand)
		}
	}
	if len(matches) == 0 {
		c.reset()
		return input
	}
	switch {
	case !c.cycling && dir < 0:
		c.idx = len(matches) - 1
	case !c.cycling:
		c.idx = 0
	default:
		// The candidates come from the last poll and can shrink between two
		// Tabs, so the index wraps against this call's matches.
		c.idx = ((c.idx+dir)%len(matches) + len(matches)) % len(matches)
	}
	c.cycling = true
	return matches[c.idx]
}

// reset ends a cycle, so the next Tab completes from what is typed then.
func (c *deferCompleter) reset() { *c = deferCompleter{} }

// deferLinkFor resolves a finished blocker to a link: set only when input
// is exactly the name of a live session other than self. Exact, case and
// all: a blocker that merely mentions a session ("waiting on phenix-2: …")
// is a note about it, not a promise to wait for it to finish. A row with no
// creation stamp cannot be told from a later session reusing its name, so it
// does not link either.
func deferLinkFor(input, self string, rows []fleetRow) deferLink {
	if self == "" || input == self {
		return deferLink{}
	}
	for _, r := range rows {
		if r.Name == input {
			if l := (deferLink{On: r.Name, Created: r.Created}); l.set() {
				return l
			}
			return deferLink{}
		}
	}
	return deferLink{}
}

// deferSubmit is what enter makes of the prompt's text: the reason as typed,
// and the link it names, if any. The link is judged on the sanitized text so
// a trailing space after a completion still links.
func deferSubmit(input, self string, rows []fleetRow) (reason string, link deferLink) {
	return input, deferLinkFor(sanitizeDeferReason(input), self, rows)
}

// deferSubmitCmd is the write enter makes in either prompt. editValue is the
// mark of the defer being edited (`D` on a deferred session), "" for a new
// defer. A new defer is the user's "1", linked or not. An edit keeps the
// mark's value; it relinks only when the text names a session, and otherwise
// rewrites the reason alone, because an edit that merely reworded a linked
// blocker must not silently unlink it.
func deferSubmitCmd(target, editValue, input, self string, rows []fleetRow) tea.Cmd {
	reason, link := deferSubmit(input, self, rows)
	switch {
	case editValue == "":
		return setDeferLinkedCmd(target, deferOnValue, reason, link)
	case link.set():
		return setDeferLinkedCmd(target, editValue, reason, link)
	default:
		return setDeferReasonCmd(target, reason)
	}
}

// deferPromptEdit applies one key other than enter/esc to an open blocker
// prompt, for both surfaces: Tab and Shift+Tab cycle through candidates,
// every other key ends the cycle and edits the text as deferInputKey does.
func deferPromptEdit(input string, c *deferCompleter, msg tea.KeyMsg, candidates []string) string {
	switch msg.String() {
	case "tab":
		return c.next(input, candidates, +1)
	case "shift+tab":
		return c.next(input, candidates, -1)
	}
	c.reset()
	return deferInputKey(input, msg)
}

// fleetFromSnapshot adapts the lobby's sessions to the rows the completer and
// linker read, in the lobby's row order. An unknown creation time (0) is kept
// as "0", which deferLink.set rejects: the name is still offered, it just
// cannot link.
func fleetFromSnapshot(sessions []swSession) []fleetRow {
	rows := make([]fleetRow, 0, len(sessions))
	for _, s := range sessions {
		rows = append(rows, fleetRow{
			Name: s.Name, Created: strconv.FormatInt(s.Created, 10), State: s.State,
			Deferred: s.Deferred, Parent: s.Parent,
		})
	}
	return rows
}
