package main

import "time"

const creditBurnWindow = 5 * time.Minute
const creditBurnMinHistory = 2 * time.Minute

type creditSpendSample struct {
	At    time.Time `json:"at"`
	Spent float64   `json:"spent"` // cumulative currency minor units
}

// Use cumulative credit spending, not balance changes: deposits, auto-reloads
// and expiring prepaid grants are not spending. Samples live in the shared
// balance cache, so heads and restarts use the same measurement history.
func recordCreditSpend(previous, next PrepaidCredits) PrepaidCredits {
	if !next.Enabled {
		return next
	}
	if next.Spent == nil || next.Currency == "" {
		next.Samples = previous.Samples
		return next
	}
	samples := previous.Samples
	if previous.Currency != next.Currency {
		samples = nil
	}
	if len(samples) > 0 {
		last := samples[len(samples)-1]
		gap := next.FetchedAt.Sub(last.At)
		if gap <= 0 || gap > 3*creditTTL || *next.Spent < last.Spent {
			samples = nil
		}
	}
	cutoff := next.FetchedAt.Add(-creditBurnWindow)
	next.Samples = make([]creditSpendSample, 0, len(samples)+1)
	for _, s := range samples {
		if !s.At.Before(cutoff) {
			next.Samples = append(next.Samples, s)
		}
	}
	next.Samples = append(next.Samples, creditSpendSample{At: next.FetchedAt, Spent: *next.Spent})
	return next
}

// The result is minor units per hour; formatting uses the billing currency's
// exponent. Zero is a measured idle rate, not missing history.
func creditSpendRate(c PrepaidCredits, now time.Time) (float64, bool) {
	if !creditFresh(c, now) || c.Spent == nil || len(c.Samples) < 2 {
		return 0, false
	}
	first, last := c.Samples[0], c.Samples[len(c.Samples)-1]
	span := last.At.Sub(first.At)
	if !last.At.Equal(c.FetchedAt) || span < creditBurnMinHistory || last.Spent < first.Spent {
		return 0, false
	}
	return (last.Spent - first.Spent) / span.Hours(), true
}
