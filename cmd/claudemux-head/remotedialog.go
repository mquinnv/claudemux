package main

import (
	"regexp"
	"strconv"
	"strings"
)

// dialogKind is what a plain pane capture shows at its bottom.
type dialogKind int

const (
	dialogNone       dialogKind = iota
	dialogAsk                   // AskUserQuestion, one single-select question
	dialogAskComplex            // multi-select or several questions
	dialogPermit
	dialogPlan
	dialogUnknown // a dialog is up but its layout is not recognised
)

// dialogRow is one numbered choice.
type dialogRow struct {
	N        int    // the digit that selects it
	Label    string // wrapped continuation lines joined with one space
	FreeText bool   // needs typing; never offered
}

// paneDialog is a dialog read from a capture. Rows is empty unless Kind is
// dialogAsk, dialogPermit or dialogPlan.
type paneDialog struct {
	Kind   dialogKind
	Prompt string // the question; for a permit dialog title, command, description, question (see permitPrompt). Never trimmed.
	Rows   []dialogRow
}

var dialogRowRe = regexp.MustCompile(`^\s*(?:\x{276f}\s*)?(\d+)\.\s+(\S.*)$`)

const (
	dialogPlanFooter   = "ctrl+g to edit in Vim · "
	dialogPlanQuestion = "Claude has written up a plan and is ready to execute"
)

// parsePaneDialog reads the dialog at the bottom of a plain tmux capture
// (no -e). Its result decides which keys a remote device may press, so every
// doubt resolves to dialogUnknown with no rows: the footer must be the last
// non-blank line, the dialog must have an opening rule, the numbered rows must
// be a contiguous block 1..K directly above the footer, and the kind-specific
// markers recorded in the Even G2 spike findings must all be present.
func parsePaneDialog(capture string) paneDialog {
	lines := strings.Split(capture, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	footer := len(lines) - 1
	for footer >= 0 && lines[footer] == "" {
		footer--
	}
	if footer < 0 {
		return paneDialog{}
	}
	foot := strings.TrimSpace(lines[footer])

	var kind dialogKind
	switch {
	case strings.HasPrefix(foot, dialogPlanFooter):
		kind = dialogPlan
	case strings.Contains(foot, "Esc to cancel") && strings.Contains(foot, "Enter to select"):
		kind = dialogAsk
	case strings.HasPrefix(foot, "Esc to cancel") && strings.Contains(foot, "Tab to amend"):
		// Without "· Tab to amend" a row has been Tab-amended into a text
		// field, where what a digit does was never measured.
		kind = dialogPermit
	case strings.Contains(foot, "Esc to cancel"), strings.Contains(foot, "Enter to select"):
		return paneDialog{Kind: dialogUnknown}
	default:
		return paneDialog{}
	}
	unknown := paneDialog{Kind: dialogUnknown}

	// The dialog opens at the last full-width rule. An AskUserQuestion has one
	// more rule, directly above its trailing "Chat about this" row, so there
	// the opener is the rule before it.
	last := -1
	for i := footer - 1; i >= 0; i-- {
		if isRuleLine(lines[i]) {
			last = i
			break
		}
	}
	if last < 0 {
		return unknown
	}
	opener := last
	if kind == dialogAsk {
		var between []string
		for _, l := range lines[last+1 : footer] {
			if l != "" {
				between = append(between, l)
			}
		}
		if len(between) == 1 {
			if m := dialogRowRe.FindStringSubmatch(between[0]); m != nil && strings.HasPrefix(m[2], "Chat about this") {
				opener = -1
				for i := last - 1; i >= 0; i-- {
					if isRuleLine(lines[i]) {
						opener = i
						break
					}
				}
				if opener < 0 {
					return unknown
				}
			}
		}
	}

	// Rows: the contiguous block of numbered rows and their indented
	// continuation lines directly above the footer, read bottom-up so text
	// further up (a command, a plan, scrollback) never counts.
	type rawRow struct {
		n     int
		label string
		conts []string
	}
	var rev []rawRow
	var pending []string // continuation lines seen, newest-first, not yet owned
	rowTop := -1
	i := footer - 1
	for i > opener && lines[i] == "" {
		i--
	}
	for ; i > opener; i-- {
		l := lines[i]
		if l == "" {
			break
		}
		if isRuleLine(l) {
			continue // the ask dialog's inner rule
		}
		if m := dialogRowRe.FindStringSubmatch(l); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil || n > 9 {
				return unknown
			}
			r := rawRow{n: n, label: strings.TrimSpace(m[2])}
			for j := len(pending) - 1; j >= 0; j-- {
				r.conts = append(r.conts, pending[j])
			}
			pending = nil
			rev = append(rev, r)
			rowTop = i
			if n == 1 {
				break // the block starts at 1; anything above is not a row
			}
			continue
		}
		if len(l)-len(strings.TrimLeft(l, " ")) >= 4 {
			pending = append(pending, strings.TrimSpace(l))
			continue
		}
		break
	}
	if len(rev) < 2 || rowTop < 0 {
		return unknown
	}
	rows := make([]rawRow, len(rev))
	for k, r := range rev {
		rows[len(rev)-1-k] = r
	}
	for k, r := range rows {
		if r.n != k+1 {
			return unknown
		}
	}

	var head []string // non-blank lines between the opening rule and the rows
	for _, l := range lines[opener+1 : rowTop] {
		if t := strings.TrimSpace(l); t != "" {
			head = append(head, t)
		}
	}

	build := func(joinWraps bool) []dialogRow {
		out := make([]dialogRow, len(rows))
		for k, r := range rows {
			label := r.label
			if joinWraps && len(r.conts) > 0 {
				label += " " + strings.Join(r.conts, " ")
			}
			out[k] = dialogRow{N: r.n, Label: label, FreeText: freeTextLabel(label)}
		}
		return out
	}

	switch kind {
	case dialogAsk:
		if len(head) < 2 {
			return unknown
		}
		header := head[0]
		complex := strings.Contains(header, "←") || strings.Contains(header, "→") ||
			strings.Contains(header, "✔ Submit")
		for _, r := range rows {
			if strings.HasPrefix(r.label, "[") {
				complex = true
			}
		}
		prompt := strings.Join(head[1:], " ")
		if complex {
			return paneDialog{Kind: dialogAskComplex, Prompt: prompt}
		}
		if !strings.HasPrefix(header, "☐ ") || !strings.Contains(foot, "↑/↓ to navigate") {
			return unknown
		}
		// Option descriptions sit where wraps would, so they are dropped.
		out := build(false)
		if len(out) < 3 ||
			!strings.HasPrefix(out[len(out)-2].Label, "Type something") ||
			!strings.HasPrefix(out[len(out)-1].Label, "Chat about this") {
			return unknown
		}
		return paneDialog{Kind: dialogAsk, Prompt: prompt, Rows: out}

	case dialogPermit:
		// The opener must be followed directly by a known title; a rule line
		// inside a command would otherwise pass for the opener and hide
		// everything above it.
		if !permitTitles[strings.TrimSpace(lines[opener+1])] {
			return unknown
		}
		q := -1
		for k, h := range head {
			if strings.HasPrefix(h, "Do you want") {
				q = k
			}
		}
		if q < 0 || !strings.HasSuffix(head[len(head)-1], "?") {
			return unknown
		}
		out := build(true)
		if !strings.HasPrefix(out[0].Label, "Yes") || !strings.HasPrefix(out[len(out)-1].Label, "No") {
			return unknown
		}
		for _, r := range out {
			if r.FreeText {
				return unknown
			}
		}
		return paneDialog{Kind: dialogPermit, Prompt: permitPrompt(head), Rows: out}

	default: // dialogPlan
		q := -1
		for k, h := range head {
			if strings.HasPrefix(h, dialogPlanQuestion) {
				q = k
			}
		}
		if q < 0 {
			return unknown
		}
		// The hint under the text row is a description, not a wrap.
		return paneDialog{Kind: dialogPlan, Prompt: strings.Join(head[q:], " "), Rows: build(false)}
	}
}

// permitTitles are the permission dialogs whose layout and keys the Even G2
// spike recorded (Bash, Edit, Write). Any other title is answered at the desk.
var permitTitles = map[string]bool{
	"Bash command": true,
	"Edit file":    true,
	"Create file":  true,
}

// permitPrompt orders a permission dialog's head lines for a small screen:
// the title, then the command or diff (between the dashed rules), then the
// model-written description, then the question. Tip lines and the dashed
// rules are dropped. head[0] is the title and the question is the last
// "Do you want" line.
func permitPrompt(head []string) string {
	q := len(head) - 1
	for k, h := range head {
		if strings.HasPrefix(h, "Do you want") {
			q = k
		}
	}
	isDash := func(h string) bool { return strings.Trim(h, "╌") == "" }
	mid := head[1:q]
	first, last := -1, -1
	for k, h := range mid {
		if isDash(h) {
			if first < 0 {
				first = k
			}
			last = k
		}
	}
	var command, desc []string
	for k, h := range mid {
		switch {
		case isDash(h) || strings.HasPrefix(h, "Tip:"):
		case first >= 0 && first < last && k > first && k < last:
			command = append(command, h)
		default:
			desc = append(desc, h)
		}
	}
	out := append([]string{head[0]}, command...)
	out = append(out, desc...)
	out = append(out, head[q:]...)
	return strings.Join(out, "\n")
}

// freeTextLabel reports whether choosing a row means typing into it. The
// permission dialog's "tell Claude" rows exist only after Tab amends a row.
func freeTextLabel(label string) bool {
	l := strings.ToLower(label)
	return strings.HasPrefix(l, "type something") ||
		strings.HasPrefix(l, "chat about this") ||
		strings.HasPrefix(l, "tell claude what to change") ||
		strings.Contains(l, "tell claude what to do")
}
