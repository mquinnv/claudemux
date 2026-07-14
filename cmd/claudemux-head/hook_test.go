package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeSettings puts contents at $HOME/.claude/settings.json and returns its path.
func writeSettings(t *testing.T, contents string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, ".claude", "settings.json")
	if contents != "" {
		if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", dir)
	return p
}

// stubScript creates a fake claudemux-map.sh for --script to point at.
func stubScript(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "claudemux-map.sh")
	if err := os.WriteFile(p, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("settings.json is not valid JSON after hook ensure: %v", err)
	}
	return m
}

// hookCommands returns every command string registered on event.
func hookCommands(t *testing.T, settings map[string]any, event string) []string {
	t.Helper()
	var out []string
	hooks, _ := settings["hooks"].(map[string]any)
	groups, _ := hooks[event].([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		inner, _ := gm["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if c, ok := hm["command"].(string); ok {
				out = append(out, c)
			}
		}
	}
	return out
}

func TestHookEnsureFreshInstall(t *testing.T) {
	p := writeSettings(t, "") // no settings.json at all
	script := stubScript(t)

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure([]string{"--script", script}, &stdout, &stderr); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0; stderr=%q", code, stderr.String())
	}

	s := readSettings(t, p)
	for _, ev := range []string{"SessionStart", "UserPromptSubmit"} {
		cmds := hookCommands(t, s, ev)
		if len(cmds) != 1 {
			t.Fatalf("%s: got %d commands, want 1: %v", ev, len(cmds), cmds)
		}
		if filepath.Base(cmds[0]) != "claudemux-map.sh" {
			t.Errorf("%s: command = %q, want it to end in claudemux-map.sh", ev, cmds[0])
		}
	}
}

// The hook script must be copied to a STABLE path, not referenced where it
// happened to be installed — a Homebrew libexec path would break on upgrade.
func TestHookEnsureCopiesScriptToStablePath(t *testing.T) {
	writeSettings(t, "")
	script := stubScript(t)

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure([]string{"--script", script}, &stdout, &stderr); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0", code)
	}

	home, _ := os.UserHomeDir()
	installed := filepath.Join(home, ".claude", "hooks", "claudemux-map.sh")
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatalf("hook script not copied to %s: %v", installed, err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed hook is not executable (mode %v) — Claude Code cannot run it", info.Mode().Perm())
	}
}

// Running twice must not duplicate the entries, and must not rewrite the file.
func TestHookEnsureIsIdempotent(t *testing.T) {
	p := writeSettings(t, "")
	script := stubScript(t)

	var b1, b2 bytes.Buffer
	runHookEnsure([]string{"--script", script}, &b1, &b1)
	first, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	if code := runHookEnsure([]string{"--script", script}, &b2, &b2); code != 0 {
		t.Fatalf("second runHookEnsure() = %d, want 0", code)
	}
	second, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(first, second) {
		t.Error("settings.json changed on the second run — hook ensure must be a no-op when already present")
	}
	s := readSettings(t, p)
	if got := len(hookCommands(t, s, "SessionStart")); got != 1 {
		t.Errorf("SessionStart has %d commands after two runs, want 1 — the entry was duplicated", got)
	}
}

// THE most important test: a real user's settings.json is full of unrelated
// config and other people's hooks. None of it may be lost.
func TestHookEnsurePreservesUnrelatedKeysAndHooks(t *testing.T) {
	orig := `{
  "model": "opus",
  "permissions": {"allow": ["Bash(ls:*)"]},
  "theme": "dark",
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "/other/tool.sh"}]}
    ],
    "PreToolUse": [
      {"hooks": [{"type": "command", "command": "/unrelated/event.sh"}]}
    ]
  }
}`
	p := writeSettings(t, orig)
	script := stubScript(t)

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure([]string{"--script", script}, &stdout, &stderr); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0; stderr=%q", code, stderr.String())
	}

	s := readSettings(t, p)

	if s["model"] != "opus" {
		t.Errorf("model = %v, want \"opus\" — an unrelated top-level key was lost", s["model"])
	}
	if s["theme"] != "dark" {
		t.Errorf("theme = %v, want \"dark\" — an unrelated top-level key was lost", s["theme"])
	}
	if _, ok := s["permissions"]; !ok {
		t.Error("permissions key was dropped — this is the user's live security config")
	}

	// Another tool's SessionStart hook must survive alongside ours.
	cmds := hookCommands(t, s, "SessionStart")
	var sawOther, sawOurs bool
	for _, c := range cmds {
		if c == "/other/tool.sh" {
			sawOther = true
		}
		if filepath.Base(c) == "claudemux-map.sh" {
			sawOurs = true
		}
	}
	if !sawOther {
		t.Error("another tool's SessionStart hook was clobbered")
	}
	if !sawOurs {
		t.Error("our hook was not registered on SessionStart")
	}

	// An unrelated EVENT must survive untouched.
	if got := hookCommands(t, s, "PreToolUse"); len(got) != 1 || got[0] != "/unrelated/event.sh" {
		t.Errorf("PreToolUse = %v, want the unrelated hook preserved", got)
	}
}

// A settings.json we cannot parse must be left ALONE. Replacing a corrupt but
// hand-recoverable config with our own is the worst possible outcome.
func TestHookEnsureRefusesMalformedSettings(t *testing.T) {
	orig := `{"model": "opus", TRUNCATED`
	p := writeSettings(t, orig)
	script := stubScript(t)

	var stdout, stderr bytes.Buffer
	code := runHookEnsure([]string{"--script", script}, &stdout, &stderr)

	if code != 3 {
		t.Errorf("runHookEnsure() = %d, want 3 for a malformed settings.json", code)
	}
	if stderr.String() == "" {
		t.Error("stderr empty, want the parse error reported")
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != orig {
		t.Error("settings.json was modified despite being unparseable — it must be left exactly as found")
	}
}

// Malformed settings.json must leave the operation a COMPLETE no-op: not just
// refusing to write the config, but also refusing to copy the hook script.
// A half-done install (script copied but settings not updated) is worse than
// no install at all — the hook directive points to a path that may disappear
// or be out of sync.
func TestHookEnsureMalformedSettingsCopiesNothing(t *testing.T) {
	orig := `{"model": "opus", TRUNCATED`
	settingsPath := writeSettings(t, orig)
	script := stubScript(t)

	var stdout, stderr bytes.Buffer
	code := runHookEnsure([]string{"--script", script}, &stdout, &stderr)

	if code != 3 {
		t.Fatalf("runHookEnsure() = %d, want 3 for a malformed settings.json", code)
	}

	// Verify settings.json was not touched
	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != orig {
		t.Error("settings.json was modified despite being unparseable — it must be left exactly as found")
	}

	// Verify the hook script was not copied
	home, _ := os.UserHomeDir()
	hooksDir := filepath.Join(home, ".claude", "hooks")
	installed := filepath.Join(hooksDir, hookScriptName)

	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Errorf("hook script was copied despite malformed config: %v — a half-done install is worse than none", err)
	}

	if _, err := os.Stat(hooksDir); !os.IsNotExist(err) {
		t.Errorf("hooks/ directory was created despite malformed config — a half-done install is worse than none")
	}
}

// The exit-2 usage path must be exercised: an unknown flag should return 2
// and report the error to stderr.
func TestHookEnsureUsageErrorOnUnknownFlag(t *testing.T) {
	writeSettings(t, "")

	var stdout, stderr bytes.Buffer
	code := runHookEnsure([]string{"--bogus"}, &stdout, &stderr)

	if code != 2 {
		t.Errorf("runHookEnsure(--bogus) = %d, want 2 for usage error", code)
	}
	if stderr.String() == "" {
		t.Error("stderr empty, want usage error reported")
	}
}

// A backup must exist before we overwrite a file that had content.
func TestHookEnsureBacksUpBeforeWriting(t *testing.T) {
	p := writeSettings(t, `{"model": "opus"}`)
	script := stubScript(t)

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure([]string{"--script", script}, &stdout, &stderr); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0", code)
	}

	matches, err := filepath.Glob(p + ".bak-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("found %d backups, want exactly 1", len(matches))
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"model": "opus"}` {
		t.Errorf("backup = %q, want the ORIGINAL contents", string(b))
	}
}
