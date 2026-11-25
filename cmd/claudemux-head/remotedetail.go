package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
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

	switch dlg.Kind {
	case dialogAsk:
		fillAsk(&d, dlg, in)
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
	}

	labels := make([]string, len(d.Options))
	for i, o := range d.Options {
		labels[i] = o.Label
	}
	d.Episode = remoteEpisode(in.Sess.Since, d.Kind, d.Body, labels)
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
// drops wrapped continuation lines and so can truncate a long label.
func fillAsk(d *remoteDetail, dlg paneDialog, in remoteInputs) {
	desk := func() {
		d.Kind, d.Note = "desk", noteAtDesk
		d.Body = headRunes(dlg.Prompt, remoteBodyRunes)
		d.Options = []remoteOption{}
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
			desk()
			return
		}
		q := qs[0]
		for i, o := range q.Options {
			if !strings.HasPrefix(o.Label, rows[i].Label) {
				desk()
				return
			}
			labels[i] = o.Label
		}
		if q.Question != "" {
			d.Body = headRunes(q.Question, remoteBodyRunes)
		}
	}
	if len(rows) == 0 {
		desk()
		return
	}
	for i, r := range rows {
		d.Options = append(d.Options, remoteOption{ID: "key:" + strconv.Itoa(r.N), Label: labels[i]})
	}
}

// keyOptions lists a permit or plan dialog's typeable-free rows as key options.
func keyOptions(dlg paneDialog) []remoteOption {
	opts := []remoteOption{}
	for _, r := range dlg.Rows {
		if r.FreeText {
			continue
		}
		opts = append(opts, remoteOption{
			ID:    "key:" + strconv.Itoa(r.N),
			Label: r.Label,
			Broad: broadChoice(dlg.Kind, r, len(dlg.Rows)),
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
	for i, s := range in.Suggested {
		d.Options = append(d.Options, remoteOption{ID: fmt.Sprintf("suggest:%d", i), Label: s, Suggested: true})
	}
	for i, s := range in.Replies {
		d.Options = append(d.Options, remoteOption{ID: fmt.Sprintf("reply:%d", i), Label: s})
	}
}

// broadChoice reports whether a row grants more than the one thing asked: on
// a permit dialog anything between Yes and No, on a plan dialog row 1 (Yes,
// and use auto mode). rowCount is the number of rows in the dialog.
func broadChoice(kind dialogKind, row dialogRow, rowCount int) bool {
	switch kind {
	case dialogPermit:
		return row.N != 1 && row.N != rowCount
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
