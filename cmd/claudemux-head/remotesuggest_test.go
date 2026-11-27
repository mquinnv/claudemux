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
	got, err := api.summarizer().SuggestReplies(context.Background(), waitingSession("Idle"), "● Done.\n", []string{"continue"})
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
	for _, s := range []string{"Session topic: db choice", "Fixed replies (do not repeat): continue", "● Done."} {
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
	} {
		api := newFakeMessagesAPI(t, tc.status, apiErrorBody(tc.kind))
		_, err := api.summarizer().SuggestReplies(context.Background(), waitingSession("Idle"), "screen", nil)
		if got := suggestReason(err); err == nil || got != tc.reason {
			t.Errorf("%d: err %v, reason %q, want %q", tc.status, err, got, tc.reason)
		}
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
		{"500", &anthropic.Error{StatusCode: 500}, suggestErrAPI},
		{"wrapped 401", fmt.Errorf("call: %w", &anthropic.Error{StatusCode: 401}), suggestErrAuth},
		{"deadline", context.DeadlineExceeded, suggestErrTimeout},
		{"wrapped deadline", fmt.Errorf("waiting for a call slot: %w", context.DeadlineExceeded), suggestErrTimeout},
		{"connection refused", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, suggestErrNetwork},
		{"net timeout", &net.OpError{Op: "read", Net: "tcp", Err: timeoutErr{}}, suggestErrTimeout},
		{"suggestError passthrough", &suggestError{suggestErrNoReplies, "x"}, suggestErrNoReplies},
		{"wrapped suggestError", fmt.Errorf("x: %w", &suggestError{suggestErrBadResp, "y"}), suggestErrBadResp},
		{"anything else", errors.New("boom"), suggestErrAPI},
	} {
		if got := suggestReason(tc.err); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.what, got, tc.want)
		}
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
	waitFor(t, "the failed call", func() bool { return c.get(s).Status == suggestStatusFailed })
	if st := c.get(s); st.Error != suggestErrAuth {
		t.Fatalf("reason = %q, want %q", st.Error, suggestErrAuth)
	}
	clock.advance(suggestRetryAfter + time.Second)
	if c.needsCall(s) {
		t.Error("needsCall past the ordinary back-off for an auth failure")
	}
	c.ensure(s, nil, func() string { return "screen" })
	if st := c.get(s).Status; st != suggestStatusFailed || gen.count() != 1 {
		t.Fatalf("retried an auth failure after %v: status %q, calls %d", suggestRetryAfter+time.Second, st, gen.count())
	}
	clock.advance(suggestAuthRetry - suggestRetryAfter)
	if !c.needsCall(s) {
		t.Error("needsCall false past suggestAuthRetry")
	}
	c.ensure(s, nil, func() string { return "screen" })
	waitFor(t, "the retry", func() bool { return gen.count() == 2 && c.get(s).Status == suggestStatusFailed })
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

type panicSuggester struct{}

func (panicSuggester) SuggestReplies(context.Context, swSession, string, []string) ([]remoteSuggestion, error) {
	panic("kaboom")
}

func TestRemoteSuggestionsPanicIsAnError(t *testing.T) {
	c := newRemoteSuggestions(panicSuggester{})
	s := idleSession()
	c.ensure(s, nil, func() string { return "screen" })
	waitFor(t, "the panicked call", func() bool { return c.get(s).Status == suggestStatusFailed })
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
