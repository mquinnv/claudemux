# plan-monitor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a live-updating TUI that displays the active Claude Code plan and tasks for the session in the current working directory.

**Architecture:** Go CLI using bubbletea for the TUI event loop, lipgloss for styling, and glamour for markdown rendering. Data is read from `~/.claude/tasks/<session-id>/*.json` and `~/.claude/plans/*.md` with 1-second polling. Session is auto-detected from cwd or overridden via `--session` flag.

**Tech Stack:** Go, bubbletea, lipgloss, glamour

---

## File Structure

```
plan-monitor/
├── main.go              # entry point, flag parsing, orchestration
├── session.go           # path encoding, sessions-index parsing, session selection
├── session_test.go      # tests for session discovery
├── tasks.go             # read and parse task JSON files from disk
├── tasks_test.go        # tests for task reading
├── plan.go              # plan discovery from JSONL + mtime fallback
├── plan_test.go         # tests for plan discovery
├── tui.go               # bubbletea model, update, view
├── go.mod
└── go.sum
```

---

### Task 1: Project Scaffolding

**Files:**
- Create: `main.go`
- Create: `go.mod`

- [ ] **Step 1: Install Go via Homebrew**

```bash
brew install go
```

Expected: Go 1.22+ installed.

- [ ] **Step 2: Initialize Go module**

```bash
cd /Users/michael/Projects/plan-monitor
go mod init plan-monitor
```

- [ ] **Step 3: Add dependencies**

```bash
go get github.com/charmbracelet/bubbletea@latest
go get github.com/charmbracelet/lipgloss@latest
go get github.com/charmbracelet/glamour@latest
```

- [ ] **Step 4: Create minimal main.go**

```go
package main

import "fmt"

func main() {
	fmt.Println("plan-monitor")
}
```

- [ ] **Step 5: Verify it compiles and runs**

```bash
go run .
```

Expected: prints `plan-monitor`

- [ ] **Step 6: Commit**

```bash
git add main.go go.mod go.sum
git commit -m "chore: scaffold Go project with dependencies"
```

---

### Task 2: Session Discovery

**Files:**
- Create: `session.go`
- Create: `session_test.go`

- [ ] **Step 1: Write failing tests for path encoding**

`session_test.go`:

```go
package main

import (
	"testing"
)

func TestEncodeProjectPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/Users/michael/Projects/foo", "-Users-michael-Projects-foo"},
		{"/Users/michael/Projects/plan-monitor", "-Users-michael-Projects-plan-monitor"},
		{"/home/user/code", "-home-user-code"},
	}
	for _, tt := range tests {
		got := encodeProjectPath(tt.input)
		if got != tt.want {
			t.Errorf("encodeProjectPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -run TestEncodeProjectPath -v
```

Expected: FAIL — `encodeProjectPath` undefined.

- [ ] **Step 3: Implement encodeProjectPath**

`session.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func encodeProjectPath(absPath string) string {
	return strings.ReplaceAll(absPath, "/", "-")
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test -run TestEncodeProjectPath -v
```

Expected: PASS

- [ ] **Step 5: Write failing test for session index parsing**

Add to `session_test.go`:

```go
func TestParseSessionsIndex(t *testing.T) {
	data := []byte(`{
		"version": 1,
		"entries": [
			{
				"sessionId": "aaa-111",
				"projectPath": "/Users/test/project",
				"modified": "2026-04-18T10:00:00.000Z",
				"firstPrompt": "hello",
				"messageCount": 5
			},
			{
				"sessionId": "bbb-222",
				"projectPath": "/Users/test/project",
				"modified": "2026-04-18T12:00:00.000Z",
				"firstPrompt": "world",
				"messageCount": 10
			}
		]
	}`)

	entries, err := parseSessionsIndex(data)
	if err != nil {
		t.Fatalf("parseSessionsIndex returned error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].SessionID != "aaa-111" {
		t.Errorf("first entry sessionId = %q, want %q", entries[0].SessionID, "aaa-111")
	}
}

func TestFindMostRecentSession(t *testing.T) {
	entries := []sessionEntry{
		{SessionID: "older", Modified: "2026-04-18T10:00:00.000Z"},
		{SessionID: "newer", Modified: "2026-04-18T12:00:00.000Z"},
	}
	got := findMostRecentSession(entries)
	if got != "newer" {
		t.Errorf("findMostRecentSession = %q, want %q", got, "newer")
	}
}
```

- [ ] **Step 6: Run test to verify it fails**

```bash
go test -run "TestParseSessionsIndex|TestFindMostRecentSession" -v
```

Expected: FAIL — types and functions undefined.

- [ ] **Step 7: Implement session index parsing and selection**

Add to `session.go`:

```go
type sessionEntry struct {
	SessionID    string `json:"sessionId"`
	ProjectPath  string `json:"projectPath"`
	Modified     string `json:"modified"`
	FirstPrompt  string `json:"firstPrompt"`
	MessageCount int    `json:"messageCount"`
	GitBranch    string `json:"gitBranch"`
}

type sessionsIndex struct {
	Version int            `json:"version"`
	Entries []sessionEntry `json:"entries"`
}

func parseSessionsIndex(data []byte) ([]sessionEntry, error) {
	var idx sessionsIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parsing sessions index: %w", err)
	}
	return idx.Entries, nil
}

func findMostRecentSession(entries []sessionEntry) string {
	if len(entries) == 0 {
		return ""
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Modified > entries[j].Modified
	})
	return entries[0].SessionID
}
```

- [ ] **Step 8: Run tests to verify they pass**

```bash
go test -run "TestEncodeProjectPath|TestParseSessionsIndex|TestFindMostRecentSession" -v
```

Expected: all PASS

- [ ] **Step 9: Write failing test for resolveSession (integration-style)**

Add to `session_test.go`:

```go
func TestResolveSession(t *testing.T) {
	// Create temp dir structure mimicking ~/.claude/projects/<encoded>/
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "-test-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	indexData := `{
		"version": 1,
		"entries": [
			{"sessionId": "sess-abc", "modified": "2026-04-18T14:00:00.000Z"}
		]
	}`
	if err := os.WriteFile(filepath.Join(projectDir, "sessions-index.json"), []byte(indexData), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveSession(tmpDir, "/test/project", "")
	if err != nil {
		t.Fatalf("resolveSession error: %v", err)
	}
	if got != "sess-abc" {
		t.Errorf("resolveSession = %q, want %q", got, "sess-abc")
	}
}

func TestResolveSessionExplicitOverride(t *testing.T) {
	got, err := resolveSession("", "", "explicit-id")
	if err != nil {
		t.Fatalf("resolveSession error: %v", err)
	}
	if got != "explicit-id" {
		t.Errorf("resolveSession = %q, want %q", got, "explicit-id")
	}
}
```

- [ ] **Step 10: Run tests to verify failure**

```bash
go test -run "TestResolveSession" -v
```

Expected: FAIL — `resolveSession` undefined.

- [ ] **Step 11: Implement resolveSession**

Add to `session.go`:

```go
func resolveSession(claudeProjectsDir string, cwd string, explicitSession string) (string, error) {
	if explicitSession != "" {
		return explicitSession, nil
	}

	encoded := encodeProjectPath(cwd)
	indexPath := filepath.Join(claudeProjectsDir, encoded, "sessions-index.json")

	data, err := os.ReadFile(indexPath)
	if err != nil {
		return "", fmt.Errorf("reading sessions index at %s: %w", indexPath, err)
	}

	entries, err := parseSessionsIndex(data)
	if err != nil {
		return "", err
	}

	sessionID := findMostRecentSession(entries)
	if sessionID == "" {
		return "", fmt.Errorf("no sessions found for project %s", cwd)
	}

	return sessionID, nil
}
```

- [ ] **Step 12: Run all session tests**

```bash
go test -run "TestEncodeProjectPath|TestParseSessionsIndex|TestFindMostRecentSession|TestResolveSession" -v
```

Expected: all PASS

- [ ] **Step 13: Commit**

```bash
git add session.go session_test.go
git commit -m "feat: add session discovery with path encoding and index parsing"
```

---

### Task 3: Task File Reading

**Files:**
- Create: `tasks.go`
- Create: `tasks_test.go`

- [ ] **Step 1: Write failing test for task parsing**

`tasks_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadTasks(t *testing.T) {
	tmpDir := t.TempDir()

	// Write task files
	task1 := `{"id":"1","subject":"Do thing","description":"desc","status":"completed","blocks":[],"blockedBy":[]}`
	task2 := `{"id":"2","subject":"Other thing","description":"desc2","activeForm":"Doing other thing","status":"in_progress","blocks":[],"blockedBy":[]}`
	task3 := `{"id":"3","subject":"Future thing","description":"desc3","status":"pending","blocks":[],"blockedBy":[]}`

	os.WriteFile(filepath.Join(tmpDir, "1.json"), []byte(task1), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "2.json"), []byte(task2), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "3.json"), []byte(task3), 0o644)
	// Also write a .lock file that should be ignored
	os.WriteFile(filepath.Join(tmpDir, ".lock"), []byte(""), 0o644)

	tasks, err := readTasks(tmpDir)
	if err != nil {
		t.Fatalf("readTasks error: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}

	// Should be sorted by ID
	if tasks[0].ID != "1" || tasks[1].ID != "2" || tasks[2].ID != "3" {
		t.Errorf("tasks not sorted by ID: got %s, %s, %s", tasks[0].ID, tasks[1].ID, tasks[2].ID)
	}
	if tasks[1].ActiveForm != "Doing other thing" {
		t.Errorf("task 2 activeForm = %q, want %q", tasks[1].ActiveForm, "Doing other thing")
	}
}

func TestReadTasksEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	tasks, err := readTasks(tmpDir)
	if err != nil {
		t.Fatalf("readTasks error: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(tasks))
	}
}

func TestReadTasksMissingDir(t *testing.T) {
	tasks, err := readTasks("/nonexistent/path")
	if err != nil {
		t.Fatalf("readTasks should not error on missing dir, got: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(tasks))
	}
}

func TestTaskCounts(t *testing.T) {
	tasks := []task{
		{ID: "1", Status: "completed"},
		{ID: "2", Status: "in_progress"},
		{ID: "3", Status: "pending"},
		{ID: "4", Status: "completed"},
	}
	completed, total := taskCounts(tasks)
	if completed != 2 || total != 4 {
		t.Errorf("taskCounts = (%d, %d), want (2, 4)", completed, total)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

```bash
go test -run "TestReadTasks|TestTaskCounts" -v
```

Expected: FAIL — types and functions undefined.

- [ ] **Step 3: Implement task reading**

`tasks.go`:

```go
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type task struct {
	ID          string   `json:"id"`
	Subject     string   `json:"subject"`
	Description string   `json:"description"`
	ActiveForm  string   `json:"activeForm,omitempty"`
	Status      string   `json:"status"`
	Blocks      []string `json:"blocks"`
	BlockedBy   []string `json:"blockedBy"`
}

func readTasks(dir string) ([]task, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading tasks dir: %w", err)
	}

	var tasks []task
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue // file may have been removed between readdir and read
		}

		var t task
		if err := json.Unmarshal(data, &t); err != nil {
			continue
		}
		tasks = append(tasks, t)
	}

	sort.Slice(tasks, func(i, j int) bool {
		a, _ := strconv.Atoi(tasks[i].ID)
		b, _ := strconv.Atoi(tasks[j].ID)
		return a < b
	})

	return tasks, nil
}

func taskCounts(tasks []task) (completed, total int) {
	total = len(tasks)
	for _, t := range tasks {
		if t.Status == "completed" {
			completed++
		}
	}
	return
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test -run "TestReadTasks|TestTaskCounts" -v
```

Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add tasks.go tasks_test.go
git commit -m "feat: add task file reading and parsing"
```

---

### Task 4: Plan Discovery

**Files:**
- Create: `plan.go`
- Create: `plan_test.go`

- [ ] **Step 1: Write failing test for plan discovery from JSONL**

`plan_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindPlanNameFromJSONL(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "session.jsonl")

	// Write JSONL with an EnterPlanMode reference embedded in assistant message
	lines := []string{
		`{"type":"user","message":"hello"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"planning"},{"type":"tool_use","name":"EnterPlanMode","input":{}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"here is the plan"}]}}`,
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	os.WriteFile(jsonlPath, []byte(content), 0o644)

	// EnterPlanMode doesn't contain the plan name directly — the plan name
	// comes from the plans directory. So this test verifies we detect that
	// a plan was entered.
	found := sessionUsedPlan(jsonlPath)
	if !found {
		t.Error("expected sessionUsedPlan to return true")
	}
}

func TestFindPlanNameFromJSONLNoPlan(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "session.jsonl")

	lines := []string{
		`{"type":"user","message":"hello"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"no plan here"}]}}`,
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	os.WriteFile(jsonlPath, []byte(content), 0o644)

	found := sessionUsedPlan(jsonlPath)
	if found {
		t.Error("expected sessionUsedPlan to return false")
	}
}

func TestFindMostRecentPlan(t *testing.T) {
	tmpDir := t.TempDir()

	// Create two plan files
	os.WriteFile(filepath.Join(tmpDir, "old-plan.md"), []byte("# Old Plan\nstuff"), 0o644)
	// Touch the second file to be newer
	os.WriteFile(filepath.Join(tmpDir, "new-plan.md"), []byte("# New Plan\nbetter stuff"), 0o644)

	name, content, err := findMostRecentPlan(tmpDir)
	if err != nil {
		t.Fatalf("findMostRecentPlan error: %v", err)
	}
	if name == "" {
		t.Fatal("expected a plan name, got empty")
	}
	if content == "" {
		t.Fatal("expected plan content, got empty")
	}
}

func TestFindMostRecentPlanEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	name, content, err := findMostRecentPlan(tmpDir)
	if err != nil {
		t.Fatalf("findMostRecentPlan error: %v", err)
	}
	if name != "" || content != "" {
		t.Errorf("expected empty results, got name=%q content=%q", name, content)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

```bash
go test -run "TestFindPlan|TestSessionUsedPlan" -v
```

Expected: FAIL — functions undefined.

- [ ] **Step 3: Implement plan discovery**

`plan.go`:

```go
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// sessionUsedPlan scans the JSONL file line-by-line for EnterPlanMode tool calls.
// Does not parse full JSON — just looks for the string to avoid loading large files.
func sessionUsedPlan(jsonlPath string) bool {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB line buffer
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "EnterPlanMode") {
			return true
		}
	}
	return false
}

// findMostRecentPlan returns the name and content of the most recently modified
// .md file in the plans directory.
func findMostRecentPlan(plansDir string) (name string, content string, err error) {
	entries, err := os.ReadDir(plansDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil
		}
		return "", "", fmt.Errorf("reading plans dir: %w", err)
	}

	type planFile struct {
		name    string
		modTime int64
	}

	var plans []planFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		plans = append(plans, planFile{name: entry.Name(), modTime: info.ModTime().UnixNano()})
	}

	if len(plans) == 0 {
		return "", "", nil
	}

	sort.Slice(plans, func(i, j int) bool {
		return plans[i].modTime > plans[j].modTime
	})

	mostRecent := plans[0]
	data, err := os.ReadFile(filepath.Join(plansDir, mostRecent.name))
	if err != nil {
		return "", "", fmt.Errorf("reading plan file: %w", err)
	}

	return mostRecent.name, string(data), nil
}

// discoverPlan finds the active plan content. It checks if the session used a plan,
// and returns the most recently modified plan file.
func discoverPlan(plansDir string, jsonlPath string) (title string, content string) {
	if !sessionUsedPlan(jsonlPath) {
		return "", ""
	}

	name, planContent, err := findMostRecentPlan(plansDir)
	if err != nil || name == "" {
		return "", ""
	}

	// Extract title from first markdown heading
	for _, line := range strings.Split(planContent, "\n") {
		if strings.HasPrefix(line, "# ") {
			title = strings.TrimPrefix(line, "# ")
			break
		}
	}
	if title == "" {
		title = strings.TrimSuffix(name, ".md")
	}

	return title, planContent
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test -run "TestFindPlan|TestSessionUsedPlan" -v
```

Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add plan.go plan_test.go
git commit -m "feat: add plan discovery from JSONL and plans directory"
```

---

### Task 5: TUI Model and Rendering

**Files:**
- Create: `tui.go`

- [ ] **Step 1: Create the bubbletea model**

`tui.go`:

```go
package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#7D56F4")).
			Padding(0, 1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7D56F4")).
			MarginTop(1)

	completedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#04B575"))

	inProgressStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFCC00"))

	pendingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))

	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666")).
			MarginTop(1)
)

type tickMsg time.Time

type model struct {
	// Config
	tasksDir  string
	plansDir  string
	jsonlPath string
	cwd       string
	sessionID string

	// State
	tasks       []task
	planTitle   string
	planContent string
	lastUpdate  time.Time
	width       int
	height      int
	scroll      int
	ready       bool
	err         error
}

func newModel(tasksDir, plansDir, jsonlPath, cwd, sessionID string) model {
	return model{
		tasksDir:  tasksDir,
		plansDir:  plansDir,
		jsonlPath: jsonlPath,
		cwd:       cwd,
		sessionID: sessionID,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		m.pollData(),
		m.tick(),
	)
}

func (m model) tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m model) pollData() tea.Cmd {
	return func() tea.Msg {
		return dataMsg{time: time.Now()}
	}
}

type dataMsg struct {
	time time.Time
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.scroll > 0 {
				m.scroll--
			}
		case "down", "j":
			m.scroll++
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true

	case tickMsg:
		return m, tea.Batch(m.pollData(), m.tick())

	case dataMsg:
		tasks, err := readTasks(m.tasksDir)
		if err != nil {
			m.err = err
		} else {
			m.tasks = tasks
		}

		title, content := discoverPlan(m.plansDir, m.jsonlPath)
		m.planTitle = title
		m.planContent = content
		m.lastUpdate = msg.time
	}

	return m, nil
}

func (m model) View() string {
	if !m.ready {
		return "Loading..."
	}

	var b strings.Builder

	// Header
	header := titleStyle.Render("plan-monitor") + " " +
		lipgloss.NewStyle().Foreground(lipgloss.Color("#666666")).Render(m.cwd)
	b.WriteString(header)
	b.WriteString("\n")

	// Plan section
	if m.planTitle != "" {
		b.WriteString(headerStyle.Render("Plan: " + m.planTitle))
		b.WriteString("\n")

		rendered, err := renderMarkdown(m.planContent, m.width-4)
		if err == nil && rendered != "" {
			// Limit plan display height
			lines := strings.Split(rendered, "\n")
			maxPlanLines := m.height / 3
			if maxPlanLines < 5 {
				maxPlanLines = 5
			}
			if len(lines) > maxPlanLines {
				start := m.scroll
				if start > len(lines)-maxPlanLines {
					start = len(lines) - maxPlanLines
				}
				if start < 0 {
					start = 0
				}
				lines = lines[start : start+maxPlanLines]
				lines = append(lines, pendingStyle.Render("  ... (scroll with j/k)"))
			}
			b.WriteString(strings.Join(lines, "\n"))
			b.WriteString("\n")
		}
	}

	// Tasks section
	completed, total := taskCounts(m.tasks)
	if total > 0 {
		taskHeader := fmt.Sprintf("Tasks (%d/%d complete)", completed, total)
		b.WriteString(headerStyle.Render(taskHeader))
		b.WriteString("\n")

		for _, t := range m.tasks {
			var icon, line string
			switch t.Status {
			case "completed":
				icon = completedStyle.Render("  ✓")
				line = completedStyle.Render(" " + t.Subject)
			case "in_progress":
				icon = inProgressStyle.Render("  ⟳")
				label := t.Subject
				if t.ActiveForm != "" {
					label = t.ActiveForm
				}
				line = inProgressStyle.Render(" " + label)
			default:
				icon = pendingStyle.Render("  ○")
				line = pendingStyle.Render(" " + t.Subject)
			}
			b.WriteString(icon + line + "\n")
		}
	} else {
		b.WriteString(headerStyle.Render("Tasks"))
		b.WriteString("\n")
		b.WriteString(pendingStyle.Render("  No active tasks"))
		b.WriteString("\n")
	}

	// Footer
	elapsed := time.Since(m.lastUpdate).Truncate(time.Second)
	shortID := m.sessionID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	footer := fmt.Sprintf("Session: %s · Updated %s ago · q to quit", shortID, elapsed)
	b.WriteString(footerStyle.Render(footer))

	return b.String()
}

func renderMarkdown(md string, width int) (string, error) {
	if width < 20 {
		width = 20
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return "", err
	}
	return r.Render(md)
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go build .
```

Expected: compiles with no errors.

- [ ] **Step 3: Commit**

```bash
git add tui.go
git commit -m "feat: add bubbletea TUI with plan and task rendering"
```

---

### Task 6: Wire Up main.go

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Replace main.go with full CLI entry point**

`main.go`:

```go
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	sessionFlag := flag.String("session", "", "Use a specific session ID instead of auto-detecting")
	flag.Parse()

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting working directory: %v\n", err)
		os.Exit(1)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting home directory: %v\n", err)
		os.Exit(1)
	}

	claudeProjectsDir := filepath.Join(homeDir, ".claude", "projects")
	plansDir := filepath.Join(homeDir, ".claude", "plans")

	sessionID, err := resolveSession(claudeProjectsDir, cwd, *sessionFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding session: %v\n", err)
		fmt.Fprintf(os.Stderr, "Make sure you're in a directory with an active Claude Code session.\n")
		os.Exit(1)
	}

	tasksDir := filepath.Join(homeDir, ".claude", "tasks", sessionID)
	encodedPath := encodeProjectPath(cwd)
	jsonlPath := filepath.Join(claudeProjectsDir, encodedPath, sessionID+".jsonl")

	m := newModel(tasksDir, plansDir, jsonlPath, cwd, sessionID)
	p := tea.NewProgram(m, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go build .
```

Expected: compiles with no errors, produces `plan-monitor` binary.

- [ ] **Step 3: Run it in this project directory**

```bash
./plan-monitor
```

Expected: launches TUI showing the current session's tasks (the brainstorming tasks from this conversation). Press `q` to quit.

- [ ] **Step 4: Test with --session flag**

```bash
./plan-monitor --session nonexistent-id
```

Expected: launches but shows "No active tasks" (since there are no task files for that session).

- [ ] **Step 5: Run all tests**

```bash
go test ./... -v
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add main.go
git commit -m "feat: wire up CLI entry point with session resolution"
```

---

### Task 7: Install Binary

**Files:** none (install step only)

- [ ] **Step 1: Install to GOPATH/bin**

```bash
go install .
```

Expected: `plan-monitor` binary available on PATH (assuming `~/go/bin` is in PATH).

- [ ] **Step 2: Test from a different project directory**

```bash
cd ~/Projects/sonar && plan-monitor
```

Expected: shows tasks/plan for the most recent Claude session in that project. Press `q` to quit.

- [ ] **Step 3: Final commit with any cleanup**

```bash
cd /Users/michael/Projects/plan-monitor
git add -A
git status
# Only commit if there are changes
git diff --cached --quiet || git commit -m "chore: final cleanup"
```
