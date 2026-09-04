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

// bgAgentResult is the harness record on an async agent launch's tool_result.
func bgAgentResult(id string) map[string]any {
	return map[string]any{"isAsync": true, "agentId": id}
}

// bgResumeResult is the harness record on a SendMessage that RESUMED a dormant
// background agent — the id comes back under resumedAgentId.
func bgResumeResult(id string) map[string]any {
	return map[string]any{
		"success": true, "message": "Resuming agent " + id, "resumedAgentId": id,
		"pin": map[string]any{"id": id, "name": id, "ref": "38e480"},
	}
}

// bgQueuedResult is the harness record on a SendMessage the recipient will
// pick up at its next tool round: a pin echoing the id, and no resumedAgentId.
func bgQueuedResult(id string) map[string]any {
	return map[string]any{
		"success": true,
		"message": "Message queued for delivery to " + id + " at its next tool round.",
		"pin":     map[string]any{"id": id, "name": id, "ref": "e6bfb0"},
	}
}

// bgSendMessageLaunch is one complete SendMessage turn: the tool_use, then its
// acknowledgement carrying whichever harness record `result` describes.
func bgSendMessageLaunch(t *testing.T, id, ts string, result map[string]any) []Event {
	t.Helper()
	use := "toolu_" + id
	return bgParse(t,
		bgToolUseLine(t, use, "SendMessage", ts, map[string]any{
			"to": id, "summary": "Resume the task", "message": "carry on",
		}),
		bgResultLine(t, use, "Resuming agent "+id, ts, result),
	)
}

// bgAgentLaunch is one complete async-agent launch: the Agent tool_use, then
// its acknowledgement carrying the harness's isAsync/agentId record.
func bgAgentLaunch(t *testing.T, id, ts string) []Event {
	t.Helper()
	use := "toolu_" + id
	return bgParse(t,
		bgToolUseLine(t, use, "Agent", ts, map[string]any{
			"description": "test agent", "prompt": "do things",
		}),
		bgResultLine(t, use, "Async agent launched: "+id, ts, bgAgentResult(id)),
	)
}

// bgTouchAgentFile creates/updates the agent's transcript in dir with the
// given mtime, creating the subagents layout the way Claude Code does.
func bgTouchAgentFile(t *testing.T, dir, id string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-"+id+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
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

// Originally captured launches, one per kind, from the user's own personal
// traffic (not an employer transcript, unlike the three recovered fixtures
// below — see testdata/README.md). DERIVED from real transcript lines the
// same way: harness structure, wording, field names, ids and key order
// untouched; cwd, git branch, doc/session-scoped paths, and every
// session/message/request identifier replaced with neutral placeholders.
//
// launch-skill-fork is the third launch kind: a Skill that runs as a forked
// background agent (e.g. `/code-review high`, Claude Code 2.1.232). Its
// record carries no isAsync at all — the harness marks it with
// `background: true` + `status: "forked"` alongside the same `agentId`, and
// the agent writes the same subagents/agent-<id>.jsonl liveness file. Missing
// this shape made claudemux call a session Idle for the entire (often
// hour-long) review the fork was running.
//
// launch-agent-resume is the fourth: a SendMessage that RESUMES an agent that
// had stopped — the way a session picks work back up after the agent was
// killed, or after the harness reports "No completion record was found ...
// from the previous session". The harness runs the resumed agent in the
// background exactly like a fresh async launch, and it notifies under the same
// id, but the record says neither isAsync nor background — the id comes back
// under `resumedAgentId` instead. 270 of these exist in this machine's
// transcripts. Missing the shape is what made a session with a resumed agent
// working for hours publish Idle to the switchboard.
//
// launch-agent-queued is the fifth, and the weakest: a SendMessage the harness
// only queued — "at its next tool round", success, a pin, no resumedAgentId.
// Captured from the session that exposed it, which had already consumed its
// agent's completion notification (so the id was retired) and then nudged that
// same agent back into a twenty-minute run while publishing Idle throughout.
// See TestBgQueuedSendMessageRevivesARetiredAgent for the sequence and
// TestBgQueuedSendMessageWithoutAnAgentTranscriptIsNotALaunch for the guard
// that keeps a pin from a non-agent recipient out.
func TestBgTrackerRegistersRealTranscriptLaunches(t *testing.T) {
	tests := []struct {
		fixture string
		wantID  string
	}{
		{"launch-shell.jsonl", "boigiwsir"},
		{"launch-agent.jsonl", "a99a8221a00c2d373"},
		{"launch-skill-fork.jsonl", "aaba848fe04645123"},
		{"launch-agent-resume.jsonl", "aad446f291008f662"},
		{"launch-agent-queued.jsonl", "aae6f316ac814766f"},
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
		{"an object whose background has the wrong type", map[string]any{"background": "yes", "agentId": "a1"}},
		{"a skill record that is not backgrounded", map[string]any{"background": false, "status": "completed", "agentId": "a1"}},
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

// A typed prompt does NOT retire running work. The old wipe made a session
// with four running agents read Idle the moment the human typed once, and
// the conductor then treated it as waiting. Completions retire tasks;
// liveness/caps expire the stale — the wipe's safety role is gone.
func TestBgTrackerSurvivesGenuinePrompt(t *testing.T) {
	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe(bgShellLaunch(t, "aaa", "2026-08-13T10:00:00Z"), now)
	b.observe([]Event{{Type: "user", UserText: "what's up?"}}, now)
	if n, _ := b.outstanding(now); n != 1 {
		t.Errorf("outstanding = %d, want 1: typing must not erase running work", n)
	}
}

// A completion notification retires only the task id it names, not every
// outstanding task.
func TestBgTrackerNotificationRetiresOnlyItsOwnTask(t *testing.T) {
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

// A running agent's transcript keeps advancing; while it does, the launch
// must keep counting far past the old 30-minute cliff.
func TestBgAgentAliveFilePastOldCap(t *testing.T) {
	launch := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	now := launch.Add(2 * time.Hour)
	b := newBgTracker()
	b.subagentsDir = t.TempDir()
	b.observe(bgAgentLaunch(t, "agentaaa", "2026-08-13T10:00:00Z"), launch)
	bgTouchAgentFile(t, b.subagentsDir, "agentaaa", now.Add(-1*time.Minute))
	if n, _ := b.outstanding(now); n != 1 {
		t.Errorf("outstanding = %d, want 1: a live agent must count past 30m", n)
	}
}

// A forked-skill launch is an agent, not a shell: it writes the same
// subagents/agent-<id>.jsonl file, so it must follow the liveness regime and
// keep counting past the shell cap while that file advances. (The observed
// fork ran a multi-hour /code-review; the shell cap would have called its
// session Idle 30 minutes in.)
func TestBgSkillForkFollowsAgentLiveness(t *testing.T) {
	events, launch := bgFixture(t, "launch-skill-fork.jsonl")
	now := launch.Add(2 * time.Hour)
	b := newBgTracker()
	b.subagentsDir = t.TempDir()
	b.observe(events, launch)
	bgTouchAgentFile(t, b.subagentsDir, "aaba848fe04645123", now.Add(-1*time.Minute))
	if n, _ := b.outstanding(now); n != 1 {
		t.Errorf("outstanding = %d, want 1: a live forked skill must count past 30m", n)
	}
}

// An agent whose transcript stopped advancing died without notifying; it must
// stop counting after the stall threshold so the session isn't hidden forever.
func TestBgAgentStalledFileExpires(t *testing.T) {
	launch := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	now := launch.Add(2 * time.Hour)
	b := newBgTracker()
	b.subagentsDir = t.TempDir()
	b.observe(bgAgentLaunch(t, "agentbbb", "2026-08-13T10:00:00Z"), launch)
	bgTouchAgentFile(t, b.subagentsDir, "agentbbb", now.Add(-bgAgentStallAge-time.Minute))
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0: a stalled agent transcript means the agent is gone", n)
	}
}

// No transcript file yet: normal for a just-spawned agent (grace), stale for
// anything older — a seeded pre-restart launch whose agent is long gone.
func TestBgAgentMissingFileGraceThenDrop(t *testing.T) {
	launch := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.subagentsDir = t.TempDir()
	b.observe(bgAgentLaunch(t, "agentccc", "2026-08-13T10:00:00Z"), launch)
	if n, _ := b.outstanding(launch.Add(1 * time.Minute)); n != 1 {
		t.Errorf("within spawn grace: outstanding = %d, want 1", n)
	}
	if n, _ := b.outstanding(launch.Add(bgAgentSpawnGrace + time.Minute)); n != 0 {
		t.Errorf("past spawn grace with no file: outstanding = %d, want 0", n)
	}
}

// With no subagentsDir configured there is no liveness source; agents must
// fall back to the shell cap rather than counting forever.
func TestBgAgentNoLivenessDirFallsBackToShellCap(t *testing.T) {
	launch := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe(bgAgentLaunch(t, "agentddd", "2026-08-13T10:00:00Z"), launch)
	if n, _ := b.outstanding(launch.Add(bgShellMaxAge + time.Minute)); n != 0 {
		t.Errorf("outstanding = %d, want 0: no liveness dir means the old cap applies", n)
	}
}

// The hard cap backstops a file that keeps advancing forever (e.g. a wedged
// agent looping): even alive-looking agents stop counting after bgAgentMaxAge.
func TestBgAgentHardCap(t *testing.T) {
	launch := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	now := launch.Add(bgAgentMaxAge + time.Hour)
	b := newBgTracker()
	b.subagentsDir = t.TempDir()
	b.observe(bgAgentLaunch(t, "agenteee", "2026-08-12T10:00:00Z"), launch)
	bgTouchAgentFile(t, b.subagentsDir, "agenteee", now.Add(-1*time.Minute))
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0: the hard cap must win over liveness", n)
	}
}

// A resumed agent is an agent: it writes the same subagents/agent-<id>.jsonl
// and runs for hours, so it must follow the liveness regime rather than the
// 30-minute shell cap. Registering the resume but capping it like a shell
// would still leave the session reading Idle for most of the agent's life.
func TestBgResumedAgentFollowsAgentLiveness(t *testing.T) {
	launch := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	now := launch.Add(2 * time.Hour)
	b := newBgTracker()
	b.subagentsDir = t.TempDir()
	b.observe(bgSendMessageLaunch(t, "agentres", "2026-08-17T10:00:00Z", bgResumeResult("agentres")), launch)
	bgTouchAgentFile(t, b.subagentsDir, "agentres", now.Add(-1*time.Minute))
	if n, _ := b.outstanding(now); n != 1 {
		t.Errorf("outstanding = %d, want 1: a resumed agent must count past 30m while its transcript advances", n)
	}
}

// The other SendMessage outcome: the harness only QUEUES the message, for the
// recipient's next tool round. This was read as "nothing started", on the
// reasoning that an agent taking delivery must already be running and so
// already tracked. The session below is why that reasoning fails — it is the
// boats-work sequence that made claudemux publish Idle while an agent worked
// on for twenty minutes:
//
//	launch (isAsync)            -> tracked
//	task-notification completed -> RETIRED
//	SendMessage, queued         -> the agent runs again, under no launch record
//
// Nothing else in the transcript marks that third step, so the queued record
// has to count. The agent's own transcript is what says it is real.
func TestBgQueuedSendMessageRevivesARetiredAgent(t *testing.T) {
	launch := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.subagentsDir = t.TempDir()
	b.observe(bgAgentLaunch(t, "agentrev", "2026-08-17T10:00:00Z"), launch)
	b.observe([]Event{bgDoneEvent("agentrev")}, launch.Add(5*time.Minute))
	if n, _ := b.outstanding(launch.Add(5 * time.Minute)); n != 0 {
		t.Fatalf("outstanding = %d, want 0: the completion notification must retire the launch", n)
	}

	nudge := launch.Add(6 * time.Minute)
	// The agent's own transcript already exists from its first (completed)
	// run — realistically it is on disk, with an mtime from that run, before
	// the queued acknowledgment is ever observed. Without this, observe()
	// would (correctly, per the "history, not news" guard) see no liveness
	// file at all for a queued-but-not-yet-tracked id and decline to track
	// it — which is exactly right for a pin to a non-agent recipient, but
	// wrong here, where the recipient IS this session's own agent.
	bgTouchAgentFile(t, b.subagentsDir, "agentrev", launch.Add(4*time.Minute))
	b.observe(bgSendMessageLaunch(t, "agentrev", "2026-08-17T10:06:00Z", bgQueuedResult("agentrev")), nudge)
	bgTouchAgentFile(t, b.subagentsDir, "agentrev", nudge)
	now := nudge.Add(20 * time.Minute)
	bgTouchAgentFile(t, b.subagentsDir, "agentrev", now.Add(-1*time.Minute))
	if n, _ := b.outstanding(now); n != 1 {
		t.Errorf("outstanding = %d, want 1: a queued nudge set the agent running again", n)
	}
}

// The same record comes back for a message queued to something that is not one
// of this session's agents at all — another session, a teammate — and that
// starts no background work here. There is no launch to grant a spawn grace
// to, so the absence of an agent transcript settles it immediately rather than
// hiding the session from the conductor for two minutes.
func TestBgQueuedSendMessageWithoutAnAgentTranscriptIsNotALaunch(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.subagentsDir = t.TempDir()
	b.observe(bgSendMessageLaunch(t, "not-an-agent", "2026-08-17T10:00:00Z", bgQueuedResult("not-an-agent")), now)
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0: nothing this session launched writes agent-not-an-agent.jsonl", n)
	}
}

// A queued message to work already being counted must not restart the clock:
// the switchboard shows the Background age from the oldest outstanding launch,
// and a session that nudges a long-running agent every few minutes would
// otherwise always look freshly busy.
func TestBgQueuedSendMessageKeepsTheOriginalLaunchTime(t *testing.T) {
	launch := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.subagentsDir = t.TempDir()
	b.observe(bgAgentLaunch(t, "agentbusy", "2026-08-17T10:00:00Z"), launch)
	nudge := launch.Add(30 * time.Minute)
	b.observe(bgSendMessageLaunch(t, "agentbusy", "2026-08-17T10:30:00Z", bgQueuedResult("agentbusy")), nudge)
	bgTouchAgentFile(t, b.subagentsDir, "agentbusy", nudge)
	n, oldest := b.outstanding(nudge)
	if n != 1 {
		t.Fatalf("outstanding = %d, want 1", n)
	}
	if !oldest.Equal(launch) {
		t.Errorf("oldest = %v, want %v: the nudge is not a new piece of work", oldest, launch)
	}
}

// A named fork's task id is not alphanumeric — the harness builds it from the
// prompt, e.g. `awhat-is-apiwebhookscallr-53690e0dfb7cf9f8`. An id-charset the
// notification parser cannot express means that agent's completion never
// retires it, and the session stays Background until an expiry timer catches
// it minutes later.
func TestBgCompletionAcceptsNonAlphanumericTaskID(t *testing.T) {
	const id = "awhat-is-apiwebhookscallr-53690e0dfb7cf9f8"
	got := bgCompletions(Event{Type: "queue-operation",
		QueueText: "<task-notification>\n<task-id>" + id + "</task-id>\n<status>completed</status>\n</task-notification>"})
	if len(got) != 1 || got[0] != id {
		t.Fatalf("bgCompletions = %q, want [%s]", got, id)
	}
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.subagentsDir = t.TempDir()
	b.observe(bgSendMessageLaunch(t, id, "2026-08-17T10:00:00Z", bgResumeResult(id)), now)
	if n, _ := b.outstanding(now); n != 1 {
		t.Fatalf("outstanding = %d, want 1", n)
	}
	b.observe([]Event{bgDoneEvent(id)}, now)
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0: a fork's own completion must retire it", n)
	}
}

func TestSubagentsDirFor(t *testing.T) {
	got := subagentsDirFor("/home/u/.claude/projects/-p/abc-123.jsonl")
	want := filepath.Join("/home/u/.claude/projects/-p", "abc-123", "subagents")
	if got != want {
		t.Errorf("subagentsDirFor = %q, want %q", got, want)
	}
}

// A session that ENTERS A WORKTREE keeps its id but gets its transcript moved
// to a different project directory, so the head re-binds through moveSession.
// The bg tracker's liveness source is derived from the transcript path, so a
// move that leaves it pointing at the old directory makes every later agent
// launch stat a file that will never exist: the agent expires via
// bgAgentSpawnGrace ~2 minutes in and the session reads Idle while its agent
// is still running. Observed live on 2026-08-20 — a 45-minute background agent
// published Background for two minutes, then Idle for the remaining 43.
func TestBgTrackerFollowsMovedTranscript(t *testing.T) {
	root := t.TempDir()
	sid := "5639283b-9222-46d8-8052-fc5415fc9884"
	oldPath := filepath.Join(root, "old", sid+".jsonl")
	newPath := filepath.Join(root, "new", sid+".jsonl")
	for _, p := range []string{oldPath, newPath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(`{"type":"assistant","timestamp":"2026-08-20T13:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	launch := time.Date(2026, 8, 20, 13, 34, 13, 0, time.UTC)
	m := model{bg: newBgTracker(), jsonlPath: oldPath, sessionID: sid}
	m.bg.subagentsDir = subagentsDirFor(oldPath)

	// The worktree move: same session id, new path.
	m.moveSession(newPath, launch)

	// A launch that arrives AFTER the move, by incremental tail.
	m.bg.observe(bgAgentLaunch(t, "a48070ee992d02136", "2026-08-20T13:34:13Z"), launch)

	now := launch.Add(5 * time.Minute)
	bgTouchAgentFile(t, subagentsDirFor(newPath), "a48070ee992d02136", now.Add(-10*time.Second))
	m.recomputeFromEvents(now)

	if m.state.Kind != StateBackground {
		t.Errorf("state = %v, want StateBackground: a live agent must keep counting after the transcript moves", m.state.Kind)
	}
}

// An expiry is a guess, not a fact: the tracker stops counting the task but
// remembers that it gave up, so the head can publish doubt instead of a
// confident Idle. This is the case that sent the conductor into ag-admin on
// 2026-09-04 — a hung ssh past the 30-minute shell cap.
func TestBgTrackerRemembersExpiry(t *testing.T) {
	b := newBgTracker()
	b.observe(bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"), time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC))
	late := time.Date(2026, 8, 11, 10, 31, 0, 0, time.UTC)
	if n, _ := b.outstanding(late); n != 0 {
		t.Fatalf("outstanding = %d, want 0", n)
	}
	if got := b.unsure(); got != 1 {
		t.Errorf("unsure = %d, want 1: an expiry must be remembered", got)
	}
	// Still remembered on the next poll — outstanding is called every tick.
	if n, _ := b.outstanding(late.Add(time.Second)); n != 0 {
		t.Fatalf("outstanding = %d, want 0", n)
	}
	if got := b.unsure(); got != 1 {
		t.Errorf("unsure = %d after second poll, want 1", got)
	}
}

// A completion is a fact: retiring a task through its notification leaves
// no doubt behind.
func TestBgTrackerCompletionLeavesNoDoubt(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe(bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"), now)
	b.observe([]Event{{Type: "user", Timestamp: "2026-08-11T10:05:00Z",
		UserText: "<task-notification>\n<task-id>aaa</task-id>\n<status>completed</status>\n</task-notification>"}}, now)
	if n, _ := b.outstanding(now.Add(6 * time.Minute)); n != 0 {
		t.Fatalf("outstanding = %d, want 0", n)
	}
	if got := b.unsure(); got != 0 {
		t.Errorf("unsure = %d, want 0: a completion is not a guess", got)
	}
}

// Doubt clears the moment the conversation moves on: a user or assistant
// event newer than the expiry proves the human (or Claude) engaged, and a
// later Stop is then a real one. Bookkeeping events do not count — the
// harness writes attachments and snapshots without anyone present.
func TestBgTrackerDoubtClearsOnNewTurn(t *testing.T) {
	b := newBgTracker()
	b.observe(bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"), time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC))
	late := time.Date(2026, 8, 11, 10, 31, 0, 0, time.UTC)
	b.outstanding(late)
	if got := b.unsure(); got != 1 {
		t.Fatalf("unsure = %d, want 1", got)
	}
	b.observe([]Event{{Type: "attachment", Timestamp: "2026-08-11T10:32:00Z"}}, late.Add(time.Minute))
	if got := b.unsure(); got != 1 {
		t.Errorf("unsure = %d after bookkeeping event, want 1", got)
	}
	b.observe([]Event{{Type: "user", Timestamp: "2026-08-11T10:33:00Z", UserText: "how's it going?"}}, late.Add(2*time.Minute))
	if got := b.unsure(); got != 0 {
		t.Errorf("unsure = %d after a new user turn, want 0", got)
	}
}

// An event OLDER than the expiry cannot clear it — a reseed replays history
// that predates the drop.
func TestBgTrackerDoubtIgnoresOlderTurns(t *testing.T) {
	b := newBgTracker()
	b.observe(bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z"), time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC))
	late := time.Date(2026, 8, 11, 10, 31, 0, 0, time.UTC)
	b.outstanding(late)
	b.observe([]Event{{Type: "assistant", Timestamp: "2026-08-11T10:01:00Z", UserText: "old text"}}, late.Add(time.Second))
	if got := b.unsure(); got != 1 {
		t.Errorf("unsure = %d, want 1: an older turn does not resolve a later expiry", got)
	}
}

// A launch already dead the first time the tracker ever sees it is history,
// not news: newModel/switchSession seed up to 500 events on every head start
// or rotation, and a backgrounded shell in that window that never notified
// (a dev server, a killed process) is already well past bgShellMaxAge by the
// time it is observed. Before the fix, observe() added it anyway, and the
// very next outstanding() call expired it with expiredAt == now — newer than
// every replayed event — so unsure() could never clear from history and the
// session was stuck publishing Unsure:1 until a human typed. It must never be
// tracked in the first place: never counted, so never doubted.
func TestBgTrackerSeededDeadLaunchIsHistoryNotDoubt(t *testing.T) {
	now := time.Date(2026, 8, 20, 18, 0, 0, 0, time.UTC)
	launchTS := now.Add(-8 * time.Hour)
	events := bgShellLaunch(t, "aaa", launchTS.Format(time.RFC3339))
	events = append(events, Event{
		Type: "assistant", Timestamp: now.Add(-time.Minute).Format(time.RFC3339), UserText: "wrapped up",
	})
	b := newBgTracker()
	b.observe(events, now)
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0: an already-dead seeded launch must not be tracked", n)
	}
	if got := b.unsure(); got != 0 {
		t.Errorf("unsure = %d, want 0: a launch the head never once saw alive was never counted, so it cannot be doubted", got)
	}
}

// The seeding-recovery counterpart: a launch still inside its liveness
// window at seed time IS tracked, which is what lets newModel/switchSession
// recover a session's real Background state across a head restart or
// rotation (see the seeding comment in tui.go).
func TestBgTrackerSeededLiveLaunchIsTracked(t *testing.T) {
	launchTS := time.Date(2026, 8, 20, 18, 0, 0, 0, time.UTC)
	now := launchTS.Add(5 * time.Minute)
	b := newBgTracker()
	b.observe(bgShellLaunch(t, "aaa", launchTS.Format(time.RFC3339)), now)
	if n, _ := b.outstanding(now); n != 1 {
		t.Errorf("outstanding = %d, want 1: a launch still inside its window at seed time must be tracked", n)
	}
	if got := b.unsure(); got != 0 {
		t.Errorf("unsure = %d, want 0", got)
	}
}

// A reseed onto the SAME tracker (moveSession) must not resurrect cleared
// doubt or double-count: before the fix 1 guard, re-observing an expired
// launch's original events after the doubt had already cleared re-added the
// dead task, and the next outstanding() call expired it again — unsure()
// went 1 -> 0 -> 1 across reseeds, and would go to 2 on a third.
func TestBgTrackerReseedAfterClearDoesNotDoubleCount(t *testing.T) {
	b := newBgTracker()
	launchEvents := bgShellLaunch(t, "aaa", "2026-08-11T10:00:00Z")
	b.observe(launchEvents, time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC))

	late := time.Date(2026, 8, 11, 10, 31, 0, 0, time.UTC)
	if n, _ := b.outstanding(late); n != 0 {
		t.Fatalf("outstanding = %d, want 0 after expiry", n)
	}
	if got := b.unsure(); got != 1 {
		t.Fatalf("unsure = %d, want 1 after expiry", got)
	}

	// A newer conversation turn clears the doubt.
	clearAt := late.Add(time.Minute)
	b.observe([]Event{{Type: "user", Timestamp: "2026-08-11T10:32:00Z", UserText: "still there?"}}, clearAt)
	if got := b.unsure(); got != 0 {
		t.Fatalf("unsure = %d, want 0 after a newer turn clears it", got)
	}

	// A reseed (e.g. moveSession) replays the ORIGINAL launch events again,
	// with today's now.
	b.observe(launchEvents, clearAt)
	if n, _ := b.outstanding(clearAt); n != 0 {
		t.Errorf("outstanding = %d, want 0: a reseeded, already-dead launch must not be re-tracked", n)
	}
	if got := b.unsure(); got != 0 {
		t.Errorf("unsure = %d, want 0: the reseed must not resurrect cleared doubt", got)
	}
}
