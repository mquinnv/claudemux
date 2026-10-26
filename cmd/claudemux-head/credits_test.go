package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestFetchPrepaidCredits(t *testing.T) {
	for _, tc := range []struct {
		body   string
		wantOK bool
		want   float64
	}{
		{`{"amount":1234,"currency":"USD"}`, true, 1234},
		{`{"amount":0,"currency":"GBP"}`, true, 0},
		{`{"currency":"USD"}`, false, 0},
		{`{"amount":1234,"currency":"bogus"}`, false, 0},
	} {
		t.Run(tc.body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("missing bearer")
				}
				switch r.URL.Path {
				case "/api/oauth/usage":
					fmt.Fprint(w, `{"extra_usage":{"is_enabled":true}}`)
				case "/api/oauth/organizations/test-org/prepaid/credits":
					fmt.Fprint(w, tc.body)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			got, err := fetchPrepaidCredits(context.Background(), srv.Client(), srv.URL, creditIdentity{Token: "test-token", Org: "test-org"}, time.Now())
			if (err == nil) != tc.wantOK {
				t.Fatalf("error = %v", err)
			}
			if tc.wantOK && (!got.Enabled || got.Amount == nil || *got.Amount != tc.want) {
				t.Fatalf("credits = %+v", got)
			}
		})
	}
}

func TestCreditGaugeReplacesOnlyExhaustedWindows(t *testing.T) {
	now := time.Now()
	amount := 1234.0
	for _, window := range []string{"5h", "wk", "model", "both"} {
		t.Run(window, func(t *testing.T) {
			m := model{rateOK: true, width: 180, rateLimits: RateLimits{
				FiveHour: Window{UsedPercent: 20, ResetsAt: now.Add(time.Hour)},
				SevenDay: Window{UsedPercent: 30, ResetsAt: now.Add(24 * time.Hour)},
			}, credits: PrepaidCredits{Enabled: true, Amount: &amount, Currency: "USD", FetchedAt: now}}
			if window == "5h" || window == "both" {
				m.rateLimits.FiveHour.UsedPercent = 100
			}
			if window == "wk" || window == "both" {
				m.rateLimits.SevenDay.UsedPercent = 100
			}
			if window == "model" {
				m.modelWindows = []ModelWindow{{Name: "Fable", UsedPercent: 100, ResetsAt: now.Add(time.Hour)}}
			}
			line := ansi.Strip(renderMetersLine(m, now))
			if strings.Count(line, "USAGE CREDITS") != 1 || !strings.Contains(line, "$12.34 left") {
				t.Fatalf("meters = %q", line)
			}
			if strings.Contains(line, "100%") || strings.Contains(line, "burn ") || strings.Contains(line, "empty in") {
				t.Fatalf("exhausted gauge still shown: %q", line)
			}
			if window == "5h" && !strings.Contains(line, "wk ") {
				t.Fatalf("unexhausted week lost: %q", line)
			}
			if window == "wk" && !strings.Contains(line, "5h ") {
				t.Fatalf("unexhausted 5h lost: %q", line)
			}
			m.width = 65
			if line = ansi.Strip(renderMetersLine(m, now)); !strings.Contains(line, "USAGE CREDITS") {
				t.Fatalf("credit indicator dropped on narrow head: %q", line)
			}
		})
	}
}

func TestCreditGaugeRequiresEnabledAndCurrentBalance(t *testing.T) {
	now := time.Now()
	amount := 500.0
	m := model{rateOK: true, rateLimits: RateLimits{FiveHour: Window{UsedPercent: 100, ResetsAt: now.Add(time.Hour)}}}
	m.credits = PrepaidCredits{Amount: &amount, Currency: "USD", FetchedAt: now}
	if s := strings.Join(rateGaugeParts(m, now, 10).parts, " "); strings.Contains(s, "USAGE CREDITS") {
		t.Fatalf("disabled credits shown: %q", s)
	}
	m.credits.Enabled = true
	m.credits.FetchedAt = now.Add(-10 * time.Minute)
	if s := strings.Join(rateGaugeParts(m, now, 10).parts, " "); strings.Contains(s, "$5.00") {
		t.Fatalf("stale balance shown as current: %q", s)
	}
	m.credits.FetchedAt = now
	m.credits.Amount = nil
	if s := strings.Join(rateGaugeParts(m, now, 10).parts, " "); !strings.Contains(s, "balance unavailable") {
		t.Fatalf("unknown balance not marked: %q", s)
	}
}

func TestCreditCacheRefreshAndFailure(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "credits.json")
	amount := 1234.0
	calls := 0
	fetch := func(context.Context) (PrepaidCredits, error) {
		calls++
		if calls > 1 {
			return PrepaidCredits{}, errors.New("offline")
		}
		return PrepaidCredits{Enabled: true, Amount: &amount, Currency: "USD", FetchedAt: now}, nil
	}
	first := refreshCreditCache(context.Background(), path, now, fetch)
	second := refreshCreditCache(context.Background(), path, now.Add(30*time.Second), fetch)
	if calls != 1 || first.Amount == nil || second.Amount == nil || *second.Amount != amount {
		t.Fatalf("cache did not share the answer: calls=%d, %+v", calls, second)
	}
	failed := refreshCreditCache(context.Background(), path, now.Add(creditTTL), fetch)
	if calls != 2 || failed.Amount != nil || !failed.Enabled {
		t.Fatalf("failure retained a balance or lost credit mode: calls=%d %+v", calls, failed)
	}
	_ = refreshCreditCache(context.Background(), path, now.Add(creditTTL+time.Second), fetch)
	if calls != 2 {
		t.Fatalf("failure did not back off: %d calls", calls)
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "token") {
		t.Fatal("cache must not carry credentials")
	}
}

func TestCreditCacheLockAndReset(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "credits.json")
	if err := os.WriteFile(path+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	_ = refreshCreditCache(context.Background(), path, now, func(context.Context) (PrepaidCredits, error) {
		t.Fatal("fetched while another head holds lock")
		return PrepaidCredits{}, nil
	})
	rl := RateLimits{FiveHour: Window{UsedPercent: 100, ResetsAt: now.Add(-time.Second)}}
	if exhaustedBudget(rl, nil, now) {
		t.Fatal("expired 100% window still enables credits")
	}
	rl.FiveHour.ResetsAt = now.Add(time.Hour)
	rl.FiveHour.Used = 99.6
	if exhaustedBudget(rl, nil, now) {
		t.Fatal("rounded 100% window falsely enables credits")
	}
}

func TestCreditModelExhaustionUsesExactPercentage(t *testing.T) {
	now := time.Now()
	for _, pct := range []float64{99.6, 100} {
		blob := []byte(fmt.Sprintf(`{"rate_limits_available":true,"rate_limits":{"limits":[{"kind":"weekly_scoped","percent":%g,"resets_at":%q,"scope":{"model":{"display_name":"Fable"}}}]}}`, pct, now.Add(time.Hour).Format(time.RFC3339)))
		u, err := parseUsageResponse(blob, now)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "usage.json")
		if err := writeUsageCache(path, u); err != nil {
			t.Fatal(err)
		}
		cached, _ := readUsageCache(path, now)
		if exhaustedBudget(RateLimits{}, cached.Models, now) != (pct >= 100) {
			t.Fatalf("model %g%% misclassified after cache roundtrip: %+v", pct, cached.Models)
		}
	}
}

func TestNarrowHeadPrioritizesCreditsOverContext(t *testing.T) {
	now := time.Now()
	amount := 1234.0
	m := model{width: 35, rateOK: true, rateLimits: RateLimits{FiveHour: Window{UsedPercent: 100, ResetsAt: now.Add(time.Hour)}}, credits: PrepaidCredits{Enabled: true, Amount: &amount, Currency: "USD", FetchedAt: now}}
	for name, line := range map[string]string{"split": renderMetersLine(m, now), "packed": renderStatusbar(m, now, "")} {
		if plain := ansi.Strip(line); !strings.Contains(plain, "USAGE CREDITS · $12.34 left") {
			t.Errorf("%s lost credits: %q", name, plain)
		}
	}
}

func TestCreditsStayInExhaustedMeterPosition(t *testing.T) {
	now := time.Now()
	amount := 1234.0
	m := model{width: 180, rateOK: true, rateLimits: RateLimits{
		FiveHour: Window{UsedPercent: 20, ResetsAt: now.Add(time.Hour)},
		SevenDay: Window{UsedPercent: 100, ResetsAt: now.Add(time.Hour)},
	}, modelWindows: []ModelWindow{{Name: "Fable", UsedPercent: 50, ResetsAt: now.Add(time.Hour)}}, credits: PrepaidCredits{Enabled: true, Amount: &amount, Currency: "USD", FetchedAt: now}}
	line := ansi.Strip(renderMetersLine(m, now))
	ctx, fh, credits, fable := strings.Index(line, "ctx "), strings.Index(line, "5h "), strings.Index(line, "USAGE CREDITS"), strings.Index(line, "fable ")
	if ctx < 0 || fh <= ctx || credits <= fh || fable <= credits {
		t.Fatalf("weekly slot moved: %q", line)
	}
}

func TestExplicitBearerCannotReadLocalCreditCache(t *testing.T) {
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "CLAUDE_LOCAL_OAUTH_API_BASE"} {
		t.Setenv(key, "")
	}
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":{"organizationUuid":"local-org"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(dir + "\x00local-org"))
	path := filepath.Join(dir, "claudemux", fmt.Sprintf("credits-%x.json", hash[:8]))
	amount := 1234.0
	if err := writeJSONAtomic(path, PrepaidCredits{Enabled: true, Amount: &amount, Currency: "USD", FetchedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := creditsCmd(true)().(creditMsg).credits; got.Amount == nil {
		t.Fatal("test setup did not expose cached local balance")
	}
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "explicit-bearer")
	if got := creditsCmd(true)().(creditMsg).credits; got.Amount != nil || got.Enabled {
		t.Fatal("explicit bearer session borrowed local subscriber balance")
	}
}

func TestCreditProfileUsesSelectedConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":{"organizationUuid":"selected-org"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	org, gotDir, err := creditProfile()
	if err != nil || org != "selected-org" || gotDir != dir {
		t.Fatalf("profile = %q, %q, %v", org, gotDir, err)
	}
}

func TestCreditDisabledDoesNotReadBalance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/oauth/usage" {
			t.Errorf("disabled credits requested %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"extra_usage":{"is_enabled":false}}`)
	}))
	defer srv.Close()
	got, err := fetchPrepaidCredits(context.Background(), srv.Client(), srv.URL, creditIdentity{Org: "org", Token: "test"}, time.Now())
	if err != nil || got.Enabled || got.Amount != nil {
		t.Fatalf("disabled credits = %+v, %v", got, err)
	}
}

func TestSwitchboardCreditGaugeAndUpdates(t *testing.T) {
	now := time.Now()
	amount := 2500.0
	m := swRateModel(now, 65)
	m.rateLimits.SevenDay.UsedPercent = 100
	updated, cmd := m.Update(creditMsg{PrepaidCredits{Enabled: true, Amount: &amount, Currency: "USD", FetchedAt: now}})
	if cmd != nil {
		t.Fatal("balance update started a second timer instead of using the limits poll")
	}
	line := ansi.Strip(swMetersLine(updated.(swModel), now))
	if !strings.Contains(line, "USAGE CREDITS · $25.00 left") || strings.Contains(line, "100%") {
		t.Fatalf("switchboard = %q", line)
	}
	head := model{}
	h, cmd := head.Update(creditMsg{PrepaidCredits{Enabled: true, Amount: &amount, Currency: "USD", FetchedAt: now}})
	if cmd != nil || h.(model).credits.Amount == nil {
		t.Fatal("head failed to store balance or started an extra timer")
	}
}

func TestCreditFetchStartsWithFirstExhaustedSnapshot(t *testing.T) {
	now := time.Now()
	rl := RateLimits{SevenDay: Window{UsedPercent: 100, ResetsAt: now.Add(time.Hour)}}
	m := model{}
	updated, _ := m.Update(dataMsg{time: now, rateLimits: rl})
	m = updated.(model)
	if !m.creditsLoading {
		t.Fatal("head did not start fetching as soon as rates arrived")
	}
	updated, _ = m.Update(dataMsg{time: now.Add(time.Second), rateLimits: rl})
	if !updated.(model).creditsLoading {
		t.Fatal("second head snapshot dropped in-flight fetch")
	}
	sw := swModel{}
	s, _ := sw.Update(swSnapshotMsg{at: now, rl: rl, err: errors.New("tmux unavailable")})
	if !s.(swModel).creditsLoading {
		t.Fatal("switchboard did not fetch credits when rates arrived")
	}
}

func TestUnavailableCreditResultKeepsModeWithoutOldBalance(t *testing.T) {
	now := time.Now()
	amount := 1000.0
	m := model{credits: PrepaidCredits{Enabled: true, Amount: &amount, Currency: "USD", FetchedAt: now}}
	u, _ := m.Update(creditMsg{})
	m = u.(model)
	if !m.credits.Enabled || m.credits.Amount != nil {
		t.Fatal("unavailable read restored percentage mode or kept old money")
	}
}

func TestCreditRefreshCadenceAndSingleFlight(t *testing.T) {
	now := time.Now()
	rl := RateLimits{SevenDay: Window{UsedPercent: 100, ResetsAt: now.Add(time.Hour)}}
	loading := false
	if startCreditRefresh(true, rl, nil, &loading, now.Add(-59*time.Second), now) != nil || loading {
		t.Fatal("refreshed inside minute")
	}
	if startCreditRefresh(true, rl, nil, &loading, now.Add(-time.Minute), now) == nil || !loading {
		t.Fatal("minute refresh was skipped")
	}
	if startCreditRefresh(true, rl, nil, &loading, time.Time{}, now) != nil {
		t.Fatal("overlapped an in-flight fetch")
	}
	knownOff := mergeCreditResult(PrepaidCredits{Enabled: true}, PrepaidCredits{FetchedAt: now}, now)
	if knownOff.Enabled {
		t.Fatal("a successful disabled verdict did not clear credit mode")
	}
}

func TestStartupLockLoserReadsWinningBalanceOnNextPoll(t *testing.T) {
	now := time.Now()
	amount := 500.0
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprint(stale), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credits.json")
			if stale {
				if err := writeJSONAtomic(path, PrepaidCredits{Enabled: true, Amount: &amount, Currency: "USD", FetchedAt: now.Add(-3 * time.Minute)}); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path+".lock", nil, 0600); err != nil {
				t.Fatal(err)
			}
			loser := refreshCreditCache(context.Background(), path, now, func(context.Context) (PrepaidCredits, error) {
				t.Fatal("lock loser fetched")
				return PrepaidCredits{}, nil
			})
			rl := RateLimits{SevenDay: Window{UsedPercent: 100, ResetsAt: now.Add(time.Hour)}}
			m := model{rateOK: true, rateLimits: rl}
			u, _ := m.Update(creditMsg{loser})
			m = u.(model)
			if startCreditRefresh(true, rl, nil, &m.creditsLoading, m.creditCheckedAt, now.Add(time.Second)) == nil {
				t.Fatal("head waited a minute after losing lock")
			}
			sw := swModel{}
			s, _ := sw.Update(creditMsg{loser})
			sw = s.(swModel)
			if startCreditRefresh(true, rl, nil, &sw.creditsLoading, sw.creditCheckedAt, now.Add(time.Second)) == nil {
				t.Fatal("switchboard waited a minute after losing lock")
			}
			winner := PrepaidCredits{Enabled: true, Amount: &amount, Currency: "USD", FetchedAt: now.Add(time.Second)}
			if err := writeJSONAtomic(path, winner); err != nil {
				t.Fatal(err)
			}
			got := refreshCreditCache(context.Background(), path, now.Add(2*time.Second), func(context.Context) (PrepaidCredits, error) {
				t.Fatal("fresh winning cache should avoid fetch")
				return PrepaidCredits{}, nil
			})
			u, _ = m.Update(creditMsg{got})
			if u.(model).credits.Amount == nil {
				t.Fatal("winning balance not picked up")
			}
		})
	}
}
