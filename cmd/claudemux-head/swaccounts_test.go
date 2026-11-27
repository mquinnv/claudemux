package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// swTwoAccounts is a two-account `claude:` block with ameriglide as default.
func swTwoAccounts(t *testing.T) ClaudeConfig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDEMUX_RATE_LIMITS_PATH", "")
	return ClaudeConfig{
		DefaultAccount: "ameriglide",
		Accounts: map[string]ClaudeAccount{
			"ameriglide": {},
			"bespoke":    {ConfigDir: filepath.Join(home, ".claude-bespoke"), Color: "blue"},
		},
	}
}

func swAcctSnap(accounts ...string) swSnapshot {
	var s swSnapshot
	for i, a := range accounts {
		s.Sessions = append(s.Sessions, swSession{Name: "s" + string(rune('a'+i)), State: "Idle", Context: -1, Account: a})
	}
	return s
}

func TestSwMeterAccountsOnlyLive(t *testing.T) {
	cfg := swTwoAccounts(t)
	if got := swMeterAccounts(cfg, swAcctSnap("bespoke")); !slices.Equal(got, []string{"bespoke"}) {
		t.Errorf("bespoke only live = %v, want [bespoke]", got)
	}
	if got := swMeterAccounts(cfg, swAcctSnap("bespoke", "ameriglide")); !slices.Equal(got, []string{"ameriglide", "bespoke"}) {
		t.Errorf("both live = %v, want [ameriglide bespoke]", got)
	}
	if got := swMeterAccounts(cfg, swSnapshot{}); !slices.Equal(got, []string{"ameriglide"}) {
		t.Errorf("nothing live = %v, want the default account's row", got)
	}
	// A head that predates @claudemux_account publishes nothing: count it as
	// the default account. An unknown name is nobody's row.
	if got := swMeterAccounts(cfg, swAcctSnap("", "ghost")); !slices.Equal(got, []string{"ameriglide"}) {
		t.Errorf("unlabelled + unknown = %v, want [ameriglide]", got)
	}
	if got := swMeterAccounts(ClaudeConfig{}, swAcctSnap("bespoke")); !slices.Equal(got, []string{implicitAccount}) {
		t.Errorf("single mode = %v, want [%s]", got, implicitAccount)
	}
}

func swRL(five, week int) RateLimits {
	now := time.Now()
	return RateLimits{
		FiveHour: Window{UsedPercent: five, ResetsAt: now.Add(5 * time.Hour)},
		SevenDay: Window{UsedPercent: week, ResetsAt: now.Add(3 * 24 * time.Hour)},
	}
}

func TestSwMetersLinePerAccount(t *testing.T) {
	cfg := swTwoAccounts(t)
	m := newSwModelFor("", cfg)
	m.width, m.height = 140, 40
	snap := swAcctSnap("bespoke", "ameriglide")
	next, _ := m.Update(swSnapshotMsg{at: time.Now(), snap: snap,
		rl: map[string]RateLimits{"ameriglide": swRL(21, 31), "bespoke": swRL(72, 82)}})
	m = next.(swModel)
	lines := strings.Split(ansi.Strip(swMetersLine(m, time.Now())), "\n")
	if len(lines) != 2 {
		t.Fatalf("meters = %q, want two rows", lines)
	}
	if !strings.HasPrefix(lines[0], " ameriglide ") || !strings.Contains(lines[0], "21%") || !strings.Contains(lines[0], "31%") {
		t.Errorf("row 0 = %q, want ameriglide's own 21%%/31%%", lines[0])
	}
	if !strings.HasPrefix(lines[1], " bespoke ") || !strings.Contains(lines[1], "72%") || !strings.Contains(lines[1], "82%") {
		t.Errorf("row 1 = %q, want bespoke's own 72%%/82%%", lines[1])
	}
	// Badges are padded to one width so the gauges start in one column.
	if strings.Index(lines[0], "5h") != strings.Index(lines[1], "5h") {
		t.Errorf("gauges not aligned:\n%q\n%q", lines[0], lines[1])
	}
	for _, l := range lines {
		if w := lipgloss.Width(l); w > m.width {
			t.Errorf("row %q is %d wide, pane is %d", l, w, m.width)
		}
	}

	// One account's unreadable cache keeps its row as "meters unavailable"
	// and leaves the other's.
	next, _ = m.Update(swSnapshotMsg{at: time.Now(), snap: snap,
		rl:    map[string]RateLimits{"bespoke": swRL(73, 83)},
		rlErr: map[string]error{"ameriglide": errors.New("no such file")}})
	m = next.(swModel)
	lines = strings.Split(ansi.Strip(swMetersLine(m, time.Now())), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "ameriglide meters unavailable") ||
		!strings.Contains(lines[1], "bespoke") || !strings.Contains(lines[1], "73%") {
		t.Errorf("after ameriglide error, meters = %q, want ameriglide unavailable + bespoke's row", lines)
	}
	if m.meterRows() != 2 {
		t.Errorf("meterRows = %d, want 2", m.meterRows())
	}

	// Hidden: bespoke has data but no live session.
	next, _ = m.Update(swSnapshotMsg{at: time.Now(), snap: swAcctSnap("ameriglide"),
		rl: map[string]RateLimits{"ameriglide": swRL(22, 32), "bespoke": swRL(74, 84)}})
	m = next.(swModel)
	got := ansi.Strip(swMetersLine(m, time.Now()))
	if strings.Contains(got, "bespoke") || !strings.Contains(got, "ameriglide") {
		t.Errorf("idle bespoke must be hidden, meters = %q", got)
	}
}

func TestSwMetersLineSingleUnchanged(t *testing.T) {
	if got := swMetersLine(goldenMetersModel(goldenNow, 120), goldenNow); got != goldenMeters120 {
		t.Errorf("meters@120 =\n%q\nwant\n%q", got, goldenMeters120)
	}
	if got := swMetersLine(goldenMetersModel(goldenNow, 60), goldenNow); got != goldenMeters60 {
		t.Errorf("meters@60 =\n%q\nwant\n%q", got, goldenMeters60)
	}
	if got := goldenViewModel().View(); got != goldenView {
		t.Errorf("view =\n%q\nwant\n%q", got, goldenView)
	}
	// Sessions that carry an account change nothing in single mode.
	m := goldenViewModel()
	m.snap.Sessions[0].Account = implicitAccount
	if got := m.View(); got != goldenView {
		t.Errorf("view with account field =\n%q\nwant\n%q", got, goldenView)
	}
	v := buildWebFleetView(webTestSnapshot(), goldenRL(webGoldenNow), true, goldenWindows(webGoldenNow), webGoldenNow, webHeadline{})
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != goldenWebJSON {
		t.Errorf("web json =\n%s\nwant\n%s", b, goldenWebJSON)
	}
}

func TestSwSessionRowBadge(t *testing.T) {
	cfg := swTwoAccounts(t)
	m := goldenViewModel()
	m.snap.Sessions[0].Account = "bespoke"
	m.snap.Sessions[1].Account = "ameriglide"
	if v := m.View(); strings.Contains(v, "bespoke") || strings.Contains(v, "ameriglide") {
		t.Errorf("single mode row shows an account:\n%s", v)
	}
	m.claude = cfg
	v := m.View()
	var apiRow, webRow string
	for _, l := range strings.Split(ansi.Strip(v), "\n") {
		if lipgloss.Width(l) > m.width {
			t.Errorf("line %q is wider than the pane (%d)", l, m.width)
		}
		switch {
		case strings.Contains(l, " api "):
			apiRow = l
		case strings.Contains(l, " web "):
			webRow = l
		}
	}
	if !strings.Contains(apiRow, "bespoke") || !strings.Contains(webRow, "ameriglide") {
		t.Fatalf("multi rows lack badges:\napi=%q\nweb=%q", apiRow, webRow)
	}
	// The badge is a fleet-wide column, so the topic still starts in one place.
	if strings.Index(apiRow, "build fixes") < 0 || strings.Index(apiRow, "Idle") < 0 {
		t.Fatalf("api row = %q", apiRow)
	}
	if lipgloss.Width(apiRow[:strings.Index(apiRow, "Idle")]) != lipgloss.Width(webRow[:strings.Index(webRow, "Thinking")]) {
		t.Errorf("state column sheared:\n%q\n%q", apiRow, webRow)
	}
}

func TestSwPollParsesAccount(t *testing.T) {
	base := []string{"s", "Idle", "100", "10", "sum", "prm", "m", "", "", "", "", "", "d", "", "777", "", "", "a/b#3"}
	withAcct := strings.Join(append(slices.Clone(base), "bespoke"), "\t")
	old := slices.Clone(base)
	old[0] = "t"
	paneOut := "s\t%1\tclaudemux-head\ttopic\nt\t%2\tclaudemux-head\ttopic"
	snap := buildSwSnapshot(withAcct+"\n"+strings.Join(old, "\t"), paneOut, "", "")
	if len(snap.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(snap.Sessions))
	}
	for _, s := range snap.Sessions {
		switch s.Name {
		case "s":
			if s.Account != "bespoke" || s.PRNumber != 3 {
				t.Errorf("19-field line: %+v", s)
			}
		case "t":
			if s.Account != "" || s.PRNumber != 3 {
				t.Errorf("18-field line: %+v", s)
			}
		}
	}
	// tmux options hold anything; a malformed name is no account.
	bad := strings.Join(append(slices.Clone(base), "Not An Account"), "\t")
	if s := buildSwSnapshot(bad, "s\t%1\tclaudemux-head\ttopic", "", ""); len(s.Sessions) != 1 || s.Sessions[0].Account != "" {
		t.Errorf("malformed account parsed: %+v", s.Sessions)
	}
}

func TestSwHeaderGrowsWithMeterRows(t *testing.T) {
	cfg := swTwoAccounts(t)
	m := newSwModelFor("", cfg)
	m.width, m.height = 140, 14
	var accts []string
	for i := 0; i < 20; i++ {
		accts = append(accts, []string{"ameriglide", "bespoke"}[i%2])
	}
	next, _ := m.Update(swSnapshotMsg{at: time.Now(), snap: swAcctSnap(accts...),
		rl: map[string]RateLimits{"ameriglide": swRL(21, 31), "bespoke": swRL(72, 82)}})
	m = next.(swModel)
	for _, hidden := range []bool{true, false} {
		m.previewHidden = hidden
		if n := strings.Count(m.View(), "\n") + 1; n > m.height {
			t.Errorf("previewHidden=%v: view is %d lines, pane is %d", hidden, n, m.height)
		}
	}
}

func TestSwUsageSpawnOnlyForShownAccounts(t *testing.T) {
	cfg := swTwoAccounts(t)
	m := newSwModelFor("", cfg)
	next, _ := m.Update(swSnapshotMsg{at: time.Now(), snap: swAcctSnap("ameriglide"),
		rl: map[string]RateLimits{"ameriglide": swRL(21, 31), "bespoke": swRL(72, 82)}})
	m = next.(swModel)
	now := time.Now()
	if !m.usageAllowed("ameriglide", now) {
		t.Error("shown account may not spawn")
	}
	if m.usageAllowed("bespoke", now) {
		t.Error("hidden account may spawn")
	}
	if m.others["bespoke"] == nil || m.others["bespoke"].usageCachePath == m.usageCachePath {
		t.Errorf("bespoke must have its own usage cache, got %+v", m.others["bespoke"])
	}
}

func TestWebFleetAccountsMulti(t *testing.T) {
	snap := webTestSnapshot()
	snap.Sessions[0].Account = "bespoke"
	accts := []webAccountFacts{
		{Name: "ameriglide", Color: "#ff4d4d", RL: goldenRL(webGoldenNow), RLOK: true},
		{Name: "bespoke", Color: "#4d79ff", RL: swRL(70, 80), RLOK: true, Windows: goldenWindows(webGoldenNow)},
	}
	v := buildWebFleetView(snap, goldenRL(webGoldenNow), true, nil, webGoldenNow, webHeadline{}, accts...)
	b, _ := json.Marshal(v)
	var raw struct {
		Budget   map[string]any `json:"budget"`
		Accounts []struct {
			Name     string         `json:"name"`
			Color    string         `json:"color"`
			FiveHour map[string]any `json:"five_hour"`
			Models   []any          `json:"models"`
		} `json:"accounts"`
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Budget == nil {
		t.Error("legacy budget must stay for old pages")
	}
	if len(raw.Accounts) != 2 || raw.Accounts[0].Name != "ameriglide" || raw.Accounts[1].Color != "#4d79ff" {
		t.Fatalf("accounts = %+v", raw.Accounts)
	}
	if raw.Accounts[1].FiveHour["used_pct"] != float64(70) || len(raw.Accounts[1].Models) != 1 {
		t.Errorf("bespoke meters = %+v", raw.Accounts[1])
	}
	if raw.Sessions[0]["account"] != "bespoke" || raw.Sessions[0]["account_color"] != "#4d79ff" {
		t.Errorf("session account = %v / %v", raw.Sessions[0]["account"], raw.Sessions[0]["account_color"])
	}
	if _, ok := raw.Sessions[1]["account"]; ok {
		t.Error("a session without an account must omit it")
	}
}

// swBoolRows is a meter row count from the old has-meters flag.
func swBoolRows(b bool) int {
	if b {
		return 1
	}
	return 0
}
