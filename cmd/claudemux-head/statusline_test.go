package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runStatuslineInto runs the subcommand against a payload and returns the
// exit code plus whatever landed at the cache path.
func runStatuslineInto(t *testing.T, payload string) (int, string, []byte) {
	t.Helper()
	dir := t.TempDir()
	cache := filepath.Join(dir, "nested", "rate-limits.json")
	t.Setenv("CLAUDEMUX_RATE_LIMITS_PATH", cache)

	// The process's REAL stdout, not an injected writer: runStatusline takes
	// no writers any more, so the only way it could break the user's status
	// line is by printing to os.Stdout — which is also the one route a stray
	// fmt.Println would have taken straight past the injected buffer this
	// used to assert on.
	captured := filepath.Join(dir, "stdout")
	f, err := os.Create(captured)
	if err != nil {
		t.Fatal(err)
	}
	realOut := os.Stdout
	os.Stdout = f
	code := runStatusline(strings.NewReader(payload))
	os.Stdout = realOut
	f.Close()
	out, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(cache)
	if err != nil {
		data = nil
	}
	return code, string(out), data
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

// Every running Claude Code writes its own last-seen percentage into the one
// cache, so a process that is a few seconds behind overwrites a fresher value
// with a lower one and the file flip-flops (21, 22, 21, 22...). Usage cannot
// fall inside a window, so a lower value for the SAME reset, arriving within
// the lag window of a higher one, is staleness and is held off.
func TestStatuslineMergeHoldsBriefDipInSameWindow(t *testing.T) {
	now := time.Date(2026, 9, 4, 20, 0, 0, 0, time.UTC)
	prev := rawRateLimitsFile{Source: statuslineSource, UpdatedAt: now.Add(-5 * time.Second).Unix()}
	prev.FiveHour.UsedPercentage, prev.FiveHour.ResetsAt = 22, 1788577200
	prev.SevenDay.UsedPercentage, prev.SevenDay.ResetsAt = 4, 1788814800
	in := prev
	in.UpdatedAt = now.Unix()
	in.FiveHour.UsedPercentage = 21

	out, write := mergeRateLimitWrite(&prev, in, now)
	if write {
		t.Fatalf("a lagging write changed nothing, want no write; got %+v", out)
	}
	if out.FiveHour.UsedPercentage != 22 {
		t.Errorf("five_hour = %v, want the fresher 22 held", out.FiveHour.UsedPercentage)
	}
}

// A drop that persists past the lag window is real — the limit was rescaled —
// and lands.
func TestStatuslineMergeLetsPersistentDropLand(t *testing.T) {
	now := time.Date(2026, 9, 4, 20, 0, 0, 0, time.UTC)
	prev := rawRateLimitsFile{Source: statuslineSource, UpdatedAt: now.Add(-(statuslineLagWindow + time.Second)).Unix()}
	prev.FiveHour.UsedPercentage, prev.FiveHour.ResetsAt = 22, 1788577200
	in := prev
	in.UpdatedAt = now.Unix()
	in.FiveHour.UsedPercentage = 21

	out, write := mergeRateLimitWrite(&prev, in, now)
	if !write || out.FiveHour.UsedPercentage != 21 {
		t.Errorf("write=%v five_hour=%v, want the drop written as 21", write, out.FiveHour.UsedPercentage)
	}
}

// A different reset time is a new window; its lower value is the truth even
// a second after the old window's high one.
func TestStatuslineMergeNewWindowAlwaysLands(t *testing.T) {
	now := time.Date(2026, 9, 4, 20, 0, 0, 0, time.UTC)
	prev := rawRateLimitsFile{Source: statuslineSource, UpdatedAt: now.Add(-time.Second).Unix()}
	prev.FiveHour.UsedPercentage, prev.FiveHour.ResetsAt = 97, 1788577200
	in := prev
	in.UpdatedAt = now.Unix()
	in.FiveHour.UsedPercentage, in.FiveHour.ResetsAt = 1, 1788595200

	out, write := mergeRateLimitWrite(&prev, in, now)
	if !write || out.FiveHour.UsedPercentage != 1 {
		t.Errorf("write=%v five_hour=%v, want the new window's 1 written", write, out.FiveHour.UsedPercentage)
	}
}

// End to end: a cache written moments ago at 22 is not lowered to 21 by the
// next statusline render for the same window.
func TestStatuslineLaggingWriterDoesNotLowerCache(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "rate-limits.json")
	t.Setenv("CLAUDEMUX_RATE_LIMITS_PATH", cache)
	seed := rawRateLimitsFile{Source: statuslineSource, UpdatedAt: time.Now().Unix()}
	seed.FiveHour.UsedPercentage, seed.FiveHour.ResetsAt = 22, 1788577200
	seed.SevenDay.UsedPercentage, seed.SevenDay.ResetsAt = 4, 1788814800
	blob, _ := json.Marshal(seed)
	if err := os.WriteFile(cache, blob, 0o600); err != nil {
		t.Fatal(err)
	}

	code := runStatusline(strings.NewReader(`{"rate_limits": {
		"five_hour": {"used_percentage": 21, "resets_at": 1788577200},
		"seven_day": {"used_percentage": 4, "resets_at": 1788814800}
	}}`))
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	data, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	var got rawRateLimitsFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("cache does not parse: %v", err)
	}
	if got.FiveHour.UsedPercentage != 22 {
		t.Errorf("five_hour = %v, want 22 held against the lagging 21", got.FiveHour.UsedPercentage)
	}
}

// multiAccountHome writes a two-account config.yml (default=ameriglide, which
// has a config dir; bespoke is the ~/.claude account) under a temp HOME and
// returns the home and the claudemux cache dir.
func multiAccountHome(t *testing.T) (home, cacheDir string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CLAUDEMUX_RATE_LIMITS_PATH", "")
	cfgDir := filepath.Join(home, ".config", "claudemux")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "claude:\n  default_account: ameriglide\n  accounts:\n    ameriglide:\n      config_dir: ~/.claude-accounts/ameriglide\n    bespoke: {}\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, filepath.Join(home, ".claude", "claudemux")
}

func cacheFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

const statuslineTestPayload = `{"rate_limits":{"five_hour":{"used_percentage":12,"resets_at":1787321400},"seven_day":{"used_percentage":62,"resets_at":1787605200}}}`

func TestStatuslineWritesAccountFile(t *testing.T) {
	home, cache := multiAccountHome(t)

	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude-accounts", "ameriglide"))
	runStatusline(strings.NewReader(statuslineTestPayload))
	if got := strings.Join(cacheFiles(t, cache), ","); got != "rate-limits-ameriglide.json,rate-limits.json" {
		t.Errorf("default account wrote %q, want its own file plus the legacy one", got)
	}

	os.RemoveAll(cache)
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	runStatusline(strings.NewReader(statuslineTestPayload))
	if got := strings.Join(cacheFiles(t, cache), ","); got != "rate-limits-bespoke.json" {
		t.Errorf("non-default account wrote %q, want only rate-limits-bespoke.json", got)
	}

	os.RemoveAll(cache)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "somewhere-else"))
	runStatusline(strings.NewReader(statuslineTestPayload))
	if got := cacheFiles(t, cache); len(got) != 0 {
		t.Errorf("unmappable dir wrote %v, want nothing", got)
	}
}

// Ruling R1: with no claude: block the legacy file is written whatever
// CLAUDE_CONFIG_DIR says.
func TestStatuslineSingleAccountIgnoresConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CLAUDEMUX_RATE_LIMITS_PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "custom"))
	runStatusline(strings.NewReader(statuslineTestPayload))
	if got := strings.Join(cacheFiles(t, filepath.Join(home, ".claude", "claudemux")), ","); got != "rate-limits.json" {
		t.Errorf("wrote %q, want rate-limits.json", got)
	}
}
