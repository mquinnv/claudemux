package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func multiCfg(t *testing.T) (Config, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	bdir := filepath.Join(home, ".claude-bespoke")
	t.Setenv("CLAUDE_CONFIG_DIR", bdir)
	cfg := defaultConfig()
	cfg.Claude = ClaudeConfig{
		DefaultAccount: "ameriglide",
		Accounts: map[string]ClaudeAccount{
			"ameriglide": {},
			"bespoke":    {ConfigDir: bdir, Color: "blue"},
		},
	}
	return cfg, bdir
}

func TestHeadUsesAccountCaches(t *testing.T) {
	cfg, bdir := multiCfg(t)
	t.Setenv("CLAUDEMUX_RATE_LIMITS_PATH", "")
	m := newModel(cfg, filepath.Join(t.TempDir(), "x.jsonl"), "s", false)
	if m.account != "bespoke" {
		t.Fatalf("account = %q, want bespoke", m.account)
	}
	if m.accountColor != "#4d79ff" {
		t.Errorf("accountColor = %q, want #4d79ff", m.accountColor)
	}
	if !strings.HasSuffix(m.rateLimitsPath, "rate-limits-bespoke.json") {
		t.Errorf("rateLimitsPath = %q", m.rateLimitsPath)
	}
	if !strings.HasSuffix(m.usageCachePath, "usage-bespoke.json") {
		t.Errorf("usageCachePath = %q", m.usageCachePath)
	}
	if m.claudeConfigDir != bdir {
		t.Errorf("claudeConfigDir = %q, want %q", m.claudeConfigDir, bdir)
	}
}

func TestMetersLineAccountBadge(t *testing.T) {
	now := time.Now()
	base := model{
		width:  120,
		rateOK: true,
		rateLimits: RateLimits{
			FiveHour: Window{UsedPercent: 20, ResetsAt: now.Add(5 * time.Hour)},
			SevenDay: Window{UsedPercent: 30, ResetsAt: now.Add(3 * 24 * time.Hour)},
		},
	}
	single := renderMetersLine(base, now)
	if strings.Contains(ansi.Strip(single), "bespoke") {
		t.Fatalf("single mode shows a badge: %q", single)
	}

	multi := base
	multi.account, multi.accountColor = "bespoke", "#4d79ff"
	got := renderMetersLine(multi, now)
	plain := ansi.Strip(got)
	if !strings.Contains(plain, " bespoke ctx") {
		t.Errorf("multi line = %q, want badge before ctx", plain)
	}
	if w := ansi.StringWidth(got); w != base.width {
		t.Errorf("width = %d, want %d", w, base.width)
	}
	for w := 10; w <= 120; w++ {
		multi.width = w
		if lw := ansi.StringWidth(renderMetersLine(multi, now)); lw > w {
			t.Fatalf("width %d: line is %d wide", w, lw)
		}
	}
}

func TestAccountColor(t *testing.T) {
	cc := ClaudeConfig{Accounts: map[string]ClaudeAccount{
		"a": {Color: "purple"}, "b": {Color: "#ABCDEF"}, "c": {}, "d": {Color: "bogus"},
	}}
	if got := accountColor(cc, "a"); got != "#b34dff" {
		t.Errorf("named = %q", got)
	}
	if got := accountColor(cc, "b"); got != "#abcdef" {
		t.Errorf("hex = %q", got)
	}
	if accountColor(cc, "c") != accountColor(cc, "c") || accountColor(cc, "c") == "" {
		t.Errorf("hash fallback unstable")
	}
	if accountColor(cc, "d") != accountColor(ClaudeConfig{}, "d") {
		t.Errorf("invalid color should fall back to hash")
	}
}
