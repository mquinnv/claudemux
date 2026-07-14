package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
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

// RateLimits is the in-memory shape of ~/.claude/abtop-rate-limits.json.
type RateLimits struct {
	Source    string
	UpdatedAt time.Time
	FiveHour  Window
	SevenDay  Window
}

type Window struct {
	UsedPercent int
	ResetsAt    time.Time
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

// readRateLimits parses the abtop-rate-limits.json cache file.
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
			ResetsAt:    time.Unix(raw.FiveHour.ResetsAt, 0),
		},
		SevenDay: Window{
			UsedPercent: int(math.Round(raw.SevenDay.UsedPercentage)),
			ResetsAt:    time.Unix(raw.SevenDay.ResetsAt, 0),
		},
	}, nil
}

// pctSample is a snapshot of five_hour.used_percentage at a point in time.
type pctSample struct {
	at  time.Time
	pct int
}

// burnRatePctPerMin returns percentage-points per minute over the last
// ~15 minutes. Returns 0 sentinel for "not enough data" (<2min span) or
// "rate is zero or negative" (idle / window resetting).
func burnRatePctPerMin(samples []pctSample, now time.Time) float64 {
	cutoff := now.Add(-15 * time.Minute)
	var earliest pctSample
	var latest pctSample
	earliestSet := false
	for _, s := range samples {
		if !s.at.After(cutoff) {
			continue
		}
		if !earliestSet || s.at.Before(earliest.at) {
			earliest = s
			earliestSet = true
		}
		if s.at.After(latest.at) {
			latest = s
		}
	}
	if !earliestSet {
		return 0
	}
	span := latest.at.Sub(earliest.at)
	if span < 2*time.Minute {
		return 0
	}
	delta := float64(latest.pct - earliest.pct)
	if delta <= 0 {
		return 0
	}
	return delta / span.Minutes()
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

// defaultRateLimitsPath returns the abtop statusline cache path. Override
// with CLAUDE_HEAD_RATE_LIMITS_PATH env var.
func defaultRateLimitsPath() string {
	if p := os.Getenv("CLAUDE_HEAD_RATE_LIMITS_PATH"); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home + "/.claude/abtop-rate-limits.json"
	}
	return ""
}
