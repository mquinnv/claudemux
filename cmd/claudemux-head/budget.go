package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Per-model context window budgets (in tokens). Prefix-match supported.
var modelContextBudget = map[string]int{
	"claude-opus-4-7":     200_000,
	"claude-sonnet-4-6":   200_000,
	"claude-haiku-4-5":    200_000,
	"claude-opus-4-7[1m]": 1_000_000,
}

const defaultContextBudget = 200_000

func contextPercent(model string, u Usage) float64 {
	total := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens + u.OutputTokens
	budget := contextBudget(model)
	// Claude Code does not write the [1m]/variant suffix to the JSONL, so the
	// model string lookup may underreport the budget. If observed usage
	// exceeds the table value, infer the larger context variant.
	if total > budget {
		budget = 1_000_000
	}
	return 100.0 * float64(total) / float64(budget)
}

// RateLimits is the in-memory shape of the rate-limit cache file — ours
// (~/.claude/claudemux/rate-limits.json, written by `claudemux-head
// statusline`) and abtop's older file, which share this schema.
//
// Source distinguishes the writer: "claudemux" for ours, "claude" for abtop's.
type RateLimits struct {
	Source    string
	UpdatedAt time.Time
	FiveHour  Window
	SevenDay  Window
}

type Window struct {
	// UsedPercent is what the gauges print. Used is the cache's unrounded
	// used_percentage, kept because the spike gauge measures movement over a
	// few minutes, where a whole point is most of the signal.
	UsedPercent int
	Used        float64
	ResetsAt    time.Time
}

// usedExact is the percentage to sample for burn rates: the unrounded value
// when the Window came from the cache, else the rounded one — a Window built
// from an integer-only source must not sample as zero.
func (w Window) usedExact() float64 {
	if w.Used != 0 {
		return w.Used
	}
	return float64(w.UsedPercent)
}

type rawRateLimitsFile struct {
	Source    string `json:"source"`
	UpdatedAt int64  `json:"updated_at"`
	FiveHour  struct {
		UsedPercentage float64 `json:"used_percentage"`
		ResetsAt       int64   `json:"resets_at"`
	} `json:"five_hour"`
	SevenDay struct {
		UsedPercentage float64 `json:"used_percentage"`
		ResetsAt       int64   `json:"resets_at"`
	} `json:"seven_day"`
}

// readRateLimits parses a rate-limit cache file — whichever of the two
// defaultRateLimitsPath resolved (see there); the schema is the same either
// way.
func readRateLimits(path string) (RateLimits, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RateLimits{}, err
	}
	var raw rawRateLimitsFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return RateLimits{}, err
	}
	return RateLimits{
		Source:    raw.Source,
		UpdatedAt: time.Unix(raw.UpdatedAt, 0),
		FiveHour: Window{
			UsedPercent: int(math.Round(raw.FiveHour.UsedPercentage)),
			Used:        raw.FiveHour.UsedPercentage,
			ResetsAt:    time.Unix(raw.FiveHour.ResetsAt, 0),
		},
		SevenDay: Window{
			UsedPercent: int(math.Round(raw.SevenDay.UsedPercentage)),
			Used:        raw.SevenDay.UsedPercentage,
			ResetsAt:    time.Unix(raw.SevenDay.ResetsAt, 0),
		},
	}, nil
}

// pctSample is a snapshot of five_hour.used_percentage at the moment it
// changed. Samples are recorded only on change, so the percentage between two
// samples is the earlier one's: the series is piecewise constant, and the
// value at any instant is the newest sample at or before it.
//
// The percentage is whole points. It was fractional until 2026-09-04, when the
// utilization Claude Code copies into the payload became an integer; the
// float is kept so a fraction is used if one ever comes back.
type pctSample struct {
	at  time.Time
	pct float64
}

// burnRatePctPerMin returns percentage-points per minute over the last
// ~15 minutes — the smooth signal the "empty in X" projection wants. Returns
// 0 sentinel for "not enough data" (<5min of history) or "rate is zero or
// negative" (idle / window resetting).
func burnRatePctPerMin(samples []pctSample, now time.Time) float64 {
	return burnRate(samples, now, 15*time.Minute, 5*time.Minute)
}

// burnPctPerHour is the spike-o-meter's reading: the last five minutes' burn
// as percent of the 5h window per hour. The unit is chosen to read literally
// — "40%/h" means this pace eats 40% of the window every hour — where a
// multiple of some reference pace needed the reference explained. For
// calibration, 100% over 5h is 20%/h: the pace a fresh window absorbs
// exactly, and where burnColor turns yellow.
//
// The lookback is short because a spike meter is for seeing a burst while it
// happens; the ETA's fifteen minutes would report it ten minutes late and
// average it away. The percentage is whole points, so the reading's
// resolution is one point per span: three minutes is where one point is the
// 20%/h warn line, and less than that made a two-point wobble read as a
// hundred. 0 for idle, a window that just reset, or not enough data.
func burnPctPerHour(samples []pctSample, now time.Time) float64 {
	return burnRate(samples, now, 5*time.Minute, 3*time.Minute) * 60
}

// burnRate is percentage-points per minute over the lookback: the rise from
// the value the window held at the cutoff to the value it holds now, divided
// by the lookback. The baseline is the newest sample at or before the cutoff
// (see pctSample), so a step that happened twenty seconds ago is one point
// over the whole window rather than one point over twenty seconds.
//
// With no sample that old — a head that just started — the earliest sample
// stands in as the baseline and the span runs from it to now; that span must
// reach minSpan, because a short one turns quantization into pace. 0 when the
// percentage did not rise.
func burnRate(samples []pctSample, now time.Time, lookback, minSpan time.Duration) float64 {
	if len(samples) == 0 {
		return 0
	}
	cutoff := now.Add(-lookback)
	var base, earliest, latest pctSample
	baseSet, earliestSet := false, false
	for _, s := range samples {
		if !s.at.After(cutoff) {
			if !baseSet || s.at.After(base.at) {
				base = s
				baseSet = true
			}
			continue
		}
		if !earliestSet || s.at.Before(earliest.at) {
			earliest = s
			earliestSet = true
		}
	}
	for _, s := range samples {
		if s.at.After(latest.at) {
			latest = s
		}
	}
	// With a baseline, the window held base.pct at the cutoff and the span
	// is the whole lookback; without one, measure from the earliest sample.
	start := cutoff
	switch {
	case baseSet:
	case earliestSet:
		base, start = earliest, earliest.at
	default:
		return 0
	}
	span := now.Sub(start)
	if span < minSpan {
		return 0
	}
	delta := latest.pct - base.pct
	if delta <= 0 {
		return 0
	}
	return delta / span.Minutes()
}

// trimSamples drops samples older than cutoff, keeping the newest of them:
// that one is the value the window held AT the cutoff, and burnRate needs it
// as the baseline for the first step after a long flat stretch.
func trimSamples(samples []pctSample, cutoff time.Time) []pctSample {
	baseIdx := -1
	for i, s := range samples {
		if !s.at.After(cutoff) && (baseIdx < 0 || s.at.After(samples[baseIdx].at)) {
			baseIdx = i
		}
	}
	trimmed := samples[:0]
	for i, s := range samples {
		if i == baseIdx || s.at.After(cutoff) {
			trimmed = append(trimmed, s)
		}
	}
	return trimmed
}

// Spike gauge calibration, in percent of the window per hour. The bar is full
// at a whole window per hour — five times the sustainable pace; bursts past
// that happen (a fleet of agents all mid-turn) but past it the answer is
// "very" either way. Yellow starts at the sustainable pace itself: faster
// than the window can absorb, though the 5h bar's fill decides whether that
// matters yet. Red at twice it.
const (
	burnGaugeMaxPerHour = 100.0
	burnWarnPerHour     = 100.0 / 5 // 100% over the 5h window
	burnHotPerHour      = 2 * burnWarnPerHour
)

// burnFillPct maps a %/h reading onto the bar's 0–100 fill.
func burnFillPct(perHour float64) float64 {
	if perHour >= burnGaugeMaxPerHour {
		return 100
	}
	if perHour < 0 {
		return 0
	}
	return perHour / burnGaugeMaxPerHour * 100
}

// burnColor bands the spike gauge (see the calibration constants). Reuses
// thresholdColor's palette so the meter line stays one system.
func burnColor(perHour float64) string {
	switch {
	case perHour >= burnHotPerHour:
		return thresholdColor(85)
	case perHour >= burnWarnPerHour:
		return thresholdColor(70)
	default:
		return thresholdColor(0)
	}
}

// Rate-limit window lengths, for pacing a window's fill against its reset.
const (
	fiveHourWindow = 5 * time.Hour
	weekWindow     = 7 * 24 * time.Hour
)

// paceMinElapsed is the least of a window that must have elapsed before
// projecting from it. The projection is a straight line from the window's
// start through now, so early on a single turn is most of the line. For the
// week, 3% (~5h) is enough that one busy morning doesn't extrapolate to a red
// bar for a week that will end nowhere near the limit. For the 5h window the
// same 3% is nine minutes — one hot first turn after a reset painted the bar
// red for the next half hour — so it waits 15% (45 minutes) instead.
func paceMinElapsed(window time.Duration) float64 {
	if window <= fiveHourWindow {
		return 0.15
	}
	return 0.03
}

// paceColor colors a rate-limit gauge by whether its usage is on pace to
// exhaust the window by its reset, rather than by how full the bar is. A bar
// at 85% with a day left in the week is fine — it will end under the limit —
// while 40% with most of the week left is not. The usage is projected
// linearly to the reset (used / fraction-elapsed): red when the projection
// reaches the limit, yellow when it lands within a few points of it (the
// projection is a straight line through a spiky signal, so 95% projected is
// a coin flip), green otherwise.
//
// Falls back to the raw fill (thresholdColor) when no projection is
// meaningful: an unknown or already-past reset time, or a window so freshly
// started that a sample would swamp the estimate. An exhausted window is red
// regardless.
func paceColor(usedPct int, resetsAt time.Time, window time.Duration, now time.Time) string {
	if usedPct >= 100 {
		return thresholdColor(100)
	}
	left := resetsAt.Sub(now)
	if resetsAt.IsZero() || left <= 0 || left >= window {
		return thresholdColor(float64(usedPct))
	}
	elapsed := 1 - left.Seconds()/window.Seconds()
	if elapsed < paceMinElapsed(window) {
		return thresholdColor(float64(usedPct))
	}
	switch projected := float64(usedPct) / elapsed; {
	case projected >= 100:
		return thresholdColor(100)
	case projected >= 95:
		return thresholdColor(70)
	default:
		return thresholdColor(0)
	}
}

// etaToEmptyPct projects time until usedPct reaches 100 at given burn rate.
func etaToEmptyPct(usedPct int, ratePctPerMin float64) time.Duration {
	if ratePctPerMin <= 0 || usedPct >= 100 {
		return 0
	}
	remaining := float64(100 - usedPct)
	mins := remaining / ratePctPerMin
	return time.Duration(mins * float64(time.Minute))
}

func totalTokens(u Usage) int {
	return u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens + u.OutputTokens
}

// contextBudget returns the token budget for a model. Exact match wins;
// otherwise the longest matching prefix wins so that variants like
// "claude-opus-4-7[1m]" don't fall back to the shorter "claude-opus-4-7"
// entry.
func contextBudget(model string) int {
	if v, ok := modelContextBudget[model]; ok {
		return v
	}
	bestLen := 0
	best := defaultContextBudget
	for k, v := range modelContextBudget {
		if strings.HasPrefix(model, k) && len(k) > bestLen {
			bestLen = len(k)
			best = v
		}
	}
	return best
}

// formatBudget renders a token count as "200k" or "1M".
func formatBudget(n int) string {
	if n >= 1_000_000 {
		if n%1_000_000 == 0 {
			return fmt.Sprintf("%dM", n/1_000_000)
		}
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

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

// refreshedRateLimitsPath re-resolves a path a panel picked at startup, so a
// long-lived head migrates off the fallback without being restarted.
//
// The window this closes: `hook ensure` claims the statusLine slot, abtop's
// shim stops writing, and a head starts before Claude Code's next statusline
// render — so at construction time neither our cache exists nor abtop's file
// is being refreshed. defaultRateLimitsPath pins that head to abtop's file,
// and it then shows the last numbers abtop ever wrote, frozen but perfectly
// confident, for the whole life of the pane. Stale meters are worse than
// absent ones.
//
// Once we are on our own cache (or an explicit CLAUDEMUX_RATE_LIMITS_PATH,
// which resolves to the same string) this is a no-op: there is nowhere better
// to migrate to, and a stat per tick for the rest of the process would buy
// nothing. Only a panel still holding the fallback pays for the re-resolve.
func refreshedRateLimitsPath(cur string) string {
	if cur == "" || cur == defaultStatuslineCachePath() {
		return cur
	}
	return defaultRateLimitsPath()
}
