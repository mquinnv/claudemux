package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Amount is the prepaid balance in currency minor units, not the monthly
// spending allowance. A nil amount is unknown; zero is a real empty balance.
type PrepaidCredits struct {
	Enabled       bool                `json:"enabled"`
	Amount        *float64            `json:"amount,omitempty"`
	Currency      string              `json:"currency,omitempty"`
	FetchedAt     time.Time           `json:"fetched_at"`
	Spent         *float64            `json:"spent,omitempty"`
	Samples       []creditSpendSample `json:"spend_samples,omitempty"`
	BalanceAt     time.Time           `json:"balance_at,omitempty"`
	RefreshFailed bool                `json:"refresh_failed,omitempty"`
	UsageRetryAt  time.Time           `json:"usage_retry_at,omitempty"`
}

const creditTTL = time.Minute
const creditCacheReadTTL = 5 * time.Second

type creditIdentity struct{ Token, Org string }

// This endpoint is separate from /usage: monthly_limit - used_credits is
// spending headroom, and must never be presented as prepaid money remaining.
func fetchPrepaidCredits(ctx context.Context, client *http.Client, base string, id creditIdentity, now time.Time) (PrepaidCredits, error) {
	return fetchPrepaidCreditStatus(ctx, client, base, id, now, time.Time{})
}

type creditHTTPStatus int

func (s creditHTTPStatus) Error() string { return fmt.Sprintf("credits request status %d", s) }

func fetchPrepaidCreditStatus(ctx context.Context, client *http.Client, base string, id creditIdentity, now, usageRetryAt time.Time) (PrepaidCredits, error) {
	get := func(path string, dst any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return errors.New("invalid credits request")
		}
		req.Header.Set("Authorization", "Bearer "+id.Token)
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
		resp, err := client.Do(req)
		if err != nil {
			return errors.New("credits request failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return creditHTTPStatus(resp.StatusCode)
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(dst); err != nil {
			return errors.New("invalid credits response")
		}
		return nil
	}
	var usage struct {
		Extra *struct {
			Enabled  bool     `json:"is_enabled"`
			Spent    *float64 `json:"used_credits"`
			Currency string   `json:"currency"`
		} `json:"extra_usage"`
	}
	out := PrepaidCredits{FetchedAt: now}
	var usageErr error
	if usageRetryAt.After(now) {
		out.UsageRetryAt = usageRetryAt
		usageErr = errors.New("usage counter cooling down")
	} else {
		usageErr = get("/api/oauth/usage", &usage)
		var status creditHTTPStatus
		if errors.As(usageErr, &status) && status == 429 {
			out.UsageRetryAt = now.Add(5 * time.Minute)
		}
	}
	if usageErr == nil {
		if usage.Extra == nil {
			usageErr = errors.New("credit availability unknown")
		} else {
			out.Enabled = usage.Extra.Enabled
			if !out.Enabled {
				return out, nil
			}
		}
	}
	var balance struct {
		Amount   *float64 `json:"amount"`
		Currency string   `json:"currency"`
	}
	if err := get("/api/oauth/organizations/"+url.PathEscape(id.Org)+"/prepaid/credits", &balance); err != nil {
		return out, err
	}
	currency := strings.ToUpper(balance.Currency)
	if balance.Amount == nil || len(currency) != 3 || strings.IndexFunc(currency, func(r rune) bool { return r < 'A' || r > 'Z' }) >= 0 {
		return out, errors.New("invalid prepaid balance")
	}
	out.Amount = balance.Amount
	out.Currency = currency
	out.BalanceAt = now
	if usageErr != nil {
		return out, usageErr
	}
	if usage.Extra.Spent != nil && *usage.Extra.Spent >= 0 && (usage.Extra.Currency == "" || strings.EqualFold(usage.Extra.Currency, currency)) {
		out.Spent = usage.Extra.Spent
	}
	return out, nil
}

// Resolve the active Claude profile without ever persisting its bearer token.
// Custom profiles use their own config, secure store, and credit cache.
func creditProfile() (org, dir string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	dir = os.Getenv("CLAUDE_CONFIG_DIR")
	config := filepath.Join(home, ".claude.json")
	if dir == "" {
		dir = filepath.Join(home, ".claude")
	} else {
		config = filepath.Join(dir, ".claude.json")
	}
	var body struct {
		Account struct {
			Org string `json:"organizationUuid"`
		} `json:"oauthAccount"`
	}
	blob, err := os.ReadFile(config)
	if err != nil {
		return "", "", err
	}
	if err = json.Unmarshal(blob, &body); err != nil {
		return "", "", err
	}
	if body.Account.Org == "" {
		return "", "", errors.New("no Claude OAuth organization")
	}
	return body.Account.Org, dir, nil
}

func creditToken(ctx context.Context, dir string) (string, error) {
	storeDir, custom := os.LookupEnv("CLAUDE_SECURESTORAGE_CONFIG_DIR")
	if !custom {
		storeDir = dir
	} else if storeDir == "" {
		home, _ := os.UserHomeDir()
		storeDir = filepath.Join(home, ".claude")
	}
	var blob []byte
	if runtime.GOOS == "darwin" {
		service := "Claude Code-credentials"
		if custom && os.Getenv("CLAUDE_SECURESTORAGE_CONFIG_DIR") != "" || !custom && os.Getenv("CLAUDE_CONFIG_DIR") != "" {
			hash := sha256.Sum256([]byte(storeDir))
			service += fmt.Sprintf("-%x", hash[:4])
		}
		// Never put the token on an argument list or in diagnostics. The
		// keychain may be locked; the enclosing timeout bounds that wait.
		blob, _ = exec.CommandContext(ctx, "security", "find-generic-password", "-w", "-s", service).Output()
	}
	if len(blob) == 0 {
		blob, _ = os.ReadFile(filepath.Join(storeDir, ".credentials.json"))
	}
	var body struct {
		OAuth struct {
			Token   string  `json:"accessToken"`
			Expires float64 `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(blob, &body) != nil || body.OAuth.Token == "" {
		return "", errors.New("Claude OAuth token unavailable")
	}
	if body.OAuth.Expires > 0 && body.OAuth.Expires < float64(time.Now().UnixMilli()) {
		return "", errors.New("Claude OAuth token expired")
	}
	return body.OAuth.Token, nil
}

func readCreditCache(path string) PrepaidCredits {
	var out PrepaidCredits
	if blob, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(blob, &out)
	} else if os.IsNotExist(err) && strings.HasPrefix(filepath.Base(path), "credits-v2-") {
		// Seed only availability and the last known balance from older heads.
		// A separate version prevents old writers from deleting new history
		// or overwriting a healthy balance with an unavailable result.
		out = readCreditCache(filepath.Join(filepath.Dir(path), strings.Replace(filepath.Base(path), "credits-v2-", "credits-", 1)))
		if out.BalanceAt.IsZero() {
			out.BalanceAt = out.FetchedAt
		}
		out.FetchedAt = time.Time{}
		out.RefreshFailed = true
		out.Samples = nil
	}
	return out
}

func creditFresh(c PrepaidCredits, now time.Time) bool {
	return !c.FetchedAt.IsZero() && now.Sub(c.FetchedAt) >= 0 && now.Sub(c.FetchedAt) < 2*creditTTL
}

// Shared, profile-scoped single-flight cache. Failed reads cache an unknown
// amount too, preventing a fleet from hammering the endpoint or the keychain.
func refreshCreditCache(ctx context.Context, path string, now time.Time, fetch func(context.Context) (PrepaidCredits, error)) PrepaidCredits {
	previous := readCreditCache(path)
	if age := now.Sub(previous.FetchedAt); age >= 0 && age < creditTTL {
		return previous
	}
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return PrepaidCredits{}
	}
	lock := path + ".lock"
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if st, e := os.Stat(lock); e == nil && now.Sub(st.ModTime()) > usageLockStale {
			_ = os.Remove(lock)
		}
		return previous
	}
	f.Close()
	defer os.Remove(lock)
	// Another head may have populated the cache just before we got the lock.
	if latest := readCreditCache(path); now.Sub(latest.FetchedAt) >= 0 && now.Sub(latest.FetchedAt) < creditTTL {
		return latest
	}
	out, err := fetch(ctx)
	if err != nil {
		out.Spent = nil
		if out.UsageRetryAt.IsZero() && previous.UsageRetryAt.After(now) {
			out.UsageRetryAt = previous.UsageRetryAt
		}
		if out.Amount == nil {
			out.Amount = previous.Amount
			out.BalanceAt = previous.BalanceAt
			if out.BalanceAt.IsZero() {
				out.BalanceAt = previous.FetchedAt
			}
			out.RefreshFailed = true
		}
		if out.Currency == "" {
			out.Currency = previous.Currency
		}
		out.FetchedAt = now
		if !out.Enabled {
			out.Enabled = previous.Enabled
		}
	}
	if out.Amount != nil && out.BalanceAt.IsZero() {
		out.BalanceAt = out.FetchedAt
	}
	out = recordCreditSpend(previous, out)
	_ = writeJSONAtomic(path, out)
	return out
}

type creditMsg struct{ credits PrepaidCredits }

// Use the first exhausted limits snapshot, rather than a timer that can run
// before startup has loaded those limits. Read the shared cache frequently so
// all views converge; refreshCreditCache bounds real network reads by a minute.
func startCreditRefresh(rateOK bool, rl RateLimits, models []ModelWindow, loading *bool, checkedAt, now time.Time) tea.Cmd {
	if !rateOK || *loading || !exhaustedBudget(rl, models, now) {
		return nil
	}
	if age := now.Sub(checkedAt); !checkedAt.IsZero() && age >= 0 && age < creditCacheReadTTL {
		return nil
	}
	*loading = true
	return creditsCmd(true)
}

func mergeCreditResult(previous, next PrepaidCredits, now time.Time) PrepaidCredits {
	if next.FetchedAt.IsZero() && previous.Enabled {
		if previous.BalanceAt.IsZero() {
			previous.BalanceAt = previous.FetchedAt
		}
		previous.FetchedAt = now
		previous.RefreshFailed = true
		previous.Spent = nil
		return previous
	}
	return next
}

func creditReadStamp(c PrepaidCredits, now time.Time) time.Time {
	if creditFresh(c, now) {
		return now
	}
	return time.Time{} // a lost cache lock race must retry on the next poll
}

func exhaustedWindow(w Window, now time.Time) bool {
	return w.usedExact() >= 100 && w.ResetsAt.After(now)
}

func exhaustedBudget(rl RateLimits, models []ModelWindow, now time.Time) bool {
	if exhaustedWindow(rl.FiveHour, now) || exhaustedWindow(rl.SevenDay, now) {
		return true
	}
	for _, m := range models {
		if m.usedExact() >= 100 && m.ResetsAt.After(now) {
			return true
		}
	}
	return false
}

func creditsCmd(needed bool) tea.Cmd {
	return func() tea.Msg {
		if !needed {
			return creditMsg{}
		}
		// API keys and third-party providers must never borrow a local
		// subscriber's credentials to read their billing information.
		for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "CLAUDE_LOCAL_OAUTH_API_BASE"} {
			if os.Getenv(key) != "" {
				return creditMsg{}
			}
		}
		org, dir, err := creditProfile()
		if err != nil {
			return creditMsg{}
		}
		hash := sha256.Sum256([]byte(dir + "\x00" + org))
		cache := filepath.Join(dir, "claudemux", fmt.Sprintf("credits-v2-%x.json", hash[:8]))
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		out := refreshCreditCache(ctx, cache, time.Now(), func(ctx context.Context) (PrepaidCredits, error) {
			token, err := creditToken(ctx, dir)
			if err != nil {
				return PrepaidCredits{}, err
			}
			client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			return fetchPrepaidCreditStatus(ctx, client, "https://api.anthropic.com", creditIdentity{Token: token, Org: org}, time.Now(), readCreditCache(cache).UsageRetryAt)
		})
		return creditMsg{out}
	}
}

func creditBalanceText(c PrepaidCredits, now time.Time) string {
	if c.Amount == nil {
		return "USAGE CREDITS · balance unavailable"
	}
	balanceAt := c.BalanceAt
	if balanceAt.IsZero() {
		balanceAt = c.FetchedAt
	}
	if c.RefreshFailed || !creditFresh(PrepaidCredits{FetchedAt: balanceAt}, now) {
		return "USAGE CREDITS · last " + formatCreditMoney(*c.Amount, c.Currency) + " (" + formatDuration(now.Sub(balanceAt)) + " ago; retrying)"
	}
	text := "USAGE CREDITS · " + formatCreditMoney(*c.Amount, c.Currency) + " left"
	if rate, ok := creditSpendRate(c, now); ok {
		return text + " · " + formatCreditMoney(rate, c.Currency) + "/hr"
	}
	if c.Spent == nil {
		return text + " · rate unavailable"
	}
	unit := c.Currency
	if unit == "USD" {
		unit = "$"
	}
	return text + " · measuring " + unit + "/hr"
}

func formatCreditMoney(amount float64, currency string) string {
	// Claude's prepaid amount is in minor units; currencies without cents
	// use their ISO exponent. Keep the currency explicit outside USD.
	divisor := 100.0
	precision := 2
	switch currency {
	case "JPY", "KRW", "VND", "CLP", "XAF", "XOF", "XPF", "BIF", "DJF", "GNF", "KMF", "PYG", "RWF", "UGX", "VUV":
		divisor = 1
		precision = 0
	case "BHD", "JOD", "KWD", "OMR", "TND":
		divisor = 1000
		precision = 3
	}
	if currency == "USD" {
		return fmt.Sprintf("$%.2f", amount/divisor)
	}
	return fmt.Sprintf("%s %.*f", currency, precision, amount/divisor)
}

func creditGauges(rl RateLimits, models []ModelWindow, samples []pctSample, now time.Time, barW int, credits PrepaidCredits) gaugeSet {
	gs := rateGauges(rl, models, samples, now, barW)
	if !credits.Enabled || !exhaustedBudget(rl, models, now) {
		return gs
	}
	parts := []string{}
	bars := []bool{}
	creditIndex := -1
	for i, p := range gs.parts {
		if i == 1 || i >= gs.barred {
			continue
		} // burn and ETA are no longer meaningful
		exhausted := i == 0 && exhaustedWindow(rl.FiveHour, now) || i == 2 && exhaustedWindow(rl.SevenDay, now)
		if i >= 3 {
			exhausted = models[i-3].usedExact() >= 100 && models[i-3].ResetsAt.After(now)
		}
		if exhausted {
			if creditIndex < 0 {
				creditIndex = len(parts)
				style := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#9A4D00", Dark: "#FFD75F"})
				parts = append(parts, style.Render("[⚡ "+creditBalanceText(credits, now)+"]"))
				bars = append(bars, false)
			}
			continue
		}
		parts = append(parts, p)
		bars = append(bars, true)
	}
	return gaugeSet{parts: parts, barred: len(parts) - 1, bars: bars, creditIndex: creditIndex}
}
