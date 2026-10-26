package main

import (
	"strings"
	"testing"
	"time"
)

func TestCreditBurnUsesSpendCounterAndIgnoresTopUps(t *testing.T) {
	start := time.Now()
	c := PrepaidCredits{}
	for i := 0; i <= 3; i++ {
		spent := 1000 + float64(i)*10
		balance := 5000 - float64(i)*10
		if i == 3 {
			balance += 5000
		}
		c = recordCreditSpend(c, PrepaidCredits{Enabled: true, Spent: &spent, Amount: &balance, Currency: "USD", FetchedAt: start.Add(time.Duration(i) * time.Minute)})
		if rate, ok := creditSpendRate(c, c.FetchedAt); i < 2 {
			if ok {
				t.Fatal("rate shown before two minutes of history")
			}
		} else if !ok || rate != 600 {
			t.Fatalf("burn = %g, %v; want 600 cents/hour", rate, ok)
		}
	}
	if text := creditBalanceText(c, c.FetchedAt); !strings.Contains(text, "$99.70 left") || !strings.Contains(text, "$6.00/hr") {
		t.Fatalf("credit text = %q", text)
	}
}

func TestCreditBurnResetsAndIdle(t *testing.T) {
	start := time.Now()
	amount := 5000.0
	spent := 1000.0
	c := PrepaidCredits{}
	for i := 0; i <= 2; i++ {
		c = recordCreditSpend(c, PrepaidCredits{Enabled: true, Spent: &spent, Amount: &amount, Currency: "USD", FetchedAt: start.Add(time.Duration(i) * time.Minute)})
	}
	if rate, ok := creditSpendRate(c, c.FetchedAt); !ok || rate != 0 {
		t.Fatalf("idle rate = %g, %v", rate, ok)
	}
	for _, kind := range []string{"counter reset", "currency change", "long gap"} {
		t.Run(kind, func(t *testing.T) {
			next := c
			next.FetchedAt = c.FetchedAt.Add(time.Minute)
			switch kind {
			case "counter reset":
				zero := 0.0
				next.Spent = &zero
			case "currency change":
				next.Currency = "GBP"
			case "long gap":
				next.FetchedAt = c.FetchedAt.Add(10 * time.Minute)
			}
			next = recordCreditSpend(c, next)
			if _, ok := creditSpendRate(next, next.FetchedAt); ok {
				t.Fatal("rate survived measurement reset")
			}
		})
	}
}

func TestCreditBurnSurvivesSharedCacheAndExpires(t *testing.T) {
	start := time.Now()
	amount := 1000.0
	c := PrepaidCredits{}
	for i := 0; i <= 2; i++ {
		spent := float64(i) * 20
		c = recordCreditSpend(c, PrepaidCredits{Enabled: true, Spent: &spent, Amount: &amount, Currency: "USD", FetchedAt: start.Add(time.Duration(i) * time.Minute)})
	}
	path := t.TempDir() + "/credits.json"
	if err := writeJSONAtomic(path, c); err != nil {
		t.Fatal(err)
	}
	c = readCreditCache(path)
	if rate, ok := creditSpendRate(c, c.FetchedAt); !ok || rate != 1200 {
		t.Fatalf("cached burn = %g, %v", rate, ok)
	}
	if _, ok := creditSpendRate(c, c.FetchedAt.Add(3*time.Minute)); ok {
		t.Fatal("stale rate shown as current")
	}
}
