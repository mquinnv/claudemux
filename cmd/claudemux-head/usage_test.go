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

// countingClaude is fakeClaude plus a spawn counter: every invocation appends
// a line to counter, so a test can assert how many spawns actually happened.
func countingClaude(t *testing.T, counter, body string, exitCode int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "counting-claude")
	script := "#!/bin/sh\necho x >> " + counter + "\n"
	if body != "" {
		script += "cat <<'FAKEEOF'\n" + body + "\nFAKEEOF\n"
	}
	script += "cat >/dev/null\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// spawnCount reports how many times a countingClaude ran. A missing file is
// zero spawns, not an error.
func spawnCount(t *testing.T, counter string) int {
	t.Helper()
	data, err := os.ReadFile(counter)
	if err != nil {
		return 0
	}
	return len(splitLines(string(data)))
}

// A failed fetch writes no cache, so usageTTL — measured off the cache's own
// timestamp — cannot gate the retry. Without a failure marker every pane would
// re-spawn a claude on every check interval for as long as `claude` is missing
// or get_usage errors, and the lock would not help because staggered panes
// never collide. After a failure, nothing may spawn again until usageTTL has
// passed.
func TestRefreshUsageCacheBacksOffAfterFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	counter := filepath.Join(dir, "spawns")
	bin := countingClaude(t, counter, "", 1)

	now := time.Now()
	if _, err := refreshUsageCache(context.Background(), path, bin, now); err == nil {
		t.Fatal("refreshUsageCache returned nil error for a failing fetch, want an error")
	}
	if n := spawnCount(t, counter); n != 1 {
		t.Fatalf("spawned %d times on the first attempt, want 1", n)
	}

	// Every later attempt inside the window — this process's next tick, and
	// any other pane's — must not spawn.
	for _, after := range []time.Duration{time.Minute, 5 * time.Minute, usageTTL - time.Second} {
		if _, err := refreshUsageCache(context.Background(), path, bin, now.Add(after)); err != nil {
			t.Errorf("refreshUsageCache at +%s returned %v, want the backoff reported as a plain cache read", after, err)
		}
		if n := spawnCount(t, counter); n != 1 {
			t.Fatalf("spawned %d times by +%s, want still 1 (the backoff did not hold)", n, after)
		}
	}

	// Past the window it tries again — the backoff is a delay, not a latch.
	if _, err := refreshUsageCache(context.Background(), path, bin, now.Add(usageTTL+time.Minute)); err == nil {
		t.Error("refreshUsageCache past the backoff window returned nil error, want the failing fetch's error")
	}
	if n := spawnCount(t, counter); n != 2 {
		t.Errorf("spawned %d times past the window, want 2 (the backoff never expired)", n)
	}
}

// A transient failure must never clear rows a good poll already stored: the
// marker is a separate file and the cache is not touched on the failure path.
func TestRefreshUsageCacheFailureKeepsGoodCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	counter := filepath.Join(dir, "spawns")
	now := time.Now()

	if err := writeUsageCache(path, PlanUsage{
		Available: true,
		Models:    []ModelWindow{{Name: "Fable", UsedPercent: 26}},
		FetchedAt: now.Add(-usageTTL - time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	bin := countingClaude(t, counter, "", 1)
	if _, err := refreshUsageCache(context.Background(), path, bin, now); err == nil {
		t.Fatal("want an error from the failing fetch")
	}
	got, _ := readUsageCache(path, now)
	if len(got.Models) != 1 || got.Models[0].Name != "Fable" {
		t.Fatalf("cache after a failed poll = %+v, want the stored Fable row untouched", got.Models)
	}

	// And during the backoff the stored rows are what callers get back, so a
	// pane that restarts mid-outage still shows them.
	served, err := refreshUsageCache(context.Background(), path, bin, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("refreshUsageCache inside the backoff = %v, want nil", err)
	}
	if len(served.Models) != 1 || served.Models[0].Name != "Fable" {
		t.Errorf("served = %+v, want the stored Fable row", served.Models)
	}
	if n := spawnCount(t, counter); n != 1 {
		t.Errorf("spawned %d times, want 1", n)
	}
}

// Recovery is immediate: a poll that works clears the marker rather than
// leaving the rest of the window to run down.
func TestRefreshUsageCacheSuccessClearsBackoff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	counter := filepath.Join(dir, "spawns")
	now := time.Now()

	if _, err := refreshUsageCache(context.Background(), path, countingClaude(t, counter, "", 1), now); err == nil {
		t.Fatal("want an error from the failing fetch")
	}
	if !usageBackoffActive(path, now.Add(time.Minute)) {
		t.Fatal("backoff not armed after a failure")
	}

	good := countingClaude(t, counter, fixture(t, "get_usage_response.json"), 0)
	if _, err := refreshUsageCache(context.Background(), path, good, now.Add(usageTTL+time.Minute)); err != nil {
		t.Fatalf("refreshUsageCache after the window: %v", err)
	}
	if usageBackoffActive(path, now.Add(usageTTL+time.Minute)) {
		t.Error("backoff still armed after a successful poll, want it cleared")
	}
	if _, err := os.Stat(usageFailMarkerPath(path)); !os.IsNotExist(err) {
		t.Errorf("failure marker still on disk after a successful poll (stat err = %v)", err)
	}
}

// unavailableClaude impersonates a Claude Code answering from a pane whose
// environment carries ANTHROPIC_API_KEY or CLAUDE_CODE_USE_BEDROCK=1: the
// request succeeds and reports rate_limits_available:false.
func unavailableClaude(t *testing.T, counter string) string {
	t.Helper()
	return countingClaude(t, counter,
		`{"type":"control_response","response":{"subtype":"success","request_id":"usage-1",`+
			`"response":{"subscription_type":null,"rate_limits_available":false,"rate_limits":null}}}`, 0)
}

// One pane's credentials must never take the model rows away from the others.
//
// fetchPlanUsage inherits the environment of the pane that spawned it, but the
// cache is machine-global: a head started with CLAUDE_CODE_USE_BEDROCK=1 or
// ANTHROPIC_API_KEY set wins the single-flight lock, is told
// rate_limits_available:false, and — before this — wrote available:false with
// no rows over a subscriber pane's good answer. Every other head and the
// switchboard then read that, latched, and stopped polling for the life of the
// process: the model meters vanished machine-wide until every pane restarted.
//
// So an unavailable verdict is never persisted. It is returned to the caller
// (with Fetched set, which is the scope it applies to) and the shared cache is
// left exactly as it was.
func TestRefreshUsageCacheKeepsSharedRowsWhenUnavailable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	counter := filepath.Join(dir, "spawns")
	now := time.Now()

	// What a subscriber pane fetched earlier, now past the TTL.
	if err := writeUsageCache(path, PlanUsage{
		Available: true,
		Models:    []ModelWindow{{Name: "Fable", UsedPercent: 26}},
		FetchedAt: now.Add(-usageTTL - time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := refreshUsageCache(context.Background(), path, unavailableClaude(t, counter), now)
	if err != nil {
		t.Fatalf("refreshUsageCache: %v", err)
	}
	if got.Available {
		t.Error("Available = true, want the unavailable verdict reported to the caller")
	}
	if !got.Fetched {
		t.Error("Fetched = false, want the verdict marked as this process's own")
	}
	if n := spawnCount(t, counter); n != 1 {
		t.Fatalf("spawned %d times, want 1", n)
	}

	// The shared cache — what every OTHER pane on the machine reads.
	onDisk, _ := readUsageCache(path, now)
	if !onDisk.Available {
		t.Error("available:false was written to the machine-global cache; every other pane would read it and stop polling")
	}
	if len(onDisk.Models) != 1 || onDisk.Models[0].Name != "Fable" {
		t.Errorf("cached models = %+v, want the subscriber pane's Fable row untouched", onDisk.Models)
	}
}

// The unavailable path is not a failure: a later poll from a pane with real
// OAuth credentials must be able to run immediately rather than sitting out a
// backoff armed by the Bedrock pane beside it.
func TestRefreshUsageCacheUnavailableArmsNoBackoff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	counter := filepath.Join(dir, "spawns")
	now := time.Now()

	if _, err := refreshUsageCache(context.Background(), path, unavailableClaude(t, counter), now); err != nil {
		t.Fatalf("refreshUsageCache: %v", err)
	}
	if usageBackoffActive(path, now.Add(time.Second)) {
		t.Fatal("backoff armed by an unavailable verdict: it would hold back the subscriber panes too")
	}

	good := countingClaude(t, counter, fixture(t, "get_usage_response.json"), 0)
	got, err := refreshUsageCache(context.Background(), path, good, now.Add(time.Second))
	if err != nil {
		t.Fatalf("refreshUsageCache from a subscriber pane: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].Name != "Fable" {
		t.Errorf("got = %+v, want the subscriber pane's fetch to have gone through", got.Models)
	}
	if n := spawnCount(t, counter); n != 2 {
		t.Errorf("spawned %d times, want 2 (the subscriber pane was held back)", n)
	}
}

// A wall clock that steps BACKWARDS — suspend/resume, an NTP correction —
// leaves a cache written moments ago stamped in the future. The backoff marker
// already shrugged that off (it took the absolute age); the cache read did not
// (it required age >= 0 and called any future stamp infinitely stale), so the
// two halves of this file disagreed about the same clock and the same skew,
// and the machine went back to spawning a Claude Code for an answer already on
// disk. One symmetric rule now covers both: inside the TTL window in either
// direction is inside the window.
func TestUsageWindowsAgreeAcrossABackwardClockJump(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	counter := filepath.Join(dir, "spawns")
	now := time.Now()

	// Written a few minutes ago; then the clock stepped back further, so the
	// stamp reads a minute into the future. The answer is a minute old.
	skewed := now.Add(time.Minute)
	if err := writeUsageCache(path, PlanUsage{
		Available: true,
		Models:    []ModelWindow{{Name: "Fable", UsedPercent: 26}},
		FetchedAt: skewed,
	}); err != nil {
		t.Fatal(err)
	}
	got, fresh := readUsageCache(path, now)
	if !fresh {
		t.Error("fresh = false for a future-stamped cache: a clock step back re-spawns a Claude Code for an answer already on disk")
	}
	if len(got.Models) != 1 {
		t.Errorf("Models = %+v, want the cached row still served", got.Models)
	}
	// The other half of the file, on the identical stamp. These two must not
	// disagree: that disagreement is the whole finding.
	if !usageStampFresh(skewed, now) {
		t.Error("usageStampFresh disagrees with the cache read on the same future stamp")
	}

	// End to end through the loop's own command, which reads the cache against
	// the real clock: nothing may spawn while a future-stamped cache is inside
	// the window.
	t.Setenv("CLAUDEMUX_CLAUDE_BIN", countingClaude(t, counter, fixture(t, "get_usage_response.json"), 0))
	_ = usageCmd(path, true)()
	if n := spawnCount(t, counter); n != 0 {
		t.Errorf("spawned %d times against a future-stamped cache, want 0", n)
	}

	// The window is bounded in the future too, so a nonsense stamp — a clock
	// that jumped forward, a hand-edited file — cannot pin the meters to frozen
	// numbers forever. It reads stale, is refetched, and is rewritten against
	// the current clock, which settles the loop back to one spawn per TTL
	// rather than one per check.
	ahead := filepath.Join(t.TempDir(), "usage.json")
	if err := writeUsageCache(ahead, PlanUsage{Available: true, FetchedAt: now.Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, fresh := readUsageCache(ahead, now); fresh {
		t.Error("fresh = true for a cache stamped a day ahead, want it refetched rather than trusted forever")
	}
	if usageStampFresh(now.Add(24*time.Hour), now) {
		t.Error("usageStampFresh accepts a day-ahead stamp; a backoff marker carrying one could then wedge the poller off")
	}
	_ = usageCmd(ahead, true)()
	if n := spawnCount(t, counter); n != 1 {
		t.Fatalf("spawned %d times for the day-ahead cache, want exactly 1", n)
	}
	if _, fresh := readUsageCache(ahead, now); !fresh {
		t.Error("the refetch did not re-stamp the cache against the current clock: the machine would spawn again on the very next check")
	}
	_ = usageCmd(ahead, true)()
	if n := spawnCount(t, counter); n != 1 {
		t.Errorf("spawned %d times by the second check, want still 1", n)
	}
}
