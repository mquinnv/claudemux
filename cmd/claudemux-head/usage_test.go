package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// fakeClaudeLingering impersonates a claude that answers get_usage promptly,
// drains stdin, and then keeps the process alive for lingerSecs before
// exiting — modeling a real claude that does post-response cleanup before
// its own process exits.
func fakeClaudeLingering(t *testing.T, body string, lingerSecs int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-claude-lingering")
	script := "#!/bin/sh\n" +
		"cat <<'FAKEEOF'\n" + body + "\nFAKEEOF\n" +
		"cat >/dev/null\n" +
		"sleep " + strconv.Itoa(lingerSecs) + "\n" +
		"exit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
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

// A claude that answers promptly but then lingers before exiting (e.g. doing
// its own post-response cleanup) must not delay a successful poll: once we
// have parsed the answer, fetchPlanUsage must stop waiting on the child
// instead of blocking until it exits on its own.
func TestFetchPlanUsageReturnsPromptlyDespiteLingeringChild(t *testing.T) {
	const lingerSecs = 5
	path := fakeClaudeLingering(t, fixture(t, "get_usage_response.json"), lingerSecs)

	start := time.Now()
	got, err := fetchPlanUsage(context.Background(), path, time.Now())
	if err != nil {
		t.Fatalf("fetchPlanUsage: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("fetchPlanUsage took %v, want it to return well under the %ds the child lingers after answering", elapsed, lingerSecs)
	}
	if len(got.Models) != 1 || got.Models[0].Name != "Fable" {
		t.Errorf("got = %+v, want the parsed Fable row", got)
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
			defer signalDone(done)
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

// signalDone signals one completion on a shared channel without closing it.
func signalDone(ch chan struct{}) { ch <- struct{}{} }

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
