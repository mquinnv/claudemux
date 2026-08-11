package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// repoDocPath locates a file relative to the repo root, resolved via this
// test file's own path (like worktreeHookPath in worktreehook_test.go) so
// it doesn't depend on the working directory the test binary happens to run
// from.
func repoDocPath(t *testing.T, relPath string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", relPath)
}

// --- fixture builders -------------------------------------------------------
//
// Launch detection is gated on the tool_use that PRODUCED a result, so every
// launch fixture is a pair of events: the assistant turn that called the tool,
// then the user turn carrying its tool_result. That is how the transcript
// really reads (see testdata/launch-*.jsonl), and it is why these helpers
// return a slice rather than one event.

func bgToolUseEvent(toolUseID, name, ts string, input map[string]interface{}) Event {
	return Event{
		Type:      "assistant",
		Timestamp: ts,
		ToolUses:  []ToolUse{{ID: toolUseID, Name: name, Input: input}},
	}
}

func bgResultEvent(toolUseID, content, ts string) Event {
	return Event{
		Type:        "user",
		Timestamp:   ts,
		ToolResults: []ToolResult{{ToolUseID: toolUseID, Content: content}},
	}
}

// bgShellLaunch is one complete background-shell launch: the Bash tool_use
// carrying run_in_background, then its acknowledgement.
func bgShellLaunch(id, ts string) []Event {
	use := "toolu_" + id
	return []Event{
		bgToolUseEvent(use, "Bash", ts, map[string]interface{}{
			"command":           "sleep 300",
			"run_in_background": true,
		}),
		bgResultEvent(use, "Command running in background with ID: "+id+
			". Output is being written to: /tmp/x", ts),
	}
}

func bgDoneEvent(id string) Event {
	return Event{Type: "queue-operation", QueueText: "<task-notification>\n<task-id>" + id + "</task-id>\n<status>completed</status>"}
}

// bgFixture loads one of the real captured transcript excerpts in testdata,
// parsing it exactly as production does — parseEvent per line — and returns
// the events plus a clock reading taken from the transcript itself, so an
// old fixture never silently expires against a wall clock.
func bgFixture(t *testing.T, name string) ([]Event, time.Time) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	var events []Event
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		e, ok := parseEvent(line)
		if !ok {
			t.Fatalf("%s: parseEvent rejected a captured line", name)
		}
		events = append(events, e)
	}
	if len(events) != 2 {
		t.Fatalf("%s: want a tool_use event and its tool_result event, got %d events", name, len(events))
	}
	if len(events[0].ToolUses) != 1 || len(events[1].ToolResults) != 1 {
		t.Fatalf("%s: fixture is no longer a tool_use/tool_result pair", name)
	}
	return events, parseTimestampOr(events[1].Timestamp, time.Now())
}

// --- launches: the structural gate ------------------------------------------

// A launch is registered from the identity of the tool that produced the
// result, using payloads captured verbatim from real transcripts. Both kinds
// go through parseEvent first, so the content arrives flattened and
// JSON-unescaped the way the running head sees it.
func TestBgTrackerRegistersRealTranscriptLaunches(t *testing.T) {
	tests := []struct {
		fixture string
		wantID  string
	}{
		{"launch-shell.jsonl", "boigiwsir"},
		{"launch-agent.jsonl", "a99a8221a00c2d373"},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			events, now := bgFixture(t, tt.fixture)
			b := newBgTracker()
			b.observe(events, now)
			if n, _ := b.outstanding(now); n != 1 {
				t.Fatalf("outstanding = %d, want 1: a real launch must register", n)
			}
			// Retiring by the expected id is what proves the id was
			// extracted, not merely that something was counted.
			b.observe([]Event{bgDoneEvent(tt.wantID)}, now)
			if n, _ := b.outstanding(now); n != 0 {
				t.Errorf("outstanding = %d, want 0: the launch should have been tracked under %q", n, tt.wantID)
			}
		})
	}
}

// The tool_use and its result usually arrive in different polls: the assistant
// turn lands as soon as it is written, the result whenever the tool returns.
// The pending map has to survive between observe calls or every real launch
// whose result was even one poll behind would go unnoticed.
func TestBgTrackerLaunchSpansPollBatches(t *testing.T) {
	events, now := bgFixture(t, "launch-shell.jsonl")
	b := newBgTracker()
	b.observe(events[:1], now) // poll 1: the tool_use alone
	if n, _ := b.outstanding(now); n != 0 {
		t.Fatalf("outstanding = %d, want 0: a tool_use is not yet a launch", n)
	}
	b.observe(events[1:], now) // poll 2: its result
	if n, _ := b.outstanding(now); n != 1 {
		t.Errorf("outstanding = %d, want 1: the launch must survive the batch boundary", n)
	}
}

// bgShape is a tool_result payload that quotes a launch marker without any
// launch having happened.
type bgShape struct {
	name    string
	content string
}

// bgQuotingShapes builds every shape of quoted launch text that has fooled, or
// could fool, a text-only rule. The two doc fixtures and the lines derived from
// them are read from disk rather than invented here: the previous rounds' hand
// written negatives were written to fit the fix, which let the real shapes
// through.
func bgQuotingShapes(t *testing.T) []bgShape {
	t.Helper()
	specRel := "docs/superpowers/specs/2026-08-11-background-work-state-design.md"
	planRel := "docs/superpowers/plans/2026-08-11-background-work-state.md"
	specPath := repoDocPath(t, specRel)
	spec := bgReadDoc(t, specPath, specRel)
	plan := bgReadDoc(t, repoDocPath(t, planRel), planRel)

	// The spec's own worked examples, verbatim. shellLine is the exact shape
	// that defeated the absolute anchor: `grep` given a single file emits no
	// path prefix, so the quoted sentence lands at byte 0 of the result.
	shellLine, shellLineNo := bgFindLine(t, spec, specRel, "a line beginning with the shell launch sentence", func(s string) bool {
		return strings.HasPrefix(s, "Command running in background with ID:")
	})
	agentLine, agentLineNo := bgFindLine(t, spec, specRel, "a line quoting the async-agent payload", func(s string) bool {
		return strings.Contains(s, "Async agent launched") && strings.Contains(s, "agentId:")
	})
	// In the doc the payload's `\n` is two literal characters; a real
	// tool_result has been through flattenText's json.Unmarshal by the time
	// bgwork sees it. Unescaping gives the shapes below the *decoded* form —
	// the one a per-line anchor cannot tell from the genuine article.
	agentDecoded := strings.ReplaceAll(agentLine, `\n`, "\n")

	return []bgShape{
		{"the design spec, read whole", spec},
		{"the plan doc, read whole", plan},
		{"a bare line quoting the shell example", shellLine},
		{"a pretty-printed agent payload with real newlines",
			"Async agent launched successfully.\nagentId: abc123def\n"},
		{"a fenced code block quoting the agent example unescaped",
			"```\n" + agentDecoded + "\n```\n"},
		{"a terminal transcript containing the agent payload",
			"$ cat /tmp/launch.txt\n" + agentDecoded + "\n$ \n"},
		{"Go source with a raw string literal containing the agent payload",
			"package main\n\nconst payload = `" + agentDecoded + "`\n"},
		{"a log line containing the agent payload",
			"2026-08-11T15:29:59Z INFO harness " + strings.ReplaceAll(agentDecoded, "\n", " ")},
		{"a grep -n style prefixed line quoting the shell example",
			fmt.Sprintf("%s:%d:%s", specPath, shellLineNo, shellLine)},
		{"a grep -n style prefixed line quoting the agent example",
			fmt.Sprintf("%s:%d:%s", specPath, agentLineNo, agentLine)},
	}
}

// bgReadDoc reads a repo doc and refuses to hand back one that no longer
// carries a hazardous shape. Guarding on the bare substrings would be vacuous:
// the prose of the Detection-rules section names both markers, so a doc could
// lose its worked examples — the only text that can actually be mistaken for a
// launch — while a substring guard kept passing.
func bgReadDoc(t *testing.T, path, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	content := string(raw)
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "Command running in background with ID:") {
			return content
		}
		if strings.Contains(line, "Async agent launched") && strings.Contains(line, "agentId:") {
			return content
		}
	}
	t.Fatalf("%s no longer quotes a launch payload in a shape that could be mistaken for one; "+
		"this fixture has stopped testing anything — restore the example or drop the fixture", rel)
	return ""
}

// bgFindLine returns the first line of doc satisfying want, plus its 1-based
// number, failing loudly if the shape is gone rather than testing nothing.
func bgFindLine(t *testing.T, doc, rel, desc string, want func(string) bool) (string, int) {
	t.Helper()
	for i, line := range strings.Split(doc, "\n") {
		if want(line) {
			return line, i + 1
		}
	}
	t.Fatalf("%s no longer contains %s; update this fixture", rel, desc)
	return "", 0
}

// The whole class of false positive, closed at the root: a tool_result's text
// cannot start background work, because the decision is made from the tool that
// produced it. Every shape below is text ABOUT a launch. Under a Read — and
// under a result whose tool_use was never recorded at all — none of them is
// even a near miss; there is simply no launch-capable tool in the picture.
func TestBgLaunchesInertWhenToolCannotLaunch(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	const ts = "2026-08-11T10:00:00Z"
	for _, shape := range bgQuotingShapes(t) {
		t.Run("read/"+shape.name, func(t *testing.T) {
			b := newBgTracker()
			b.observe([]Event{
				bgToolUseEvent("toolu_read", "Read", ts, map[string]interface{}{
					"file_path": "/repo/docs/spec.md",
				}),
				bgResultEvent("toolu_read", shape.content, ts),
			}, now)
			if n, _ := b.outstanding(now); n != 0 {
				t.Errorf("outstanding = %d, want 0: a Read cannot launch anything", n)
			}
		})
		t.Run("unrecorded/"+shape.name, func(t *testing.T) {
			b := newBgTracker()
			b.observe([]Event{bgResultEvent("toolu_never_seen", shape.content, ts)}, now)
			if n, _ := b.outstanding(now); n != 0 {
				t.Errorf("outstanding = %d, want 0: a result with no recorded tool_use is not a launch", n)
			}
		})
	}
}

// The proof that the gate, not the text, is doing the work: text that is inert
// under a Read must still register under a genuine background Bash tool_use.
// The payload here is a grep-prefixed line — a launch sentence that does NOT
// begin at byte 0, which the previous round's absolute anchor rejected outright
// and which a sweep found 42 of 844 real shell results share.
func TestBgLaunchRegistersUnderRealLaunchToolUse(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	const ts = "2026-08-11T10:00:00Z"
	var prefixed string
	for _, shape := range bgQuotingShapes(t) {
		if shape.name == "a grep -n style prefixed line quoting the shell example" {
			prefixed = shape.content
		}
	}
	if prefixed == "" {
		t.Fatal("the prefixed-shell shape went missing from bgQuotingShapes")
	}

	b := newBgTracker()
	b.observe([]Event{
		bgToolUseEvent("toolu_bash", "Bash", ts, map[string]interface{}{
			"command":           "sleep 300",
			"run_in_background": true,
		}),
		bgResultEvent("toolu_bash", prefixed, ts),
	}, now)
	if n, _ := b.outstanding(now); n != 1 {
		t.Fatalf("outstanding = %d, want 1: the same text a Read cannot launch with must launch under a background Bash", n)
	}
	b.observe([]Event{bgDoneEvent("boigiwsir")}, now)
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0: the id from the launch text should retire it", n)
	}
}

// A Bash tool_use without run_in_background is not a launch, whatever its
// output says — that is exactly the grep/sed/cat case that quotes the sentence.
func TestBgForegroundBashDoesNotRegister(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	const ts = "2026-08-11T10:00:00Z"
	b := newBgTracker()
	b.observe([]Event{
		bgToolUseEvent("toolu_grep", "Bash", ts, map[string]interface{}{
			"command": "grep -r 'background with ID' docs/",
		}),
		bgResultEvent("toolu_grep", "Command running in background with ID: boigiwsir. Output is being written to: /tmp/x", ts),
	}, now)
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0: a foreground Bash quoting the sentence launched nothing", n)
	}
}

// The agent tool dispatches foreground agents too, and their result is the
// agent's final report rather than a launch acknowledgement. Tool identity says
// "agent dispatch"; the sentence is what says "and it was an async one".
func TestBgForegroundAgentDoesNotRegister(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	const ts = "2026-08-11T10:00:00Z"
	b := newBgTracker()
	b.observe([]Event{
		bgToolUseEvent("toolu_agent", "Agent", ts, map[string]interface{}{
			"subagent_type": "general-purpose",
			"description":   "Review the diff",
		}),
		bgResultEvent("toolu_agent", "STATUS: DONE\nReviewed the diff; no findings. agentId: notalaunch", ts),
	}, now)
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0: a foreground agent's report is not a launch", n)
	}
}

// A tool_use whose result never arrives — the session was killed mid-tool, or
// the head rotated — must not sit in the pending map forever. It expires on the
// same sweep that expires outstanding tasks, and a late result then finds
// nothing to resolve.
func TestBgTrackerPendingToolUseExpires(t *testing.T) {
	launched := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	const ts = "2026-08-11T10:00:00Z"
	b := newBgTracker()
	b.observe([]Event{bgToolUseEvent("toolu_bash", "Bash", ts, map[string]interface{}{
		"command":           "sleep 300",
		"run_in_background": true,
	})}, launched)

	late := launched.Add(bgMaxAge + time.Minute)
	if n, _ := b.outstanding(late); n != 0 {
		t.Fatalf("outstanding = %d, want 0", n)
	}
	b.observe([]Event{bgResultEvent("toolu_bash", "Command running in background with ID: aaa", late.Format(time.RFC3339))}, late)
	if n, _ := b.outstanding(late); n != 0 {
		t.Errorf("outstanding = %d, want 0: a pending tool_use past the cap must have been dropped", n)
	}
}

func TestBgCompletions(t *testing.T) {
	const payload = "<task-notification>\n<task-id>boigiwsir</task-id>\n" +
		"<tool-use-id>toolu_01VSdCK</tool-use-id>\n<status>completed</status>\n</task-notification>"

	t.Run("queue-operation form", func(t *testing.T) {
		got := bgCompletions(Event{Type: "queue-operation", QueueText: payload})
		if len(got) != 1 || got[0] != "boigiwsir" {
			t.Errorf("bgCompletions = %q, want [boigiwsir]", got)
		}
	})

	t.Run("delivered user turn form", func(t *testing.T) {
		got := bgCompletions(Event{Type: "user", UserText: payload})
		if len(got) != 1 || got[0] != "boigiwsir" {
			t.Errorf("bgCompletions = %q, want [boigiwsir]", got)
		}
	})

	// The literal string appears in ordinary skill documentation. Matching it
	// mid-text would retire tasks that never completed.
	t.Run("prose mentioning the tag is not a completion", func(t *testing.T) {
		prose := "Monitor fires `<task-notification>` messages and wakes this loop. " +
			"See <task-id>boigiwsir</task-id> in the docs."
		if got := bgCompletions(Event{Type: "user", UserText: prose}); len(got) != 0 {
			t.Errorf("bgCompletions = %q, want none: prose is not a notification", got)
		}
	})

	t.Run("notification without a task id", func(t *testing.T) {
		if got := bgCompletions(Event{Type: "user", UserText: "<task-notification>\n<status>failed</status>"}); len(got) != 0 {
			t.Errorf("bgCompletions = %q, want none", got)
		}
	})
}

func TestBgTrackerPairsLaunchAndCompletion(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe(bgShellLaunch("aaa", "2026-08-11T10:00:00Z"), now)
	if n, _ := b.outstanding(now); n != 1 {
		t.Fatalf("outstanding = %d, want 1 after a launch", n)
	}
	b.observe([]Event{bgDoneEvent("aaa")}, now)
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0 after its completion", n)
	}
}

func TestBgTrackerCountsAndOldest(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 10, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe(append(
		bgShellLaunch("aaa", "2026-08-11T10:00:00Z"),
		bgShellLaunch("bbb", "2026-08-11T10:05:00Z")...,
	), now)
	n, oldest := b.outstanding(now)
	if n != 2 {
		t.Errorf("outstanding = %d, want 2", n)
	}
	if want := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC); !oldest.Equal(want) {
		t.Errorf("oldest = %v, want %v: the duration must read as how long work has been out", oldest, want)
	}
	// Retiring the older one moves the clock to the survivor.
	b.observe([]Event{bgDoneEvent("aaa")}, now)
	if _, oldest = b.outstanding(now); !oldest.Equal(time.Date(2026, 8, 11, 10, 5, 0, 0, time.UTC)) {
		t.Errorf("oldest = %v, want the surviving launch", oldest)
	}
}

// A task that never notifies must not mark the session busy forever — that
// would make the conductor refuse to ever visit it.
func TestBgTrackerExpiresStaleLaunches(t *testing.T) {
	b := newBgTracker()
	b.observe(bgShellLaunch("aaa", "2026-08-11T10:00:00Z"), time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC))
	late := time.Date(2026, 8, 11, 10, 31, 0, 0, time.UTC)
	if n, _ := b.outstanding(late); n != 0 {
		t.Errorf("outstanding = %d, want 0: a launch past the cap stops counting", n)
	}
}

// If the human typed at the session, whatever it was tracking is moot.
func TestBgTrackerClearedByGenuinePrompt(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe(bgShellLaunch("aaa", "2026-08-11T10:00:00Z"), now)
	b.observe([]Event{{Type: "user", UserText: "what's up?"}}, now)
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0 after a real prompt", n)
	}
}

// The delivered notification turn is a user event, but it is not the human
// typing — it must not be mistaken for one.
func TestBgTrackerNotificationTurnIsNotAPrompt(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe(append(
		bgShellLaunch("aaa", "2026-08-11T10:00:00Z"),
		bgShellLaunch("bbb", "2026-08-11T10:00:00Z")...,
	), now)
	b.observe([]Event{{Type: "user", UserText: "<task-notification>\n<task-id>aaa</task-id>"}}, now)
	if n, _ := b.outstanding(now); n != 1 {
		t.Errorf("outstanding = %d, want 1: the notification retires its own task, not the set", n)
	}
}

// The spec requires this to be harmless: the same task id can arrive as
// both completion forms (the queue-operation that lands the moment the task
// ends, and the delivered user turn once the session wakes to consume it).
// Retiring it twice must not error or resurrect it.
func TestBgTrackerIdempotentAcrossCompletionForms(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe(bgShellLaunch("aaa", "2026-08-11T10:00:00Z"), now)
	b.observe([]Event{bgDoneEvent("aaa")}, now)                                                                                  // queue-operation form
	b.observe([]Event{{Type: "user", UserText: "<task-notification>\n<task-id>aaa</task-id>\n<status>completed</status>"}}, now) // delivered form, same id
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0: the same id retired by both forms must stay retired", n)
	}
}

// A completion for an id this tracker never launched (e.g. from before a
// head restart) must be a harmless no-op delete, not a crash or a negative
// count.
func TestBgTrackerCompletionForUnknownIDIsHarmless(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe([]Event{bgDoneEvent("neverlaunched")}, now)
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0", n)
	}
}

// The same id launched twice (a retried tool call, say) must not double
// count — the tracker keys on id, so the second launch just re-stamps it.
func TestBgTrackerSameIDLaunchedTwice(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe(bgShellLaunch("aaa", "2026-08-11T10:00:00Z"), now)
	b.observe(bgShellLaunch("aaa", "2026-08-11T10:05:00Z"), now)
	if n, _ := b.outstanding(now); n != 1 {
		t.Errorf("outstanding = %d, want 1: relaunching the same id must not double-count", n)
	}
}
