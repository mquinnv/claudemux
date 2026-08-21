package main

import (
	"bytes"
	"encoding/json"
	"os"
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

// stubScript creates fake shipped scripts in one directory and returns the
// path to claudemux-map.sh, for --script to point at. Every script in
// hookScripts is written, because hook ensure resolves the others as siblings
// of the --script path — a directory holding only the map script is not a
// layout that occurs in any real install.
func stubScript(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, hs := range hookScripts {
		p := filepath.Join(dir, hs.name)
		if err := os.WriteFile(p, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, hookScriptName)
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

func TestHookEnsureFreshInstall(t *testing.T) {
	p := writeSettings(t, "") // no settings.json at all
	script := stubScript(t)

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure([]string{"--script", script}, &stdout, &stderr); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0; stderr=%q", code, stderr.String())
	}

	s := readSettings(t, p)

	// SessionStart carries only the pane-map hook: the worktree hook has
	// nothing to record before a prompt exists.
	start := hookCommands(t, s, "SessionStart")
	if len(start) != 1 || filepath.Base(start[0]) != "claudemux-map.sh" {
		t.Errorf("SessionStart = %v, want exactly claudemux-map.sh", start)
	}

	// UserPromptSubmit carries all three scripts, and only those: an exact
	// count catches a duplicate-registration bug that a "contains" check
	// would miss.
	ups := hookCommands(t, s, "UserPromptSubmit")
	if len(ups) != 3 {
		t.Fatalf("UserPromptSubmit commands = %v, want exactly 3 entries", ups)
	}
	sawMap := false
	for _, c := range ups {
		if filepath.Base(c) == "claudemux-map.sh" {
			sawMap = true
		}
	}
	if !sawMap {
		t.Errorf("UserPromptSubmit = %v, want claudemux-map.sh registered", ups)
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

// The statusline artifact is this whole binary (potentially tens of MB), and
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
func TestHookEnsureDoesNotRewriteUnchangedStatusline(t *testing.T) {
	writeSettings(t, "")
	script := stubScript(t)

	var b1, b2 bytes.Buffer
	if code := runHookEnsure([]string{"--script", script}, &b1, &b1); code != 0 {
		t.Fatalf("first runHookEnsure() = %d, want 0 (stderr %s)", code, b1.String())
	}

	home, _ := os.UserHomeDir()
	slPath := filepath.Join(home, ".claude", "hooks", statuslineScriptName)
	wantBytes, err := os.ReadFile(slPath)
	if err != nil {
		t.Fatalf("statusline artifact not installed at %s: %v", slPath, err)
	}

	if err := os.Chmod(slPath, 0o444); err != nil {
		t.Fatalf("chmod %s read-only: %v", slPath, err)
	}
	// Restore write permission before t.TempDir()'s own cleanup tries to
	// remove the directory tree; t.Cleanup runs LIFO, so this (registered
	// after writeSettings/stubScript's t.TempDir calls) runs first.
	t.Cleanup(func() { _ = os.Chmod(slPath, 0o755) })

	if code := runHookEnsure([]string{"--script", script}, &b2, &b2); code != 0 {
		t.Fatalf("second runHookEnsure() = %d, want 0 — the write must be skipped when the artifact is already current, even with the destination read-only (stderr %s)", code, b2.String())
	}

	got, err := os.ReadFile(slPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wantBytes) {
		t.Error("statusline artifact content changed after the second run, want it untouched")
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

	// An unrelated hook on an event we also register on must survive
	// alongside ours (the ask hook now claims PreToolUse too).
	pre := hookCommands(t, s, "PreToolUse")
	var sawUnrelated, sawAsk bool
	for _, c := range pre {
		if c == "/unrelated/event.sh" {
			sawUnrelated = true
		}
		if filepath.Base(c) == "claudemux-ask.sh" {
			sawAsk = true
		}
	}
	if !sawUnrelated || !sawAsk || len(pre) != 2 {
		t.Errorf("PreToolUse = %v, want the unrelated hook preserved beside claudemux-ask.sh", pre)
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

func TestHookEnsureRegistersAllScripts(t *testing.T) {
	settingsPath := writeSettings(t, "")
	script := stubScript(t)

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure([]string{"--script", script}, &stdout, &stderr); code != 0 {
		t.Fatalf("hook ensure = %d, want 0 (stderr %s)", code, stderr.String())
	}

	settings := readSettings(t, settingsPath)
	ups := hookCommands(t, settings, "UserPromptSubmit")
	if len(ups) != 3 {
		t.Fatalf("UserPromptSubmit commands = %v, want 3 entries", ups)
	}
	var sawMap, sawWorktree, sawAsk bool
	for _, c := range ups {
		switch filepath.Base(c) {
		case "claudemux-map.sh":
			sawMap = true
		case "claudemux-worktree.sh":
			sawWorktree = true
		case "claudemux-ask.sh":
			sawAsk = true
		}
	}
	if !sawMap || !sawWorktree || !sawAsk {
		t.Errorf("UserPromptSubmit = %v, want all three scripts", ups)
	}

	// The worktree hook has nothing to record at session start; registering it
	// on SessionStart would inject its instruction before a prompt exists.
	start := hookCommands(t, settings, "SessionStart")
	if len(start) != 1 || filepath.Base(start[0]) != "claudemux-map.sh" {
		t.Errorf("SessionStart = %v, want only claudemux-map.sh", start)
	}

	// The ask hook needs the tool-call events, and must be alone there: no
	// other claudemux script has business running on every tool call.
	for _, event := range []string{"PreToolUse", "PostToolUse"} {
		cmds := hookCommands(t, settings, event)
		if len(cmds) != 1 || filepath.Base(cmds[0]) != "claudemux-ask.sh" {
			t.Errorf("%s = %v, want only claudemux-ask.sh", event, cmds)
		}
		// Without a matcher this hook would run on every tool call of every
		// session, spawning bash+jq twice per call just to exit at the
		// tool-name check.
		matcher, hasKey := hookGroupMatcher(t, settings, event, cmds[0])
		if !hasKey || matcher != "AskUserQuestion" {
			t.Errorf("%s claudemux-ask.sh matcher = %q (present=%v), want \"AskUserQuestion\"", event, matcher, hasKey)
		}
	}

	// UserPromptSubmit is not a tool-call event: none of the three scripts'
	// entries there may carry a matcher key, empty or otherwise.
	for _, c := range ups {
		if matcher, hasKey := hookGroupMatcher(t, settings, "UserPromptSubmit", c); hasKey {
			t.Errorf("UserPromptSubmit %s has matcher %q, want no matcher key at all", filepath.Base(c), matcher)
		}
	}
}

func TestHookEnsureDoesNotDuplicateOnSecondRun(t *testing.T) {
	settingsPath := writeSettings(t, "")
	script := stubScript(t)

	var stdout, stderr bytes.Buffer
	for i := 0; i < 2; i++ {
		if code := runHookEnsure([]string{"--script", script}, &stdout, &stderr); code != 0 {
			t.Fatalf("run %d: hook ensure = %d (stderr %s)", i, code, stderr.String())
		}
	}

	settings := readSettings(t, settingsPath)
	if got := hookCommands(t, settings, "UserPromptSubmit"); len(got) != 3 {
		t.Errorf("UserPromptSubmit after two runs = %v, want 3 entries", got)
	}
}

// If a later shipped script is missing, nothing must be copied and
// settings.json must stay untouched — including the earlier script(s) that
// would otherwise have resolved and copied fine. A half-done install (one
// script live, settings unregistered) is worse than none: the old
// single-script code could not produce that state, and the multi-script loop
// must not either.
func TestHookEnsureMissingScriptCopiesNothing(t *testing.T) {
	writeSettings(t, "") // no settings.json at all

	// Lay down only the FIRST shipped script; the second is missing — exactly
	// the install.sh/release.yml gap this task's own addendum fixed.
	dir := t.TempDir()
	first := hookScripts[0]
	if err := os.WriteFile(filepath.Join(dir, first.name), []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, hookScriptName)

	var stdout, stderr bytes.Buffer
	code := runHookEnsure([]string{"--script", script}, &stdout, &stderr)
	if code != 4 {
		t.Fatalf("runHookEnsure() = %d, want 4 when a shipped script is missing (stderr %s)", code, stderr.String())
	}

	home, _ := os.UserHomeDir()

	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Errorf("settings.json exists (err=%v), want it never written when a shipped script is missing", err)
	}

	hooksDir := filepath.Join(home, ".claude", "hooks")
	for _, hs := range hookScripts {
		if _, err := os.Stat(filepath.Join(hooksDir, hs.name)); !os.IsNotExist(err) {
			t.Errorf("%s was copied to %s despite a missing shipped script — a partial install is worse than none", hs.name, hooksDir)
		}
	}

	// The statusline artifact must be absent too. The current code is safe by
	// ordering — validation returns 4 strictly before any copy, including the
	// statusline's — but that guarantee is only real if a test pins it; without
	// this a future reordering that copied the statusline before validation
	// finished would go undetected.
	if _, err := os.Stat(filepath.Join(hooksDir, statuslineScriptName)); !os.IsNotExist(err) {
		t.Errorf("%s was copied to %s despite a missing shipped script — a partial install is worse than none", statuslineScriptName, hooksDir)
	}
}

// The development layout keeps the shipped scripts in the repo and puts only a
// symlinked `claudemux` on PATH, while claudemux-head is a `go install` output
// in GOBIN — so the scripts are NOT siblings of the running binary. Resolution
// must fall through to the claudemux on PATH, exactly as shippedScriptPath
// already does for project-color-resolve.sh.
//
// This is a regression test with a real scar: hook ensure resolved siblings of
// the binary only. That was survivable while a missing script merely skipped one
// hook, and stopped being survivable once validate-all-before-copy-any made any
// single miss fatal — a dev checkout then registered NOTHING, losing the
// pane-map hook along with the worktree hook.
func TestHookEnsureResolvesScriptsViaClaudemuxOnPath(t *testing.T) {
	writeSettings(t, "")

	// Mirror the REPO's shape, not a flat directory: claudemux in bin/, the hook
	// scripts in hooks/ beside it. A flat layout passes even without the hooks/
	// candidate — which is exactly how the first attempt at this fix shipped
	// green and still failed on a real checkout.
	repo := t.TempDir()
	binDir := filepath.Join(repo, "bin")
	hooksDir := filepath.Join(repo, "hooks")
	for _, d := range []string{binDir, hooksDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(binDir, "claudemux"),
		[]byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, hs := range hookScripts {
		if err := os.WriteFile(filepath.Join(hooksDir, hs.name),
			[]byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pathDir := binDir
	t.Setenv("PATH", pathDir)

	var stdout, stderr bytes.Buffer
	if code := runHookEnsure(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("runHookEnsure() = %d, want 0 in the development layout (stderr %s)", code, stderr.String())
	}

	home, _ := os.UserHomeDir()
	settings := readSettings(t, filepath.Join(home, ".claude", "settings.json"))
	ups := hookCommands(t, settings, "UserPromptSubmit")
	if len(ups) != 3 {
		t.Errorf("UserPromptSubmit = %v, want all three scripts registered from the PATH-resolved layout", ups)
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
