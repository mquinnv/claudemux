package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunConfigSet(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T) // establishes XDG_CONFIG_HOME and, for some cases, an existing config.yml
		args     []string
		wantCode int
		check    func(t *testing.T, configPath string)
	}{
		{
			name:     "creates config.yml when none exists",
			setup:    func(t *testing.T) { t.Setenv("XDG_CONFIG_HOME", t.TempDir()) },
			args:     []string{"launch.layout", "no-shell"},
			wantCode: 0,
			check: func(t *testing.T, _ string) {
				cfg, err := loadConfig()
				if err != nil {
					t.Fatalf("loadConfig() error = %v", err)
				}
				if cfg.Launch.Layout != "no-shell" {
					t.Errorf("Launch.Layout = %q, want %q", cfg.Launch.Layout, "no-shell")
				}
			},
		},
		{
			name:     "preserves comments and unrelated keys",
			setup:    func(t *testing.T) { writeConfig(t, "# my note\nsummary:\n  model: claude-haiku-4-5\n") },
			args:     []string{"launch.layout", "head-bottom"},
			wantCode: 0,
			check: func(t *testing.T, path string) {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				got := string(raw)
				if !strings.Contains(got, "# my note") {
					t.Errorf("config.yml lost the comment:\n%s", got)
				}
				if !strings.Contains(got, "model: claude-haiku-4-5") {
					t.Errorf("config.yml lost summary.model:\n%s", got)
				}
				cfg, err := loadConfig()
				if err != nil {
					t.Fatalf("loadConfig() error = %v", err)
				}
				if cfg.Launch.Layout != "head-bottom" {
					t.Errorf("Launch.Layout = %q, want %q", cfg.Launch.Layout, "head-bottom")
				}
			},
		},
		{
			name:     "merges into an existing launch mapping without duplicating the key",
			setup:    func(t *testing.T) { writeConfig(t, "launch:\n  auto_worktree: true\n") },
			args:     []string{"launch.layout", "shell-bottom"},
			wantCode: 0,
			check: func(t *testing.T, path string) {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if n := strings.Count(string(raw), "launch:"); n != 1 {
					t.Errorf("config.yml has %d \"launch:\" keys, want 1:\n%s", n, raw)
				}
				cfg, err := loadConfig()
				if err != nil {
					t.Fatalf("loadConfig() error = %v", err)
				}
				if !cfg.Launch.AutoWorktree {
					t.Error("Launch.AutoWorktree = false, want true — merging layout must not disturb the existing sibling key")
				}
				if cfg.Launch.Layout != "shell-bottom" {
					t.Errorf("Launch.Layout = %q, want %q", cfg.Launch.Layout, "shell-bottom")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)
			dir, err := configDir()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "config.yml")

			var stdout, stderr bytes.Buffer
			code := runConfigSet(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("runConfigSet() = %d, want %d; stderr=%q", code, tt.wantCode, stderr.String())
			}
			if tt.check != nil {
				tt.check(t, path)
			}
		})
	}
}

// An invalid launch.layout parses fine as YAML but fails Config.validate(),
// same as loadConfig would reject it — config set must not brick the next
// launch by writing it anyway.
func TestRunConfigSetInvalidLayoutLeavesFileUntouched(t *testing.T) {
	writeConfig(t, "launch:\n  auto_worktree: true\n")
	dir, err := configDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runConfigSet([]string{"launch.layout", "sideways"}, &stdout, &stderr)
	if code != 3 {
		t.Errorf("runConfigSet() = %d, want 3 for an invalid layout value", code)
	}
	if stderr.String() == "" {
		t.Error("stderr empty, want the validation error reported")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("config.yml changed on a rejected write.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// A config.yml that already fails to parse must fail config set the same way
// it fails config get — and must not be overwritten by the attempt.
func TestRunConfigSetMalformedConfigLeavesFileUntouched(t *testing.T) {
	writeConfig(t, "summary:\n  enabled: [bad\n")
	dir, err := configDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runConfigSet([]string{"launch.layout", "no-shell"}, &stdout, &stderr)
	if code != 3 {
		t.Errorf("runConfigSet() = %d, want 3 for a malformed config.yml", code)
	}
	if stderr.String() == "" {
		t.Error("stderr empty, want the parse error reported")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("config.yml changed after a failed config set.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// Both malformed invocations, not just the zero-arg one: config set takes
// exactly a path and a value.
func TestRunConfigSetWrongArgCount(t *testing.T) {
	for _, args := range [][]string{nil, {"launch.layout"}, {"a", "b", "c"}} {
		var stdout, stderr bytes.Buffer

		if code := runConfigSet(args, &stdout, &stderr); code != 2 {
			t.Errorf("runConfigSet(%v) = %d, want 2 (usage error)", args, code)
		}
		if stderr.String() == "" {
			t.Errorf("runConfigSet(%v): stderr empty, want a usage message for a malformed invocation", args)
		}
	}
}
