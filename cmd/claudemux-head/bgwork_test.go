package main

import (
	"encoding/json"
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
// Every event below is built as a transcript LINE and run through parseEvent,
// never assembled as an Event literal. The signal these tests are about — the
// harness's `toolUseResult` — is a sibling of `message` at the top level of the
// line, so a hand-built Event would be testing the test's own idea of the
// payload rather than the parser's. Building lines also keeps the fixtures
// honest against testdata/*.jsonl, which are real transcript excerpts.

// bgToolUseLine renders the assistant turn that calls a tool.
func bgToolUseLine(t *testing.T, toolUseID, name, ts string, input map[string]any) string {
	t.Helper()
	return bgMarshalLine(t, map[string]any{
		"type":      "assistant",
		"timestamp": ts,
		"message": map[string]any{
			"role": "assistant",
			"content": []any{map[string]any{
				"type": "tool_use", "id": toolUseID, "name": name, "input": input,
			}},
		},
	})
}

// bgResultLine renders the user turn carrying a tool_result. toolUseResult is
// written as the top-level sibling the harness really uses; pass nil to omit it
// entirely, which is how a result with no harness record reads.
func bgResultLine(t *testing.T, toolUseID, content, ts string, toolUseResult any) string {
	t.Helper()
	line := map[string]any{
		"type":      "user",
		"timestamp": ts,
		"message": map[string]any{
			"role": "user",
			"content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": toolUseID, "content": content,
			}},
		},
	}
	if toolUseResult != nil {
		line["toolUseResult"] = toolUseResult
	}
	return bgMarshalLine(t, line)
}

func bgMarshalLine(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling a transcript line: %v", err)
	}
	return string(b)
}

// bgParse turns transcript lines into events the way production does.
func bgParse(t *testing.T, lines ...string) []Event {
	t.Helper()
	var events []Event
	for _, line := range lines {
		e, ok := parseEvent(line)
		if !ok {
			t.Fatalf("parseEvent rejected a line it must accept: %s", line)
		}
		events = append(events, e)
	}
	return events
}

// bgShellResult is the harness record on a background shell's tool_result.
func bgShellResult(id string) map[string]any {
	return map[string]any{
		"stdout": "", "stderr": "", "interrupted": false,
		"isImage": false, "noOutputExpected": false, "backgroundTaskId": id,
	}
}

// bgShellLaunch is one complete background-shell launch: the Bash tool_use
// carrying run_in_background, then its acknowledgement — text and harness
// record both, exactly as a real transcript writes them.
func bgShellLaunch(t *testing.T, id, ts string) []Event {
	t.Helper()
	use := "toolu_" + id
	return bgParse(t,
		bgToolUseLine(t, use, "Bash", ts, map[string]any{
			"command": "sleep 300", "run_in_background": true,
		}),
		bgResultLine(t, use, "Command running in background with ID: "+id+
			". Output is being written to: /tmp/x", ts, bgShellResult(id)),
	)
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

// bgAssertFixtureRegisters observes a fixture and proves the launch was tracked
// under wantID — counting something is not evidence the right id was read.
func bgAssertFixtureRegisters(t *testing.T, fixture, wantID string) {
	t.Helper()
	events, now := bgFixture(t, fixture)
	b := newBgTracker()
	b.observe(events, now)
	if n, _ := b.outstanding(now); n != 1 {
		t.Fatalf("outstanding = %d, want 1: a real launch must register", n)
	}
	b.observe([]Event{bgDoneEvent(wantID)}, now)
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0: the launch should have been tracked under %q", n, wantID)
	}
}

// --- launches: the harness's own record --------------------------------------

// The two originally captured launches, one per kind, from claudemux's own
// traffic (not an employer transcript, unlike the three recovered fixtures
// below — see testdata/README.md). DERIVED from real transcript lines the
// same way: harness structure, wording, field names, ids and key order
// untouched; cwd, git branch, doc/session-scoped paths, and every
// session/message/request identifier replaced with neutral placeholders.
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
			bgAssertFixtureRegisters(t, tt.fixture, tt.wantID)
		})
	}
}

// The launches text detection could never see. Each of these is a real shell
// backgrounding that carries `toolUseResult.backgroundTaskId` like any other,
// but whose *tool_use input* has no run_in_background flag and whose *result
// text* either uses a different wording or none the old patterns knew:
//
// Counts below are reachable figures: main-session transcripts only, which is
// all the head ever tails (522 shell launches, 1557 async-agent launches, 2079
// total across 325 files). subagents/*.jsonl sits one directory level too deep
// for its glob, so it never opens those; corpus-wide, including subagent files,
// there are 1915 files, 821 shell launches and 1596 agent launches.
//
//   - auto: Claude Code backgrounded a plain Bash on its own. 3 of the 522
//     reachable shell launches are in this shape (36 of 821 corpus-wide).
//   - timeout: the command overran its timeout and was moved to the background
//     (23 of 522 reachable).
//   - user: the human backgrounded it from the UI (2 of 522 reachable).
//
// Together the three reachable shapes total 28 of the 2079 main-session shell +
// agent launches (522 + 1557) — 1.35%.
//
// PRIVACY: all three shapes exist on this machine only inside the user's
// employer's project transcripts, so these fixtures are DERIVED from real
// lines with content redacted — the harness's structure, wording, field names,
// ids and key order are untouched, while commands, cwd, git branch, project
// slugs and every session/message/request identifier were replaced with neutral
// placeholders. See the report at
// .superpowers/sdd/2026-08-11-background-work-state/harness-signal-report.md.
func TestBgTrackerRegistersRecoveredLaunches(t *testing.T) {
	tests := []struct {
		fixture string
		wantID  string
	}{
		{"launch-shell-auto.jsonl", "bhhcrhd2d"},
		{"launch-shell-timeout.jsonl", "bgk9vrtgb"},
		{"launch-shell-user.jsonl", "b5wz612zv"},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			bgAssertFixtureRegisters(t, tt.fixture, tt.wantID)
		})
	}
}

// The recovered fixtures earn their keep only if they really are the shapes
// text detection missed: no run_in_background in the input. If a future
// recapture quietly replaced them with ordinary flagged launches they would
// still pass above while testing nothing new.
func TestBgRecoveredFixturesCarryNoBackgroundFlag(t *testing.T) {
	for _, name := range []string{"launch-shell-auto.jsonl", "launch-shell-timeout.jsonl", "launch-shell-user.jsonl"} {
		t.Run(name, func(t *testing.T) {
			events, _ := bgFixture(t, name)
			if _, ok := events[0].ToolUses[0].Input["run_in_background"]; ok {
				t.Errorf("%s carries run_in_background; it is no longer an example of a launch the flag cannot reveal", name)
			}
		})
	}
}

// A tool_use is a request, not a launch: nothing is running until the harness
// says so on the result. This also guards the batch boundary — the calling turn
// and its result routinely land in different polls.
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

// bgCarrier is one way a quoted launch payload can reach the head: which tool
// produced the result, and what the harness recorded alongside it.
type bgCarrier struct {
	name string
	// events builds the turns carrying shape as a tool_result payload.
	events func(t *testing.T, shape, ts string) []Event
}

// bgCarriers is every carrier a quoted payload has been seen to arrive under.
// The Agent carrier is the one that matters most here: an agent dispatch is the
// only carrier the previous round could not clear structurally, so it fell back
// to requiring an "Async agent launched" substring — and a foreground Agent
// that merely READ this repo's own spec then reported a phantom launch.
var bgCarriers = []bgCarrier{
	{"read", func(t *testing.T, shape, ts string) []Event {
		return bgParse(t,
			bgToolUseLine(t, "toolu_read", "Read", ts, map[string]any{"file_path": "/repo/docs/spec.md"}),
			bgResultLine(t, "toolu_read", shape, ts, nil),
		)
	}},
	{"unrecorded", func(t *testing.T, shape, ts string) []Event {
		// A result whose tool_use never reached this head at all.
		return bgParse(t, bgResultLine(t, "toolu_never_seen", shape, ts, nil))
	}},
	{"foreground-bash", func(t *testing.T, shape, ts string) []Event {
		// A grep/cat that printed the payload. The harness writes a
		// toolUseResult here too — it simply has no backgroundTaskId.
		return bgParse(t,
			bgToolUseLine(t, "toolu_grep", "Bash", ts, map[string]any{
				"command": "grep -r 'background with ID' docs/",
			}),
			bgResultLine(t, "toolu_grep", shape, ts, map[string]any{
				"stdout": shape, "stderr": "", "interrupted": false,
				"isImage": false, "noOutputExpected": false,
			}),
		)
	}},
	{"agent", func(t *testing.T, shape, ts string) []Event {
		// A FOREGROUND agent whose final report quotes the payload — it read
		// the spec, or reviewed this very diff. The harness records nothing
		// async: real foreground Agent results write a plain string here.
		return bgParse(t,
			bgToolUseLine(t, "toolu_agent", "Agent", ts, map[string]any{
				"subagent_type": "general-purpose", "description": "Review the diff",
			}),
			bgResultLine(t, "toolu_agent", shape, ts, shape),
		)
	}},
}

// The whole class of false positive, closed at the root: a tool_result's text
// cannot start background work, because the decision is made from the harness's
// own record on the result rather than from anything the result says. Every
// shape below is text ABOUT a launch, under every carrier it can arrive on.
func TestBgLaunchesInertUnderEveryCarrier(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	const ts = "2026-08-11T10:00:00Z"
	for _, carrier := range bgCarriers {
		for _, shape := range bgQuotingShapes(t) {
			t.Run(carrier.name+"/"+shape.name, func(t *testing.T) {
				b := newBgTracker()
				b.observe(carrier.events(t, shape.content, ts), now)
				if n, _ := b.outstanding(now); n != 0 {
					t.Errorf("outstanding = %d, want 0: %s carried text about a launch, it did not launch anything", n, carrier.name)
				}
			})
		}
	}
}

// The proof that the harness's field, not the text, is what registers: a result
// whose text quotes one id while the harness records another must track the
// harness's. Under the old text rules this read the quoted id — the wrong one.
func TestBgLaunchIDComesFromHarnessNotText(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	const ts = "2026-08-11T10:00:00Z"
	b := newBgTracker()
	b.observe(bgParse(t,
		bgToolUseLine(t, "toolu_bash", "Bash", ts, map[string]any{
			"command": "sleep 300", "run_in_background": true,
		}),
		// The text names a stale id copied out of a document; the harness
		// names the one that is actually running.
		bgResultLine(t, "toolu_bash", "Command running in background with ID: quotedid. Output is being written to: /tmp/x",
			ts, bgShellResult("realtaskid")),
	), now)
	if n, _ := b.outstanding(now); n != 1 {
		t.Fatalf("outstanding = %d, want 1", n)
	}
	b.observe([]Event{bgDoneEvent("quotedid")}, now)
	if n, _ := b.outstanding(now); n != 1 {
		t.Errorf("outstanding = %d, want 1: the id quoted in the text is not what is running", n)
	}
	b.observe([]Event{bgDoneEvent("realtaskid")}, now)
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0: the harness's own id must be the one tracked", n)
	}
}

// An ordinary Bash result: the harness wrote a toolUseResult, it simply records
// no background task. This is the overwhelmingly common case and it must stay
// silent.
func TestBgOrdinaryBashResultDoesNotRegister(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	const ts = "2026-08-11T10:00:00Z"
	b := newBgTracker()
	b.observe(bgParse(t,
		bgToolUseLine(t, "toolu_bash", "Bash", ts, map[string]any{"command": "ls -la"}),
		bgResultLine(t, "toolu_bash", "total 0\ndrwxr-xr-x  2 michael  staff  64 Aug 11 10:00 .", ts,
			map[string]any{
				"stdout": "total 0", "stderr": "", "interrupted": false,
				"isImage": false, "noOutputExpected": false,
			}),
	), now)
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0: an ordinary shell result launched nothing", n)
	}
}

// toolUseResult is not always an object — plenty of tools write a bare string
// there, and some write an array. A shape the launch decode cannot read must
// leave the event otherwise intact rather than fail parseEvent or drop the
// event: the same line still carries the timestamp and tool_result the rest of
// the head classifies from.
func TestBgNonObjectToolUseResultIsHarmless(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	const ts = "2026-08-11T10:00:00Z"
	shapes := []struct {
		name  string
		value any
	}{
		{"a plain string", "Error: this command is too complex to verify"},
		{"an array of content blocks", []any{map[string]any{"type": "text", "text": "hi"}}},
		{"a number", 42},
		{"null", nil}, // written explicitly below, not omitted
		{"an object whose id field has the wrong type", map[string]any{"backgroundTaskId": 123}},
		{"an object whose isAsync has the wrong type", map[string]any{"isAsync": "yes", "agentId": "a1"}},
		{"an object with an empty id", map[string]any{"backgroundTaskId": ""}},
		{"an agent record that is not async", map[string]any{"isAsync": false, "agentId": "a1"}},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			line := bgMarshalLine(t, map[string]any{
				"type":      "user",
				"timestamp": ts,
				"message": map[string]any{
					"role": "user",
					"content": []any{map[string]any{
						"type": "tool_result", "tool_use_id": "toolu_x", "content": "some output",
					}},
				},
				"toolUseResult": s.value,
			})
			e, ok := parseEvent(line)
			if !ok {
				t.Fatalf("parseEvent dropped the event; a toolUseResult it cannot read must not cost the line")
			}
			if e.Timestamp != ts {
				t.Errorf("Timestamp = %q, want %q: the rest of the event must survive", e.Timestamp, ts)
			}
			if len(e.ToolResults) != 1 || e.ToolResults[0].Content != "some output" {
				t.Errorf("ToolResults = %+v, want the tool_result intact", e.ToolResults)
			}
			b := newBgTracker()
			b.observe([]Event{e}, now)
			if n, _ := b.outstanding(now); n != 0 {
				t.Errorf("outstanding = %d, want 0: an unreadable toolUseResult is not a launch", n)
			}
		})
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
	b.observe(bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"), now)
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
		bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"),
		bgShellLaunch(t, "bbb", "2026-08-11T10:05:00Z")...,
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
	b.observe(bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"), time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC))
	late := time.Date(2026, 8, 11, 10, 31, 0, 0, time.UTC)
	if n, _ := b.outstanding(late); n != 0 {
		t.Errorf("outstanding = %d, want 0: a launch past the cap stops counting", n)
	}
}

// If the human typed at the session, whatever it was tracking is moot.
func TestBgTrackerClearedByGenuinePrompt(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe(bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"), now)
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
		bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"),
		bgShellLaunch(t, "bbb", "2026-08-11T10:00:00Z")...,
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
	b.observe(bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"), now)
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
	b.observe(bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"), now)
	b.observe(bgShellLaunch(t, "aaa", "2026-08-11T10:05:00Z"), now)
	if n, _ := b.outstanding(now); n != 1 {
		t.Errorf("outstanding = %d, want 1: relaunching the same id must not double-count", n)
	}
}
