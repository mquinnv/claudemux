package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// conversationLine is one genuine user turn — the smallest record that makes
// a transcript describe a conversation rather than a session's bookkeeping.
const conversationLine = `{"type":"user","timestamp":"2026-09-10T10:05:33.532Z","message":{"role":"user","content":"fix the head"}}`

// bookkeepingStub is what Claude Code writes to a successor transcript the
// moment it records the continuation: the session's title, mode and
// snapshot records — and no conversation at all. Observed 2026-09-10 on
// remix-2: the fork's file held these lines for seven minutes before the
// conversation was copied in.
const bookkeepingStub = `{"type":"ai-title","aiTitle":"CRM-776 Medium reporting UI","sessionId":"new-id"}
{"type":"agent-name","agentName":"CRM-776 Medium reporting UI","sessionId":"new-id"}
{"type":"mode","mode":"normal","sessionId":"new-id"}
{"type":"permission-mode","permissionMode":"auto","sessionId":"new-id"}
{"type":"file-history-snapshot","messageId":"m1","snapshot":{"messageId":"m1","trackedFileBackups":{}},"isSnapshotUpdate":false}
`

// A summarize call against a transcript with no content events cannot
// succeed: the tool call is forced and placeholders are forbidden, so Haiku
// invents a label ("session setup", remix-2 on 2026-09-10) that the head
// then keeps as a real summary — and with a summary in hand, the growth rule
// never fires again. Only the next busy→idle edge would replace it, which on
// a long first turn is hours away. So no content, no call.
func TestCanSummarizeRequiresContent(t *testing.T) {
	now := time.Date(2026, 9, 10, 11, 14, 0, 0, time.UTC)
	m := model{
		summarizer:         &Summarizer{},
		minSummaryInterval: 20 * time.Second,
		lastSummaryAt:      now.Add(-time.Hour),
		allEvents: []Event{
			{Type: "ai-title"},
			{Type: "mode"},
			{Type: "attachment"},
		},
	}
	if m.canSummarize(now) {
		t.Error("canSummarize = true on bookkeeping-only events, want false")
	}
	m.allEvents = append(m.allEvents, Event{Type: "user", UserText: "fix the head"})
	if !m.canSummarize(now) {
		t.Error("canSummarize = false once a genuine prompt is on the ring, want true")
	}
}

// Rotation onto a transcript that has no conversation yet must not seed: the
// seed is what put "session setup" on remix-2's tab. It must also leave the
// growth baseline at zero so the first real events trigger the first call.
func TestSwitchSessionSkipsSeedWithoutContent(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old-sess.jsonl")
	if err := os.WriteFile(old, []byte(conversationLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(dir, "new-sess.jsonl")
	if err := os.WriteFile(stub, []byte(bookkeepingStub), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 10, 11, 14, 0, 0, time.UTC)
	m := model{
		jsonlPath:          old,
		sessionID:          "old-sess",
		reader:             newEventReader(old),
		summarizer:         &Summarizer{},
		minSummaryInterval: 20 * time.Second,
		lastSummaryAt:      now.Add(-time.Hour),
	}

	cmd := m.switchSession(stub, now)

	if cmd != nil {
		t.Error("cmd non-nil, want nil: nothing to summarize in a bookkeeping-only transcript")
	}
	if m.summarizing {
		t.Error("summarizing = true, want false: no call was issued")
	}
	if m.lastSummaryEvents != 0 {
		t.Errorf("lastSummaryEvents = %d, want 0 so growth can fire the first call", m.lastSummaryEvents)
	}
}

// newModel's summarizing flag is Init's seed decision (Init cannot set it),
// so it must be false on a transcript with no content — otherwise Init
// fires the seed against nothing and the flag guards a call that should
// never have gone out.
func TestNewModelDoesNotSeedWithoutContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")
	if err := os.WriteFile(path, []byte(bookkeepingStub), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("CLAUDEMUX_ENV", filepath.Join(t.TempDir(), "absent"))

	m := newModel(defaultConfig(), path, "sess", false)

	if m.summarizer == nil {
		t.Fatal("summarizer = nil, want non-nil when ANTHROPIC_API_KEY is set")
	}
	if m.summarizing {
		t.Error("summarizing = true, want false: there is nothing to seed from")
	}
}
