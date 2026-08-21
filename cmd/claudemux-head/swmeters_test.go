package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// swRateModel builds a swModel with live rate-limit data and enough burn-rate
// samples to produce the "empty in X" ETA, mirroring the head's meters tests.
func swRateModel(now time.Time, width int) swModel {
	return swModel{
		width:  width,
		height: 40,
		rateOK: true,
		rateLimits: RateLimits{
			FiveHour: Window{UsedPercent: 20, ResetsAt: now.Add(5 * time.Hour)},
			SevenDay: Window{UsedPercent: 30, ResetsAt: now.Add(3 * 24 * time.Hour)},
		},
		pctSamples: []pctSample{
			{at: now.Add(-10 * time.Minute), pct: 10},
			{at: now, pct: 20},
		},
	}
}

// swMetersLine mirrors the head's meters line: as width shrinks it drops
// gauges from the end in the head's order — eta, then wk — and 5h never drops.
func TestSwMetersLineDropsGauges(t *testing.T) {
	now := time.Now()
	m := swRateModel(now, 200)
	full := swMetersLine(m, now)
	for _, want := range []string{"5h", "wk", "empty in", "%→"} {
		if !strings.Contains(full, want) {
			t.Fatalf("full meters line = %q, want it to contain %q", full, want)
		}
	}

	fullW := lipgloss.Width(full)
	fhFloor := lipgloss.Width(rateGauges(m.rateLimits, nil, nil, now, defaultBarW).parts[0]) + 2
	sawEtaDrop, sawWkDrop := false, false
	for w := fullW; w >= fhFloor; w-- {
		m.width = w
		line := swMetersLine(m, now)
		if !strings.Contains(line, "5h") {
			t.Fatalf("at width %d, 5h gauge dropped: %q", w, line)
		}
		if !sawEtaDrop && !strings.Contains(line, "empty in") {
			sawEtaDrop = true
			if !strings.Contains(line, "wk") {
				t.Fatalf("at width %d, eta dropped but wk also gone: %q", w, line)
			}
		}
		if sawEtaDrop && !sawWkDrop && !strings.Contains(line, "wk") {
			sawWkDrop = true
			if strings.Contains(line, "empty in") {
				t.Fatalf("at width %d, wk dropped but eta still present: %q", w, line)
			}
		}
	}
	if !sawEtaDrop {
		t.Fatal("never observed eta being dropped as width shrank")
	}
	if !sawWkDrop {
		t.Fatal("never observed wk being dropped as width shrank")
	}
}

// The meters line owns its whole line, so its two bars widen to consume the
// leftover columns rather than leaving a fixed 10-cell bar adrift on a wide
// pane. Slack splits across the two bar-carrying gauges only, so at most a
// couple of columns may go unspent.
func TestSwMetersLineWidensBarsToFillPane(t *testing.T) {
	now := time.Now()
	m := swRateModel(now, 0)

	barCells := func(s string) int {
		n := 0
		for _, r := range s {
			if r == '█' || r == '░' {
				n++
			}
		}
		return n
	}

	var prevBars int
	for _, w := range []int{80, 100, 120, 160, 200} {
		m.width = w
		line := swMetersLine(m, now)
		content := len(" ") + lipgloss.Width(strings.TrimRight(ansi.Strip(line), " "))
		if unspent := w - content; unspent > 3 {
			t.Errorf("at width %d, %d columns left unspent (want <= 3): %q", w, unspent, line)
		}
		if bars := barCells(line); bars <= prevBars {
			t.Errorf("at width %d, total bar cells = %d, want more than %d at the previous width", w, bars, prevBars)
		} else {
			prevBars = bars
		}
	}
}

// A snapshot message carries the rate limits into the model — and keeps
// carrying them on polls where tmux failed, since the meters describe the
// account, not the fleet. A failed cache read hides the meters (rateOK false)
// until the next good read.
func TestSwUpdateStoresRateLimits(t *testing.T) {
	now := time.Now()
	rl := RateLimits{
		FiveHour: Window{UsedPercent: 42, ResetsAt: now.Add(2 * time.Hour)},
		SevenDay: Window{UsedPercent: 7, ResetsAt: now.Add(6 * 24 * time.Hour)},
	}
	m := newSwModel("%1")

	next, _ := m.Update(swSnapshotMsg{at: now, rl: rl})
	m = next.(swModel)
	if !m.rateOK || m.rateLimits.FiveHour.UsedPercent != 42 {
		t.Fatalf("after good poll: rateOK=%v fiveHour=%d, want true/42", m.rateOK, m.rateLimits.FiveHour.UsedPercent)
	}
	if len(m.pctSamples) != 1 || m.pctSamples[0].pct != 42 {
		t.Fatalf("after good poll: pctSamples = %+v, want one sample at 42", m.pctSamples)
	}

	// Same percentage again: no duplicate sample.
	next, _ = m.Update(swSnapshotMsg{at: now.Add(time.Second), rl: rl})
	m = next.(swModel)
	if len(m.pctSamples) != 1 {
		t.Fatalf("unchanged pct grew samples: %+v", m.pctSamples)
	}

	// tmux failed but the cache read worked: meters still update.
	rl.FiveHour.UsedPercent = 50
	next, _ = m.Update(swSnapshotMsg{at: now.Add(2 * time.Second), rl: rl, err: errors.New("tmux wedged")})
	m = next.(swModel)
	if m.rateLimits.FiveHour.UsedPercent != 50 || len(m.pctSamples) != 2 {
		t.Fatalf("tmux-error poll dropped rate limits: fiveHour=%d samples=%+v", m.rateLimits.FiveHour.UsedPercent, m.pctSamples)
	}

	// Cache read failed: meters hide, samples stay for the next good read.
	next, _ = m.Update(swSnapshotMsg{at: now.Add(3 * time.Second), rlErr: errors.New("no such file")})
	m = next.(swModel)
	if m.rateOK {
		t.Fatal("rateOK still true after a failed cache read")
	}
}

// The lobby shows the meters line under the title when data is available, and
// no placeholder when it isn't.
func TestSwViewMetersLine(t *testing.T) {
	now := time.Now()
	m := swRateModel(now, 100)
	view := m.View()
	if !strings.Contains(view, "5h") || !strings.Contains(view, "wk") {
		t.Fatalf("view with rate data must show the 5h/wk meters:\n%s", view)
	}

	m.rateOK = false
	view = m.View()
	if strings.Contains(view, "5h") {
		t.Fatalf("view without rate data must not show meters:\n%s", view)
	}
}

// The lobby renders the same per-model rows the head does — it must not be
// the one panel missing them — in the same position: after wk, before the eta.
func TestSwMetersLineRendersModelRows(t *testing.T) {
	now := time.Now()
	m := swRateModel(now, 200)
	m.modelWindows = []ModelWindow{{Name: "Fable", UsedPercent: 26, ResetsAt: now.Add(72 * time.Hour)}}

	line := ansi.Strip(swMetersLine(m, now))
	for _, want := range []string{"5h", "wk", "fable", "26%", "empty in"} {
		if !strings.Contains(line, want) {
			t.Fatalf("lobby meters line = %q, want %q", line, want)
		}
	}
	if strings.Index(line, "fable") < strings.Index(line, "wk") {
		t.Errorf("fable renders before wk in %q", line)
	}
	if strings.Index(line, "fable") > strings.Index(line, "empty in") {
		t.Errorf("fable renders after the eta in %q", line)
	}
}

// The lobby's drop order with a model row: eta, then fab, then wk — 5h stays.
func TestSwMetersLineDropOrderWithModels(t *testing.T) {
	now := time.Now()
	m := swRateModel(now, 200)
	m.modelWindows = []ModelWindow{{Name: "Fable", UsedPercent: 26, ResetsAt: now.Add(72 * time.Hour)}}

	sawEta, sawFab := false, false
	for w := lipgloss.Width(swMetersLine(m, now)); w >= 20; w-- {
		m.width = w
		line := swMetersLine(m, now)
		if !strings.Contains(line, "5h") {
			t.Fatalf("at width %d the 5h gauge dropped: %q", w, line)
		}
		if !sawEta && !strings.Contains(line, "empty in") {
			sawEta = true
			if !strings.Contains(line, "fable") {
				t.Fatalf("at width %d the eta dropped but fablele went with it: %q", w, line)
			}
		}
		if sawEta && !sawFab && !strings.Contains(line, "fable") {
			sawFab = true
			if !strings.Contains(line, "wk") {
				t.Fatalf("at width %d fable dropped but wk went with it: %q", w, line)
			}
		}
		if sawFab && strings.Contains(line, "fable") {
			t.Fatalf("at width %d fable came back after dropping: %q", w, line)
		}
	}
	if !sawEta || !sawFab {
		t.Fatalf("never observed the eta and fable drops (eta=%v fab=%v)", sawEta, sawFab)
	}
}

// Same lost-race guard as the head's: the lobby must not blank its rows when
// it loses the single-flight race and gets an empty cache back.
func TestSwUsageMsgEmptyLostRaceKeepsRows(t *testing.T) {
	now := time.Now()
	m := swRateModel(now, 200)
	m.modelWindows = []ModelWindow{{Name: "Fable", UsedPercent: 26}}

	got, cmd := m.Update(usageMsg{usage: PlanUsage{}})
	next := got.(swModel)
	if len(next.modelWindows) != 1 {
		t.Errorf("modelWindows = %+v, want the existing row kept", next.modelWindows)
	}
	if cmd == nil {
		t.Error("cmd = nil, want the loop rescheduled after a lost race")
	}

	got, cmd = m.Update(usageMsg{err: errors.New("spawn failed")})
	if next := got.(swModel); len(next.modelWindows) != 1 {
		t.Errorf("modelWindows = %+v, want the existing row kept through an error", next.modelWindows)
	}
	if cmd == nil {
		t.Error("cmd = nil, want the loop rescheduled after an error")
	}

	// A non-subscriber answer the LOBBY fetched itself quiets its spawns while
	// keeping both the rows and the cache half of the loop — see the head's
	// TestUsageMsgUnavailableQuietsOwnSpawnsAndKeepsRows.
	got, cmd = m.Update(usageMsg{usage: PlanUsage{Available: false, FetchedAt: now, Fetched: true}})
	next = got.(swModel)
	if next.usageMaySpawn(now.Add(usageCheckInterval)) {
		t.Error("the lobby's next tick may still spawn after its own unavailable answer, want the spawns quieted")
	}
	if len(next.modelWindows) != 1 {
		t.Errorf("modelWindows = %+v, want the rows kept: the verdict is about this process's credentials, not the account", next.modelWindows)
	}
	if cmd == nil {
		t.Error("cmd = nil, want the cache half of the lobby's loop still ticking")
	}

	// The same verdict arriving from the shared cache is another pane's
	// business and must change nothing here.
	got, _ = m.Update(usageMsg{usage: PlanUsage{Available: false, FetchedAt: now}})
	if next := got.(swModel); !next.usageMaySpawn(now.Add(usageCheckInterval)) {
		t.Error("a cached unavailable verdict quieted the lobby, want it ignored")
	}
}

// The lobby's half of the same rule: with a model row it has three bars
// (5h, wk, fab), not the two it used to hardcode. Dividing the slack by two
// overshoots on three bars, the overflow guard rejects the widened set, and
// the line ends up short of the pane by the whole slack.
func TestSwMetersLineWidensEveryBarIncludingModelRows(t *testing.T) {
	now := time.Now()
	m := swRateModel(now, 0)
	m.modelWindows = []ModelWindow{{Name: "Fable", UsedPercent: 26, ResetsAt: now.Add(72 * time.Hour)}}

	var prevBars int
	for _, w := range []int{120, 140, 160, 200} {
		m.width = w
		line := swMetersLine(m, now)
		for _, want := range []string{"5h", "wk", "fable", "empty in"} {
			if !strings.Contains(line, want) {
				t.Fatalf("at width %d the %q gauge is missing: %q", w, want, line)
			}
		}
		// Three bars share the slack, so at most two columns may be stranded.
		content := len(" ") + lipgloss.Width(strings.TrimRight(ansi.Strip(line), " "))
		if unspent := w - content; unspent > 2 {
			t.Errorf("at width %d, %d columns left unspent (want <= 2): the slack is being divided by the wrong bar count: %q", w, unspent, line)
		}
		if bars := barCellCount(line); bars <= prevBars {
			t.Errorf("at width %d, total bar cells = %d, want more than %d at the previous width", w, bars, prevBars)
		} else {
			prevBars = bars
		}
	}
}
