package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// installedHookBinary is where hook ensure puts its copy of this binary under
// the current HOME — what every registered command runs.
func installedHookBinary(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, ".claude", "hooks", hookBinaryName)
}

// hookCmd is the registered command for the hook named name.
func hookCmd(t *testing.T, name string) string {
	t.Helper()
	for _, h := range claudemuxHooks {
		if h.name == name {
			return hookCommandFor(installedHookBinary(t), h)
		}
	}
	t.Fatalf("no hook named %q", name)
	return ""
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

// hookGroupMatcher returns the "matcher" value of the group on event that
// contains command, and whether the group had a "matcher" key at all —
// callers need that distinction to catch a stray `"matcher": ""`, which must
// never be written (an absent key and an empty string are not the same thing
// to Claude Code).
func hookGroupMatcher(t *testing.T, settings map[string]any, event, command string) (matcher string, hasKey bool) {
	t.Helper()
	hooks, _ := settings["hooks"].(map[string]any)
	groups, _ := hooks[event].([]any)
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		inner, _ := gm["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if c, _ := hm["command"].(string); c != command {
				continue
			}
			m, ok := gm["matcher"]
			if !ok {
				return "", false
			}
			ms, _ := m.(string)
			return ms, true
		}
	}
	t.Fatalf("no group on event %q contains command %q", event, command)
	return "", false
}

func sameStrings(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]int{}
	for _, g := range got {
		seen[g]++
	}
	for _, w := range want {
		if seen[w] == 0 {
			return false
		}
		seen[w]--
	}
	return true
}

func TestHookEnsureFreshInstall(t *testing.T) {
	p := writeSettings(t, "") // no settings.json at all

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0; stderr=%q", code, stderr.String())
	}

	s := readSettings(t, p)

	// SessionStart carries only the pane-map hook: the worktree hook has
	// nothing to record before a prompt exists.
	if start := hookCommands(t, s, "SessionStart"); !sameStrings(start, hookCmd(t, "map")) {
		t.Errorf("SessionStart = %v, want exactly the map hook", start)
	}

	// UserPromptSubmit carries all three hooks, and only those: an exact
	// count catches a duplicate-registration bug that a "contains" check
	// would miss.
	if ups := hookCommands(t, s, "UserPromptSubmit"); !sameStrings(ups, hookCmd(t, "map"), hookCmd(t, "worktree"), hookCmd(t, "ask")) {
		t.Errorf("UserPromptSubmit = %v, want exactly map, worktree and ask", ups)
	}
}

// The hooks must run from a STABLE copy of this binary, not from wherever it
// happened to be installed — a Homebrew libexec path would break on upgrade.
func TestHookEnsureInstallsBinaryToStablePath(t *testing.T) {
	writeSettings(t, "")

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0", code)
	}

	installed := installedHookBinary(t)
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatalf("binary not copied to %s: %v", installed, err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary is not executable (mode %v) — Claude Code cannot run it", info.Mode().Perm())
	}
	self, _ := os.Executable()
	want, _ := os.ReadFile(self)
	if got, _ := os.ReadFile(installed); !bytes.Equal(got, want) {
		t.Error("installed binary is not a copy of the running one")
	}
}

// Running twice must not duplicate the entries, and must not rewrite the file.
func TestHookEnsureIsIdempotent(t *testing.T) {
	p := writeSettings(t, "")

	var b1, b2 bytes.Buffer
	runHookEnsure(nil, &b1, &b1)
	first, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	if code := runHookEnsure(nil, &b2, &b2); code != 0 {
		t.Fatalf("second runHookEnsure() = %d, want 0", code)
	}
	second, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(first, second) {
		t.Error("settings.json changed on the second run — hook ensure must be a no-op when already present")
	}
	if b2.String() != "" {
		t.Errorf("second run said %q, want silence", b2.String())
	}
	s := readSettings(t, p)
	if got := len(hookCommands(t, s, "SessionStart")); got != 1 {
		t.Errorf("SessionStart has %d commands after two runs, want 1 — the entry was duplicated", got)
	}
	if got := len(hookCommands(t, s, "UserPromptSubmit")); got != 3 {
		t.Errorf("UserPromptSubmit has %d commands after two runs, want 3", got)
	}
}

// The installed binary is this whole binary (potentially tens of MB), and
// hook ensure runs on every launch of bin/claudemux — so a second run must
// not rewrite it when it is already current. copyExecutableIfChanged must
// skip the write.
//
// An mtime comparison is not strong enough evidence: on a filesystem with
// coarse mtime resolution, two writes inside the same tick produce identical
// mtimes, so a regression to an unconditional copy could still pass this
// test. Instead the destination is made read-only after the first run: if
// the write is correctly skipped, the second run still succeeds (exit 0)
// even though it could not have written; if the copy regressed to
// unconditional, os.WriteFile against a read-only file fails and
// runHookEnsure returns 4. The exit code alone proves the write was
// skipped, not merely that it wrote identical bytes again — and the byte
// comparison confirms the destination still holds the current binary.
func TestHookEnsureDoesNotRewriteUnchangedBinary(t *testing.T) {
	writeSettings(t, "")

	var b1, b2 bytes.Buffer
	if code := runHookEnsure(nil, &b1, &b1); code != 0 {
		t.Fatalf("first runHookEnsure() = %d, want 0 (stderr %s)", code, b1.String())
	}

	binPath := installedHookBinary(t)
	wantBytes, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("binary not installed at %s: %v", binPath, err)
	}

	if err := os.Chmod(binPath, 0o444); err != nil {
		t.Fatalf("chmod %s read-only: %v", binPath, err)
	}
	// Restore write permission before t.TempDir()'s own cleanup tries to
	// remove the directory tree; t.Cleanup runs LIFO, so this (registered
	// after writeSettings's t.TempDir call) runs first.
	t.Cleanup(func() { _ = os.Chmod(binPath, 0o755) })

	if code := runHookEnsure(nil, &b2, &b2); code != 0 {
		t.Fatalf("second runHookEnsure() = %d, want 0 — the write must be skipped when the binary is already current, even with the destination read-only (stderr %s)", code, b2.String())
	}

	got, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wantBytes) {
		t.Error("installed binary changed after the second run, want it untouched")
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

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure(nil, &stdout, &stderr); code != 0 {
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
	if cmds := hookCommands(t, s, "SessionStart"); !sameStrings(cmds, "/other/tool.sh", hookCmd(t, "map")) {
		t.Errorf("SessionStart = %v, want the other tool's hook beside ours", cmds)
	}

	// An unrelated hook on an event we also register on must survive
	// alongside ours (the ask hook claims PreToolUse too).
	if pre := hookCommands(t, s, "PreToolUse"); !sameStrings(pre, "/unrelated/event.sh", hookCmd(t, "ask")) {
		t.Errorf("PreToolUse = %v, want the unrelated hook preserved beside the ask hook", pre)
	}
}

// A settings.json we cannot parse must be left ALONE. Replacing a corrupt but
// hand-recoverable config with our own is the worst possible outcome.
func TestHookEnsureRefusesMalformedSettings(t *testing.T) {
	orig := `{"model": "opus", TRUNCATED`
	p := writeSettings(t, orig)

	var stdout, stderr bytes.Buffer
	code := runHookEnsure(nil, &stdout, &stderr)

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
// refusing to write the config, but also refusing to install the binary or
// shim anything. A half-done install (files in place but settings not
// updated) is worse than no install at all.
func TestHookEnsureMalformedSettingsCopiesNothing(t *testing.T) {
	orig := `{"model": "opus", TRUNCATED`
	settingsPath := writeSettings(t, orig)

	var stdout, stderr bytes.Buffer
	code := runHookEnsure(nil, &stdout, &stderr)

	if code != 3 {
		t.Fatalf("runHookEnsure() = %d, want 3 for a malformed settings.json", code)
	}

	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != orig {
		t.Error("settings.json was modified despite being unparseable — it must be left exactly as found")
	}

	hooksDir := filepath.Dir(installedHookBinary(t))
	if _, err := os.Stat(hooksDir); !os.IsNotExist(err) {
		t.Errorf("hooks/ directory was created despite malformed config — a half-done install is worse than none")
	}
}

// The exit-2 usage path must be exercised: an unknown flag should return 2
// and report the error to stderr. `--script` is one now: it pointed at the
// bash scripts this binary replaced, and nothing passes it any more.
func TestHookEnsureUsageErrorOnUnknownFlag(t *testing.T) {
	writeSettings(t, "")

	for _, flag := range []string{"--bogus", "--script"} {
		var stdout, stderr bytes.Buffer
		if code := runHookEnsure([]string{flag, "x"}, &stdout, &stderr); code != 2 {
			t.Errorf("runHookEnsure(%s) = %d, want 2 for usage error", flag, code)
		}
		if stderr.String() == "" {
			t.Errorf("%s: stderr empty, want usage error reported", flag)
		}
	}
}

// A backup must exist before we overwrite a file that had content.
func TestHookEnsureBacksUpBeforeWriting(t *testing.T) {
	p := writeSettings(t, `{"model": "opus"}`)

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure(nil, &stdout, &stderr); code != 0 {
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

func TestHookEnsureRegistersAllHooks(t *testing.T) {
	settingsPath := writeSettings(t, "")

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("hook ensure = %d, want 0 (stderr %s)", code, stderr.String())
	}

	settings := readSettings(t, settingsPath)
	ups := hookCommands(t, settings, "UserPromptSubmit")
	if !sameStrings(ups, hookCmd(t, "map"), hookCmd(t, "worktree"), hookCmd(t, "ask")) {
		t.Errorf("UserPromptSubmit = %v, want all three hooks", ups)
	}

	// The worktree hook has nothing to record at session start; registering it
	// on SessionStart would inject its instruction before a prompt exists.
	if start := hookCommands(t, settings, "SessionStart"); !sameStrings(start, hookCmd(t, "map")) {
		t.Errorf("SessionStart = %v, want only the map hook", start)
	}

	// The ask hook needs the tool-call events, and must be alone there: no
	// other claudemux hook has business running on every tool call.
	for _, event := range []string{"PreToolUse", "PostToolUse"} {
		cmds := hookCommands(t, settings, event)
		if !sameStrings(cmds, hookCmd(t, "ask")) {
			t.Errorf("%s = %v, want only the ask hook", event, cmds)
			continue
		}
		// Without a matcher this hook would run on every tool call of every
		// session, spawning a process twice per call just to exit at the
		// tool-name check.
		matcher, hasKey := hookGroupMatcher(t, settings, event, cmds[0])
		if !hasKey || matcher != "AskUserQuestion" {
			t.Errorf("%s ask hook matcher = %q (present=%v), want \"AskUserQuestion\"", event, matcher, hasKey)
		}
	}

	// PermissionRequest fires only when a dialog is about to open, so it needs
	// no matcher to stay cheap — and must not have one: every tool's dialog
	// blocks the session the same way.
	perm := hookCommands(t, settings, "PermissionRequest")
	if !sameStrings(perm, hookCmd(t, "ask")) {
		t.Errorf("PermissionRequest = %v, want only the ask hook", perm)
	} else if matcher, hasKey := hookGroupMatcher(t, settings, "PermissionRequest", perm[0]); hasKey {
		t.Errorf("PermissionRequest ask hook has matcher %q, want no matcher key", matcher)
	}

	// UserPromptSubmit is not a tool-call event: none of the three hooks'
	// entries there may carry a matcher key, empty or otherwise.
	for _, c := range ups {
		if matcher, hasKey := hookGroupMatcher(t, settings, "UserPromptSubmit", c); hasKey {
			t.Errorf("UserPromptSubmit %s has matcher %q, want no matcher key at all", c, matcher)
		}
	}
}

// legacySettings is a settings.json as the bash-script releases left it, with
// another tool's hooks mixed in — one of them sharing a group with ours.
func legacySettings(hooksDir string) string {
	sh := func(name string) string { return filepath.Join(hooksDir, name) }
	return `{
  "model": "opus",
  "statusLine": {"type": "command", "command": "` + sh("claudemux-statusline") + ` statusline"},
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "` + sh("claudemux-map.sh") + `"}]},
      {"hooks": [{"type": "command", "command": "/other/tool.sh"}]}
    ],
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": "` + sh("claudemux-map.sh") + `"}]},
      {"hooks": [{"type": "command", "command": "` + sh("claudemux-worktree.sh") + `"},
                 {"type": "command", "command": "/other/prompt.sh"}]},
      {"hooks": [{"type": "command", "command": "` + sh("claudemux-ask.sh") + `"}]}
    ],
    "PreToolUse": [
      {"matcher": "AskUserQuestion", "hooks": [{"type": "command", "command": "` + sh("claudemux-ask.sh") + `"}]},
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/other/bash-guard.sh"}]}
    ],
    "PostToolUse": [
      {"matcher": "AskUserQuestion", "hooks": [{"type": "command", "command": "` + sh("claudemux-ask.sh") + `"}]}
    ],
    "PermissionRequest": [
      {"hooks": [{"type": "command", "command": "` + sh("claudemux-ask.sh") + `"}]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "bash ` + sh("claudemux-ask.sh") + `"}]}
    ]
  }
}`
}

// An install from before the Go hooks: every old script entry is replaced by
// its Go command — none left, none duplicated — and nothing that is not ours
// moves, including a hook that shared a group with one of ours.
func TestHookEnsureMigratesLegacyScriptEntries(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	hooksDir := filepath.Join(home, ".claude", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	p := filepath.Join(home, ".claude", "settings.json")
	if err := os.WriteFile(p, []byte(legacySettings(hooksDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0 (stderr %s)", code, stderr.String())
	}
	s := readSettings(t, p)

	want := map[string][]string{
		"SessionStart":      {hookCmd(t, "map"), "/other/tool.sh"},
		"UserPromptSubmit":  {hookCmd(t, "map"), hookCmd(t, "worktree"), hookCmd(t, "ask"), "/other/prompt.sh"},
		"PreToolUse":        {hookCmd(t, "ask"), "/other/bash-guard.sh"},
		"PostToolUse":       {hookCmd(t, "ask")},
		"PermissionRequest": {hookCmd(t, "ask")},
	}
	for event, cmds := range want {
		if got := hookCommands(t, s, event); !sameStrings(got, cmds...) {
			t.Errorf("%s = %v, want %v", event, got, cmds)
		}
	}
	// The hand-added Stop entry ran a script that no longer ships; its group
	// emptied, and so did the event, so neither is left as an empty husk.
	if hooks := s["hooks"].(map[string]any); hooks["Stop"] != nil {
		t.Errorf("Stop = %v, want the event removed once its only hook was", hooks["Stop"])
	}
	// The other tool's PreToolUse matcher is untouched, and ours is restored.
	if m, _ := hookGroupMatcher(t, s, "PreToolUse", "/other/bash-guard.sh"); m != "Bash" {
		t.Errorf("the other tool's PreToolUse matcher = %q, want \"Bash\"", m)
	}
	if m, _ := hookGroupMatcher(t, s, "PreToolUse", hookCmd(t, "ask")); m != "AskUserQuestion" {
		t.Errorf("ask hook PreToolUse matcher = %q, want \"AskUserQuestion\"", m)
	}
	if cmd, _ := statusLineCommand(t, s); cmd != shQuote(installedHookBinary(t))+" statusline" {
		t.Errorf("statusLine = %q, want the legacy claudemux statusline taken over", cmd)
	}
	if s["model"] != "opus" {
		t.Errorf("model = %v, want it preserved", s["model"])
	}

	// A second run finds nothing left to migrate.
	before, _ := os.ReadFile(p)
	var again bytes.Buffer
	if code := runHookEnsure(nil, &again, &again); code != 0 {
		t.Fatalf("second runHookEnsure() = %d (stderr %s)", code, again.String())
	}
	if after, _ := os.ReadFile(p); !bytes.Equal(before, after) {
		t.Error("settings.json changed on the run after the migration")
	}
}

// Claude Code snapshots hook commands at session start, so sessions that were
// running when the upgrade landed keep invoking the old files. Each old file
// that is present becomes a shim into the new binary; none is created where
// there was none.
func TestHookEnsureShimsLegacyFilesThatExist(t *testing.T) {
	writeSettings(t, "")
	hooksDir := filepath.Dir(installedHookBinary(t))
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claudemux-map.sh", "claudemux-statusline"} {
		if err := os.WriteFile(filepath.Join(hooksDir, name), []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0 (stderr %s)", code, stderr.String())
	}

	bin := installedHookBinary(t)
	for name, wantArgs := range map[string]string{"claudemux-map.sh": "hook map", "claudemux-statusline": `"$@"`} {
		got, err := os.ReadFile(filepath.Join(hooksDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, legacyShim(bin, wantArgs)) {
			t.Errorf("%s = %q, want the shim into %s %s", name, got, bin, wantArgs)
		}
	}
	for _, name := range []string{"claudemux-ask.sh", "claudemux-worktree.sh"} {
		if _, err := os.Stat(filepath.Join(hooksDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s was created (err=%v); only files that already existed get a shim", name, err)
		}
	}
}

// The shim has to be a faithful pass-through: same stdin, same stdout, same
// exit code — and a binary path with a space or a quote in it must survive
// the shell.
func TestLegacyShimPassesThrough(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := filepath.Join(t.TempDir(), "it's a dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "claudemux-head")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"args:$*\"\ncat\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "claudemux-ask.sh")
	if err := os.WriteFile(shim, legacyShim(fake, "hook ask"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shim)
	cmd.Stdin = strings.NewReader(`{"payload":1}`)
	out, err := cmd.Output()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 7 {
		t.Fatalf("shim exit = %v, want the wrapped command's 7", err)
	}
	if string(out) != "args:hook ask\n{\"payload\":1}" {
		t.Errorf("shim stdout = %q", out)
	}
}

// statusLineCommand digs the registered command back out of a settings map.
func statusLineCommand(t *testing.T, settings map[string]any) (string, bool) {
	t.Helper()
	sl, ok := settings["statusLine"].(map[string]any)
	if !ok {
		return "", false
	}
	cmd, ok := sl["command"].(string)
	return cmd, ok
}

func TestSetStatusLineClaimsWhenAbsent(t *testing.T) {
	settings := map[string]any{}
	var errBuf bytes.Buffer
	if !setStatusLine(settings, "/h/.claude/hooks/claudemux-statusline", &errBuf) {
		t.Fatal("setStatusLine reported no change, want it to claim an empty slot")
	}
	cmd, ok := statusLineCommand(t, settings)
	if !ok || cmd != "/h/.claude/hooks/claudemux-statusline" {
		t.Errorf("statusLine command = %q (ok=%v), want ours", cmd, ok)
	}
	sl := settings["statusLine"].(map[string]any)
	if sl["type"] != "command" {
		t.Errorf("statusLine type = %v, want \"command\"", sl["type"])
	}
}

// abtop's shim is exactly what we are replacing, matched by basename so the
// user's install prefix does not matter.
func TestSetStatusLineReplacesAbtop(t *testing.T) {
	settings := map[string]any{"statusLine": map[string]any{
		"type": "command", "command": "/Users/x/.claude/abtop-statusline.sh",
	}}
	var errBuf bytes.Buffer
	if !setStatusLine(settings, "/h/claudemux-statusline", &errBuf) {
		t.Fatal("setStatusLine reported no change, want it to replace abtop's shim")
	}
	if cmd, _ := statusLineCommand(t, settings); cmd != "/h/claudemux-statusline" {
		t.Errorf("statusLine command = %q, want ours", cmd)
	}
}

// A statusline someone actually built is never clobbered. The user is told why
// their meters are dark instead.
func TestSetStatusLineLeavesThirdPartyAlone(t *testing.T) {
	settings := map[string]any{"statusLine": map[string]any{
		"type": "command", "command": "/Users/x/bin/my-fancy-statusline",
	}}
	var errBuf bytes.Buffer
	if setStatusLine(settings, "/h/claudemux-statusline", &errBuf) {
		t.Fatal("setStatusLine reported a change, want a third-party slot untouched")
	}
	if cmd, _ := statusLineCommand(t, settings); cmd != "/Users/x/bin/my-fancy-statusline" {
		t.Errorf("statusLine command = %q, want it unchanged", cmd)
	}
	if !strings.Contains(errBuf.String(), "my-fancy-statusline") {
		t.Errorf("stderr = %q, want it to name the command it declined to replace", errBuf.String())
	}
}

// Already ours: no change, no write, no noise. hook ensure runs on every
// launch, so this is the overwhelmingly common case.
func TestSetStatusLineIdempotent(t *testing.T) {
	settings := map[string]any{"statusLine": map[string]any{
		"type": "command", "command": "/h/claudemux-statusline",
	}}
	var errBuf bytes.Buffer
	if setStatusLine(settings, "/h/claudemux-statusline", &errBuf) {
		t.Fatal("setStatusLine reported a change on a slot it already owns")
	}
	if errBuf.String() != "" {
		t.Errorf("stderr = %q, want silence", errBuf.String())
	}
}

// The statusline command from before the installed binary was renamed is
// ours, not a third party's: it is taken over, silently.
func TestSetStatusLineTakesOverLegacy(t *testing.T) {
	settings := map[string]any{"statusLine": map[string]any{
		"type": "command", "command": "/h/claudemux-statusline statusline",
	}}
	var errBuf bytes.Buffer
	if !setStatusLine(settings, "/h/claudemux-head statusline", &errBuf, "/h/claudemux-statusline statusline") {
		t.Fatal("setStatusLine reported no change, want it to take over the legacy command")
	}
	if cmd, _ := statusLineCommand(t, settings); cmd != "/h/claudemux-head statusline" {
		t.Errorf("statusLine command = %q, want ours", cmd)
	}
	if errBuf.String() != "" {
		t.Errorf("stderr = %q, want silence", errBuf.String())
	}
}

// Taking over a slot we own swaps the command, not the object: anything else
// the user set on their statusLine, such as padding, stays.
func TestSetStatusLineTakeoverKeepsOtherKeys(t *testing.T) {
	settings := map[string]any{"statusLine": map[string]any{
		"type": "command", "command": "/h/claudemux-statusline statusline", "padding": 2.0,
	}}
	var errBuf bytes.Buffer
	if !setStatusLine(settings, "/h/claudemux-head statusline", &errBuf, "/h/claudemux-statusline statusline") {
		t.Fatal("setStatusLine reported no change, want it to take over the legacy command")
	}
	sl := settings["statusLine"].(map[string]any)
	if sl["command"] != "/h/claudemux-head statusline" || sl["type"] != "command" {
		t.Errorf("statusLine = %v, want our command", sl)
	}
	if sl["padding"] != 2.0 {
		t.Errorf("statusLine padding = %v, want the user's 2 kept", sl["padding"])
	}
}

// Claude Code runs every hook command through a shell, so the binary path has
// to be quoted for it: shQuote must round-trip anything a HOME can contain.
func TestShQuoteRoundTrips(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	for _, s := range []string{"/plain/path", "/with space/x", "/it's/x", `/a"b$c\d` + "`e`", "''", ""} {
		out, err := exec.Command("sh", "-c", "printf %s "+shQuote(s)).Output()
		if err != nil {
			t.Fatalf("sh -c with shQuote(%q): %v", s, err)
		}
		if string(out) != s {
			t.Errorf("shQuote(%q) came back from sh as %q", s, out)
		}
	}
}

// A HOME with a space (and a quote) in it: every registered command still runs
// the installed binary, because the path is quoted for the shell Claude Code
// runs it through.
func TestHookEnsureQuotesTheBinaryPath(t *testing.T) {
	home := filepath.Join(t.TempDir(), "it's my home")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	p := filepath.Join(home, ".claude", "settings.json")

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0 (stderr %s)", code, stderr.String())
	}
	s := readSettings(t, p)
	bin := installedHookBinary(t)
	if got := hookCommands(t, s, "PermissionRequest"); !sameStrings(got, shQuote(bin)+" hook ask") {
		t.Errorf("PermissionRequest = %v, want the quoted %s hook ask", got, bin)
	}
	if cmd, _ := statusLineCommand(t, s); cmd != shQuote(bin)+" statusline" {
		t.Errorf("statusLine = %q, want the quoted %s statusline", cmd, bin)
	}

	// And the shell really does run the right file with the right arguments.
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	fake := filepath.Join(t.TempDir(), "a dir's name", hookBinaryName)
	if err := os.MkdirAll(filepath.Dir(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"args:$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-c", hookCommandFor(fake, claudemuxHooks[0])).Output()
	if err != nil {
		t.Fatalf("sh -c %q: %v", hookCommandFor(fake, claudemuxHooks[0]), err)
	}
	if string(out) != "args:hook "+claudemuxHooks[0].name+"\n" {
		t.Errorf("hook command printed %q", out)
	}
}

// Releases before the path was quoted registered the bare form. Those entries
// are ours and are replaced, not left beside the quoted ones to run twice.
func TestHookEnsureMigratesUnquotedCommands(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	hooksDir := filepath.Join(home, ".claude", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	bin := filepath.Join(hooksDir, hookBinaryName)
	p := filepath.Join(home, ".claude", "settings.json")
	if err := os.WriteFile(p, []byte(`{
  "statusLine": {"type": "command", "command": "`+bin+` statusline", "padding": 2},
  "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "`+bin+` hook map"}]}],
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": "`+bin+` hook map"}]},
      {"hooks": [{"type": "command", "command": "`+bin+` hook worktree"}]},
      {"hooks": [{"type": "command", "command": "`+bin+` hook ask"}]}
    ],
    "PreToolUse": [{"matcher": "AskUserQuestion", "hooks": [{"type": "command", "command": "`+bin+` hook ask"}]}]
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0 (stderr %s)", code, stderr.String())
	}
	s := readSettings(t, p)
	want := map[string][]string{
		"SessionStart":      {hookCmd(t, "map")},
		"UserPromptSubmit":  {hookCmd(t, "map"), hookCmd(t, "worktree"), hookCmd(t, "ask")},
		"PreToolUse":        {hookCmd(t, "ask")},
		"PostToolUse":       {hookCmd(t, "ask")},
		"PermissionRequest": {hookCmd(t, "ask")},
	}
	for event, cmds := range want {
		if got := hookCommands(t, s, event); !sameStrings(got, cmds...) {
			t.Errorf("%s = %v, want %v", event, got, cmds)
		}
	}
	sl, _ := s["statusLine"].(map[string]any)
	if sl["command"] != shQuote(bin)+" statusline" {
		t.Errorf("statusLine command = %v, want the quoted form", sl["command"])
	}
	if sl["padding"] != 2.0 {
		t.Errorf("statusLine padding = %v, want it kept", sl["padding"])
	}

	before, _ := os.ReadFile(p)
	var again bytes.Buffer
	if code := runHookEnsure(nil, &again, &again); code != 0 {
		t.Fatalf("second runHookEnsure() = %d (stderr %s)", code, again.String())
	}
	if after, _ := os.ReadFile(p); !bytes.Equal(before, after) {
		t.Error("settings.json changed on the run after the migration")
	}
	if again.Len() != 0 {
		t.Errorf("second run output = %q, want nothing", again.String())
	}
}

// settings.json is replaced atomically, and a user who locked it down to 0600
// keeps it that way.
func TestHookEnsureKeepsSettingsMode(t *testing.T) {
	p := writeSettings(t, `{"model": "opus"}`)
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runHookEnsure(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0 (stderr %s)", code, stderr.String())
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("settings.json mode = %v, want the user's 0600 kept", info.Mode().Perm())
	}
	if readSettings(t, p)["model"] != "opus" {
		t.Error("model lost")
	}
}

// A legacy file that cannot be replaced by its shim is reported (exit 4, the
// file named) but costs nothing else: settings.json is still migrated and the
// skills are still installed.
func TestHookEnsureShimFailureStillRegistersAndInstallsSkills(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	hooksDir := filepath.Join(home, ".claude", "hooks")
	t.Setenv("HOME", home)
	// A directory where the old script was: it "exists", so a shim is due,
	// and no file can be renamed over it.
	blocked := filepath.Join(hooksDir, "claudemux-ask.sh")
	if err := os.MkdirAll(filepath.Join(blocked, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, ".claude", "settings.json")
	if err := os.WriteFile(p, []byte(legacySettings(hooksDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure(nil, &stdout, &stderr); code != 4 {
		t.Fatalf("runHookEnsure() = %d, want 4 (stderr %s)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), blocked) {
		t.Errorf("stderr = %q, want it to name %s", stderr.String(), blocked)
	}
	if !strings.Contains(stderr.String(), "registered") {
		t.Errorf("stderr = %q, want it to say the hooks are registered", stderr.String())
	}
	if got := hookCommands(t, readSettings(t, p), "PermissionRequest"); !sameStrings(got, hookCmd(t, "ask")) {
		t.Errorf("PermissionRequest = %v, want settings.json migrated anyway", got)
	}
	skills, err := os.ReadDir(filepath.Join(home, ".claude", "skills"))
	if err != nil || len(skills) == 0 {
		t.Errorf("skills not installed (entries %v, err %v)", skills, err)
	}
}

// The installed binary is this whole ~16MB binary, and Claude Code executes it
// on every statusline render and every hook of every session on the machine. An in-place
// rewrite therefore races those renders, and losing the race means ETXTBSY —
// at which point runHookEnsure returns 4 BEFORE setStatusLine runs and before
// settings.json is written, so a single unlucky launch silently drops its
// whole hook registration. Replacing the file by rename cannot fail that way:
// rename needs write permission on the DIRECTORY, not on the file, and it
// leaves any process already executing the old inode running it untouched.
//
// ETXTBSY cannot be produced portably from a test. A read-only destination is
// the same shape of failure — the existing file cannot be opened for writing —
// and the same fix distinguishes them.
func TestHookEnsureInstallsOverAnUnwritableStatusline(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the file mode this test depends on")
	}
	settingsPath := writeSettings(t, "")

	var b1 bytes.Buffer
	if code := runHookEnsure(nil, &b1, &b1); code != 0 {
		t.Fatalf("first runHookEnsure() = %d, want 0 (stderr %s)", code, b1.String())
	}
	home, _ := os.UserHomeDir()
	slPath := filepath.Join(home, ".claude", "hooks", hookBinaryName)
	wantBytes, err := os.ReadFile(slPath)
	if err != nil {
		t.Fatalf("statusline artifact not installed at %s: %v", slPath, err)
	}

	// An artifact from an older build: different bytes, so the copy is due —
	// and unwritable, standing in for one being executed right now.
	if err := os.WriteFile(slPath, []byte("#!/bin/sh\n# an older build\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(slPath, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(slPath, 0o755) })

	// Drop the statusLine claim so this run has to make it again — that is the
	// registration a failed copy silently takes down with it.
	settings := readSettings(t, settingsPath)
	delete(settings, "statusLine")
	out, marshalErr := json.MarshalIndent(settings, "", "  ")
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if err := os.WriteFile(settingsPath, out, 0o644); err != nil {
		t.Fatal(err)
	}

	var b2 bytes.Buffer
	if code := runHookEnsure(nil, &b2, &b2); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0: a statusline artifact that cannot be opened for writing must be REPLACED, not fail the whole run (stderr %s)", code, b2.String())
	}
	sl, _ := readSettings(t, settingsPath)["statusLine"].(map[string]any)
	if cmd, _ := sl["command"].(string); cmd == "" {
		t.Error("statusLine was not registered: the failed copy took the whole registration down with it")
	}
	got, err := os.ReadFile(slPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wantBytes) {
		t.Errorf("the stale artifact is still in place (%d bytes, want the %d-byte current binary)", len(got), len(wantBytes))
	}
	if info, err := os.Stat(slPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755: a statusline Claude Code cannot execute is no better than a stale one", info.Mode().Perm())
	}
}

// The other half of replace-by-rename: a render already executing the old
// artifact keeps reading the old bytes, and no half-written file is ever
// visible at the destination path.
func TestCopyExecutableIfChangedLeavesTheOldInodeIntact(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("the new build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("the old build"), 0o755); err != nil {
		t.Fatal(err)
	}

	inFlight, err := os.Open(dst) // the render that is already running
	if err != nil {
		t.Fatal(err)
	}
	defer inFlight.Close()

	if err := copyExecutableIfChanged(src, dst); err != nil {
		t.Fatalf("copyExecutableIfChanged: %v", err)
	}

	buf := make([]byte, len("the old build"))
	if _, err := inFlight.Read(buf); err != nil {
		t.Fatalf("reading the in-flight handle: %v", err)
	}
	if string(buf) != "the old build" {
		t.Errorf("the in-flight handle now reads %q: the running artifact was rewritten under it", buf)
	}
	if got, _ := os.ReadFile(dst); string(got) != "the new build" {
		t.Errorf("dst = %q, want the new build", got)
	}

	// Nothing left behind: a temp file that survives becomes a second ~16MB
	// copy in ~/.claude/hooks on every launch.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("%d files in the directory, want just src and dst: %v", len(entries), entries)
	}
}
