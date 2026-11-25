package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// remoteOption is one thing the glasses can press.
type remoteOption struct {
	ID        string `json:"id"` // "key:2" or "reply:0" or "suggest:1"
	Label     string `json:"label"`
	Broad     bool   `json:"broad,omitempty"`
	Suggested bool   `json:"suggested,omitempty"`
}

// remoteDetail is what the glasses show for one session.
type remoteDetail struct {
	Session string         `json:"session"`
	Title   string         `json:"title"` // "<description or name> · <topic>"
	Kind    string         `json:"kind"`  // ask | permit | plan | idle | desk
	Body    string         `json:"body"`
	Note    string         `json:"note,omitempty"` // "answer at the desk", "prompt has a draft"
	Options []remoteOption `json:"options"`
	Episode string         `json:"episode"`
}

// remoteInputs is everything buildRemoteDetail reads; the caller gathers it
// from tmux and the transcript so the function itself stays pure.
type remoteInputs struct {
	Sess      swSession
	Capture   string // plain capture of ClaudePane
	CaptureE  string // -e capture, for promptEmpty
	Marker    askMarker
	MarkerOK  bool
	LastText  string // last assistant text from the transcript, "" if unknown
	Suggested []string
	Replies   []string // used as given: an empty list means no fixed replies
	VimMode   bool
}

const (
	remoteBodyRunes = 400
	remoteIdleLines = 8
	noteAtDesk      = "answer at the desk"
	noteDraft       = "prompt has a draft"
	noteWorking     = "working"
	noteSent        = "sent"
	noteCannotRead  = "cannot read pane"
)

// buildRemoteDetail decides what the glasses show and which options they may
// press. Anything it cannot read with certainty becomes kind desk with no
// options, because an option is a key the glasses will send.
func buildRemoteDetail(in remoteInputs) remoteDetail {
	d := remoteDetail{
		Session: in.Sess.Name,
		Title:   remoteTitle(in.Sess),
		Options: []remoteOption{},
	}
	dlg := parsePaneDialog(in.Capture)
	// full is the untrimmed text the episode covers: two dialogs that differ
	// only past the body's cut are still two episodes.
	full := dlg.Prompt

	switch dlg.Kind {
	case dialogAsk:
		full = fillAsk(&d, dlg, in)
	case dialogPermit:
		d.Kind = "permit"
		d.Body = headRunes(dlg.Prompt, remoteBodyRunes)
		d.Options = keyOptions(dlg)
	case dialogPlan:
		d.Kind = "plan"
		d.Body = headRunes(dlg.Prompt, remoteBodyRunes)
		d.Options = keyOptions(dlg)
	case dialogAskComplex, dialogUnknown:
		d.Kind, d.Note = "desk", noteAtDesk
		d.Body = headRunes(dlg.Prompt, remoteBodyRunes)
	default: // no dialog on screen
		switch {
		case in.Sess.State == "Idle":
			fillIdle(&d, in)
		case isWaiting(in.Sess.State):
			// Asking, Awaiting or an ask tool call, but no dialog we can
			// see: never guess at keys.
			d.Kind, d.Note = "desk", noteAtDesk
		default:
			// Opened from the fleet list while busy.
			fillIdle(&d, in)
			d.Note = noteWorking
			d.Options = []remoteOption{}
		}
		full = d.Body
	}

	// Suggested options are left out: they arrive from the model after the
	// glasses first saw the session, and must not turn a tap on a fixed reply
	// into a stale episode. The sender still requires the exact option ID in
	// the rebuilt detail, and suggestions come only from the cache.
	var labels []string
	for _, o := range d.Options {
		if !o.Suggested {
			labels = append(labels, o.Label)
		}
	}
	d.Episode = remoteEpisode(in.Sess.Since, d.Kind, full, labels)
	return d
}

func remoteTitle(s swSession) string {
	t := s.Description
	if t == "" {
		t = s.Name
	}
	if s.Topic != "" {
		t += " · " + s.Topic
	}
	return t
}

// fillAsk handles a single-select, single-question AskUserQuestion. Labels
// come from the hook marker when it carries them, because the pane parse
// drops wrapped continuation lines and so can truncate a long label. It
// returns the untrimmed question the episode covers.
func fillAsk(d *remoteDetail, dlg paneDialog, in remoteInputs) string {
	full := dlg.Prompt
	desk := func() string {
		d.Kind, d.Note = "desk", noteAtDesk
		d.Body = headRunes(dlg.Prompt, remoteBodyRunes)
		d.Options = []remoteOption{}
		return dlg.Prompt
	}
	var rows []dialogRow
	for _, r := range dlg.Rows {
		if !r.FreeText {
			rows = append(rows, r)
		}
	}
	d.Kind = "ask"
	d.Body = headRunes(dlg.Prompt, remoteBodyRunes)

	labels := make([]string, len(rows))
	for i, r := range rows {
		labels[i] = r.Label
	}
	if in.MarkerOK && len(in.Marker.Questions) > 0 {
		qs := in.Marker.Questions
		if len(qs) != 1 || qs[0].MultiSelect || len(qs[0].Options) != len(rows) {
			return desk()
		}
		q := qs[0]
		for i, o := range q.Options {
			if !strings.HasPrefix(o.Label, rows[i].Label) {
				return desk()
			}
			labels[i] = o.Label
		}
		if q.Question != "" {
			d.Body = headRunes(q.Question, remoteBodyRunes)
			full = q.Question
		}
	}
	if len(rows) == 0 {
		return desk()
	}
	for i, r := range rows {
		d.Options = append(d.Options, remoteOption{ID: "key:" + strconv.Itoa(r.N), Label: labels[i]})
	}
	return full
}

// remoteBodyFitRunes is roughly what the glasses' three body lines show. A
// permit prompt longer than this cannot have been read in full, so its plain
// Yes takes a second tap too.
const remoteBodyFitRunes = 130

// The lobby's estimate of the glasses' body region. The glasses lay text out
// in pixels with the firmware's font metrics (glasses/src/layout.ts) and ask
// for a second tap themselves when a permit body was cut; this 46-column,
// three-line count (remoteBodyLines matches BODY_LINES in glasses/src/view.ts)
// is only a backstop for a client that does not. A permit dialog's title,
// file name and command or diff must fit in it for its plain Yes to be a
// single tap.
const (
	remoteBodyCols  = 46
	remoteBodyLines = 3
)

// remoteWrapLines counts the lines glasses/src/layout.ts wrap breaks text
// into at cols columns, before its line cap: "\n" is a hard break, each
// paragraph is word-wrapped (an empty one is one line), a word longer than
// cols is split into cols-wide pieces. Blank text is no lines. Width is in
// runes; layout.ts counts UTF-16 units, which only differ outside the BMP.
func remoteWrapLines(text string, cols int) int {
	if strings.TrimSpace(text) == "" || cols <= 0 {
		return 0
	}
	n := 0
	for _, par := range strings.Split(text, "\n") {
		words := strings.Fields(par)
		if len(words) == 0 {
			n++
			continue
		}
		cur := 0 // width of the line being built; 0 means empty
		for _, w := range words {
			wl := utf8.RuneCountInString(w)
			if cur > 0 && cur+1+wl <= cols {
				cur += 1 + wl
				continue
			}
			if cur > 0 {
				n++
			}
			if wl <= cols {
				cur = wl
				continue
			}
			pieces := (wl + cols - 1) / cols
			n += pieces - 1 // all but the last piece are full lines
			cur = wl - (pieces-1)*cols
		}
		if cur > 0 {
			n++
		}
	}
	return n
}

// keyOptions lists a permit or plan dialog's offerable rows as key options:
// never a free-text row, and on a plan dialog only rows that say Yes. Each
// option's digit is its row's number, not its position in the list.
func keyOptions(dlg paneDialog) []remoteOption {
	opts := []remoteOption{}
	long := dlg.Kind == dialogPermit && (utf8.RuneCountInString(dlg.Prompt) > remoteBodyFitRunes ||
		remoteWrapLines(dlg.Essential, remoteBodyCols) > remoteBodyLines)
	for _, r := range dlg.Rows {
		if r.FreeText || (dlg.Kind == dialogPlan && !strings.HasPrefix(r.Label, "Yes")) {
			continue
		}
		broad := broadChoice(dlg.Kind, r, len(opts) == 0)
		if long && r.Label == "Yes" {
			broad = true
		}
		opts = append(opts, remoteOption{
			ID:    "key:" + strconv.Itoa(r.N),
			Label: r.Label,
			Broad: broad,
		})
	}
	return opts
}

func fillIdle(d *remoteDetail, in remoteInputs) {
	d.Kind = "idle"
	d.Body = tailRunes(strings.TrimSpace(in.LastText), remoteBodyRunes)
	if d.Body == "" {
		d.Body = strings.TrimSpace(strings.Join(previewTail(in.Capture, remoteIdleLines), "\n"))
	}
	if !promptEmpty(in.CaptureE, in.VimMode) {
		d.Note = noteDraft
		return
	}
	// Fixed replies first: suggestions arrive a poll or two later, and
	// appended below they never move a row the cursor may be on.
	for i, s := range in.Replies {
		d.Options = append(d.Options, remoteOption{ID: fmt.Sprintf("reply:%d", i), Label: s})
	}
	for i, s := range in.Suggested {
		d.Options = append(d.Options, remoteOption{ID: fmt.Sprintf("suggest:%d", i), Label: s, Suggested: true})
	}
}

// broadChoice reports whether a row grants more than the one thing asked. On
// a permit dialog a row is narrow only when it is the first offered
// (non-free-text) row and reads exactly "Yes", or reads exactly "No"; every
// other offered row is broad. On a plan dialog row 1 (Yes, and use auto mode)
// is broad. first says whether row is the first non-free-text row; it does not
// depend on free-text rows elsewhere in the dialog.
func broadChoice(kind dialogKind, row dialogRow, first bool) bool {
	switch kind {
	case dialogPermit:
		if row.Label == "No" {
			return false
		}
		return !(first && row.Label == "Yes")
	case dialogPlan:
		return row.N == 1
	}
	return false
}

// remoteEpisode identifies one waiting episode: the same since, kind, body
// and option labels hash the same, and any change gives a new value.
func remoteEpisode(since time.Time, kind, body string, labels []string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00%s\x00%s", since.UnixNano(), kind, body)
	for _, l := range labels {
		h.Write([]byte{0})
		h.Write([]byte(l))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// tailRunes keeps the last n runes of s, starting at a word boundary.
func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := r[len(r)-n:]
	if !unicode.IsSpace(r[len(r)-n-1]) {
		// the cut fell inside a word: drop its remainder
		for i, c := range cut {
			if unicode.IsSpace(c) {
				cut = cut[i:]
				break
			}
		}
	}
	return strings.TrimSpace(string(cut))
}

// headRunes keeps the first n runes of s, ending at a word boundary, with
// "..." marking the cut. Used for permit prompts, where the command at the
// top matters most.
func headRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := r[:n]
	if !unicode.IsSpace(r[n]) {
		for i := len(cut) - 1; i >= 0; i-- {
			if unicode.IsSpace(cut[i]) {
				cut = cut[:i]
				break
			}
		}
	}
	return strings.TrimSpace(string(cut)) + "..."
}
