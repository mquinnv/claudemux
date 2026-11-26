package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const testRemoteToken = "0123456789abcdef-test"

// fakeRemoteTmux records every write and serves canned captures per pane.
type fakeRemoteTmux struct {
	mu       sync.Mutex
	screens  map[string]string
	calls    []string
	failKeys bool
}

func (f *fakeRemoteTmux) capture(pane string, _ bool, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.screens[pane]
	if !ok {
		return "", errors.New("no such pane")
	}
	return s, nil
}

func (f *fakeRemoteTmux) typeText(pane, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "type "+pane+" "+text)
	return nil
}

func (f *fakeRemoteTmux) keys(pane string, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failKeys {
		return errors.New("tmux down")
	}
	f.calls = append(f.calls, "keys "+pane+" "+strings.Join(keys, " "))
	// An Esc closes a dialog: the fake shows the empty prompt behind it.
	if len(keys) == 1 && keys[0] == "Escape" {
		f.screens[pane] = idleScreen("")
	}
	return nil
}

func (f *fakeRemoteTmux) run(args []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "run "+strings.Join(args, " "))
	return nil
}

func (f *fakeRemoteTmux) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// idleScreen is claude's idle input box with draft typed in it ("" = empty).
func idleScreen(draft string) string {
	rule := strings.Repeat("─", 30)
	return "● Done. Tests pass.\n\n" + rule + "\n❯ " + draft + "\n" + rule + "\n  ? for shortcuts\n"
}

func dialogScreen() string {
	return "Which database?\n❯ 1. postgres\n  2. sqlite\n  3. Type something.\n"
}

type fakeSuggester struct {
	mu    sync.Mutex
	calls int
	out   []remoteSuggestion
	err   error
	gate  chan struct{}
}

func (f *fakeSuggester) SuggestReplies(_ context.Context, _ swSession, _ string, _ []string) ([]remoteSuggestion, error) {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.out, f.err
}

func (f *fakeSuggester) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

var rt0 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func testFleet(sessions ...swSession) *webFleet {
	f := newWebFleet()
	f.publish(swSnapshot{Sessions: sessions}, RateLimits{}, false, nil, rt0)
	return f
}

func newTestRemote(t *testing.T, fleet *webFleet, gen remoteSuggester) (*webRemote, *fakeRemoteTmux, http.Handler) {
	t.Helper()
	ft := &fakeRemoteTmux{screens: map[string]string{}}
	cfg := defaultConfig()
	r := newWebRemote(fleet, cfg, testRemoteToken, gen)
	r.tmux = ft
	r.vimMode = false
	r.sleep = func(time.Duration) {}
	h := webHandlerWith(fleet, webAllow{}, func(mux *http.ServeMux) { r.register(mux, nil) })
	return r, ft, h
}

func remoteDo(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Host = "localhost:7474"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func remoteCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e remoteErrorBody
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	return e.Code
}

func TestRemoteRequiresToken(t *testing.T) {
	_, _, h := newTestRemote(t, testFleet(), nil)
	if rec := remoteDo(t, h, "GET", "/api/remote/queue", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d", rec.Code)
	}
	if rec := remoteDo(t, h, "GET", "/api/remote/queue", "wrong-token-wrong-token", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d", rec.Code)
	}
	if rec := remoteDo(t, h, "GET", "/api/remote/queue", testRemoteToken, nil); rec.Code != http.StatusOK {
		t.Fatalf("right token: got %d %s", rec.Code, rec.Body)
	}
	// The page itself carries no data and loads without one.
	if rec := remoteDo(t, h, "GET", "/remote", "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "claudemux remote") {
		t.Fatalf("page: got %d", rec.Code)
	}
}

func TestRemoteStillTailnetOnly(t *testing.T) {
	_, _, h := newTestRemote(t, testFleet(), nil)
	req := httptest.NewRequest("GET", "/api/remote/queue", nil)
	req.RemoteAddr = "203.0.113.9:4444"
	req.Header.Set("Authorization", "Bearer "+testRemoteToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("off-tailnet with a good token: got %d, want 403", rec.Code)
	}
}

func TestRemoteSetupErrorIsReported(t *testing.T) {
	fleet := testFleet()
	r := newWebRemote(fleet, defaultConfig(), "", nil)
	h := webHandlerWith(fleet, webAllow{}, func(mux *http.ServeMux) { r.register(mux, errors.New("no token set")) })
	rec := remoteDo(t, h, "GET", "/api/remote/queue", testRemoteToken, nil)
	if rec.Code != http.StatusServiceUnavailable || remoteCode(t, rec) != "setup" {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestRemoteOffWithoutControl(t *testing.T) {
	h := webHandler(testFleet(), webAllow{})
	for _, p := range []string{"/remote", "/api/remote/queue"} {
		if rec := remoteDo(t, h, "GET", p, testRemoteToken, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s with control off: got %d, want 404", p, rec.Code)
		}
	}
}

func TestRemotePostNeedsJSON(t *testing.T) {
	fleet := testFleet(swSession{Name: "a", State: "Idle", Since: rt0, ClaudePane: "%1", Context: -1})
	_, _, h := newTestRemote(t, fleet, nil)
	req := httptest.NewRequest("POST", "/api/remote/send", strings.NewReader(`{"name":"a","text":"hi"}`))
	req.RemoteAddr = "127.0.0.1:1"
	req.Host = "localhost"
	req.Header.Set("Authorization", "Bearer "+testRemoteToken)
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestRemoteQueueOrderMatchesConductor(t *testing.T) {
	fleet := testFleet(
		swSession{Name: "busy", State: "Thinking", Since: rt0, Context: -1},
		swSession{Name: "newer", State: "Idle", Since: rt0.Add(time.Minute), Context: -1},
		swSession{Name: "older", State: "Asking", Since: rt0, Context: -1},
		swSession{Name: "parked", State: "Idle", Since: rt0.Add(-time.Hour), Deferred: true, Context: -1},
		swSession{Name: "closing", State: "Idle", Since: rt0.Add(2 * time.Minute), Closing: "ready", Context: -1},
	)
	_, _, h := newTestRemote(t, fleet, nil)
	rec := remoteDo(t, h, "GET", "/api/remote/queue", testRemoteToken, nil)
	var v remoteQueueView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	want := []string{"closing", "older", "newer"}
	if !reflect.DeepEqual(v.Queue, want) {
		t.Fatalf("queue = %v, want %v", v.Queue, want)
	}
	if len(v.Sessions) != 5 {
		t.Fatalf("sessions = %d, want all 5", len(v.Sessions))
	}
	if !reflect.DeepEqual(v.Replies, defaultRemoteReplies) {
		t.Fatalf("replies = %v", v.Replies)
	}
	for _, s := range v.Sessions {
		if s.Name == "older" && (!s.Dialog || s.Queue != 2) {
			t.Errorf("older: dialog=%v queue=%d", s.Dialog, s.Queue)
		}
	}
}

func TestRemoteSeesPrivacyHiddenSessions(t *testing.T) {
	f := newWebFleet()
	f.privacy = &webPrivacy{orgs: map[string]bool{"me": true}, hidden: map[string]bool{}, cache: map[string]webVisEntry{}, kick: make(chan struct{}, 1)}
	// Nothing resolved yet, so the privacy filter fails closed and hides
	// everything from the status page...
	f.publish(swSnapshot{Sessions: []swSession{{Name: "secret", State: "Idle", Context: -1}}}, RateLimits{}, false, nil, rt0)
	if n := len(f.view().Sessions); n != 0 {
		t.Fatalf("status page shows %d sessions, want 0", n)
	}
	// ...but the owner's remote still sees it.
	if n := len(f.allSessions()); n != 1 {
		t.Fatalf("remote sees %d sessions, want 1", n)
	}
}

func idleSession() swSession {
	return swSession{Name: "a", State: "Idle", Since: rt0, ClaudePane: "%1", HeadPane: "%0", Context: -1}
}

func TestRemoteSendTypesThenSubmits(t *testing.T) {
	s := idleSession()
	_, ft, h := newTestRemote(t, testFleet(s), nil)
	ft.screens["%1"] = idleScreen("")
	rec := remoteDo(t, h, "POST", "/api/remote/send", testRemoteToken,
		remoteSendReq{Name: "a", Episode: remoteEpisode(s), Text: "run the tests\n"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	want := []string{"type %1 run the tests", "keys %1 Enter"}
	if got := ft.log(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestRemoteSendRefusesToTypeOverADraft(t *testing.T) {
	s := idleSession()
	_, ft, h := newTestRemote(t, testFleet(s), nil)
	ft.screens["%1"] = idleScreen("half a thought")
	rec := remoteDo(t, h, "POST", "/api/remote/send", testRemoteToken,
		remoteSendReq{Name: "a", Episode: remoteEpisode(s), Text: "continue"})
	if rec.Code != http.StatusConflict || remoteCode(t, rec) != "draft" {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if len(ft.log()) != 0 {
		t.Fatalf("typed anyway: %q", ft.log())
	}
	rec = remoteDo(t, h, "POST", "/api/remote/send", testRemoteToken,
		remoteSendReq{Name: "a", Episode: remoteEpisode(s), Text: "continue", Replace: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("replace: got %d %s", rec.Code, rec.Body)
	}
	want := []string{"keys %1 C-u", "type %1 continue", "keys %1 Enter"}
	if got := ft.log(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestRemoteSendIntoDialog(t *testing.T) {
	s := idleSession()
	s.State = "Asking"
	_, ft, h := newTestRemote(t, testFleet(s), nil)
	ft.screens["%1"] = dialogScreen()
	rec := remoteDo(t, h, "POST", "/api/remote/send", testRemoteToken,
		remoteSendReq{Name: "a", Episode: remoteEpisode(s), Text: "use postgres"})
	if rec.Code != http.StatusConflict || remoteCode(t, rec) != "dialog" {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	rec = remoteDo(t, h, "POST", "/api/remote/send", testRemoteToken,
		remoteSendReq{Name: "a", Episode: remoteEpisode(s), Text: "use postgres", Escape: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("escape: got %d %s", rec.Code, rec.Body)
	}
	want := []string{"keys %1 Escape", "type %1 use postgres", "keys %1 Enter"}
	if got := ft.log(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestRemoteWriteRefusedWhenEpisodeMoved(t *testing.T) {
	s := idleSession()
	_, ft, h := newTestRemote(t, testFleet(s), nil)
	ft.screens["%1"] = idleScreen("")
	stale := swSession{State: "Asking", Since: rt0.Add(-time.Minute)}
	for _, tc := range []struct {
		path string
		body any
	}{
		{"/api/remote/send", remoteSendReq{Name: "a", Episode: remoteEpisode(stale), Text: "yes"}},
		{"/api/remote/key", remoteKeyReq{Name: "a", Episode: remoteEpisode(stale), Key: "1"}},
	} {
		rec := remoteDo(t, h, "POST", tc.path, testRemoteToken, tc.body)
		if rec.Code != http.StatusConflict || remoteCode(t, rec) != "moved" {
			t.Errorf("%s: got %d %s", tc.path, rec.Code, rec.Body)
		}
	}
	if len(ft.log()) != 0 {
		t.Fatalf("wrote to a moved session: %q", ft.log())
	}
}

func TestRemoteKeyWhitelist(t *testing.T) {
	s := idleSession()
	s.State = "Awaiting"
	_, ft, h := newTestRemote(t, testFleet(s), nil)
	for _, k := range []string{"C-c", "C-z", "q", "10", ""} {
		rec := remoteDo(t, h, "POST", "/api/remote/key", testRemoteToken, remoteKeyReq{Name: "a", Episode: remoteEpisode(s), Key: k})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("key %q: got %d", k, rec.Code)
		}
	}
	rec := remoteDo(t, h, "POST", "/api/remote/key", testRemoteToken, remoteKeyReq{Name: "a", Episode: remoteEpisode(s), Key: "2"})
	if rec.Code != http.StatusOK {
		t.Fatalf("key 2: got %d %s", rec.Code, rec.Body)
	}
	if got := ft.log(); !reflect.DeepEqual(got, []string{"keys %1 2"}) {
		t.Fatalf("calls = %q", got)
	}
}

func TestRemoteDeferTargetsThePaneNotTheName(t *testing.T) {
	_, ft, h := newTestRemote(t, testFleet(idleSession()), nil)
	rec := remoteDo(t, h, "POST", "/api/remote/defer", testRemoteToken, remoteDeferReq{Name: "a", On: true, Reason: "waiting on CI"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	want := "run " + strings.Join(deferArgs("%0", true, "waiting on CI"), " ")
	if got := ft.log(); len(got) != 1 || got[0] != want {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestRemoteSessionDetailAndSuggestions(t *testing.T) {
	s := idleSession()
	next := swSession{Name: "b", State: "Idle", Since: rt0.Add(time.Minute), ClaudePane: "%2", Context: -1}
	gen := &fakeSuggester{out: []remoteSuggestion{{Label: "run tests", Text: "run the tests"}}}
	r, ft, h := newTestRemote(t, testFleet(s, next), gen)
	ft.screens["%1"] = idleScreen("")
	ft.screens["%2"] = idleScreen("")

	rec := remoteDo(t, h, "GET", "/api/remote/session?name=a", testRemoteToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	var v remoteSessionView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if !v.PromptEmpty || !strings.Contains(v.Screen, "Tests pass") || v.Session.Queue != 1 {
		t.Fatalf("detail = %+v", v)
	}
	// Drafting runs in the background; wait for it and the prefetch of b.
	deadline := time.Now().Add(2 * time.Second)
	for gen.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if gen.count() != 2 {
		t.Fatalf("suggest calls = %d, want 2 (current + next in queue)", gen.count())
	}
	for r.suggest.get(s).Status != suggestStatusReady && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got := r.suggest.get(s)
	if got.Status != suggestStatusReady || len(got.Items) != 1 || got.Items[0].Text != "run the tests" {
		t.Fatalf("suggestions = %+v", got)
	}
	// Polling again in the same episode does not call the model again.
	remoteDo(t, h, "GET", "/api/remote/session?name=a", testRemoteToken, nil)
	time.Sleep(20 * time.Millisecond)
	if gen.count() != 2 {
		t.Fatalf("suggest calls after re-poll = %d, want 2", gen.count())
	}
}

func TestRemoteSuggestionsOffWithoutKey(t *testing.T) {
	s := idleSession()
	_, ft, h := newTestRemote(t, testFleet(s), nil)
	ft.screens["%1"] = idleScreen("")
	rec := remoteDo(t, h, "GET", "/api/remote/session?name=a", testRemoteToken, nil)
	var v remoteSessionView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.Suggestions.Status != suggestStatusOff {
		t.Fatalf("status = %q, want off", v.Suggestions.Status)
	}
}

func TestRemoteSuggestionsNewEpisodeRedrafts(t *testing.T) {
	gen := &fakeSuggester{out: []remoteSuggestion{{Label: "x", Text: "x"}}}
	c := newRemoteSuggestions(gen)
	s := idleSession()
	wait := func(n int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for gen.count() < n && time.Now().Before(deadline) {
			time.Sleep(2 * time.Millisecond)
		}
		if gen.count() != n {
			t.Fatalf("calls = %d, want %d", gen.count(), n)
		}
	}
	c.ensure(s, nil, func() string { return "screen" })
	wait(1)
	s.Since = s.Since.Add(time.Minute)
	if st := c.get(s).Status; st != suggestStatusWait {
		t.Fatalf("new episode status = %q, want pending", st)
	}
	c.ensure(s, nil, func() string { return "screen" })
	wait(2)
}

func TestCleanSuggestions(t *testing.T) {
	in := []remoteSuggestion{
		{Label: "Run tests", Text: "run the tests."},
		{Label: "dup", Text: "Run the tests"},
		{Label: "q", Text: "should I deploy?"},
		{Label: "fixed", Text: "continue"},
		{Label: "key in prompt", Key: "1"},
		{Label: "", Text: "ship it"},
		{Label: "long", Text: strings.Repeat("a", suggestTextRunes+1)},
		{Label: "five", Text: "five"},
		{Label: "six", Text: "six"},
	}
	got := cleanSuggestions(in, false, []string{"Continue"})
	want := []remoteSuggestion{
		{Label: "Run tests", Text: "run the tests"},
		{Label: "ship it", Text: "ship it"},
		{Label: "five", Text: "five"},
		{Label: "six", Text: "six"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prompt:\n got %+v\nwant %+v", got, want)
	}

	dialog := cleanSuggestions([]remoteSuggestion{
		{Label: "postgres", Key: "1"},
		{Label: "typed", Text: "use sqlite"},
		{Label: "again", Key: "1"},
		{Label: "bad", Key: "0"},
		{Label: "", Key: "2"},
	}, true, nil)
	wantD := []remoteSuggestion{{Label: "postgres", Key: "1"}, {Label: "option 2", Key: "2"}}
	if !reflect.DeepEqual(dialog, wantD) {
		t.Fatalf("dialog:\n got %+v\nwant %+v", dialog, wantD)
	}
}

func TestRemoteTokenRules(t *testing.T) {
	cfg := defaultConfig()
	cfg.Summary.APIKeyFile = t.TempDir() + "/env"
	t.Setenv(remoteTokenKey, "")
	if _, err := remoteToken(cfg); err == nil {
		t.Fatal("no token: want error")
	}
	t.Setenv(remoteTokenKey, "short")
	if _, err := remoteToken(cfg); err == nil {
		t.Fatal("short token: want error")
	}
	t.Setenv(remoteTokenKey, testRemoteToken)
	if tok, err := remoteToken(cfg); err != nil || tok != testRemoteToken {
		t.Fatalf("got %q %v", tok, err)
	}
}

func TestRemoteRepliesConfigValidation(t *testing.T) {
	cfg := defaultConfig()
	cfg.Web.Replies = []string{"ok", "  "}
	if err := cfg.validate(); err == nil {
		t.Fatal("blank reply: want error")
	}
	cfg.Web.Replies = []string{"1", "2", "3", "4", "5", "6", "7"}
	if err := cfg.validate(); err == nil {
		t.Fatal("too many replies: want error")
	}
	cfg.Web.Replies = []string{"ship it"}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	r := newWebRemote(newWebFleet(), cfg, testRemoteToken, nil)
	if !reflect.DeepEqual(r.replies, []string{"ship it"}) {
		t.Fatalf("replies = %v", r.replies)
	}
}

func TestRemoteTrimPromptBox(t *testing.T) {
	screen := remoteScreenText(idleScreen(""), 50)
	if got := remoteTrimPromptBox(screen); got != "● Done. Tests pass." {
		t.Fatalf("got %q", got)
	}
	if got := remoteTrimPromptBox("no box here"); got != "no box here" {
		t.Fatalf("got %q", got)
	}
}
