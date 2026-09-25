package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func headlineResponse(text string) string {
	input, _ := json.Marshal(map[string]string{"text": text})
	return toolUseResponseRaw(headlineToolName, json.RawMessage(input))
}

func TestBuildHeadlinePromptOmitsPromptsKeepsBlockers(t *testing.T) {
	sessions := append(webTestSnapshot().Sessions, swSession{Name: "fresh", Context: -1})
	p := buildHeadlinePrompt(sessions)
	if strings.Contains(p, "run <b>the</b> tests") {
		t.Errorf("the raw prompt must never reach the headline model:\n%s", p)
	}
	for _, want := range []string{"api", "Idle", "build fixes", "fixing the build", "web", "Thinking", "blocked", "waiting on review"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Index(p, "api") > strings.Index(p, "web") || strings.Index(p, "web") > strings.Index(p, "blocked") {
		t.Errorf("sessions must keep lobby order:\n%s", p)
	}
	if !strings.Contains(p, "unknown") {
		t.Errorf("an unpublished state must read as unknown, not blank:\n%s", p)
	}
}

func TestHeadlineParsesToolCall(t *testing.T) {
	d := &fakeDoer{body: headlineResponse("Fixing the build while a deploy waits on review.")}
	got, err := testSummarizer(d).Headline(context.Background(), webTestSnapshot().Sessions)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Fixing the build while a deploy waits on review." {
		t.Errorf("got %q", got)
	}
	tools, _ := d.gotReq["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("request tools = %v, want exactly the headline tool", d.gotReq["tools"])
	}
	if choice, _ := d.gotReq["tool_choice"].(map[string]any); choice["name"] != headlineToolName {
		t.Errorf("tool_choice = %v, want forced %q", d.gotReq["tool_choice"], headlineToolName)
	}
}

func TestHeadlineRejectsPlaceholderAndTextOnly(t *testing.T) {
	for name, body := range map[string]string{"placeholder": headlineResponse("n/a"), "text-only": textOnlyResponse()} {
		d := &fakeDoer{body: body}
		if _, err := testSummarizer(d).Headline(context.Background(), webTestSnapshot().Sessions); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestHeadlineGate(t *testing.T) {
	t0 := time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)
	g := headlineGate{interval: 2 * time.Minute}
	if !g.due("a", t0) {
		t.Fatal("first snapshot must be due without waiting the interval")
	}
	if g.due("a", t0.Add(time.Second)) {
		t.Error("same fingerprint must never be due")
	}
	if g.due("b", t0.Add(time.Minute)) {
		t.Error("a change inside the interval must wait")
	}
	if !g.due("b", t0.Add(2*time.Minute)) {
		t.Error("the same change must fire once the interval has passed")
	}
	if g.due("b", t0.Add(10*time.Minute)) {
		t.Error("after firing, the fingerprint is recorded and must not fire again")
	}
	zero := headlineGate{}
	if !zero.due("a", t0) || !zero.due("b", t0) || zero.due("b", t0) {
		t.Error("a zero interval fires on every change and only on change")
	}
}
