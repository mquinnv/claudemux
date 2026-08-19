package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// writeConfig points XDG_CONFIG_HOME at a temp dir and writes config.yml into
// it, returning nothing — tests then call loadConfig(), which resolves that
// path itself. Passing the path explicitly would not exercise configDir().
func writeConfig(t *testing.T, contents string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "claudemux")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", root)
}

func TestLoadConfigMissingFileYieldsDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // empty dir: no config.yml

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v, want nil — a missing config file is the common case for a fresh install and must not be an error", err)
	}
	if !cfg.Summary.Enabled {
		t.Error("Summary.Enabled = false, want true by default")
	}
	if cfg.Summary.Model != "claude-haiku-4-5" {
		t.Errorf("Summary.Model = %q, want %q", cfg.Summary.Model, "claude-haiku-4-5")
	}
	if cfg.Summary.MinInterval.Duration != 20*time.Second {
		t.Errorf("Summary.MinInterval = %v, want 20s", cfg.Summary.MinInterval.Duration)
	}
}

// A file naming only ONE key must not reset its siblings to their zero values.
// This is the bug that makes `enabled` silently false when a user sets only a model.
func TestLoadConfigPartialFileKeepsDefaultsForAbsentKeys(t *testing.T) {
	writeConfig(t, "summary:\n  model: claude-sonnet-5\n")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.Summary.Model != "claude-sonnet-5" {
		t.Errorf("Summary.Model = %q, want the value from the file", cfg.Summary.Model)
	}
	if !cfg.Summary.Enabled {
		t.Error("Summary.Enabled = false, want true — a file that does not mention `enabled` must leave the default alone, not zero it")
	}
	if cfg.Summary.MinInterval.Duration != 20*time.Second {
		t.Errorf("Summary.MinInterval = %v, want the 20s default preserved", cfg.Summary.MinInterval.Duration)
	}
}

func TestLoadConfigParsesDurationString(t *testing.T) {
	writeConfig(t, "summary:\n  min_interval: 45s\n")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.Summary.MinInterval.Duration != 45*time.Second {
		t.Errorf("Summary.MinInterval = %v, want 45s", cfg.Summary.MinInterval.Duration)
	}
}

func TestLoadConfigMalformedYAMLIsFatal(t *testing.T) {
	writeConfig(t, "summary:\n  enabled: [not, a, bool\n")

	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() error = nil, want an error — silently running on defaults after a typo hides what the user asked for")
	}
}

// A negative floor parses fine but silently REMOVES the rate limit on billable
// API calls (canSummarize's `elapsed >= interval` is unconditionally true below
// zero) — the exact opposite of what typing a floor means. It must be rejected.
func TestLoadConfigNegativeMinIntervalIsFatal(t *testing.T) {
	writeConfig(t, "summary:\n  min_interval: -5s\n")

	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() error = nil, want an error — a negative min_interval removes the rate limit on the user's own API key rather than setting one")
	}
}

// Zero is the honest way to say "no floor" and must remain a legal opt-out —
// rejecting it would deny a deliberate choice.
func TestLoadConfigZeroMinIntervalIsAllowed(t *testing.T) {
	writeConfig(t, "summary:\n  min_interval: 0s\n")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v, want nil — 0 is a legitimate opt-out from the rate floor", err)
	}
	if cfg.Summary.MinInterval.Duration != 0 {
		t.Errorf("Summary.MinInterval = %v, want 0", cfg.Summary.MinInterval.Duration)
	}
}

// api_key_file defaults to <config dir>/env, resolved to an absolute path so
// that `config get summary.api_key_file` prints the file actually consulted
// rather than a blank the reader has to know how to expand.
func TestLoadConfigAPIKeyFileDefaultsToConfigDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root) // no config.yml

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "claudemux", "env")
	if cfg.Summary.APIKeyFile != want {
		t.Errorf("Summary.APIKeyFile = %q, want %q", cfg.Summary.APIKeyFile, want)
	}
}

// The secret can live anywhere — including a path a secret manager already
// mounts a FIFO at, which is the whole reason this is a path and not the key.
func TestLoadConfigAPIKeyFileHonorsExplicitPathAndExpandsTilde(t *testing.T) {
	writeConfig(t, "summary:\n  api_key_file: ~/.config/claudemux/env\n")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "claudemux", "env")
	if cfg.Summary.APIKeyFile != want {
		t.Errorf("Summary.APIKeyFile = %q, want %q — a leading ~ must expand, or the path is read literally and never found", cfg.Summary.APIKeyFile, want)
	}
}

func TestLoadConfigInvalidDurationIsFatal(t *testing.T) {
	writeConfig(t, "summary:\n  min_interval: 20 fortnights\n")

	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() error = nil, want an error for an unparseable duration")
	}
}

func TestLoadConfigUnknownKeyIsFatal(t *testing.T) {
	writeConfig(t, "sumary:\n  enabled: true\n") // typo: "sumary"

	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() error = nil, want an error — a typoed key must not be silently ignored")
	}
}

func TestLoadConfigEmptyFileYieldsDefaults(t *testing.T) {
	writeConfig(t, "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v, want nil for an empty file", err)
	}
	if !cfg.Summary.Enabled {
		t.Error("Summary.Enabled = false, want the default true for an empty file")
	}
}

func TestLoadConfigReadsOnePasswordAccounts(t *testing.T) {
	writeConfig(t, "onepassword:\n  default_account: my.1password.com\n  accounts:\n    acme: acme.1password.com\n")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if got := cfg.OnePassword.Accounts["acme"]; got != "acme.1password.com" {
		t.Errorf("OnePassword.Accounts[acme] = %q, want %q", got, "acme.1password.com")
	}
	if cfg.OnePassword.DefaultAccount != "my.1password.com" {
		t.Errorf("OnePassword.DefaultAccount = %q", cfg.OnePassword.DefaultAccount)
	}
}

func TestConfigDirPrefersXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")

	dir, err := configDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join("/xdg", "claudemux") {
		t.Errorf("configDir() = %q, want /xdg/claudemux-head", dir)
	}
}

// os.UserConfigDir() returns ~/Library/Application Support on macOS. claudemux-head
// must use ~/.config there, so configDir must be hand-rolled.
func TestConfigDirFallsBackToDotConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")

	dir, err := configDir()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "claudemux")
	if dir != want {
		t.Errorf("configDir() = %q, want %q — not os.UserConfigDir(), which is ~/Library/Application Support on macOS", dir, want)
	}
}

func TestTabTitleDefaultsTrue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config.yml

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Summary.TabTitle {
		t.Error("Summary.TabTitle = false, want true by default")
	}
}

func TestTabTitleCanBeDisabled(t *testing.T) {
	writeConfig(t, "summary:\n  tab_title: false\n")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Summary.TabTitle {
		t.Error("Summary.TabTitle = true, want false when set false in the file")
	}
	// Setting only tab_title must not disturb the other summary defaults.
	if !cfg.Summary.Enabled {
		t.Error("Summary.Enabled = false — a partial file zeroed an unrelated default")
	}
	if cfg.Summary.Model != "claude-haiku-4-5" {
		t.Errorf("Summary.Model = %q, want the default preserved", cfg.Summary.Model)
	}
}

func TestDurationMarshalYAML(t *testing.T) {
	cfg := Config{
		Summary: SummaryConfig{
			Enabled:     true,
			Model:       "claude-haiku-4-5",
			MinInterval: Duration{20 * time.Second},
		},
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}

	yamlStr := string(data)
	if !strings.Contains(yamlStr, "min_interval: 20s") {
		t.Errorf("marshalled YAML does not contain 'min_interval: 20s'.\nGot:\n%s", yamlStr)
	}

	// Verify it does NOT serialize as nanoseconds (the bug would look like this)
	if strings.Contains(yamlStr, "20000000000") {
		t.Errorf("marshalled YAML serialized Duration as nanoseconds instead of string form.\nGot:\n%s", yamlStr)
	}
}

func TestAutoWorktreeDefaultsFalse(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config.yml

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Launch.AutoWorktree {
		t.Error("Launch.AutoWorktree = true, want false by default — launching into a surprise worktree must be opt-in")
	}
}

func TestAutoWorktreeCanBeEnabled(t *testing.T) {
	writeConfig(t, "launch:\n  auto_worktree: true\n")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if !cfg.Launch.AutoWorktree {
		t.Error("Launch.AutoWorktree = false, want true when set in the file")
	}
	// A file naming only launch keys must not zero the summary defaults.
	if !cfg.Summary.Enabled {
		t.Error("Summary.Enabled = false — a partial file zeroed an unrelated default")
	}
}

func TestAutoWorktreeUnknownKeyUnderLaunchIsFatal(t *testing.T) {
	writeConfig(t, "launch:\n  auto_worktre: true\n") // typo: missing final e

	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() error = nil, want an error — a typo under launch: must fail loudly, not silently behave as unset")
	}
}

// Empty is "never chose" and must be legal — the launcher's onboarding probe
// depends on an unset layout resolving without error.
func TestLaunchLayoutDefaultsEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config.yml

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Launch.Layout != "" {
		t.Errorf("Launch.Layout = %q, want empty by default", cfg.Launch.Layout)
	}
}

func TestLaunchLayoutCanBeSet(t *testing.T) {
	writeConfig(t, "launch:\n  layout: no-shell\n")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.Launch.Layout != "no-shell" {
		t.Errorf("Launch.Layout = %q, want %q", cfg.Launch.Layout, "no-shell")
	}
}

// Every legal layout name must round-trip through loadConfig without tripping
// validate() — a regression here would reject a value the picker TUI can
// legitimately write.
func TestLaunchLayoutAllLegalValues(t *testing.T) {
	for _, layout := range []string{"shell-right", "no-shell", "shell-bottom", "head-bottom"} {
		writeConfig(t, "launch:\n  layout: "+layout+"\n")

		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig() error = %v for layout %q", err, layout)
		}
		if cfg.Launch.Layout != layout {
			t.Errorf("Launch.Layout = %q, want %q", cfg.Launch.Layout, layout)
		}
	}
}

// A layout name outside the fixed set parses fine as YAML but names nothing
// bin/claudemux knows how to build — reject it the same way an invalid
// min_interval is rejected, rather than letting the launcher fall back to a
// silent default.
func TestLaunchLayoutInvalidValueIsFatal(t *testing.T) {
	writeConfig(t, "launch:\n  layout: sideways\n")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("loadConfig() error = nil, want an error for an unrecognized launch.layout value")
	}
	if !strings.Contains(err.Error(), "launch.layout") {
		t.Errorf("error = %q, want it to mention launch.layout", err.Error())
	}
}

func TestTeardownCommandDefaultsToDone(t *testing.T) {
	cfg := defaultConfig()
	if cfg.Teardown.Command != "/done" {
		t.Errorf("default teardown.command = %q, want %q", cfg.Teardown.Command, "/done")
	}
}

// An explicitly empty command is a legal opt-out (press x becomes a gated
// exit-and-kill), so it must survive decoding rather than being re-defaulted.
func TestTeardownCommandEmptyStringIsPreserved(t *testing.T) {
	writeConfig(t, "teardown:\n  command: \"\"\n")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Teardown.Command != "" {
		t.Errorf("teardown.command = %q, want empty", cfg.Teardown.Command)
	}
}

func TestTeardownCommandOverride(t *testing.T) {
	writeConfig(t, "teardown:\n  command: /wrapup\n")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Teardown.Command != "/wrapup" {
		t.Errorf("teardown.command = %q, want /wrapup", cfg.Teardown.Command)
	}
	// Untouched blocks keep their defaults — the partial-decode contract.
	if cfg.Summary.Model != "claude-haiku-4-5" {
		t.Errorf("summary.model = %q, want default", cfg.Summary.Model)
	}
}

// Unset is "use the layout's own default" (30% beside claude, 20% below it),
// so empty has to survive validate() the way an unset layout does.
func TestLaunchShellPaneKeysDefaultEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config.yml

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Launch.ShellSize != "" {
		t.Errorf("Launch.ShellSize = %q, want empty by default", cfg.Launch.ShellSize)
	}
	if cfg.Launch.ShellCommand != "" {
		t.Errorf("Launch.ShellCommand = %q, want empty by default", cfg.Launch.ShellCommand)
	}
}

// Both spellings tmux's `split-window -l` accepts: a percentage of the space
// being split, and an absolute count of columns or rows.
func TestLaunchShellSizeAcceptsPercentAndCount(t *testing.T) {
	for _, size := range []string{"30%", "45%", "80", "5", "100%"} {
		writeConfig(t, "launch:\n  shell_size: \""+size+"\"\n")

		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig() error = %v for shell_size %q", err, size)
		}
		if cfg.Launch.ShellSize != size {
			t.Errorf("Launch.ShellSize = %q, want %q", cfg.Launch.ShellSize, size)
		}
	}
}

// Anything else would reach `tmux split-window -l` verbatim and fail at launch
// with tmux's own error rather than this file's — reject it here, where the
// message can name the key that is wrong.
func TestLaunchShellSizeInvalidValueIsFatal(t *testing.T) {
	for _, size := range []string{"wide", "30 %", "%30", "-10", "30%%", "1.5", "0"} {
		writeConfig(t, "launch:\n  shell_size: \""+size+"\"\n")

		_, err := loadConfig()
		if err == nil {
			t.Fatalf("loadConfig() error = nil for shell_size %q, want an error", size)
		}
		if !strings.Contains(err.Error(), "launch.shell_size") {
			t.Errorf("error = %q, want it to mention launch.shell_size", err.Error())
		}
	}
}

// An explicitly empty size is the same as an absent one — the layout default —
// not a value to reject.
func TestLaunchShellSizeEmptyStringIsLegal(t *testing.T) {
	writeConfig(t, "launch:\n  shell_size: \"\"\n")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v, want nil for an explicitly empty shell_size", err)
	}
	if cfg.Launch.ShellSize != "" {
		t.Errorf("Launch.ShellSize = %q, want empty", cfg.Launch.ShellSize)
	}
}

// The command is a shell command line, not a program name: it is spliced into
// the pane's command verbatim, so arguments, flags and quoting all have to
// survive decoding untouched.
func TestLaunchShellCommandRoundTrips(t *testing.T) {
	const cmd = `gh-hud --repo "$(basename $PWD)" -w`
	writeConfig(t, "launch:\n  shell_command: '"+cmd+"'\n")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.Launch.ShellCommand != cmd {
		t.Errorf("Launch.ShellCommand = %q, want %q", cfg.Launch.ShellCommand, cmd)
	}
}

// bin/claudemux reads both keys through `config get`, so a value that loads
// but cannot be resolved by its dotted path never reaches the launcher.
func TestConfigGetResolvesShellPaneKeys(t *testing.T) {
	writeConfig(t, "launch:\n  shell_size: 40%\n  shell_command: gh-hud\n")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	for path, want := range map[string]string{
		"launch.shell_size":    "40%",
		"launch.shell_command": "gh-hud",
	} {
		got, ok := configLookup(cfg, path)
		if !ok {
			t.Errorf("configLookup(%q) not found", path)
			continue
		}
		if got != want {
			t.Errorf("configLookup(%q) = %q, want %q", path, got, want)
		}
	}
}
