package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestContextPercentFromUsage(t *testing.T) {
	model := "claude-opus-4-7"
	u := Usage{InputTokens: 50_000, CacheReadInputTokens: 80_000, OutputTokens: 1_000}
	pct := contextPercent(model, u)
	if pct < 65 || pct > 66 {
		t.Errorf("contextPercent = %v, want ~65", pct)
	}
}

func TestContextPercentUnknownModel(t *testing.T) {
	u := Usage{InputTokens: 50_000}
	pct := contextPercent("some-future-model", u)
	if pct < 0 {
		t.Errorf("unknown model should default budget; got %v", pct)
	}
}

func TestReadRateLimits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rate-limits.json")
	body := `{"source":"claude","updated_at":1777557367,` +
		`"five_hour":{"used_percentage":9,"resets_at":1777572000},` +
		`"seven_day":{"used_percentage":11,"resets_at":1777928400}}`
	os.WriteFile(path, []byte(body), 0o644)

	rl, err := readRateLimits(path)
	if err != nil {
		t.Fatalf("readRateLimits error: %v", err)
	}
	if rl.FiveHour.UsedPercent != 9 || rl.SevenDay.UsedPercent != 11 {
		t.Errorf("percentages = %d/%d, want 9/11", rl.FiveHour.UsedPercent, rl.SevenDay.UsedPercent)
	}
	if rl.FiveHour.ResetsAt.Unix() != 1777572000 {
		t.Errorf("five_hour ResetsAt = %v, want unix 1777572000", rl.FiveHour.ResetsAt)
	}
	if rl.UpdatedAt.Unix() != 1777557367 {
		t.Errorf("UpdatedAt = %v", rl.UpdatedAt)
	}
}

func TestReadRateLimitsFloatPercent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rate-limits.json")
	body := `{"source":"claude","updated_at":1777666707,` +
		`"five_hour":{"used_percentage":14.000000000000002,"resets_at":1777678800},` +
		`"seven_day":{"used_percentage":28.6,"resets_at":1777928400}}`
	os.WriteFile(path, []byte(body), 0o644)

	rl, err := readRateLimits(path)
	if err != nil {
		t.Fatalf("readRateLimits error: %v", err)
	}
	if rl.FiveHour.UsedPercent != 14 || rl.SevenDay.UsedPercent != 29 {
		t.Errorf("percentages = %d/%d, want 14/29", rl.FiveHour.UsedPercent, rl.SevenDay.UsedPercent)
	}
}

func TestReadRateLimitsMissingFile(t *testing.T) {
	_, err := readRateLimits("/nonexistent/path.json")
	if err == nil {
		t.Errorf("expected error for missing file")
	}
}

func TestBurnRatePctRolling(t *testing.T) {
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	samples := []pctSample{
		{at: now.Add(-30 * time.Minute), pct: 2},
		{at: now.Add(-15 * time.Minute), pct: 5},
		{at: now.Add(-10 * time.Minute), pct: 10},
		{at: now, pct: 20},
	}
	rate := burnRatePctPerMin(samples, now)
	if rate < 0.9 || rate > 1.1 {
		t.Errorf("burnRatePctPerMin = %v, want ~1.0", rate)
	}
}

func TestBurnRatePctInsufficientData(t *testing.T) {
	now := time.Now()
	samples := []pctSample{{at: now.Add(-30 * time.Second), pct: 5}}
	rate := burnRatePctPerMin(samples, now)
	if rate != 0 {
		t.Errorf("burnRatePctPerMin with <2min data = %v, want 0 (sentinel)", rate)
	}
}

func TestBurnRatePctNonPositive(t *testing.T) {
	now := time.Now()
	samples := []pctSample{
		{at: now.Add(-15 * time.Minute), pct: 50},
		{at: now, pct: 50},
	}
	rate := burnRatePctPerMin(samples, now)
	if rate != 0 {
		t.Errorf("flat pct should give 0 burn rate, got %v", rate)
	}
}

func TestETAToEmptyPct(t *testing.T) {
	eta := etaToEmptyPct(60, 2.0)
	if eta != 20*time.Minute {
		t.Errorf("etaToEmptyPct = %v, want 20m", eta)
	}
}

func TestETAToEmptyPctZeroRate(t *testing.T) {
	if etaToEmptyPct(50, 0) != 0 {
		t.Errorf("zero-rate ETA should be 0 sentinel")
	}
}

func TestETAToEmptyPctAlreadyFull(t *testing.T) {
	if etaToEmptyPct(100, 5) != 0 {
		t.Errorf("at-or-past 100%% should be 0 sentinel")
	}
}

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

func TestPaceColor(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour
	cases := []struct {
		name string
		used int
		left time.Duration // until reset; 0 means no reset time known
		want string
	}{
		// 85% used with 12h of the week left projects to ~92% at reset —
		// under the limit, so green even though the bar is nearly full.
		{"nearly full but on pace to finish under", 85, 12 * time.Hour, "#04B575"},
		// 85% used with half the week left projects to 170%: red.
		{"on pace to exhaust", 85, week / 2, "#EF4444"},
		// 45% used with 90h of 168h left: 45 / (78/168) ≈ 97% projected —
		// inside the yellow band.
		{"close to pace", 45, 90 * time.Hour, "#FFCC00"},
		// 40% used with 90h left projects to ~86%: green.
		{"half full but under pace", 40, 90 * time.Hour, "#04B575"},
		{"exhausted is always red", 100, week / 2, "#EF4444"},
		{"exhausted with a minute left is red", 100, time.Minute, "#EF4444"},
		// Too early in the window to project anything: fall back to fill.
		{"window just started falls back to fill", 90, week - time.Minute, "#EF4444"},
		{"window just started low fill is green", 10, week - time.Minute, "#04B575"},
		{"no reset time falls back to fill", 75, 0, "#FFCC00"},
		// A reset in the past means the cache is stale; fall back to fill.
		{"past reset falls back to fill", 75, -time.Hour, "#FFCC00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var resets time.Time
			if c.left != 0 {
				resets = now.Add(c.left)
			}
			if got := paceColor(c.used, resets, week, now); got != c.want {
				t.Errorf("paceColor(%d, %v left) = %s, want %s", c.used, c.left, got, c.want)
			}
		})
	}
}

// The spike-o-meter reads burn as a multiple of the sustainable pace (100%
// per 5h). 2 points in 3 minutes is 0.667 pt/min — exactly 2x sustainable.
func TestBurnMultipleAgainstSustainablePace(t *testing.T) {
	now := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	samples := []pctSample{
		{at: now.Add(-3 * time.Minute), pct: 18.0},
		{at: now, pct: 20.0},
	}
	if got := burnMultiple(samples, now); got < 1.99 || got > 2.01 {
		t.Errorf("burnMultiple = %v, want ~2.0", got)
	}
}

// Sub-point movement counts: samples carry the cache's unrounded percentage,
// so a run of readings whose rounded percent never changes still yields a
// rate. Rounded to whole points, 18.2→18.4→18.6 is flat and would read 0x.
func TestBurnMultipleUsesFractionalSamples(t *testing.T) {
	now := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	samples := []pctSample{
		{at: now.Add(-4 * time.Minute), pct: 18.2},
		{at: now.Add(-2 * time.Minute), pct: 18.4},
		{at: now, pct: 18.6},
	}
	if got := burnMultiple(samples, now); got <= 0 {
		t.Errorf("burnMultiple over fractional samples = %v, want > 0", got)
	}
}

// The spike gauge looks back five minutes, not the ETA's fifteen: a burst
// that ended eight minutes ago is over, and the meter must say so.
func TestBurnMultipleIgnoresOldBurst(t *testing.T) {
	now := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	samples := []pctSample{
		{at: now.Add(-12 * time.Minute), pct: 5},
		{at: now.Add(-8 * time.Minute), pct: 25},
		{at: now.Add(-4 * time.Minute), pct: 25},
		{at: now, pct: 25},
	}
	if got := burnMultiple(samples, now); got != 0 {
		t.Errorf("burnMultiple after a burst ended = %v, want 0", got)
	}
	if got := burnRatePctPerMin(samples, now); got <= 0 {
		t.Errorf("the ETA's 15-minute rate should still see the burst, got %v", got)
	}
}

// Idle, a fresh window (percent fell), and too little span all read 0x.
func TestBurnMultipleZeroCases(t *testing.T) {
	now := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	cases := map[string][]pctSample{
		"none":   nil,
		"flat":   {{at: now.Add(-3 * time.Minute), pct: 40}, {at: now, pct: 40}},
		"reset":  {{at: now.Add(-3 * time.Minute), pct: 90}, {at: now, pct: 2}},
		"narrow": {{at: now.Add(-20 * time.Second), pct: 10}, {at: now, pct: 12}},
	}
	for name, samples := range cases {
		if got := burnMultiple(samples, now); got != 0 {
			t.Errorf("%s: burnMultiple = %v, want 0", name, got)
		}
	}
}

// readRateLimits keeps the cache's unrounded percentage alongside the
// rounded one the gauges print, because the spike gauge needs the fraction.
func TestReadRateLimitsKeepsExactPercent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rl.json")
	os.WriteFile(path, []byte(`{"five_hour":{"used_percentage":18.4,"resets_at":1787608800},`+
		`"seven_day":{"used_percentage":90,"resets_at":1787605200}}`), 0o644)
	rl, err := readRateLimits(path)
	if err != nil {
		t.Fatal(err)
	}
	if rl.FiveHour.UsedPercent != 18 || rl.FiveHour.Used != 18.4 {
		t.Errorf("five_hour = %+v, want 18 / 18.4", rl.FiveHour)
	}
	if got := rl.FiveHour.usedExact(); got != 18.4 {
		t.Errorf("usedExact = %v, want 18.4", got)
	}
	// A Window built without the exact value (older code paths, tests) still
	// samples from its rounded percent rather than reading as zero.
	if got := (Window{UsedPercent: 42}).usedExact(); got != 42 {
		t.Errorf("usedExact fallback = %v, want 42", got)
	}
}
