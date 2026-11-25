package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func suggestResponse(text string) string {
	input, _ := json.Marshal(map[string]string{"text": text})
	return toolUseResponseRaw(suggestToolName, json.RawMessage(input))
}

func TestCleanSuggestions(t *testing.T) {
	long := strings.Repeat("a", 61)
	exact := strings.Repeat("b", 60)
	cases := map[string]struct {
		in   string
		want []string
	}{
		"bullets":   {"- run the tests\n* commit it\n• push it", []string{"run the tests", "commit it", "push it"}},
		"numbering": {"1. run the tests\n2) commit it", []string{"run the tests", "commit it"}},
		"quotes":    {"\"run the tests\"\n'commit it'", []string{"run the tests", "commit it"}},
		"too long":  {long + "\nrun the tests\n" + exact, []string{"run the tests", exact}},
		"four":      {"one\ntwo\nthree\nfour", []string{"one", "two", "three"}},
		"blanks":    {"\n  \nrun the tests\n\n", []string{"run the tests"}},
		"empty":     {"", nil},
		"spaces":    {"   \n\n", nil},
	}
	for name, c := range cases {
		if got := cleanSuggestions(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: cleanSuggestions(%q) = %q, want %q", name, c.in, got, c.want)
		}
	}
}

func TestSuggestCache(t *testing.T) {
	var c suggestCache
	t0 := time.Now()
	if _, ok := c.get("api", t0); ok {
		t.Fatal("empty cache must miss")
	}
	c.put("api", t0, []string{"run the tests"})
	if v, ok := c.get("api", t0); !ok || !reflect.DeepEqual(v, []string{"run the tests"}) {
		t.Errorf("same Since: got %v, %v", v, ok)
	}
	if _, ok := c.get("api", t0.Add(time.Second)); ok {
		t.Error("a later Since must miss")
	}
	if _, ok := c.get("web", t0); ok {
		t.Error("another session must miss")
	}
}

func TestSuggestRepliesParsesToolCall(t *testing.T) {
	d := &fakeDoer{body: suggestResponse("1. run the tests again\n2. commit the fix\n\"push the branch\"")}
	got, err := testSummarizer(d).SuggestReplies(context.Background(), "All done. Tests pass.")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run the tests again", "commit the fix", "push the branch"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if choice, _ := d.gotReq["tool_choice"].(map[string]any); choice["name"] != suggestToolName {
		t.Errorf("tool_choice = %v, want forced %q", d.gotReq["tool_choice"], suggestToolName)
	}
	msgs, _ := json.Marshal(d.gotReq["messages"])
	if !strings.Contains(string(msgs), "All done. Tests pass.") {
		t.Errorf("the last message must reach the model: %s", msgs)
	}
}

func TestSuggestRepliesNilAndErrors(t *testing.T) {
	var s *Summarizer
	if got, err := s.SuggestReplies(context.Background(), "x"); got != nil || err != nil {
		t.Errorf("nil summarizer = %v, %v; want nil, nil", got, err)
	}
	d := &fakeDoer{body: textOnlyResponse()}
	if _, err := testSummarizer(d).SuggestReplies(context.Background(), "x"); err == nil {
		t.Error("text-only response: want an error")
	}
	d = &fakeDoer{body: suggestResponse("n/a")}
	if got, err := testSummarizer(d).SuggestReplies(context.Background(), "x"); err == nil || got != nil {
		t.Errorf("placeholder: got %v, %v; want an error", got, err)
	}
}

func TestSuggestSystemPromptForbidsStandingPermission(t *testing.T) {
	for _, want := range []string{"always", "don't ask again", "do whatever you think", "question", "complete instruction"} {
		if !strings.Contains(suggestSystemPrompt, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
}
