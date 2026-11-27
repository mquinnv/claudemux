package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"runtime"
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
	// captures lists the pane of every capture, in order (reads are not in
	// calls, which is writes only).
	captures []string
	// escapeScreen, when set, is what the pane shows after an Escape
	// (default: the empty prompt).
	escapeScreen string
	// clearScreen, when set, is what the pane shows after a C-u (default:
	// the empty prompt). Set it to a draft to model a C-u that cleared
	// nothing (a multi-line draft, a mode the remote misread).
	clearScreen string
}

func (f *fakeRemoteTmux) captured() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.captures...)
}

func (f *fakeRemoteTmux) capture(pane string, _ bool, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captures = append(f.captures, pane)
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
		if f.escapeScreen != "" {
			f.screens[pane] = f.escapeScreen
		}
	}
	if len(keys) == 1 && keys[0] == "C-u" {
		f.screens[pane] = idleScreen("")
		if f.clearScreen != "" {
			f.screens[pane] = f.clearScreen
		}
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

// waitFor polls cond until it holds, failing the test after ten seconds
// (generous, for a loaded -race run).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
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

// Two sends at once must not interleave keystrokes: the second waits for the
// first's Enter before it captures or types anything. The lock is global, so
// it holds across sessions too (the second send here is to another session,
// since a second answer to the same episode is refused as "moved").
func TestRemoteWritesAreSerialized(t *testing.T) {
	a := idleSession()
	b := swSession{Name: "b", State: "Idle", Since: rt0, ClaudePane: "%2", Context: -1}
	r, ft, h := newTestRemote(t, testFleet(a, b), nil)
	ft.screens["%1"] = idleScreen("")
	ft.screens["%2"] = idleScreen("")
	typing, release := make(chan string, 2), make(chan struct{})
	ft.set(func(f *fakeRemoteTmux) { f.typing, f.release = typing, release })

	var wg sync.WaitGroup
	codes := make([]int, 2)
	send := func(i int, s swSession, text string) {
		defer wg.Done()
		codes[i] = remoteDo(t, h, "POST", "/api/remote/send", testRemoteToken,
			remoteSendReq{Name: s.Name, Episode: remoteEpisode(s), Text: text}).Code
	}
	wg.Add(1)
	go send(0, a, "first")
	if got := <-typing; got != "first" {
		t.Fatalf("typing %q, want first", got)
	}
	// The first send is mid-type: the write lock is held.
	if r.writeMu.TryLock() {
		r.writeMu.Unlock()
		t.Fatal("write lock not held while a send is typing")
	}
	if got := ft.captured(); len(got) != 1 {
		t.Fatalf("captures before the second send = %q, want just the first's", got)
	}
	wg.Add(1)
	go send(1, b, "second")
	// The second send is parked on the write lock; holding it, the first
	// keeps it from capturing (or typing) anything.
	waitWriteMuWaiters(t, "handleSend", 1)
	if got := ft.captured(); len(got) != 1 {
		t.Fatalf("second send captured while the first held the lock: captures %q", got)
	}
	if got := ft.log(); !reflect.DeepEqual(got, []string{"type %1 first"}) {
		t.Fatalf("second send wrote while the first held the lock: calls %q", got)
	}
	close(release)
	if got := <-typing; got != "second" {
		t.Fatalf("typing %q, want second", got)
	}
	wg.Wait()
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
		t.Fatalf("codes = %v", codes)
	}
	want := []string{"type %1 first", "keys %1 Enter", "type %2 second", "keys %2 Enter"}
	if got := ft.log(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

// writeMuWaiters counts goroutines inside (*webRemote).<handler> that are
// parked acquiring a sync.Mutex. The only mutex a handler waits on for long
// is writeMu: the fake's own lock is never held across a block, and a
// goroutine inside the fake is excluded anyway.
func writeMuWaiters(handler string) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "(*webRemote)."+handler) && strings.Contains(g, "(*Mutex).Lock") &&
			!strings.Contains(g, "(*fakeRemoteTmux)") {
			count++
		}
	}
	return count
}

// waitWriteMuWaiters waits until n requests in handler are blocked on the
// write lock: a deterministic "they are queued", where a sleep only hopes.
func waitWriteMuWaiters(t *testing.T, handler string, n int) {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d %s request(s) blocked on writeMu", n, handler), func() bool {
		return writeMuWaiters(handler) >= n
	})
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
		if v.Suggestions.Status != suggestStatusFailed || v.Suggestions.Error != suggestErrNoScreen {
			t.Errorf("suggestions = %+v, want error %q", v.Suggestions, suggestErrNoScreen)
		}
		if n := suggestEntryCount(r.suggest); n != 0 {
			t.Errorf("drafted replies from a screen that could not be read (%d entries)", n)
		}
	})
	t.Run("waiting without a pane", func(t *testing.T) {
		bare := swSession{Name: "a", State: "Idle", Since: rt0, Context: -1}
		r, _, h := newTestRemote(t, testFleet(bare), gen)
		v := decodeSessionView(t, remoteDo(t, h, "GET", "/api/remote/session?name=a", testRemoteToken, nil))
		if v.Suggestions.Status != suggestStatusFailed || v.Suggestions.Error != suggestErrNoPane {
			t.Errorf("suggestions = %+v, want error %q", v.Suggestions, suggestErrNoPane)
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
	clock.advance(suggestRetryAfter - time.Second)
	c.ensure(s, nil, func() string { return "screen" })
	if st := c.get(s).Status; st != suggestStatusFailed || failing.count() != 1 {
		t.Fatalf("retried inside the back-off: status %q, calls %d", st, failing.count())
	}
	// Past it, the next poll retries.
	clock.advance(2 * time.Second)
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
	for _, tc := range []struct {
		what    string
		replies []string
		ok      bool
	}{
		{"exactly the cap", []string{"1", "2", "3", "4", "5", "6"}, true},
		{"one rune over the length cap", []string{strings.Repeat("a", remoteReplyMaxRunes+1)}, false},
		{"the length cap in multi-byte runes", []string{strings.Repeat("é", remoteReplyMaxRunes)}, true},
		{"an escape sequence", []string{"ok\x1b[2J"}, false},
		{"a C1 control", []string{"a\u009bb"}, false},
	} {
		cfg.Web.Replies = tc.replies
		if err := cfg.validate(); (err == nil) != tc.ok {
			t.Errorf("%s: validate() = %v, want ok=%v", tc.what, err, tc.ok)
		}
	}
	cfg.Web.Replies = []string{"  ship it  ", "continue"}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	r := newWebRemote(newWebFleet(), cfg, testRemoteToken, nil)
	if !reflect.DeepEqual(r.replies, []string{"ship it", "continue"}) {
		t.Fatalf("replies = %q, want trimmed", r.replies)
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

// A send must name the episode it answers; a key too, except Escape, which
// is safe whatever the session is doing now.
func TestRemoteWritesNeedAnEpisode(t *testing.T) {
	s := idleSession()
	_, ft, h := newTestRemote(t, testFleet(s), nil)
	ft.screens["%1"] = idleScreen("")
	for _, tc := range []struct {
		path string
		body any
	}{
		{"/api/remote/send", remoteSendReq{Name: "a", Text: "yes"}},
		{"/api/remote/key", remoteKeyReq{Name: "a", Key: "1"}},
		{"/api/remote/key", remoteKeyReq{Name: "a", Key: "Enter"}},
	} {
		rec := remoteDo(t, h, "POST", tc.path, testRemoteToken, tc.body)
		if rec.Code != http.StatusBadRequest || remoteCode(t, rec) != "episode" {
			t.Errorf("%s %+v: got %d %s, want 400 episode", tc.path, tc.body, rec.Code, rec.Body)
		}
	}
	if got := ft.log(); len(got) != 0 {
		t.Fatalf("wrote without an episode: %q", got)
	}
	if got := ft.captured(); len(got) != 0 {
		t.Fatalf("captured without an episode: %q", got)
	}

	// Escape with no episode reaches a session that has moved on since the
	// page last looked (it is mid-turn now): interrupting is the point.
	moved := idleSession()
	moved.State = "Thinking"
	moved.Since = rt0.Add(time.Minute)
	_, ft, h = newTestRemote(t, testFleet(moved), nil)
	rec := remoteDo(t, h, "POST", "/api/remote/key", testRemoteToken, remoteKeyReq{Name: "a", Key: "Escape"})
	if rec.Code != http.StatusOK {
		t.Fatalf("Escape without an episode: got %d %s, want 200", rec.Code, rec.Body)
	}
	if got := ft.log(); !reflect.DeepEqual(got, []string{"keys %1 Escape"}) {
		t.Fatalf("calls = %q, want just the Escape", got)
	}
}

func TestRemoteWriteTargetRefusals(t *testing.T) {
	bare := swSession{Name: "bare", State: "Asking", Since: rt0, Context: -1}
	for _, tc := range []struct {
		what   string
		path   string
		body   any
		status int
		code   string
	}{
		{"send to a gone session", "/api/remote/send", remoteSendReq{Name: "ghost", Episode: "x", Text: "hi"}, http.StatusNotFound, "gone"},
		{"key to a gone session", "/api/remote/key", remoteKeyReq{Name: "ghost", Episode: "x", Key: "1"}, http.StatusNotFound, "gone"},
		{"Escape to a gone session", "/api/remote/key", remoteKeyReq{Name: "ghost", Key: "Escape"}, http.StatusNotFound, "gone"},
		{"send with no claude pane", "/api/remote/send", remoteSendReq{Name: "bare", Episode: remoteEpisode(bare), Text: "hi", Escape: true}, http.StatusConflict, "nopane"},
		{"key with no claude pane", "/api/remote/key", remoteKeyReq{Name: "bare", Episode: remoteEpisode(bare), Key: "1"}, http.StatusConflict, "nopane"},
	} {
		_, ft, h := newTestRemote(t, testFleet(bare), nil)
		rec := remoteDo(t, h, "POST", tc.path, testRemoteToken, tc.body)
		if rec.Code != tc.status || remoteCode(t, rec) != tc.code {
			t.Errorf("%s: got %d %s, want %d %s", tc.what, rec.Code, rec.Body, tc.status, tc.code)
		}
		if got := ft.log(); len(got) != 0 {
			t.Errorf("%s: tmux writes %q", tc.what, got)
		}
		if got := ft.captured(); len(got) != 0 {
			t.Errorf("%s: tmux captures %q", tc.what, got)
		}
	}
}

// Escape closed the dialog but the prompt behind it holds a draft: the send
// stops there (the episode the page answered is gone), even with replace.
func TestRemoteSendDismissedIntoADraft(t *testing.T) {
	for _, replace := range []bool{false, true} {
		s := idleSession()
		s.State = "Asking"
		_, ft, h := newTestRemote(t, testFleet(s), nil)
		ft.screens["%1"] = dialogScreen()
		ft.escapeScreen = idleScreen("half a thought")
		rec := remoteDo(t, h, "POST", "/api/remote/send", testRemoteToken,
			remoteSendReq{Name: "a", Episode: remoteEpisode(s), Text: "use postgres", Escape: true, Replace: replace})
		if rec.Code != http.StatusConflict || remoteCode(t, rec) != "dismissed_draft" {
			t.Errorf("replace=%v: got %d %s, want 409 dismissed_draft", replace, rec.Code, rec.Body)
		}
		if got := ft.log(); !reflect.DeepEqual(got, []string{"keys %1 Escape"}) {
			t.Errorf("replace=%v: calls = %q, want the Escape only (nothing typed or cleared)", replace, got)
		}
	}
}

// Requests whose client left while another write held the lock type nothing
// once they get it, and answer nothing.
func TestRemoteWritesDroppedWhenClientGone(t *testing.T) {
	s := idleSession()
	_, ft, h := newTestRemote(t, testFleet(s), nil)
	ft.screens["%1"] = idleScreen("")
	typing, release := make(chan string, 2), make(chan struct{})
	ft.set(func(f *fakeRemoteTmux) { f.typing, f.release = typing, release })

	firstDone := make(chan int, 1)
	go func() {
		firstDone <- remoteDo(t, h, "POST", "/api/remote/send", testRemoteToken,
			remoteSendReq{Name: "a", Episode: remoteEpisode(s), Text: "first"}).Code
	}()
	if got := <-typing; got != "first" {
		t.Fatalf("typing %q, want first", got)
	}

	// The requests start with a LIVE context and queue behind the lock; the
	// phone gives up only once they are parked there.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	routes := []remoteRoute{
		{"POST", "/api/remote/send", remoteSendReq{Name: "a", Episode: remoteEpisode(s), Text: "second"}},
		{"POST", "/api/remote/key", remoteKeyReq{Name: "a", Episode: remoteEpisode(s), Key: "1"}},
		{"POST", "/api/remote/key", remoteKeyReq{Name: "a", Key: "Escape"}},
	}
	recs := make([]*httptest.ResponseRecorder, len(routes))
	var wg sync.WaitGroup
	for i, rt := range routes {
		req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(routeBody(t, rt))).WithContext(ctx)
		req.RemoteAddr = "127.0.0.1:5555"
		req.Host = "localhost:7474"
		req.Header.Set("Authorization", "Bearer "+testRemoteToken)
		req.Header.Set("Content-Type", "application/json")
		recs[i] = httptest.NewRecorder()
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h.ServeHTTP(recs[i], req)
		}(i)
	}
	waitWriteMuWaiters(t, "handleSend", 1)
	waitWriteMuWaiters(t, "handleKey", 2)
	if got := ft.log(); !reflect.DeepEqual(got, []string{"type %1 first"}) {
		t.Fatalf("queued requests wrote before the lock was free: %q", got)
	}
	cancel()
	close(release)
	if code := <-firstDone; code != http.StatusOK {
		t.Fatalf("first send: %d", code)
	}
	wg.Wait()
	for i, rec := range recs {
		if rec.Body.Len() != 0 {
			t.Errorf("%s %+v answered a gone client: %s", routes[i].path, routes[i].body, rec.Body)
		}
	}
	want := []string{"type %1 first", "keys %1 Enter"}
	if got := ft.log(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want only the first send's", got)
	}
}

// A session that leaves the fleet loses its suggestion entry at the next
// queue poll.
func TestRemoteQueuePrunesGoneSessions(t *testing.T) {
	a, b := idleSession(), swSession{Name: "b", State: "Idle", Since: rt0, ClaudePane: "%2", Context: -1}
	fleet := testFleet(a, b)
	gen := &fakeSuggester{out: []remoteSuggestion{{Label: "x", Text: "x"}}}
	r, _, h := newTestRemote(t, fleet, gen)
	r.suggest.ensure(a, nil, func() string { return "screen" })
	r.suggest.ensure(b, nil, func() string { return "screen" })
	waitFor(t, "both drafts", func() bool {
		return r.suggest.get(a).Status == suggestStatusReady && r.suggest.get(b).Status == suggestStatusReady
	})
	fleet.publish(swSnapshot{Sessions: []swSession{b}}, RateLimits{}, false, nil, rt0)
	if rec := remoteDo(t, h, "GET", "/api/remote/queue", testRemoteToken, nil); rec.Code != http.StatusOK {
		t.Fatalf("queue: %d %s", rec.Code, rec.Body)
	}
	r.suggest.mu.Lock()
	_, hasA := r.suggest.entries["a"]
	_, hasB := r.suggest.entries["b"]
	r.suggest.mu.Unlock()
	if hasA || !hasB {
		t.Fatalf("after prune: a kept=%v, b kept=%v; want only b", hasA, hasB)
	}
}

// prefetch reads the next stop's screen only when a call is actually due.
func TestRemotePrefetchSkipsWhenNoCallIsDue(t *testing.T) {
	next := swSession{Name: "b", State: "Idle", Since: rt0, ClaudePane: "%2", Context: -1}

	ready := &fakeSuggester{out: []remoteSuggestion{{Label: "x", Text: "x"}}}
	r, ft, _ := newTestRemote(t, testFleet(next), ready)
	ft.screens["%2"] = idleScreen("")
	r.prefetch(next)
	waitFor(t, "the prefetched draft", func() bool { return r.suggest.get(next).Status == suggestStatusReady })
	if got := ft.captured(); !reflect.DeepEqual(got, []string{"%2"}) {
		t.Fatalf("first prefetch captures = %q, want one of %%2", got)
	}
	r.prefetch(next)
	if got := ft.captured(); len(got) != 1 || ready.count() != 1 {
		t.Fatalf("prefetch of a ready stop: captures %q, calls %d", got, ready.count())
	}

	// Failed inside its back-off: no capture either.
	clock := &fakeClock{t: rt0}
	failing := &fakeSuggester{err: errors.New("boom")}
	r, ft, _ = newTestRemote(t, testFleet(next), failing)
	r.suggest.now = clock.now
	ft.screens["%2"] = idleScreen("")
	r.prefetch(next)
	waitFor(t, "the failed prefetch", func() bool { return r.suggest.get(next).Status == suggestStatusFailed })
	clock.advance(suggestRetryAfter - time.Second)
	r.prefetch(next)
	if got := ft.captured(); len(got) != 1 || failing.count() != 1 {
		t.Fatalf("prefetch inside the back-off: captures %q, calls %d", got, failing.count())
	}

	// Not waiting, or no suggester: nothing to draft, nothing captured.
	busy := next
	busy.State = "Thinking"
	r, ft, _ = newTestRemote(t, testFleet(busy), ready)
	ft.screens["%2"] = idleScreen("")
	r.prefetch(busy)
	r2, ft2, _ := newTestRemote(t, testFleet(next), nil)
	ft2.screens["%2"] = idleScreen("")
	r2.prefetch(next)
	if len(ft.captured())+len(ft2.captured()) != 0 {
		t.Fatalf("captured with nothing to draft: %q %q", ft.captured(), ft2.captured())
	}
}

// startSwitchboardWeb with web.control on: the page always loads, and the
// API answers 503 setup until a usable token is present.
func TestStartSwitchboardWebWithControl(t *testing.T) {
	cfg := defaultConfig()
	cfg.Web.Listen = "127.0.0.1:0"
	cfg.Web.Control = true
	cfg.Summary.Enabled = false
	cfg.Summary.APIKeyFile = t.TempDir() + "/env" // never the real one
	noSuffix := func() (webAllow, error) { return webAllow{}, nil }
	noIP := func() (string, error) { return "", errors.New("unused") }

	get := func(t *testing.T, w *swWeb, path string) (int, string) {
		t.Helper()
		req, err := http.NewRequest("GET", "http://"+w.addr()+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testRemoteToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(body)
	}

	t.Run("no token", func(t *testing.T) {
		t.Setenv(remoteTokenKey, "")
		w, err := startSwitchboardWeb(cfg, noIP, noSuffix)
		if err != nil || w == nil {
			t.Fatalf("got %+v, %v", w, err)
		}
		defer w.stop()
		if code, body := get(t, w, "/remote"); code != http.StatusOK || !strings.Contains(body, "claudemux remote") {
			t.Fatalf("page: %d", code)
		}
		code, body := get(t, w, "/api/remote/queue")
		var e remoteErrorBody
		_ = json.Unmarshal([]byte(body), &e)
		if code != http.StatusServiceUnavailable || e.Code != "setup" {
			t.Fatalf("queue: %d %s, want 503 setup", code, body)
		}
		if want := remoteTokenKey + " is not set (looked in the environment and the summary key file)"; e.Message != want {
			t.Errorf("setup message = %q, want %q", e.Message, want)
		}
		if strings.Contains(body, cfg.Summary.APIKeyFile) {
			t.Errorf("setup body leaks the key file path: %s", body)
		}
	})
	t.Run("short token", func(t *testing.T) {
		t.Setenv(remoteTokenKey, "8bytes!!")
		w, err := startSwitchboardWeb(cfg, noIP, noSuffix)
		if err != nil || w == nil {
			t.Fatalf("got %+v, %v", w, err)
		}
		defer w.stop()
		code, body := get(t, w, "/api/remote/queue")
		var e remoteErrorBody
		_ = json.Unmarshal([]byte(body), &e)
		if code != http.StatusServiceUnavailable || e.Code != "setup" {
			t.Fatalf("queue: %d %s, want 503 setup", code, body)
		}
		if want := remoteTokenKey + " is too short (8 bytes; it needs at least 16)"; e.Message != want {
			t.Errorf("setup message = %q, want %q", e.Message, want)
		}
		if strings.Contains(body, cfg.Summary.APIKeyFile) || strings.Contains(body, "8bytes!!") {
			t.Errorf("setup body leaks the key file path or the token: %s", body)
		}
	})
	t.Run("token", func(t *testing.T) {
		t.Setenv(remoteTokenKey, testRemoteToken)
		w, err := startSwitchboardWeb(cfg, noIP, noSuffix)
		if err != nil || w == nil {
			t.Fatalf("got %+v, %v", w, err)
		}
		defer w.stop()
		code, body := get(t, w, "/api/remote/queue")
		if code != http.StatusOK {
			t.Fatalf("queue: %d %s", code, body)
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(body), &raw); err != nil {
			t.Fatal(err)
		}
		if got, ok := raw["suggest"].(bool); !ok || got {
			t.Errorf("suggest = %v, want false with summaries disabled", raw["suggest"])
		}
	})
}

// tmuxIsolated points every tmux call this process makes (execRemoteTmux
// included) at a private server, starts it with one pane running shell, and
// returns that pane's id.
func tmuxIsolated(t *testing.T, shell string) string {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	// Short and outside $TMPDIR: a unix socket path is capped near 104 bytes.
	dir, err := os.MkdirTemp("/tmp", "cmxrt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TMUX_TMPDIR", dir)
	out, err := exec.Command("tmux", "-f", "/dev/null", "new-session", "-d", "-x", "120", "-y", "20",
		"-s", "t", "-P", "-F", "#{pane_id}", shell).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-server").Run() })
	return strings.TrimSpace(string(out))
}

// tmuxRemoteBuffers lists the remote's paste buffers left in the server.
func tmuxRemoteBuffers(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("tmux", "list-buffers", "-F", "#{buffer_name}").CombinedOutput()
	if err != nil {
		t.Fatalf("list-buffers: %v: %s", err, out)
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "claudemux-remote-") {
			names = append(names, l)
		}
	}
	return names
}

// A multi-line message goes in as ONE bracketed paste: the newline arrives
// inside the paste brackets (where claude treats it as text), never as a
// bare Enter between the lines.
func TestExecRemoteTmuxTypeTextMultiLine(t *testing.T) {
	// The pane asks for bracketed paste as claude does, then shows every
	// byte it gets: a raw tty (no echo, CR kept as CR) read by cat -v.
	pane := tmuxIsolated(t, `printf '\033[?2004h'; stty raw -echo; echo READY; exec cat -v`)
	tm := execRemoteTmux{}
	waitFor(t, "the pane to be ready", func() bool {
		s, err := tm.capture(pane, false, 0)
		return err == nil && strings.Contains(s, "READY")
	})
	if err := tm.typeText(pane, "a\nb"); err != nil {
		t.Fatalf("typeText: %v", err)
	}
	// cat -v flushes at a newline: a raw LF (C-j) shows what came before it.
	if err := tm.keys(pane, "C-j"); err != nil {
		t.Fatal(err)
	}
	var screen string
	waitFor(t, "the paste to show", func() bool {
		s, err := tm.capture(pane, false, 0)
		screen = s
		return err == nil && strings.Contains(s, "201~")
	})
	if want := "^[[200~a^Mb^[[201~"; !strings.Contains(screen, want) {
		t.Fatalf("pane shows:\n%s\nwant one bracketed paste %q (a, then b, no Enter outside the brackets)", screen, want)
	}
	if bufs := tmuxRemoteBuffers(t); len(bufs) != 0 {
		t.Fatalf("paste buffers left behind: %q", bufs)
	}
}

func TestExecRemoteTmuxTypeTextMissingPane(t *testing.T) {
	tmuxIsolated(t, "cat")
	err := execRemoteTmux{}.typeText("%999", "a\nb")
	if err == nil {
		t.Fatal("paste into a missing pane: want an error")
	}
	if msg := err.Error(); !strings.Contains(msg, "paste-buffer") && !strings.Contains(msg, "%999") {
		t.Errorf("error %q names neither paste-buffer nor the pane", msg)
	}
	if bufs := tmuxRemoteBuffers(t); len(bufs) != 0 {
		t.Fatalf("a failed paste left its buffer behind: %q", bufs)
	}
}

// The episode check happens UNDER the write lock: a request that queued
// behind another write while its session moved on is refused once it gets
// the lock, instead of answering a question it never saw. The first write is
// to another session so the refusal can only come from the episode check.
func TestRemoteEpisodeCheckedUnderTheLock(t *testing.T) {
	for _, tc := range []struct {
		what, path, handler, state string
		body                       func(s swSession) any
	}{
		{"send", "/api/remote/send", "handleSend", "Idle", func(s swSession) any {
			return remoteSendReq{Name: "a", Episode: remoteEpisode(s), Text: "yes"}
		}},
		{"key", "/api/remote/key", "handleKey", "Awaiting", func(s swSession) any {
			return remoteKeyReq{Name: "a", Episode: remoteEpisode(s), Key: "1"}
		}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			a := idleSession()
			a.State = tc.state
			b := swSession{Name: "b", State: "Idle", Since: rt0, ClaudePane: "%2", Context: -1}
			fleet := testFleet(a, b)
			r, ft, h := newTestRemote(t, fleet, nil)
			ft.screens["%1"] = idleScreen("")
			if tc.state == "Awaiting" {
				ft.screens["%1"] = dialogScreen()
			}
			ft.screens["%2"] = idleScreen("")
			typing, release := make(chan string, 1), make(chan struct{})
			ft.set(func(f *fakeRemoteTmux) { f.typing, f.release = typing, release })

			first := make(chan int, 1)
			go func() {
				first <- remoteDo(t, h, "POST", "/api/remote/send", testRemoteToken,
					remoteSendReq{Name: "b", Episode: remoteEpisode(b), Text: "first"}).Code
			}()
			if got := <-typing; got != "first" {
				t.Fatalf("typing %q, want first", got)
			}
			second := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				second <- remoteDo(t, h, "POST", tc.path, testRemoteToken, tc.body(a))
			}()
			waitWriteMuWaiters(t, tc.handler, 1)
			if r.writeMu.TryLock() {
				r.writeMu.Unlock()
				t.Fatal("write lock not held while the first send is typing")
			}
			if got := ft.captured(); !reflect.DeepEqual(got, []string{"%2"}) {
				t.Fatalf("captures while queued = %q, want only the first send's", got)
			}
			// While #2 waits, a moves on to a new episode.
			moved := a
			moved.State = "Thinking"
			moved.Since = rt0.Add(time.Minute)
			fleet.publish(swSnapshot{Sessions: []swSession{moved, b}}, RateLimits{}, false, nil, rt0)
			close(release)
			if code := <-first; code != http.StatusOK {
				t.Fatalf("first send: %d", code)
			}
			rec := <-second
			if rec.Code != http.StatusConflict || remoteCode(t, rec) != "moved" {
				t.Fatalf("queued %s: got %d %s, want 409 moved", tc.what, rec.Code, rec.Body)
			}
			want := []string{"type %2 first", "keys %2 Enter"}
			if got := ft.log(); !reflect.DeepEqual(got, want) {
				t.Fatalf("calls = %q, want only the first send's %q", got, want)
			}
		})
	}
}

// vimInsertScreen is the idle input box as vim mode draws it while typing.
func vimInsertScreen(draft string) string {
	rule := strings.Repeat("─", 30)
	return "● Done. Tests pass.\n\n" + rule + "\n❯ " + draft + "\n" + rule + "\n  -- INSERT --\n"
}

// handleSend only types into a prompt it has recognised as empty, and only
// clears a draft it has recognised as a draft; anything else is refused with
// a code the page can explain, before any key is pressed.
func TestRemoteSendVimAndReplaceSafety(t *testing.T) {
	for _, tc := range []struct {
		what        string
		vim         bool
		screen      string
		clearScreen string
		replace     bool
		status      int
		code        string
		calls       []string
	}{
		{what: "vim NORMAL, empty box", vim: true, screen: idleScreen(""),
			status: http.StatusConflict, code: "vim_normal"},
		{what: "vim NORMAL, replace", vim: true, screen: idleScreen(""), replace: true,
			status: http.StatusConflict, code: "vim_normal"},
		{what: "vim NORMAL, draft, replace", vim: true, screen: idleScreen("half"), replace: true,
			status: http.StatusConflict, code: "vim_normal"},
		{what: "vim INSERT, empty box", vim: true, screen: vimInsertScreen(""),
			status: http.StatusOK, calls: []string{"type %1 go", "keys %1 Enter"}},
		{what: "vim INSERT, draft", vim: true, screen: vimInsertScreen("half"),
			status: http.StatusConflict, code: "draft"},
		{what: "vim INSERT, draft, replace", vim: true, screen: vimInsertScreen("half"), clearScreen: vimInsertScreen(""), replace: true,
			status: http.StatusOK, calls: []string{"keys %1 C-u", "type %1 go", "keys %1 Enter"}},
		{what: "no input box", screen: "Running the tests…\n",
			status: http.StatusConflict, code: "unrecognized"},
		{what: "no input box, replace", screen: "Running the tests…\n", replace: true,
			status: http.StatusConflict, code: "unrecognized"},
		{what: "draft, replace, C-u clears nothing", screen: idleScreen("half"), clearScreen: idleScreen("half"), replace: true,
			status: http.StatusConflict, code: "not_cleared", calls: []string{"keys %1 C-u"}},
		{what: "draft, replace, C-u clears it", screen: idleScreen("half"), replace: true,
			status: http.StatusOK, calls: []string{"keys %1 C-u", "type %1 go", "keys %1 Enter"}},
	} {
		s := idleSession()
		r, ft, h := newTestRemote(t, testFleet(s), nil)
		r.vimMode = tc.vim
		ft.screens["%1"] = tc.screen
		ft.clearScreen = tc.clearScreen
		rec := remoteDo(t, h, "POST", "/api/remote/send", testRemoteToken,
			remoteSendReq{Name: "a", Episode: remoteEpisode(s), Text: "go", Replace: tc.replace})
		if rec.Code != tc.status || (tc.code != "" && remoteCode(t, rec) != tc.code) {
			t.Errorf("%s: got %d %s, want %d %s", tc.what, rec.Code, rec.Body, tc.status, tc.code)
		}
		if got := ft.log(); !reflect.DeepEqual(got, tc.calls) {
			t.Errorf("%s: calls = %q, want %q", tc.what, got, tc.calls)
		}
	}
}

// One answer per episode: a second send, or a second option key, for the
// same name+episode is refused as "moved" even before the session publishes
// its new state. Moving around a dialog (and Escape) is not an answer.
func TestRemoteAnsweredEpisodeIsNotAnsweredTwice(t *testing.T) {
	t.Run("send", func(t *testing.T) {
		s := idleSession()
		fleet := testFleet(s)
		_, ft, h := newTestRemote(t, fleet, nil)
		ft.screens["%1"] = idleScreen("")
		send := func(sess swSession) *httptest.ResponseRecorder {
			return remoteDo(t, h, "POST", "/api/remote/send", testRemoteToken,
				remoteSendReq{Name: "a", Episode: remoteEpisode(sess), Text: "go"})
		}
		if rec := send(s); rec.Code != http.StatusOK {
			t.Fatalf("first send: %d %s", rec.Code, rec.Body)
		}
		if rec := send(s); rec.Code != http.StatusConflict || remoteCode(t, rec) != "moved" {
			t.Fatalf("resend: got %d %s, want 409 moved", rec.Code, rec.Body)
		}
		if got := ft.log(); len(got) != 2 {
			t.Fatalf("resend wrote: %q", got)
		}
		// A new episode is a new question.
		next := s
		next.Since = rt0.Add(time.Minute)
		fleet.publish(swSnapshot{Sessions: []swSession{next}}, RateLimits{}, false, nil, rt0)
		if rec := send(next); rec.Code != http.StatusOK {
			t.Fatalf("send in the next episode: %d %s", rec.Code, rec.Body)
		}
		if got := ft.log(); len(got) != 4 {
			t.Fatalf("calls = %q, want two full sends", got)
		}
	})
	t.Run("keys", func(t *testing.T) {
		s := idleSession()
		s.State = "Awaiting"
		_, ft, h := newTestRemote(t, testFleet(s), nil)
		ft.screens["%1"] = dialogScreen()
		key := func(k string) *httptest.ResponseRecorder {
			return remoteDo(t, h, "POST", "/api/remote/key", testRemoteToken,
				remoteKeyReq{Name: "a", Episode: remoteEpisode(s), Key: k})
		}
		for _, k := range []string{"Down", "Down", "Up", "Tab", "BTab", "Space"} {
			if rec := key(k); rec.Code != http.StatusOK {
				t.Fatalf("%s: got %d %s, want 200 (moving is not answering)", k, rec.Code, rec.Body)
			}
		}
		if rec := key("1"); rec.Code != http.StatusOK {
			t.Fatalf("first 1: %d %s", rec.Code, rec.Body)
		}
		for _, k := range []string{"1", "2", "Enter"} {
			if rec := key(k); rec.Code != http.StatusConflict || remoteCode(t, rec) != "moved" {
				t.Errorf("%s after an answer: got %d %s, want 409 moved", k, rec.Code, rec.Body)
			}
		}
		for _, k := range []string{"Down", "Escape"} {
			if rec := key(k); rec.Code != http.StatusOK {
				t.Errorf("%s after an answer: got %d %s, want 200 (exempt)", k, rec.Code, rec.Body)
			}
		}
		want := []string{"keys %1 Down", "keys %1 Down", "keys %1 Up", "keys %1 Tab", "keys %1 BTab", "keys %1 Space",
			"keys %1 1", "keys %1 Down", "keys %1 Escape"}
		if got := ft.log(); !reflect.DeepEqual(got, want) {
			t.Fatalf("calls = %q, want %q", got, want)
		}
	})
	t.Run("Enter answers too", func(t *testing.T) {
		s := idleSession()
		s.State = "Awaiting"
		_, ft, h := newTestRemote(t, testFleet(s), nil)
		ft.screens["%1"] = dialogScreen()
		for i, want := range []int{http.StatusOK, http.StatusConflict} {
			rec := remoteDo(t, h, "POST", "/api/remote/key", testRemoteToken,
				remoteKeyReq{Name: "a", Episode: remoteEpisode(s), Key: "Enter"})
			if rec.Code != want {
				t.Errorf("Enter #%d: got %d %s, want %d", i+1, rec.Code, rec.Body, want)
			}
		}
		if got := ft.log(); !reflect.DeepEqual(got, []string{"keys %1 Enter"}) {
			t.Fatalf("calls = %q", got)
		}
	})
}

// The defer reason is shown at the desk and on the phone: control characters
// are refused, and an over-long reason is cut to 200 runes.
func TestRemoteDeferReason(t *testing.T) {
	_, ft, h := newTestRemote(t, testFleet(idleSession()), nil)
	for _, reason := range []string{"ci\x1b[2J", "ding\x07", "a\u009bb"} {
		rec := remoteDo(t, h, "POST", "/api/remote/defer", testRemoteToken, remoteDeferReq{Name: "a", On: true, Reason: reason})
		if rec.Code != http.StatusBadRequest || remoteCode(t, rec) != "control" {
			t.Errorf("reason %q: got %d %s, want 400 control", reason, rec.Code, rec.Body)
		}
	}
	if got := ft.log(); len(got) != 0 {
		t.Fatalf("refused defers ran tmux: %q", got)
	}
	long := strings.Repeat("é", 300)
	rec := remoteDo(t, h, "POST", "/api/remote/defer", testRemoteToken, remoteDeferReq{Name: "a", On: true, Reason: long})
	if rec.Code != http.StatusOK {
		t.Fatalf("long reason: got %d %s", rec.Code, rec.Body)
	}
	want := "run " + strings.Join(deferArgs("%0", true, strings.Repeat("é", 200)), " ")
	if got := ft.log(); len(got) != 1 || got[0] != want {
		t.Fatalf("calls = %q, want the reason cut to 200 runes", got)
	}
}

// Every error code the page branches on is one the server emits, and the
// page names an episode on every send and key, which the server requires.
func TestRemotePageAndServerAgreeOnCodes(t *testing.T) {
	page := string(webRemoteHTML)
	src, err := os.ReadFile("webremote.go")
	if err != nil {
		t.Fatal(err)
	}
	// "episode" and "control" are emitted too, but the page has no special
	// handling for them: they reach the user through the generic error toast.
	for _, code := range []string{"token", "setup", "draft", "dismissed_draft", "dialog", "moved", "partial",
		"vim_normal", "unrecognized", "not_cleared"} {
		if !strings.Contains(page, "'"+code+"'") {
			t.Errorf("the page never checks for code %q", code)
		}
		if !strings.Contains(string(src), `"`+code+`"`) {
			t.Errorf("webremote.go never emits code %q", code)
		}
	}
	for _, path := range []string{"send", "key"} {
		re := regexp.MustCompile(`api\('/api/remote/` + path + `',\s*\{[^}]*\bepisode\b`)
		if !re.MatchString(page) {
			t.Errorf("the page's %s call does not carry an episode", path)
		}
	}
	if strings.Contains(page, "Haiku had nothing to suggest") || !strings.Contains(page, "Nothing to suggest for this one.") {
		t.Error("the empty-suggestions note should read \"Nothing to suggest for this one.\"")
	}
}

// A tab goes in through the paste path, not send-keys -l (where tmux would
// read it as a key): it arrives as a literal tab inside the paste brackets.
func TestExecRemoteTmuxTypeTextTab(t *testing.T) {
	pane := tmuxIsolated(t, `printf '\033[?2004h'; stty raw -echo; echo READY; exec cat -v`)
	tm := execRemoteTmux{}
	waitFor(t, "the pane to be ready", func() bool {
		s, err := tm.capture(pane, false, 0)
		return err == nil && strings.Contains(s, "READY")
	})
	if err := tm.typeText(pane, "a\tb"); err != nil {
		t.Fatalf("typeText: %v", err)
	}
	if err := tm.keys(pane, "C-j"); err != nil {
		t.Fatal(err)
	}
	var screen string
	waitFor(t, "the paste to show", func() bool {
		s, err := tm.capture(pane, false, 0)
		screen = s
		return err == nil && strings.Contains(s, "201~")
	})
	// cat -v leaves a tab as a tab, and the capture may expand it to spaces:
	// either way it must sit inside the paste brackets.
	if want := regexp.MustCompile(`\^\[\[200~a[\t ]+b\^\[\[201~`); !want.MatchString(screen) {
		t.Fatalf("pane shows:\n%s\nwant one bracketed paste of a<tab>b", screen)
	}
	if bufs := tmuxRemoteBuffers(t); len(bufs) != 0 {
		t.Fatalf("paste buffers left behind: %q", bufs)
	}
}
