# claude-head Open-Source Release — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish `claude-head` (Go TUI) and `claude-env` (bash tmux launcher) together as an MIT-licensed open-source repo at `github.com/mquinnv/claude-head`, with XDG-compliant config and no machine- or employer-specific references in tracked files.

**Architecture:** A new `config.go` owns a YAML `Config` loaded from `$XDG_CONFIG_HOME/claude-head/config.yml` (falling back to `~/.config/claude-head`). Secrets stay in a separate line-oriented env file at the same directory, because it is served in production by a 1Password-mounted FIFO and a FIFO cannot serve structured YAML. A `claude-head config get <dotted.path>` subcommand lets the bash launcher read nested YAML without a second parser. `claude-env` moves into `bin/` with its hardcoded 1Password org map replaced by config lookups.

**Tech Stack:** Go 1.26, `gopkg.in/yaml.v3`, bubbletea/lipgloss (existing), bash, tmux.

## Global Constraints

- Module path is `github.com/mquinnv/claude-head`. The project is a single `package main` with no internal imports — the rename requires **no** import rewrites.
- Config directory is resolved by a hand-rolled `configDir()`. **Never use `os.UserConfigDir()`** — it returns `~/Library/Application Support` on macOS, which is the wrong directory and wrong on the primary dev machine.
- `XDG_CONFIG_HOME` *replaces* the `~/.config` default; it is not the first entry of a search path.
- Process environment beats the env file for `ANTHROPIC_API_KEY` / `ANTHROPIC_BASE_URL`. This precedence is existing behavior and must not change.
- A missing `config.yml` is **not** an error (defaults). A malformed one **is** fatal.
- No tracked file may contain: `michael`, `ameriglide`, `phenixcrm`, `inetalliance`, `whiteleaf`, `remix`, `crm-427`, or `z2flv6tntbjgjxs7ooys3o3s6i`. Task 7 enforces this with a grep gate.
- Every task ends green: `go build ./... && go vet ./... && go test ./...`.

---

### Task 1: `config.go` — Config struct and loader

**Files:**
- Create: `config.go`
- Create: `config_test.go`
- Modify: `go.mod` (add `gopkg.in/yaml.v3`)

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Duration struct{ time.Duration }` with `UnmarshalYAML(*yaml.Node) error` and `MarshalYAML() (any, error)`
  - `type Config struct { Summary SummaryConfig; OnePassword OnePasswordConfig }`
  - `type SummaryConfig struct { Enabled bool; Model string; MinInterval Duration }`
  - `type OnePasswordConfig struct { DefaultAccount string; Accounts map[string]string }`
  - `func defaultConfig() Config`
  - `func configDir() (string, error)`
  - `func loadConfig() (Config, error)`

- [ ] **Step 1: Add the YAML dependency**

```bash
go get gopkg.in/yaml.v3@v3.0.1
```

- [ ] **Step 2: Write the failing tests**

Create `config_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeConfig points XDG_CONFIG_HOME at a temp dir and writes config.yml into
// it, returning nothing — tests then call loadConfig(), which resolves that
// path itself. Passing the path explicitly would not exercise configDir().
func writeConfig(t *testing.T, contents string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "claude-head")
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
	if dir != filepath.Join("/xdg", "claude-head") {
		t.Errorf("configDir() = %q, want /xdg/claude-head", dir)
	}
}

// os.UserConfigDir() returns ~/Library/Application Support on macOS. claude-head
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
	want := filepath.Join(home, ".config", "claude-head")
	if dir != want {
		t.Errorf("configDir() = %q, want %q — not os.UserConfigDir(), which is ~/Library/Application Support on macOS", dir, want)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./... -run TestLoadConfig -v`
Expected: FAIL — `undefined: loadConfig`, `undefined: configDir`.

- [ ] **Step 4: Write the implementation**

Create `config.go`:

```go
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration to accept a Go duration string ("20s") in YAML.
// yaml.v3 will not do this on its own: time.Duration is an int64, so a bare
// `min_interval: 20s` fails to decode. MarshalYAML is the other half — the
// `config get` subcommand round-trips Config through YAML to resolve dotted
// paths, and without it a Duration would serialize as a raw nanosecond count.
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string like \"20s\": %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalYAML() (any, error) {
	return d.String(), nil
}

// Config is claude-head's non-secret configuration. Secrets are deliberately
// NOT here: they live in a separate line-oriented env file (see env.go) that a
// secret manager can serve over a FIFO, which YAML cannot be.
type Config struct {
	Summary     SummaryConfig     `yaml:"summary"`
	OnePassword OnePasswordConfig `yaml:"onepassword"`
}

type SummaryConfig struct {
	Enabled     bool     `yaml:"enabled"`
	Model       string   `yaml:"model"`
	MinInterval Duration `yaml:"min_interval"`
}

// OnePasswordConfig is consumed by bin/claude-env via `claude-head config get`,
// not by the TUI. It ships empty: Accounts maps a GitHub org to the 1Password
// account that holds its secrets, and any built-in mapping would be one user's
// employer structure baked into everyone's binary.
type OnePasswordConfig struct {
	DefaultAccount string            `yaml:"default_account"`
	Accounts       map[string]string `yaml:"accounts"`
}

func defaultConfig() Config {
	return Config{
		Summary: SummaryConfig{
			Enabled:     true,
			Model:       "claude-haiku-4-5",
			MinInterval: Duration{20 * time.Second},
		},
	}
}

// configDir resolves claude-head's config directory per the XDG basedir spec:
// $XDG_CONFIG_HOME replaces the default when set, rather than being searched
// before it.
//
// Hand-rolled on purpose. os.UserConfigDir() returns ~/Library/Application
// Support on macOS, which is not where a terminal tool's dotfile-style config
// belongs and not where this project's users keep it.
func configDir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "claude-head"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "claude-head"), nil
}

// loadConfig reads config.yml, returning defaults for the keys it does not name.
//
// A missing file is not an error — that is a fresh install, and the tool must
// run. A file that exists but does not parse IS an error: continuing on defaults
// would silently discard exactly what the user sat down to configure.
func loadConfig() (Config, error) {
	cfg := defaultConfig()

	dir, err := configDir()
	if err != nil {
		// No home directory and no XDG override. Nothing to read; defaults are
		// still a working configuration, so this is not fatal.
		return cfg, nil
	}
	path := filepath.Join(dir, "config.yml")

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return defaultConfig(), fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	// Decoding INTO cfg (already holding defaults) is what gives partial files
	// their semantics: yaml.v3 assigns only the fields the document names, so an
	// absent `enabled` keeps its default rather than becoming false.
	dec := yaml.NewDecoder(f)
	// Reject unknown fields so a typo ("sumary:") fails loudly instead of being
	// silently ignored, which would look exactly like the setting not working.
	dec.KnownFields(true)

	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return defaultConfig(), nil // empty file: defaults
		}
		return defaultConfig(), fmt.Errorf("parsing %s: %w", path, err)
	}
	return cfg, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./... -run 'TestLoadConfig|TestConfigDir' -v`
Expected: PASS (9 tests).

- [ ] **Step 6: Verify the whole suite is still green**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add config.go config_test.go go.mod go.sum
git commit -m "feat(config): load YAML config from XDG config dir"
```

---

### Task 2: `claude-head config get <dotted.path>` subcommand

**Files:**
- Create: `configget.go`
- Create: `configget_test.go`
- Modify: `main.go` (dispatch the subcommand before `flag.Parse()`)

**Interfaces:**
- Consumes: `Config`, `loadConfig()`, `defaultConfig()` from Task 1.
- Produces:
  - `func configLookup(cfg Config, dotted string) (string, bool)`
  - `func runConfigGet(args []string, stdout, stderr io.Writer) int` — the process exit code.

**Why:** `bin/claude-env` (Task 5) is bash and must read `onepassword.accounts.<org>`, a *nested* key. Its existing `sed`-based `project_field` only handles flat top-level keys. Rather than teach bash to parse YAML or take a `yq` dependency, the Go binary — which claude-env already requires on `PATH` — answers the query.

- [ ] **Step 1: Write the failing tests**

Create `configget_test.go`:

```go
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
	dir := filepath.Join(root, "claude-head")
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

// claude-env calls this on EVERY launch, for orgs the user has not configured.
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

func TestRunConfigGetWrongArgCount(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if code := runConfigGet(nil, &stdout, &stderr); code != 2 {
		t.Errorf("runConfigGet(nil) = %d, want 2 (usage error)", code)
	}
	if stderr.String() == "" {
		t.Error("stderr empty, want a usage message for a malformed invocation")
	}
}

// A malformed config.yml is fatal for the TUI, and must also be fatal here —
// but distinguishable from "absent key" (exit 1) so claude-env's `|| true` does
// not mask a real config error into silence. Exit 3, with an explanation.
func TestRunConfigGetMalformedConfigExitsThree(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "claude-head")
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run 'TestConfigLookup|TestRunConfigGet' -v`
Expected: FAIL — `undefined: configLookup`, `undefined: runConfigGet`.

- [ ] **Step 3: Write the implementation**

Create `configget.go`:

```go
package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// configLookup resolves a dotted path ("onepassword.accounts.acme") against cfg
// and returns its value as a string, or ok=false if the path is absent or does
// not land on a scalar.
//
// It round-trips cfg through YAML rather than reflecting over the struct: the
// YAML tags already define the exact key names a user writes in config.yml, so
// marshalling gives us that namespace for free and cannot drift from it. This
// runs once per process invocation, so the cost is irrelevant.
func configLookup(cfg Config, dotted string) (string, bool) {
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return "", false
	}
	var tree map[string]any
	if err := yaml.Unmarshal(raw, &tree); err != nil {
		return "", false
	}

	var cur any = tree
	for _, part := range strings.Split(dotted, ".") {
		node, ok := cur.(map[string]any)
		if !ok {
			return "", false // walked into a scalar with path left to go
		}
		cur, ok = node[part]
		if !ok {
			return "", false
		}
	}

	switch v := cur.(type) {
	case string:
		return v, true
	case bool:
		return strconv.FormatBool(v), true
	case int:
		return strconv.Itoa(v), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	default:
		// Maps and sequences have no single printable value.
		return "", false
	}
}

// runConfigGet implements `claude-head config get <dotted.path>` and returns the
// process exit code.
//
// The exit codes are a contract with bin/claude-env, which calls this on every
// launch and wraps it in `|| true`:
//
//	0 — found; value on stdout
//	1 — absent key; NOTHING on stdout or stderr. An org the user has not
//	    configured is the normal case, not an error worth printing.
//	2 — usage error (wrong argument count)
//	3 — config.yml exists but does not parse. Distinct from 1 so that a real
//	    config error is not laundered into silence by the caller's `|| true`.
func runConfigGet(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: claude-head config get <dotted.path>")
		return 2
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "claude-head: %v\n", err)
		return 3
	}

	v, ok := configLookup(cfg, args[0])
	if !ok {
		return 1
	}
	fmt.Fprintln(stdout, v)
	return 0
}
```

- [ ] **Step 4: Wire the subcommand into `main.go`**

Modify `main.go` — add the dispatch as the first statement of `main()`, **before** `flag.Parse()`, so that `config` is treated as a subcommand rather than a stray positional argument:

```go
func main() {
	// Subcommand dispatch must precede flag.Parse(): `config` is a bare first
	// arg, not a flag, and flag.Parse() would stop at it and silently ignore the
	// rest. bin/claude-env depends on this path.
	if len(os.Args) > 1 && os.Args[1] == "config" {
		if len(os.Args) > 2 && os.Args[2] == "get" {
			os.Exit(runConfigGet(os.Args[3:], os.Stdout, os.Stderr))
		}
		fmt.Fprintln(os.Stderr, "usage: claude-head config get <dotted.path>")
		os.Exit(2)
	}

	sessionFlag := flag.String("session", "", "Use a specific session ID instead of auto-detecting")
	flag.Parse()

	// ... rest unchanged for now; Task 4 revisits this function.
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./... -run 'TestConfigLookup|TestRunConfigGet' -v`
Expected: PASS (9 tests).

- [ ] **Step 6: Verify the subcommand end-to-end**

```bash
go build -o /tmp/ch . && XDG_CONFIG_HOME=$(mktemp -d) /tmp/ch config get summary.min_interval
```
Expected: prints `20s`, exit 0.

```bash
XDG_CONFIG_HOME=$(mktemp -d) /tmp/ch config get onepassword.accounts.acme; echo "exit=$?"
```
Expected: no output, `exit=1`.

- [ ] **Step 7: Verify the whole suite is still green**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add configget.go configget_test.go main.go
git commit -m "feat(config): add 'config get' subcommand for the bash launcher"
```

---

### Task 3: Relocate the env file to the XDG config dir

**Files:**
- Modify: `env.go:22-34` (`claudeHeadEnvPaths`), `env.go:72-78` (goroutine-leak comment)
- Modify: `env_test.go` (path-list test)

**Interfaces:**
- Consumes: `configDir()` from Task 1.
- Produces: `claudeHeadEnvPaths() []string` — now at most 2 entries, never a `~/Projects` path.

**Why:** `env.go:32` hardcodes `~/Projects/claude-head/.env`, which exists on exactly one machine.

- [ ] **Step 1: Write the failing test**

In `env_test.go`, add:

```go
// The env file lives beside config.yml in the XDG config dir. It must NOT be
// looked for in ~/Projects — that path was hardcoded to one developer's machine.
func TestClaudeHeadEnvPathsUsesConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_HEAD_ENV", "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")

	paths := claudeHeadEnvPaths()

	want := filepath.Join("/xdg", "claude-head", "env")
	if len(paths) != 1 || paths[0] != want {
		t.Errorf("claudeHeadEnvPaths() = %v, want exactly [%q]", paths, want)
	}
	for _, p := range paths {
		if strings.Contains(p, "Projects") {
			t.Errorf("claudeHeadEnvPaths() returned %q — the ~/Projects fallback is hardcoded to one machine and must be gone", p)
		}
	}
}
```

Add `"strings"` to the `env_test.go` import block if absent.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./... -run TestClaudeHeadEnvPathsUsesConfigDir -v`
Expected: FAIL — the returned slice still contains the `~/Projects` path and has 2 entries.

- [ ] **Step 3: Rewrite `claudeHeadEnvPaths`**

Replace `env.go` lines 18–34 with:

```go
// claudeHeadEnvPaths lists the files claude-head reads its OWN secrets from,
// highest precedence first. Deliberately not the monitored project's env:
// claude-head watches sessions across many repos and must never inherit their
// secrets, nor depend on which repo a pane happened to launch from.
//
// CLAUDE_HEAD_ENV, when set, is the ONLY path consulted — it overrides the
// default outright rather than merely being tried first, so a caller can point
// claude-head at a specific file and be certain nothing else is read.
func claudeHeadEnvPaths() []string {
	if p := os.Getenv("CLAUDE_HEAD_ENV"); p != "" {
		return []string{p}
	}
	dir, err := configDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(dir, "env")}
}
```

- [ ] **Step 4: Correct the stale goroutine arithmetic in `readEnvFileValue`**

The comment at `env.go:72-78` says "with the current two default paths the worst case is 4 parked goroutines". There is now one default path. Replace that sentence:

```go
	// The read runs in a goroutine because a FIFO open is uninterruptible: if no
	// writer ever appears, this goroutine parks for the life of the process. That
	// leak is bounded — one per configured path, per lookup, at startup — and is
	// the price of not blocking the render loop. summarizerEnvOptions does two
	// lookups (API key, then base URL), so with the single default path the worst
	// case is 2 parked goroutines.
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./... -run TestClaudeHeadEnvPaths -v`
Expected: PASS. Both this test and the existing `CLAUDE_HEAD_ENV`-precedence test must pass.

- [ ] **Step 6: Verify the whole suite is still green**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add env.go env_test.go
git commit -m "feat(env): read secrets from the XDG config dir, drop ~/Projects fallback"
```

---

### Task 4: Wire `Config` into the model

**Files:**
- Modify: `main.go` (call `loadConfig()`, pass `Config` to `newModel`)
- Modify: `tui.go:42` (delete `minSummaryInterval` const), `tui.go:120-150` (`newModel`), `tui.go:359-362` (`canSummarize`)
- Modify: `summary.go:195-207` (`newSummarizer`), `summary.go:236` (hardcoded model)
- Modify: `tui_test.go` (existing `newModel` callers + a new disabled-summarizer test)

**Interfaces:**
- Consumes: `Config`, `loadConfig()` from Task 1.
- Produces:
  - `func newModel(cfg Config, jsonlPath, sessionID string, followActive bool) model` — **signature change**; `cfg` is first.
  - `func newSummarizer(cfg SummaryConfig, opts ...option.RequestOption) *Summarizer` — **signature change**.
  - `model` gains fields `summaryModel string` and `minSummaryInterval time.Duration`.
  - `Summarizer` gains field `model string`.

**Why:** the summarizer model, its on/off switch, and its rate limit are hardcoded. `summary.enabled: false` must produce a nil summarizer even when an API key is present — otherwise a user who explicitly disabled the feature still gets billed.

- [ ] **Step 1: Write the failing tests**

In `tui_test.go`, add:

```go
// summary.enabled: false must disable the summarizer OUTRIGHT — no client is
// constructed even though a key is available. A user who turned the feature off
// must not be billed for it.
func TestNewSummarizerDisabledByConfig(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("CLAUDE_HEAD_ENV", filepath.Join(t.TempDir(), "absent"))

	cfg := defaultConfig().Summary
	cfg.Enabled = false

	if s := newSummarizer(cfg); s != nil {
		t.Fatal("newSummarizer() non-nil, want nil when summary.enabled is false — an explicitly disabled feature must not construct a billable client")
	}
}

func TestNewSummarizerEnabledUsesConfiguredModel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("CLAUDE_HEAD_ENV", filepath.Join(t.TempDir(), "absent"))

	cfg := defaultConfig().Summary
	cfg.Model = "claude-sonnet-5"

	s := newSummarizer(cfg)
	if s == nil {
		t.Fatal("newSummarizer() = nil, want non-nil when enabled with a key present")
	}
	if s.model != "claude-sonnet-5" {
		t.Errorf("Summarizer.model = %q, want the configured model", s.model)
	}
}

// The rate limit between summary calls comes from config, not a constant.
func TestNewModelUsesConfiguredMinInterval(t *testing.T) {
	cfg := defaultConfig()
	cfg.Summary.MinInterval = Duration{90 * time.Second}

	m := newModel(cfg, filepath.Join(t.TempDir(), "s.jsonl"), "sess", false)

	if m.minSummaryInterval != 90*time.Second {
		t.Errorf("model.minSummaryInterval = %v, want 90s from config", m.minSummaryInterval)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run 'TestNewSummarizer|TestNewModelUsesConfiguredMinInterval' -v`
Expected: FAIL — `newSummarizer` takes no `SummaryConfig`; `newModel` takes no `Config`; `Summarizer` has no `model` field.

- [ ] **Step 3: Thread config through `summary.go`**

Add the `model` field and gate on `Enabled`:

```go
// Summarizer turns a session transcript into the topic/now status lines.
type Summarizer struct {
	client anthropic.Client
	model  string
}
```

Replace `newSummarizer` (keeping its entire existing doc comment about `WithoutEnvironmentDefaults`, which still applies) with:

```go
func newSummarizer(cfg SummaryConfig, opts ...option.RequestOption) *Summarizer {
	// An explicitly disabled summarizer constructs no client at all, even when a
	// key is present: the feature is billable, and "off" must mean off.
	if !cfg.Enabled {
		return nil
	}
	if len(opts) == 0 {
		if opts = summarizerEnvOptions(); opts == nil {
			return nil
		}
	}
	opts = append([]option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithMaxRetries(1),
		option.WithRequestTimeout(summaryRequestTimeout),
	}, opts...)
	return &Summarizer{client: anthropic.NewClient(opts...), model: cfg.Model}
}
```

In `Summarize`, replace the hardcoded model at `summary.go:236`:

```go
		Model:      anthropic.Model(s.model),
```

- [ ] **Step 4: Thread config through `tui.go`**

Delete the `minSummaryInterval` const at `tui.go:41-42`. Add two fields to `model` (beside `summarizer`):

```go
	summarizer         *Summarizer
	summaryModel       string
	minSummaryInterval time.Duration
```

Change `newModel`'s signature and body:

```go
func newModel(cfg Config, jsonlPath, sessionID string, followActive bool) model {
	r := newEventReader(jsonlPath)
	r.SeedFromEnd(500)
	seeded, _ := r.Seeded()

	summarizer := newSummarizer(cfg.Summary)

	m := model{
		jsonlPath:      jsonlPath,
		sessionID:      sessionID,
		followActive:   followActive,
		selfPane:       os.Getenv("TMUX_PANE"),
		paneDir:        paneMapDir(),
		reader:         r,
		allEvents:      seeded,
		rateLimitsPath: defaultRateLimitsPath(),
		firstPrompt:    r.FirstPrompt(),
		// Init always issues the first poll itself (see Init below), so the
		// flag starts held to prevent the first 1s tick from firing a second,
		// concurrent poll that races on EventReader.offset.
		polling:            true,
		summarizer:         summarizer,
		summaryModel:       cfg.Summary.Model,
		minSummaryInterval: cfg.Summary.MinInterval.Duration,
		// Init unconditionally fires the seed summarize call when summarizer
		// != nil (see Init below); this flag must already be held at that
		// point, for the same reason polling starts true above — Init has a
		// value receiver and cannot set it itself, so a fast busy→idle edge
		// on the very first poll would otherwise race a second concurrent
		// call against the seed call.
		summarizing: summarizer != nil,
	}
	m.recomputeFromEvents(time.Now())
	return m
}
```

At `tui.go:362`, read the interval from the model instead of the deleted const:

```go
	return now.Sub(m.lastSummaryAt) >= m.minSummaryInterval
```

- [ ] **Step 5: Load the config in `main.go`**

In `main()`, after the subcommand dispatch and `flag.Parse()`:

```go
	cfg, err := loadConfig()
	if err != nil {
		// A config file that exists but does not parse is fatal: running on
		// defaults would silently discard what the user configured.
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}
```

and change the `newModel` call:

```go
	m := newModel(cfg, jsonlPath, sessionID, followActive)
```

- [ ] **Step 6: Update existing `newModel` / `newSummarizer` callers in tests**

Every existing `newModel(...)` call in `tui_test.go` gains `defaultConfig()` as its first argument; every existing `newSummarizer()` call gains `defaultConfig().Summary`. Find them:

```bash
grep -n 'newModel(\|newSummarizer(' tui_test.go summary_test.go
```

Update each. The existing `TestNewModelSeedsSummarizingWithSummarizer` and the two API-key tests keep their current assertions — only the call shape changes.

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./... -v -run 'TestNewSummarizer|TestNewModel'`
Expected: PASS.

- [ ] **Step 8: Verify the whole suite is still green**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add main.go tui.go tui_test.go summary.go summary_test.go
git commit -m "feat(summary): drive model, interval, and on/off from config"
```

---

### Task 5: Bring `claude-env` into the repo

**Files:**
- Create: `bin/claude-env` (mode 0755) — from `~/.local/bin/claude-env`
- Create: `bin/project-color-resolve.sh` (mode 0755) — from `~/.tmux/project-color-resolve.sh`
- Create: `.project.yml.example`

**Interfaces:**
- Consumes: `claude-head config get onepassword.accounts.<org>` and `... onepassword.default_account` from Task 2.
- Produces: the `bin/claude-env` launcher and the documented `.project.yml` format (`color`, `name`, `op_env`, `op_account`).

**Why:** claude-env is the launcher that *creates* the claude-head pane. Publishing claude-head without it means telling users to build the tmux layout themselves. Its hardcoded org→1Password map is the single worst leak in the project.

- [ ] **Step 1: Copy both scripts in verbatim**

```bash
mkdir -p bin
cp ~/.local/bin/claude-env bin/claude-env
cp ~/.tmux/project-color-resolve.sh bin/project-color-resolve.sh
chmod 755 bin/claude-env bin/project-color-resolve.sh
```

- [ ] **Step 2: Confirm the copies are syntactically valid before editing**

```bash
bash -n bin/claude-env && bash -n bin/project-color-resolve.sh && echo OK
```
Expected: `OK`.

- [ ] **Step 3: Replace the hardcoded 1Password org map**

In `bin/claude-env`, replace the whole `op_account_for` function. The current body hardcodes `ameriglide|phenixcrm`, `inetalliance`, and `whiteleaf` — one employer's org structure. Replace it with:

```bash
# op_account_for DIR — the 1Password account for DIR's repo. Precedence:
#   1. explicit `op_account` in DIR/.project.yml
#   2. onepassword.accounts.<github-org> from claude-head's config.yml
#   3. onepassword.default_account from the same
# An empty result is valid and expected: `op` then picks its own default account.
#
# `claude-head config get` exits 1 for a key the user has not configured, which
# is the normal case — hence `|| true`, which also keeps `set -e` from killing us.
op_account_for() {
  local work_dir="$1" acct origin org
  acct="$(project_field "$work_dir/.project.yml" op_account)"
  if [ -n "$acct" ]; then
    echo "$acct"
    return 0
  fi

  origin="$(git -C "$work_dir" remote get-url origin 2>/dev/null || true)"
  org="$(sed -n 's#.*github\.com[:/]\([^/]*\)/.*#\1#p' <<< "$origin")"
  if [ -n "$org" ]; then
    acct="$(claude-head config get "onepassword.accounts.$org" 2>/dev/null || true)"
    if [ -n "$acct" ]; then
      echo "$acct"
      return 0
    fi
  fi

  claude-head config get onepassword.default_account 2>/dev/null || true
}
```

- [ ] **Step 4: Make `inject_op_env` tolerate an empty account**

`op_account_for` can now legitimately return "". Passing `--account ""` to `op` is an error, so build the argument list conditionally. In `inject_op_env`, replace the `op environment read` call:

```bash
  acct="$(op_account_for "$work_dir")"
  local -a acct_args=()
  [ -n "$acct" ] && acct_args=(--account "$acct")
  if ! env_out="$(op environment read "$env_id" "${acct_args[@]+"${acct_args[@]}"}")"; then
    echo "claude-env: op environment read $env_id failed; launching without secrets" >&2
    return 1
  fi
```

The `"${acct_args[@]+"${acct_args[@]}"}"` form expands to nothing when the array is empty — a bare `"${acct_args[@]}"` is an unbound-variable error under `set -u`, which this script sets.

- [ ] **Step 5: Source the color resolver from the script's own directory**

`apply_status_color` and `apply_tab_color` both hardcode `$HOME/.tmux/project-color-resolve.sh`, which exists on one machine. In **both** functions, replace the `resolver` assignment:

```bash
  local resolver
  resolver="$(dirname "$(readlink -f "$0")")/project-color-resolve.sh"
```

Keep the existing `[ -f "$resolver" ] || return 0` guard: a broken install must lose colors, not fail to launch.

Note `readlink -f` is GNU-flavored; on macOS it exists in coreutils but the BSD `readlink` lacks `-f` before Ventura. Bash on macOS 13+ has it. If this proves a problem, the fallback is `cd "$(dirname "$0")" && pwd`. Verify with Step 7.

- [ ] **Step 6: Update the header comment**

The script's header still says "Layout: top-left = claude-head (short)". That is accurate — leave it. But add a dependency line beneath the usage block:

```bash
# Requires: tmux, claude-head (on PATH), git. Optional: op (1Password
# Environments), zoxide (directory-query resolution), iTerm2 (tab coloring).
```

- [ ] **Step 7: Verify syntax and lint**

```bash
bash -n bin/claude-env && shellcheck bin/claude-env bin/project-color-resolve.sh
```
Expected: `bash -n` silent. `shellcheck` may emit warnings (SC1090 is already suppressed inline); fix any **error**-level finding, and any warning that indicates a real bug. Do not chase style-only notices.

- [ ] **Step 8: Verify the account lookup resolves against real config**

```bash
go build -o /tmp/ch . && export PATH="/tmp:$PATH" && mv /tmp/ch /tmp/claude-head
export XDG_CONFIG_HOME=$(mktemp -d)
mkdir -p "$XDG_CONFIG_HOME/claude-head"
printf 'onepassword:\n  accounts:\n    acme: acme.1password.com\n' > "$XDG_CONFIG_HOME/claude-head/config.yml"
claude-head config get onepassword.accounts.acme
```
Expected: prints `acme.1password.com`.

- [ ] **Step 9: Write `.project.yml.example`**

Create `.project.yml.example`:

```yaml
# claude-env reads this from the root of a project you launch it in.
# Copy to .project.yml. NOTE: .project.yml is gitignored on purpose —
# op_env is an ID pointing at your secrets, and should not be committed.

# Tab/status-bar color for this project's tmux session.
# One of: red blue green yellow purple orange pink cyan — or a #hex value.
color: blue

# Passed to `claude -n`, naming the session in Claude Code.
name: my-project

# 1Password Environment ID. When set, claude-env reads that Environment once at
# launch and injects its variables into the tmux session environment, so every
# pane inherits them and Claude never calls `op` itself. Omit to launch without
# secret injection.
# op_env: abcdefghijklmnopqrstuvwxyz

# 1Password account for the above. Optional: without it, claude-env maps this
# repo's GitHub org through `onepassword.accounts` in ~/.config/claude-head/config.yml,
# then falls back to `onepassword.default_account`, then to op's own default.
# op_account: my.1password.com
```

- [ ] **Step 10: Commit**

```bash
git add bin/claude-env bin/project-color-resolve.sh .project.yml.example
git commit -m "feat(claude-env): vendor the tmux launcher, de-hardcode the 1Password account map"
```

---

### Task 6: Sanitize the repo

**Files:**
- Delete: `.project.yml`, `docs/superpowers/` (7 tracked files)
- Modify: `.gitignore`
- Modify: `tui_test.go`, `session_test.go`, `events_test.go` (fixtures)
- Modify: `env.go`, `summary.go`, `env_test.go` (1Password comment phrasing)

**Interfaces:**
- Consumes: nothing.
- Produces: a tracked tree with no personal identifiers. Task 7's grep gate depends on this.

- [ ] **Step 1: Stop tracking the personal/process files**

`.project.yml` holds a live 1Password Environment UUID (`op_env: z2flv6tntbjgjxs7ooys3o3s6i`). `docs/superpowers/` is process exhaust full of absolute paths and internal ticket names, describing a predecessor tool ("plan-monitor").

```bash
git rm --cached .project.yml
git rm -r --cached docs/superpowers
```

The working-tree copies stay (you still use `.project.yml`; the specs/plans remain readable locally) — only tracking stops.

- [ ] **Step 2: Extend `.gitignore`**

Replace `.gitignore` with:

```gitignore
# Build output
claude-head

# Per-user project config. Contains op_env, an ID pointing at your secrets.
# Gitignored so that neither this repo nor any repo using claude-env can leak it.
# See .project.yml.example for the format.
.project.yml

# Local agent/process artifacts
.claude/settings.local.json
.superpowers/
docs/superpowers/
```

- [ ] **Step 3: Verify nothing personal is still tracked**

```bash
git ls-files | xargs grep -rniE 'michael|ameriglide|phenixcrm|inetalliance|whiteleaf|remix|crm-427|z2flv6tntbjgjxs7ooys3o3s6i' || echo "CLEAN"
```
Expected: hits remain **only** in `tui_test.go`, `session_test.go`, and `events_test.go`. Fix those in Steps 4–6. Anything else is a miss — investigate before continuing.

- [ ] **Step 4: Rewrite the fixtures in `tui_test.go`**

Substitute consistently — the encoded forms must stay consistent with their decoded counterparts or the path-encoding tests break:

- `/Users/michael/Projects/remix` → `/Users/alice/Projects/webapp`
- `-Users-michael-Projects-remix` → `-Users-alice-Projects-webapp`
- `crm-427-dataloader` → `feature-branch`
- `crm-427` → `feature-branch`

```bash
sed -i '' \
  -e 's#/Users/michael/Projects/remix#/Users/alice/Projects/webapp#g' \
  -e 's#-Users-michael-Projects-remix#-Users-alice-Projects-webapp#g' \
  -e 's#/Users/michael/#/Users/alice/#g' \
  -e 's#crm-427-dataloader#feature-branch#g' \
  -e 's#crm-427#feature-branch#g' \
  tui_test.go
```

Then read the diff — `a-very-long-worktree-name-here` at `tui_test.go:461` is a *deliberate* width-overflow fixture and must stay long. Confirm the substitution did not shorten any string whose length the test depends on.

- [ ] **Step 5: Rewrite the fixtures in `session_test.go`**

```bash
sed -i '' -e 's#/Users/michael/#/Users/alice/#g' session_test.go
```

`session_test.go:16` asserts `{"/Users/michael/Projects/claude-head", "-Users-michael-Projects-claude-head"}` — both halves are rewritten by the substitution above, keeping the encode/decode pair consistent.

- [ ] **Step 6: Rewrite the fixture in `events_test.go`**

`events_test.go:17-18` uses `/ameriglide-core:pickup`, a private plugin's slash command. Replace both the input and the expectation:

```bash
sed -i '' -e 's#/ameriglide-core:pickup#/myplugin:deploy#g' events_test.go
```

- [ ] **Step 7: Soften the 1Password comments (do not delete them)**

The FIFO comments in `env.go:11-16`, `env.go:36-47`, and `summary.go:151-154` explain *why* the retry and timeout logic exists. They stay — 1Password is a public product, and deleting the rationale would leave unexplainable code. Reword to present it as one example rather than the setup. In `env.go:11-16`:

```go
// envFileTimeout bounds reading an env file. The file may be a FIFO mounted by
// a secret manager (1Password Environments, for one), and opening a FIFO with no
// writer blocks forever — so a locked or absent secret agent would otherwise hang
// the whole TUI at startup, not merely the summarizer. On timeout we return
// nothing and the pane falls back to raw prompts.
```

and in `env.go:36-47`:

```go
// envFileRetryDelays are the backoffs between attempts to read one env file.
//
// A FIFO-backed env file (e.g. a mounted 1Password Environment) serves one reader
// at a time: when several claude-head panes start at once they race on the open,
// and the losers see an empty pipe — not an error, just no key, which would
// silently disable the summarizer for most panes. Contention is transient (a loser
// succeeds milliseconds later), so retry. The first attempt is immediate; these
// are the waits before each retry.
```

Apply the same softening to the comments in `summary.go` and `env_test.go` that name 1Password. Do not change any code or assertion in this step.

- [ ] **Step 8: Verify the suite is still green after the fixture rewrite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS. A failure here means a substitution broke a length- or encoding-sensitive assertion — fix the fixture, do not weaken the test.

- [ ] **Step 9: Re-run the leak grep**

```bash
git ls-files | xargs grep -rniE 'michael|ameriglide|phenixcrm|inetalliance|whiteleaf|remix|crm-427|z2flv6tntbjgjxs7ooys3o3s6i' && echo "LEAK FOUND — fix before committing" || echo "CLEAN"
```
Expected: `CLEAN`.

- [ ] **Step 10: Commit**

```bash
git add -A
git commit -m "chore: remove personal identifiers from tracked files"
```

---

### Task 7: Release scaffolding — module, license, README

**Files:**
- Modify: `go.mod` (module path)
- Create: `LICENSE`
- Create: `README.md`

**Interfaces:**
- Consumes: everything above. This task documents it.
- Produces: the published repo surface.

- [ ] **Step 1: Rename the module**

```bash
go mod edit -module github.com/mquinnv/claude-head
go build ./... && go test ./...
```
Expected: PASS with no import edits — the project is a single `package main` with no internal imports.

- [ ] **Step 2: Add the MIT license**

Create `LICENSE` with the standard MIT text, `Copyright (c) 2026 Michael Ventura`.

- [ ] **Step 3: Write `README.md`**

It must cover, in this order:

1. **What it is.** `claude-head` is a status-bar "head" for a Claude Code session: it tails the session transcript and renders state, token/rate-limit meters, the first and latest prompt, and an LLM-generated one-line summary of what the session is doing. `claude-env` is the tmux launcher that builds the session it lives in.

2. **The pane model**, with an ASCII diagram of what `claude-env` creates:

```
┌────────────────────────┬──────────┐
│ claude-head  (4 rows)  │          │
├────────────────────────┤  shell   │
│                        │  (30%)   │
│ claude                 │          │
│                        │          │
└────────────────────────┴──────────┘
```

3. **Install.**

```bash
go install github.com/mquinnv/claude-head@latest

git clone https://github.com/mquinnv/claude-head
ln -s "$PWD/claude-head/bin/claude-env"             ~/.local/bin/claude-env
ln -s "$PWD/claude-head/bin/project-color-resolve.sh" ~/.local/bin/project-color-resolve.sh
```

State explicitly that the two scripts **must remain siblings** — `claude-env` sources the resolver by a path relative to its own location.

4. **Dependencies**, and what degrades without each: **tmux** (required), **claude-head on PATH** (required by claude-env), **jq** (required by the pane hook), **git** (org inference for 1Password), **zoxide** (optional — `claude-env someproject` fuzzy-resolution), **`op`** (optional — 1Password Environment injection), **iTerm2** (optional — tab coloring; other terminals ignore the escape sequences harmlessly).

5. **The Claude Code hook** — without it, claude-head cannot bind a pane to its sibling's transcript and falls back to most-recently-active-session detection, which is wrong when several sessions run at once:

```bash
mkdir -p ~/.claude/hooks
ln -sf "$PWD/hooks/claude-head-map.sh" ~/.claude/hooks/claude-head-map.sh
```

and register it on **both** `SessionStart` and `UserPromptSubmit` in `~/.claude/settings.json`. Show the JSON. Note the hook must stay silent on stdout, because `UserPromptSubmit` stdout is injected into the model's context.

6. **Configuration** — `~/.config/claude-head/config.yml` (honors `XDG_CONFIG_HOME`), every key with its default:

```yaml
summary:
  enabled: true
  model: claude-haiku-4-5
  min_interval: 20s

onepassword:
  default_account: ""
  accounts: {}
```

State that an unknown key is a startup error, not a silent no-op.

7. **`.project.yml`** — the per-project file claude-env reads (`color`, `name`, `op_env`, `op_account`). Point at `.project.yml.example`. Warn that it is gitignored by this repo on purpose and that `op_env` should not be committed.

8. **Billing — this must be prominent, not a footnote.** Summaries call the Anthropic API with **the user's own key**, billed to them: roughly one Haiku call per `min_interval` (20s default) of *active* session, and none while idle. `summary.enabled: false` disables it entirely, and without a key the feature is simply off — claude-head falls back to showing the raw prompt.

9. **Secret managers / 1Password** — see Step 4.

- [ ] **Step 4: Write the README's secret-manager section**

The env file is read as a **stream**, not required to be a regular file. It can therefore be a FIFO mounted by a secret manager, so the API key never lands on disk. Document three setups:

- **Plain file** (the baseline): `~/.config/claude-head/env`, mode `0600`, `KEY=value` per line, `#` comments, no quoting.
- **`op run`**: `op run -- claude-head` — the process environment beats the env file, so no file is needed at all.
- **Mounted FIFO** (1Password Environments): mount the Environment at `~/.config/claude-head/env`; claude-head reads it per launch.

Then document the two behaviors a FIFO user will otherwise mistake for bugs:

- **Contention is expected.** A FIFO serves one reader at a time. Several panes launching at once race on the open, and the losers see an empty pipe. claude-head retries with backoff (50ms / 150ms / 400ms) and the losers succeed milliseconds later. This is by design.
- **A locked agent degrades, it does not hang.** If the secret agent is locked or absent, opening the FIFO blocks forever. claude-head times out after 2s and runs *without* summaries rather than freezing the TUI.

Note that recognized keys are `ANTHROPIC_API_KEY` and `ANTHROPIC_BASE_URL`, that `ANTHROPIC_BASE_URL` is honored explicitly for gateway users, and that `CLAUDE_HEAD_ENV` overrides the env-file path outright.

- [ ] **Step 5: Verify every command in the README actually runs**

Do not skip this. Execute the install, hook, and config snippets in a scratch `HOME` and confirm each works as written:

```bash
export HOME=$(mktemp -d) XDG_CONFIG_HOME=$(mktemp -d)
go build -o "$HOME/claude-head" .
mkdir -p "$XDG_CONFIG_HOME/claude-head"
printf 'summary:\n  enabled: false\n' > "$XDG_CONFIG_HOME/claude-head/config.yml"
"$HOME/claude-head" config get summary.enabled   # -> false
```
Expected: prints `false`. Fix the README if any snippet is wrong; a README whose first command fails is worse than no README.

- [ ] **Step 6: Final full verification**

```bash
go build ./... && go vet ./... && go test ./... && bash -n bin/claude-env
git ls-files | xargs grep -rniE 'michael|ameriglide|phenixcrm|inetalliance|whiteleaf|remix|crm-427|z2flv6tntbjgjxs7ooys3o3s6i' && echo "LEAK" || echo "CLEAN"
```
Expected: tests PASS, `CLEAN`. (`LICENSE` legitimately contains "Michael Ventura" — exclude it from the grep or accept that one hit knowingly.)

- [ ] **Step 7: Commit**

```bash
git add go.mod LICENSE README.md
git commit -m "docs: add README and MIT license, rename module for publication"
```

---

## Post-plan: what the user must do by hand

These are **not** agent tasks — they need Michael's own credentials and judgment:

1. **Move the 1Password account map into personal config.** The four entries deleted from `bin/claude-env` must go into `~/.config/claude-head/config.yml` or 1Password injection breaks on his machine:

```yaml
onepassword:
  default_account: whiteleaf.1password.com
  accounts:
    ameriglide: ameriglide.1password.com
    phenixcrm: ameriglide.1password.com
    inetalliance: team-iai.1password.com
```

2. **Migrate the env file.** `~/Projects/claude-head/.env` is no longer read. Point the 1Password mount at `~/.config/claude-head/env` instead.

3. **Replace `~/.local/bin/claude-env`** with a symlink to `bin/claude-env` in the repo, so there is one copy.

4. **Create the GitHub repo** `mquinnv/claude-head` and push. `go install github.com/mquinnv/claude-head@latest` cannot work until the module path resolves.

5. **Rotate `op_env: z2flv6tntbjgjxs7ooys3o3s6i`** if the repo's git *history* will be public. This plan stops the file being tracked going forward, but the UUID remains in every prior commit. It is an identifier rather than a credential — reading the Environment still requires 1Password auth — so this is a judgment call, not an emergency.
