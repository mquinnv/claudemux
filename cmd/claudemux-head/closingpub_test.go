package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestClosingPublishValue(t *testing.T) {
	cases := []struct {
		phase   teardownPhase
		blocked bool
		want    string
	}{
		{teardownIdle, false, ""},
		// A blocked reading left over from an aborted teardown must not
		// publish: idle is idle.
		{teardownIdle, true, ""},
		{teardownSent, false, "wrapup"},
		{teardownSent, true, "blocked"},
		{teardownReady, false, "ready"},
		{teardownExiting, false, "exiting"},
		{teardownDirect, false, "direct"},
	}
	for _, c := range cases {
		if got := closingPublishValue(c.phase, c.blocked); got != c.want {
			t.Errorf("closingPublishValue(%v, %v) = %q, want %q", c.phase, c.blocked, got, c.want)
		}
	}
}

func TestClosingArgs(t *testing.T) {
	got, ok := closingArgs("%3", "ready")
	if want := []string{"set-option", "-t", "%3", closingOption, "ready"}; !ok || !slices.Equal(got, want) {
		t.Errorf("closingArgs set = %v ok=%v, want %v", got, ok, want)
	}
	// Idle unsets the option rather than writing "": the lobby reads an unset
	// option and an empty one alike, but an unset one leaves nothing behind.
	got, ok = closingArgs("%3", "")
	if want := []string{"set-option", "-t", "%3", "-u", closingOption}; !ok || !slices.Equal(got, want) {
		t.Errorf("closingArgs unset = %v ok=%v, want %v", got, ok, want)
	}
	if _, ok := closingArgs("", "ready"); ok {
		t.Error("closingArgs outside tmux must report nothing to do")
	}
}

func TestMaybePublishClosingPublishesOnChangeOnly(t *testing.T) {
	m := &model{selfPane: "%3"}
	if m.maybePublishClosing() != nil {
		t.Fatal("an idle head with nothing published has nothing to say")
	}
	m.teardown = teardownSent
	if m.maybePublishClosing() == nil || m.publishedClosing != "wrapup" {
		t.Fatalf("arming must publish, publishedClosing=%q", m.publishedClosing)
	}
	if m.maybePublishClosing() != nil {
		t.Fatal("an unchanged phase must not republish")
	}
	m.teardownBlocked = true
	if m.maybePublishClosing() == nil || m.publishedClosing != "blocked" {
		t.Fatalf("a blocked gate must republish, publishedClosing=%q", m.publishedClosing)
	}
	m.teardown, m.teardownBlocked = teardownIdle, false
	if m.maybePublishClosing() == nil || m.publishedClosing != "" {
		t.Fatalf("an abort must clear the mark, publishedClosing=%q", m.publishedClosing)
	}
}

func TestMaybePublishClosingOutsideTmux(t *testing.T) {
	m := &model{teardown: teardownReady}
	if m.maybePublishClosing() != nil {
		t.Error("no pane, nothing to publish against")
	}
}

// The publish rides on the one-second tick, so every teardown transition — a
// key, a probe result, an abort — reaches the lobby whichever path made it.
func TestTickPublishesClosing(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownDirect
	next, _ := m.Update(tickMsg(time.Now()))
	if got := next.(model).publishedClosing; got != "direct" {
		t.Errorf("publishedClosing = %q after a tick in teardownDirect, want direct", got)
	}
	next, _ = next.(model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	next, _ = next.(model).Update(tickMsg(time.Now()))
	if got := next.(model).publishedClosing; got != "" {
		t.Errorf("publishedClosing = %q after esc cancelled the teardown, want cleared", got)
	}
}

// Closing parses from the fourteenth field. A 13-field listing (a lobby
// fixture older than the option) still parses, with nothing closing.
func TestBuildSwSnapshotParsesClosing(t *testing.T) {
	sessOut := "api\tIdle\t1754700000\t37\t\t\t\t\t\t\t\t\t\tready\n" +
		"web\tThinking\t1754700000\t37\t\t\t\t\t\t\t\t\t\t\n" +
		"old\tIdle\t1754700000\t37\t\t\t\t\t\t\t\t\t\n"
	paneOut := "api\t%1\tclaudemux-head\tt\n" +
		"web\t%2\tclaudemux-head\tt\n" +
		"old\t%3\tclaudemux-head\tt\n"
	s := buildSwSnapshot(sessOut, paneOut, "", "")
	if len(s.Sessions) != 3 {
		t.Fatalf("parsed %d sessions, want 3: %+v", len(s.Sessions), s.Sessions)
	}
	if api, _ := s.session("api"); api.Closing != "ready" {
		t.Errorf("api.Closing = %q, want ready", api.Closing)
	}
	if web, _ := s.session("web"); web.Closing != "" {
		t.Errorf("web.Closing = %q, want empty for an unset option", web.Closing)
	}
	if old, _ := s.session("old"); old.Closing != "" {
		t.Errorf("old.Closing = %q, want empty for a 13-field line", old.Closing)
	}
}

// Three groups, each keeping tmux's order: active, closing, deferred. A
// session that is both closing and deferred belongs with the closing ones —
// the teardown is the newer act.
func TestSwSortSessionsGroupsClosingBetweenActiveAndDeferred(t *testing.T) {
	sessions := []swSession{
		{Name: "def1", Deferred: true},
		{Name: "close1", Closing: "wrapup"},
		{Name: "act1"},
		{Name: "both", Deferred: true, Closing: "ready"},
		{Name: "def2", Deferred: true},
		{Name: "act2"},
		{Name: "close2", Closing: "blocked"},
	}
	swSortSessions(sessions)
	var got []string
	for _, s := range sessions {
		got = append(got, s.Name)
	}
	want := []string{"act1", "act2", "close1", "both", "close2", "def1", "def2"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestSwDividerAt(t *testing.T) {
	fleet := []swSession{
		{Name: "a"}, {Name: "c1", Closing: "ready"}, {Name: "c2", Closing: "wrapup"},
		{Name: "d1", Deferred: true}, {Name: "d2", Deferred: true},
	}
	want := []string{"", swClosingDividerLabel, "", swDividerLabel, ""}
	for i := range fleet {
		if got := swDividerAt(fleet, i); got != want[i] {
			t.Errorf("swDividerAt(fleet, %d) = %q, want %q", i, got, want[i])
		}
	}
	// A group that opens the list has nothing above it to be divided from,
	// but the group after it still does.
	noActive := fleet[1:]
	if got := swDividerAt(noActive, 0); got != "" {
		t.Errorf("a closing group at the top needs no divider, got %q", got)
	}
	if got := swDividerAt(noActive, 2); got != swDividerLabel {
		t.Errorf("deferred after closing still needs its divider, got %q", got)
	}
}

func TestClosingText(t *testing.T) {
	for v, want := range map[string]string{
		"wrapup":  "⏻ wrapping up…",
		"blocked": "⏻ wrap-up blocked",
		"ready":   "⏻ press x to tear down",
		"exiting": "⏻ exiting claude…",
		"direct":  "⏻ kill session? press X",
		// A value from a newer head still reads as closing.
		"someday": "⏻ closing",
	} {
		if got := closingText(v); got != want {
			t.Errorf("closingText(%q) = %q, want %q", v, got, want)
		}
	}
	if got := closingText(""); got != "" {
		t.Errorf("closingText(\"\") = %q, want empty", got)
	}
}

func TestSwDetailLineLeadsWithClosingPhase(t *testing.T) {
	sess := swSession{Name: "api", Closing: "ready", Summary: "wrapping up the branch"}
	got := ansi.Strip(swDetailLine(sess))
	if want := "⏻ press x to tear down · wrapping up the branch"; got != want {
		t.Errorf("detail = %q, want %q", got, want)
	}
	if swSessionRows(swSession{Name: "bare", Closing: "wrapup"}) != 2 {
		t.Error("a closing row always has a detail line: the phase")
	}
}

func TestSwModelViewDrawsClosingDividerAndBadge(t *testing.T) {
	m := swTestModel()
	m.width = 140 // wide enough that the badge column is not clipped
	m.snap.Sessions = []swSession{
		{Name: "api", State: "Idle", Context: -1},
		{Name: "web", State: "Idle", Context: -1, Closing: "ready"},
		{Name: "scratch", State: "Idle", Context: -1, Deferred: true},
	}
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	row := func(name string) int {
		for i, l := range lines {
			if strings.Contains(l, " "+name+" ") {
				return i
			}
		}
		t.Fatalf("no row for %s:\n%s", name, strings.Join(lines, "\n"))
		return -1
	}
	web, scratch := row("web"), row("scratch")
	if !strings.Contains(lines[web-1], swClosingDividerLabel) {
		t.Errorf("line above the first closing row = %q, want the closing divider", lines[web-1])
	}
	if !strings.Contains(lines[web], "READY") {
		t.Errorf("closing row lacks its badge: %q", lines[web])
	}
	if !strings.Contains(lines[web+1], "press x to tear down") {
		t.Errorf("closing row's detail line = %q, want the phase", lines[web+1])
	}
	if !strings.Contains(lines[scratch-1], swDividerLabel) {
		t.Errorf("line above the first deferred row = %q, want the deferred divider", lines[scratch-1])
	}
	if strings.Contains(lines[row("api")], "READY") {
		t.Error("an active row must not carry the closing badge")
	}
}

// Both dividers cost a line each, and the list budget has to pay for both.
func TestSwModelViewBothDividersCountedInListBudget(t *testing.T) {
	m := swPreviewModel()
	m.height = 26
	var sessions []swSession
	for i := 0; i < 30; i++ {
		s := swSession{Name: fmt.Sprintf("sess-%02d", i), State: "Idle", Context: -1, ClaudePane: "%2"}
		switch {
		case i >= 28:
			s.Deferred = true
		case i >= 26:
			s.Closing = "wrapup"
		}
		sessions = append(sessions, s)
	}
	m.snap.Sessions = sessions
	m.sel = 29
	raw := m.View()
	plain := ansi.Strip(raw)
	if !strings.Contains(plain, swClosingDividerLabel) || !strings.Contains(plain, swDividerLabel) {
		t.Fatalf("expected both dividers on screen at this selection:\n%s", plain)
	}
	if got := len(strings.Split(raw, "\n")); got > m.height {
		t.Errorf("view is %d lines, want at most %d", got, m.height)
	}
}

// The badge is reserved for on every row as soon as any session is closing,
// exactly as DEFER is, so a narrow pane clips the topic and not the badge.
func TestSwModelViewClosingBadgeSurvivesNarrowPane(t *testing.T) {
	m := swTestModel()
	m.width = swRowChromeW + swTopicColMinW + swCloseBadgeW
	m.snap.Sessions = []swSession{
		{Name: "api", State: "Idle", Context: 37, Topic: "a long topic that will be clipped", Model: "claude-opus-4-7", Closing: "ready"},
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "READY") {
		t.Errorf("badge clipped at width %d:\n%s", m.width, view)
	}
}

func TestStatusLineCountsClosing(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := &conductor{}
	closing := waiting("b", 200)
	closing.Closing = "ready"
	busyClosing := busy("c")
	busyClosing.Closing = "wrapup"
	got := c.statusLine(snapAt("switchboard", waiting("a", 100), closing, busyClosing), now)
	// Closing sessions are still conducted, so the waiting one counts as
	// waiting too; the suffix counts every closing session, busy or not.
	if want := "conducting · 2 waiting · 2 closing"; got != want {
		t.Errorf("statusLine = %q, want %q", got, want)
	}
}

// The conductor still brings the human to a closing session: the wrap-up's
// confirmation and the final x both need them.
func TestWaitingQueueKeepsClosingSessions(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	closing := waiting("closing", 100)
	closing.Closing = "ready"
	q := snapAt("switchboard", closing, waiting("normal", 200)).waitingQueue(nil, now)
	if len(q) != 2 || q[0].Name != "closing" {
		t.Errorf("queue = %v, want the closing session conducted in its turn", q)
	}
}

func TestBuildWebFleetViewCarriesClosing(t *testing.T) {
	snap := webTestSnapshot()
	snap.Sessions[1].Closing = "blocked"
	v := buildWebFleetView(snap, RateLimits{}, false, nil, time.Now(), webHeadline{})
	if v.Counts.Closing != 1 {
		t.Errorf("Counts.Closing = %d, want 1", v.Counts.Closing)
	}
	web := v.Sessions[1]
	if web.Closing != "blocked" || web.ClosingLabel != "wrap-up blocked" {
		t.Errorf("web = %+v, want closing=blocked with its label", web)
	}
	if api := v.Sessions[0]; api.Closing != "" || api.ClosingLabel != "" {
		t.Errorf("api = %+v, want no closing fields", api)
	}
}

// Entering teardown changes what the fleet is doing; moving between teardown
// phases does not, and must not buy another headline call.
func TestWebFingerprintMovesOnClosingButNotOnPhase(t *testing.T) {
	base := webFingerprint(webTestSnapshot().Sessions)
	closing := webTestSnapshot().Sessions
	closing[0].Closing = "wrapup"
	fp := webFingerprint(closing)
	if fp == base {
		t.Error("a session starting to close must move the fingerprint")
	}
	closing[0].Closing = "ready"
	if webFingerprint(closing) != fp {
		t.Error("a phase change within teardown must not move the fingerprint")
	}
}

func TestBuildHeadlinePromptMarksClosing(t *testing.T) {
	sessions := []swSession{{Name: "api", State: "Idle", Closing: "ready"}, {Name: "web", State: "Idle"}}
	got := buildHeadlinePrompt(sessions)
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if !strings.Contains(lines[1], "closing") {
		t.Errorf("api's line must say it is closing: %q", lines[1])
	}
	if strings.Contains(lines[2], "closing") {
		t.Errorf("web's line must not: %q", lines[2])
	}
}

func TestWebPageRendersClosingSection(t *testing.T) {
	for _, want := range []string{`id="closing"`, "closingRow", "s.closing_label", "v.counts.closing"} {
		if !strings.Contains(string(webPageHTML), want) {
			t.Errorf("webpage.html lacks %q", want)
		}
	}
}

// The closing badge tells running, waiting-on-you and ready apart, and every
// variant is the one width swTopicW reserved for.
func TestSwCloseBadgeByPhase(t *testing.T) {
	cases := []struct {
		closing, state, want string
		style                lipgloss.Style
	}{
		{"wrapup", "Busy", "/DONE", swBadgeCloseRunStyle},
		{"wrapup", "Tool:Bash", "/DONE", swBadgeCloseRunStyle},
		{"wrapup", "Awaiting", "CONFIRM", swBadgeCloseWaitStyle},
		{"wrapup", "Tool:AskUserQuestion", "CONFIRM", swBadgeCloseWaitStyle},
		{"blocked", "Idle", "BLOCKED", swBadgeCloseWaitStyle},
		{"ready", "Idle", "READY", swBadgeCloseReadyStyle},
		{"direct", "Busy", "KILL?", swBadgeCloseReadyStyle},
		{"exiting", "Idle", "EXITING", swBadgeCloseRunStyle},
		{"from-a-newer-head", "Idle", "CLOSE", swBadgeCloseReadyStyle},
	}
	for _, c := range cases {
		label, style := swCloseBadgeLabel(c.closing, c.state)
		if label != c.want || style.GetBackground() != c.style.GetBackground() {
			t.Errorf("swCloseBadgeLabel(%q, %q) = %q, want %q in its style", c.closing, c.state, label, c.want)
		}
		if w := lipgloss.Width(swCloseBadgeText(c.closing, c.state)); w != swCloseBadgeW {
			t.Errorf("badge for %q/%q is %d wide, want %d", c.closing, c.state, w, swCloseBadgeW)
		}
	}
}
