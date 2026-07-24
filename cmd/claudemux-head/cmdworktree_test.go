package main

import (
	"testing"
	"time"
)

func cmdEvent(sidechain bool, command string) Event {
	return Event{
		IsSidechain: sidechain,
		ToolUses:    []ToolUse{{Name: "Bash", Input: map[string]interface{}{"command": command}}},
	}
}

func TestRecentCommandStrings(t *testing.T) {
	events := []Event{
		cmdEvent(false, "one"),
		cmdEvent(true, "sidechain-two"), // excluded
		cmdEvent(false, "three"),
		{IsSidechain: false, UserText: "plain message, no tool"}, // no command
		cmdEvent(false, "four"),
	}
	got := recentCommandStrings(events, 2)
	// Newest-first collection of the last 2 *commands*, main-session only.
	// Fields are newline-joined, so match by substring rather than equality.
	if len(got) != 2 || !contains(got[0], "four") || !contains(got[1], "three") {
		t.Fatalf("recentCommandStrings = %q, want newest-first [four three]", got)
	}
	// A multi-field tool_use folds all its string inputs into one entry.
	ev := Event{ToolUses: []ToolUse{{Name: "Edit", Input: map[string]interface{}{
		"file_path": "/wt/app/x.ts", "old_string": "a", "new_string": "b",
	}}}}
	one := recentCommandStrings([]Event{ev}, 5)
	if len(one) != 1 || !contains(one[0], "/wt/app/x.ts") {
		t.Fatalf("recentCommandStrings folded = %v, want one entry containing the path", one)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestDominantWorktree(t *testing.T) {
	roots := []worktreeRoot{
		{path: "/Users/x/Projects/remix-crm-569", name: "remix-crm-569"},
		{path: "/Users/x/Projects/remix-crm-567", name: "remix-crm-567"},
	}
	cmd569 := "git -C /Users/x/Projects/remix-crm-569 status"
	cmd567 := "git -C /Users/x/Projects/remix-crm-567 log"

	// One worktree dominates a window of commands.
	win := []string{cmd569, cmd569, cmd569, cmd569, cmd567}
	if got := dominantWorktree(win, roots); got != "remix-crm-569" {
		t.Errorf("dominantWorktree = %q, want remix-crm-569", got)
	}

	// A single stray reference is not dominant → clear.
	if got := dominantWorktree([]string{cmd567, "ls", "pwd", "echo hi"}, roots); got != "" {
		t.Errorf("dominantWorktree (one-off) = %q, want \"\"", got)
	}

	// No worktree references at all → clear.
	if got := dominantWorktree([]string{"ls", "go test ./..."}, roots); got != "" {
		t.Errorf("dominantWorktree (none) = %q, want \"\"", got)
	}

	// A near-tie between two worktrees is ambiguous → clear rather than flicker.
	tie := []string{cmd569, cmd569, cmd569, cmd567, cmd567, cmd567}
	if got := dominantWorktree(tie, roots); got != "" {
		t.Errorf("dominantWorktree (tie) = %q, want \"\"", got)
	}

	// A command referencing a nested file path under a worktree still attributes
	// to that worktree.
	nested := []string{
		"cat /Users/x/Projects/remix-crm-569/app/routes/foo.ts",
		"cat /Users/x/Projects/remix-crm-569/app/routes/bar.ts",
		"cat /Users/x/Projects/remix-crm-569/gql/baz.ts",
	}
	if got := dominantWorktree(nested, roots); got != "remix-crm-569" {
		t.Errorf("dominantWorktree (nested paths) = %q, want remix-crm-569", got)
	}
}

func TestLinkedWorktreeRoots(t *testing.T) {
	repo, worktree := initRepoWithWorktree(t, "webapp-crm-570")
	now := time.Now()

	roots := linkedWorktreeRoots(repo, now)
	if len(roots) != 1 {
		t.Fatalf("linkedWorktreeRoots = %v, want exactly the one linked worktree", roots)
	}
	if roots[0].name != "webapp-crm-570" || roots[0].path != worktree {
		t.Errorf("root = %+v, want name=webapp-crm-570 path=%s", roots[0], worktree)
	}
	// The main worktree is excluded.
	for _, r := range roots {
		if r.path == repo {
			t.Errorf("main worktree %s must not be listed", repo)
		}
	}
	// Querying from inside the linked worktree yields the same set.
	if from := linkedWorktreeRoots(worktree, now); len(from) != 1 || from[0].name != "webapp-crm-570" {
		t.Errorf("linkedWorktreeRoots(from worktree) = %v, want the same one", from)
	}
	// A non-repo dir yields nothing.
	if r := linkedWorktreeRoots(t.TempDir(), now); len(r) != 0 {
		t.Errorf("linkedWorktreeRoots(non-repo) = %v, want none", r)
	}
}

// End-to-end: a session whose cwd stays in the main repo but whose commands
// drive a linked worktree at arm's length shows that worktree's chip.
func TestWorktreeChipArmsLength(t *testing.T) {
	repo, worktree := initRepoWithWorktree(t, "webapp-crm-571")
	now := time.Now()

	var events []Event
	// cwd is always the main repo; commands reach into the worktree by path.
	for i := 0; i < 6; i++ {
		e := cmdEvent(false, "git -C "+worktree+" status")
		e.Cwd = repo
		events = append(events, e)
	}
	m := model{allEvents: events}
	m.sessionCwd = lastMainCwd(events, "")
	m.cmdWorktree = commandWorktree(m.sessionCwd, events, now)

	if m.sessionCwd != repo {
		t.Fatalf("sessionCwd = %q, want the main repo %q", m.sessionCwd, repo)
	}
	if got := m.worktreeChip(); got != "webapp-crm-571" {
		t.Errorf("worktreeChip() = %q, want webapp-crm-571 (arm's-length)", got)
	}

	// When recent commands stop referencing the worktree, the chip clears.
	var idle []Event
	for i := 0; i < 30; i++ {
		e := cmdEvent(false, "go test ./...")
		e.Cwd = repo
		idle = append(idle, e)
	}
	m2 := model{allEvents: idle}
	m2.sessionCwd = lastMainCwd(idle, "")
	m2.cmdWorktree = commandWorktree(m2.sessionCwd, idle, now)
	if got := m2.worktreeChip(); got != "" {
		t.Errorf("worktreeChip() after leaving worktree = %q, want \"\"", got)
	}

	// A session whose cwd genuinely IS the worktree wins via cwd, no commands needed.
	m3 := model{sessionCwd: worktree}
	if got := m3.worktreeChip(); got != "webapp-crm-571" {
		t.Errorf("worktreeChip() cwd-in-worktree = %q, want webapp-crm-571", got)
	}
}
