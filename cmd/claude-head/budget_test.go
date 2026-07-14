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
