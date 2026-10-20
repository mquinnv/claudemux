package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The width follows bin/claudemux's shell_size_for: project, then config.yml,
// then 30% — and an illegal value at either level falls through rather than
// reaching tmux.
func TestShellSplitSize(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name, configPath, launch, want string
	}{
		{"nothing set", filepath.Join(dir, "missing.yml"), "", "30%"},
		{"config.yml only", filepath.Join(dir, "missing.yml"), "40%", "40%"},
		{"project beats config.yml", write("a.yml", "shell_size: 90\n"), "40%", "90"},
		{"illegal project falls through", write("b.yml", "shell_size: wide\n"), "40%", "40%"},
		{"illegal config.yml falls through", filepath.Join(dir, "missing.yml"), "0%", "30%"},
		{"unparseable project file", write("c.yml", "shell_size: [\n"), "", "30%"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shellSplitSize(tt.configPath, tt.launch); got != tt.want {
				t.Errorf("shellSplitSize() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShellSplitArgs(t *testing.T) {
	want := []string{"split-window", "-h", "-l", "30%", "-t", "%7", "-c", "/repo/.claude/worktrees/x"}
	if got := shellSplitArgs("%7", "30%", "/repo/.claude/worktrees/x"); !reflect.DeepEqual(got, want) {
		t.Errorf("shellSplitArgs() = %q, want %q", got, want)
	}
	want = []string{"split-window", "-h", "-l", "30%", "-t", "%7"}
	if got := shellSplitArgs("%7", "30%", ""); !reflect.DeepEqual(got, want) {
		t.Errorf("shellSplitArgs() no dir = %q, want %q", got, want)
	}
}

func TestShellSplitChip(t *testing.T) {
	now := time.Now()
	if got := shellSplitChip("", now, now); got != "" {
		t.Errorf("no note: chip = %q, want empty", got)
	}
	if got := shellSplitChip("no claude pane", now.Add(-time.Second), now); got != "▌ no claude pane" {
		t.Errorf("fresh note: chip = %q", got)
	}
	if got := shellSplitChip("no claude pane", now.Add(-teardownNoteTTL), now); got != "" {
		t.Errorf("expired note: chip = %q, want empty", got)
	}
}

// `t` issues the split only inside tmux, and clears a stale failure so the
// chip doesn't outlive the retry that may fix it.
func TestShellSplitKey(t *testing.T) {
	key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")}

	if _, cmd := (model{shellSplitSize: "30%"}).Update(key); cmd != nil {
		t.Error("outside tmux: want no command")
	}

	m := model{selfPane: "%1", shellSplitSize: "30%", shellSplitNote: "shell split failed", shellSplitNoteAt: time.Now()}
	got, cmd := m.Update(key)
	if cmd == nil {
		t.Fatal("inside tmux: want a split command")
	}
	if n := got.(model).shellSplitNote; n != "" {
		t.Errorf("note not cleared: %q", n)
	}
}
