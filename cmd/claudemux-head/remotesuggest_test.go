package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// fakeMessagesAPI is an httptest Messages API: every call gets status and
// body, and the request bodies are kept.
type fakeMessagesAPI struct {
	srv    *httptest.Server
	mu     sync.Mutex
	status int
	body   string
	reqs   []map[string]any
}

func newFakeMessagesAPI(t *testing.T, status int, body string) *fakeMessagesAPI {
	t.Helper()
	f := &fakeMessagesAPI{status: status, body: body}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		f.mu.Lock()
		f.reqs = append(f.reqs, req)
		status, body := f.status, f.body
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// Retry at once: the SDK retries 429 and 5xx (WithMaxRetries(1)).
		w.Header().Set("retry-after-ms", "1")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMessagesAPI) summarizer() *Summarizer {
	cfg := defaultConfig().Summary
	cfg.Enabled = true
	return newSummarizer(cfg, option.WithBaseURL(f.srv.URL), option.WithAPIKey("x"))
}

func (f *fakeMessagesAPI) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

// userText is the text of the first user message of request i.
func (f *fakeMessagesAPI) userText(t *testing.T, i int) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.reqs) {
		t.Fatalf("no request %d (have %d)", i, len(f.reqs))
	}
	msgs, _ := f.reqs[i]["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("request has no messages: %v", f.reqs[i])
	}
	msg, _ := msgs[0].(map[string]any)
	content, _ := msg["content"].([]any)
	var b strings.Builder
	for _, c := range content {
		if m, ok := c.(map[string]any); ok {
			if s, ok := m["text"].(string); ok {
				b.WriteString(s)
			}
		}
	}
	return b.String()
}

// suggestResponse is a Messages API answer calling toolName with input.
func suggestResponse(stop, toolName string, input json.RawMessage) string {
	body, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant",
		"model": "claude-haiku-4-5", "stop_reason": stop,
		"content": []any{map[string]any{
			"type": "tool_use", "id": "toolu_1", "name": toolName, "input": input,
		}},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	return string(body)
}

func suggestReplies(replies ...remoteSuggestion) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"replies": replies})
	return b
}

func apiErrorBody(kind string) string {
	return fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":"nope"}}`, kind)
}

func waitingSession(state string) swSession {
	return swSession{Name: "a", State: state, Since: rt0, ClaudePane: "%1", Context: -1, Topic: "db choice"}
}

func TestSuggestRepliesParsesAndCleans(t *testing.T) {
	api := newFakeMessagesAPI(t, 200, suggestResponse("tool_use", suggestToolName, suggestReplies(
		remoteSuggestion{Label: "Run tests", Text: "run the tests."},
		remoteSuggestion{Label: "dup", Text: "Run the tests"},
		remoteSuggestion{Label: "q", Text: "should I deploy?"},
		remoteSuggestion{Label: "fixed", Text: "continue"},
		remoteSuggestion{Label: "key", Key: "1"},
		remoteSuggestion{Label: "ship", Text: "ship it"},
	)))
	sess := waitingSession("Idle")
	sess.Prompt = "  add the\n  migration  "
	got, err := api.summarizer().SuggestReplies(context.Background(), sess, "● Done.\n", []string{"continue"})
	if err != nil {
		t.Fatalf("SuggestReplies: %v", err)
	}
	want := []remoteSuggestion{{Label: "Run tests", Text: "run the tests"}, {Label: "ship", Text: "ship it"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	text := api.userText(t, 0)
	if strings.Contains(text, "DIALOG") {
		t.Errorf("a prompt state was sent as a dialog:\n%s", text)
	}
	for _, s := range []string{"Session topic: db choice", "Developer's last prompt: add the migration\n", "Fixed replies (do not repeat): continue", "● Done."} {
		if !strings.Contains(text, s) {
			t.Errorf("request lacks %q:\n%s", s, text)
		}
	}
}

func TestSuggestRepliesBadResponses(t *testing.T) {
	ok := suggestReplies(remoteSuggestion{Label: "x", Text: "do x"})
	textOnly, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant",
		"model": "claude-haiku-4-5", "stop_reason": "end_turn",
		"content": []any{map[string]any{"type": "text", "text": "no tool call here"}},
		"usage":   map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	for _, tc := range []struct {
		what, body, reason string
	}{
		{"cut off at max_tokens", suggestResponse("max_tokens", suggestToolName, ok), suggestErrBadResp},
		{"refused", suggestResponse("refusal", suggestToolName, ok), suggestErrBadResp},
		{"no tool_use", string(textOnly), suggestErrBadResp},
		{"another tool", suggestResponse("tool_use", "summarize", ok), suggestErrBadResp},
		{"tool input of the wrong shape", suggestResponse("tool_use", suggestToolName, json.RawMessage(`{"replies":"run the tests"}`)), suggestErrBadResp},
		{"every reply unusable", suggestResponse("tool_use", suggestToolName, suggestReplies(
			remoteSuggestion{Label: "q", Text: "deploy?"},
			remoteSuggestion{Label: "fixed", Text: "continue"},
			remoteSuggestion{Label: "esc", Text: "a\x1bb"},
		)), suggestErrNoReplies},
	} {
		api := newFakeMessagesAPI(t, 200, tc.body)
		items, err := api.summarizer().SuggestReplies(context.Background(), waitingSession("Idle"), "screen", []string{"continue"})
		if err == nil {
			t.Errorf("%s: got %+v, want an error", tc.what, items)
			continue
		}
		if got := suggestReason(err); got != tc.reason {
			t.Errorf("%s: reason %q (%v), want %q", tc.what, got, err, tc.reason)
		}
	}
}

func TestSuggestRepliesEmptyScreenMakesNoCall(t *testing.T) {
	api := newFakeMessagesAPI(t, 200, suggestResponse("tool_use", suggestToolName, suggestReplies(remoteSuggestion{Label: "x", Text: "x"})))
	for _, screen := range []string{"", " \n\t\n"} {
		_, err := api.summarizer().SuggestReplies(context.Background(), waitingSession("Idle"), screen, nil)
		if got := suggestReason(err); err == nil || got != suggestErrNoScreen {
			t.Errorf("screen %q: err %v (reason %q), want %q", screen, err, got, suggestErrNoScreen)
		}
	}
	if n := api.calls(); n != 0 {
		t.Fatalf("an empty screen made %d API calls", n)
	}
}

func TestSuggestRepliesDialogKeysOnly(t *testing.T) {
	api := newFakeMessagesAPI(t, 200, suggestResponse("tool_use", suggestToolName, suggestReplies(
		remoteSuggestion{Label: "postgres", Key: "1"},
		remoteSuggestion{Label: "typed", Text: "use sqlite"},
		remoteSuggestion{Label: "sqlite", Key: "2"},
	)))
	got, err := api.summarizer().SuggestReplies(context.Background(), waitingSession("Asking"), dialogScreen(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []remoteSuggestion{{Label: "postgres", Key: "1"}, {Label: "sqlite", Key: "2"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if text := api.userText(t, 0); !strings.Contains(text, "DIALOG") {
		t.Fatalf("dialog request does not say DIALOG:\n%s", text)
	}
}

func TestSuggestRepliesSendsOnlyTheScreenTail(t *testing.T) {
	api := newFakeMessagesAPI(t, 200, suggestResponse("tool_use", suggestToolName, suggestReplies(remoteSuggestion{Label: "x", Text: "x"})))
	tail := strings.Repeat("é", suggestScreenRunes-len("TAIL-END")) + "TAIL-END"
	screen := "HEAD-START" + strings.Repeat("y", 500) + tail
	if _, err := api.summarizer().SuggestReplies(context.Background(), waitingSession("Idle"), screen, nil); err != nil {
		t.Fatal(err)
	}
	text := api.userText(t, 0)
	if strings.Contains(text, "HEAD-START") || strings.Contains(text, "yy") {
		t.Error("the screen's head was sent")
	}
	if !strings.HasSuffix(text, tail) {
		t.Errorf("the last %d runes of the screen were not sent whole", suggestScreenRunes)
	}
}

func TestSuggestRepliesAPIErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		kind   string
		reason string
	}{
		{401, "authentication_error", suggestErrAuth},
		{403, "permission_error", suggestErrAuth},
		{429, "rate_limit_error", suggestErrRateLimit},
		{404, "not_found_error", "api error 404: not_found_error: nope"},
		{400, "invalid_request_error", "api error 400: invalid_request_error: nope"},
	} {
		api := newFakeMessagesAPI(t, tc.status, apiErrorBody(tc.kind))
		_, err := api.summarizer().SuggestReplies(context.Background(), waitingSession("Idle"), "screen", nil)
		if got := suggestReason(err); err == nil || got != tc.reason {
			t.Errorf("%d: err %v, reason %q, want %q", tc.status, err, got, tc.reason)
		}
	}
}

// An "api error <status>" reason carries the API's error type and message,
// sanitized, so the page can tell low credit from a bad model; the
// "api error <status>" prefix the page and suggestBackoff match on stays.
func TestSuggestRepliesAPIErrorDetail(t *testing.T) {
	long := strings.Repeat("x", 300)
	for _, tc := range []struct {
		what   string
		status int
		body   string
		want   string
	}{
		{"credit", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low"}}`,
			"api error 400: invalid_request_error: Your credit balance is too low"},
		{"whitespace collapsed", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"  too\n\tlow  "}}`,
			"api error 400: invalid_request_error: too low"},
		{"control rune", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"bad\u0007bell"}}`,
			"api error 400"},
		{"control rune in type", 400, `{"type":"error","error":{"type":"x\u001b[31m","message":"m"}}`,
			"api error 400"},
		{"no error object", 400, `{"type":"error"}`, "api error 400"},
		{"not json", 400, `oops`, "api error 400"},
		{"message only", 400, `{"error":{"message":"only"}}`, "api error 400: only"},
		{"long", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"` + long + `"}}`,
			"api error 400: " + truncateRunes("invalid_request_error: "+long, suggestAPIDetailRunes)},
	} {
		api := newFakeMessagesAPI(t, tc.status, tc.body)
		_, err := api.summarizer().SuggestReplies(context.Background(), waitingSession("Idle"), "screen", nil)
		got := suggestReason(err)
		if err == nil || got != tc.want {
			t.Errorf("%s: err %v, reason %q, want %q", tc.what, err, got, tc.want)
		}
		if !strings.HasPrefix(got, "api error 400") {
			t.Errorf("%s: reason %q lost the api error 400 prefix", tc.what, got)
		}
	}

	// The long reason is clipped: the detail is at most suggestAPIDetailRunes.
	api := newFakeMessagesAPI(t, 400, `{"type":"error","error":{"type":"invalid_request_error","message":"`+long+`"}}`)
	_, err := api.summarizer().SuggestReplies(context.Background(), waitingSession("Idle"), "screen", nil)
	reason := suggestReason(err)
	if n := len([]rune(strings.TrimPrefix(reason, "api error 400: "))); n != suggestAPIDetailRunes {
		t.Errorf("long detail is %d runes, want %d: %q", n, suggestAPIDetailRunes, reason)
	}
	if !strings.HasSuffix(reason, "…") {
		t.Errorf("long reason %q has no ellipsis", reason)
	}
	if got := suggestBackoff(reason); got != suggestConfigRetry {
		t.Errorf("suggestBackoff(long 400) = %v, want suggestConfigRetry", got)
	}

	// A 404 with a body keeps its "api error 404" prefix.
	api = newFakeMessagesAPI(t, 404, `{"type":"error","error":{"type":"not_found_error","message":"model: claude-nope"}}`)
	_, err = api.summarizer().SuggestReplies(context.Background(), waitingSession("Idle"), "screen", nil)
	if got := suggestReason(err); !strings.HasPrefix(got, "api error 404") || got != "api error 404: not_found_error: model: claude-nope" {
		t.Errorf("404 reason %q", got)
	}
}

// timeoutErr is a net.Error that timed out.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestSuggestReason(t *testing.T) {
	for _, tc := range []struct {
		what string
		err  error
		want string
	}{
		{"401", &anthropic.Error{StatusCode: 401}, suggestErrAuth},
		{"403", &anthropic.Error{StatusCode: 403}, suggestErrAuth},
		{"429", &anthropic.Error{StatusCode: 429}, suggestErrRateLimit},
		{"408", &anthropic.Error{StatusCode: 408}, suggestErrTimeout},
		{"504", &anthropic.Error{StatusCode: 504}, suggestErrTimeout},
		{"400", &anthropic.Error{StatusCode: 400}, "api error 400"},
		{"404", &anthropic.Error{StatusCode: 404}, "api error 404"},
		{"500", &anthropic.Error{StatusCode: 500}, "api error 500"},
		{"wrapped 404", fmt.Errorf("call: %w", &anthropic.Error{StatusCode: 404}), "api error 404"},
		{"wrapped 401", fmt.Errorf("call: %w", &anthropic.Error{StatusCode: 401}), suggestErrAuth},
		{"deadline", context.DeadlineExceeded, suggestErrTimeout},
		{"wrapped deadline", fmt.Errorf("waiting for a call slot: %w", context.DeadlineExceeded), suggestErrTimeout},
		{"connection refused", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, suggestErrNetwork},
		{"net timeout", &net.OpError{Op: "read", Net: "tcp", Err: timeoutErr{}}, suggestErrTimeout},
		{"suggestError passthrough", &suggestError{suggestErrNoReplies, "x"}, suggestErrNoReplies},
		{"wrapped suggestError", fmt.Errorf("x: %w", &suggestError{suggestErrBadResp, "y"}), suggestErrBadResp},
		{"canceled", context.Canceled, suggestErrInternal},
		{"wrapped canceled", fmt.Errorf("x: %w", context.Canceled), suggestErrInternal},
		{"anything else", errors.New("boom"), suggestErrInternal},
	} {
		if got := suggestReason(tc.err); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.what, got, tc.want)
		}
	}
}

// A 4xx other than auth, rate limit or timeout is a configuration problem
// (wrong model, bad request) that won't fix itself: it waits out
// suggestConfigRetry. A 5xx is the API's own trouble: the ordinary back-off.
func TestSuggestBackoffAPIErrors(t *testing.T) {
	if suggestConfigRetry != 5*time.Minute {
		t.Errorf("suggestConfigRetry = %v, want 5m", suggestConfigRetry)
	}
	for _, tc := range []struct {
		reason string
		want   time.Duration
	}{
		{"api error 404", suggestConfigRetry},
		{suggestReason(&anthropic.Error{StatusCode: 404}), suggestConfigRetry},
		{"api error 500", suggestRetryAfter},
		{suggestReason(&anthropic.Error{StatusCode: 500}), suggestRetryAfter},
		{suggestErrAuth, suggestAuthRetry},
		{suggestErrNoReplies, suggestNoRepliesRetry},
		{suggestErrBadResp, suggestNoRepliesRetry},
		{suggestErrNetwork, suggestRetryAfter},
	} {
		if got := suggestBackoff(tc.reason); got != tc.want {
			t.Errorf("suggestBackoff(%q) = %v, want %v", tc.reason, got, tc.want)
		}
	}
}

// A key press puts the session's entry on hold: the screen is about to
// change, so the chips go back to pending and no call starts until
// suggestKeyHold has passed.
func TestRemoteSuggestionsKeyPressedHold(t *testing.T) {
	var nilC *remoteSuggestions
	nilC.keyPressed("a") // must not panic

	clock := &fakeClock{t: rt0}
	s := idleSession()
	gen := &fakeSuggester{out: []remoteSuggestion{{Label: "x", Text: "do x"}}}
	c := newRemoteSuggestions(gen)
	c.now = clock.now
	c.keyPressed("ghost") // no entry: a no-op
	if n := suggestEntryCount(c); n != 0 {
		t.Fatalf("keyPressed on an unknown session made %d entries", n)
	}

	c.ensure(s, nil, func() string { return "screen" })
	waitFor(t, "the draft", func() bool { return c.get(s).Status == suggestStatusReady })

	c.keyPressed(s.Name)
	if st := c.get(s); st.Status != suggestStatusPending || len(st.Items) != 0 {
		t.Fatalf("after keyPressed: %+v, want pending with no items", st)
	}
	if c.needsCall(s) {
		t.Error("needsCall during the key hold")
	}
	c.ensure(s, nil, func() string { return "screen" })
	if n := gen.count(); n != 1 {
		t.Fatalf("ensure during the key hold made a call: %d calls", n)
	}
	clock.advance(suggestKeyHold - time.Millisecond)
	if c.needsCall(s) {
		t.Error("needsCall just before the key hold ends")
	}
	clock.advance(time.Millisecond)
	if !c.needsCall(s) {
		t.Fatal("needsCall false once the key hold has passed")
	}
	c.ensure(s, nil, func() string { return "screen" })
	waitFor(t, "the redraft", func() bool { return c.get(s).Status == suggestStatusReady })
	if n := gen.count(); n != 2 {
		t.Fatalf("calls = %d, want 2", n)
	}
	if st := c.get(s); !reflect.DeepEqual(st.Items, gen.out) {
		t.Errorf("items = %+v, want %+v", st.Items, gen.out)
	}
}

// A call already running when a key is pressed answers for the old screen:
// finishCall discards it and the hold stays.
func TestRemoteSuggestionsKeyPressedDiscardsInFlight(t *testing.T) {
	clock := &fakeClock{t: rt0}
	s := idleSession()
	gen := &gatedSuggester{gates: map[time.Time]chan struct{}{}}
	c := newRemoteSuggestions(gen)
	c.now = clock.now
	c.ensure(s, nil, func() string { return "screen" })
	waitFor(t, "the call to start", func() bool { return gen.begun() == 1 })
	c.keyPressed(s.Name)
	close(gen.gate(s.Since))
	// The slot is given back only after finishCall has run.
	waitFor(t, "the call to finish", func() bool { return gen.finished() == 1 && len(c.sem) == 0 })
	if st := c.get(s); st.Status != suggestStatusPending || len(st.Items) != 0 {
		t.Fatalf("the in-flight result survived the key press: %+v", st)
	}
	if c.needsCall(s) {
		t.Error("needsCall during the key hold after the stale result")
	}
	clock.advance(suggestKeyHold)
	if !c.needsCall(s) {
		t.Error("needsCall false once the key hold has passed")
	}
}

// seqSuggester holds each call until its own release, in start order, and
// answers with whatever that release carries. Unlike gatedSuggester it can
// tell apart two calls of the same episode.
type seqSuggester struct {
	mu       sync.Mutex
	releases []chan []remoteSuggestion
	started  int
	done     int
}

// release returns call i's (0-based, start order) release channel.
func (g *seqSuggester) release(i int) chan []remoteSuggestion {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.releaseLocked(i)
}

func (g *seqSuggester) releaseLocked(i int) chan []remoteSuggestion {
	for len(g.releases) <= i {
		g.releases = append(g.releases, make(chan []remoteSuggestion, 1))
	}
	return g.releases[i]
}

func (g *seqSuggester) SuggestReplies(context.Context, swSession, string, []string) ([]remoteSuggestion, error) {
	g.mu.Lock()
	r := g.releaseLocked(g.started)
	g.started++
	g.mu.Unlock()
	items := <-r
	g.mu.Lock()
	g.done++
	g.mu.Unlock()
	return items, nil
}

func (g *seqSuggester) counts() (started, done int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.started, g.done
}

// A call superseded by a later call of the same episode is discarded even
// once the hold is gone: call A starts, a key is pressed, the hold passes and
// call B starts; A answering afterwards must not fill B's entry.
func TestRemoteSuggestionsSupersededCallDiscarded(t *testing.T) {
	clock := &fakeClock{t: rt0}
	s := idleSession()
	gen := &seqSuggester{}
	c := newRemoteSuggestions(gen)
	c.now = clock.now

	c.ensure(s, nil, func() string { return "screen A" })
	waitFor(t, "call A to start", func() bool { n, _ := gen.counts(); return n == 1 })
	c.keyPressed(s.Name)
	clock.advance(suggestKeyHold)
	c.ensure(s, nil, func() string { return "screen B" })
	waitFor(t, "call B to start", func() bool { n, _ := gen.counts(); return n == 2 })
	time.Sleep(10 * time.Millisecond)
	if n, _ := gen.counts(); n != 2 {
		t.Fatalf("calls started = %d, want 2", n)
	}

	x := []remoteSuggestion{{Label: "x", Text: "answer for A"}}
	y := []remoteSuggestion{{Label: "y", Text: "answer for B"}}
	gen.release(0) <- x
	// A has returned and given its slot back (B still holds one), so
	// finishCall has run for A.
	waitFor(t, "call A to finish", func() bool { _, d := gen.counts(); return d == 1 && len(c.sem) == 1 })
	if st := c.get(s); st.Status != suggestStatusPending || len(st.Items) != 0 {
		t.Fatalf("superseded call A filled the entry: %+v", st)
	}

	gen.release(1) <- y
	waitFor(t, "call B's draft", func() bool { return c.get(s).Status == suggestStatusReady })
	if st := c.get(s); !reflect.DeepEqual(st.Items, y) {
		t.Errorf("items = %+v, want B's %+v", st.Items, y)
	}
}

// A key press doesn't cut short a configuration or rate-limit back-off: an
// entry in error "auth", "rate limit" or "api error 4xx" stays in error, and
// no call is due until its back-off passes.
func TestRemoteSuggestionsKeyPressedKeepsConfigBackoff(t *testing.T) {
	for _, tc := range []struct {
		reason  string
		backoff time.Duration
	}{
		{suggestErrAuth, suggestAuthRetry},
		{"api error 404", suggestConfigRetry},
		{suggestErrRateLimit, suggestRetryAfter},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			clock := &fakeClock{t: rt0}
			s := idleSession()
			gen := &fakeSuggester{err: &suggestError{tc.reason, "nope"}}
			c := newRemoteSuggestions(gen)
			c.now = clock.now
			c.ensure(s, nil, func() string { return "screen" })
			waitFor(t, "the failed call", func() bool { return c.get(s).Status == suggestStatusError })
			if st := c.get(s); st.Error != tc.reason {
				t.Fatalf("reason = %q, want %q", st.Error, tc.reason)
			}

			c.keyPressed(s.Name)
			if st := c.get(s); st.Status != suggestStatusError || st.Error != tc.reason {
				t.Fatalf("after keyPressed: %+v, want error %q", st, tc.reason)
			}
			clock.advance(suggestKeyHold + time.Second)
			if c.needsCall(s) {
				t.Error("needsCall once the key hold passed, inside the back-off")
			}
			clock.advance(tc.backoff - suggestKeyHold - 2*time.Second)
			if c.needsCall(s) {
				t.Error("needsCall just before the back-off ends")
			}
			c.ensure(s, nil, func() string { return "screen" })
			if n := gen.count(); n != 1 {
				t.Fatalf("calls = %d inside the back-off, want 1", n)
			}
			clock.advance(time.Second)
			if !c.needsCall(s) {
				t.Error("needsCall false once the back-off passed")
			}
		})
	}
}

// A key press ends an ordinary back-off: the screen is about to change, so an
// entry in error "no usable replies" or "api error 500" goes pending under
// the key hold, and a call is due once the hold passes.
func TestRemoteSuggestionsKeyPressedEndsOrdinaryBackoff(t *testing.T) {
	for _, reason := range []string{suggestErrNoReplies, "api error 500"} {
		t.Run(reason, func(t *testing.T) {
			clock := &fakeClock{t: rt0}
			s := idleSession()
			gen := &fakeSuggester{err: &suggestError{reason, "nope"}}
			c := newRemoteSuggestions(gen)
			c.now = clock.now
			c.ensure(s, nil, func() string { return "screen" })
			waitFor(t, "the failed call", func() bool { return c.get(s).Status == suggestStatusError })
			if st := c.get(s); st.Error != reason {
				t.Fatalf("reason = %q, want %q", st.Error, reason)
			}

			c.keyPressed(s.Name)
			if st := c.get(s); st.Status != suggestStatusPending || len(st.Items) != 0 || st.Error != "" {
				t.Fatalf("after keyPressed: %+v, want pending with no items", st)
			}
			if c.needsCall(s) {
				t.Error("needsCall during the key hold")
			}
			clock.advance(suggestKeyHold - time.Millisecond)
			if c.needsCall(s) {
				t.Error("needsCall just before the key hold ends")
			}
			clock.advance(time.Millisecond)
			if !c.needsCall(s) {
				t.Error("needsCall false once the key hold passed")
			}
		})
	}
}

// A pending entry with a call in flight needs no second call, however long
// the call runs: only a failed entry's back-off expires.
func TestRemoteSuggestionsPendingNeedsNoCall(t *testing.T) {
	clock := &fakeClock{t: rt0}
	s := idleSession()
	gate := make(chan struct{})
	gen := &gatedSuggester{gates: map[time.Time]chan struct{}{s.Since: gate}}
	c := newRemoteSuggestions(gen)
	c.now = clock.now
	defer close(gate)
	c.ensure(s, nil, func() string { return "screen" })
	waitFor(t, "the call to start", func() bool { return gen.begun() == 1 })
	clock.advance(31 * time.Second)
	if c.needsCall(s) {
		t.Error("needsCall for a pending entry with a call in flight")
	}
	c.ensure(s, nil, func() string { return "screen" })
	time.Sleep(10 * time.Millisecond)
	if n := gen.begun(); n != 1 {
		t.Errorf("calls started = %d, want 1", n)
	}
}

// An auth failure won't fix itself in seconds: it waits out suggestAuthRetry,
// not suggestRetryAfter.
func TestRemoteSuggestionsAuthBackoff(t *testing.T) {
	clock := &fakeClock{t: rt0}
	s := idleSession()
	gen := &fakeSuggester{err: &suggestError{suggestErrAuth, "401"}}
	c := newRemoteSuggestions(gen)
	c.now = clock.now
	c.ensure(s, nil, func() string { return "screen" })
	waitFor(t, "the failed call", func() bool { return c.get(s).Status == suggestStatusError })
	if st := c.get(s); st.Error != suggestErrAuth {
		t.Fatalf("reason = %q, want %q", st.Error, suggestErrAuth)
	}
	clock.advance(suggestRetryAfter + time.Second)
	if c.needsCall(s) {
		t.Error("needsCall past the ordinary back-off for an auth failure")
	}
	c.ensure(s, nil, func() string { return "screen" })
	if st := c.get(s).Status; st != suggestStatusError || gen.count() != 1 {
		t.Fatalf("retried an auth failure after %v: status %q, calls %d", suggestRetryAfter+time.Second, st, gen.count())
	}
	clock.advance(suggestAuthRetry - suggestRetryAfter)
	if !c.needsCall(s) {
		t.Error("needsCall false past suggestAuthRetry")
	}
	c.ensure(s, nil, func() string { return "screen" })
	waitFor(t, "the retry", func() bool { return gen.count() == 2 && c.get(s).Status == suggestStatusError })
}

func TestRemoteSuggestionsNeedsCall(t *testing.T) {
	s := idleSession()
	if (&remoteSuggestions{}).needsCall(s) {
		t.Error("needsCall with no suggester")
	}
	gate := make(chan struct{})
	gen := &gatedSuggester{gates: map[time.Time]chan struct{}{s.Since: gate}}
	c := newRemoteSuggestions(gen)
	busy := s
	busy.State = "Thinking"
	if c.needsCall(busy) {
		t.Error("needsCall for a session that isn't waiting")
	}
	if !c.needsCall(s) {
		t.Fatal("needsCall false with no entry")
	}
	c.ensure(s, nil, func() string { return "screen" })
	if c.needsCall(s) {
		t.Error("needsCall while a call is pending")
	}
	close(gate)
	waitFor(t, "the draft", func() bool { return c.get(s).Status == suggestStatusReady })
	if c.needsCall(s) {
		t.Error("needsCall for a ready episode")
	}
	next := s
	next.Since = s.Since.Add(time.Minute)
	if !c.needsCall(next) {
		t.Error("needsCall false for a new episode")
	}
}

// At most suggestMaxInFlight calls run at once: a third session's entry is
// pending with no call started until one of the running two finishes.
func TestRemoteSuggestionsInFlightCap(t *testing.T) {
	if suggestMaxInFlight != 2 {
		t.Fatalf("suggestMaxInFlight = %d; this test is written for 2", suggestMaxInFlight)
	}
	gen := &gatedSuggester{gates: map[time.Time]chan struct{}{}}
	c := newRemoteSuggestions(gen)
	var sess []swSession
	for i, name := range []string{"a", "b", "c"} {
		s := swSession{Name: name, State: "Idle", Since: rt0.Add(time.Duration(i) * time.Minute), ClaudePane: "%1", Context: -1}
		sess = append(sess, s)
	}
	c.ensure(sess[0], nil, func() string { return "screen" })
	c.ensure(sess[1], nil, func() string { return "screen" })
	waitFor(t, "two calls in flight", func() bool { return gen.begun() == 2 })
	c.ensure(sess[2], nil, func() string { return "screen" })
	if st := c.get(sess[2]).Status; st != suggestStatusPending {
		t.Fatalf("third session status = %q, want pending", st)
	}
	// The third goroutine is parked on the semaphore, not in the suggester.
	time.Sleep(20 * time.Millisecond)
	if n := gen.begun(); n != 2 {
		t.Fatalf("calls started = %d with the cap full, want 2", n)
	}
	close(gen.gate(sess[0].Since))
	waitFor(t, "the first draft", func() bool { return c.get(sess[0]).Status == suggestStatusReady })
	waitFor(t, "the third call to start", func() bool { return gen.begun() == 3 })
	close(gen.gate(sess[2].Since))
	waitFor(t, "the third draft", func() bool { return c.get(sess[2]).Status == suggestStatusReady })
	if st := c.get(sess[1]).Status; st != suggestStatusPending {
		t.Errorf("second session status = %q, want still pending", st)
	}
	close(gen.gate(sess[1].Since))
	waitFor(t, "the second draft", func() bool { return c.get(sess[1]).Status == suggestStatusReady })
}

type panicSuggester struct{}

func (panicSuggester) SuggestReplies(context.Context, swSession, string, []string) ([]remoteSuggestion, error) {
	panic("kaboom")
}

func TestRemoteSuggestionsPanicIsAnError(t *testing.T) {
	c := newRemoteSuggestions(panicSuggester{})
	s := idleSession()
	c.ensure(s, nil, func() string { return "screen" })
	waitFor(t, "the panicked call", func() bool { return c.get(s).Status == suggestStatusError })
	if st := c.get(s); st.Error != suggestErrInternal {
		t.Fatalf("reason = %q, want %q", st.Error, suggestErrInternal)
	}
	// The slot was given back.
	waitFor(t, "the slot", func() bool { return len(c.sem) == 0 })
}

func TestRemoteNextInQueue(t *testing.T) {
	q := []swSession{
		{Name: "a", ClaudePane: "%1"},
		{Name: "b"}, // no pane
		{Name: "c", ClaudePane: "%3"},
		{Name: "d", ClaudePane: "%4"},
	}
	for _, tc := range []struct {
		queue []swSession
		name  string
		want  string // "" = none
	}{
		{q, "a", "c"},         // skips b, which has no pane
		{q, "c", "d"},         // the next one
		{q, "d", ""},          // the end of the queue: no wrap-around
		{q, "ghost", "a"},     // not queued: the head
		{q[1:2], "ghost", ""}, // nothing with a pane
		{nil, "a", ""},
	} {
		got, ok := remoteNextInQueue(tc.queue, tc.name)
		if tc.want == "" {
			if ok {
				t.Errorf("after %q: got %q, want none", tc.name, got.Name)
			}
			continue
		}
		if !ok || got.Name != tc.want {
			t.Errorf("after %q: got %q (%v), want %q", tc.name, got.Name, ok, tc.want)
		}
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
		{Label: "seven", Text: "seven"},
	}
	got, drops := cleanSuggestionsCounted(in, false, []string{"Continue"})
	want := []remoteSuggestion{
		{Label: "Run tests", Text: "run the tests"},
		{Label: "ship it", Text: "ship it"},
		{Label: "five", Text: "five"},
		{Label: "six", Text: "six"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prompt:\n got %+v\nwant %+v", got, want)
	}
	if len(got) != suggestMax {
		t.Errorf("kept %d, want suggestMax (%d)", len(got), suggestMax)
	}
	if n := drops[suggestDropOverMax]; n != 1 {
		t.Errorf("drops[%q] = %d, want 1 (drops: %v)", suggestDropOverMax, n, drops)
	}

	// Placeholder text (see placeholderLine) is dropped, not sent.
	ph, phDrops := cleanSuggestionsCounted([]remoteSuggestion{
		{Label: "na", Text: "n/a"},
		{Label: "angle", Text: "<next step>"},
		{Label: "none", Text: "None"},
		{Label: "ok", Text: "run the tests"},
	}, false, nil)
	if want := []remoteSuggestion{{Label: "ok", Text: "run the tests"}}; !reflect.DeepEqual(ph, want) {
		t.Fatalf("placeholders:\n got %+v\nwant %+v", ph, want)
	}
	if n := phDrops[suggestDropPlaceholder]; n != 3 {
		t.Errorf("drops[%q] = %d, want 3 (drops: %v)", suggestDropPlaceholder, n, phDrops)
	}

	// A label is clipped to suggestLabelRunes; the text it sends is not.
	longLabel := strings.Repeat("é", suggestLabelRunes+5)
	for _, dlg := range []bool{false, true} {
		in := remoteSuggestion{Label: longLabel, Text: "ship it"}
		if dlg {
			in = remoteSuggestion{Label: longLabel, Key: "1"}
		}
		got := cleanSuggestions([]remoteSuggestion{in}, dlg, nil)
		if len(got) != 1 {
			t.Fatalf("dialog=%v long label: kept %d, want 1", dlg, len(got))
		}
		if n := len([]rune(got[0].Label)); n != suggestLabelRunes {
			t.Errorf("dialog=%v long label: %d runes, want %d", dlg, n, suggestLabelRunes)
		}
		if want := truncateRunes(longLabel, suggestLabelRunes); got[0].Label != want {
			t.Errorf("dialog=%v label = %q, want %q", dlg, got[0].Label, want)
		}
		if !dlg && got[0].Text != "ship it" {
			t.Errorf("text = %q, want it unclipped", got[0].Text)
		}
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

// Fixed replies are compared in the same normal form as drafted text, so a
// fixed "continue." still hides a drafted "Continue".
func TestCleanSuggestionsNormalizesFixedReplies(t *testing.T) {
	got := cleanSuggestions([]remoteSuggestion{
		{Label: "c", Text: "continue"},
		{Label: "C", Text: "Continue."},
		{Label: "spaced", Text: "commit   and push"},
		{Label: "ok", Text: "run the tests"},
	}, false, []string{"continue.", "  Commit and  push. "})
	if want := []remoteSuggestion{{Label: "ok", Text: "run the tests"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// "No usable replies" means the model looked at this screen and had nothing:
// the screen of a parked session doesn't change, so it waits out the long
// suggestNoRepliesRetry, not suggestRetryAfter.
func TestRemoteSuggestionsNoRepliesBackoff(t *testing.T) {
	if suggestNoRepliesRetry <= suggestRetryAfter+time.Second {
		t.Fatalf("suggestNoRepliesRetry = %v, want well past suggestRetryAfter (%v)", suggestNoRepliesRetry, suggestRetryAfter)
	}
	clock := &fakeClock{t: rt0}
	s := idleSession()
	gen := &fakeSuggester{err: &suggestError{suggestErrNoReplies, "all dropped"}}
	c := newRemoteSuggestions(gen)
	c.now = clock.now
	c.ensure(s, nil, func() string { return "screen" })
	waitFor(t, "the failed call", func() bool { return c.get(s).Status == suggestStatusError })
	if st := c.get(s); st.Error != suggestErrNoReplies {
		t.Fatalf("reason = %q, want %q", st.Error, suggestErrNoReplies)
	}
	clock.advance(suggestRetryAfter + time.Second)
	if c.needsCall(s) {
		t.Error("needsCall past the ordinary back-off for no usable replies")
	}
	c.ensure(s, nil, func() string { return "screen" })
	if st := c.get(s).Status; st != suggestStatusError || gen.count() != 1 {
		t.Fatalf("retried after %v: status %q, calls %d", suggestRetryAfter+time.Second, st, gen.count())
	}
	clock.advance(suggestNoRepliesRetry - suggestRetryAfter)
	if !c.needsCall(s) {
		t.Error("needsCall false past suggestNoRepliesRetry")
	}
	c.ensure(s, nil, func() string { return "screen" })
	waitFor(t, "the retry", func() bool { return gen.count() == 2 && c.get(s).Status == suggestStatusError })
}
