package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// The web status page's data. The lobby's Update publishes every snapshot
// here; the HTTP handlers (webserver.go) and the headline worker
// (webheadline.go) read it. Nothing in this file touches tmux or bubbletea:
// the holder is the one seam between the TUI's goroutine and the server's.
// Design: docs/superpowers/specs/2026-09-25-switchboard-web-status-page-design.md.

// webHeadline is the fleet's LLM-written one-liner. Fingerprint is
// webFingerprint of the sessions it was written from, so a view can tell a
// current headline from one the fleet has moved past.
type webHeadline struct {
	Text        string
	At          time.Time
	Fingerprint string
}

type webFleet struct {
	mu       sync.RWMutex
	snap     swSnapshot
	rl       RateLimits
	rlOK     bool
	windows  []ModelWindow
	taken    time.Time
	headline webHeadline

	// title is the page's heading (web.title); privacy drops sessions the
	// page must not show (web.hide_private_orgs). Both are set once at
	// construction and never change.
	title   string
	privacy *webPrivacy
}

func newWebFleet() *webFleet { return &webFleet{title: defaultWebTitle} }

// publish replaces the fleet facts. Only the lobby's Update calls it.
func (w *webFleet) publish(snap swSnapshot, rl RateLimits, rlOK bool, windows []ModelWindow, taken time.Time) {
	// Filtered here, before anything reads it, so hidden sessions reach
	// neither the page, its counts, nor the headline model's prompt. snap is
	// a copy; reassigning its slice leaves the lobby's own untouched.
	snap.Sessions = w.privacy.visible(snap.Sessions)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.snap = snap
	w.rl = rl
	w.rlOK = rlOK
	w.windows = append([]ModelWindow(nil), windows...)
	w.taken = taken
}

// setHeadline replaces the headline. Only the headline worker calls it.
func (w *webFleet) setHeadline(h webHeadline) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.headline = h
}

// sessions is a copy of the current fleet, for the headline worker.
func (w *webFleet) sessions() []swSession {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return append([]swSession(nil), w.snap.Sessions...)
}

// view is the JSON shape of the current facts, built under the read lock so
// a request never sees a half-published snapshot.
func (w *webFleet) view() webFleetView {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return buildWebFleetView(w.snap, w.rl, w.rlOK, w.windows, w.taken, w.headline)
}

// The wire shape of GET /api/fleet. Times are RFC 3339 strings, "" (and
// omitted) when unknown, so the page never has to special-case a zero time.
type webFleetView struct {
	TakenAt  string           `json:"taken_at"`
	Headline *webHeadlineView `json:"headline,omitempty"`
	Counts   webCounts        `json:"counts"`
	Budget   *webBudget       `json:"budget,omitempty"`
	Sessions []webSession     `json:"sessions"`
}

type webHeadlineView struct {
	Text  string `json:"text"`
	At    string `json:"at"`
	Stale bool   `json:"stale"`
}

type webCounts struct {
	Sessions int `json:"sessions"`
	Waiting  int `json:"waiting"`
	Deferred int `json:"deferred"`
	Closing  int `json:"closing"`
}

// Color is the terminal's gauge color for the window (paceColor), so the
// page's bars band green/yellow/red exactly as the lobby's meters do.
type webWindow struct {
	UsedPct  int    `json:"used_pct"`
	ResetsAt string `json:"resets_at,omitempty"`
	Color    string `json:"color"`
}

type webModelWindow struct {
	Name     string `json:"name"`
	UsedPct  int    `json:"used_pct"`
	ResetsAt string `json:"resets_at,omitempty"`
	Color    string `json:"color"`
}

type webBudget struct {
	FiveHour webWindow        `json:"five_hour"`
	Weekly   webWindow        `json:"weekly"`
	Models   []webModelWindow `json:"models,omitempty"`
}

type webSession struct {
	Name        string `json:"name"`
	Color       string `json:"color,omitempty"`
	Emoji       string `json:"emoji,omitempty"`
	Description string `json:"description,omitempty"`
	State       string `json:"state"`
	StateEmoji  string `json:"state_emoji,omitempty"`
	StateRaw    string `json:"state_raw"`
	Waiting     bool   `json:"waiting"`
	Since       string `json:"since,omitempty"`
	ContextPct  *int   `json:"context_pct,omitempty"`
	CtxColor    string `json:"context_color,omitempty"`
	Model       string `json:"model,omitempty"`
	Topic       string `json:"topic,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	Deferred    bool   `json:"deferred"`
	DeferReason string `json:"defer_reason,omitempty"`
	// Closing is the head's teardown phase (closingPublishValue), omitted when
	// the session is not closing; ClosingLabel is that phase as the page
	// prints it.
	Closing      string `json:"closing,omitempty"`
	ClosingLabel string `json:"closing_label,omitempty"`
}

// defaultWebTitle is the page heading when web.title is unset.
const defaultWebTitle = "claudemux fleet"

// webModelName is the model as the lobby's rows print it ("opus 5.5"), ""
// when unknown rather than shortModel's em-dash placeholder.
func webModelName(m string) string {
	if m == "" {
		return ""
	}
	return shortModel(m)
}

func webTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// webStateWord is the state as the page prints it: the published value, or
// "unknown" for a head that has not published yet — the same word the lobby
// row uses (swStateText), minus its emoji cell.
func webStateWord(raw string) string {
	if raw == "" {
		return "unknown"
	}
	return raw
}

func buildWebFleetView(snap swSnapshot, rl RateLimits, rlOK bool, windows []ModelWindow, taken time.Time, h webHeadline) webFleetView {
	v := webFleetView{TakenAt: webTime(taken), Sessions: []webSession{}}
	for _, s := range snap.Sessions {
		ws := webSession{
			Name:        s.Name,
			Emoji:       s.Emoji,
			Description: s.Description,
			State:       webStateWord(s.State),
			StateRaw:    s.State,
			Waiting:     isWaiting(s.State),
			Since:       webTime(s.Since),
			Model:       webModelName(s.Model),
			Topic:       s.Topic,
			Summary:     s.Summary,
			Prompt:      s.Prompt,
			Deferred:    s.Deferred,
			Closing:     s.Closing,
			// The lobby's wording without its ⏻ glyph: the page's closing
			// section already carries one in its heading.
			ClosingLabel: strings.TrimPrefix(closingText(s.Closing), "⏻ "),
		}
		if isHex6(s.Color) {
			ws.Color = "#" + s.Color
		}
		// The lobby row's action symbol (bell, brain, wrench...), unpadded.
		if k, ok := publishedStateKind(s.State); ok {
			ws.StateEmoji = strings.TrimSpace(stateEmoji(k))
		}
		if s.Context >= 0 {
			pct := s.Context
			ws.ContextPct = &pct
			ws.CtxColor = thresholdColor(float64(pct))
		}
		if s.Deferred {
			ws.DeferReason = sanitizeDeferReason(s.DeferReason)
		}
		v.Sessions = append(v.Sessions, ws)
		v.Counts.Sessions++
		if ws.Waiting {
			v.Counts.Waiting++
		}
		// One list each, as the lobby groups them (swGroupOf): a session that
		// is both closing and deferred is counted where it is shown.
		switch swGroupOf(s) {
		case swGroupClosing:
			v.Counts.Closing++
		case swGroupDeferred:
			v.Counts.Deferred++
		}
	}
	if rlOK {
		now := taken
		if now.IsZero() {
			now = time.Now()
		}
		b := &webBudget{
			FiveHour: webWindow{UsedPct: rl.FiveHour.UsedPercent, ResetsAt: webTime(rl.FiveHour.ResetsAt),
				Color: paceColor(rl.FiveHour.UsedPercent, rl.FiveHour.ResetsAt, fiveHourWindow, now)},
			Weekly: webWindow{UsedPct: rl.SevenDay.UsedPercent, ResetsAt: webTime(rl.SevenDay.ResetsAt),
				Color: paceColor(rl.SevenDay.UsedPercent, rl.SevenDay.ResetsAt, weekWindow, now)},
		}
		for _, mw := range windows {
			b.Models = append(b.Models, webModelWindow{Name: mw.Name, UsedPct: mw.UsedPercent, ResetsAt: webTime(mw.ResetsAt),
				Color: paceColor(mw.UsedPercent, mw.ResetsAt, weekWindow, now)})
		}
		v.Budget = b
	}
	if h.Text != "" {
		v.Headline = &webHeadlineView{
			Text:  h.Text,
			At:    webTime(h.At),
			Stale: h.Fingerprint != webFingerprint(snap.Sessions),
		}
	}
	return v
}

// webFingerprint hashes what the headline is written from — the facts that
// describe what the fleet is DOING. Timers, context percentages and the raw
// prompt are left out on purpose: they move constantly, and each move must
// not look like a reason for another billable call. For the same reason a
// closing session contributes the fact that it is closing and not its phase,
// which steps through several values on the way out.
func webFingerprint(sessions []swSession) string {
	var b strings.Builder
	for _, s := range sessions {
		reason := ""
		if s.Deferred {
			reason = "deferred:" + sanitizeDeferReason(s.DeferReason)
		}
		closing := ""
		if s.Closing != "" {
			closing = "closing"
		}
		b.WriteString(strings.Join([]string{s.Name, webStateWord(s.State), s.Topic, s.Summary, reason, closing}, "\x1f"))
		b.WriteString("\x1e")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}
