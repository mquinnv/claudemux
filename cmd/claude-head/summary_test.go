package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func TestCondenseTranscript(t *testing.T) {
	events := []Event{
		{Type: "user", UserText: "fix the worktree chip"},
		{Type: "assistant", UserText: "Looking at tui.go now."},
		{Type: "assistant", ToolUses: []ToolUse{{Name: "Read"}, {Name: "Edit"}}},
		{Type: "user", UserText: "<system-reminder>ignore me</system-reminder>"},
		{Type: "file-history-snapshot", UserText: "bookkeeping"},
		{Type: "user", IsMeta: true, UserText: "caveat notice"},
	}

	got := condenseTranscript("fix the worktree chip", events, 30)

	for _, want := range []string{
		"first prompt: fix the worktree chip",
		"user: fix the worktree chip",
		"assistant: Looking at tui.go now.",
		"tool: Read",
		"tool: Edit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript missing %q\ngot:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"system-reminder", "bookkeeping", "caveat notice"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("transcript should not contain %q\ngot:\n%s", unwanted, got)
		}
	}
}

// maxEvents budgets CONTENT events, not raw ring entries: the tail is taken
// after bookkeeping is filtered out, so interleaved bookkeeping cannot push a
// content event out of the window.
func TestCondenseTranscriptKeepsTail(t *testing.T) {
	var events []Event
	for _, s := range []string{"one", "two", "three"} {
		events = append(events, Event{Type: "attachment", UserText: "bookkeeping " + s})
		events = append(events, Event{Type: "user", UserText: s})
	}

	got := condenseTranscript("one", events, 2)

	if strings.Contains(got, "user: one") {
		t.Errorf("oldest content event should be dropped when over maxEvents\ngot:\n%s", got)
	}
	for _, want := range []string{"user: two", "user: three"} {
		if !strings.Contains(got, want) {
			t.Errorf("maxEvents counts content events, so %q must survive\ngot:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "first prompt: one") {
		t.Errorf("first prompt anchors the topic and must survive truncation\ngot:\n%s", got)
	}
}

// The event ring is the RAW JSONL stream, where bookkeeping records
// (attachment, mode, permission-mode, ...) outnumber content records several to
// one. Slicing the raw tail before filtering spent most of the window on records
// that say nothing about the session, starving the recency-sensitive `now` line.
func TestCondenseTranscriptBookkeepingDoesNotConsumeWindow(t *testing.T) {
	events := []Event{{Type: "user", UserText: "fix the summary window"}}
	for i := 0; i < summaryMaxEvents; i++ {
		events = append(events, Event{Type: "attachment", UserText: "attachment " + strconv.Itoa(i)})
	}
	events = append(events,
		Event{Type: "assistant", UserText: "reading summary.go"},
		Event{Type: "agent-color", UserText: "agent-color bookkeeping"},
		Event{Type: "assistant", ToolUses: []ToolUse{{Name: "Edit"}}},
		Event{Type: "permission-mode", UserText: "permission-mode bookkeeping"},
	)

	got := condenseTranscript("fix the summary window", events, summaryMaxEvents)

	for _, want := range []string{
		"user: fix the summary window",
		"assistant: reading summary.go",
		"tool: Edit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("bookkeeping must not consume the window: %q missing\ngot:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"attachment", "agent-color", "permission-mode"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("transcript should not contain %q\ngot:\n%s", unwanted, got)
		}
	}
}

// Claude Code can write the same prompt as several last-prompt records in a row.
// Emitting each one duplicates the user line and spends window on nothing.
func TestCondenseTranscriptSkipsRepeatedLastPrompt(t *testing.T) {
	events := []Event{
		{Type: "user", UserText: "fix the chip"},
		{Type: "last-prompt", UserText: "fix the chip"},
		{Type: "last-prompt", UserText: "  fix the chip  "},
		{Type: "assistant", UserText: "on it"},
	}

	got := condenseTranscript("", events, summaryMaxEvents)

	if n := strings.Count(got, "user: fix the chip"); n != 1 {
		t.Errorf("repeated last-prompt records must collapse to one user line, got %d\ngot:\n%s", n, got)
	}
}

// The de-duplication guard is for repeated last-prompt bookkeeping only: two
// genuine user turns that happen to carry the same text are two real prompts.
func TestCondenseTranscriptKeepsRepeatedUserTurns(t *testing.T) {
	events := []Event{
		{Type: "user", UserText: "again"},
		{Type: "user", UserText: "again"},
	}

	got := condenseTranscript("", events, summaryMaxEvents)

	if n := strings.Count(got, "user: again"); n != 2 {
		t.Errorf("repeated user turns are real prompts and must both survive, got %d\ngot:\n%s", n, got)
	}
}

func TestCondenseTranscriptTruncatesLongText(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := condenseTranscript("", []Event{{Type: "user", UserText: long}}, 30)

	for _, line := range strings.Split(got, "\n") {
		if len([]rune(line)) > 220 {
			t.Errorf("line exceeds truncation budget: %d runes", len([]rune(line)))
		}
	}
}

func TestCondenseTranscriptNegativeMaxEvents(t *testing.T) {
	events := []Event{
		{Type: "user", UserText: "one"},
		{Type: "user", UserText: "two"},
		{Type: "user", UserText: "three"},
	}

	got := condenseTranscript("one", events, -1)

	if strings.Contains(got, "user:") {
		t.Errorf("negative maxEvents must yield no tail events, not panic\ngot:\n%s", got)
	}
}

func TestCondenseTranscriptZeroMaxEvents(t *testing.T) {
	events := []Event{
		{Type: "user", UserText: "one"},
		{Type: "user", UserText: "two"},
	}

	got := condenseTranscript("one", events, 0)

	if strings.Contains(got, "user:") {
		t.Errorf("zero maxEvents must yield no tail events\ngot:\n%s", got)
	}
}

func TestCondenseTranscriptAssistantToolOnlyEmitsNoBlankLine(t *testing.T) {
	events := []Event{
		{Type: "assistant", ToolUses: []ToolUse{{Name: "Read"}}},
	}

	got := condenseTranscript("", events, 30)

	if strings.Contains(got, "assistant: ") {
		t.Errorf("assistant event with empty UserText must not emit a blank assistant line\ngot:\n%s", got)
	}
	if !strings.Contains(got, "tool: Read") {
		t.Errorf("tool use must still be emitted\ngot:\n%s", got)
	}
}

type fakeDoer struct {
	body      string
	status    int
	err       error
	gotReq    map[string]any
	gotHeader http.Header
	gotURL    *url.URL
	calls     int
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	f.gotHeader = req.Header
	f.gotURL = req.URL
	if req.Body != nil {
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &f.gotReq)
	}
	status := f.status
	if status == 0 {
		status = 200
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Request:    req,
	}, nil
}

func toolUseResponse(topic, now string) string {
	input, _ := json.Marshal(map[string]string{"topic": topic, "now": now})
	return toolUseResponseRaw(summarizeToolName, json.RawMessage(input))
}

func toolUseResponseRaw(toolName string, input json.RawMessage) string {
	body, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant",
		"model": "claude-haiku-4-5", "stop_reason": "tool_use",
		"content": []any{map[string]any{
			"type": "tool_use", "id": "toolu_1", "name": toolName,
			"input": input,
		}},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	return string(body)
}

func textOnlyResponse() string {
	body, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant",
		"model": "claude-haiku-4-5", "stop_reason": "end_turn",
		"content": []any{map[string]any{"type": "text", "text": "no tool call here"}},
		"usage":   map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	return string(body)
}

func testSummarizer(d *fakeDoer) *Summarizer {
	return newSummarizer(defaultConfig().Summary, option.WithAPIKey("test-key"), option.WithHTTPClient(d))
}

func TestSummarizeReturnsBothLines(t *testing.T) {
	d := &fakeDoer{body: toolUseResponse("fixing the worktree chip", "running the tui tests")}
	s := testSummarizer(d)

	got, err := s.Summarize(context.Background(), "fix the chip", []Event{{Type: "user", UserText: "fix the chip"}}, "")
	if err != nil {
		t.Fatalf("Summarize() error = %v", err)
	}
	if got.Topic != "fixing the worktree chip" {
		t.Errorf("Topic = %q, want %q", got.Topic, "fixing the worktree chip")
	}
	if got.Now != "running the tui tests" {
		t.Errorf("Now = %q, want %q", got.Now, "running the tui tests")
	}
}

func TestSummarizeForcesTheTool(t *testing.T) {
	d := &fakeDoer{body: toolUseResponse("a", "b")}
	s := testSummarizer(d)

	if _, err := s.Summarize(context.Background(), "", nil, ""); err != nil {
		t.Fatalf("Summarize() error = %v", err)
	}

	choice, ok := d.gotReq["tool_choice"].(map[string]any)
	if !ok {
		t.Fatalf("request has no tool_choice: %v", d.gotReq)
	}
	if choice["type"] != "tool" || choice["name"] != summarizeToolName {
		t.Errorf("tool_choice = %v, want a forced %q call", choice, summarizeToolName)
	}
	if d.gotReq["model"] != string(anthropic.ModelClaudeHaiku4_5) {
		t.Errorf("model = %v, want %v", d.gotReq["model"], anthropic.ModelClaudeHaiku4_5)
	}
}

func TestSummarizeAnchorsThePreviousTopic(t *testing.T) {
	d := &fakeDoer{body: toolUseResponse("a", "b")}
	s := testSummarizer(d)

	prevTopic := "fixing the worktree chip"
	if _, err := s.Summarize(context.Background(), "", nil, prevTopic); err != nil {
		t.Fatalf("Summarize() error = %v", err)
	}

	messages, ok := d.gotReq["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("request has no messages: %v", d.gotReq)
	}
	msg, ok := messages[0].(map[string]any)
	if !ok || msg["role"] != "user" {
		t.Fatalf("messages[0] is not a user message: %v", messages[0])
	}
	content, ok := msg["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("user message has no content blocks: %v", msg)
	}
	block, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] is not an object: %v", content[0])
	}
	text, _ := block["text"].(string)
	if !strings.Contains(text, prevTopic) {
		t.Errorf("previous topic must anchor the user message content specifically; got:\n%s", text)
	}
}

func TestSummarizeErrorsOnAPIFailure(t *testing.T) {
	d := &fakeDoer{status: 500, body: `{"type":"error","error":{"type":"api_error","message":"boom"}}`}
	s := testSummarizer(d)

	if _, err := s.Summarize(context.Background(), "", nil, ""); err == nil {
		t.Error("Summarize() error = nil, want an error so the TUI falls back to raw prompts")
	}
}

func TestSummarizeErrorsOnNoToolUseBlock(t *testing.T) {
	d := &fakeDoer{body: textOnlyResponse()}
	s := testSummarizer(d)

	if _, err := s.Summarize(context.Background(), "", nil, ""); err == nil {
		t.Error("Summarize() error = nil, want an error when the response has no tool_use block")
	}
}

func TestSummarizeErrorsOnWrongToolName(t *testing.T) {
	input, _ := json.Marshal(map[string]string{"foo": "bar"})
	d := &fakeDoer{body: toolUseResponseRaw("not_"+summarizeToolName, input)}
	s := testSummarizer(d)

	if _, err := s.Summarize(context.Background(), "", nil, ""); err == nil {
		t.Error("Summarize() error = nil, want an error when the tool_use block names a different tool")
	}
}

func TestSummarizeErrorsOnMalformedToolInput(t *testing.T) {
	d := &fakeDoer{body: toolUseResponseRaw(summarizeToolName, json.RawMessage(`{"topic":123,"now":true}`))}
	s := testSummarizer(d)

	if _, err := s.Summarize(context.Background(), "", nil, ""); err == nil {
		t.Error("Summarize() error = nil, want an error when the tool input doesn't decode into Summary")
	}
}

func TestSummarizeErrorsOnBothFieldsEmpty(t *testing.T) {
	d := &fakeDoer{body: toolUseResponse("", "")}
	s := testSummarizer(d)

	if _, err := s.Summarize(context.Background(), "", nil, ""); err == nil {
		t.Error("Summarize() error = nil, want an error when both topic and now are empty")
	}
}

func TestSummarizeErrorsOnNowEmpty(t *testing.T) {
	d := &fakeDoer{body: toolUseResponse("fixing the worktree chip", "")}
	s := testSummarizer(d)

	if _, err := s.Summarize(context.Background(), "", nil, ""); err == nil {
		t.Error("Summarize() error = nil, want an error when now is empty even though topic is set — a half-empty summary must not render a blank status row")
	}
}

func TestSummarizeErrorsOnTopicEmpty(t *testing.T) {
	d := &fakeDoer{body: toolUseResponse("", "running the tui tests")}
	s := testSummarizer(d)

	if _, err := s.Summarize(context.Background(), "", nil, ""); err == nil {
		t.Error("Summarize() error = nil, want an error when topic is empty even though now is set")
	}
}

// A whitespace-only line is as useless as an empty one, but it passes
// promptRows' non-empty predicate — so the pane would render the "—" placeholder
// instead of falling back to the raw first/last prompts.
func TestSummarizeErrorsOnWhitespaceOnlyLines(t *testing.T) {
	for _, tt := range []struct {
		name       string
		topic, now string
	}{
		{"whitespace topic", "   ", "running the tui tests"},
		{"whitespace now", "fixing the worktree chip", "\t\n "},
		{"both whitespace", " ", "  "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := &fakeDoer{body: toolUseResponse(tt.topic, tt.now)}
			s := testSummarizer(d)

			if _, err := s.Summarize(context.Background(), "", nil, ""); err == nil {
				t.Error("Summarize() error = nil, want an error: a whitespace-only line must fall back to the raw prompts, not render as the placeholder")
			}
		})
	}
}

func TestNewSummarizerNilWithoutKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	// Without this, configValue falls through to the real claude-head env
	// file on this machine (and any FIFO-backed one blocks for envFileTimeout).
	t.Setenv("CLAUDE_HEAD_ENV", filepath.Join(t.TempDir(), "absent"))
	if s := newSummarizer(defaultConfig().Summary); s != nil {
		t.Error("newSummarizer() must return nil without a key — a keyless SDK client would fall back to the OAuth profile and spend the subscription budget")
	}
}

func TestNewSummarizerNilWithWhitespaceOnlyKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "   ")
	t.Setenv("CLAUDE_HEAD_ENV", filepath.Join(t.TempDir(), "absent"))
	if s := newSummarizer(defaultConfig().Summary); s != nil {
		t.Error("newSummarizer() must return nil for a whitespace-only key — a blank key would 401 on every poll instead of cleanly disabling the feature")
	}
}

// TestNewSummarizerNeverAuthenticatesFromAmbientCredentials proves the billing
// invariant structurally: a caller option that carries no credential (WithHTTPClient)
// must not let the SDK's own environment/profile resolution contribute one, no matter
// what is or isn't configured on the machine running the test.
func TestNewSummarizerNeverAuthenticatesFromAmbientCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	// Not strictly required by the current bypass-on-caller-options code path,
	// but pinned anyway: this test's entire point is ambient-credential
	// isolation, so it must not itself depend on what's on this machine.
	t.Setenv("CLAUDE_HEAD_ENV", filepath.Join(t.TempDir(), "absent"))
	d := &fakeDoer{body: toolUseResponse("a", "b")}

	s := newSummarizer(defaultConfig().Summary, option.WithHTTPClient(d))
	if s == nil {
		t.Fatal("newSummarizer() = nil; a caller-supplied option should still construct a client")
	}
	if _, err := s.Summarize(context.Background(), "", nil, ""); err != nil {
		t.Fatalf("Summarize() error = %v; a client built from caller-only options must not depend on ambient credentials", err)
	}
	if d.gotHeader == nil {
		t.Fatal("fake transport never received a request")
	}
	if key := d.gotHeader.Get("X-Api-Key"); key != "" {
		t.Errorf("request carried an x-api-key the caller never supplied (%q) — the SDK's environment/profile credential resolution must be disabled structurally", key)
	}
	if auth := d.gotHeader.Get("Authorization"); auth != "" {
		t.Errorf("request carried an Authorization header the caller never supplied (%q) — the SDK's environment/profile credential resolution must be disabled structurally", auth)
	}
}

// option.WithoutEnvironmentDefaults() — which newSummarizer applies
// unconditionally to keep the SDK away from an ambient, subscription-billing
// credential — also suppresses the SDK's own ANTHROPIC_BASE_URL handling. A user
// behind a gateway whose ANTHROPIC_API_KEY is a *proxy* token would then have
// that token sent to api.anthropic.com: a secret disclosed to a host they never
// chose. The base URL must be honored explicitly.
func TestSummarizerEnvOptionsHonorBaseURL(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "proxy-token")
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.internal.example/v1")

	d := &fakeDoer{body: toolUseResponse("a", "b")}
	s := newSummarizer(defaultConfig().Summary, append(summarizerEnvOptions(defaultConfig().Summary), option.WithHTTPClient(d))...)
	if s == nil {
		t.Fatal("newSummarizer() = nil, want a client")
	}
	if _, err := s.Summarize(context.Background(), "", nil, ""); err != nil {
		t.Fatalf("Summarize() error = %v", err)
	}
	if d.gotURL == nil {
		t.Fatal("fake transport never received a request")
	}
	if d.gotURL.Host != "gateway.internal.example" {
		t.Errorf("request host = %q, want the configured ANTHROPIC_BASE_URL host — a proxy token must never be sent to api.anthropic.com", d.gotURL.Host)
	}
	if got := d.gotHeader.Get("X-Api-Key"); got != "proxy-token" {
		t.Errorf("x-api-key = %q, want the env key", got)
	}
}

// Without ANTHROPIC_BASE_URL set, the SDK's production default must still apply.
func TestSummarizerEnvOptionsDefaultBaseURL(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	// The empty ANTHROPIC_BASE_URL above would otherwise fall through to
	// whatever ANTHROPIC_BASE_URL the real claude-head env file supplies.
	t.Setenv("CLAUDE_HEAD_ENV", filepath.Join(t.TempDir(), "absent"))

	d := &fakeDoer{body: toolUseResponse("a", "b")}
	s := newSummarizer(defaultConfig().Summary, append(summarizerEnvOptions(defaultConfig().Summary), option.WithHTTPClient(d))...)
	if s == nil {
		t.Fatal("newSummarizer() = nil, want a client")
	}
	if _, err := s.Summarize(context.Background(), "", nil, ""); err != nil {
		t.Fatalf("Summarize() error = %v", err)
	}
	if d.gotURL == nil {
		t.Fatal("fake transport never received a request")
	}
	if d.gotURL.Host != "api.anthropic.com" {
		t.Errorf("request host = %q, want api.anthropic.com when no base URL is configured", d.gotURL.Host)
	}
}

func TestSummarizerEnvOptionsNilWithoutKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.internal.example")
	t.Setenv("CLAUDE_HEAD_ENV", filepath.Join(t.TempDir(), "absent"))

	if opts := summarizerEnvOptions(defaultConfig().Summary); opts != nil {
		t.Error("summarizerEnvOptions(defaultConfig().Summary) must be nil without a key — a base URL alone is not a credential and must not construct a client")
	}
}

// TestNewSummarizerCapsRetriesOnCallerOptionsPath proves the retry cap applies even when
// the caller supplies its own options (not just on the zero-options path), so a poll loop
// never blocks for seconds on SDK backoff when the API is down.
func TestNewSummarizerCapsRetriesOnCallerOptionsPath(t *testing.T) {
	d := &fakeDoer{status: 500, body: `{"type":"error","error":{"type":"api_error","message":"boom"}}`}
	s := testSummarizer(d)

	if _, err := s.Summarize(context.Background(), "", nil, ""); err == nil {
		t.Fatal("Summarize() error = nil, want an error from the fake 500 responses")
	}
	if d.calls != 2 {
		t.Errorf("request attempts = %d, want 2 (1 initial + WithMaxRetries(1)); the retry cap must apply on the caller-options path too", d.calls)
	}
}

// A forced tool call cannot decline, so on a too-thin transcript the model emits
// a non-answer ("<UNKNOWN>", "n/a") rather than refusing. Rendering that puts a
// literal "<UNKNOWN>" on the status line where the raw prompt would have said
// more — so treat it as no summary and fall back.
func TestSummarizeRejectsPlaceholderLines(t *testing.T) {
	tests := []struct {
		name  string
		topic string
		now   string
	}{
		{"angle-bracketed topic", "<UNKNOWN>", "running tests"},
		{"angle-bracketed now", "fixing the chip", "<unknown>"},
		{"literal unknown", "unknown", "running tests"},
		{"n/a", "n/a", "running tests"},
		{"none", "fixing the chip", "none"},
		{"dash", "-", "running tests"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &fakeDoer{body: toolUseResponse(tt.topic, tt.now)}
			s := testSummarizer(d)
			if _, err := s.Summarize(context.Background(), "", nil, ""); err == nil {
				t.Errorf("Summarize() with topic=%q now=%q returned nil error; a placeholder must "+
					"error so the pane falls back to the raw prompts", tt.topic, tt.now)
			}
		})
	}
}
