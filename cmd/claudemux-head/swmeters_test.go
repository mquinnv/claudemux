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
	fhFloor := lipgloss.Width(rateGauges(m.rateLimits, nil, now, defaultBarW)[0]) + 2
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
