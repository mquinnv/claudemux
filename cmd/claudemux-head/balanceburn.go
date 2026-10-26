package main

import (
	"encoding/json"
	"os"
	"time"
)

type creditBalanceSample struct {
	At     time.Time `json:"at"`
	Amount float64   `json:"amount"`
}

type creditBalanceHistory struct {
	Currency string                `json:"currency"`
	Samples  []creditBalanceSample `json:"samples"`
}

func creditBalanceAt(c PrepaidCredits) time.Time {
	if !c.BalanceAt.IsZero() {
		return c.BalanceAt
	}
	return c.FetchedAt
}

// This is a net-balance estimate. A detected top-up, currency change or long
// gap starts a new window rather than producing negative or misleading burn.
func recordCreditBalance(previous, next PrepaidCredits) PrepaidCredits {
	next.BalanceSamples = previous.BalanceSamples
	if !next.Enabled || next.Amount == nil || next.RefreshFailed || next.Currency == "" {
		return next
	}
	at := creditBalanceAt(next)
	if at.IsZero() {
		return next
	}
	samples := previous.BalanceSamples
	if previous.Currency != next.Currency {
		samples = nil
	}
	if len(samples) > 0 {
		last := samples[len(samples)-1]
		if !at.After(last.At) {
			return next
		}
		if at.Sub(last.At) > 3*creditTTL || *next.Amount > last.Amount {
			samples = nil
		}
	}
	next.BalanceSamples = make([]creditBalanceSample, 0, len(samples)+1)
	for _, s := range samples {
		if !s.At.Before(at.Add(-creditBurnWindow)) {
			next.BalanceSamples = append(next.BalanceSamples, s)
		}
	}
	next.BalanceSamples = append(next.BalanceSamples, creditBalanceSample{At: at, Amount: *next.Amount})
	return next
}

func creditBalanceRate(c PrepaidCredits, now time.Time) (float64, bool) {
	if c.RefreshFailed || c.Amount == nil || !creditFresh(PrepaidCredits{FetchedAt: creditBalanceAt(c)}, now) || len(c.BalanceSamples) < 2 {
		return 0, false
	}
	first, last := c.BalanceSamples[0], c.BalanceSamples[len(c.BalanceSamples)-1]
	span := last.At.Sub(first.At)
	if !last.At.Equal(creditBalanceAt(c)) || span < creditBurnMinHistory || first.Amount < last.Amount {
		return 0, false
	}
	return (first.Amount - last.Amount) / span.Hours(), true
}

func readCreditBalanceHistory(path string) creditBalanceHistory {
	var out creditBalanceHistory
	if blob, err := os.ReadFile(path + ".balance-history"); err == nil {
		_ = json.Unmarshal(blob, &out)
	}
	return out
}

// Keep history beside the cache, under its own lock. Older heads can refresh
// the balance JSON without deleting the new estimator's observations.
// This function never makes a network request.
func recordCreditBalanceHistory(path string, c PrepaidCredits) PrepaidCredits {
	history := readCreditBalanceHistory(path)
	if history.Currency == c.Currency {
		c.BalanceSamples = history.Samples
	}
	if !c.Enabled || c.Amount == nil || c.RefreshFailed {
		return c
	}
	at := creditBalanceAt(c)
	if len(c.BalanceSamples) > 0 && !at.After(c.BalanceSamples[len(c.BalanceSamples)-1].At) {
		return c
	}
	lock := path + ".balance-history.lock"
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if st, e := os.Stat(lock); e == nil && time.Since(st.ModTime()) > usageLockStale {
			_ = os.Remove(lock)
		}
		return c
	}
	f.Close()
	defer os.Remove(lock)
	history = readCreditBalanceHistory(path)
	out := recordCreditBalance(PrepaidCredits{Currency: history.Currency, BalanceSamples: history.Samples}, c)
	_ = writeJSONAtomic(path+".balance-history", creditBalanceHistory{Currency: out.Currency, Samples: out.BalanceSamples})
	return out
}
