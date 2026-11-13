package main

import (
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Find mode (`/`): the status line collects a query, and every keystroke moves
// the selection to the session whose name best matches it, so `/rem` + enter
// jumps to remix without walking the list. The list is not filtered — rows
// keep their places, and the ones that do not match dim — so the fleet's
// layout (families, groups, the scroll window) stays exactly what the user
// knows, and esc puts the cursor back where it was.

// swFindRank scores how well name matches query, lower is better, -1 for no
// match: 0 a prefix, 1 a prefix of a later word (after - _ . / or space),
// 2 any substring, 3 the query's runes in order (a subsequence, so "rmx"
// finds remix). Case-insensitive. An empty query matches nothing: with no
// query yet there is nothing to rank, and every row would otherwise dim.
func swFindRank(name, query string) int {
	if query == "" {
		return -1
	}
	n, q := strings.ToLower(name), strings.ToLower(query)
	if strings.HasPrefix(n, q) {
		return 0
	}
	for i := 1; i < len(n); i++ {
		if strings.ContainsRune("-_./ ", rune(n[i-1])) && strings.HasPrefix(n[i:], q) {
			return 1
		}
	}
	if strings.Contains(n, q) {
		return 2
	}
	rest := []rune(q)
	for _, r := range n {
		if len(rest) > 0 && r == rest[0] {
			rest = rest[1:]
		}
	}
	if len(rest) == 0 {
		return 3
	}
	return -1
}

// swFindPRRank scores how well a session's PR number matches query, on the
// same scale as swFindRank: 0 the exact number, 1 a prefix of it ("#12" finds
// #123). A leading "#" is optional, so "123" and "#123" both work. Digits
// only — no substring or subsequence, since "2" would then hit every PR with
// a 2 in it. A bare "#" matches every session with a PR, at the weakest rank.
func swFindPRRank(pr int, query string) int {
	if pr <= 0 || query == "" {
		return -1
	}
	q := strings.TrimPrefix(query, "#")
	if q == "" {
		if query == "#" {
			return 3
		}
		return -1
	}
	for _, r := range q {
		if r < '0' || r > '9' {
			return -1
		}
	}
	n := strconv.Itoa(pr)
	switch {
	case n == q:
		return 0
	case strings.HasPrefix(n, q):
		return 1
	}
	return -1
}

// swFindSessionRank is a session's best rank for query, by name or PR number.
func swFindSessionRank(s swSession, query string) int {
	r := swFindRank(s.Name, query)
	if p := swFindPRRank(s.PRNumber, query); p >= 0 && (r < 0 || p < r) {
		r = p
	}
	return r
}

// swFindMatches returns the indexes of the sessions whose name or PR number
// matches query,
// best rank first and list order within a rank, so the first entry is where
// the selection lands and the cycle keys walk the rest in a stable order.
func swFindMatches(sessions []swSession, query string) []int {
	type hit struct{ idx, rank int }
	var hits []hit
	for i, s := range sessions {
		if r := swFindSessionRank(s, query); r >= 0 {
			hits = append(hits, hit{i, r})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].rank < hits[b].rank })
	out := make([]int, len(hits))
	for i, h := range hits {
		out[i] = h.idx
	}
	return out
}

// startFind opens find mode, remembering the selected session's name so esc
// can put the cursor back even if a poll reorders the list meanwhile.
func (m *swModel) startFind() {
	m.finding = true
	m.findInput = ""
	m.findPrevName = ""
	if m.sel >= 0 && m.sel < len(m.snap.Sessions) {
		m.findPrevName = m.snap.Sessions[m.sel].Name
	}
}

// endFind closes find mode, leaving the selection where it is.
func (m *swModel) endFind() {
	m.finding = false
	m.findInput = ""
	m.findPrevName = ""
}

// findSelect moves the selection to the match step places along from the
// current one (wrapping), or to the best match when the current selection is
// not a match. A query with no match leaves the selection alone.
func (m *swModel) findSelect(step int) tea.Cmd {
	matches := swFindMatches(m.snap.Sessions, m.findInput)
	if len(matches) == 0 {
		return nil
	}
	next := matches[0]
	if step != 0 {
		for i, idx := range matches {
			if idx == m.sel {
				next = matches[((i+step)%len(matches)+len(matches))%len(matches)]
				break
			}
		}
	}
	if next == m.sel {
		return nil
	}
	m.sel = next
	m.followSelection()
	return m.previewCmd()
}

// findKey handles one keystroke in find mode.
func (m swModel) findKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		prev := m.findPrevName
		m.endFind()
		for i, s := range m.snap.Sessions {
			if s.Name == prev && i != m.sel {
				m.sel = i
				m.followSelection()
				cmd := m.previewCmd()
				return m, cmd
			}
		}
		return m, nil
	case "enter":
		// Jump only to a session the query actually names: enter on a query
		// that matches nothing keeps the prompt open rather than jumping to
		// whatever row the cursor happened to be on.
		if len(swFindMatches(m.snap.Sessions, m.findInput)) == 0 {
			return m, nil
		}
		m.endFind()
		if m.sel < len(m.snap.Sessions) && m.cond.client != "" {
			return m, swSwitchCmd(m.cond.client, m.snap.Sessions[m.sel].Name, nil)
		}
		return m, nil
	case "down", "tab", "ctrl+n":
		cmd := m.findSelect(1)
		return m, cmd
	case "up", "shift+tab", "ctrl+p":
		cmd := m.findSelect(-1)
		return m, cmd
	case "backspace":
		r := []rune(m.findInput)
		if len(r) == 0 {
			m.endFind()
			return m, nil
		}
		m.findInput = string(r[:len(r)-1])
		cmd := m.findSelect(0)
		return m, cmd
	}
	if msg.Type == tea.KeyRunes && !msg.Alt {
		m.findInput += string(msg.Runes)
	} else if msg.Type == tea.KeySpace {
		m.findInput += " "
	} else {
		return m, nil
	}
	cmd := m.findSelect(0)
	return m, cmd
}

// findStatus is the status line while finding: the query, then how many
// sessions it matches.
func (m swModel) findStatus() string {
	s := "/" + m.findInput + "▌"
	if m.findInput == "" {
		return s
	}
	switch n := len(swFindMatches(m.snap.Sessions, m.findInput)); n {
	case 0:
		return s + swWaitStyle.Render("  no match")
	case 1:
		return s + swStatusStyle.Render("  1 match")
	default:
		return s + swStatusStyle.Render("  "+strconv.Itoa(n)+" matches")
	}
}
