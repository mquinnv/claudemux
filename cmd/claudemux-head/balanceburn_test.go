package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBalanceBurnDoesNotNeedUsageCounter(t *testing.T) {
	start := time.Now()
	c := PrepaidCredits{}
	for i := 0; i <= 2; i++ {
		amount := 10000 - float64(i)*10
		at := start.Add(time.Duration(i) * time.Minute)
		c = recordCreditBalance(c, PrepaidCredits{Enabled: true, Amount: &amount, Currency: "USD", FetchedAt: at, BalanceAt: at})
	}
	if rate, ok := creditBalanceRate(c, c.FetchedAt); !ok || rate != 600 {
		t.Fatalf("balance rate=%g %v", rate, ok)
	}
	if text := creditBalanceText(c, c.FetchedAt); !strings.Contains(text, "≈$6.00/hr") {
		t.Fatalf("missing estimate: %q", text)
	}
	topup := 15000.0
	next := c
	next.Amount = &topup
	next.BalanceAt = c.BalanceAt.Add(time.Minute)
	next.FetchedAt = next.BalanceAt
	next = recordCreditBalance(c, next)
	if _, ok := creditBalanceRate(next, next.FetchedAt); ok {
		t.Fatal("top-up did not reset estimate")
	}
}

func TestBalanceHistorySurvivesOlderCacheWriter(t *testing.T) {
	start := time.Now()
	path := t.TempDir() + "/credits.json"
	for i := 0; i <= 2; i++ {
		amount := 10000 - float64(i)*10
		at := start.Add(time.Duration(i) * time.Minute)
		c := PrepaidCredits{Enabled: true, Amount: &amount, Currency: "USD", FetchedAt: at, BalanceAt: at}
		c = recordCreditBalanceHistory(path, c)
		c.BalanceSamples = nil // simulate an older binary writing only fields it knows
		if err := writeJSONAtomic(path, c); err != nil {
			t.Fatal(err)
		}
	}
	c := readCreditCache(path)
	if rate, ok := creditBalanceRate(c, c.FetchedAt); !ok || rate != 600 {
		t.Fatalf("history lost across writers: %g %v", rate, ok)
	}
}

func TestUsageMetadataSuccessAlsoGetsLongCooldown(t *testing.T) {
	now := time.Now()
	usageCalls, balanceCalls := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/oauth/usage" {
			usageCalls++
			fmt.Fprint(w, `{"extra_usage":{"is_enabled":true,"used_credits":100,"currency":"USD"}}`)
			return
		}
		balanceCalls++
		fmt.Fprint(w, `{"amount":10000,"currency":"USD"}`)
	}))
	defer srv.Close()
	first, err := fetchPrepaidCreditStatus(context.Background(), srv.Client(), srv.URL, creditIdentity{Token: "test", Org: "org"}, now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 10; i++ {
		_, _ = fetchPrepaidCreditStatus(context.Background(), srv.Client(), srv.URL, creditIdentity{Token: "test", Org: "org"}, now.Add(time.Duration(i)*time.Minute), first.UsageRetryAt)
	}
	if usageCalls != 1 || balanceCalls != 11 {
		t.Fatalf("unexpected endpoint counts usage=%d balance=%d", usageCalls, balanceCalls)
	}
}
