package main

import (
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
