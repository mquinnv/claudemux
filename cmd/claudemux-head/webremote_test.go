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
	mu      sync.Mutex
	screens map[string]string
	calls   []string
	// failCapture makes every capture fail; a pane with no screen fails too.
	failCapture bool
	// failType makes typeText fail.
	failType bool
	// failKeys names the keys whose press fails ("*" = every key).
	failKeys map[string]bool
	// typing, when set, receives once per typeText call, and the call then
	// blocks until release is closed: a write held mid-flight.
	typing  chan string
	release chan struct{}
}

func (f *fakeRemoteTmux) capture(pane string, _ bool, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCapture {
		return "", errors.New("tmux down")
	}
	s, ok := f.screens[pane]
	if !ok {
		return "", errors.New("no such pane")
	}
	return s, nil
}

func (f *fakeRemoteTmux) typeText(pane, text string) error {
	f.mu.Lock()
	if f.failType {
		f.mu.Unlock()
		return errors.New("tmux down")
	}
	f.calls = append(f.calls, "type "+pane+" "+text)
	typing, release := f.typing, f.release
	f.mu.Unlock()
	if typing != nil {
		typing <- text
		<-release
	}
	return nil
}

func (f *fakeRemoteTmux) keys(pane string, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		if f.failKeys["*"] || f.failKeys[k] {
			return errors.New("tmux down")
		}
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

func (f *fakeRemoteTmux) set(fn func(f *fakeRemoteTmux)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// idleScreen is claude's idle input box with draft typed in it ("" = empty).
func idleScreen(draft string) string {
	rule := strings.Repeat("─", 30)
	return "● Done. Tests pass.\n\n" + rule + "\n❯ " + draft + "\n" + rule + "\n  ? for shortcuts\n"
}

func dialogScreen() string {
	return "Which database?\n❯ 1. postgres\n  2. sqlite\n  3. Type something.\n"
}

// fakeSuggester answers every call with out/err and counts the calls.
type fakeSuggester struct {
	mu    sync.Mutex
	calls int
	out   []remoteSuggestion
	err   error
}

func (f *fakeSuggester) SuggestReplies(_ context.Context, _ swSession, _ string, _ []string) ([]remoteSuggestion, error) {
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

// gatedSuggester holds each call until its episode's gate is closed, then
// answers with an item naming that episode's Since.
type gatedSuggester struct {
	mu    sync.Mutex
	gates map[time.Time]chan struct{}
	done  int
}

func (g *gatedSuggester) gate(since time.Time) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gates[since] == nil {
		g.gates[since] = make(chan struct{})
	}
	return g.gates[since]
}

func (g *gatedSuggester) SuggestReplies(_ context.Context, sess swSession, _ string, _ []string) ([]remoteSuggestion, error) {
	<-g.gate(sess.Since)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.done++
	tag := sess.Since.UTC().Format(time.RFC3339)
	return []remoteSuggestion{{Label: tag, Text: "reply for " + tag}}, nil
}

func (g *gatedSuggester) finished() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.done
}

// fakeClock is a settable now() safe to read from the suggest goroutines.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// waitFor polls cond until it holds, failing the test after two seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// suggestEntryCount is how many sessions have a suggest entry. ensure makes
// the entry synchronously before any call starts, so zero entries proves no
// call was ever started.
func suggestEntryCount(c *remoteSuggestions) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

var rt0 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func testFleet(sessions ...swSession) *webFleet {
	f := newWebFleet()
	f.publish(swSnapshot{Sessions: sessions}, RateLimits{}, false, nil, rt0)
	return f
}

func newTestRemote(t *testing.T, fleet *webFleet, gen remoteSuggester) (*webRemote, *fakeRemoteTmux, http.Handler) {
	t.Helper()
	return newTestRemoteWith(t, fleet, gen, testRemoteToken, nil)
}

func newTestRemoteWith(t *testing.T, fleet *webFleet, gen remoteSuggester, token string, tokenErr error) (*webRemote, *fakeRemoteTmux, http.Handler) {
	t.Helper()
	ft := &fakeRemoteTmux{screens: map[string]string{}}
	cfg := defaultConfig()
	r := newWebRemote(fleet, cfg, token, gen)
	r.tmux = ft
	r.vimMode = false
	r.sleep = func(time.Duration) {}
	h := webHandlerWith(fleet, webAllow{}, func(mux *http.ServeMux) { r.register(mux, tokenErr) })
	return r, ft, h
}

// remoteDoRaw sends body as-is with a raw Authorization header ("" = none).
func remoteDoRaw(t *testing.T, h http.Handler, method, path, auth, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5555"
	req.Host = "localhost:7474"
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func remoteDo(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		raw = string(b)
	}
	auth := ""
	if token != "" {
		auth = "Bearer " + token
	}
	return remoteDoRaw(t, h, method, path, auth, raw)
}

func remoteCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e remoteErrorBody
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	return e.Code
}

func decodeSessionView(t *testing.T, rec *httptest.ResponseRecorder) remoteSessionView {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("session: got %d %s", rec.Code, rec.Body)
	}
	var v remoteSessionView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func idleSession() swSession {
	return swSession{Name: "a", State: "Idle", Since: rt0, ClaudePane: "%1", HeadPane: "%0", Context: -1}
}

// remoteRoute is one API call with a body that would succeed if authorized.
type remoteRoute struct {
	method, path string
	body         any
}

func remoteRoutes(s swSession) []remoteRoute {
	ep := remoteEpisode(s)
	return []remoteRoute{
		{"GET", "/api/remote/queue", nil},
		{"GET", "/api/remote/session?name=a", nil},
		{"POST", "/api/remote/send", remoteSendReq{Name: "a", Episode: ep, Text: "hello"}},
		{"POST", "/api/remote/key", remoteKeyReq{Name: "a", Episode: ep, Key: "1"}},
		{"POST", "/api/remote/defer", remoteDeferReq{Name: "a", On: true, Reason: "x"}},
	}
}

func routeBody(t *testing.T, rt remoteRoute) string {
	t.Helper()
	if rt.body == nil {
		return ""
	}
	b, err := json.Marshal(rt.body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRemoteRequiresToken(t *testing.T) {
	s := idleSession()
	_, ft, h := newTestRemote(t, testFleet(s), nil)
	ft.screens["%1"] = idleScreen("")
	for _, rt := range remoteRoutes(s) {
		for _, auth := range []string{
			"",
			"Bearer wrong-token-wrong-token",
			"Bearer " + testRemoteToken + "x",
			"Basic " + testRemoteToken,
			"bearer " + testRemoteToken,
			"Bearer ",
			testRemoteToken,
		} {
			rec := remoteDoRaw(t, h, rt.method, rt.path, auth, routeBody(t, rt))
			if rec.Code != http.StatusUnauthorized || remoteCode(t, rec) != "token" {
				t.Errorf("%s %s with %q: got %d %s, want 401 token", rt.method, rt.path, auth, rec.Code, rec.Body)
			}
		}
	}
	if got := ft.log(); len(got) != 0 {
		t.Fatalf("unauthorized requests reached tmux: %q", got)
	}
	if rec := remoteDo(t, h, "GET", "/api/remote/queue", testRemoteToken, nil); rec.Code != http.StatusOK {
		t.Fatalf("right token: got %d %s", rec.Code, rec.Body)
	}
	// The page itself carries no data and loads without one.
	if rec := remoteDo(t, h, "GET", "/remote", "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "claudemux remote") {
		t.Fatalf("page: got %d", rec.Code)
	}
}

// An empty configured token must never match an empty presented one.
func TestRemoteEmptyTokenNeverAuthorizes(t *testing.T) {
	s := idleSession()
	s.State = "Awaiting"
	_, ft, h := newTestRemoteWith(t, testFleet(s), nil, "", nil)
	ft.screens["%1"] = dialogScreen()
	for _, auth := range []string{"", "Bearer ", "Bearer    ", "Bearer \t"} {
		rec := remoteDoRaw(t, h, "POST", "/api/remote/key", auth, routeBody(t, remoteRoute{body: remoteKeyReq{Name: "a", Episode: remoteEpisode(s), Key: "1"}}))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("auth %q: got %d %s, want 401", auth, rec.Code, rec.Body)
		}
	}
	if got := ft.log(); len(got) != 0 {
		t.Fatalf("pressed keys with no token configured: %q", got)
	}
}

func TestRemoteStillTailnetOnly(t *testing.T) {
	s := idleSession()
	_, ft, h := newTestRemote(t, testFleet(s), nil)
	ft.screens["%1"] = idleScreen("")
	for _, rt := range remoteRoutes(s) {
		req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(routeBody(t, rt)))
		req.RemoteAddr = "203.0.113.9:4444"
		req.Header.Set("Authorization", "Bearer "+testRemoteToken)
		if rt.method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s off-tailnet with a good token: got %d, want 403", rt.method, rt.path, rec.Code)
		}
	}
	if got := ft.log(); len(got) != 0 {
		t.Fatalf("off-tailnet requests reached tmux: %q", got)
	}
}

func TestRemoteSetupErrorIsReported(t *testing.T) {
	s := idleSession()
	_, ft, h := newTestRemoteWith(t, testFleet(s), nil, "", errors.New("no token set"))
	ft.screens["%1"] = idleScreen("")
	for _, rt := range remoteRoutes(s) {
		rec := remoteDoRaw(t, h, rt.method, rt.path, "Bearer "+testRemoteToken, routeBody(t, rt))
		if rec.Code != http.StatusServiceUnavailable || remoteCode(t, rec) != "setup" {
			t.Errorf("%s %s: got %d %s, want 503 setup", rt.method, rt.path, rec.Code, rec.Body)
		}
	}
	if got := ft.log(); len(got) != 0 {
		t.Fatalf("setup-error requests reached tmux: %q", got)
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

func TestRemotePageHeaders(t *testing.T) {
	_, _, h := newTestRemote(t, testFleet(), nil)
	rec := remoteDo(t, h, "GET", "/remote", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for k, want := range map[string]string{
		"Cache-Control":           "no-store",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": webContentSecurityPolicy,
		"Referrer-Policy":         "no-referrer",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestRemotePostNeedsJSON(t *testing.T) {
	fleet := testFleet(swSession{Name: "a", State: "Idle", Since: rt0, ClaudePane: "%1", Context: -1})
	_, ft, h := newTestRemote(t, fleet, nil)
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
	if len(ft.log()) != 0 {
		t.Fatalf("typed anyway: %q", ft.log())
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
	f.publish(swSnapshot{Sessions: []swSession{{Name: "secret", State: "Idle", Since: rt0, ClaudePane: "%1", Context: -1}}}, RateLimits{}, false, nil, rt0)
	if n := len(f.view().Sessions); n != 0 {
		t.Fatalf("status page shows %d sessions, want 0", n)
	}
	// ...but the owner's remote still sees it, through its own API.
	_, _, h := newTestRemote(t, f, nil)
	rec := remoteDo(t, h, "GET", "/api/remote/queue", testRemoteToken, nil)
	var v remoteQueueView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Sessions) != 1 || v.Sessions[0].Name != "secret" || !reflect.DeepEqual(v.Queue, []string{"secret"}) {
		t.Fatalf("remote queue = %+v, want the hidden session", v)
	}
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

func TestRemoteSendMultiLineAndSemicolon(t *testing.T) {
	s := idleSession()
	for _, tc := range []struct{ in, typed string }{
		// CRLF from a phone keyboard becomes LF, trailing newlines go, and
		// the message is typed in ONE call (a paste), submitted once.
		{"line1\r\nline2\n\n", "line1\nline2"},
		// A trailing ';' is text, not a tmux command separator.
		{"ls;", "ls;"},
		{"a\tb", "a\tb"},
	} {
		_, ft, h := newTestRemote(t, testFleet(s), nil)
		ft.screens["%1"] = idleScreen("")
		rec := remoteDo(t, h, "POST", "/api/remote/send", testRemoteToken,
			remoteSendReq{Name: "a", Episode: remoteEpisode(s), Text: tc.in})
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: got %d %s", tc.in, rec.Code, rec.Body)
		}
		want := []string{"type %1 " + tc.typed, "keys %1 Enter"}
		if got := ft.log(); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: calls = %q, want %q", tc.in, got, want)
		}
	}
}

func TestRemoteSendBodyValidation(t *testing.T) {
	s := idleSession()
	ep := remoteEpisode(s)
	sendJSON := func(text string) string {
		b, _ := json.Marshal(remoteSendReq{Name: "a", Episode: ep, Text: text})
		return string(b)
	}
	for _, tc := range []struct {
		what, body, code string
	}{
		{"whitespace only", sendJSON("  \n\t \n"), "empty"},
		{"over the rune cap", sendJSON(strings.Repeat("é", remoteTextMaxRunes+1)), "long"},
		{"unknown field", `{"name":"a","episode":"` + ep + `","text":"hi","submit":true}`, "body"},
		{"not JSON", `name=a`, "body"},
		{"over the body cap", sendJSON(strings.Repeat("a", remoteBodyMax+1)), "body"},
		{"escape sequence", sendJSON("hi\x1b[2Jthere"), "control"},
		{"bell", sendJSON("ding\x07"), "control"},
		{"lone CR", sendJSON("one\rtwo"), "control"},
		{"DEL", sendJSON("a\x7fb"), "control"},
		{"C1 control", sendJSON("a\u0085b"), "control"},
		{"C1 CSI", sendJSON("a\u009b2Jb"), "control"},
	} {
		_, ft, h := newTestRemote(t, testFleet(s), nil)
		ft.screens["%1"] = idleScreen("")
		rec := remoteDoRaw(t, h, "POST", "/api/remote/send", "Bearer "+testRemoteToken, tc.body)
		if rec.Code != http.StatusBadRequest || remoteCode(t, rec) != tc.code {
			t.Errorf("%s: got %d %s, want 400 %s", tc.what, rec.Code, rec.Body, tc.code)
		}
		if got := ft.log(); len(got) != 0 {
			t.Errorf("%s: typed anyway: %q", tc.what, got)
		}
	}

	// Exactly at the cap, in multi-byte runes, is accepted whole.
	_, ft, h := newTestRemote(t, testFleet(s), nil)
	ft.screens["%1"] = idleScreen("")
	full := strings.Repeat("é", remoteTextMaxRunes)
	rec := remoteDoRaw(t, h, "POST", "/api/remote/send", "Bearer "+testRemoteToken, sendJSON(full))
	if rec.Code != http.StatusOK {
		t.Fatalf("at the cap: got %d %s", rec.Code, rec.Body)
	}
	if got := ft.log(); len(got) != 2 || got[0] != "type %1 "+full {
		t.Fatalf("at the cap: calls = %d entries", len(got))
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

func TestRemoteSendTmuxFailures(t *testing.T) {
	for _, tc := range []struct {
		what   string
		state  string
		screen string
		req    remoteSendReq
		break_ func(f *fakeRemoteTmux)
		code   string
		calls  []string
	}{
		{
			what: "capture fails", state: "Idle", screen: idleScreen(""),
			req:    remoteSendReq{Text: "go"},
			break_: func(f *fakeRemoteTmux) { f.failCapture = true },
			code:   "tmux", calls: nil,
		},
		{
			what: "type fails on an empty prompt", state: "Idle", screen: idleScreen(""),
			req:    remoteSendReq{Text: "go"},
			break_: func(f *fakeRemoteTmux) { f.failType = true },
			code:   "tmux", calls: nil,
		},
		{
			what: "type fails after the draft was cleared", state: "Idle", screen: idleScreen("draft"),
			req:    remoteSendReq{Text: "go", Replace: true},
			break_: func(f *fakeRemoteTmux) { f.failType = true },
			code:   "partial", calls: []string{"keys %1 C-u"},
		},
		{
			what: "C-u fails", state: "Idle", screen: idleScreen("draft"),
			req:    remoteSendReq{Text: "go", Replace: true},
			break_: func(f *fakeRemoteTmux) { f.failKeys = map[string]bool{"C-u": true} },
			code:   "tmux", calls: nil,
		},
		{
			what: "Enter fails after typing", state: "Idle", screen: idleScreen(""),
			req:    remoteSendReq{Text: "go"},
			break_: func(f *fakeRemoteTmux) { f.failKeys = map[string]bool{"Enter": true} },
			code:   "partial", calls: []string{"type %1 go"},
		},
		{
			what: "Escape fails", state: "Asking", screen: dialogScreen(),
			req:    remoteSendReq{Text: "go", Escape: true},
			break_: func(f *fakeRemoteTmux) { f.failKeys = map[string]bool{"Escape": true} },
			code:   "tmux", calls: nil,
		},
		{
			what: "capture fails after the dialog was dismissed", state: "Asking", screen: dialogScreen(),
			req:    remoteSendReq{Text: "go", Escape: true},
			break_: func(f *fakeRemoteTmux) { f.failCapture = true },
			code:   "partial", calls: []string{"keys %1 Escape"},
		},
	} {
		s := idleSession()
		s.State = tc.state
		_, ft, h := newTestRemote(t, testFleet(s), nil)
		ft.screens["%1"] = tc.screen
		ft.set(tc.break_)
		tc.req.Name, tc.req.Episode = "a", remoteEpisode(s)
		rec := remoteDo(t, h, "POST", "/api/remote/send", testRemoteToken, tc.req)
		if rec.Code != http.StatusBadGateway || remoteCode(t, rec) != tc.code {
			t.Errorf("%s: got %d %s, want 502 %s", tc.what, rec.Code, rec.Body, tc.code)
		}
		if got := ft.log(); !reflect.DeepEqual(got, tc.calls) {
			t.Errorf("%s: calls = %q, want %q (never Enter after a failure)", tc.what, got, tc.calls)
		}
	}
}

func TestRemoteKeyTmuxFailure(t *testing.T) {
	s := idleSession()
	s.State = "Awaiting"
	_, ft, h := newTestRemote(t, testFleet(s), nil)
	ft.screens["%1"] = dialogScreen()
	ft.set(func(f *fakeRemoteTmux) { f.failKeys = map[string]bool{"*": true} })
	rec := remoteDo(t, h, "POST", "/api/remote/key", testRemoteToken, remoteKeyReq{Name: "a", Episode: remoteEpisode(s), Key: "2"})
	if rec.Code != http.StatusBadGateway || remoteCode(t, rec) != "tmux" {
		t.Fatalf("got %d %s, want 502 tmux", rec.Code, rec.Body)
	}
}

// Two sends at once must not interleave keystrokes in one prompt: the second
// waits for the first's Enter before it captures or types anything.
func TestRemoteWritesAreSerialized(t *testing.T) {
	s := idleSession()
	r, ft, h := newTestRemote(t, testFleet(s), nil)
	ft.screens["%1"] = idleScreen("")
	typing, release := make(chan string, 2), make(chan struct{})
	ft.set(func(f *fakeRemoteTmux) { f.typing, f.release = typing, release })

	var wg sync.WaitGroup
	codes := make([]int, 2)
	send := func(i int, text string) {
		defer wg.Done()
		codes[i] = remoteDo(t, h, "POST", "/api/remote/send", testRemoteToken,
			remoteSendReq{Name: "a", Episode: remoteEpisode(s), Text: text}).Code
	}
	wg.Add(1)
	go send(0, "first")
	if got := <-typing; got != "first" {
		t.Fatalf("typing %q, want first", got)
	}
	// The first send is mid-type: the write lock is held.
	if r.writeMu.TryLock() {
		r.writeMu.Unlock()
		t.Fatal("write lock not held while a send is typing")
	}
	wg.Add(1)
	go send(1, "second")
	close(release)
	if got := <-typing; got != "second" {
		t.Fatalf("typing %q, want second", got)
	}
	wg.Wait()
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
		t.Fatalf("codes = %v", codes)
	}
	want := []string{"type %1 first", "keys %1 Enter", "type %1 second", "keys %1 Enter"}
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

func TestRemoteDeferEdges(t *testing.T) {
	noHead := idleSession()
	noHead.HeadPane = ""
	noPanes := swSession{Name: "bare", State: "Idle", Since: rt0, Context: -1}
	_, ft, h := newTestRemote(t, testFleet(noHead, noPanes), nil)

	// No head pane: the claude pane addresses the session instead.
	rec := remoteDo(t, h, "POST", "/api/remote/defer", testRemoteToken, remoteDeferReq{Name: "a", On: true, Reason: "later"})
	if rec.Code != http.StatusOK {
		t.Fatalf("claude-pane fallback: got %d %s", rec.Code, rec.Body)
	}
	// On:false clears the mark.
	rec = remoteDo(t, h, "POST", "/api/remote/defer", testRemoteToken, remoteDeferReq{Name: "a", On: false})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: got %d %s", rec.Code, rec.Body)
	}
	want := []string{
		"run " + strings.Join(deferArgs("%1", true, "later"), " "),
		"run " + strings.Join(deferArgs("%1", false, ""), " "),
	}
	if got := ft.log(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}

	rec = remoteDo(t, h, "POST", "/api/remote/defer", testRemoteToken, remoteDeferReq{Name: "bare", On: true})
	if rec.Code != http.StatusConflict || remoteCode(t, rec) != "nopane" {
		t.Errorf("no panes: got %d %s, want 409 nopane", rec.Code, rec.Body)
	}
	rec = remoteDo(t, h, "POST", "/api/remote/defer", testRemoteToken, remoteDeferReq{Name: "ghost", On: true})
	if rec.Code != http.StatusNotFound || remoteCode(t, rec) != "gone" {
		t.Errorf("unknown: got %d %s, want 404 gone", rec.Code, rec.Body)
	}
	if got := ft.log(); len(got) != 2 {
		t.Fatalf("refused defers ran tmux: %q", got)
	}
}

func TestRemoteSessionDetailAndSuggestions(t *testing.T) {
	s := idleSession()
	next := swSession{Name: "b", State: "Idle", Since: rt0.Add(time.Minute), ClaudePane: "%2", Context: -1}
	gen := &fakeSuggester{out: []remoteSuggestion{{Label: "run tests", Text: "run the tests"}}}
	r, ft, h := newTestRemote(t, testFleet(s, next), gen)
	ft.screens["%1"] = idleScreen("")
	ft.screens["%2"] = idleScreen("")

	v := decodeSessionView(t, remoteDo(t, h, "GET", "/api/remote/session?name=a", testRemoteToken, nil))
	if !v.PromptEmpty || !strings.Contains(v.Screen, "Tests pass") || v.Session.Queue != 1 || v.ScreenError != "" {
		t.Fatalf("detail = %+v", v)
	}
	// Drafting runs in the background, for a and as a prefetch for b.
	waitFor(t, "suggestions for a and b", func() bool {
		return r.suggest.get(s).Status == suggestStatusReady && r.suggest.get(next).Status == suggestStatusReady
	})
	if gen.count() != 2 {
		t.Fatalf("suggest calls = %d, want 2 (current + next in queue)", gen.count())
	}
	// Polling again in the same episode serves the cache. ensure would have
	// reset the entry to pending synchronously had it started a new call, so
	// a ready answer here proves it did not.
	v = decodeSessionView(t, remoteDo(t, h, "GET", "/api/remote/session?name=a", testRemoteToken, nil))
	if v.Suggestions.Status != suggestStatusReady || len(v.Suggestions.Items) != 1 || v.Suggestions.Items[0].Text != "run the tests" {
		t.Fatalf("re-poll suggestions = %+v", v.Suggestions)
	}
	if st := r.suggest.get(next).Status; st != suggestStatusReady {
		t.Fatalf("re-poll refetched the prefetch: b is %q", st)
	}
	if gen.count() != 2 {
		t.Fatalf("suggest calls after re-poll = %d, want 2", gen.count())
	}
}

func TestRemoteSessionEdges(t *testing.T) {
	gen := &fakeSuggester{out: []remoteSuggestion{{Label: "x", Text: "x"}}}

	t.Run("unknown", func(t *testing.T) {
		_, _, h := newTestRemote(t, testFleet(idleSession()), gen)
		rec := remoteDo(t, h, "GET", "/api/remote/session?name=ghost", testRemoteToken, nil)
		if rec.Code != http.StatusNotFound || remoteCode(t, rec) != "gone" {
			t.Fatalf("got %d %s, want 404 gone", rec.Code, rec.Body)
		}
	})
	t.Run("not waiting", func(t *testing.T) {
		busy := idleSession()
		busy.State = "Thinking"
		other := swSession{Name: "b", State: "Idle", Since: rt0, ClaudePane: "%2", Context: -1}
		r, ft, h := newTestRemote(t, testFleet(busy, other), gen)
		ft.screens["%1"] = idleScreen("")
		ft.screens["%2"] = idleScreen("")
		v := decodeSessionView(t, remoteDo(t, h, "GET", "/api/remote/session?name=a", testRemoteToken, nil))
		if v.Suggestions.Status != suggestStatusOff {
			t.Errorf("status = %q, want off", v.Suggestions.Status)
		}
		if n := suggestEntryCount(r.suggest); n != 0 {
			t.Errorf("started %d suggest calls for a busy session", n)
		}
	})
	t.Run("capture fails", func(t *testing.T) {
		r, _, h := newTestRemote(t, testFleet(idleSession()), gen) // no screen for %1
		v := decodeSessionView(t, remoteDo(t, h, "GET", "/api/remote/session?name=a", testRemoteToken, nil))
		if v.ScreenError == "" || v.Screen != "" {
			t.Errorf("screen_error = %q, screen = %q", v.ScreenError, v.Screen)
		}
		if n := suggestEntryCount(r.suggest); n != 0 {
			t.Errorf("drafted replies from a screen that could not be read (%d entries)", n)
		}
	})
	t.Run("waiting without a pane", func(t *testing.T) {
		bare := swSession{Name: "a", State: "Idle", Since: rt0, Context: -1}
		r, _, h := newTestRemote(t, testFleet(bare), gen)
		v := decodeSessionView(t, remoteDo(t, h, "GET", "/api/remote/session?name=a", testRemoteToken, nil))
		if v.Suggestions.Status != suggestStatusOff {
			t.Errorf("status = %q, want off", v.Suggestions.Status)
		}
		if n := suggestEntryCount(r.suggest); n != 0 {
			t.Errorf("started %d suggest calls with no pane", n)
		}
	})
}

func TestRemoteSuggestionsOffWithoutKey(t *testing.T) {
	s := idleSession()
	_, ft, h := newTestRemote(t, testFleet(s), nil)
	ft.screens["%1"] = idleScreen("")
	v := decodeSessionView(t, remoteDo(t, h, "GET", "/api/remote/session?name=a", testRemoteToken, nil))
	if v.Suggestions.Status != suggestStatusOff {
		t.Fatalf("status = %q, want off", v.Suggestions.Status)
	}
}

func TestRemoteSuggestionsNewEpisodeRedrafts(t *testing.T) {
	gen := &fakeSuggester{out: []remoteSuggestion{{Label: "x", Text: "x"}}}
	c := newRemoteSuggestions(gen)
	s := idleSession()
	c.ensure(s, nil, func() string { return "screen" })
	waitFor(t, "first draft", func() bool { return c.get(s).Status == suggestStatusReady })
	s.Since = s.Since.Add(time.Minute)
	if st := c.get(s).Status; st != suggestStatusWait {
		t.Fatalf("new episode status = %q, want pending", st)
	}
	c.ensure(s, nil, func() string { return "screen" })
	waitFor(t, "second draft", func() bool { return c.get(s).Status == suggestStatusReady })
	if gen.count() != 2 {
		t.Fatalf("calls = %d, want 2", gen.count())
	}
}

// A slow call for an episode the session has left must not overwrite the
// state of the episode it is in now.
func TestRemoteSuggestionsDiscardStaleResult(t *testing.T) {
	gen := &gatedSuggester{gates: map[time.Time]chan struct{}{}}
	c := newRemoteSuggestions(gen)
	ep1 := idleSession()
	ep2 := ep1
	ep2.Since = ep1.Since.Add(time.Minute)

	c.ensure(ep1, nil, func() string { return "screen 1" })
	c.ensure(ep2, nil, func() string { return "screen 2" })
	// Both calls hold a slot of the in-flight semaphore, and a call gives its
	// slot back only after finish has stored (or discarded) its result.
	waitFor(t, "both calls in flight", func() bool { return len(c.sem) == 2 })
	close(gen.gate(ep1.Since))
	waitFor(t, "the stale call to finish", func() bool { return gen.finished() == 1 && len(c.sem) == 1 })
	if st := c.get(ep2); st.Status != suggestStatusWait || len(st.Items) != 0 {
		t.Fatalf("after the stale result: %+v, want pending with no items", st)
	}
	close(gen.gate(ep2.Since))
	waitFor(t, "the current call", func() bool { return c.get(ep2).Status == suggestStatusReady })
	got := c.get(ep2).Items
	want := "reply for " + ep2.Since.UTC().Format(time.RFC3339)
	if len(got) != 1 || got[0].Text != want {
		t.Fatalf("items = %+v, want only %q", got, want)
	}
}

func TestRemoteSuggestionsRetryBackoff(t *testing.T) {
	clock := &fakeClock{t: rt0}
	s := idleSession()

	failing := &fakeSuggester{err: errors.New("boom")}
	c := newRemoteSuggestions(failing)
	c.now = clock.now
	c.ensure(s, nil, func() string { return "screen" })
	waitFor(t, "the failed call", func() bool { return c.get(s).Status == suggestStatusFailed })
	if failing.count() != 1 {
		t.Fatalf("calls = %d, want 1", failing.count())
	}
	if st := c.get(s); st.Error == "" {
		t.Errorf("failed state carries no error reason: %+v", st)
	}
	// Inside the back-off a poll does not retry (a retry would flip the entry
	// to pending synchronously).
	clock.advance(10 * time.Second)
	c.ensure(s, nil, func() string { return "screen" })
	if st := c.get(s).Status; st != suggestStatusFailed {
		t.Fatalf("retried inside the back-off: status %q", st)
	}
	// Past it, the next poll retries.
	clock.advance(21 * time.Second)
	c.ensure(s, nil, func() string { return "screen" })
	waitFor(t, "the retry", func() bool { return failing.count() == 2 && c.get(s).Status == suggestStatusFailed })

	// A ready answer is kept for the whole episode, however long it lasts.
	ok := &fakeSuggester{out: []remoteSuggestion{{Label: "x", Text: "x"}}}
	c = newRemoteSuggestions(ok)
	c.now = clock.now
	c.ensure(s, nil, func() string { return "screen" })
	waitFor(t, "the draft", func() bool { return c.get(s).Status == suggestStatusReady })
	clock.advance(time.Hour)
	c.ensure(s, nil, func() string { return "screen" })
	if st := c.get(s).Status; st != suggestStatusReady || ok.count() != 1 {
		t.Fatalf("ready refetched: status %q, calls %d", st, ok.count())
	}
}

func TestToRemoteSession(t *testing.T) {
	s := idleSession()
	s.Color = "not-a-colour"
	rs := toRemoteSession(s, 0)
	if rs.Color != "" {
		t.Errorf("bad colour passed through: %q", rs.Color)
	}
	if rs.ContextPct != nil {
		t.Errorf("context_pct = %d, want absent for -1", *rs.ContextPct)
	}
	b, _ := json.Marshal(rs)
	if strings.Contains(string(b), "context_pct") || strings.Contains(string(b), `"color"`) {
		t.Errorf("JSON carries absent fields: %s", b)
	}

	s.Color = "ff8800"
	s.Context = 0
	rs = toRemoteSession(s, 3)
	if rs.Color != "#ff8800" {
		t.Errorf("colour = %q, want #ff8800", rs.Color)
	}
	if rs.ContextPct == nil || *rs.ContextPct != 0 {
		t.Errorf("context_pct = %v, want 0", rs.ContextPct)
	}
	if rs.Queue != 3 || !rs.Pane || !rs.Waiting {
		t.Errorf("session = %+v", rs)
	}
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

// A chip SENDS its text, so one carrying a terminal escape (or any other
// control character) is dropped rather than typed into a session.
func TestCleanSuggestionsDropsControlCharacters(t *testing.T) {
	got := cleanSuggestions([]remoteSuggestion{
		{Label: "clear", Text: "run\x1b[2J it"},
		{Label: "bell", Text: "ding\x07"},
		{Label: "del", Text: "a\x7fb"},
		{Label: "c1", Text: "a\u009bb"},
		{Label: "evil\x1b[31m", Text: "fine text"},
		{Label: "ok", Text: "keep me"},
	}, false, nil)
	if want := []remoteSuggestion{{Label: "ok", Text: "keep me"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prompt:\n got %+v\nwant %+v", got, want)
	}
	gotD := cleanSuggestions([]remoteSuggestion{
		{Label: "post\x1bgres", Key: "1"},
		{Label: "sqlite", Key: "2"},
	}, true, nil)
	if want := []remoteSuggestion{{Label: "sqlite", Key: "2"}}; !reflect.DeepEqual(gotD, want) {
		t.Fatalf("dialog:\n got %+v\nwant %+v", gotD, want)
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
