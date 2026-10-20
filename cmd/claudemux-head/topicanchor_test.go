package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The slack-agent session that prompted this: its first prompt was a bare
// screenshot, so after a head restart the only anchor left was "[Image #1]"
// and the topic became the plan-execution step it was in.
func TestFirstAssistantReply(t *testing.T) {
	user := func(text string) Event { return Event{Type: "user", UserText: text} }
	say := func(text string) Event { return Event{Type: "assistant", UserText: text} }

	tests := []struct {
		name   string
		events []Event
		first  string
		want   string
	}{
		{
			name:   "the reply after the first prompt",
			events: []Event{user("[Image #1]"), say("The dialled number is dropped by the sync."), say("later")},
			first:  "[Image #1]",
			want:   "The dialled number is dropped by the sync.",
		},
		{
			name:   "tool-only and sidechain turns are skipped",
			events: []Event{user("go"), {Type: "assistant", ToolUses: []ToolUse{{Name: "Read"}}}, {Type: "assistant", UserText: "subagent", IsSidechain: true}, say("main reply")},
			first:  "go",
			want:   "main reply",
		},
		{
			name:   "text before the first prompt does not count",
			events: []Event{say("stale"), user("fix it"), say("fixing")},
			first:  "fix it",
			want:   "fixing",
		},
		{
			name:   "no reply yet",
			events: []Event{user("fix it")},
			first:  "fix it",
			want:   "",
		},
		{
			name:   "a slash-command first prompt gets no reply",
			events: []Event{user("/clear"), say("cleared")},
			first:  "/clear",
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstAssistantReply(tt.events, tt.first); got != tt.want {
				t.Errorf("firstAssistantReply() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCaptureFirstReply(t *testing.T) {
	say := func(text string) Event { return Event{Type: "assistant", UserText: text} }

	// Seeded with the prompt but no reply: the next batch's first text is it.
	r := &EventReader{firstPrompt: "go"}
	r.captureFirstReply([]Event{say("on it: the sync drops the number")})
	if got := r.FirstReply(); got != "on it: the sync drops the number" {
		t.Errorf("FirstReply() = %q", got)
	}
	// Frozen once set.
	r.captureFirstReply([]Event{say("something else")})
	if got := r.FirstReply(); got != "on it: the sync drops the number" {
		t.Errorf("FirstReply() changed to %q", got)
	}

	// A batch carrying the prompt waits for the reply after it, and text
	// before the prompt is not that reply.
	r = &EventReader{firstPrompt: "fix it"}
	r.captureFirstReply([]Event{{Type: "user", UserText: "fix it"}})
	if got := r.FirstReply(); got != "" {
		t.Errorf("FirstReply() = %q before any reply", got)
	}
}

func TestCondenseTranscriptFirstReply(t *testing.T) {
	got := condenseTranscript("[Image #1]", "The dialled number is dropped.", nil, 30)
	if !strings.Contains(got, "first prompt: [Image #1]\nfirst reply: The dialled number is dropped.\n") {
		t.Errorf("anchor lines missing or out of order:\n%s", got)
	}
	if got := condenseTranscript("x", "", nil, 30); strings.Contains(got, "first reply") {
		t.Errorf("empty reply rendered a line:\n%s", got)
	}
}

func TestRecordedTopic(t *testing.T) {
	dir := t.TempDir()
	for _, r := range []sessionRecord{
		{SessionName: "a", SessionID: "s1", Topic: "capture dialled number"},
		{SessionName: "b", SessionID: "s2"},
	} {
		if err := writeSessionRecord(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	if got := recordedTopic(dir, "s1"); got != "capture dialled number" {
		t.Errorf("recordedTopic(s1) = %q", got)
	}
	for _, id := range []string{"s2", "s3", ""} {
		if got := recordedTopic(dir, id); got != "" {
			t.Errorf("recordedTopic(%q) = %q, want empty", id, got)
		}
	}
}

func TestSessionRecordKeepsCarriedTopic(t *testing.T) {
	m := model{sessionID: "s1", carriedTopic: "capture dialled number"}
	if got := m.sessionRecordFor(time.Now()).Topic; got != "capture dialled number" {
		t.Errorf("record topic before a summary = %q, want the carried one", got)
	}
	m.summary = Summary{Topic: "fresh", Now: "n", Tab: "t"}
	if got := m.sessionRecordFor(time.Now()).Topic; got != "fresh" {
		t.Errorf("record topic after a summary = %q, want the landed one", got)
	}
}

func TestSummarizeUsesCarriedTopicAsPrevious(t *testing.T) {
	d := &fakeDoer{body: toolUseResponse("a", "b")}
	m := model{summarizer: testSummarizer(d), carriedTopic: "capture dialled number"}
	m.summarize()()
	if !strings.Contains(reqText(d), "Previous topic: capture dialled number") {
		t.Errorf("carried topic not sent as the previous topic; body:\n%s", reqText(d))
	}

	d = &fakeDoer{body: toolUseResponse("a", "b")}
	m = model{summarizer: testSummarizer(d), carriedTopic: "old", summary: Summary{Topic: "landed"}}
	m.summarize()()
	if !strings.Contains(reqText(d), "Previous topic: landed") {
		t.Errorf("a landed topic must win over the carried one; body:\n%s", reqText(d))
	}
}

// reqText is the whole captured request body as JSON text, for substring checks.
func reqText(d *fakeDoer) string {
	b, _ := json.Marshal(d.gotReq)
	return string(b)
}
