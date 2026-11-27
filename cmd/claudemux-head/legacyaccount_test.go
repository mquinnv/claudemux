package main

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// swDeployAccounts is the deployment shape: the default account has its own
// config dir and the other one stays in ~/.claude. Sessions opened before
// accounts existed carry no account and run on ~/.claude, i.e. on bespoke.
func swDeployAccounts(t *testing.T) ClaudeConfig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDEMUX_RATE_LIMITS_PATH", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return ClaudeConfig{
		DefaultAccount: "ameriglide",
		Accounts: map[string]ClaudeAccount{
			"ameriglide": {ConfigDir: filepath.Join(home, ".claude-accounts", "ameriglide")},
			"bespoke":    {},
		},
	}
}

// argAfter returns the element after flag in args, exact-element matching.
func argAfter(args []string, flag string) (string, bool) {
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		return "", false
	}
	return args[i+1], true
}

func TestRestoreArgsAccount(t *testing.T) {
	exists := func(p string) bool { return p != "/gone" }
	multi := swDeployAccounts(t)
	l := lostRec("r", "abc", "/p/remix", "/p/remix", 1, false)

	// A recorded account is passed as is, in either mode.
	l.Rec.Account = "ameriglide"
	args, err := restoreArgs(l, multi, exists)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"-d", "-W", "-N", "r", "-r", "abc", "-a", "ameriglide", "--", "/p/remix"}; !slices.Equal(args, want) {
		t.Errorf("args = %q, want %q", args, want)
	}

	// Multi mode: an account-less (pre-accounts) record ran on ~/.claude, so
	// it restores on the account with no config_dir, never the default.
	l.Rec.Account = ""
	args, err = restoreArgs(l, multi, exists)
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := argAfter(args, "-a"); !ok || a != "bespoke" {
		t.Errorf("legacy record args = %q, want -a bespoke", args)
	}
	if args[len(args)-2] != "--" || args[len(args)-1] != "/p/remix" {
		t.Errorf("-a must come before --: %q", args)
	}

	// Multi mode with no ~/.claude account: an error, never a silent default.
	noHome := ClaudeConfig{DefaultAccount: "a", Accounts: map[string]ClaudeAccount{
		"a": {ConfigDir: "/x/a"}, "b": {ConfigDir: "/x/b"}}}
	if _, err := restoreArgs(l, noHome, exists); err == nil || !strings.Contains(err.Error(), "~/.claude") {
		t.Errorf("no ~/.claude account: err = %v, want one naming ~/.claude", err)
	}

	// Single mode: unchanged, no -a for an account-less record.
	args, err = restoreArgs(l, ClaudeConfig{}, exists)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(args, "-a") {
		t.Errorf("single mode must not pass -a: %q", args)
	}
	// One configured account is single mode too.
	one := ClaudeConfig{DefaultAccount: "solo", Accounts: map[string]ClaudeAccount{"solo": {}}}
	if args, _ := restoreArgs(l, one, exists); slices.Contains(args, "-a") {
		t.Errorf("one-account mode must not pass -a: %q", args)
	}
}

func TestSessionAccountMapping(t *testing.T) {
	cfg := swDeployAccounts(t)
	for in, want := range map[string]string{
		"":           "bespoke",
		"bespoke":    "bespoke",
		"ameriglide": "ameriglide",
		"ghost":      "",
	} {
		if got := cfg.SessionAccount(in); got != want {
			t.Errorf("SessionAccount(%q) = %q, want %q", in, got, want)
		}
	}
	// No account without config_dir: the default stands in for "".
	noHome := ClaudeConfig{DefaultAccount: "a", Accounts: map[string]ClaudeAccount{
		"a": {ConfigDir: "/x/a"}, "b": {ConfigDir: "/x/b"}}}
	if got := noHome.SessionAccount(""); got != "a" {
		t.Errorf("no ~/.claude account: SessionAccount(\"\") = %q, want default a", got)
	}
	if got := (ClaudeConfig{}).SessionAccount(""); got != implicitAccount {
		t.Errorf("single: SessionAccount(\"\") = %q, want %q", got, implicitAccount)
	}
}

// The head records its own (env-derived) account when the tmux option is
// empty, in multi mode only.
func TestHeadRecordsLegacyAccount(t *testing.T) {
	cfg := swDeployAccounts(t)
	m := newModel(Config{Claude: cfg}, filepath.Join(t.TempDir(), "x.jsonl"), "sid", false)
	if r := m.sessionRecordFor(time.Now()); r.Account != "bespoke" {
		t.Errorf("multi, no CLAUDE_CONFIG_DIR: record account = %q, want bespoke", r.Account)
	}
	r, ok := mergeTmuxRecord(m.sessionRecordFor(time.Now()), "s\t/p\t\t\n")
	if !ok || r.Account != "bespoke" {
		t.Errorf("empty @claudemux_account: account = %q, want bespoke kept", r.Account)
	}
	r, ok = mergeTmuxRecord(m.sessionRecordFor(time.Now()), "s\t/p\t\tameriglide\n")
	if !ok || r.Account != "ameriglide" || r.SessionName != "s" || r.LaunchDir != "/p" {
		t.Errorf("tmux account must win: %+v", r)
	}

	single := newModel(Config{}, filepath.Join(t.TempDir(), "x.jsonl"), "sid", false)
	r, _ = mergeTmuxRecord(single.sessionRecordFor(time.Now()), "s\t/p\t\t\n")
	if r.Account != "" {
		t.Errorf("single mode record account = %q, want empty", r.Account)
	}
}

// After install every live session is account-less: the lobby must read them
// as the ~/.claude account (bespoke), not the default.
func TestSwLegacySessionsAreHomeAccount(t *testing.T) {
	cfg := swDeployAccounts(t)
	if got := swMeterAccounts(cfg, swAcctSnap("", "")); !slices.Equal(got, []string{"bespoke"}) {
		t.Errorf("legacy sessions meter rows = %v, want [bespoke]", got)
	}
	if got := swMeterAccounts(cfg, swAcctSnap("", "ameriglide", "ghost")); !slices.Equal(got, []string{"ameriglide", "bespoke"}) {
		t.Errorf("mixed = %v, want [ameriglide bespoke]", got)
	}
	m := newSwModelFor("", cfg)
	if got := m.rowAccount(swSession{}); got != "bespoke" {
		t.Errorf("rowAccount(legacy) = %q, want bespoke", got)
	}
	if got := m.rowAccount(swSession{Account: "ghost"}); got != "" {
		t.Errorf("rowAccount(unknown) = %q, want none", got)
	}
	if got := newSwModelFor("", ClaudeConfig{}).rowAccount(swSession{}); got != "" {
		t.Errorf("single rowAccount = %q, want none", got)
	}
}

func TestSwWebBadgesEveryAccount(t *testing.T) {
	cfg := swDeployAccounts(t)
	m := newSwModelFor("", cfg)
	m.width, m.height = 140, 40
	// bespoke's cache reads, ameriglide's does not exist yet.
	snap := swAcctSnap("", "ameriglide")
	next, _ := m.Update(swSnapshotMsg{at: time.Now(), snap: snap,
		rl:    map[string]RateLimits{"bespoke": swRL(40, 50)},
		rlErr: map[string]error{"ameriglide": errors.New("no such file")}})
	m = next.(swModel)
	v := buildWebFleetView(m.webSnapshot(), m.rateLimits, m.rateOK, m.modelWindows, time.Now(), webHeadline{}, m.webAccounts()...)
	if v.Sessions[0].Account != "bespoke" || v.Sessions[0].AccountColor != accountColor(cfg, "bespoke") {
		t.Errorf("legacy session badge = %q %q, want bespoke", v.Sessions[0].Account, v.Sessions[0].AccountColor)
	}
	if v.Sessions[1].Account != "ameriglide" || v.Sessions[1].AccountColor != accountColor(cfg, "ameriglide") {
		t.Errorf("ameriglide session badge = %q %q, want ameriglide despite its unreadable cache", v.Sessions[1].Account, v.Sessions[1].AccountColor)
	}
	var names []string
	for _, a := range v.Accounts {
		names = append(names, a.Name)
		if a.Name == "ameriglide" && a.webBudget != nil {
			t.Error("ameriglide row must carry no budget while its cache is unreadable")
		}
		if a.Name == "bespoke" && a.webBudget == nil {
			t.Error("bespoke row lost its budget")
		}
	}
	if !slices.Equal(names, []string{"ameriglide", "bespoke"}) {
		t.Errorf("web account rows = %v, want the lobby's [ameriglide bespoke]", names)
	}
	// The lobby's own snapshot is left alone.
	if m.snap.Sessions[0].Account != "" {
		t.Error("webSnapshot mutated the lobby snapshot")
	}
	// Only badges: an account with no live session gets a colour, no row.
	next, _ = m.Update(swSnapshotMsg{at: time.Now(), snap: swAcctSnap(""),
		rl: map[string]RateLimits{"bespoke": swRL(40, 50)}, rlErr: map[string]error{"ameriglide": errors.New("x")}})
	m = next.(swModel)
	v = buildWebFleetView(m.webSnapshot(), m.rateLimits, m.rateOK, m.modelWindows, time.Now(), webHeadline{}, m.webAccounts()...)
	if len(v.Accounts) != 1 || v.Accounts[0].Name != "bespoke" {
		t.Errorf("rows = %+v, want bespoke only", v.Accounts)
	}
	// Single mode: no accounts, snapshot as is.
	sm := newSwModelFor("", ClaudeConfig{})
	sm.snap = swAcctSnap("")
	if sm.webAccounts() != nil || sm.webSnapshot().Sessions[0].Account != "" {
		t.Error("single mode web must be unchanged")
	}
}

func TestSwMetersUnavailableRow(t *testing.T) {
	cfg := swDeployAccounts(t)
	m := newSwModelFor("", cfg)
	m.width, m.height = 140, 40
	next, _ := m.Update(swSnapshotMsg{at: time.Now(), snap: swAcctSnap("", "ameriglide"),
		rl:    map[string]RateLimits{"bespoke": swRL(40, 50)},
		rlErr: map[string]error{"ameriglide": errors.New("no such file")}})
	m = next.(swModel)
	lines := strings.Split(ansi.Strip(swMetersLine(m, time.Now())), "\n")
	if len(lines) != 2 {
		t.Fatalf("meters = %q, want two rows", lines)
	}
	if !strings.HasPrefix(lines[0], " ameriglide ") || !strings.Contains(lines[0], "meters unavailable") {
		t.Errorf("row 0 = %q, want ameriglide meters unavailable", lines[0])
	}
	if !strings.HasPrefix(lines[1], " bespoke ") || !strings.Contains(lines[1], "40%") {
		t.Errorf("row 1 = %q, want bespoke's 40%%", lines[1])
	}
	if m.meterRows() != 2 {
		t.Errorf("meterRows = %d, want 2 (the unavailable row counts)", m.meterRows())
	}
	if n := strings.Count(m.View(), "\n") + 1; n > m.height {
		t.Errorf("view is %d lines, pane is %d", n, m.height)
	}
	// Only the default's row and nothing readable: still one row, unavailable.
	m2 := newSwModelFor("", cfg)
	m2.width, m2.height = 140, 40
	next, _ = m2.Update(swSnapshotMsg{at: time.Now(), snap: swSnapshot{},
		rlErr: map[string]error{"ameriglide": errors.New("x"), "bespoke": errors.New("x")}})
	m2 = next.(swModel)
	if got := ansi.Strip(swMetersLine(m2, time.Now())); !strings.Contains(got, "ameriglide") || !strings.Contains(got, "meters unavailable") || m2.meterRows() != 1 {
		t.Errorf("empty fleet meters = %q rows %d", got, m2.meterRows())
	}
}
