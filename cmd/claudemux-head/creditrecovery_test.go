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

func TestUsageRateLimitDoesNotHideSuccessfulBalance(t *testing.T) {
	now := time.Now()
	usageCalls, balanceCalls := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/oauth/usage" {
			usageCalls++
			w.WriteHeader(429)
			return
		}
		balanceCalls++
		fmt.Fprint(w, `{"amount":1234,"currency":"USD"}`)
	}))
	defer srv.Close()
	first, err := fetchPrepaidCreditStatus(context.Background(), srv.Client(), srv.URL, creditIdentity{Token: "test", Org: "org"}, now, time.Time{})
	if err == nil || first.Amount == nil || *first.Amount != 1234 || !first.UsageRetryAt.After(now) {
		t.Fatalf("partial result lost balance/cooldown: %+v %v", first, err)
	}
	next, _ := fetchPrepaidCreditStatus(context.Background(), srv.Client(), srv.URL, creditIdentity{Token: "test", Org: "org"}, now.Add(time.Minute), first.UsageRetryAt)
	if next.Amount == nil || usageCalls != 1 || balanceCalls != 2 {
		t.Fatalf("usage cooldown blocked balance or retried usage: %d/%d", usageCalls, balanceCalls)
	}
}

func TestCreditFailurePreservesLastSuccessfulBalance(t *testing.T) {
	now := time.Now()
	amount := 1234.0
	path := t.TempDir() + "/credits.json"
	previous := PrepaidCredits{Enabled: true, Amount: &amount, Currency: "USD", FetchedAt: now.Add(-time.Minute)}
	if err := writeJSONAtomic(path, previous); err != nil {
		t.Fatal(err)
	}
	got := refreshCreditCache(context.Background(), path, now, func(context.Context) (PrepaidCredits, error) { return PrepaidCredits{}, fmt.Errorf("timeout") })
	text := creditBalanceText(got, now)
	if got.Amount == nil || !strings.Contains(text, "last $12.34") || !strings.Contains(text, "retrying") || strings.Contains(text, "left") {
		t.Fatalf("last balance not labeled honestly: %q", text)
	}
	if cached := readCreditCache(path); cached.Amount == nil {
		t.Fatal("failure erased shared last balance")
	}
}

func TestPartialBalanceAndVersionedCacheRecoverAcrossViews(t *testing.T) {
	now := time.Now()
	amount := 1000.0
	dir := t.TempDir()
	legacy := dir + "/credits-account.json"
	path := dir + "/credits-v2-account.json"
	if err := writeJSONAtomic(legacy, PrepaidCredits{Enabled: true, Amount: &amount, Currency: "USD", FetchedAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	seed := readCreditCache(path)
	if !seed.Enabled || !seed.FetchedAt.IsZero() {
		t.Fatal("legacy cache did not seed availability and force refresh")
	}
	updated := 2500.0
	got := refreshCreditCache(context.Background(), path, now, func(context.Context) (PrepaidCredits, error) {
		return PrepaidCredits{Amount: &updated, Currency: "USD", FetchedAt: now}, creditHTTPStatus(429)
	})
	if got.Amount == nil || *got.Amount != updated || !got.Enabled || got.RefreshFailed {
		t.Fatalf("successful partial balance discarded: %+v", got)
	}
	if err := writeJSONAtomic(legacy, PrepaidCredits{Enabled: true, FetchedAt: now}); err != nil {
		t.Fatal(err)
	}
	if cached := readCreditCache(path); cached.Amount == nil || *cached.Amount != updated {
		t.Fatal("old head's failed refresh damaged new cache")
	}
}
