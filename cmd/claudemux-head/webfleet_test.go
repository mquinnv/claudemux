package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func webTestSnapshot() swSnapshot {
	return swSnapshot{Sessions: []swSession{
		{Name: "api", State: "Idle", Since: time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC),
			Context: 37, Topic: "build fixes", Summary: "fixing the build", Prompt: "run <b>the</b> tests & go",
			Model: "claude-opus-4-7", Color: "8b5cf6", Emoji: "🔀"},
		{Name: "web", State: "Thinking", Since: time.Date(2026, 9, 25, 14, 1, 0, 0, time.UTC), Context: -1},
		{Name: "blocked", State: "Idle", Context: 12, Topic: "phenix deploy", Deferred: true, DeferReason: "waiting on\treview"},
	}}
}

func TestBuildWebFleetViewMapsSessions(t *testing.T) {
	snap := webTestSnapshot()
	taken := time.Date(2026, 9, 25, 14, 3, 0, 0, time.UTC)
	v := buildWebFleetView(snap, RateLimits{}, false, nil, taken, webHeadline{})

	if v.TakenAt != "2026-09-25T14:03:00Z" {
		t.Errorf("TakenAt = %q", v.TakenAt)
	}
	if v.Counts != (webCounts{Sessions: 3, Waiting: 2, Deferred: 1}) {
		t.Errorf("Counts = %+v", v.Counts)
	}
	if v.Budget != nil {
		t.Error("Budget must be nil when the rate-limit cache is unreadable")
	}
	if v.Headline != nil {
		t.Error("Headline must be nil before the first headline")
	}
	if len(v.Sessions) != 3 || v.Sessions[0].Name != "api" || v.Sessions[2].Name != "blocked" {
		t.Fatalf("Sessions order lost: %+v", v.Sessions)
	}
	api := v.Sessions[0]
	if api.Color != "#8b5cf6" || api.Emoji != "🔀" || api.State != "Idle" || api.StateRaw != "Idle" || !api.Waiting {
		t.Errorf("api = %+v", api)
	}
	if api.ContextPct == nil || *api.ContextPct != 37 {
		t.Errorf("api.ContextPct = %v, want 37", api.ContextPct)
	}
	if api.Since != "2026-09-25T14:00:00Z" {
		t.Errorf("api.Since = %q", api.Since)
	}
	if api.Prompt != "run <b>the</b> tests & go" {
		t.Errorf("Prompt must pass through unescaped for JSON: %q", api.Prompt)
	}
	web := v.Sessions[1]
	if web.ContextPct != nil {
		t.Error("ContextPct must be nil for an unpublished context (-1)")
	}
	if web.Waiting {
		t.Error("Thinking is not waiting")
	}
	blocked := v.Sessions[2]
	if !blocked.Deferred || blocked.DeferReason != "waiting on review" {
		t.Errorf("blocked = %+v, want deferred with a sanitized reason", blocked)
	}
}

func TestBuildWebFleetViewUnknownStateAndBadColor(t *testing.T) {
	snap := swSnapshot{Sessions: []swSession{{Name: "new", Context: -1, Color: "not-hex"}}}
	v := buildWebFleetView(snap, RateLimits{}, false, nil, time.Now(), webHeadline{})
	s := v.Sessions[0]
	if s.State != "unknown" || s.StateRaw != "" {
		t.Errorf("state = %q/%q, want unknown/\"\"", s.State, s.StateRaw)
	}
	if s.Color != "" {
		t.Errorf("Color = %q, want \"\" for a non-hex value", s.Color)
	}
	if s.Since != "" {
		t.Errorf("Since = %q, want \"\" for a zero time", s.Since)
	}
}

func TestBuildWebFleetViewBudgetAndHeadline(t *testing.T) {
	rl := RateLimits{
		FiveHour: Window{UsedPercent: 41, ResetsAt: time.Date(2026, 9, 25, 17, 0, 0, 0, time.UTC)},
		SevenDay: Window{UsedPercent: 63, ResetsAt: time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)},
	}
	windows := []ModelWindow{{Name: "opus", UsedPercent: 20, ResetsAt: time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)}}
	snap := webTestSnapshot()
	fp := webFingerprint(snap.Sessions)
	h := webHeadline{Text: "Fixing the build while a deploy waits on review.", At: time.Date(2026, 9, 25, 14, 2, 0, 0, time.UTC), Fingerprint: fp}
	v := buildWebFleetView(snap, rl, true, windows, time.Now(), h)

	if v.Budget == nil || v.Budget.FiveHour.UsedPct != 41 || v.Budget.Weekly.UsedPct != 63 {
		t.Fatalf("Budget = %+v", v.Budget)
	}
	if v.Budget.FiveHour.ResetsAt != "2026-09-25T17:00:00Z" {
		t.Errorf("FiveHour.ResetsAt = %q", v.Budget.FiveHour.ResetsAt)
	}
	if len(v.Budget.Models) != 1 || v.Budget.Models[0].Name != "opus" || v.Budget.Models[0].UsedPct != 20 {
		t.Errorf("Models = %+v", v.Budget.Models)
	}
	if v.Headline == nil || v.Headline.Text != h.Text || v.Headline.At != "2026-09-25T14:02:00Z" || v.Headline.Stale {
		t.Fatalf("Headline = %+v, want fresh", v.Headline)
	}

	snap.Sessions[0].Summary = "tests green, opening the PR"
	v = buildWebFleetView(snap, rl, true, windows, time.Now(), h)
	if v.Headline == nil || !v.Headline.Stale {
		t.Error("Headline.Stale must be true once the fleet's fingerprint moves past the headline's")
	}
}

func TestWebFleetViewJSONOmitsAbsentFacts(t *testing.T) {
	v := buildWebFleetView(webTestSnapshot(), RateLimits{}, false, nil, time.Now(), webHeadline{})
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, absent := range []string{`"budget"`, `"headline"`} {
		if strings.Contains(s, absent) {
			t.Errorf("JSON must omit %s when there is none: %s", absent, s)
		}
	}
	if strings.Count(s, `"context_pct"`) != 2 {
		t.Errorf("context_pct must appear for the two sessions that published one, got %d in %s", strings.Count(s, `"context_pct"`), s)
	}
	for _, present := range []string{`"taken_at"`, `"counts"`, `"sessions"`, `"state_raw":"Thinking"`, `"waiting":true`, `"defer_reason":"waiting on review"`} {
		if !strings.Contains(s, present) {
			t.Errorf("JSON missing %s: %s", present, s)
		}
	}
}

func TestWebFingerprint(t *testing.T) {
	base := webTestSnapshot().Sessions
	fp := webFingerprint(base)
	if fp == "" || fp != webFingerprint(base) {
		t.Fatal("fingerprint must be stable for identical input")
	}
	moved := webTestSnapshot().Sessions
	moved[0].Since = moved[0].Since.Add(time.Minute)
	moved[0].Context = 80
	moved[0].Prompt = "something else entirely"
	if webFingerprint(moved) != fp {
		t.Error("timers, context and prompt must not move the fingerprint")
	}
	changed := webTestSnapshot().Sessions
	changed[0].Summary = "opening the PR"
	if webFingerprint(changed) == fp {
		t.Error("a summary change must move the fingerprint")
	}
	state := webTestSnapshot().Sessions
	state[1].State = "Idle"
	if webFingerprint(state) == fp {
		t.Error("a state change must move the fingerprint")
	}
	undeferred := webTestSnapshot().Sessions
	undeferred[2].Deferred = false
	if webFingerprint(undeferred) == fp {
		t.Error("clearing a defer must move the fingerprint")
	}
	if webFingerprint(nil) == fp {
		t.Error("an empty fleet must not share a fingerprint with a populated one")
	}
}

func TestWebFleetPublishAndView(t *testing.T) {
	f := newWebFleet()
	if v := f.view(); len(v.Sessions) != 0 || v.Sessions == nil {
		t.Fatalf("empty holder must view as an empty (not null) session list: %+v", v.Sessions)
	}
	snap := webTestSnapshot()
	f.publish(snap, RateLimits{}, false, nil, time.Now())
	if got := f.sessions(); len(got) != 3 || got[0].Name != "api" {
		t.Fatalf("sessions() = %+v", got)
	}
	f.setHeadline(webHeadline{Text: "x", At: time.Now(), Fingerprint: webFingerprint(snap.Sessions)})
	v := f.view()
	if v.Headline == nil || v.Headline.Text != "x" || len(v.Sessions) != 3 {
		t.Fatalf("view() = %+v", v)
	}
}

func TestWebFleetViewFamilies(t *testing.T) {
	iai := withDefer(fam("iai-admin", 20, "", 0))
	iai.DeferReason = "waiting on beejax"
	bj := withDefer(fam("beejax", 40, "iai-admin", 20))
	bj.DeferReason = "needs review"
	snap := swSnapshot{Sessions: swArrangeFamilies([]swSession{
		fam("remix-3", 10, "", 0), iai, fam("remix-6", 30, "", 0),
		withDefer(fam("digiscreener", 5, "", 0)), bj,
		fam("usm", 41, "iai-admin", 20), fam("ag", 42, "iai-admin", 20),
		fam("lost", 50, "gone", 1),
	})}
	v := buildWebFleetView(snap, RateLimits{}, false, nil, time.Time{}, webHeadline{})
	by := map[string]webSession{}
	for _, s := range v.Sessions {
		by[s.Name] = s
	}
	b := by["beejax"]
	if b.Depth != 1 || b.Group != "active" || !b.Deferred || b.DeferReason != "needs review" {
		t.Errorf("beejax = %+v", b)
	}
	if by["digiscreener"].Group != "deferred" || by["iai-admin"].Group != "active" || by["iai-admin"].Depth != 0 {
		t.Errorf("groups/depth wrong: %+v %+v", by["digiscreener"], by["iai-admin"])
	}
	if by["lost"].From != "gone" || by["usm"].From != "" {
		t.Errorf("From: lost=%q usm=%q", by["lost"].From, by["usm"].From)
	}
	if v.Counts.Deferred != 3 {
		t.Errorf("Counts.Deferred = %d, want 3 (per session)", v.Counts.Deferred)
	}
	// A closing parent with a deferred (not closing) child: the family rides
	// in the closing list, and each row still says what it really is.
	cp := fam("winding", 60, "", 0)
	cp.Closing = "wrapup"
	cc := withDefer(fam("parked", 61, "winding", 60))
	cc.DeferReason = "waits on ci"
	cv := buildWebFleetView(swSnapshot{Sessions: swArrangeFamilies([]swSession{cp, cc})}, RateLimits{}, false, nil, time.Time{}, webHeadline{})
	if len(cv.Sessions) != 2 {
		t.Fatalf("closing family = %+v", cv.Sessions)
	}
	w, k := cv.Sessions[0], cv.Sessions[1]
	if w.Group != "closing" || w.Closing != "wrapup" || w.ClosingLabel == "" || w.Deferred {
		t.Errorf("winding = %+v", w)
	}
	if k.Group != "closing" || k.Closing != "" || k.ClosingLabel != "" || !k.Deferred || k.DeferReason != "waits on ci" || k.Depth != 1 {
		t.Errorf("parked = %+v", k)
	}
	kraw, _ := json.Marshal(k)
	if !strings.Contains(string(kraw), `"group":"closing"`) || !strings.Contains(string(kraw), `"deferred":true`) || strings.Contains(string(kraw), `"closing"`+":") {
		t.Errorf("parked json = %s", kraw)
	}
	raw, _ := json.Marshal(by["usm"])
	if !strings.Contains(string(raw), `"depth":1`) || !strings.Contains(string(raw), `"group":"active"`) || strings.Contains(string(raw), `"from"`) {
		t.Errorf("usm json = %s", raw)
	}
}
