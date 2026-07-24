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
	IsSidechain bool   // true for subagent (Task) entries — excluded from the worktree chip
	RawLine     string
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
		IsSidechain bool            `json:"isSidechain"`
		Message     json.RawMessage `json:"message"`
		LastPrompt  string          `json:"lastPrompt"` // present on type=last-prompt events
	}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return Event{}, false
	}

	ev := Event{Type: raw.Type, IsMeta: raw.IsMeta, Timestamp: raw.Timestamp, Cwd: raw.Cwd, IsSidechain: raw.IsSidechain, RawLine: line}

	if raw.Type == "last-prompt" && raw.LastPrompt != "" {
		ev.UserText = cleanCommandText(raw.LastPrompt)
	}

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
				ev.ToolResults = append(ev.ToolResults, tr)
			}
		}
	}
}
