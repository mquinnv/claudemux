package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigLookupNestedScalar(t *testing.T) {
	cfg := defaultConfig()
	cfg.OnePassword.Accounts = map[string]string{"acme": "acme.1password.com"}

	got, ok := configLookup(cfg, "onepassword.accounts.acme")
	if !ok {
		t.Fatal("configLookup() ok = false, want true for a present nested key")
	}
	if got != "acme.1password.com" {
		t.Errorf("configLookup() = %q, want %q", got, "acme.1password.com")
	}
}

func TestConfigLookupAbsentKey(t *testing.T) {
	cfg := defaultConfig()

	if _, ok := configLookup(cfg, "onepassword.accounts.nope"); ok {
		t.Error("configLookup() ok = true, want false for an absent key")
	}
	if _, ok := configLookup(cfg, "no.such.path"); ok {
		t.Error("configLookup() ok = true, want false for an unknown top-level path")
	}
}

// A dotted path that lands on a map, not a scalar, has no printable value.
func TestConfigLookupMapIsNotAScalar(t *testing.T) {
	cfg := defaultConfig()
	cfg.OnePassword.Accounts = map[string]string{"acme": "acme.1password.com"}

	if _, ok := configLookup(cfg, "onepassword.accounts"); ok {
		t.Error("configLookup() ok = true for a map, want false — only scalars are printable")
	}
}

// The Duration wrapper must round-trip through YAML as "20s", not as a raw
// nanosecond count, or `config get summary.min_interval` prints 20000000000.
func TestConfigLookupDurationPrintsAsString(t *testing.T) {
	got, ok := configLookup(defaultConfig(), "summary.min_interval")
	if !ok {
		t.Fatal("configLookup() ok = false for summary.min_interval")
	}
	if got != "20s" {
		t.Errorf("configLookup() = %q, want %q — Duration must MarshalYAML as a duration string", got, "20s")
	}
}

func TestConfigLookupBool(t *testing.T) {
	got, ok := configLookup(defaultConfig(), "summary.enabled")
	if !ok {
		t.Fatal("configLookup() ok = false for summary.enabled")
	}
	if got != "true" {
		t.Errorf("configLookup() = %q, want %q", got, "true")
	}
}

func TestRunConfigGetPrintsValueAndExitsZero(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "claudemux")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "onepassword:\n  accounts:\n    acme: acme.1password.com\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", root)

	var stdout, stderr bytes.Buffer
	code := runConfigGet([]string{"onepassword.accounts.acme"}, &stdout, &stderr)

	if code != 0 {
		t.Errorf("runConfigGet() = %d, want 0; stderr=%q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "acme.1password.com" {
		t.Errorf("stdout = %q, want %q", got, "acme.1password.com")
	}
}

// claudemux calls this on EVERY launch, for orgs the user has not configured.
// An absent key must be a quiet exit 1, never a crash and never noise on stderr.
func TestRunConfigGetAbsentKeyExitsOneSilently(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config.yml at all

	var stdout, stderr bytes.Buffer
	code := runConfigGet([]string{"onepassword.accounts.acme"}, &stdout, &stderr)

	if code != 1 {
		t.Errorf("runConfigGet() = %d, want 1 for an absent key", code)
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty — an unconfigured org is normal, not an error to report", stderr.String())
	}
}

// Both branches of the arg-count guard, not just the zero-arg one: the guard is
// a single `len(args) != 1`, and a future narrowing of it to `== 0` would let
// `config get a b` through silently while still passing a zero-arg-only test.
func TestRunConfigGetWrongArgCount(t *testing.T) {
	for _, args := range [][]string{nil, {"a", "b"}} {
		var stdout, stderr bytes.Buffer

		if code := runConfigGet(args, &stdout, &stderr); code != 2 {
			t.Errorf("runConfigGet(%v) = %d, want 2 (usage error)", args, code)
		}
		if stderr.String() == "" {
			t.Errorf("runConfigGet(%v): stderr empty, want a usage message for a malformed invocation", args)
		}
	}
}

// A malformed config.yml is fatal for the TUI, and must also be fatal here —
// but distinguishable from "absent key" (exit 1) so claudemux's `|| true` does
// not mask a real config error into silence. Exit 3, with an explanation.
func TestRunConfigGetMalformedConfigExitsThree(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "claudemux")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("summary:\n  enabled: [bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", root)

	var stdout, stderr bytes.Buffer
	code := runConfigGet([]string{"summary.enabled"}, &stdout, &stderr)

	if code != 3 {
		t.Errorf("runConfigGet() = %d, want 3 for a malformed config", code)
	}
	if stderr.String() == "" {
		t.Error("stderr empty, want the parse error reported")
	}
}

func TestConfigGetLaunchAutoWorktree(t *testing.T) {
	writeConfig(t, "launch:\n  auto_worktree: true\n")

	var stdout, stderr bytes.Buffer
	code := runConfigGet([]string{"launch.auto_worktree"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "true" {
		t.Errorf("config get launch.auto_worktree = %q, want %q", got, "true")
	}
}

func TestConfigGetLaunchAutoWorktreeDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config.yml

	var stdout, stderr bytes.Buffer
	code := runConfigGet([]string{"launch.auto_worktree"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "false" {
		t.Errorf("config get launch.auto_worktree = %q, want %q — the key must resolve (exit 0) even when unset, so the launcher reads a definite false rather than an absent key", got, "false")
	}
}

// This is the launcher's "never chose" probe: bin/claudemux calls `config get
// launch.layout` on every launch to decide whether to offer onboarding, and
// needs a quiet exit 0 with nothing on stdout — not exit 1 — because an unset
// layout is a legal value (empty string), not an absent key.
func TestConfigGetLaunchLayoutDefaultIsEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config.yml

	var stdout, stderr bytes.Buffer
	code := runConfigGet([]string{"launch.layout"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "" {
		t.Errorf("stdout = %q, want empty for an unset launch.layout", got)
	}
}

func TestConfigGetLaunchLayoutSet(t *testing.T) {
	writeConfig(t, "launch:\n  layout: no-shell\n")

	var stdout, stderr bytes.Buffer
	code := runConfigGet([]string{"launch.layout"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "no-shell" {
		t.Errorf("config get launch.layout = %q, want %q", got, "no-shell")
	}
}

func TestConfigGetLaunchLayoutInvalidValueExitsThree(t *testing.T) {
	writeConfig(t, "launch:\n  layout: sideways\n")

	var stdout, stderr bytes.Buffer
	code := runConfigGet([]string{"launch.layout"}, &stdout, &stderr)
	if code != 3 {
		t.Errorf("exit code = %d, want 3 for an invalid launch.layout", code)
	}
	if !strings.Contains(stderr.String(), "launch.layout") {
		t.Errorf("stderr = %q, want it to mention launch.layout", stderr.String())
	}
}
