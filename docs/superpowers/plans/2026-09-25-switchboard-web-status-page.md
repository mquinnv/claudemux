# Switchboard Web Status Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The switchboard lobby serves a read-only, live-updating web page of the fleet, with an LLM-written headline, on the node's Tailscale address.

**Architecture:** The lobby process (`claudemux-head switchboard`) opens one HTTP listener when `web.listen` is configured. Every poll copies the fleet snapshot into a mutex-guarded holder that the HTTP handlers read; a background worker turns that holder's sessions into a one-sentence headline via the existing Haiku summarizer, gated on a content fingerprint and an interval. One embedded HTML page polls a JSON route every three seconds.

**Tech Stack:** Go 1.26 (`net/http` with method-pattern `ServeMux`, `embed`), bubbletea lobby (`switchboardtui.go`), `anthropic-sdk-go` v1.57 via the existing `Summarizer`, yaml.v3 config. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-09-25-switchboard-web-status-page-design.md`

## Global Constraints

- Everything lives in package `main` under `cmd/claudemux-head/`; tests are `*_test.go` beside the code, table-driven, using only the standard library and the package's existing helpers (`fakeDoer`, `toolUseResponseRaw`, `writeConfig`, `swTestModel`).
- Run tests with `cd cmd/claudemux-head && go test ./...`. Run `go vet ./...` before every commit.
- The HTTP surface is `GET /` and `GET /api/fleet` only; POST is 405, any other path 404; both routes send `Cache-Control: no-store`.
- Nothing the web code does may write to stdout or stderr: the lobby is a TUI. Log only through `teardownLogf`, and give `http.Server` an `ErrorLog` that discards.
- The headline prompt never includes `swSession.Prompt`.
- `web.listen` default is `""` (off). `web.headline_interval` default is `2m`; negative is rejected at load; zero is a legal "no floor".
- The `tailscale` keyword host resolves via `tailscale ip -4`, first non-empty line, 3-second timeout.
- Bind or resolve failure is not fatal: the lobby runs and shows the reason in its title row.
- Commit messages end with the two attribution lines already used on this branch (see `git log -1`).

## Review Focus

1. A topic, summary or prompt containing `<script>` or `&` must render as literal text on the page. Pinned in Task 4: the page source contains no `innerHTML`, and the JSON test passes such text through unchanged.
2. A port already in use must leave the lobby running with the reason visible, not crash it. Pinned in Task 4 (second bind on the same port fails) and Task 7 (`webErr` shows in the title).
3. `tailscale ip -4` printing nothing (tailscale down) or the binary missing must produce an error that names tailscale. Pinned in Task 2.
4. An empty fleet must never spend a Haiku call. Pinned in Task 6.
5. A headline call that fails must keep the previous headline and mark it stale once the fleet changes. Pinned in Tasks 3 and 6.

---

### Task 1: `web` config section and `parseWebListen`

**Files:**
- Modify: `cmd/claudemux-head/config.go` (Config struct at line 46, `defaultConfig` at ~220, `validate` at ~383)
- Create: `cmd/claudemux-head/weblisten.go`
- Test: `cmd/claudemux-head/weblisten_test.go`, `cmd/claudemux-head/config_test.go`

**Interfaces:**
- Consumes: `Duration`, `Config`, `writeConfig(t, contents)` test helper.
- Produces:
  - `type WebConfig struct { Listen string; HeadlineInterval Duration }` as `Config.Web`.
  - `type webListen struct { Host, Port string; Tailscale bool }`
  - `func parseWebListen(s string) (l webListen, on bool, err error)` — `on` is false for `""`.
  - `const webTailscaleHost = "tailscale"`

- [ ] **Step 1: Write the failing parse tests**

Create `cmd/claudemux-head/weblisten_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestParseWebListen(t *testing.T) {
	cases := []struct {
		in        string
		on        bool
		host      string
		port      string
		tailscale bool
		errPart   string
	}{
		{in: "", on: false},
		{in: "tailscale:7474", on: true, host: "tailscale", port: "7474", tailscale: true},
		{in: "127.0.0.1:7474", on: true, host: "127.0.0.1", port: "7474"},
		{in: ":7474", on: true, host: "", port: "7474"},
		{in: "tailscale", errPart: "host:port"},
		{in: "tailscale:", errPart: "port"},
		{in: "tailscale:http", errPart: "port"},
		{in: "tailscale:70000", errPart: "port"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			l, on, err := parseWebListen(c.in)
			if c.errPart != "" {
				if err == nil || !strings.Contains(err.Error(), c.errPart) {
					t.Fatalf("parseWebListen(%q) err = %v, want one mentioning %q", c.in, err, c.errPart)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseWebListen(%q) err = %v", c.in, err)
			}
			if on != c.on {
				t.Fatalf("on = %v, want %v", on, c.on)
			}
			if l.Host != c.host || l.Port != c.port || l.Tailscale != c.tailscale {
				t.Errorf("got %+v, want host=%q port=%q tailscale=%v", l, c.host, c.port, c.tailscale)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd cmd/claudemux-head && go test -run TestParseWebListen ./...`
Expected: FAIL to compile, `undefined: parseWebListen`.

- [ ] **Step 3: Write `weblisten.go`**

```go
package main

import (
	"fmt"
	"net"
	"strconv"
)

// webTailscaleHost is the keyword host in web.listen that means "this node's
// Tailscale IPv4", resolved at lobby start (see resolveWebListen).
const webTailscaleHost = "tailscale"

// webListen is a parsed web.listen value.
type webListen struct {
	Host      string // as written; "" means every interface
	Port      string
	Tailscale bool // Host was the tailscale keyword
}

// parseWebListen validates web.listen. "" is the off switch (on=false, no
// error). Anything else must be host:port with a numeric port: a value tmux
// or net.Listen would reject is refused here, where the message can name the
// key, rather than at bind time in a running lobby.
func parseWebListen(s string) (webListen, bool, error) {
	if s == "" {
		return webListen{}, false, nil
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return webListen{}, false, fmt.Errorf("web.listen is %q: must be host:port, like \"tailscale:7474\" or \"127.0.0.1:7474\"", s)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return webListen{}, false, fmt.Errorf("web.listen is %q: port must be a number from 1 to 65535", s)
	}
	return webListen{Host: host, Port: port, Tailscale: host == webTailscaleHost}, true, nil
}
```

- [ ] **Step 4: Run the parse tests**

Run: `cd cmd/claudemux-head && go test -run TestParseWebListen ./...`
Expected: PASS.

- [ ] **Step 5: Write the failing config tests**

Append to `cmd/claudemux-head/config_test.go`:

```go
func TestLoadConfigWebDefaultsOff(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Web.Listen != "" {
		t.Errorf("Web.Listen = %q, want \"\" (off) by default", cfg.Web.Listen)
	}
	if cfg.Web.HeadlineInterval.Duration != 2*time.Minute {
		t.Errorf("Web.HeadlineInterval = %v, want 2m", cfg.Web.HeadlineInterval.Duration)
	}
}

func TestLoadConfigWebSectionParses(t *testing.T) {
	writeConfig(t, "web:\n  listen: tailscale:7474\n  headline_interval: 30s\n")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Web.Listen != "tailscale:7474" {
		t.Errorf("Web.Listen = %q", cfg.Web.Listen)
	}
	if cfg.Web.HeadlineInterval.Duration != 30*time.Second {
		t.Errorf("Web.HeadlineInterval = %v, want 30s", cfg.Web.HeadlineInterval.Duration)
	}
}

func TestLoadConfigWebBadListenIsFatal(t *testing.T) {
	writeConfig(t, "web:\n  listen: tailscale:http\n")
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "web.listen") {
		t.Fatalf("loadConfig() err = %v, want one naming web.listen", err)
	}
}

func TestLoadConfigWebNegativeHeadlineIntervalIsFatal(t *testing.T) {
	writeConfig(t, "web:\n  headline_interval: -1m\n")
	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "web.headline_interval") {
		t.Fatalf("loadConfig() err = %v, want one naming web.headline_interval", err)
	}
}

func TestLoadConfigWebZeroHeadlineIntervalIsAllowed(t *testing.T) {
	writeConfig(t, "web:\n  headline_interval: 0s\n")
	if _, err := loadConfig(); err != nil {
		t.Fatalf("loadConfig() err = %v, want nil — zero is the documented opt-out", err)
	}
}
```

- [ ] **Step 6: Run them to verify they fail**

Run: `cd cmd/claudemux-head && go test -run 'TestLoadConfigWeb' ./...`
Expected: FAIL to compile, `cfg.Web undefined`.

- [ ] **Step 7: Add `WebConfig` to `config.go`**

Add the field to `Config`:

```go
type Config struct {
	Summary     SummaryConfig     `yaml:"summary"`
	Head        HeadConfig        `yaml:"head"`
	OnePassword OnePasswordConfig `yaml:"onepassword"`
	Launch      LaunchConfig      `yaml:"launch"`
	Teardown    TeardownConfig    `yaml:"teardown"`
	Web         WebConfig         `yaml:"web"`
}
```

Add the type after `TeardownConfig`:

```go
// WebConfig is the switchboard's web status page (webserver.go). Listen is
// the address the lobby binds: "" (the default) serves nothing; "host:port"
// is passed to net.Listen; the keyword host "tailscale" is replaced by this
// node's Tailscale IPv4 at lobby start, which is the only way the page is
// meant to be reached. HeadlineInterval is the floor between fleet-headline
// calls, with summary.min_interval's rules: each call bills the user's key,
// negative is rejected, zero means no floor.
type WebConfig struct {
	Listen           string   `yaml:"listen"`
	HeadlineInterval Duration `yaml:"headline_interval"`
}
```

In `defaultConfig`, add:

```go
		Web: WebConfig{
			HeadlineInterval: Duration{2 * time.Minute},
		},
```

In `validate`, before the final `return nil`:

```go
	if c.Web.HeadlineInterval.Duration < 0 {
		return fmt.Errorf("web.headline_interval is %s: a negative floor removes the rate limit on billable API calls instead of setting one; use 0 to disable it deliberately",
			c.Web.HeadlineInterval.Duration)
	}
	if _, _, err := parseWebListen(c.Web.Listen); err != nil {
		return err
	}
```

- [ ] **Step 8: Run the config tests and the full suite**

Run: `cd cmd/claudemux-head && go test ./... && go vet ./...`
Expected: PASS. (`config get` round-trips Config through YAML; `Duration.MarshalYAML` already exists, so `web.headline_interval` prints as `2m0s`.)

- [ ] **Step 9: Commit**

```bash
git add cmd/claudemux-head/config.go cmd/claudemux-head/config_test.go cmd/claudemux-head/weblisten.go cmd/claudemux-head/weblisten_test.go
git commit -m "config: add web.listen and web.headline_interval"
```

---

### Task 2: Resolve the `tailscale` keyword

**Files:**
- Modify: `cmd/claudemux-head/weblisten.go`
- Test: `cmd/claudemux-head/weblisten_test.go`

**Interfaces:**
- Consumes: `webListen`, `webTailscaleHost`.
- Produces:
  - `func resolveWebListen(l webListen, tailscaleIP func() (string, error)) (string, error)` — a `net.Listen` address.
  - `func parseTailscaleIP(out string) (string, error)` — first non-empty line, must parse as an IP.
  - `func tailscaleIPv4() (string, error)` — runs `tailscale ip -4`; the production `tailscaleIP` argument.

- [ ] **Step 1: Write the failing tests**

Append to `weblisten_test.go`:

```go
func TestResolveWebListenPassesPlainAddressThrough(t *testing.T) {
	called := false
	stub := func() (string, error) { called = true; return "100.64.0.15", nil }
	addr, err := resolveWebListen(webListen{Host: "127.0.0.1", Port: "7474"}, stub)
	if err != nil || addr != "127.0.0.1:7474" {
		t.Fatalf("got %q, %v; want 127.0.0.1:7474", addr, err)
	}
	if called {
		t.Error("tailscale resolver called for a plain address")
	}
	addr, err = resolveWebListen(webListen{Host: "", Port: "7474"}, stub)
	if err != nil || addr != ":7474" {
		t.Fatalf("got %q, %v; want :7474", addr, err)
	}
}

func TestResolveWebListenTailscale(t *testing.T) {
	ok := func() (string, error) { return "100.64.0.15", nil }
	addr, err := resolveWebListen(webListen{Host: "tailscale", Port: "7474", Tailscale: true}, ok)
	if err != nil || addr != "100.64.0.15:7474" {
		t.Fatalf("got %q, %v; want 100.64.0.15:7474", addr, err)
	}
	bad := func() (string, error) { return "", errors.New("exec: tailscale: not found") }
	_, err = resolveWebListen(webListen{Host: "tailscale", Port: "7474", Tailscale: true}, bad)
	if err == nil || !strings.Contains(err.Error(), "tailscale") {
		t.Fatalf("err = %v, want one naming tailscale", err)
	}
}

func TestParseTailscaleIP(t *testing.T) {
	cases := []struct {
		out     string
		want    string
		errPart string
	}{
		{out: "100.64.0.15\n", want: "100.64.0.15"},
		{out: "\n  100.64.0.15  \nfd7a::1\n", want: "100.64.0.15"},
		{out: "", errPart: "printed nothing"},
		{out: "\n\n", errPart: "printed nothing"},
		{out: "Tailscale is stopped.\n", errPart: "not an address"},
	}
	for _, c := range cases {
		got, err := parseTailscaleIP(c.out)
		if c.errPart != "" {
			if err == nil || !strings.Contains(err.Error(), c.errPart) {
				t.Errorf("parseTailscaleIP(%q) err = %v, want %q", c.out, err, c.errPart)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("parseTailscaleIP(%q) = %q, %v; want %q", c.out, got, err, c.want)
		}
	}
}
```

Add `"errors"` to the test file's imports.

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/claudemux-head && go test -run 'TestResolveWebListen|TestParseTailscaleIP' ./...`
Expected: FAIL to compile, `undefined: resolveWebListen`.

- [ ] **Step 3: Implement**

Append to `weblisten.go` (add `"context"`, `"errors"`, `"os/exec"`, `"strings"`, `"time"` to its imports):

```go
// resolveWebListen turns a parsed web.listen into the address net.Listen
// binds. Only the tailscale keyword needs resolving; tailscaleIP is a
// parameter so tests never run the binary.
func resolveWebListen(l webListen, tailscaleIP func() (string, error)) (string, error) {
	if !l.Tailscale {
		return net.JoinHostPort(l.Host, l.Port), nil
	}
	ip, err := tailscaleIP()
	if err != nil {
		return "", fmt.Errorf("web.listen %s:%s: %w", webTailscaleHost, l.Port, err)
	}
	return net.JoinHostPort(ip, l.Port), nil
}

// tailscaleIPv4 asks the tailscale CLI for this node's IPv4. Bounded by a
// timeout because it runs at lobby start, before the TUI is up: a hung CLI
// must not hang the launch.
func tailscaleIPv4() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tailscale", "ip", "-4").Output()
	if err != nil {
		return "", fmt.Errorf("tailscale ip -4: %w", err)
	}
	return parseTailscaleIP(string(out))
}

// parseTailscaleIP takes the first non-blank line of `tailscale ip -4` and
// insists it is an address: a stopped tailscale prints a sentence there.
func parseTailscaleIP(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		ip := strings.TrimSpace(line)
		if ip == "" {
			continue
		}
		if net.ParseIP(ip) == nil {
			return "", fmt.Errorf("tailscale ip -4 printed %q, not an address: is tailscale up?", ip)
		}
		return ip, nil
	}
	return "", errors.New("tailscale ip -4 printed nothing: is tailscale up?")
}
```

- [ ] **Step 4: Run tests**

Run: `cd cmd/claudemux-head && go test -run 'TestResolveWebListen|TestParseTailscaleIP|TestParseWebListen' ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/weblisten.go cmd/claudemux-head/weblisten_test.go
git commit -m "web: resolve the tailscale listen keyword"
```

---

### Task 3: The fleet holder and its JSON view

**Files:**
- Create: `cmd/claudemux-head/webfleet.go`
- Test: `cmd/claudemux-head/webfleet_test.go`

**Interfaces:**
- Consumes: `swSnapshot`, `swSession`, `isWaiting`, `RateLimits`, `Window`, `ModelWindow`, `isHex6`, `sanitizeDeferReason`.
- Produces:
  - `type webHeadline struct { Text string; At time.Time; Fingerprint string }`
  - `type webFleet struct{ … }`, `func newWebFleet() *webFleet`
  - `func (w *webFleet) publish(snap swSnapshot, rl RateLimits, rlOK bool, windows []ModelWindow, taken time.Time)`
  - `func (w *webFleet) setHeadline(h webHeadline)`
  - `func (w *webFleet) sessions() []swSession` — a copy.
  - `func (w *webFleet) view() webFleetView`
  - `func buildWebFleetView(snap swSnapshot, rl RateLimits, rlOK bool, windows []ModelWindow, taken time.Time, h webHeadline) webFleetView`
  - `func webFingerprint(sessions []swSession) string`
  - `func webStateWord(raw string) string`
  - JSON types `webFleetView`, `webHeadlineView`, `webCounts`, `webBudget`, `webWindow`, `webModelWindow`, `webSession` (fields below).

- [ ] **Step 1: Write the failing tests**

Create `cmd/claudemux-head/webfleet_test.go`:

```go
package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func webTestSnapshot() swSnapshot {
	return swSnapshot{Sessions: []swSession{
		{Name: "api", State: "Idle", Since: time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC),
			Context: 37, Topic: "build fixes", Summary: "fixing the build", Prompt: "run <b>the</b> tests & go",
			Model: "claude-opus-4-7", Color: "8b5cf6", Emoji: "🔀"},
		{Name: "web", State: "Thinking", Since: time.Date(2026, 9, 25, 14, 1, 0, 0, time.UTC), Context: -1},
		{Name: "blocked", State: "Idle", Context: 12, Topic: "phenix deploy", Deferred: true, DeferReason: "waiting on\treview"},
	}}
}

func TestBuildWebFleetViewMapsSessions(t *testing.T) {
	snap := webTestSnapshot()
	taken := time.Date(2026, 9, 25, 14, 3, 0, 0, time.UTC)
	v := buildWebFleetView(snap, RateLimits{}, false, nil, taken, webHeadline{})

	if v.TakenAt != "2026-09-25T14:03:00Z" {
		t.Errorf("TakenAt = %q", v.TakenAt)
	}
	if v.Counts != (webCounts{Sessions: 3, Waiting: 2, Deferred: 1}) {
		t.Errorf("Counts = %+v", v.Counts)
	}
	if v.Budget != nil {
		t.Error("Budget must be nil when the rate-limit cache is unreadable")
	}
	if v.Headline != nil {
		t.Error("Headline must be nil before the first headline")
	}
	if len(v.Sessions) != 3 || v.Sessions[0].Name != "api" || v.Sessions[2].Name != "blocked" {
		t.Fatalf("Sessions order lost: %+v", v.Sessions)
	}
	api := v.Sessions[0]
	if api.Color != "#8b5cf6" || api.Emoji != "🔀" || api.State != "Idle" || api.StateRaw != "Idle" || !api.Waiting {
		t.Errorf("api = %+v", api)
	}
	if api.ContextPct == nil || *api.ContextPct != 37 {
		t.Errorf("api.ContextPct = %v, want 37", api.ContextPct)
	}
	if api.Since != "2026-09-25T14:00:00Z" {
		t.Errorf("api.Since = %q", api.Since)
	}
	if api.Prompt != "run <b>the</b> tests & go" {
		t.Errorf("Prompt must pass through unescaped for JSON: %q", api.Prompt)
	}
	web := v.Sessions[1]
	if web.ContextPct != nil {
		t.Error("ContextPct must be nil for an unpublished context (-1)")
	}
	if web.Waiting {
		t.Error("Thinking is not waiting")
	}
	blocked := v.Sessions[2]
	if !blocked.Deferred || blocked.DeferReason != "waiting on review" {
		t.Errorf("blocked = %+v, want deferred with a sanitized reason", blocked)
	}
}

func TestBuildWebFleetViewUnknownStateAndBadColor(t *testing.T) {
	snap := swSnapshot{Sessions: []swSession{{Name: "new", Context: -1, Color: "not-hex"}}}
	v := buildWebFleetView(snap, RateLimits{}, false, nil, time.Now(), webHeadline{})
	s := v.Sessions[0]
	if s.State != "unknown" || s.StateRaw != "" {
		t.Errorf("state = %q/%q, want unknown/\"\"", s.State, s.StateRaw)
	}
	if s.Color != "" {
		t.Errorf("Color = %q, want \"\" for a non-hex value", s.Color)
	}
	if s.Since != "" {
		t.Errorf("Since = %q, want \"\" for a zero time", s.Since)
	}
}

func TestBuildWebFleetViewBudgetAndHeadline(t *testing.T) {
	rl := RateLimits{
		FiveHour: Window{UsedPercent: 41, ResetsAt: time.Date(2026, 9, 25, 17, 0, 0, 0, time.UTC)},
		SevenDay: Window{UsedPercent: 63, ResetsAt: time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)},
	}
	windows := []ModelWindow{{Name: "opus", UsedPercent: 20, ResetsAt: time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)}}
	snap := webTestSnapshot()
	fp := webFingerprint(snap.Sessions)
	h := webHeadline{Text: "Fixing the build while a deploy waits on review.", At: time.Date(2026, 9, 25, 14, 2, 0, 0, time.UTC), Fingerprint: fp}
	v := buildWebFleetView(snap, rl, true, windows, time.Now(), h)

	if v.Budget == nil || v.Budget.FiveHour.UsedPct != 41 || v.Budget.Weekly.UsedPct != 63 {
		t.Fatalf("Budget = %+v", v.Budget)
	}
	if v.Budget.FiveHour.ResetsAt != "2026-09-25T17:00:00Z" {
		t.Errorf("FiveHour.ResetsAt = %q", v.Budget.FiveHour.ResetsAt)
	}
	if len(v.Budget.Models) != 1 || v.Budget.Models[0].Name != "opus" || v.Budget.Models[0].UsedPct != 20 {
		t.Errorf("Models = %+v", v.Budget.Models)
	}
	if v.Headline == nil || v.Headline.Text != h.Text || v.Headline.At != "2026-09-25T14:02:00Z" || v.Headline.Stale {
		t.Fatalf("Headline = %+v, want fresh", v.Headline)
	}

	snap.Sessions[0].Summary = "tests green, opening the PR"
	v = buildWebFleetView(snap, rl, true, windows, time.Now(), h)
	if v.Headline == nil || !v.Headline.Stale {
		t.Error("Headline.Stale must be true once the fleet's fingerprint moves past the headline's")
	}
}

func TestWebFleetViewJSONOmitsAbsentFacts(t *testing.T) {
	v := buildWebFleetView(webTestSnapshot(), RateLimits{}, false, nil, time.Now(), webHeadline{})
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, absent := range []string{`"budget"`, `"headline"`} {
		if strings.Contains(s, absent) {
			t.Errorf("JSON must omit %s when there is none: %s", absent, s)
		}
	}
	if strings.Count(s, `"context_pct"`) != 2 {
		t.Errorf("context_pct must appear for the two sessions that published one, got %d in %s", strings.Count(s, `"context_pct"`), s)
	}
	for _, present := range []string{`"taken_at"`, `"counts"`, `"sessions"`, `"state_raw":"Thinking"`, `"waiting":true`, `"defer_reason":"waiting on review"`} {
		if !strings.Contains(s, present) {
			t.Errorf("JSON missing %s: %s", present, s)
		}
	}
}

func TestWebFingerprint(t *testing.T) {
	base := webTestSnapshot().Sessions
	fp := webFingerprint(base)
	if fp == "" || fp != webFingerprint(base) {
		t.Fatal("fingerprint must be stable for identical input")
	}
	moved := webTestSnapshot().Sessions
	moved[0].Since = moved[0].Since.Add(time.Minute)
	moved[0].Context = 80
	moved[0].Prompt = "something else entirely"
	if webFingerprint(moved) != fp {
		t.Error("timers, context and prompt must not move the fingerprint")
	}
	changed := webTestSnapshot().Sessions
	changed[0].Summary = "opening the PR"
	if webFingerprint(changed) == fp {
		t.Error("a summary change must move the fingerprint")
	}
	state := webTestSnapshot().Sessions
	state[1].State = "Idle"
	if webFingerprint(state) == fp {
		t.Error("a state change must move the fingerprint")
	}
	undeferred := webTestSnapshot().Sessions
	undeferred[2].Deferred = false
	if webFingerprint(undeferred) == fp {
		t.Error("clearing a defer must move the fingerprint")
	}
	if webFingerprint(nil) == fp {
		t.Error("an empty fleet must not share a fingerprint with a populated one")
	}
}

func TestWebFleetPublishAndView(t *testing.T) {
	f := newWebFleet()
	if v := f.view(); len(v.Sessions) != 0 || v.Sessions == nil {
		t.Fatalf("empty holder must view as an empty (not null) session list: %+v", v.Sessions)
	}
	snap := webTestSnapshot()
	f.publish(snap, RateLimits{}, false, nil, time.Now())
	if got := f.sessions(); len(got) != 3 || got[0].Name != "api" {
		t.Fatalf("sessions() = %+v", got)
	}
	f.setHeadline(webHeadline{Text: "x", At: time.Now(), Fingerprint: webFingerprint(snap.Sessions)})
	v := f.view()
	if v.Headline == nil || v.Headline.Text != "x" || len(v.Sessions) != 3 {
		t.Fatalf("view() = %+v", v)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/claudemux-head && go test -run 'TestBuildWebFleetView|TestWebFleet|TestWebFingerprint' ./...`
Expected: FAIL to compile, `undefined: buildWebFleetView`.

- [ ] **Step 3: Write `webfleet.go`**

```go
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// The web status page's data. The lobby's Update publishes every snapshot
// here; the HTTP handlers (webserver.go) and the headline worker
// (webheadline.go) read it. Nothing in this file touches tmux or bubbletea:
// the holder is the one seam between the TUI's goroutine and the server's.
// Design: docs/superpowers/specs/2026-09-25-switchboard-web-status-page-design.md.

// webHeadline is the fleet's LLM-written one-liner. Fingerprint is
// webFingerprint of the sessions it was written from, so a view can tell a
// current headline from one the fleet has moved past.
type webHeadline struct {
	Text        string
	At          time.Time
	Fingerprint string
}

type webFleet struct {
	mu       sync.RWMutex
	snap     swSnapshot
	rl       RateLimits
	rlOK     bool
	windows  []ModelWindow
	taken    time.Time
	headline webHeadline
}

func newWebFleet() *webFleet { return &webFleet{} }

// publish replaces the fleet facts. Only the lobby's Update calls it.
func (w *webFleet) publish(snap swSnapshot, rl RateLimits, rlOK bool, windows []ModelWindow, taken time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.snap = snap
	w.rl = rl
	w.rlOK = rlOK
	w.windows = append([]ModelWindow(nil), windows...)
	w.taken = taken
}

// setHeadline replaces the headline. Only the headline worker calls it.
func (w *webFleet) setHeadline(h webHeadline) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.headline = h
}

// sessions is a copy of the current fleet, for the headline worker.
func (w *webFleet) sessions() []swSession {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return append([]swSession(nil), w.snap.Sessions...)
}

// view is the JSON shape of the current facts, built under the read lock so
// a request never sees a half-published snapshot.
func (w *webFleet) view() webFleetView {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return buildWebFleetView(w.snap, w.rl, w.rlOK, w.windows, w.taken, w.headline)
}

// The wire shape of GET /api/fleet. Times are RFC 3339 strings, "" (and
// omitted) when unknown, so the page never has to special-case a zero time.
type webFleetView struct {
	TakenAt  string           `json:"taken_at"`
	Headline *webHeadlineView `json:"headline,omitempty"`
	Counts   webCounts        `json:"counts"`
	Budget   *webBudget       `json:"budget,omitempty"`
	Sessions []webSession     `json:"sessions"`
}

type webHeadlineView struct {
	Text  string `json:"text"`
	At    string `json:"at"`
	Stale bool   `json:"stale"`
}

type webCounts struct {
	Sessions int `json:"sessions"`
	Waiting  int `json:"waiting"`
	Deferred int `json:"deferred"`
}

type webWindow struct {
	UsedPct  int    `json:"used_pct"`
	ResetsAt string `json:"resets_at,omitempty"`
}

type webModelWindow struct {
	Name     string `json:"name"`
	UsedPct  int    `json:"used_pct"`
	ResetsAt string `json:"resets_at,omitempty"`
}

type webBudget struct {
	FiveHour webWindow        `json:"five_hour"`
	Weekly   webWindow        `json:"weekly"`
	Models   []webModelWindow `json:"models,omitempty"`
}

type webSession struct {
	Name        string `json:"name"`
	Color       string `json:"color,omitempty"`
	Emoji       string `json:"emoji,omitempty"`
	State       string `json:"state"`
	StateRaw    string `json:"state_raw"`
	Waiting     bool   `json:"waiting"`
	Since       string `json:"since,omitempty"`
	ContextPct  *int   `json:"context_pct,omitempty"`
	Model       string `json:"model,omitempty"`
	Topic       string `json:"topic,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	Deferred    bool   `json:"deferred"`
	DeferReason string `json:"defer_reason,omitempty"`
}

func webTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// webStateWord is the state as the page prints it: the published value, or
// "unknown" for a head that has not published yet — the same word the lobby
// row uses (swStateText), minus its emoji cell.
func webStateWord(raw string) string {
	if raw == "" {
		return "unknown"
	}
	return raw
}

func buildWebFleetView(snap swSnapshot, rl RateLimits, rlOK bool, windows []ModelWindow, taken time.Time, h webHeadline) webFleetView {
	v := webFleetView{TakenAt: webTime(taken), Sessions: []webSession{}}
	for _, s := range snap.Sessions {
		ws := webSession{
			Name:     s.Name,
			Emoji:    s.Emoji,
			State:    webStateWord(s.State),
			StateRaw: s.State,
			Waiting:  isWaiting(s.State),
			Since:    webTime(s.Since),
			Model:    s.Model,
			Topic:    s.Topic,
			Summary:  s.Summary,
			Prompt:   s.Prompt,
			Deferred: s.Deferred,
		}
		if isHex6(s.Color) {
			ws.Color = "#" + s.Color
		}
		if s.Context >= 0 {
			pct := s.Context
			ws.ContextPct = &pct
		}
		if s.Deferred {
			ws.DeferReason = sanitizeDeferReason(s.DeferReason)
		}
		v.Sessions = append(v.Sessions, ws)
		v.Counts.Sessions++
		if ws.Waiting {
			v.Counts.Waiting++
		}
		if s.Deferred {
			v.Counts.Deferred++
		}
	}
	if rlOK {
		b := &webBudget{
			FiveHour: webWindow{UsedPct: rl.FiveHour.UsedPercent, ResetsAt: webTime(rl.FiveHour.ResetsAt)},
			Weekly:   webWindow{UsedPct: rl.SevenDay.UsedPercent, ResetsAt: webTime(rl.SevenDay.ResetsAt)},
		}
		for _, mw := range windows {
			b.Models = append(b.Models, webModelWindow{Name: mw.Name, UsedPct: mw.UsedPercent, ResetsAt: webTime(mw.ResetsAt)})
		}
		v.Budget = b
	}
	if h.Text != "" {
		v.Headline = &webHeadlineView{
			Text:  h.Text,
			At:    webTime(h.At),
			Stale: h.Fingerprint != webFingerprint(snap.Sessions),
		}
	}
	return v
}

// webFingerprint hashes what the headline is written from — the facts that
// describe what the fleet is DOING. Timers, context percentages and the raw
// prompt are left out on purpose: they move constantly, and each move must
// not look like a reason for another billable call.
func webFingerprint(sessions []swSession) string {
	var b strings.Builder
	for _, s := range sessions {
		reason := ""
		if s.Deferred {
			reason = "deferred:" + sanitizeDeferReason(s.DeferReason)
		}
		b.WriteString(strings.Join([]string{s.Name, webStateWord(s.State), s.Topic, s.Summary, reason}, "\x1f"))
		b.WriteString("\x1e")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}
```

- [ ] **Step 4: Run tests**

Run: `cd cmd/claudemux-head && go test -run 'TestBuildWebFleetView|TestWebFleet|TestWebFingerprint' ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/webfleet.go cmd/claudemux-head/webfleet_test.go
git commit -m "web: fleet holder and JSON view"
```

---

### Task 4: HTTP handlers, the embedded page, and the server

**Files:**
- Create: `cmd/claudemux-head/webserver.go`, `cmd/claudemux-head/webpage.html`
- Test: `cmd/claudemux-head/webserver_test.go`

**Interfaces:**
- Consumes: `webFleet.view()`, `teardownLogf`.
- Produces:
  - `var webPageHTML []byte` (embedded)
  - `func webHandler(f *webFleet) http.Handler`
  - `type webServer struct{ … }`, `func startWebServer(addr string, h http.Handler) (*webServer, error)`, `func (s *webServer) addr() string`, `func (s *webServer) stop()` (nil-safe, idempotent).

- [ ] **Step 1: Write the failing tests**

Create `cmd/claudemux-head/webserver_test.go`:

```go
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func webTestHandler(t *testing.T) http.Handler {
	t.Helper()
	f := newWebFleet()
	f.publish(webTestSnapshot(), RateLimits{}, false, nil, time.Now())
	return webHandler(f)
}

func TestWebHandlerServesFleetJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	webTestHandler(t).ServeHTTP(rec, httptest.NewRequest("GET", "/api/fleet", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var v webFleetView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("body is not webFleetView JSON: %v\n%s", err, rec.Body)
	}
	if len(v.Sessions) != 3 || v.Sessions[0].Prompt != "run <b>the</b> tests & go" {
		t.Errorf("sessions = %+v", v.Sessions)
	}
}

func TestWebHandlerServesPage(t *testing.T) {
	rec := httptest.NewRecorder()
	webTestHandler(t).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	body := rec.Body.String()
	for _, want := range []string{"<title>", "/api/fleet", "read-only", "tailnet"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// Every fleet string reaches the DOM through textContent. innerHTML with
	// a prompt in it would let a session's own text run as script on a
	// teammate's browser.
	if strings.Contains(body, "innerHTML") {
		t.Error("page must not use innerHTML")
	}
	if strings.Contains(body, "http://") || strings.Contains(body, "https://") {
		t.Error("page must not reference any external origin")
	}
}

func TestWebHandlerRejectsOtherMethodsAndPaths(t *testing.T) {
	h := webTestHandler(t)
	cases := []struct {
		method, path string
		want         int
	}{
		{"POST", "/", 405},
		{"POST", "/api/fleet", 405},
		{"GET", "/other", 404},
		{"GET", "/api/", 404},
		{"GET", "/index.html", 404},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}
}

func TestWebRecoverTurnsPanicInto500(t *testing.T) {
	h := webRecover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestStartWebServerBindsAndStops(t *testing.T) {
	s, err := startWebServer("127.0.0.1:0", webTestHandler(t))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + s.addr() + "/api/fleet")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"sessions"`) {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}

	// The port is taken while the server is up: the second lobby's bind
	// fails with a reason, which Task 7 shows in the title row.
	if _, err := startWebServer(s.addr(), webTestHandler(t)); err == nil {
		t.Error("second bind on a live port must fail")
	}

	s.stop()
	s.stop() // idempotent
	var nilServer *webServer
	nilServer.stop() // nil-safe
	if _, err := http.Get("http://" + s.addr() + "/api/fleet"); err == nil {
		t.Error("GET after stop must fail")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/claudemux-head && go test -run 'TestWebHandler|TestWebRecover|TestStartWebServer' ./...`
Expected: FAIL to compile, `undefined: webHandler`.

- [ ] **Step 3: Write `webpage.html`**

Create `cmd/claudemux-head/webpage.html`:

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>claudemux fleet</title>
<style>
  :root {
    --bg: #fafafa; --fg: #1a1a1a; --dim: #6b6b6b; --card: #ffffff; --line: #e3e3e3;
    --wait: #d97706; --busy: #2563eb; --defer: #7c3aed; --bar: #e5e7eb; --fill: #2563eb;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #111214; --fg: #e8e8e8; --dim: #9a9a9a; --card: #1b1c1f; --line: #2a2b2f;
      --wait: #f59e0b; --busy: #60a5fa; --defer: #a78bfa; --bar: #2a2b2f; --fill: #60a5fa;
    }
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; padding: 16px; background: var(--bg); color: var(--fg);
    font: 15px/1.45 -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
    max-width: 880px; margin-inline: auto;
  }
  body.offline { opacity: .55; }
  header { margin-bottom: 20px; }
  h1 { font-size: 20px; margin: 0 0 4px; }
  #headline { font-size: 18px; margin: 8px 0 2px; }
  #headline-meta, .meta, footer { color: var(--dim); font-size: 13px; }
  #headline-meta.stale { color: var(--wait); }
  .gauges { display: flex; gap: 16px; flex-wrap: wrap; margin-top: 12px; }
  .gauge { flex: 1 1 200px; }
  .gauge .label { font-size: 12px; color: var(--dim); display: flex; justify-content: space-between; }
  .bar { height: 6px; background: var(--bar); border-radius: 3px; overflow: hidden; margin-top: 3px; }
  .bar > div { height: 100%; background: var(--fill); }
  .card {
    background: var(--card); border: 1px solid var(--line); border-radius: 8px;
    padding: 10px 12px; margin-bottom: 10px;
  }
  .card.waiting { border-left: 4px solid var(--wait); }
  .card.deferred { border-left: 4px solid var(--defer); }
  .row { display: flex; flex-wrap: wrap; gap: 6px 12px; align-items: baseline; }
  .name { font-weight: 600; text-shadow: 0 0 1px rgba(0,0,0,.35); }
  .state { color: var(--busy); }
  .waiting .state { color: var(--wait); }
  .badge {
    font-size: 11px; font-weight: 700; letter-spacing: .04em; padding: 1px 6px;
    border-radius: 4px; background: var(--defer); color: #fff;
  }
  .topic { font-weight: 600; margin-top: 4px; }
  .detail { color: var(--dim); margin-top: 2px; overflow-wrap: anywhere; }
  .rule { color: var(--dim); font-size: 12px; text-align: center; margin: 14px 0 8px; }
  #empty { color: var(--dim); }
  footer { margin-top: 24px; text-align: center; }
</style>
</head>
<body>
<header>
  <h1>claudemux fleet</h1>
  <div id="headline">loading…</div>
  <div id="headline-meta"></div>
  <div class="gauges" id="gauges"></div>
</header>
<main id="fleet"></main>
<div id="empty" hidden>no claudemux sessions</div>
<footer>read-only · reachable on the tailnet only · served by claudemux</footer>
<script>
(function () {
  'use strict';
  var POLL_MS = 3000;

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined && text !== null) e.textContent = text;
    return e;
  }

  function ago(iso, now) {
    if (!iso) return '';
    var s = Math.max(0, Math.round((now - Date.parse(iso)) / 1000));
    if (s < 60) return s + 's';
    var m = Math.round(s / 60);
    if (m < 60) return m + 'm';
    var h = Math.floor(m / 60);
    return h + 'h' + (m % 60 ? (m % 60) + 'm' : '');
  }

  function until(iso, now) {
    if (!iso) return '';
    var s = Math.max(0, Math.round((Date.parse(iso) - now) / 1000));
    var h = Math.floor(s / 3600), m = Math.round((s % 3600) / 60);
    if (h >= 24) return Math.floor(h / 24) + 'd' + (h % 24) + 'h';
    return h ? h + 'h' + (m ? m + 'm' : '') : m + 'm';
  }

  function gauge(label, w, now) {
    var g = el('div', 'gauge');
    var lab = el('div', 'label');
    lab.appendChild(el('span', null, label + ' ' + w.used_pct + '%'));
    if (w.resets_at) lab.appendChild(el('span', null, 'resets in ' + until(w.resets_at, now)));
    var bar = el('div', 'bar');
    var fill = el('div');
    fill.style.width = Math.min(100, Math.max(0, w.used_pct)) + '%';
    bar.appendChild(fill);
    g.appendChild(lab);
    g.appendChild(bar);
    return g;
  }

  function card(s, now) {
    var c = el('div', 'card' + (s.waiting ? ' waiting' : '') + (s.deferred ? ' deferred' : ''));
    var row = el('div', 'row');
    var name = el('span', 'name', (s.emoji ? s.emoji + ' ' : '') + s.name);
    if (s.color) name.style.color = s.color;
    row.appendChild(name);
    if (s.deferred) row.appendChild(el('span', 'badge', 'DEFER'));
    var st = s.state + (s.since ? ' · ' + ago(s.since, now) : '');
    row.appendChild(el('span', 'state', st));
    if (typeof s.context_pct === 'number') row.appendChild(el('span', 'meta', 'ctx ' + s.context_pct + '%'));
    if (s.model) row.appendChild(el('span', 'meta', s.model));
    c.appendChild(row);
    if (s.topic) c.appendChild(el('div', 'topic', s.topic));
    if (s.deferred && s.defer_reason) c.appendChild(el('div', 'detail', '◆ ' + s.defer_reason));
    if (s.summary) c.appendChild(el('div', 'detail', s.summary));
    if (s.prompt) c.appendChild(el('div', 'detail', '▌ ' + s.prompt));
    return c;
  }

  function render(v) {
    var now = Date.now();
    var head = document.getElementById('headline');
    var meta = document.getElementById('headline-meta');
    var counts = v.counts.sessions + ' session' + (v.counts.sessions === 1 ? '' : 's') +
      ' · ' + v.counts.waiting + ' waiting' + (v.counts.deferred ? ' · ' + v.counts.deferred + ' deferred' : '');
    if (v.headline && v.headline.text) {
      head.textContent = v.headline.text;
      meta.textContent = counts + ' · headline ' + ago(v.headline.at, now) + ' ago' + (v.headline.stale ? ' · updating' : '');
      meta.className = v.headline.stale ? 'stale' : '';
    } else {
      head.textContent = counts;
      meta.textContent = '';
      meta.className = '';
    }

    var gauges = document.getElementById('gauges');
    gauges.replaceChildren();
    if (v.budget) {
      gauges.appendChild(gauge('5h', v.budget.five_hour, now));
      gauges.appendChild(gauge('week', v.budget.weekly, now));
      (v.budget.models || []).forEach(function (m) { gauges.appendChild(gauge(m.name, m, now)); });
    }

    var fleet = document.getElementById('fleet');
    fleet.replaceChildren();
    var ruled = false;
    v.sessions.forEach(function (s) {
      if (s.deferred && !ruled) { fleet.appendChild(el('div', 'rule', '─ deferred ─')); ruled = true; }
      fleet.appendChild(card(s, now));
    });
    document.getElementById('empty').hidden = v.sessions.length > 0;
  }

  function poll() {
    fetch('/api/fleet', { cache: 'no-store' })
      .then(function (r) { if (!r.ok) throw new Error(r.status); return r.json(); })
      .then(function (v) { document.body.classList.remove('offline'); render(v); })
      .catch(function () {
        document.body.classList.add('offline');
        document.getElementById('headline-meta').textContent = 'lobby not reachable';
      })
      .then(function () { setTimeout(poll, POLL_MS); });
  }
  poll();
})();
</script>
</body>
</html>
```

- [ ] **Step 4: Write `webserver.go`**

```go
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// The switchboard's HTTP surface: one embedded page and one JSON route,
// both GET-only and read-only, served from a webFleet (webfleet.go). The
// lobby is a TUI, so nothing here may write to stdout or stderr — the
// server's own logger is discarded and diagnostics go to teardownLogf.

//go:embed webpage.html
var webPageHTML []byte

const (
	webReadHeaderTimeout = 5 * time.Second
	webWriteTimeout      = 10 * time.Second
	webShutdownBudget    = time.Second
)

// webHandler routes the two paths. Method patterns make the mux answer 405
// for a POST to a known path and 404 for everything else; "/{$}" matches
// the root only, so "/index.html" is not quietly the page.
func webHandler(f *webFleet) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(webPageHTML)
	})
	mux.HandleFunc("GET /api/fleet", func(w http.ResponseWriter, r *http.Request) {
		body, err := json.Marshal(f.view())
		if err != nil {
			teardownLogf("web: encoding fleet: %v", err)
			http.Error(w, "encoding fleet failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	})
	return webRecover(mux)
}

// webRecover keeps a handler panic on its own connection. net/http already
// recovers per request, but it logs the trace to the server's ErrorLog,
// which is discarded here — so the recover is explicit and the log is ours.
func webRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				teardownLogf("web: panic serving %s %s: %v", r.Method, r.URL.Path, p)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type webServer struct {
	srv      *http.Server
	bound    string
	stopOnce sync.Once
}

// startWebServer binds addr and serves h in the background. The bind is
// synchronous so the caller learns about a taken port or a bad address
// right here, before the TUI starts.
func startWebServer(addr string, h http.Handler) (*webServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: webReadHeaderTimeout,
		WriteTimeout:      webWriteTimeout,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() { _ = srv.Serve(ln) }()
	return &webServer{srv: srv, bound: ln.Addr().String()}, nil
}

// addr is the address actually bound (a ":0" port resolved).
func (s *webServer) addr() string {
	if s == nil {
		return ""
	}
	return s.bound
}

// stop closes the listener, giving in-flight responses a short budget.
// Nil-safe and idempotent: the quit path defers it and the restart path
// calls it before exec, and both may run.
func (s *webServer) stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), webShutdownBudget)
		defer cancel()
		if err := s.srv.Shutdown(ctx); err != nil {
			_ = s.srv.Close()
		}
	})
}
```

- [ ] **Step 5: Run tests**

Run: `cd cmd/claudemux-head && go test -run 'TestWebHandler|TestWebRecover|TestStartWebServer' ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/claudemux-head/webserver.go cmd/claudemux-head/webpage.html cmd/claudemux-head/webserver_test.go
git commit -m "web: HTTP handlers, embedded page and server lifecycle"
```

---

### Task 5: The headline call and its gate

**Files:**
- Create: `cmd/claudemux-head/webheadline.go`
- Test: `cmd/claudemux-head/webheadline_test.go`

**Interfaces:**
- Consumes: `Summarizer` (`client`, `model` fields), `placeholderLine`, `fakeDoer`, `toolUseResponseRaw`, `textOnlyResponse`, `testSummarizer` from `summary_test.go`, `webStateWord`, `sanitizeDeferReason`.
- Produces:
  - `const headlineToolName = "headline"`
  - `func buildHeadlinePrompt(sessions []swSession) string`
  - `func (s *Summarizer) Headline(ctx context.Context, sessions []swSession) (string, error)`
  - `type headlineGate struct { interval time.Duration; lastFP string; lastCall time.Time }`
  - `func (g *headlineGate) due(fp string, now time.Time) bool` — records the call when it returns true.

- [ ] **Step 1: Write the failing tests**

Create `cmd/claudemux-head/webheadline_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func headlineResponse(text string) string {
	input, _ := json.Marshal(map[string]string{"text": text})
	return toolUseResponseRaw(headlineToolName, json.RawMessage(input))
}

func TestBuildHeadlinePromptOmitsPromptsKeepsBlockers(t *testing.T) {
	sessions := append(webTestSnapshot().Sessions, swSession{Name: "fresh", Context: -1})
	p := buildHeadlinePrompt(sessions)
	if strings.Contains(p, "run <b>the</b> tests") {
		t.Errorf("the raw prompt must never reach the headline model:\n%s", p)
	}
	for _, want := range []string{"api", "Idle", "build fixes", "fixing the build", "web", "Thinking", "blocked", "waiting on review"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Index(p, "api") > strings.Index(p, "web") || strings.Index(p, "web") > strings.Index(p, "blocked") {
		t.Errorf("sessions must keep lobby order:\n%s", p)
	}
	if !strings.Contains(p, "unknown") {
		t.Errorf("an unpublished state must read as unknown, not blank:\n%s", p)
	}
}

func TestHeadlineParsesToolCall(t *testing.T) {
	d := &fakeDoer{body: headlineResponse("Fixing the build while a deploy waits on review.")}
	got, err := testSummarizer(d).Headline(context.Background(), webTestSnapshot().Sessions)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Fixing the build while a deploy waits on review." {
		t.Errorf("got %q", got)
	}
	tools, _ := d.gotReq["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("request tools = %v, want exactly the headline tool", d.gotReq["tools"])
	}
	if choice, _ := d.gotReq["tool_choice"].(map[string]any); choice["name"] != headlineToolName {
		t.Errorf("tool_choice = %v, want forced %q", d.gotReq["tool_choice"], headlineToolName)
	}
}

func TestHeadlineRejectsPlaceholderAndTextOnly(t *testing.T) {
	for name, body := range map[string]string{"placeholder": headlineResponse("n/a"), "text-only": textOnlyResponse()} {
		d := &fakeDoer{body: body}
		if _, err := testSummarizer(d).Headline(context.Background(), webTestSnapshot().Sessions); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestHeadlineGate(t *testing.T) {
	t0 := time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)
	g := headlineGate{interval: 2 * time.Minute}
	if !g.due("a", t0) {
		t.Fatal("first snapshot must be due without waiting the interval")
	}
	if g.due("a", t0.Add(time.Second)) {
		t.Error("same fingerprint must never be due")
	}
	if g.due("b", t0.Add(time.Minute)) {
		t.Error("a change inside the interval must wait")
	}
	if !g.due("b", t0.Add(2*time.Minute)) {
		t.Error("the same change must fire once the interval has passed")
	}
	if g.due("b", t0.Add(10*time.Minute)) {
		t.Error("after firing, the fingerprint is recorded and must not fire again")
	}
	zero := headlineGate{}
	if !zero.due("a", t0) || !zero.due("b", t0) || zero.due("b", t0) {
		t.Error("a zero interval fires on every change and only on change")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/claudemux-head && go test -run 'TestBuildHeadlinePrompt|TestHeadline' ./...`
Expected: FAIL to compile, `undefined: headlineToolName`.

- [ ] **Step 3: Write `webheadline.go`**

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// The fleet headline: one sentence over every session, for the web page's
// header. It rides on the Summarizer so summary.enabled, the key file and
// the base-URL rule all apply unchanged, and it is gated (headlineGate) so
// a fleet whose only movement is timers never spends a call.

const (
	headlineToolName  = "headline"
	headlineMaxTokens = 120
)

const headlineSystemPrompt = `You write the one-line headline for a status page that shows every live coding session one engineer is running with Claude Code.

You are given the sessions: each has a name, a state (Idle means it is waiting on the engineer; Thinking or Tool:* means Claude is working), what the session is for, what it is doing right now, and whether it is deferred (blocked on something outside the sessions) and on what.

Report one sentence, present tense, under 140 characters, that says what the engineer is working on overall and what, if anything, is waiting on them or blocked. Name the work, not the tool. Do not list every session. Do not start with "The engineer" or "Michael". No preamble, no quotes.`

// buildHeadlinePrompt is the user message: one line per session, lobby
// order, no prompts. The raw prompt is the one field that can carry a
// customer's name or a pasted secret, and the headline does not need it.
func buildHeadlinePrompt(sessions []swSession) string {
	var b strings.Builder
	b.WriteString("Sessions:\n")
	for _, s := range sessions {
		fmt.Fprintf(&b, "- %s — %s", s.Name, webStateWord(s.State))
		if s.Topic != "" {
			fmt.Fprintf(&b, " — for: %s", s.Topic)
		}
		if s.Summary != "" {
			fmt.Fprintf(&b, " — now: %s", s.Summary)
		}
		if s.Deferred {
			b.WriteString(" — deferred")
			if r := sanitizeDeferReason(s.DeferReason); r != "" {
				fmt.Fprintf(&b, ", blocked on: %s", r)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Headline asks the model for the fleet's one-liner via a forced tool call,
// the same shape as Summarize, so the reply is a field and not prose.
func (s *Summarizer) Headline(ctx context.Context, sessions []swSession) (string, error) {
	tool := anthropic.ToolParam{
		Name:        headlineToolName,
		Description: anthropic.String("Report the one-sentence headline for the fleet."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "One sentence, present tense, under 140 characters: what is being worked on overall and what is waiting.",
				},
			},
			Required: []string{"text"},
		},
	}
	resp, err := s.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:      anthropic.Model(s.model),
		MaxTokens:  headlineMaxTokens,
		System:     []anthropic.TextBlockParam{{Text: headlineSystemPrompt}},
		Tools:      []anthropic.ToolUnionParam{{OfTool: &tool}},
		ToolChoice: anthropic.ToolChoiceParamOfTool(headlineToolName),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(buildHeadlinePrompt(sessions))),
		},
	})
	if err != nil {
		return "", err
	}
	for _, block := range resp.Content {
		tu, ok := block.AsAny().(anthropic.ToolUseBlock)
		if !ok || tu.Name != headlineToolName {
			continue
		}
		var out struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(tu.JSON.Input.Raw()), &out); err != nil {
			return "", fmt.Errorf("headline tool input: %w", err)
		}
		out.Text = strings.TrimSpace(out.Text)
		if placeholderLine(out.Text) {
			return "", errPlaceholderSummary
		}
		return out.Text, nil
	}
	return "", errors.New("no headline tool call in response")
}

// headlineGate decides whether a snapshot is worth a call: only when its
// fingerprint differs from the one last called with, and not within
// interval of that call. A change that arrives during the cooldown is not
// lost — the fingerprint stays different, so the next poll after the
// cooldown fires it. due records the call when it says yes.
type headlineGate struct {
	interval time.Duration
	lastFP   string
	lastCall time.Time
}

func (g *headlineGate) due(fp string, now time.Time) bool {
	if fp == g.lastFP {
		return false
	}
	if !g.lastCall.IsZero() && now.Sub(g.lastCall) < g.interval {
		return false
	}
	g.lastFP = fp
	g.lastCall = now
	return true
}
```

- [ ] **Step 4: Run tests**

Run: `cd cmd/claudemux-head && go test -run 'TestBuildHeadlinePrompt|TestHeadline' ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/webheadline.go cmd/claudemux-head/webheadline_test.go
git commit -m "web: fleet headline call and change gate"
```

---

### Task 6: The headline worker

**Files:**
- Modify: `cmd/claudemux-head/webheadline.go`
- Test: `cmd/claudemux-head/webheadline_test.go`

**Interfaces:**
- Consumes: `webFleet.sessions()`, `webFleet.setHeadline()`, `webFingerprint`, `headlineGate`, `Summarizer.Headline`, `summaryRequestTimeout`, `teardownLogf`.
- Produces:
  - `type webHeadlineWorker struct{ … }`
  - `func startHeadlineWorker(f *webFleet, s *Summarizer, interval time.Duration) *webHeadlineWorker` — nil when `s` is nil.
  - `func (w *webHeadlineWorker) poke()` — nil-safe, never blocks.
  - `func (w *webHeadlineWorker) stop()` — nil-safe, waits for the loop to exit.

- [ ] **Step 1: Write the failing tests**

Append to `webheadline_test.go`:

```go
func waitForHeadline(t *testing.T, f *webFleet, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v := f.view(); v.Headline != nil && v.Headline.Text == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("headline never became %q; view = %+v", want, f.view().Headline)
}

func TestHeadlineWorkerWritesHeadlineOnPoke(t *testing.T) {
	f := newWebFleet()
	f.publish(webTestSnapshot(), RateLimits{}, false, nil, time.Now())
	d := &fakeDoer{body: headlineResponse("Fixing the build.")}
	w := startHeadlineWorker(f, testSummarizer(d), 0)
	defer w.stop()
	w.poke()
	waitForHeadline(t, f, "Fixing the build.")
	if v := f.view(); v.Headline.Stale {
		t.Error("a headline written from the current fleet must not be stale")
	}
	// Same fleet again: no second call.
	w.poke()
	time.Sleep(50 * time.Millisecond)
	if d.calls != 1 {
		t.Errorf("calls = %d, want 1 — an unchanged fleet must not spend a call", d.calls)
	}
}

func TestHeadlineWorkerKeepsPreviousOnError(t *testing.T) {
	f := newWebFleet()
	f.publish(webTestSnapshot(), RateLimits{}, false, nil, time.Now())
	d := &fakeDoer{body: headlineResponse("First.")}
	w := startHeadlineWorker(f, testSummarizer(d), 0)
	defer w.stop()
	w.poke()
	waitForHeadline(t, f, "First.")

	d.err = errors.New("api down")
	snap := webTestSnapshot()
	snap.Sessions[0].Summary = "opening the PR"
	f.publish(snap, RateLimits{}, false, nil, time.Now())
	w.poke()
	time.Sleep(100 * time.Millisecond)
	v := f.view()
	if v.Headline == nil || v.Headline.Text != "First." {
		t.Fatalf("a failed call must keep the previous headline, got %+v", v.Headline)
	}
	if !v.Headline.Stale {
		t.Error("the kept headline must read as stale against the changed fleet")
	}
}

func TestHeadlineWorkerSkipsEmptyFleet(t *testing.T) {
	f := newWebFleet()
	d := &fakeDoer{body: headlineResponse("Nothing.")}
	w := startHeadlineWorker(f, testSummarizer(d), 0)
	defer w.stop()
	w.poke()
	time.Sleep(50 * time.Millisecond)
	if d.calls != 0 {
		t.Errorf("calls = %d, want 0 — an empty fleet has nothing to headline", d.calls)
	}
}

func TestHeadlineWorkerNilSummarizerIsInert(t *testing.T) {
	var w *webHeadlineWorker = startHeadlineWorker(newWebFleet(), nil, 0)
	if w != nil {
		t.Fatal("no summarizer means no worker")
	}
	w.poke() // nil-safe
	w.stop() // nil-safe
}
```

Add `"errors"` to the test file's imports.

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/claudemux-head && go test -run 'TestHeadlineWorker' ./...`
Expected: FAIL to compile, `undefined: startHeadlineWorker`.

- [ ] **Step 3: Implement the worker**

Append to `webheadline.go`:

```go
// webHeadlineWorker is the goroutine that turns fleet changes into
// headline calls. The lobby pokes it after every publish; it decides, via
// the gate, whether that beat is worth a call. One goroutine, one call at a
// time: a slow API stretches the interval rather than stacking requests.
type webHeadlineWorker struct {
	fleet *webFleet
	s     *Summarizer
	gate  headlineGate
	wake  chan struct{}
	quit  chan struct{}
	done  chan struct{}
}

// startHeadlineWorker returns nil when there is no summarizer (summaries
// disabled or no key): the page then shows counts in place of a headline.
func startHeadlineWorker(f *webFleet, s *Summarizer, interval time.Duration) *webHeadlineWorker {
	if s == nil {
		return nil
	}
	w := &webHeadlineWorker{
		fleet: f,
		s:     s,
		gate:  headlineGate{interval: interval},
		wake:  make(chan struct{}, 1),
		quit:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go w.run()
	return w
}

// poke wakes the worker. The channel holds one pending wake, so a burst of
// polls collapses into one look at the fleet and the caller never blocks.
func (w *webHeadlineWorker) poke() {
	if w == nil {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *webHeadlineWorker) stop() {
	if w == nil {
		return
	}
	close(w.quit)
	<-w.done
}

func (w *webHeadlineWorker) run() {
	defer close(w.done)
	for {
		select {
		case <-w.quit:
			return
		case <-w.wake:
		}
		sessions := w.fleet.sessions()
		if len(sessions) == 0 {
			continue
		}
		fp := webFingerprint(sessions)
		if !w.gate.due(fp, time.Now()) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), summaryRequestTimeout)
		text, err := w.s.Headline(ctx, sessions)
		cancel()
		if err != nil {
			teardownLogf("web: headline: %v", err)
			continue
		}
		w.fleet.setHeadline(webHeadline{Text: text, At: time.Now(), Fingerprint: fp})
	}
}
```

- [ ] **Step 4: Run tests, including the race detector**

Run: `cd cmd/claudemux-head && go test -race -run 'TestHeadlineWorker|TestWebFleet' ./... && go vet ./...`
Expected: PASS with no race reports.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/webheadline.go cmd/claudemux-head/webheadline_test.go
git commit -m "web: headline worker"
```

---

### Task 7: Wire the server into the lobby

**Files:**
- Modify: `cmd/claudemux-head/webserver.go`
- Modify: `cmd/claudemux-head/switchboardtui.go` (`swModel` struct ~line 253, `swSnapshotMsg` case ~line 707, `View` title ~line 1170, `runSwitchboard` ~line 1533)
- Test: `cmd/claudemux-head/webserver_test.go`, `cmd/claudemux-head/switchboardtui_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1–6, `loadConfig`, `newSummarizer`, `swTitleStyle`, `swStatusStyle`, `swWaitStyle`, `clipLine`.
- Produces:
  - `type swWeb struct { fleet *webFleet; worker *webHeadlineWorker; server *webServer }`
  - `func startSwitchboardWeb(cfg Config, tailscaleIP func() (string, error)) (*swWeb, error)` — `nil, nil` when `web.listen` is empty.
  - `func (w *swWeb) addr() string`, `func (w *swWeb) stop()` — nil-safe.
  - New `swModel` fields: `web *webFleet`, `headliner *webHeadlineWorker`, `webAddr string`, `webErr string`.

- [ ] **Step 1: Write the failing tests**

Append to `webserver_test.go`:

```go
func TestStartSwitchboardWebOffWhenUnset(t *testing.T) {
	w, err := startSwitchboardWeb(defaultConfig(), func() (string, error) { return "100.64.0.15", nil })
	if err != nil || w != nil {
		t.Fatalf("got %+v, %v; want nil, nil for an empty web.listen", w, err)
	}
	w.stop() // nil-safe
	if w.addr() != "" {
		t.Error("nil swWeb must have no address")
	}
}

func TestStartSwitchboardWebBindsAndReportsResolveFailure(t *testing.T) {
	cfg := defaultConfig()
	cfg.Web.Listen = "127.0.0.1:0"
	cfg.Summary.Enabled = false // no headline worker without a key
	w, err := startSwitchboardWeb(cfg, func() (string, error) { return "", errors.New("unused") })
	if err != nil || w == nil {
		t.Fatalf("got %+v, %v", w, err)
	}
	defer w.stop()
	if !strings.HasPrefix(w.addr(), "127.0.0.1:") {
		t.Errorf("addr = %q", w.addr())
	}
	if w.worker != nil {
		t.Error("summaries disabled must mean no headline worker")
	}
	resp, err := http.Get("http://" + w.addr() + "/api/fleet")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	cfg.Web.Listen = "tailscale:0"
	_, err = startSwitchboardWeb(cfg, func() (string, error) { return "", errors.New("tailscale ip -4: not running") })
	if err == nil || !strings.Contains(err.Error(), "tailscale") {
		t.Fatalf("err = %v, want a tailscale resolve failure", err)
	}
}
```

Add `"errors"` to that file's imports. Then append to `switchboardtui_test.go`:

```go
func TestSwSnapshotPublishesToWebFleet(t *testing.T) {
	m := swTestModel()
	m.web = newWebFleet()
	next, _ := m.Update(swSnapshotMsg{snap: m.snap, at: time.Now()})
	if v := next.(swModel).web.view(); len(v.Sessions) != 3 {
		t.Fatalf("web fleet after a snapshot has %d sessions, want 3", len(v.Sessions))
	}
	// A tmux error keeps the old fleet on the page rather than blanking it.
	next, _ = next.(swModel).Update(swSnapshotMsg{at: time.Now(), err: errors.New("tmux is wedged")})
	if v := next.(swModel).web.view(); len(v.Sessions) != 3 {
		t.Errorf("a failed poll must not empty the web fleet, got %d sessions", len(v.Sessions))
	}
}

func TestSwViewShowsWebAddressOrError(t *testing.T) {
	m := swTestModel()
	m.webAddr = "100.64.0.15:7474"
	if out := m.View(); !strings.Contains(out, "http://100.64.0.15:7474/") {
		t.Errorf("title must show the page URL; got first line %q", strings.SplitN(out, "\n", 2)[0])
	}
	m.webAddr = ""
	m.webErr = "listen tcp 100.64.0.15:7474: bind: address already in use"
	out := m.View()
	if !strings.Contains(out, "web: listen tcp") {
		t.Errorf("title must show the web error; got first line %q", strings.SplitN(out, "\n", 2)[0])
	}
	next, _ := m.Update(swSnapshotMsg{snap: m.snap, at: time.Now()})
	if !strings.Contains(next.(swModel).View(), "web: listen tcp") {
		t.Error("the web error must survive a successful poll (unlike lastErr)")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd cmd/claudemux-head && go test -run 'TestStartSwitchboardWeb|TestSwSnapshotPublishesToWebFleet|TestSwViewShowsWebAddress' ./...`
Expected: FAIL to compile, `undefined: startSwitchboardWeb` / `m.web undefined`.

- [ ] **Step 3: Add `swWeb` to `webserver.go`**

```go
// swWeb is everything the lobby owns for the page: the holder its polls
// publish to, the worker it pokes, and the server. Built once in
// runSwitchboard, stopped on every exit path.
type swWeb struct {
	fleet  *webFleet
	worker *webHeadlineWorker
	server *webServer
}

// startSwitchboardWeb reads web.listen and stands the page up. nil, nil
// means the page is off. An error means the page could not start — the
// lobby shows it and runs on without one.
func startSwitchboardWeb(cfg Config, tailscaleIP func() (string, error)) (*swWeb, error) {
	l, on, err := parseWebListen(cfg.Web.Listen)
	if err != nil || !on {
		return nil, err
	}
	addr, err := resolveWebListen(l, tailscaleIP)
	if err != nil {
		return nil, err
	}
	fleet := newWebFleet()
	server, err := startWebServer(addr, webHandler(fleet))
	if err != nil {
		return nil, err
	}
	worker := startHeadlineWorker(fleet, newSummarizer(cfg.Summary), cfg.Web.HeadlineInterval.Duration)
	return &swWeb{fleet: fleet, worker: worker, server: server}, nil
}

func (w *swWeb) addr() string {
	if w == nil {
		return ""
	}
	return w.server.addr()
}

func (w *swWeb) stop() {
	if w == nil {
		return
	}
	w.server.stop()
	w.worker.stop()
}
```

- [ ] **Step 4: Wire the model**

In `switchboardtui.go`, add to `swModel` (after `restoreArchive`):

```go
	// The web status page (webserver.go). web is the holder every snapshot
	// is published to and headliner the worker poked after it — both nil
	// when web.listen is unset. webAddr is the bound address for the title
	// row; webErr is why the page could not start, kept apart from lastErr
	// because that one is cleared by every successful poll and this one
	// is not a poll's business.
	web       *webFleet
	headliner *webHeadlineWorker
	webAddr   string
	webErr    string
```

In the `swSnapshotMsg` case, right after `m.snap = msg.snap`:

```go
		if m.web != nil {
			m.web.publish(m.snap, m.rateLimits, m.rateOK, m.modelWindows, msg.at)
			m.headliner.poke()
		}
```

In `View`, replace the title assignment:

```go
	title := swTitleStyle.Render("claudemux switchboard") + "  " + swModeBadge(m.standby, m.cond.phase)
	switch {
	case m.webAddr != "":
		title += "  " + swStatusStyle.Render("http://"+m.webAddr+"/")
	case m.webErr != "":
		title += "  " + swWaitStyle.Render("web: "+m.webErr)
	}
```

In `runSwitchboard`, after the `TMUX_PANE` check and before `m := newSwModel(selfPane)`:

```go
	cfg, err := loadConfig()
	if err != nil {
		// Same rule as the session head: a config that exists but does not
		// parse must not be silently replaced by defaults.
		fmt.Fprintf(stderr, "Error loading config: %v\n", err)
		return 1
	}
```

After `m := newSwModel(selfPane)`:

```go
	web, werr := startSwitchboardWeb(cfg, tailscaleIPv4)
	if werr != nil {
		m.webErr = werr.Error()
	} else if web != nil {
		m.web, m.headliner, m.webAddr = web.fleet, web.worker, web.addr()
	}
	// The quit path stops the page here; the restart path below stops it
	// explicitly before exec, since a deferred call never runs past one.
	defer web.stop()
```

And in the restart branch, before `restartSelf(stderr)`:

```go
		web.stop()
```

Rename the existing `final, err := p.Run()` to `final, runErr := p.Run()` (and its `if err != nil` to `runErr`) so it does not shadow `err` from `loadConfig`.

- [ ] **Step 5: Run the full suite with the race detector**

Run: `cd cmd/claudemux-head && go test -race ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 6: Build and run the lobby by hand**

Run: `go build -o /tmp/claudemux-head-web ./cmd/claudemux-head` then, inside a tmux window, with a temporary config:

```bash
mkdir -p /tmp/cmxweb/claudemux && printf 'web:\n  listen: 127.0.0.1:7474\n' > /tmp/cmxweb/claudemux/config.yml
XDG_CONFIG_HOME=/tmp/cmxweb /tmp/claudemux-head-web switchboard
```

Expected: the title row reads `claudemux switchboard  CONDUCTING  http://127.0.0.1:7474/`. Open that URL in a browser: the cards match the lobby rows and change within three seconds of a session's state changing. `q` quits and the URL stops answering. (Note: a summary key is needed for the headline to appear; without one the header shows the counts line. That is the designed fallback.)

- [ ] **Step 7: Commit**

```bash
git add cmd/claudemux-head/webserver.go cmd/claudemux-head/webserver_test.go cmd/claudemux-head/switchboardtui.go cmd/claudemux-head/switchboardtui_test.go
git commit -m "switchboard: serve the web status page when web.listen is set"
```

---

### Task 8: README

**Files:**
- Modify: `README.md` (configuration block at ~line 487, and a new subsection after **After a reboot**, ~line 260)

- [ ] **Step 1: Document the keys**

In the `## Configuration` YAML block, after the `teardown:` entry, add:

```yaml
web:
  listen: ""
  headline_interval: 2m
```

After the `teardown.command` bullet (or at the end of the key list), add:

```markdown
- `web.listen` — where the switchboard serves its web status page (see **Sharing
  the fleet on the tailnet** above). `""` (the default) serves nothing.
  `tailscale:7474` binds this node's Tailscale IPv4 on port 7474; any other
  `host:port` is bound as written. A value with a missing or non-numeric port is
  rejected at startup, by name.
- `web.headline_interval` — the floor between fleet-headline calls, with
  `summary.min_interval`'s rules: it bounds what the page costs, `0` means no
  floor, negative is rejected. A call only fires when the fleet's topics,
  summaries, states or blockers actually change, so a quiet fleet costs nothing
  whatever this is set to.
```

- [ ] **Step 2: Add the subsection**

After the **After a reboot** subsection and before the "Under the title, the lobby shows…" paragraph, add:

```markdown
### Sharing the fleet on the tailnet

With `web.listen` set, the lobby also serves a **web status page**: a read-only
view of the same fleet, for teammates who want to see what you're working on
without asking. Set it once in `config.yml`:

```yaml
web:
  listen: tailscale:7474
```

and the lobby's title row shows the URL it bound — `http://100.64.0.15:7474/`,
or the node's MagicDNS name on that port. The page leads with a one-sentence
**headline** for the whole fleet (Haiku, same key and `summary.enabled` switch
as the per-session summaries; the header shows plain counts until the first one
lands), then the account's 5-hour and weekly gauges, then one card per session
with what its lobby row shows: name in the project color, state and time in it,
context %, model, topic, running summary and last prompt, with deferred sessions
and their blockers parked at the bottom under a rule. It refreshes itself every
three seconds and dims with "lobby not reachable" when the lobby is gone.

The page is up exactly when the lobby is; closing the lobby closes it. It does
no authentication: reachability is the tailnet's job, which is why the
`tailscale` keyword exists — it binds the Tailscale interface only, so nothing
off the tailnet can reach it. `web.listen: ":7474"` would bind every interface,
including whatever Wi-Fi the laptop is on; don't. The page is plain HTTP over
the tailnet's WireGuard tunnel, and never acts on a session — nothing on it can
defer, jump, or type.

The headline is a billable call on your key, gated the way the per-session
summary is: it only fires when a session's state, topic, summary or blocker
changes, and never more often than `web.headline_interval` (default `2m`). Timers
and context percentages ticking never trigger it. The raw prompt line is shown
on the page but never sent to the model. A call that fails keeps the previous
headline on the page, marked as out of date until the next one succeeds.

If the address can't be bound — the port is taken, or `tailscale ip -4` has
nothing to say because tailscale is down — the lobby starts anyway and puts the
reason in its title row where the URL would be. Fix it and restart the lobby
(`R`).
```

- [ ] **Step 3: Check the README renders and nothing else references the old key list**

Run: `grep -n "web\." README.md | head` and confirm the two keys appear in both places. Run `cd cmd/claudemux-head && go test ./...` once more.
Expected: both greps hit; tests PASS.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: the switchboard web status page"
```

---

## Self-review notes

- **Spec coverage:** §1 config → Task 1; §2 lifecycle (resolve, bind, non-fatal failure, close on quit and restart, title shows address) → Tasks 2, 4, 7; §3 holder → Task 3; §4 routes, JSON shape, page, `no-store`, phone layout, dark mode, offline dimming → Tasks 3–4; §5 headline (reuses `Summarizer`, no prompts, forced tool, fingerprint + interval gate, stale flag, first call immediate, failure keeps previous) → Tasks 5–6; §6 failure handling (recover, 500 on encode, `teardownLogf` only, timeouts) → Task 4; §7 README → Task 8; testing list → each task's Step 1, manual check → Task 7 Step 6.
- **Type consistency:** `webHeadline{Text, At, Fingerprint}` (Task 3) is what Task 6 writes; `webFleet.sessions()` / `setHeadline()` (Task 3) are what Task 6 reads and writes; `startWebServer` / `webServer.addr()` / `stop()` (Task 4) are what Task 7's `swWeb` wraps; `headlineGate.due` (Task 5) is what Task 6 calls; `resolveWebListen(l, fn)` (Task 2) matches Task 7's `startSwitchboardWeb(cfg, fn)`.
- **Deviation from spec, deliberate:** the bind-failure reason is shown in the title row rather than on the `lastErr` status line, because `lastErr` is cleared on every successful poll and the layout counts its row; a durable error belongs where the URL would be. The spec's "status line" intent (visible, not fatal) is kept.
- **Review Focus mapping:** 1 → Task 4 (`innerHTML` absent, prompt passthrough) ; 2 → Task 4 second bind + Task 7 title error survives a poll; 3 → Task 2 `parseTailscaleIP` cases; 4 → Task 6 empty-fleet test; 5 → Task 3 stale flag + Task 6 error-keeps-previous.
