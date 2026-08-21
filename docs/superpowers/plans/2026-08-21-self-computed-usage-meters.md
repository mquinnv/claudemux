# Self-Computed Usage Meters Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the abtop-written rate-limit cache with a claudemux-owned statusline command, and add per-model weekly meters (Fable on Max plans) sourced from Claude Code's `get_usage` control request.

**Architecture:** Two independent sources on two cadences. A `claudemux-head statusline` subcommand, registered in `~/.claude/settings.json`'s `statusLine` slot, writes `5h`/`wk` to `~/.claude/claudemux/rate-limits.json` on every Claude Code render (free, continuous). A separate 15-minute poller spawns a short-lived `claude` over the SDK stdio control protocol, asks it for `get_usage`, and caches per-model weekly windows to `~/.claude/claudemux/usage.json` behind a lock so N panes cause one spawn. The pull path is strictly additive: every failure degrades to "no model rows", never to a blank or broken meter line.

**Tech Stack:** Go 1.26.2, bubbletea/lipgloss/bubbles TUI, `os/exec` for the poller, `encoding/json`. No new module dependencies.

**Spec:** `docs/superpowers/specs/2026-08-21-self-computed-usage-meters-design.md`

## Global Constraints

These apply to every task. Copied verbatim from the spec.

- **Per-model rows come from `rate_limits.limits[]`**, entries with `kind == "weekly_scoped"` and a `scope.model.display_name`. Never read `seven_day_opus` / `seven_day_sonnet` — both were `null` on a live Max account while `limits[]` was populated.
- **`get_usage` is Experimental** ("the response shape may change"). Every pull-path failure — spawn error, timeout, protocol change, unparseable row, `limits[]` absent — degrades to **no model rows**, never to an error state and never to a blank panel. The `5h` and `wk` gauges must remain correct with the pull path completely broken.
- **The poller must unset `TMUX_PANE` in the child environment.** `claudemux-map.sh` keys its pane-map file on `$TMUX_PANE`; without this a poll spawned from a head pane writes a phantom session into `panes/<head-pane>.json`.
- **Poll TTL is 15 minutes**, single-flight across processes via a lock file.
- **`statusLine` is claimed only** when absent, when its command's basename is `abtop-statusline.sh`, or when it is already ours. Any other value is left byte-identical and reported on stderr.
- **Gauge drop order is fixed:** eta, then model rows, then `wk`, then `5h`. Implemented by slice position — callers drop from the end — so `rateGauges` must return `[5h, wk, <model rows…>, eta]`.
- Run the full suite with `go test ./...` from the repo root. Format with `gofmt -w` on every file touched.

---

### Task 1: `statusline` subcommand writes our own rate-limit cache

Reuses the existing on-disk schema (`rawRateLimitsFile` in `budget.go:47`) so `readRateLimits` needs no parser change — only the path changes, in Task 2.

**Files:**
- Create: `cmd/claudemux-head/statusline.go`
- Create: `cmd/claudemux-head/statusline_test.go`
- Modify: `cmd/claudemux-head/main.go:20` (`knownSubcommands`), and the dispatch chain in `main()` after the `hook` block (`cmd/claudemux-head/main.go:81-88`)

**Interfaces:**
- Consumes: `rawRateLimitsFile` (`budget.go:47`) — the JSON shape written to disk.
- Produces:
  - `func runStatusline(args []string, stdin io.Reader, stdout, stderr io.Writer) int`
  - `func defaultStatuslineCachePath() string` — `~/.claude/claudemux/rate-limits.json`, honoring `CLAUDEMUX_RATE_LIMITS_PATH`.
  - `const statuslineSource = "claudemux"`

- [ ] **Step 1: Write the failing test**

Create `cmd/claudemux-head/statusline_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runStatuslineInto runs the subcommand against a payload and returns the
// exit code plus whatever landed at the cache path.
func runStatuslineInto(t *testing.T, payload string) (int, string, []byte) {
	t.Helper()
	dir := t.TempDir()
	cache := filepath.Join(dir, "nested", "rate-limits.json")
	t.Setenv("CLAUDEMUX_RATE_LIMITS_PATH", cache)

	var out, errBuf bytes.Buffer
	code := runStatusline(nil, strings.NewReader(payload), &out, &errBuf)
	data, err := os.ReadFile(cache)
	if err != nil {
		data = nil
	}
	return code, out.String(), data
}

// The statusline command must stay silent on stdout: whatever it prints is
// rendered as the user's status line, and abtop's shim printed nothing.
func TestStatuslineWritesCacheAndStaysSilent(t *testing.T) {
	code, stdout, data := runStatuslineInto(t, `{
		"rate_limits": {
			"five_hour": {"used_percentage": 12.4, "resets_at": 1787321400},
			"seven_day": {"used_percentage": 62, "resets_at": 1787605200}
		}
	}`)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	var got rawRateLimitsFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("cache does not parse: %v (raw %q)", err, data)
	}
	if got.Source != statuslineSource {
		t.Errorf("source = %q, want %q", got.Source, statuslineSource)
	}
	if got.FiveHour.UsedPercentage != 12.4 || got.FiveHour.ResetsAt != 1787321400 {
		t.Errorf("five_hour = %+v, want 12.4 / 1787321400", got.FiveHour)
	}
	if got.SevenDay.UsedPercentage != 62 || got.SevenDay.ResetsAt != 1787605200 {
		t.Errorf("seven_day = %+v, want 62 / 1787605200", got.SevenDay)
	}
	if got.UpdatedAt == 0 {
		t.Error("updated_at = 0, want a wall-clock stamp")
	}
}

// Non-subscribers (API key, Bedrock, Vertex) get no rate_limits key at all.
// Writing a zeroed cache would render a confident "0%" meter, so write nothing
// and leave any previous cache untouched.
func TestStatuslineNoRateLimitsWritesNothing(t *testing.T) {
	code, _, data := runStatuslineInto(t, `{"model":{"id":"claude-opus-5"}}`)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if data != nil {
		t.Fatalf("wrote %q, want no file", data)
	}
}

// Each window is independently optional. A payload with only five_hour must
// still produce a usable cache.
func TestStatuslinePartialWindows(t *testing.T) {
	code, _, data := runStatuslineInto(t, `{
		"rate_limits": {"five_hour": {"used_percentage": 7, "resets_at": 1787321400}}
	}`)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	var got rawRateLimitsFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("cache does not parse: %v", err)
	}
	if got.FiveHour.UsedPercentage != 7 {
		t.Errorf("five_hour = %+v, want 7", got.FiveHour)
	}
	if got.SevenDay.ResetsAt != 0 {
		t.Errorf("seven_day = %+v, want zero value", got.SevenDay)
	}
}

// Garbage on stdin must never fail the user's status line.
func TestStatuslineGarbageExitsZero(t *testing.T) {
	code, stdout, data := runStatuslineInto(t, "not json at all")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if data != nil {
		t.Fatalf("wrote %q, want no file", data)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run TestStatusline -v`
Expected: FAIL — `undefined: runStatusline`, `undefined: statuslineSource`

- [ ] **Step 3: Write the implementation**

Create `cmd/claudemux-head/statusline.go`:

```go
package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

// statuslineSource is the "source" field this command stamps into the cache,
// distinguishing our writer from abtop's (which wrote "claude").
const statuslineSource = "claudemux"

// statuslinePayload is the subset of Claude Code's statusline JSON we consume.
// Pointers throughout: rate_limits is absent entirely for non-subscribers, and
// each window is independently optional, so "absent" must be distinguishable
// from "zero" — a zeroed cache would render a confident 0% meter.
type statuslinePayload struct {
	RateLimits *struct {
		FiveHour *statuslineWindow `json:"five_hour"`
		SevenDay *statuslineWindow `json:"seven_day"`
	} `json:"rate_limits"`
}

type statuslineWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
}

// runStatusline implements `claudemux-head statusline`, registered in the
// statusLine slot of ~/.claude/settings.json. It reads the payload Claude Code
// writes to stdin and caches the rate-limit windows for the head panes.
//
// It ALWAYS exits 0 and ALWAYS stays silent on stdout. Whatever a statusline
// command prints becomes the user's status line, and any non-zero exit is
// surfaced to them as a broken statusline — neither is an acceptable outcome
// for a cache write that only claudemux cares about.
func runStatusline(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return 0
	}
	var payload statuslinePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return 0
	}
	if payload.RateLimits == nil {
		// Not a Claude.ai subscriber session (API key, Bedrock, Vertex), or no
		// API response yet. Leave any previous cache alone rather than
		// overwriting real numbers with zeros.
		return 0
	}

	out := rawRateLimitsFile{Source: statuslineSource, UpdatedAt: time.Now().Unix()}
	if w := payload.RateLimits.FiveHour; w != nil {
		out.FiveHour.UsedPercentage = w.UsedPercentage
		out.FiveHour.ResetsAt = w.ResetsAt
	}
	if w := payload.RateLimits.SevenDay; w != nil {
		out.SevenDay.UsedPercentage = w.UsedPercentage
		out.SevenDay.ResetsAt = w.ResetsAt
	}

	path := defaultStatuslineCachePath()
	if path == "" {
		return 0
	}
	if err := writeJSONAtomic(path, out); err != nil {
		return 0
	}
	return 0
}

// writeJSONAtomic marshals v and installs it at path via a temp file and
// rename, so a head reading the cache mid-write never sees a partial file.
func writeJSONAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	blob, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// defaultStatuslineCachePath is where this command writes and the head reads.
// CLAUDEMUX_RATE_LIMITS_PATH overrides both halves in step, which is what lets
// the tests point the pair at a temp dir.
func defaultStatuslineCachePath() string {
	if p := os.Getenv("CLAUDEMUX_RATE_LIMITS_PATH"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "claudemux", "rate-limits.json")
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/claudemux-head/ -run TestStatusline -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Register the subcommand**

In `cmd/claudemux-head/main.go`, add `"statusline"` to `knownSubcommands` (line 20):

```go
var knownSubcommands = []string{"version", "config", "boot", "hook", "switchboard", "onboard", "banner", "project", "statusline"}
```

And add this dispatch block immediately after the `hook` block (after `main.go:88`):

```go
	// `statusline` is registered in the statusLine slot of ~/.claude/settings.json
	// and invoked by Claude Code on every render with the session payload on
	// stdin. Dispatched here, before flag.Parse(), for the same reason every
	// other subcommand is.
	if len(os.Args) > 1 && os.Args[1] == "statusline" {
		os.Exit(runStatusline(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
```

- [ ] **Step 6: Run the suite**

Run: `go test ./...`
Expected: PASS. `TestKnownSubcommandsAreAccepted` (`main_test.go:9`) covers the new entry automatically.

- [ ] **Step 7: Commit**

```bash
gofmt -w cmd/claudemux-head/statusline.go cmd/claudemux-head/statusline_test.go cmd/claudemux-head/main.go
git add cmd/claudemux-head/statusline.go cmd/claudemux-head/statusline_test.go cmd/claudemux-head/main.go
git commit -m "feat(meters): add claudemux-owned statusline cache writer"
```

---

### Task 2: Read our cache, fall back to abtop's

**Files:**
- Modify: `cmd/claudemux-head/budget.go:171-181` (`defaultRateLimitsPath`)
- Modify: `cmd/claudemux-head/budget_test.go` (append)

**Interfaces:**
- Consumes: `defaultStatuslineCachePath()` from Task 1.
- Produces: `defaultRateLimitsPath()` keeps its signature `func() string`; behavior changes to prefer our path.

- [ ] **Step 1: Write the failing test**

Append to `cmd/claudemux-head/budget_test.go`:

```go
// The head reads OUR cache when it exists. The abtop file is only a migration
// fallback, so a present claudemux cache must win even when both exist.
func TestDefaultRateLimitsPathPrefersOurCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDEMUX_RATE_LIMITS_PATH", "")

	ours := filepath.Join(home, ".claude", "claudemux", "rate-limits.json")
	abtop := filepath.Join(home, ".claude", "abtop-rate-limits.json")
	for _, p := range []string{ours, abtop} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(`{"source":"x"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := defaultRateLimitsPath(); got != ours {
		t.Errorf("defaultRateLimitsPath() = %q, want %q", got, ours)
	}
}

// Upgrades land between `hook ensure` registering our statusline and Claude
// Code next rendering one, so for one release a machine can have only abtop's
// file. Falling back keeps the meters alive across that gap.
func TestDefaultRateLimitsPathFallsBackToAbtop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDEMUX_RATE_LIMITS_PATH", "")

	abtop := filepath.Join(home, ".claude", "abtop-rate-limits.json")
	if err := os.MkdirAll(filepath.Dir(abtop), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abtop, []byte(`{"source":"claude"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := defaultRateLimitsPath(); got != abtop {
		t.Errorf("defaultRateLimitsPath() = %q, want %q", got, abtop)
	}
}

// With neither file present, return our path: readRateLimits then fails with a
// plain not-exist error against the location we actually want populated.
func TestDefaultRateLimitsPathNeitherPresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDEMUX_RATE_LIMITS_PATH", "")

	want := filepath.Join(home, ".claude", "claudemux", "rate-limits.json")
	if got := defaultRateLimitsPath(); got != want {
		t.Errorf("defaultRateLimitsPath() = %q, want %q", got, want)
	}
}

// The explicit override still beats both.
func TestDefaultRateLimitsPathHonorsOverride(t *testing.T) {
	t.Setenv("CLAUDEMUX_RATE_LIMITS_PATH", "/tmp/explicit.json")
	if got := defaultRateLimitsPath(); got != "/tmp/explicit.json" {
		t.Errorf("defaultRateLimitsPath() = %q, want the override", got)
	}
}
```

Ensure `budget_test.go` imports `os` and `path/filepath`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run TestDefaultRateLimitsPath -v`
Expected: FAIL — the abtop path is returned unconditionally, so the "prefers ours" and "neither present" cases fail.

- [ ] **Step 3: Write the implementation**

Replace `defaultRateLimitsPath` in `cmd/claudemux-head/budget.go` (lines 171-181):

```go
// defaultRateLimitsPath returns the rate-limit cache the head reads. Ours wins
// when present; abtop's is a migration fallback kept for one release, because
// an upgrade lands between `hook ensure` registering our statusline command and
// Claude Code next invoking one — and the meters must not blank in that gap.
// CLAUDEMUX_RATE_LIMITS_PATH overrides everything.
func defaultRateLimitsPath() string {
	if p := os.Getenv("CLAUDEMUX_RATE_LIMITS_PATH"); p != "" {
		return p
	}
	ours := defaultStatuslineCachePath()
	if ours == "" {
		return ""
	}
	if _, err := os.Stat(ours); err == nil {
		return ours
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ours
	}
	abtop := filepath.Join(home, ".claude", "abtop-rate-limits.json")
	if _, err := os.Stat(abtop); err == nil {
		return abtop
	}
	return ours
}
```

Add `"path/filepath"` to `budget.go`'s imports.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/claudemux-head/ -run TestDefaultRateLimitsPath -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Run the suite**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
gofmt -w cmd/claudemux-head/budget.go cmd/claudemux-head/budget_test.go
git add cmd/claudemux-head/budget.go cmd/claudemux-head/budget_test.go
git commit -m "feat(meters): read our rate-limit cache, fall back to abtop's"
```

---

### Task 3: `hook ensure` claims the `statusLine` slot

**Files:**
- Modify: `cmd/claudemux-head/hook.go` — add `setStatusLine`, call it from `runHookEnsure` (`hook.go:180-186`, inside the loop's aftermath, before the `if !changed` early return at `hook.go:185`)
- Modify: `cmd/claudemux-head/hook_test.go` (append)

**Interfaces:**
- Produces: `func setStatusLine(settings map[string]any, command string, stderr io.Writer) bool` — reports whether it mutated `settings`.

- [ ] **Step 1: Write the failing test**

Append to `cmd/claudemux-head/hook_test.go`:

```go
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
```

Ensure `hook_test.go` imports `bytes` and `strings`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run TestSetStatusLine -v`
Expected: FAIL — `undefined: setStatusLine`

- [ ] **Step 3: Write the implementation**

Add to `cmd/claudemux-head/hook.go`:

```go
// statuslineScriptName is the shipped statusline artifact's filename in
// ~/.claude/hooks/. It is a copy of this binary rather than a shell script:
// abtop's shim spawned bash AND python3 on every statusline render.
const statuslineScriptName = "claudemux-statusline"

// abtopStatuslineName is the shim we are replacing, matched by basename so the
// user's install prefix does not matter.
const abtopStatuslineName = "abtop-statusline.sh"

// setStatusLine points settings["statusLine"] at our command, mutating settings
// in place. Reports whether anything changed.
//
// Unlike hooks.<event>, which is an append-only list every tool can add to,
// statusLine is a single exclusive string: claiming it takes it away from
// whoever had it. So the claim is narrow — an empty slot, abtop's shim (what
// this feature exists to remove), or a slot we already own. Anything else is a
// statusline the user chose, and we decline and say so rather than clobbering
// it; the meters simply stay dark, which is recoverable, whereas a silently
// destroyed statusline is not.
func setStatusLine(settings map[string]any, command string, stderr io.Writer) bool {
	existing, _ := settings["statusLine"].(map[string]any)
	if existing != nil {
		cur, _ := existing["command"].(string)
		switch {
		case cur == command:
			return false // already ours
		case cur != "" && filepath.Base(cur) != abtopStatuslineName:
			fmt.Fprintf(stderr, "claudemux: leaving your statusLine (%s) alone; "+
				"account meters need claudemux's own statusline command, so they will stay unavailable\n", cur)
			return false
		}
	}
	settings["statusLine"] = map[string]any{"type": "command", "command": command}
	return true
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/claudemux-head/ -run TestSetStatusLine -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Ship and register the artifact**

Two changes in `runHookEnsure`.

First, install the statusline artifact as a copy of this binary. Add this immediately **after** the `for i, hs := range hookScripts` loop and **before** the `if !changed` early return (`hook.go:185`):

```go
	// The statusline artifact is this binary itself, copied into hooksDir for
	// the same reason the scripts are: settings.json must not carry a Homebrew
	// libexec path that the next upgrade invalidates.
	selfExe, err := os.Executable()
	if err == nil {
		selfExe, err = filepath.EvalSymlinks(selfExe)
	}
	if err != nil {
		fmt.Fprintf(stderr, "claudemux: locating this binary for the statusline: %v\n", err)
		return 4
	}
	slDst := filepath.Join(hooksDir, statuslineScriptName)
	if err := copyExecutable(selfExe, slDst); err != nil {
		fmt.Fprintf(stderr, "claudemux: installing %s: %v\n", statuslineScriptName, err)
		return 4
	}
	if setStatusLine(settings, slDst+" statusline", stderr) {
		changed = true
	}
```

Note the registered command is `<path> statusline` — Claude Code runs the `statusLine.command` string through a shell, so the subcommand argument rides along.

- [ ] **Step 6: Run the suite**

Run: `go test ./...`
Expected: PASS. Existing `hook ensure` tests use a `--script` stub directory; the statusline copy uses `os.Executable()` (the test binary), which copies harmlessly into the temp hooks dir.

- [ ] **Step 7: Verify against a real settings file**

```bash
go build -o /tmp/cmx-head ./cmd/claudemux-head
cp ~/.claude/settings.json /tmp/settings-before.json
/tmp/cmx-head hook ensure
python3 -c "import json;print(json.load(open('$HOME/.claude/settings.json'))['statusLine'])"
```
Expected: the `statusLine` command ends in `claudemux-statusline statusline`, and a `.bak-<epoch>` backup of the previous settings exists beside it.

- [ ] **Step 8: Commit**

```bash
gofmt -w cmd/claudemux-head/hook.go cmd/claudemux-head/hook_test.go
git add cmd/claudemux-head/hook.go cmd/claudemux-head/hook_test.go
git commit -m "feat(meters): claim the statusLine slot from abtop's shim"
```

---

### Task 4: `get_usage` client over the SDK control protocol

Pure client: spawn, speak the protocol, parse, return. No caching, no TUI — those are Tasks 5 and 6.

**Files:**
- Create: `cmd/claudemux-head/usage.go`
- Create: `cmd/claudemux-head/usage_test.go`
- Create: `cmd/claudemux-head/testdata/get_usage_response.json`

**Interfaces:**
- Produces:
  - `type ModelWindow struct { Name string; UsedPercent int; ResetsAt time.Time }`
  - `type PlanUsage struct { Available bool; Models []ModelWindow; FetchedAt time.Time }`
  - `func fetchPlanUsage(ctx context.Context, claudeBin string, now time.Time) (PlanUsage, error)`
  - `func parseUsageResponse(blob []byte, now time.Time) (PlanUsage, error)`

- [ ] **Step 1: Save the captured response as a fixture**

Create `cmd/claudemux-head/testdata/get_usage_response.json` — the real payload from a live Max account, trimmed to the fields we read plus enough neighbors to prove we ignore them:

```json
{"type":"control_response","response":{"subtype":"success","request_id":"usage-1","response":{
  "session":{"total_cost_usd":0,"model_usage":{}},
  "subscription_type":"max",
  "rate_limits_available":true,
  "rate_limits":{
    "five_hour":{"utilization":12,"resets_at":"2026-08-21T19:10:00.048003+00:00"},
    "seven_day":{"utilization":62,"resets_at":"2026-08-24T21:00:00.048024+00:00"},
    "seven_day_opus":null,
    "seven_day_sonnet":null,
    "limits":[
      {"kind":"session","group":"session","percent":12,"resets_at":"2026-08-21T19:10:00.048003+00:00","scope":null,"is_active":false},
      {"kind":"weekly_all","group":"weekly","percent":62,"resets_at":"2026-08-24T21:00:00.048024+00:00","scope":null,"is_active":true},
      {"kind":"weekly_scoped","group":"weekly","percent":26,"resets_at":"2026-08-24T21:00:00.048336+00:00","scope":{"model":{"id":null,"display_name":"Fable"},"surface":null},"is_active":false}
    ]
  }
}}}
```

- [ ] **Step 2: Write the failing test**

Create `cmd/claudemux-head/usage_test.go`:

```go
package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// fakeClaude writes a shell script that impersonates `claude` speaking the SDK
// stdio control protocol: it emits body on stdout, then drains stdin until EOF
// so the client's writes never SIGPIPE.
func fakeClaude(t *testing.T, body string, sleepSecs int, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-claude")
	script := "#!/bin/sh\n"
	if sleepSecs > 0 {
		script += "sleep " + strconv.Itoa(sleepSecs) + "\n"
	}
	if body != "" {
		script += "cat <<'FAKEEOF'\n" + body + "\nFAKEEOF\n"
	}
	script += "cat >/dev/null\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The happy path: noise before the answer, an init response we must not
// mistake for ours, and the Fable row pulled out of limits[].
func TestFetchPlanUsageParsesModelWindows(t *testing.T) {
	body := `{"type":"system","subtype":"hook_started","hook_name":"SessionStart"}
{"type":"control_response","response":{"subtype":"success","request_id":"init-1","response":{"commands":[]}}}
` + fixture(t, "get_usage_response.json")

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	got, err := fetchPlanUsage(context.Background(), fakeClaude(t, body, 0, 0), now)
	if err != nil {
		t.Fatalf("fetchPlanUsage: %v", err)
	}
	if !got.Available {
		t.Error("Available = false, want true")
	}
	if len(got.Models) != 1 {
		t.Fatalf("Models = %+v, want exactly the one weekly_scoped row", got.Models)
	}
	if got.Models[0].Name != "Fable" {
		t.Errorf("Models[0].Name = %q, want \"Fable\"", got.Models[0].Name)
	}
	if got.Models[0].UsedPercent != 26 {
		t.Errorf("Models[0].UsedPercent = %d, want 26", got.Models[0].UsedPercent)
	}
	if got.Models[0].ResetsAt.IsZero() {
		t.Error("Models[0].ResetsAt is zero, want the parsed ISO 8601 timestamp")
	}
	if !got.FetchedAt.Equal(now) {
		t.Errorf("FetchedAt = %v, want %v", got.FetchedAt, now)
	}
}

// session and weekly_all rows are already covered by the 5h/wk gauges. Only
// weekly_scoped rows with a model display_name become meters.
func TestParseUsageResponseIgnoresUnscopedLimits(t *testing.T) {
	blob := []byte(`{"rate_limits_available":true,"rate_limits":{"limits":[
		{"kind":"session","percent":12,"scope":null},
		{"kind":"weekly_all","percent":62,"scope":null},
		{"kind":"weekly_scoped","percent":5,"scope":{"model":null}},
		{"kind":"weekly_scoped","percent":9,"scope":{"model":{"display_name":""}}}
	]}}`)
	got, err := parseUsageResponse(blob, time.Now())
	if err != nil {
		t.Fatalf("parseUsageResponse: %v", err)
	}
	if len(got.Models) != 0 {
		t.Errorf("Models = %+v, want none", got.Models)
	}
}

// API key / Bedrock / Vertex sessions. The caller uses this to stop polling.
func TestParseUsageResponseUnavailable(t *testing.T) {
	got, err := parseUsageResponse([]byte(`{"rate_limits_available":false,"rate_limits":null}`), time.Now())
	if err != nil {
		t.Fatalf("parseUsageResponse: %v", err)
	}
	if got.Available {
		t.Error("Available = true, want false")
	}
	if len(got.Models) != 0 {
		t.Errorf("Models = %+v, want none", got.Models)
	}
}

// get_usage is Experimental. A shape change must degrade to no rows, not to a
// parse error that a caller might surface as a broken meter line.
func TestParseUsageResponseShapeChange(t *testing.T) {
	got, err := parseUsageResponse([]byte(`{"rate_limits_available":true,"rate_limits":{"limits":"suddenly a string"}}`), time.Now())
	if err != nil {
		t.Fatalf("parseUsageResponse should tolerate a shape change, got %v", err)
	}
	if len(got.Models) != 0 {
		t.Errorf("Models = %+v, want none", got.Models)
	}
}

// A wedged claude must not wedge the pane.
func TestFetchPlanUsageTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := fetchPlanUsage(ctx, fakeClaude(t, "", 9, 0), time.Now()); err == nil {
		t.Fatal("fetchPlanUsage returned nil error on a hung child, want an error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("fetchPlanUsage took %v, want it to abandon the child promptly", elapsed)
	}
}

// A claude that dies without answering is an error, not an empty success —
// empty success would be cached as "no model rows" for the full TTL.
func TestFetchPlanUsageChildFails(t *testing.T) {
	if _, err := fetchPlanUsage(context.Background(), fakeClaude(t, "", 0, 1), time.Now()); err == nil {
		t.Fatal("fetchPlanUsage returned nil error for a failing child, want an error")
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run "TestFetchPlanUsage|TestParseUsageResponse" -v`
Expected: FAIL — `undefined: fetchPlanUsage`, `undefined: parseUsageResponse`

- [ ] **Step 4: Write the implementation**

Create `cmd/claudemux-head/usage.go`:

```go
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// ModelWindow is one per-model weekly limit — "Fable", "Opus", "Sonnet" —
// from the usage endpoint's limits[] array.
type ModelWindow struct {
	Name        string
	UsedPercent int
	ResetsAt    time.Time
}

// PlanUsage is the subset of a get_usage response the meters render.
type PlanUsage struct {
	// Available mirrors rate_limits_available: false for API key, Bedrock,
	// Vertex, or a missing profile scope. The poller stops when it sees this.
	Available bool
	Models    []ModelWindow
	FetchedAt time.Time
}

// usageTimeout bounds one poll. The measured spawn is ~2.2s including the
// user's SessionStart hooks, so this leaves generous headroom while still
// guaranteeing the poll goroutine returns.
const usageTimeout = 20 * time.Second

// fetchPlanUsage spawns a short-lived Claude Code and asks it, over the SDK
// stdio control protocol, for the structured /usage data.
//
// Claude Code holds the OAuth credential, so we never touch the keychain: the
// endpoint behind this (GET /api/oauth/usage) is gated on the OAuth account and
// is unreachable with the platform API key the summarizer uses. The call runs
// no inference — the probe that established this returned total_cost_usd 0 and
// an empty model_usage — so it spends no tokens and none of the limit it
// reports.
func fetchPlanUsage(ctx context.Context, claudeBin string, now time.Time) (PlanUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, usageTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudeBin,
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--no-session-persistence",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
	)
	// TMUX_PANE must not reach the child: claudemux-map.sh keys its pane-map
	// file on it and exits immediately when it is absent. Without this, a poll
	// spawned from a head pane writes a phantom session into that pane's map
	// entry. Everything else in the environment is kept — the OAuth profile
	// resolution depends on HOME and CLAUDE_CONFIG_DIR.
	cmd.Env = filterEnv(os.Environ(), "TMUX_PANE")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return PlanUsage{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return PlanUsage{}, err
	}
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return PlanUsage{}, err
	}
	defer func() {
		stdin.Close()
		_ = cmd.Wait()
	}()

	// initialize must precede any other control request; get_usage follows
	// immediately, and the response is matched by request_id rather than by
	// arrival order because hook lifecycle events interleave freely.
	for _, req := range []string{
		`{"type":"control_request","request_id":"init-1","request":{"subtype":"initialize"}}`,
		`{"type":"control_request","request_id":"usage-1","request":{"subtype":"get_usage"}}`,
	} {
		if _, err := fmt.Fprintln(stdin, req); err != nil {
			return PlanUsage{}, err
		}
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // responses embed a transcript scan
	for scanner.Scan() {
		var env struct {
			Type     string `json:"type"`
			Response struct {
				Subtype   string          `json:"subtype"`
				RequestID string          `json:"request_id"`
				Response  json.RawMessage `json:"response"`
				Error     string          `json:"error"`
			} `json:"response"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
			continue // hook events and other stream noise
		}
		if env.Type != "control_response" || env.Response.RequestID != "usage-1" {
			continue
		}
		if env.Response.Subtype != "success" {
			return PlanUsage{}, fmt.Errorf("get_usage failed: %s", env.Response.Error)
		}
		return parseUsageResponse(env.Response.Response, now)
	}
	if err := ctx.Err(); err != nil {
		return PlanUsage{}, err
	}
	if err := scanner.Err(); err != nil {
		return PlanUsage{}, err
	}
	return PlanUsage{}, errors.New("claude exited without answering get_usage")
}

// filterEnv returns environ minus any entry whose name is key.
func filterEnv(environ []string, key string) []string {
	out := environ[:0:0]
	prefix := key + "="
	for _, kv := range environ {
		if len(kv) >= len(prefix) && kv[:len(prefix)] == prefix {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// parseUsageResponse extracts the per-model weekly windows from a get_usage
// response body.
//
// The rows come from limits[], NOT from the named seven_day_opus /
// seven_day_sonnet keys: on a live Max account both named keys were null while
// limits[] carried the Fable row. session and weekly_all rows are skipped —
// the 5h and wk gauges already cover them from the free statusline path.
//
// The shape is documented Experimental, so every field is decoded into its own
// tolerant type and a mismatch yields zero rows rather than an error. Only a
// body that is not JSON at all fails.
func parseUsageResponse(blob []byte, now time.Time) (PlanUsage, error) {
	var body struct {
		Available  bool `json:"rate_limits_available"`
		RateLimits *struct {
			Limits []struct {
				Kind     string   `json:"kind"`
				Percent  *float64 `json:"percent"`
				ResetsAt string   `json:"resets_at"`
				Scope    *struct {
					Model *struct {
						DisplayName string `json:"display_name"`
					} `json:"model"`
				} `json:"scope"`
			} `json:"limits"`
		} `json:"rate_limits"`
	}
	if err := json.Unmarshal(blob, &body); err != nil {
		// A limits[] that changed type (array → string, say) fails the whole
		// decode. That is a shape change, not a broken response: report no
		// rows and let the 5h/wk gauges carry on.
		var probe map[string]json.RawMessage
		if json.Unmarshal(blob, &probe) != nil {
			return PlanUsage{}, err
		}
		var avail bool
		if raw, ok := probe["rate_limits_available"]; ok {
			_ = json.Unmarshal(raw, &avail)
		}
		return PlanUsage{Available: avail, FetchedAt: now}, nil
	}

	usage := PlanUsage{Available: body.Available, FetchedAt: now}
	if body.RateLimits == nil {
		return usage, nil
	}
	for _, l := range body.RateLimits.Limits {
		if l.Kind != "weekly_scoped" || l.Scope == nil || l.Scope.Model == nil {
			continue
		}
		name := l.Scope.Model.DisplayName
		if name == "" || l.Percent == nil {
			continue
		}
		w := ModelWindow{Name: name, UsedPercent: int(*l.Percent + 0.5)}
		if t, err := time.Parse(time.RFC3339, l.ResetsAt); err == nil {
			w.ResetsAt = t
		}
		usage.Models = append(usage.Models, w)
	}
	return usage, nil
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./cmd/claudemux-head/ -run "TestFetchPlanUsage|TestParseUsageResponse" -v`
Expected: PASS (6 tests)

- [ ] **Step 6: Verify against the real binary**

This is the one step that proves the protocol still matches the installed Claude Code. Nothing downstream is worth building if it prints nothing:

```bash
printf '%s\n%s\n' \
  '{"type":"control_request","request_id":"init-1","request":{"subtype":"initialize"}}' \
  '{"type":"control_request","request_id":"usage-1","request":{"subtype":"get_usage"}}' \
  | claude -p --input-format stream-json --output-format stream-json --verbose \
  | grep -o '"kind":"weekly_scoped".\{0,160\}'
```
Expected: a `weekly_scoped` row with `"display_name":"Fable"`. If this prints nothing, `get_usage`'s shape has changed and Task 4 needs revisiting before Tasks 5-6 build on it.

- [ ] **Step 7: Commit**

```bash
gofmt -w cmd/claudemux-head/usage.go cmd/claudemux-head/usage_test.go
git add cmd/claudemux-head/usage.go cmd/claudemux-head/usage_test.go cmd/claudemux-head/testdata/get_usage_response.json
git commit -m "feat(meters): add get_usage client for per-model weekly windows"
```

---

### Task 5: Single-flight TTL cache

**Files:**
- Modify: `cmd/claudemux-head/usage.go` (append)
- Modify: `cmd/claudemux-head/usage_test.go` (append)

**Interfaces:**
- Consumes: `PlanUsage`, `fetchPlanUsage`, `writeJSONAtomic` (Task 1).
- Produces:
  - `func defaultUsageCachePath() string`
  - `func writeUsageCache(path string, u PlanUsage) error`
  - `func readUsageCache(path string, now time.Time) (PlanUsage, bool)` — second return is "fresh enough to use"
  - `func refreshUsageCache(ctx context.Context, path, claudeBin string, now time.Time) (PlanUsage, error)`
  - `func usageClaudeBin() string`
  - `const usageTTL = 15 * time.Minute`

- [ ] **Step 1: Write the failing test**

Append to `cmd/claudemux-head/usage_test.go`:

```go
// A cache written inside the TTL is served as-is: no spawn.
func TestReadUsageCacheFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	want := PlanUsage{
		Available: true,
		Models:    []ModelWindow{{Name: "Fable", UsedPercent: 26, ResetsAt: now.Add(72 * time.Hour)}},
		FetchedAt: now.Add(-5 * time.Minute),
	}
	if err := writeUsageCache(path, want); err != nil {
		t.Fatal(err)
	}
	got, fresh := readUsageCache(path, now)
	if !fresh {
		t.Fatal("fresh = false for a 5-minute-old cache, want true")
	}
	if len(got.Models) != 1 || got.Models[0].Name != "Fable" || got.Models[0].UsedPercent != 26 {
		t.Errorf("Models = %+v, want the Fable row round-tripped", got.Models)
	}
	if !got.Models[0].ResetsAt.Equal(want.Models[0].ResetsAt) {
		t.Errorf("ResetsAt = %v, want %v", got.Models[0].ResetsAt, want.Models[0].ResetsAt)
	}
}

// Past the TTL the rows are still returned — a stale Fable percentage beats no
// meter — but the caller is told to refresh.
func TestReadUsageCacheStaleStillReturnsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	if err := writeUsageCache(path, PlanUsage{
		Available: true,
		Models:    []ModelWindow{{Name: "Fable", UsedPercent: 26}},
		FetchedAt: now.Add(-usageTTL - time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	got, fresh := readUsageCache(path, now)
	if fresh {
		t.Error("fresh = true past the TTL, want false")
	}
	if len(got.Models) != 1 {
		t.Errorf("Models = %+v, want the stale row still served", got.Models)
	}
}

func TestReadUsageCacheMissing(t *testing.T) {
	got, fresh := readUsageCache(filepath.Join(t.TempDir(), "absent.json"), time.Now())
	if fresh {
		t.Error("fresh = true for a missing cache, want false")
	}
	if len(got.Models) != 0 {
		t.Errorf("Models = %+v, want none", got.Models)
	}
}

// A truncated or hand-mangled cache must not wedge the poller.
func TestReadUsageCacheCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, fresh := readUsageCache(path, time.Now()); fresh {
		t.Error("fresh = true for a corrupt cache, want false")
	}
}

// The whole point of the lock: ten head panes must cause one spawn. The fake
// appends a line per invocation so we can count them.
func TestRefreshUsageCacheIsSingleFlight(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	counter := filepath.Join(dir, "spawns")

	bin := filepath.Join(dir, "counting-claude")
	body := fixture(t, "get_usage_response.json")
	script := "#!/bin/sh\necho x >> " + counter + "\nsleep 1\ncat <<'FAKEEOF'\n" + body + "\nFAKEEOF\ncat >/dev/null\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	done := make(chan struct{})
	for i := 0; i < 5; i++ {
		go func() {
			defer close4(done)
			_, _ = refreshUsageCache(context.Background(), path, bin, now)
		}()
	}
	for i := 0; i < 5; i++ {
		<-done
	}

	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("no spawn recorded at all: %v", err)
	}
	if n := len(splitLines(string(data))); n != 1 {
		t.Errorf("spawned %d times, want exactly 1 (the lock did not hold)", n)
	}
}
```

Add these two helpers to `usage_test.go`:

```go
// close4 signals one completion on a shared channel without closing it.
func close4(ch chan struct{}) { ch <- struct{}{} }

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
```

Add `"strings"` to `usage_test.go`'s imports.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run "TestReadUsageCache|TestRefreshUsageCache" -v`
Expected: FAIL — `undefined: writeUsageCache`, `undefined: readUsageCache`, `undefined: refreshUsageCache`, `undefined: usageTTL`

- [ ] **Step 3: Write the implementation**

Append to `cmd/claudemux-head/usage.go`:

```go
// usageTTL is how long a cached usage read stays authoritative.
//
// Fifteen minutes because a poll is not free: it spawns a Claude Code (~2.2s)
// and fires every SessionStart hook the user has installed. Suppressing those
// hooks means isolating CLAUDE_CONFIG_DIR, which also loses the OAuth profile
// and so loses the answer. Weekly windows move slowly enough that this costs
// nothing in freshness, and the fast-moving 5h meter does not come from here at
// all — it rides the free statusline path.
const usageTTL = 15 * time.Minute

// usageLockStale is when a lock file is presumed abandoned by a crashed poller.
const usageLockStale = 2 * time.Minute

// rawUsageCache is the on-disk shape. Timestamps are Unix seconds so the file
// stays readable by eye and by jq.
type rawUsageCache struct {
	FetchedAt int64 `json:"fetched_at"`
	Available bool  `json:"available"`
	Models    []struct {
		Name        string `json:"name"`
		UsedPercent int    `json:"used_percent"`
		ResetsAt    int64  `json:"resets_at"`
	} `json:"models"`
}

// usageClaudeBin is the Claude Code the poller spawns. Resolved from PATH by
// default; the env override is what lets a dev point the poller at a specific
// build without touching PATH.
func usageClaudeBin() string {
	if p := os.Getenv("CLAUDEMUX_CLAUDE_BIN"); p != "" {
		return p
	}
	return "claude"
}

func defaultUsageCachePath() string {
	if p := os.Getenv("CLAUDEMUX_USAGE_CACHE_PATH"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "claudemux", "usage.json")
}

func writeUsageCache(path string, u PlanUsage) error {
	raw := rawUsageCache{FetchedAt: u.FetchedAt.Unix(), Available: u.Available}
	for _, m := range u.Models {
		var resets int64
		if !m.ResetsAt.IsZero() {
			resets = m.ResetsAt.Unix()
		}
		raw.Models = append(raw.Models, struct {
			Name        string `json:"name"`
			UsedPercent int    `json:"used_percent"`
			ResetsAt    int64  `json:"resets_at"`
		}{Name: m.Name, UsedPercent: m.UsedPercent, ResetsAt: resets})
	}
	return writeJSONAtomic(path, raw)
}

// readUsageCache loads the cache. The second return reports whether it is
// within the TTL; the rows are returned either way, because a fifteen-minute-old
// Fable percentage is far more useful than a missing meter.
func readUsageCache(path string, now time.Time) (PlanUsage, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PlanUsage{}, false
	}
	var raw rawUsageCache
	if err := json.Unmarshal(data, &raw); err != nil {
		return PlanUsage{}, false
	}
	u := PlanUsage{Available: raw.Available, FetchedAt: time.Unix(raw.FetchedAt, 0)}
	for _, m := range raw.Models {
		w := ModelWindow{Name: m.Name, UsedPercent: m.UsedPercent}
		if m.ResetsAt != 0 {
			w.ResetsAt = time.Unix(m.ResetsAt, 0)
		}
		u.Models = append(u.Models, w)
	}
	age := now.Sub(u.FetchedAt)
	return u, age >= 0 && age < usageTTL
}

// refreshUsageCache fetches and caches, taking a lock first so that N head
// panes on one machine cause one spawn rather than N. A caller that loses the
// race returns the current cache immediately instead of waiting.
func refreshUsageCache(ctx context.Context, path, claudeBin string, now time.Time) (PlanUsage, error) {
	lock := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return PlanUsage{}, err
	}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		// Someone else holds it — unless they crashed and left it behind.
		if st, statErr := os.Stat(lock); statErr == nil && now.Sub(st.ModTime()) > usageLockStale {
			os.Remove(lock)
		}
		cached, _ := readUsageCache(path, now)
		return cached, nil
	}
	f.Close()
	defer os.Remove(lock)

	usage, err := fetchPlanUsage(ctx, claudeBin, now)
	if err != nil {
		return PlanUsage{}, err
	}
	if writeErr := writeUsageCache(path, usage); writeErr != nil {
		// The fetch succeeded; a cache we could not persist still serves this
		// process for this tick.
		return usage, nil
	}
	return usage, nil
}
```

Add `"path/filepath"` to `usage.go`'s imports.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/claudemux-head/ -run "TestReadUsageCache|TestRefreshUsageCache" -v`
Expected: PASS (5 tests)

- [ ] **Step 5: Run the suite**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
gofmt -w cmd/claudemux-head/usage.go cmd/claudemux-head/usage_test.go
git add cmd/claudemux-head/usage.go cmd/claudemux-head/usage_test.go
git commit -m "feat(meters): single-flight TTL cache for plan usage"
```

---

### Task 6: Render per-model rows in both panels

`rateGauges` gains a return type because both callers need to know how many surviving parts carry a bar in order to widen them. Today that count is hardcoded (3 in the head, 2 in the switchboard) on the assumption that only ctx/5h/wk have bars — an assumption model rows break.

**Files:**
- Modify: `cmd/claudemux-head/tui.go:1666-1697` (`rateGaugeParts`, `rateGauges`), `tui.go:1590`, `tui.go:1873-1907` (`renderMetersLine`), `tui.go:199-215` (model fields), `tui.go:744-800` (`pollData`, `dataMsg`), `tui.go:1155-1170` (Update)
- Modify: `cmd/claudemux-head/switchboardtui.go:710-740` (`swMetersLine`), `:151-156` (model fields), `:280-300` (`swPollCmd`)
- Modify: `cmd/claudemux-head/tui_test.go`, `cmd/claudemux-head/swmeters_test.go:44`

**Interfaces:**
- Consumes: `PlanUsage`, `ModelWindow` (Task 4); `readUsageCache`, `refreshUsageCache`, `defaultUsageCachePath` (Task 5).
- Produces:
  - `type gaugeSet struct { parts []string; barred int }`
  - `func rateGauges(rl RateLimits, models []ModelWindow, samples []pctSample, now time.Time, barW int) gaugeSet`
  - `func rateGaugeParts(m model, now time.Time, barW int) gaugeSet`
  - `func shortModelMeter(name string) string` — the gauge label, e.g. `"Fable"` → `"fab"`

- [ ] **Step 1: Write the failing test**

Append to `cmd/claudemux-head/tui_test.go`:

```go
// Model rows sit after wk and before the eta, because callers drop from the
// END of the slice and the required drop order is eta → models → wk → 5h.
func TestRateGaugesOrdersModelRowsAfterWeek(t *testing.T) {
	now := time.Now()
	rl := RateLimits{
		FiveHour: Window{UsedPercent: 20, ResetsAt: now.Add(5 * time.Hour)},
		SevenDay: Window{UsedPercent: 30, ResetsAt: now.Add(72 * time.Hour)},
	}
	models := []ModelWindow{{Name: "Fable", UsedPercent: 26, ResetsAt: now.Add(72 * time.Hour)}}
	samples := []pctSample{{at: now.Add(-10 * time.Minute), pct: 10}, {at: now, pct: 20}}

	gs := rateGauges(rl, models, samples, now, defaultBarW)
	if len(gs.parts) != 4 {
		t.Fatalf("parts = %q, want 5h, wk, fab, eta", gs.parts)
	}
	if !strings.Contains(gs.parts[0], "5h") {
		t.Errorf("parts[0] = %q, want the 5h gauge", gs.parts[0])
	}
	if !strings.Contains(gs.parts[1], "wk") {
		t.Errorf("parts[1] = %q, want the wk gauge", gs.parts[1])
	}
	if !strings.Contains(gs.parts[2], "fab") || !strings.Contains(gs.parts[2], "26%") {
		t.Errorf("parts[2] = %q, want the Fable gauge at 26%%", gs.parts[2])
	}
	if !strings.Contains(gs.parts[3], "empty in") {
		t.Errorf("parts[3] = %q, want the eta", gs.parts[3])
	}
	// 5h, wk and fab carry bars; the eta is plain text.
	if gs.barred != 3 {
		t.Errorf("barred = %d, want 3", gs.barred)
	}
}

// No model data — the overwhelmingly common case on Pro, and every case when
// the pull path is broken — must render exactly today's gauges.
func TestRateGaugesWithoutModelsUnchanged(t *testing.T) {
	now := time.Now()
	rl := RateLimits{
		FiveHour: Window{UsedPercent: 20, ResetsAt: now.Add(5 * time.Hour)},
		SevenDay: Window{UsedPercent: 30, ResetsAt: now.Add(72 * time.Hour)},
	}
	gs := rateGauges(rl, nil, nil, now, defaultBarW)
	if len(gs.parts) != 2 {
		t.Fatalf("parts = %q, want just 5h and wk", gs.parts)
	}
	if gs.barred != 2 {
		t.Errorf("barred = %d, want 2", gs.barred)
	}
}

// A row whose percent we have but whose reset time we do not still renders —
// dropping the meter because one field is missing would be a worse outcome.
func TestRateGaugesModelWithoutResetTime(t *testing.T) {
	now := time.Now()
	rl := RateLimits{FiveHour: Window{UsedPercent: 1, ResetsAt: now.Add(time.Hour)}}
	gs := rateGauges(rl, []ModelWindow{{Name: "Fable", UsedPercent: 26}}, nil, now, defaultBarW)
	found := false
	for _, p := range gs.parts {
		if strings.Contains(p, "fab") && strings.Contains(p, "26%") {
			found = true
		}
	}
	if !found {
		t.Errorf("parts = %q, want a fab gauge despite the zero reset time", gs.parts)
	}
}

func TestShortModelMeter(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Fable", "fab"},
		{"Opus", "opu"},
		{"Sonnet", "son"},
		{"X", "x"},
	} {
		if got := shortModelMeter(tc.in); got != tc.want {
			t.Errorf("shortModelMeter(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The full drop order, exercised by shrinking the pane one column at a time.
func TestMetersLineDropOrderWithModels(t *testing.T) {
	now := time.Now()
	m := rateModelForTest(now, 200) // existing helper in tui_test.go
	m.modelWindows = []ModelWindow{{Name: "Fable", UsedPercent: 26, ResetsAt: now.Add(72 * time.Hour)}}

	full := renderMetersLine(m, now)
	for _, want := range []string{"5h", "wk", "fab", "empty in"} {
		if !strings.Contains(full, want) {
			t.Fatalf("full meters line = %q, want %q", full, want)
		}
	}

	sawEta, sawFab := false, false
	for w := lipgloss.Width(full); w >= 20; w-- {
		m.width = w
		line := renderMetersLine(m, now)
		if !sawEta && !strings.Contains(line, "empty in") {
			sawEta = true
			if !strings.Contains(line, "fab") {
				t.Fatalf("at width %d the eta dropped but fab went with it: %q", w, line)
			}
		}
		if sawEta && !sawFab && !strings.Contains(line, "fab") {
			sawFab = true
			if !strings.Contains(line, "wk") {
				t.Fatalf("at width %d fab dropped but wk went with it: %q", w, line)
			}
		}
		if sawFab && strings.Contains(line, "fab") {
			t.Fatalf("at width %d fab came back after dropping: %q", w, line)
		}
	}
	if !sawEta || !sawFab {
		t.Fatalf("never observed the eta and fab drops (eta=%v fab=%v)", sawEta, sawFab)
	}
}
```

If `rateModelForTest` does not already exist in `tui_test.go`, add it, mirroring `swRateModel` in `swmeters_test.go:15`:

```go
func rateModelForTest(now time.Time, width int) model {
	return model{
		width: width, height: 40, rateOK: true,
		rateLimits: RateLimits{
			FiveHour: Window{UsedPercent: 20, ResetsAt: now.Add(5 * time.Hour)},
			SevenDay: Window{UsedPercent: 30, ResetsAt: now.Add(72 * time.Hour)},
		},
		pctSamples: []pctSample{
			{at: now.Add(-10 * time.Minute), pct: 10},
			{at: now, pct: 20},
		},
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run "TestRateGauges|TestShortModelMeter|TestMetersLineDropOrder" -v`
Expected: FAIL — `rateGauges` takes 4 arguments and returns `[]string`; `undefined: shortModelMeter`; `model` has no `modelWindows` field.

- [ ] **Step 3: Change `rateGauges` and add the model gauge**

Replace `rateGaugeParts` and `rateGauges` in `cmd/claudemux-head/tui.go` (lines 1660-1697):

```go
// gaugeSet is the right-group gauges plus how many of them carry a progress
// bar. Callers drop parts from the END and then widen the surviving bars, so
// they need the bar count: it used to be a hardcoded constant, which was only
// correct while 5h and wk were the only bars in the group.
type gaugeSet struct {
	// parts are in the fixed order callers drop from the end of:
	// 5h, wk, model rows…, eta.
	parts []string
	// barred is how many LEADING parts carry a bar. The eta is the only
	// bar-less part and is always last, so this is len(parts) or len(parts)-1.
	barred int
}

// rateGaugeParts builds the right-group budget gauges — 5h, wk, any per-model
// weekly rows, and (when there's enough burn-rate signal) a "empty in X" ETA —
// in the fixed order callers drop from when space is tight: eta, then model
// rows, then wk, then 5h. Returns an empty set when rate-limit data isn't
// available (m.rateOK == false). barW is each gauge's bar cell width.
func rateGaugeParts(m model, now time.Time, barW int) gaugeSet {
	if !m.rateOK {
		return gaugeSet{}
	}
	return rateGauges(m.rateLimits, m.modelWindows, m.pctSamples, now, barW)
}

// rateGauges is the model-independent core of rateGaugeParts, shared with the
// switchboard (swMetersLine) so both panels build the identical gauge text
// from the same raw data.
func rateGauges(rl RateLimits, models []ModelWindow, samples []pctSample, now time.Time, barW int) gaugeSet {
	fhPct := float64(rl.FiveHour.UsedPercent)
	wkPct := float64(rl.SevenDay.UsedPercent)
	parts := []string{
		fmt.Sprintf("5h %s %d%%→%s",
			renderBar(barW, fhPct, thresholdColor(fhPct)),
			rl.FiveHour.UsedPercent,
			rl.FiveHour.ResetsAt.Local().Format("3:04p")),
		fmt.Sprintf("wk %s %d%%→%s",
			renderBar(barW, wkPct, thresholdColor(wkPct)),
			rl.SevenDay.UsedPercent,
			rl.SevenDay.ResetsAt.Local().Format("Mon")),
	}
	// Per-model weekly rows sit here — after wk, before the eta — so that the
	// drop-from-the-end order comes out as eta, models, wk, 5h.
	for _, mw := range models {
		pct := float64(mw.UsedPercent)
		seg := fmt.Sprintf("%s %s %d%%",
			shortModelMeter(mw.Name), renderBar(barW, pct, thresholdColor(pct)), mw.UsedPercent)
		// A row with a percent but no parseable reset time still earns its
		// meter; only the arrow is dropped.
		if !mw.ResetsAt.IsZero() {
			seg += "→" + mw.ResetsAt.Local().Format("Mon")
		}
		parts = append(parts, seg)
	}
	barred := len(parts)

	rate := burnRatePctPerMin(samples, now)
	if rate > 0 {
		eta := etaToEmptyPct(rl.FiveHour.UsedPercent, rate)
		if eta > 0 && now.Add(eta).Before(rl.FiveHour.ResetsAt) {
			parts = append(parts, "empty in "+formatDuration(eta))
		}
	}
	return gaugeSet{parts: parts, barred: barred}
}

// shortModelMeter renders a server-supplied model label as a three-cell gauge
// prefix matching "5h" and "wk" in weight: "Fable" → "fab", "Opus" → "opu".
// The label is whatever the server sent, so this must not assume a known set.
func shortModelMeter(name string) string {
	lower := strings.ToLower(name)
	if len(lower) > 3 {
		lower = lower[:3]
	}
	return lower
}
```

- [ ] **Step 4: Update the two callers' drop-and-widen logic**

In `renderMetersLine` (`tui.go:1873`), replace the `build`, drop, and `barCount` block:

```go
	build := func(barW int) gaugeSet {
		gs := rateGaugeParts(m, now, barW)
		// The ctx gauge is the head's own and always leads; it carries a bar,
		// so it counts toward barred.
		return gaugeSet{
			parts:  append([]string{ctxSegment(m, barW)}, gs.parts...),
			barred: gs.barred + 1,
		}
	}

	gs := build(defaultBarW)
	parts := gs.parts
	for len(parts) > 1 && lipgloss.Width(strings.Join(parts, " · ")) > avail {
		parts = parts[:len(parts)-1]
	}

	// Spend the leftover columns widening every surviving bar equally. Only the
	// parts that carry bars share the slack — the trailing eta is plain text, so
	// its share would just become blank space. barred comes from the gauge set
	// rather than a constant now that per-model rows can add bars.
	barCount := len(parts)
	if barCount > gs.barred {
		barCount = gs.barred
	}
	if barCount < 1 {
		barCount = 1
	}
	if grow := (avail - lipgloss.Width(strings.Join(parts, " · "))) / barCount; grow > 0 {
		wide := build(defaultBarW + grow).parts[:len(parts)]
		if lipgloss.Width(strings.Join(wide, " · ")) <= avail {
			parts = wide
		}
	}
```

In `renderStatusbar` (`tui.go:1590`), change:

```go
	rightParts := rateGaugeParts(m, now, defaultBarW).parts
```

In `swMetersLine` (`switchboardtui.go:711`), apply the same shape:

```go
	build := func(barW int) gaugeSet {
		return rateGauges(m.rateLimits, m.modelWindows, m.pctSamples, now, barW)
	}
	if m.width <= 0 {
		return " " + strings.Join(build(defaultBarW).parts, " · ")
	}
	avail := m.width - 2
	if avail < 1 {
		avail = 1
	}
	gs := build(defaultBarW)
	parts := gs.parts
	for len(parts) > 1 && lipgloss.Width(strings.Join(parts, " · ")) > avail {
		parts = parts[:len(parts)-1]
	}

	barCount := len(parts)
	if barCount > gs.barred {
		barCount = gs.barred
	}
	if barCount < 1 {
		barCount = 1
	}
	if grow := (avail - lipgloss.Width(strings.Join(parts, " · "))) / barCount; grow > 0 {
		wide := build(defaultBarW + grow).parts[:len(parts)]
		if lipgloss.Width(strings.Join(wide, " · ")) <= avail {
			parts = wide
		}
	}
```

And in `swmeters_test.go:44`, change `rateGauges(m.rateLimits, nil, now, defaultBarW)[0]` to:

```go
	fhFloor := lipgloss.Width(rateGauges(m.rateLimits, nil, nil, now, defaultBarW).parts[0]) + 2
```

- [ ] **Step 5: Add the model-window fields and the usage loop**

The usage loop is **self-rescheduling and completely separate from `pollData`**. Do not splice it into the `dataMsg` case: that case has six early returns and no command accumulator (`tui.go:1146-1216`), so a command appended there would be dropped on most paths. A `usageMsg` → `tea.Tick` → `usageTickMsg` → `usageMsg` cycle owns its own cadence and cannot interfere with the data poll.

Add to the `model` struct in `cmd/claudemux-head/tui.go`, beside `rateLimits` (line 213):

```go
	// modelWindows are the per-model weekly limits from the usage cache. Empty
	// whenever the pull path is unavailable or broken, which must never affect
	// the 5h/wk gauges above.
	modelWindows []ModelWindow
	// usageUnavailable records a rate_limits_available:false answer (API key,
	// Bedrock, Vertex). Once seen the loop stops: there is nothing to fetch,
	// and those users should pay zero spawns.
	usageUnavailable bool
```

and beside `rateLimitsPath` (line 199):

```go
	usageCachePath string // ~/.claude/claudemux/usage.json or override
```

Initialize it where `rateLimitsPath` is set (`tui.go:383`):

```go
		usageCachePath:   defaultUsageCachePath(),
```

Add the loop to `tui.go`:

```go
// usageCheckInterval is how often the loop re-examines the usage cache. The
// check is a file read; only a cache older than usageTTL becomes a spawn, so
// checking every minute costs nothing while keeping a cache another pane just
// refreshed from sitting unnoticed for a quarter of an hour.
const usageCheckInterval = time.Minute

type usageTickMsg struct{}

type usageMsg struct {
	usage PlanUsage
	err   error
}

// usageCmd reads the shared cache and, only when it has aged past usageTTL,
// spawns a Claude Code to refresh it. The lock inside refreshUsageCache keeps
// concurrent panes to one spawn between them.
func usageCmd(cachePath string) tea.Cmd {
	return func() tea.Msg {
		now := time.Now()
		if cached, fresh := readUsageCache(cachePath, now); fresh {
			return usageMsg{usage: cached}
		}
		usage, err := refreshUsageCache(context.Background(), cachePath, usageClaudeBin(), now)
		return usageMsg{usage: usage, err: err}
	}
}
```

Handle both messages in `Update`, alongside the existing cases:

```go
	case usageTickMsg:
		return m, usageCmd(m.usageCachePath)

	case usageMsg:
		if msg.err != nil {
			// get_usage is Experimental and the spawn can fail for a dozen
			// reasons. Keep whatever rows we already have, tell the user
			// nothing, and try again on the next tick.
			return m, usageTick()
		}
		m.modelWindows = msg.usage.Models
		if !msg.usage.Available && !msg.usage.FetchedAt.IsZero() {
			// Not a Claude.ai subscriber session. Stop the loop entirely
			// rather than spawning a Claude Code every fifteen minutes to be
			// told the same thing.
			m.usageUnavailable = true
			return m, nil
		}
		return m, usageTick()
```

with the helper:

```go
// usageTick schedules the next cache check.
func usageTick() tea.Cmd {
	return tea.Tick(usageCheckInterval, func(time.Time) tea.Msg { return usageTickMsg{} })
}
```

Finally, start the loop by adding `usageCmd(m.usageCachePath)` to the batch `Init` returns.

Mirror all of it in `cmd/claudemux-head/switchboardtui.go` — the same two fields (`modelWindows`, `usageCachePath`, `usageUnavailable`), the same `usageTickMsg` / `usageMsg` cases in the lobby's `Update`, and `usageCmd(m.usageCachePath)` added to the batch `Init` returns (`switchboardtui.go:254`), initializing `usageCachePath: defaultUsageCachePath()` in `newSwModel` (`switchboardtui.go:204`). `usageCmd`, `usageTick`, `usageTickMsg`, and `usageMsg` are package-level and shared; only the model fields and the two `Update` cases are duplicated. The lobby renders the same gauges and must not be the one panel missing them.

- [ ] **Step 6: Run the tests**

Run: `go test ./cmd/claudemux-head/ -run "TestRateGauges|TestShortModelMeter|TestMetersLine|TestSwMetersLine" -v`
Expected: PASS

- [ ] **Step 7: Run the suite**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 8: Live verification — in a real pane, not a worktree**

A worktree-hosted status pane has previously passed while the shipped feature was broken, so this check is not optional and not a `go run` in this directory.

```bash
git checkout main && git merge --no-ff worktree-self-computed-usage-meters
go install ./cmd/claudemux-head
```

Then, in a real project session, confirm in the head pane and in the lobby (`prefix + s`):
- `5h` and `wk` still render, sourced from `~/.claude/claudemux/rate-limits.json` (check `jq .source` reports `claudemux`).
- A `fab NN%→Mon` gauge appears within ~15 minutes of the pane starting.
- `~/.claude/claudemux/usage.json` exists and has one `models` entry.
- No `.lock` file is left behind after a refresh.
- Narrowing the pane drops eta, then fab, then wk — never 5h.

- [ ] **Step 9: Commit**

```bash
gofmt -w cmd/claudemux-head/tui.go cmd/claudemux-head/switchboardtui.go cmd/claudemux-head/tui_test.go cmd/claudemux-head/swmeters_test.go
git add cmd/claudemux-head/tui.go cmd/claudemux-head/switchboardtui.go cmd/claudemux-head/tui_test.go cmd/claudemux-head/swmeters_test.go
git commit -m "feat(meters): render per-model weekly gauges in head and lobby"
```

---

## Rollout note

`go install ./cmd/claudemux-head` overwrites the binary every running head pane uses. Installing from this worktree while `main` is ahead silently reverts those commits across every pane. **Merge to `main` first, then install** — this is why Task 6 Step 8 merges before it installs.
