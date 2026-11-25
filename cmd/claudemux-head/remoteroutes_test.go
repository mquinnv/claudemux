package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const remoteTestToken = "glasses-test-token-0123456789"

var remoteT0 = time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)

// fakeRemote stands in for tmux and the transcript: gather returns a capture
// per pane, SendKey records the key and swaps the pane to its after-capture,
// Nudge records the text and returns a configured outcome.
type fakeRemote struct {
	mu        sync.Mutex
	capture   map[string]string // pane -> plain capture
	captureE  map[string]string // pane -> -e capture
	after     map[string]string // pane -> capture once a key lands
	lastText  map[string]string
	gatherErr error
	keys      []string // "pane key"
	nudges    []string // "pane text"
	nudgeOut  nudgeOutcome
	keyErr    error
	keyGate   chan struct{} // when set, SendKey waits on it
	keyIn     chan struct{} // when set, SendKey signals entry
	defers    []string      // "name|reason"
	deferErr  error
	suggests  int
}

func newFakeRemote() *fakeRemote {
	return &fakeRemote{
		capture:  map[string]string{},
		captureE: map[string]string{},
		after:    map[string]string{},
		lastText: map[string]string{},
		nudgeOut: nudgeSent,
	}
}

func (f *fakeRemote) gather(s swSession) (remoteInputs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gatherErr != nil {
		return remoteInputs{}, f.gatherErr
	}
	return remoteInputs{
		Sess:     s,
		Capture:  f.capture[s.ClaudePane],
		CaptureE: f.captureE[s.ClaudePane],
		LastText: f.lastText[s.ClaudePane],
		VimMode:  true,
	}, nil
}

func (f *fakeRemote) rio(pane string, rebuild func() (remoteDetail, error)) remoteIO {
	return remoteIO{
		Rebuild: rebuild,
		SendKey: func(p, key string) error {
			if f.keyIn != nil {
				f.keyIn <- struct{}{}
			}
			if f.keyGate != nil {
				<-f.keyGate
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			f.keys = append(f.keys, p+" "+key)
			if f.keyErr != nil {
				return f.keyErr
			}
			if a, ok := f.after[p]; ok {
				f.capture[p] = a
			}
			return nil
		},
		Nudge: func(p, text string) nudgeOutcome {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.nudges = append(f.nudges, p+" "+text)
			return f.nudgeOut
		},
		Sleep: func(time.Duration) {},
	}
}

func (f *fakeRemote) deferFn(s swSession, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.defers = append(f.defers, s.Name+"|"+reason)
	return f.deferErr
}

func (f *fakeRemote) sent() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.keys...), append([]string(nil), f.nudges...)
}

// permitPane makes pane show the bash permit dialog, and the empty prompt
// once a key lands.
func (f *fakeRemote) permitPane(t *testing.T, pane string) {
	t.Helper()
	f.capture[pane] = readFixture(t, "dialog_permit_bash.txt")
	e := readFixture(t, "prompt_empty.txt")
	f.after[pane] = ansi.Strip(e)
}

func (f *fakeRemote) idlePane(t *testing.T, pane, last string) {
	t.Helper()
	e := readFixture(t, "prompt_empty.txt")
	f.capture[pane], f.captureE[pane], f.lastText[pane] = ansi.Strip(e), e, last
}

func remoteTestHub(t *testing.T, sessions []swSession, f *fakeRemote) (*remoteHub, http.Handler) {
	t.Helper()
	fleet := newWebFleet()
	fleet.publish(swSnapshot{Sessions: sessions}, RateLimits{}, false, nil, remoteT0)
	h := newRemoteHub(fleet, RemoteConfig{Enabled: true, Replies: []string{"Continue", "No, stop and wait for me"}}, remoteTestToken, nil)
	h.gather, h.rio = f.gather, f.rio
	h.setDeferFn(f.deferFn)
	return h, webHandler(fleet, webAllow{Suffix: webTestAllowSuffix}, h)
}

func remoteReq(method, path, body, token string) *http.Request {
	r := webTestRequest(method, path)
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr, r.Host = "127.0.0.1:9999", "localhost"
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func getNext(t *testing.T, h http.Handler, query string) remoteNextView {
	t.Helper()
	rec := serve(h, remoteReq("GET", "/remote/next"+query, "", remoteTestToken))
	if rec.Code != 200 {
		t.Fatalf("next: %d %s", rec.Code, rec.Body)
	}
	var v remoteNextView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("next body: %v\n%s", err, rec.Body)
	}
	return v
}

func resultOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var v struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("body %q: %v", rec.Body, err)
	}
	return v.Result
}

func waitingSess(name, pane string, since time.Time) swSession {
	return swSession{Name: name, State: "Awaiting", Since: since, ClaudePane: pane, HeadPane: pane + "h", Context: -1}
}

func TestRemoteRequiresToken(t *testing.T) {
	f := newFakeRemote()
	f.permitPane(t, "%1")
	_, h := remoteTestHub(t, []swSession{waitingSess("a", "%1", remoteT0)}, f)

	for _, c := range []struct {
		name, auth string
	}{
		{"none", ""},
		{"wrong", "Bearer nope"},
		{"prefix of the token", "Bearer " + remoteTestToken[:10]},
		{"token plus suffix", "Bearer " + remoteTestToken + "x"},
		{"no scheme", remoteTestToken},
		{"basic scheme", "Basic " + remoteTestToken},
	} {
		r := remoteReq("GET", "/remote/next", "", "")
		if c.auth != "" {
			r.Header.Set("Authorization", c.auth)
		}
		rec := serve(h, r)
		if rec.Code != 401 || rec.Body.Len() != 0 {
			t.Errorf("%s: %d %q, want 401 with an empty body", c.name, rec.Code, rec.Body)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s: a 401 must still carry CORS so the app can read it", c.name)
		}
	}
	for _, p := range []string{"/remote/next", "/remote/fleet"} {
		if rec := serve(h, remoteReq("GET", p, "", remoteTestToken)); rec.Code != 200 {
			t.Errorf("%s with the token: %d %s", p, rec.Code, rec.Body)
		}
	}
	for _, p := range []string{"/remote/next", "/remote/answer", "/remote/anything"} {
		rec := serve(h, remoteReq("OPTIONS", p, "", ""))
		if rec.Code != 204 || rec.Body.Len() != 0 {
			t.Errorf("OPTIONS %s: %d %q", p, rec.Code, rec.Body)
		}
		for k, v := range map[string]string{
			"Access-Control-Allow-Origin":  "*",
			"Access-Control-Allow-Headers": "Authorization, Content-Type",
			"Access-Control-Allow-Methods": "GET, POST, OPTIONS",
		} {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("OPTIONS %s: %s = %q, want %q", p, k, got, v)
			}
		}
	}
}

func TestRemoteRoutesAbsentWhenDisabled(t *testing.T) {
	fleet := newWebFleet()
	fleet.publish(webTestSnapshot(), RateLimits{}, false, nil, time.Now())
	h := webHandler(fleet, webAllow{}, nil)
	for _, m := range []string{"GET", "POST", "OPTIONS"} {
		if rec := serve(h, remoteReq(m, "/remote/next", "", remoteTestToken)); rec.Code != 404 {
			t.Errorf("%s /remote/next with no hub: %d, want 404", m, rec.Code)
		}
	}
	if rec := serve(h, remoteReq("GET", "/api/fleet", "", "")); rec.Code != 200 {
		t.Errorf("/api/fleet: %d", rec.Code)
	}
}

// startSwitchboardWeb builds the hub only with remote.enabled and a token; a
// missing token is reported for the status line and the routes stay absent.
func TestStartSwitchboardWebRemoteWiring(t *testing.T) {
	t.Setenv("CLAUDEMUX_ENV", "")
	t.Setenv(remoteTokenKey, "")
	envFile := filepath.Join(t.TempDir(), "env")
	cfg := defaultConfig()
	cfg.Web.Listen = "127.0.0.1:0"
	cfg.Summary.Enabled = false
	cfg.Summary.APIKeyFile = envFile
	noAllow := func() (webAllow, error) { return webAllow{}, nil }
	noIP := func() (string, error) { return "", errors.New("unused") }
	get := func(addr, auth string) int {
		req, _ := http.NewRequest("GET", "http://"+addr+"/remote/next", nil)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// Disabled: no hub, no note, no routes.
	w, err := startSwitchboardWeb(cfg, noIP, noAllow)
	if err != nil || w == nil {
		t.Fatalf("%v %v", w, err)
	}
	if w.remote != nil || w.remoteErr != "" || get(w.addr(), "x") != 404 {
		t.Errorf("disabled: remote=%v err=%q", w.remote, w.remoteErr)
	}
	w.stop()

	// Enabled without a token: the page runs, the remote does not.
	cfg.Remote.Enabled = true
	w, err = startSwitchboardWeb(cfg, noIP, noAllow)
	if err != nil || w == nil {
		t.Fatalf("%v %v", w, err)
	}
	if w.remote != nil || w.remoteErr != "remote: CLAUDEMUX_REMOTE_TOKEN is not set" {
		t.Errorf("no token: remote=%v err=%q", w.remote, w.remoteErr)
	}
	if code := get(w.addr(), ""); code != 404 {
		t.Errorf("no token: /remote/next = %d, want 404", code)
	}
	w.stop()

	// Enabled with a token from the env file.
	if err := os.WriteFile(envFile, []byte(remoteTokenKey+"=filetoken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err = startSwitchboardWeb(cfg, noIP, noAllow)
	if err != nil || w == nil || w.remote == nil || w.remoteErr != "" {
		t.Fatalf("with token: %+v %v", w, err)
	}
	defer w.stop()
	if code := get(w.addr(), ""); code != 401 {
		t.Errorf("no auth: %d", code)
	}
	if code := get(w.addr(), "filetoken"); code != 200 {
		t.Errorf("auth: %d", code)
	}

	// Enabled without web.listen: reported, nothing started.
	cfg.Web.Listen = ""
	if w2, err := startSwitchboardWeb(cfg, noIP, noAllow); w2 != nil || err == nil || !strings.Contains(err.Error(), "web.listen") {
		t.Errorf("no listen: %+v %v", w2, err)
	}
}

func TestRemoteNextShowsQueueHead(t *testing.T) {
	f := newFakeRemote()
	f.permitPane(t, "%1")
	f.permitPane(t, "%2")
	_, h := remoteTestHub(t, []swSession{
		waitingSess("newer", "%1", remoteT0.Add(time.Minute)),
		waitingSess("older", "%2", remoteT0),
	}, f)

	rec := serve(h, remoteReq("GET", "/remote/next", "", remoteTestToken))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"taken_at", "queue", "working", "detail"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing %q in %s", k, rec.Body)
		}
	}
	v := getNext(t, h, "")
	if v.Detail == nil || v.Detail.Session != "older" || v.Queue.Pos != 1 || v.Queue.Total != 2 {
		t.Fatalf("got %+v", v)
	}
	if v.Detail.Kind != "permit" || len(v.Detail.Options) == 0 || v.Detail.Episode == "" {
		t.Errorf("detail %+v", v.Detail)
	}
	if v.TakenAt != "2026-11-25T09:00:00Z" {
		t.Errorf("taken_at %q", v.TakenAt)
	}
}

func TestRemoteNextEmptyQueue(t *testing.T) {
	f := newFakeRemote()
	_, h := remoteTestHub(t, []swSession{
		{Name: "a", State: "Thinking", ClaudePane: "%1"},
		{Name: "b", State: "Tool:Bash", ClaudePane: "%2"},
	}, f)
	rec := serve(h, remoteReq("GET", "/remote/next", "", remoteTestToken))
	if !strings.Contains(rec.Body.String(), `"detail":null`) {
		t.Errorf("detail must be null: %s", rec.Body)
	}
	v := getNext(t, h, "")
	if v.Detail != nil || v.Working != 2 || v.Queue.Pos != 0 || v.Queue.Total != 0 {
		t.Errorf("%+v", v)
	}
}

func TestRemoteNextIncludesPrivateSessions(t *testing.T) {
	p, _ := fakePrivacy([]string{"mquinnv"},
		map[string]string{"secret": "/m", "open": "/c"},
		map[string]string{"/m": "git@github.com:mquinnv/menuops.git", "/c": "git@github.com:mquinnv/claudemux.git"},
		map[string]bool{"mquinnv/menuops": true}, nil)
	p.pass()
	fleet := newWebFleet()
	fleet.privacy = p
	fleet.publish(swSnapshot{Sessions: []swSession{
		waitingSess("secret", "%1", remoteT0),
		{Name: "open", State: "Thinking", ClaudePane: "%2", Context: -1},
	}}, RateLimits{}, false, nil, remoteT0)
	f := newFakeRemote()
	f.permitPane(t, "%1")
	hub := newRemoteHub(fleet, RemoteConfig{Enabled: true}, remoteTestToken, nil)
	hub.gather, hub.rio = f.gather, f.rio
	h := webHandler(fleet, webAllow{}, hub)

	if v := getNext(t, h, ""); v.Detail == nil || v.Detail.Session != "secret" {
		t.Fatalf("remote next must see the private session: %+v", v)
	}
	if rec := serve(h, remoteReq("GET", "/remote/fleet", "", remoteTestToken)); !strings.Contains(rec.Body.String(), `"secret"`) {
		t.Errorf("remote fleet must list it: %s", rec.Body)
	}
	if rec := serve(h, remoteReq("GET", "/api/fleet", "", "")); strings.Contains(rec.Body.String(), "secret") {
		t.Errorf("the public page must still hide it: %s", rec.Body)
	}
}

func TestRemoteNextOpenedSession(t *testing.T) {
	f := newFakeRemote()
	f.permitPane(t, "%1")
	f.idlePane(t, "%2", "Working on it.")
	_, h := remoteTestHub(t, []swSession{
		waitingSess("w", "%1", remoteT0),
		{Name: "busy", State: "Thinking", ClaudePane: "%2", Since: remoteT0, Context: -1},
	}, f)
	v := getNext(t, h, "?session=busy")
	if v.Detail == nil || v.Detail.Session != "busy" || v.Queue.Pos != 0 || v.Queue.Total != 1 {
		t.Fatalf("%+v", v)
	}
	if len(v.Detail.Options) != 0 || v.Detail.Note != noteWorking {
		t.Errorf("a working session offers nothing: %+v", v.Detail)
	}
	// The pin holds on the next plain poll.
	if v := getNext(t, h, ""); v.Detail == nil || v.Detail.Session != "busy" {
		t.Errorf("pin lost: %+v", v)
	}
}

func TestRemoteFleet(t *testing.T) {
	f := newFakeRemote()
	_, h := remoteTestHub(t, []swSession{
		{Name: "a", State: "Idle", Description: "Alpha", Topic: "tests", Context: -1},
		{Name: "b", State: "Thinking", Deferred: true, Context: -1},
	}, f)
	rec := serve(h, remoteReq("GET", "/remote/fleet", "", remoteTestToken))
	if rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	want := `{"sessions":[{"name":"a","title":"Alpha · tests","state":"Idle","waiting":true,"deferred":false},{"name":"b","title":"b","state":"Thinking","waiting":false,"deferred":true}]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	_, empty := remoteTestHub(t, nil, f)
	if got := strings.TrimSpace(serve(empty, remoteReq("GET", "/remote/fleet", "", remoteTestToken)).Body.String()); got != `{"sessions":[]}` {
		t.Errorf("empty fleet: %s", got)
	}
}

func answerBody(session, episode, option string) string {
	b, _ := json.Marshal(map[string]string{"session": session, "episode": episode, "option": option})
	return string(b)
}

func TestRemoteAnswerSent(t *testing.T) {
	f := newFakeRemote()
	f.permitPane(t, "%1")
	_, h := remoteTestHub(t, []swSession{waitingSess("a", "%1", remoteT0)}, f)
	v := getNext(t, h, "")
	rec := serve(h, remoteReq("POST", "/remote/answer", answerBody("a", v.Detail.Episode, "key:1"), remoteTestToken))
	if rec.Code != 200 || resultOf(t, rec) != "sent" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if keys, _ := f.sent(); !reflect.DeepEqual(keys, []string{"%1 1"}) {
		t.Errorf("keys %v", keys)
	}
}

func TestRemoteAnswerStaleEpisode(t *testing.T) {
	f := newFakeRemote()
	f.permitPane(t, "%1")
	_, h := remoteTestHub(t, []swSession{waitingSess("a", "%1", remoteT0)}, f)
	rec := serve(h, remoteReq("POST", "/remote/answer", answerBody("a", "0123456789abcdef", "key:1"), remoteTestToken))
	if rec.Code != 409 || resultOf(t, rec) != "changed" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if keys, nudges := f.sent(); len(keys)+len(nudges) != 0 {
		t.Errorf("sent %v %v", keys, nudges)
	}
}

func TestRemoteAnswerGone(t *testing.T) {
	f := newFakeRemote()
	_, h := remoteTestHub(t, []swSession{{Name: "nopane", State: "Idle", Context: -1}}, f)
	for _, name := range []string{"missing", "nopane"} {
		rec := serve(h, remoteReq("POST", "/remote/answer", answerBody(name, "e", "key:1"), remoteTestToken))
		if rec.Code != 404 || resultOf(t, rec) != "gone" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestRemoteAnswerBusy(t *testing.T) {
	f := newFakeRemote()
	f.permitPane(t, "%1")
	f.keyGate, f.keyIn = make(chan struct{}), make(chan struct{})
	_, h := remoteTestHub(t, []swSession{waitingSess("a", "%1", remoteT0)}, f)
	v := getNext(t, h, "")
	body := answerBody("a", v.Detail.Episode, "key:1")

	first := make(chan *httptest.ResponseRecorder)
	go func() { first <- serve(h, remoteReq("POST", "/remote/answer", body, remoteTestToken)) }()
	<-f.keyIn // the first answer is mid-send
	rec := serve(h, remoteReq("POST", "/remote/answer", body, remoteTestToken))
	if rec.Code != 409 || resultOf(t, rec) != "busy" {
		t.Errorf("second: %d %s", rec.Code, rec.Body)
	}
	close(f.keyGate)
	if r := <-first; r.Code != 200 {
		t.Errorf("first: %d %s", r.Code, r.Body)
	}
	if keys, _ := f.sent(); len(keys) != 1 {
		t.Errorf("keys %v", keys)
	}
}

// Every failure of the sender maps to a non-200 result; none reads as sent.
func TestRemoteAnswerOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(f *fakeRemote, h *remoteHub)
		option string
		code   int
		result string
	}{
		{"stuck", func(f *fakeRemote, _ *remoteHub) { delete(f.after, "%1") }, "key:1", 409, "stuck"},
		{"unverified", func(f *fakeRemote, _ *remoteHub) { f.keyErr = errors.New("tmux died") }, "key:1", 409, "unverified"},
		{"not offered", func(*fakeRemote, *remoteHub) {}, "key:9", 409, "changed"},
		{"typed", func(f *fakeRemote, _ *remoteHub) { f.nudgeOut = nudgeTyped }, "reply:0", 409, "typed"},
		{"draft", func(f *fakeRemote, _ *remoteHub) { f.nudgeOut = nudgeRefused }, "reply:0", 409, "draft"},
		{"unsafe", func(_ *fakeRemote, h *remoteHub) { h.cfg.Replies = []string{"go\x1b[2J"} }, "reply:0", 409, "unsafe"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeRemote()
			sess := waitingSess("a", "%1", remoteT0)
			if strings.HasPrefix(c.option, "reply:") {
				sess.State = "Idle"
				f.idlePane(t, "%1", "Done.")
			} else {
				f.permitPane(t, "%1")
			}
			hub, h := remoteTestHub(t, []swSession{sess}, f)
			c.setup(f, hub)
			v := getNext(t, h, "")
			rec := serve(h, remoteReq("POST", "/remote/answer", answerBody("a", v.Detail.Episode, c.option), remoteTestToken))
			if rec.Code != c.code || resultOf(t, rec) != c.result {
				t.Errorf("%d %s, want %d %s", rec.Code, rec.Body, c.code, c.result)
			}
		})
	}

	t.Run("rebuild error", func(t *testing.T) {
		f := newFakeRemote()
		f.permitPane(t, "%1")
		_, h := remoteTestHub(t, []swSession{waitingSess("a", "%1", remoteT0)}, f)
		v := getNext(t, h, "")
		f.mu.Lock()
		f.gatherErr = errors.New("capture failed")
		f.mu.Unlock()
		rec := serve(h, remoteReq("POST", "/remote/answer", answerBody("a", v.Detail.Episode, "key:1"), remoteTestToken))
		if rec.Code != 500 || resultOf(t, rec) != "error" {
			t.Errorf("%d %s", rec.Code, rec.Body)
		}
	})
}

func TestRemoteBadBodies(t *testing.T) {
	f := newFakeRemote()
	f.permitPane(t, "%1")
	_, h := remoteTestHub(t, []swSession{waitingSess("a", "%1", remoteT0)}, f)
	for _, c := range []struct {
		path, body string
		code       int
	}{
		{"/remote/answer", `{"session":"a","episode":"e","option":"key:1","extra":1}`, 400},
		{"/remote/answer", `{"session":"a",`, 400},
		{"/remote/answer", `{"session":"a","episode":"e","option":"key:1"} {}`, 400},
		{"/remote/answer", `{"session":"a","episode":"e"}`, 400},
		{"/remote/answer", `{"session":"a","episode":"e","option":"key:1","text":"rm -rf /"}`, 400},
		{"/remote/answer", `{"session":"a","episode":"e","option":"` + strings.Repeat("x", 8<<10) + `"}`, 413},
		{"/remote/skip", `{"session":"a","why":"x"}`, 400},
		{"/remote/skip", `[]`, 400},
		{"/remote/defer", `{}`, 400},
		{"/remote/defer", `nope`, 400},
	} {
		rec := serve(h, remoteReq("POST", c.path, c.body, remoteTestToken))
		if rec.Code != c.code {
			t.Errorf("%s %.60s: %d %s, want %d", c.path, c.body, rec.Code, rec.Body, c.code)
		}
	}
	if keys, nudges := f.sent(); len(keys)+len(nudges) != 0 || len(f.defers) != 0 {
		t.Errorf("a bad body must do nothing: %v %v %v", keys, nudges, f.defers)
	}
}

func TestRemoteSkipMovesOn(t *testing.T) {
	f := newFakeRemote()
	f.permitPane(t, "%1")
	f.permitPane(t, "%2")
	_, h := remoteTestHub(t, []swSession{
		waitingSess("first", "%1", remoteT0),
		waitingSess("second", "%2", remoteT0.Add(time.Minute)),
	}, f)
	if v := getNext(t, h, ""); v.Detail.Session != "first" {
		t.Fatalf("%+v", v.Detail)
	}
	if rec := serve(h, remoteReq("POST", "/remote/skip", `{"session":"first"}`, remoteTestToken)); rec.Code != 200 {
		t.Fatalf("skip %d %s", rec.Code, rec.Body)
	}
	if v := getNext(t, h, ""); v.Detail == nil || v.Detail.Session != "second" || v.Queue.Pos != 1 || v.Queue.Total != 1 {
		t.Errorf("after skip: %+v", v)
	}
}

func TestRemoteDeferUsesReason(t *testing.T) {
	f := newFakeRemote()
	_, h := remoteTestHub(t, []swSession{waitingSess("a", "%1", remoteT0)}, f)
	if rec := serve(h, remoteReq("POST", "/remote/defer", `{"session":"a"}`, remoteTestToken)); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !reflect.DeepEqual(f.defers, []string{"a|deferred from glasses"}) {
		t.Errorf("defers %v", f.defers)
	}
	if rec := serve(h, remoteReq("POST", "/remote/defer", `{"session":"zzz"}`, remoteTestToken)); rec.Code != 404 {
		t.Errorf("unknown: %d", rec.Code)
	}
	f.deferErr = errors.New("lobby gone")
	if rec := serve(h, remoteReq("POST", "/remote/defer", `{"session":"a"}`, remoteTestToken)); rec.Code != 500 {
		t.Errorf("failed defer: %d", rec.Code)
	}
}

func TestRemoteGuardStillApplies(t *testing.T) {
	f := newFakeRemote()
	_, h := remoteTestHub(t, []swSession{waitingSess("a", "%1", remoteT0)}, f)
	for _, m := range []string{"GET", "OPTIONS"} {
		r := remoteReq(m, "/remote/next", "", remoteTestToken)
		r.RemoteAddr = "8.8.8.8:4444"
		if rec := serve(h, r); rec.Code != 403 {
			t.Errorf("%s off-tailnet: %d, want 403", m, rec.Code)
		}
	}
}

// Suggestions never block /remote/next: a miss returns the detail without
// them and fills the cache in the background, once per session episode.
func TestRemoteSuggestionsFillInBackground(t *testing.T) {
	f := newFakeRemote()
	f.idlePane(t, "%1", "Tests pass. Shall I open the PR?")
	sess := swSession{Name: "a", State: "Idle", Since: remoteT0, ClaudePane: "%1", Context: -1}
	hub, h := remoteTestHub(t, []swSession{sess}, f)
	hub.cfg.Suggest = true
	gate := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	hub.suggestFn = func(last string) ([]string, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		if last != "Tests pass. Shall I open the PR?" {
			t.Errorf("last text %q", last)
		}
		<-gate
		return []string{"Open the PR"}, nil
	}

	for i := 0; i < 3; i++ {
		v := getNext(t, h, "")
		for _, o := range v.Detail.Options {
			if o.Suggested {
				t.Fatalf("suggestion before the model answered: %+v", o)
			}
		}
	}
	close(gate)
	deadline := time.Now().Add(2 * time.Second)
	var got remoteNextView
	for time.Now().Before(deadline) {
		got = getNext(t, h, "")
		if len(got.Detail.Options) > 0 && got.Detail.Options[0].ID == "suggest:0" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got.Detail.Options[0].ID != "suggest:0" || got.Detail.Options[0].Label != "Open the PR" {
		t.Fatalf("suggestion never arrived: %+v", got.Detail.Options)
	}
	mu.Lock()
	if calls != 1 {
		t.Errorf("model called %d times, want 1", calls)
	}
	mu.Unlock()

	// Answering a suggestion uses the cache: same episode, no model call.
	rec := serve(h, remoteReq("POST", "/remote/answer", answerBody("a", got.Detail.Episode, "suggest:0"), remoteTestToken))
	if rec.Code != 200 || resultOf(t, rec) != "sent" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, nudges := f.sent(); !reflect.DeepEqual(nudges, []string{"%1 Open the PR"}) {
		t.Errorf("nudges %v", nudges)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("answer path called the model: %d calls", calls)
	}
}

func TestRemoteSuggestionsOffDoesNotCallModel(t *testing.T) {
	f := newFakeRemote()
	f.idlePane(t, "%1", "Done.")
	hub, h := remoteTestHub(t, []swSession{{Name: "a", State: "Idle", Since: remoteT0, ClaudePane: "%1", Context: -1}}, f)
	hub.suggestFn = func(string) ([]string, error) {
		t.Error("model called with remote.suggest off")
		return nil, nil
	}
	v := getNext(t, h, "")
	rec := serve(h, remoteReq("POST", "/remote/answer", answerBody("a", v.Detail.Episode, "reply:0"), remoteTestToken))
	if rec.Code != 200 {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if _, nudges := f.sent(); !reflect.DeepEqual(nudges, []string{"%1 Continue"}) {
		t.Errorf("nudges %v", nudges)
	}
}

func TestRemoteDeferMsgSetsDefer(t *testing.T) {
	var got []string
	old := deferTmuxRun
	deferTmuxRun = func(args []string) { got = append([]string(nil), args...) }
	defer func() { deferTmuxRun = old }()

	done := make(chan error, 1)
	m := newSwModel("%0")
	_, cmd := m.Update(remoteDeferMsg{sess: swSession{Name: "a", HeadPane: "%7"}, reason: remoteDeferReason, done: done})
	if cmd == nil {
		t.Fatal("no cmd")
	}
	cmd()
	if err := <-done; err != nil {
		t.Errorf("done: %v", err)
	}
	want := deferArgs("%7", true, "deferred from glasses")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args %v, want %v", got, want)
	}
}

func TestRemoteDeferViaSend(t *testing.T) {
	fn := remoteDeferVia(func(msg tea.Msg) {
		m := msg.(remoteDeferMsg)
		if m.sess.Name != "a" || m.reason != "r" {
			t.Errorf("%+v", m)
		}
		m.done <- nil
	}, time.Second)
	if err := fn(swSession{Name: "a"}, "r"); err != nil {
		t.Errorf("%v", err)
	}
	slow := remoteDeferVia(func(tea.Msg) {}, 10*time.Millisecond)
	if err := slow(swSession{Name: "a"}, "r"); err == nil {
		t.Error("an unanswered defer must fail")
	}
}
