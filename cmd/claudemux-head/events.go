package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
)

// commandNameRe and commandArgsRe extract the inner text of the tags Claude Code
// uses when it rewrites a slash-command turn, e.g. `<command-name>/clear</command-name>
// <command-message>clear</command-message> <command-args></command-args>` (tag order
// and indentation vary). Left raw, those tags leak onto the status line, so we
// rebuild the friendly invocation. (RE2 has no backreferences, hence two patterns.)
var (
	commandNameRe = regexp.MustCompile(`(?s)<command-name>(.*?)</command-name>`)
	commandArgsRe = regexp.MustCompile(`(?s)<command-args>(.*?)</command-args>`)
)

// cleanCommandText turns Claude Code's slash-command expansion into the friendly
// invocation (`/name args`). Non-command text is returned unchanged.
func cleanCommandText(s string) string {
	m := commandNameRe.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	name := strings.TrimSpace(m[1])
	if name == "" {
		return s
	}
	if a := commandArgsRe.FindStringSubmatch(s); a != nil {
		if args := strings.TrimSpace(a[1]); args != "" {
			return name + " " + args
		}
	}
	return name
}

// flattenText renders a content field that may be a bare string or an array of
// blocks into one string. Both shapes occur on tool_result: a background shell
// launch returns a string, an async agent launch returns a text block array.
func flattenText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type ToolUse struct {
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

type ToolResult struct {
	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error"`
	Content   string `json:"-"`
}

type Event struct {
	Type        string
	IsMeta      bool
	Timestamp   string
	Model       string
	UserText    string
	ToolUses    []ToolUse
	ToolResults []ToolResult
	Usage       *Usage
	Cwd         string // transcript's per-entry cwd; tracks worktree moves
	// GitBranch is the entry's branch, recorded by Claude Code on every line.
	// Reading it here is what lets the head show a branch without shelling out
	// to git on every poll. Empty when the session's directory is not a repo.
	// A detached HEAD records the literal string "HEAD" — see lastGitBranch.
	GitBranch string
	// QueueText is the top-level `content` of a queue-operation event, which
	// is where a finished background task's notification arrives first — the
	// delivered user turn only follows when the session next runs. Empty for
	// every other event type.
	QueueText string
	// BgTaskID and BgAgentID are the harness's own record that this entry's
	// tool_result STARTED background work — a background shell and an async
	// agent respectively. They come from the top-level `toolUseResult`, a
	// sibling of `message`, so no tool's output can forge one: a command's
	// stdout lands inside `toolUseResult.stdout` and cannot add a key beside
	// it. Empty when the entry is not a launch, which is nearly always.
	BgTaskID  string
	BgAgentID string
	// BgQueuedAgentID is the recipient of a SendMessage the harness only
	// QUEUED — evidence that background work may be running, but not proof it
	// is: the same record comes back for a message queued to something that is
	// not one of this session's agents. It is kept apart from BgAgentID so
	// bgTracker can hold it to a stricter liveness test. See extractLaunch.
	BgQueuedAgentID string
	IsSidechain     bool // true for subagent (Task) entries — excluded from the worktree chip
	RawLine         string
}

type EventReader struct {
	path        string
	offset      int64
	seeded      []Event
	seedErr     error
	firstPrompt string // genuine first user prompt of the session, captured before ring truncation
}

func newEventReader(path string) *EventReader {
	return &EventReader{path: path}
}

func (r *EventReader) SeedFromEnd(maxEvents int) {
	f, err := os.Open(r.path)
	if err != nil {
		r.seedErr = err
		return
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		r.seedErr = err
		return
	}

	// Set offset to position after the last complete newline so any
	// trailing partial line is re-read on the next Tail() once completed.
	lastNL := -1
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == '\n' {
			lastNL = i
			break
		}
	}
	if lastNL == -1 {
		r.offset = 0
	} else {
		r.offset = int64(lastNL + 1)
	}

	events := parseLines(data, true)
	// Capture the session's genuine first prompt from the full parse, before
	// the ring truncates the head away. lastUserPrompt scans newest-first; the
	// first prompt needs an oldest-first scan, which firstUserPrompt does.
	r.firstPrompt = firstUserPrompt(events)
	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}
	r.seeded = events
}

func (r *EventReader) Seeded() ([]Event, error) {
	return r.seeded, r.seedErr
}

// FirstPrompt returns the session's first user prompt, captured at seed time.
func (r *EventReader) FirstPrompt() string {
	return r.firstPrompt
}

// upgradeFirstPrompt lets a genuine prompt displace a slash-command placeholder.
// firstPrompt is captured once at seed, so a session seeded when `/clear` was its
// only prompt would otherwise keep `/clear` forever: the real prompt arrives on a
// later Tail, and firstUserPrompt never runs again. Once a non-command prompt is
// stored it freezes — that one is the session's genuine first prompt.
func (r *EventReader) upgradeFirstPrompt(events []Event) {
	if r.firstPrompt != "" && !strings.HasPrefix(r.firstPrompt, "/") {
		return
	}
	p := firstUserPrompt(events)
	if p == "" {
		return
	}
	if r.firstPrompt == "" || !strings.HasPrefix(p, "/") {
		r.firstPrompt = p
	}
}

func (r *EventReader) Tail() ([]Event, error) {
	f, err := os.Open(r.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if _, err := f.Seek(r.offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	lastNL := -1
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == '\n' {
			lastNL = i
			break
		}
	}
	var consume []byte
	if lastNL == -1 {
		consume = nil
	} else {
		consume = data[:lastNL+1]
		r.offset += int64(lastNL + 1)
	}

	if len(consume) == 0 {
		return nil, nil
	}
	events := parseLines(consume, false)
	r.upgradeFirstPrompt(events)
	return events, nil
}

func parseLines(data []byte, dropPartial bool) []Event {
	var events []Event
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	hasTrailingNL := len(data) > 0 && data[len(data)-1] == '\n'

	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		// Oversized lines (>4 MiB) are dropped here; not fatal — we
		// return whatever we managed to parse so the panel keeps
		// rendering rather than crashing the TUI.
		_ = err
	}
	if dropPartial && !hasTrailingNL && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}

	for _, line := range lines {
		if e, ok := parseEvent(line); ok {
			events = append(events, e)
		}
	}
	return events
}

func parseEvent(line string) (Event, bool) {
	if line == "" {
		return Event{}, false
	}
	var raw struct {
		Type        string          `json:"type"`
		IsMeta      bool            `json:"isMeta"`
		Timestamp   string          `json:"timestamp"`
		Cwd         string          `json:"cwd"`
		GitBranch   string          `json:"gitBranch"`
		IsSidechain bool            `json:"isSidechain"`
		Message     json.RawMessage `json:"message"`
		Content     json.RawMessage `json:"content"`    // top level; queue-operation only
		LastPrompt  string          `json:"lastPrompt"` // present on type=last-prompt events
		// ToolUseResult is the harness's structured record of what a tool
		// actually did, written beside `message` rather than inside it. Raw
		// because its shape varies per tool — see extractLaunch.
		ToolUseResult json.RawMessage `json:"toolUseResult"`
	}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return Event{}, false
	}

	ev := Event{Type: raw.Type, IsMeta: raw.IsMeta, Timestamp: raw.Timestamp, Cwd: raw.Cwd,
		GitBranch: raw.GitBranch, IsSidechain: raw.IsSidechain, RawLine: line}

	if raw.Type == "last-prompt" && raw.LastPrompt != "" {
		ev.UserText = cleanCommandText(raw.LastPrompt)
	}

	if raw.Type == "queue-operation" {
		ev.QueueText = flattenText(raw.Content)
	}

	extractLaunch(&ev, raw.ToolUseResult)

	if len(raw.Message) > 0 {
		var msg struct {
			Model   string          `json:"model"`
			Content json.RawMessage `json:"content"`
			Usage   *Usage          `json:"usage"`
		}
		if err := json.Unmarshal(raw.Message, &msg); err == nil {
			ev.Model = msg.Model
			ev.Usage = msg.Usage
			extractContent(&ev, msg.Content)
		}
	}
	return ev, true
}

// extractLaunch reads the harness's own launch record off a `toolUseResult`.
// Verified against all 1915 transcripts under ~/.claude/projects on this
// machine: `backgroundTaskId` is a non-empty string on all 821 background shell
// launches and nothing else; `isAsync` is a bool that is only ever true, paired
// with a non-empty string `agentId`, on all 1596 async agent launches and
// nothing else. A third shape appeared with Claude Code 2.1.232: a Skill that
// runs as a forked background agent writes `background: true` (with
// `status: "forked"` and the same non-empty `agentId`) and no `isAsync` key at
// all — see testdata/launch-skill-fork.jsonl, captured from a real
// `/code-review high` launch.
//
// `toolUseResult` is frequently NOT an object — 4035 entries write a bare
// string there and 2728 an array — so a failed decode is the normal case, not
// an error. It must leave the event untouched rather than fail parseEvent or
// drop the line: the same entry still carries the tool_result and timestamp the
// rest of the head classifies from.
func extractLaunch(ev *Event, toolUseResult json.RawMessage) {
	if len(toolUseResult) == 0 {
		return
	}
	var res struct {
		BackgroundTaskID string `json:"backgroundTaskId"`
		IsAsync          bool   `json:"isAsync"`
		Background       bool   `json:"background"`
		AgentID          string `json:"agentId"`
		ResumedAgentID   string `json:"resumedAgentId"`
		Success          bool   `json:"success"`
		Pin              struct {
			ID string `json:"id"`
		} `json:"pin"`
	}
	if json.Unmarshal(toolUseResult, &res) != nil {
		return
	}
	ev.BgTaskID = res.BackgroundTaskID
	// The bool is the load-bearing half: the same `Agent` tool dispatches
	// foreground agents (and the same `Skill` tool runs skills inline), and
	// only a launch that ends the main thread's turn counts. Async agents say
	// `isAsync: true`; forked background skills say `background: true`. Every
	// observed record with an agentId sets one of them, so requiring it costs
	// nothing today and keeps a future foreground record — which would have
	// to say false on both — from reading as a launch.
	if res.IsAsync || res.Background {
		ev.BgAgentID = res.AgentID
	}
	// A SendMessage that restarts a STOPPED agent is a launch in every way that
	// matters here: the agent runs in the background, the main thread's turn
	// ends, and the completion comes back as a task-notification under this
	// same id. The harness records it under its own key — `resumedAgentId` —
	// and sets neither isAsync nor background, so it needs its own read. Last,
	// and guarded on non-empty, so it can only ever add an id: a record that
	// somehow carried both keys must not have its async agentId overwritten.
	//
	// The key is written ONLY on a resume: 270 of these across every transcript
	// on this machine, all with success true and a string id. Neither of the
	// other two SendMessage outcomes carries it — a message merely QUEUED to
	// the recipient (below) and an unreachable recipient, whose record is
	// success:false with no pin at all.
	if res.ResumedAgentID != "" {
		ev.BgAgentID = res.ResumedAgentID
	}
	// The queued outcome — "Message queued for delivery to <id> at its next
	// tool round", success:true, a pin echoing the recipient's id, no
	// resumedAgentId. This was read as "nothing started, and whatever is
	// running was already tracked" until a session that had just consumed its
	// agent's completion notification nudged that same agent with a queued
	// message: the agent ran for another twenty minutes while the session,
	// having retired the id on the notification, published Idle.
	//
	// So a queued message is a launch record too — but a weak one, because a
	// pin also comes back for a recipient that is not this session's agent at
	// all. Its id therefore lands in its own field, and bgTracker holds it to
	// the strict liveness rule that distinguishes the two: a running agent has
	// a recently-written transcript, and anything else has no file to find.
	if ev.BgAgentID == "" && res.Success && res.Pin.ID != "" {
		ev.BgQueuedAgentID = res.Pin.ID
	}
}

func extractContent(ev *Event, content json.RawMessage) {
	if len(content) == 0 {
		return
	}
	// Plain string content (e.g. simple user message).
	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		ev.UserText = cleanCommandText(asString)
		return
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(content, &blocks); err != nil {
		return
	}
	for _, raw := range blocks {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			continue
		}
		switch head.Type {
		case "text":
			var t struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(raw, &t); err == nil && ev.UserText == "" {
				ev.UserText = cleanCommandText(t.Text)
			}
		case "tool_use":
			var tu ToolUse
			if err := json.Unmarshal(raw, &tu); err == nil {
				ev.ToolUses = append(ev.ToolUses, tu)
			}
		case "tool_result":
			var tr ToolResult
			if err := json.Unmarshal(raw, &tr); err == nil {
				// Content is tagged `json:"-"` because the payload is not
				// always a string — flatten whichever shape arrived.
				var body struct {
					Content json.RawMessage `json:"content"`
				}
				if json.Unmarshal(raw, &body) == nil {
					tr.Content = flattenText(body.Content)
				}
				ev.ToolResults = append(ev.ToolResults, tr)
			}
		}
	}
}
