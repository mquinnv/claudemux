package main

import (
	"time"
)

// Golden fixtures for the single-account lobby and web page, captured before
// per-account meters existed: single mode must render byte-identically.

// goldenNow is local noon so the meters' wall-clock reset labels read the
// same in every time zone; webGoldenNow is UTC because the JSON spells out
// the offset.
var (
	goldenNow    = time.Date(2026, 11, 27, 12, 0, 0, 0, time.Local)
	webGoldenNow = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
)

func goldenRL(now time.Time) RateLimits {
	return RateLimits{
		FiveHour: Window{UsedPercent: 20, ResetsAt: now.Add(5 * time.Hour)},
		SevenDay: Window{UsedPercent: 30, ResetsAt: now.Add(3 * 24 * time.Hour)},
	}
}

func goldenWindows(now time.Time) []ModelWindow {
	return []ModelWindow{{Name: "Fable", UsedPercent: 12, ResetsAt: now.Add(3 * 24 * time.Hour)}}
}

func goldenMetersModel(now time.Time, width int) swModel {
	m := swModel{width: width, height: 40}
	m.rateOK = true
	m.rateLimits = goldenRL(now)
	m.modelWindows = goldenWindows(now)
	m.pctSamples = []pctSample{{at: now.Add(-10 * time.Minute), pct: 10}, {at: now, pct: 20}}
	return m
}

func goldenViewModel() swModel {
	m := swModel{width: 140, height: 30, previewHidden: true}
	m.snap = swSnapshot{Sessions: []swSession{
		{Name: "api", State: "Idle", Context: 37, Topic: "build fixes", Model: "claude-opus-4-7", Color: "8b5cf6"},
		{Name: "web", State: "Thinking", Context: -1, PRNumber: 12},
	}}
	return m
}

const (
	goldenMeters120 = " 5h ██░░░░░░░░ 20%→5:00p · burn ██████████ 120%/h · wk ███░░░░░░░ 30%→Mon · fable █░░░░░░░░░ 12%→Mon · empty in 1h20m   "
	goldenMeters60  = " 5h ███░░░░░░░░░░░░ 20%→5:00p · burn ███████████████ 120%/h "
	goldenView      = "claudemux switchboard   CONDUCTING \n\n      api                          build fixes                              🔔 Idle                  ██░░░  37% opus 4.7     \n      web                      #12                                          🧠 Thinking                                      \n\nconducting · 1 waiting\n? keys · space conduct/standby · j/k select · enter jump · esc back · p preview · n new · d defer · D blocker · / find · R restart · ^R res…"
	goldenWebJSON   = "{\"taken_at\":\"2026-11-27T12:00:00Z\",\"counts\":{\"sessions\":3,\"waiting\":2,\"deferred\":1,\"closing\":0},\"budget\":{\"five_hour\":{\"used_pct\":20,\"resets_at\":\"2026-11-27T17:00:00Z\",\"color\":\"#04B575\"},\"weekly\":{\"used_pct\":30,\"resets_at\":\"2026-11-30T12:00:00Z\",\"color\":\"#04B575\"},\"models\":[{\"name\":\"Fable\",\"used_pct\":12,\"resets_at\":\"2026-11-30T12:00:00Z\",\"color\":\"#04B575\"}]},\"sessions\":[{\"name\":\"api\",\"color\":\"#8b5cf6\",\"emoji\":\"🔀\",\"state\":\"Idle\",\"state_emoji\":\"🔔\",\"state_raw\":\"Idle\",\"waiting\":true,\"since\":\"2026-09-25T14:00:00Z\",\"context_pct\":37,\"context_color\":\"#04B575\",\"model\":\"opus 4.7\",\"topic\":\"build fixes\",\"summary\":\"fixing the build\",\"prompt\":\"run \\u003cb\\u003ethe\\u003c/b\\u003e tests \\u0026 go\",\"deferred\":false,\"depth\":0,\"group\":\"active\"},{\"name\":\"web\",\"state\":\"Thinking\",\"state_emoji\":\"🧠\",\"state_raw\":\"Thinking\",\"waiting\":false,\"since\":\"2026-09-25T14:01:00Z\",\"deferred\":false,\"depth\":0,\"group\":\"active\"},{\"name\":\"blocked\",\"state\":\"Idle\",\"state_emoji\":\"🔔\",\"state_raw\":\"Idle\",\"waiting\":true,\"context_pct\":12,\"context_color\":\"#04B575\",\"topic\":\"phenix deploy\",\"deferred\":true,\"defer_reason\":\"waiting on review\",\"depth\":0,\"group\":\"active\"}]}"
)
