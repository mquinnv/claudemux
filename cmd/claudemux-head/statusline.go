package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

// statuslineSource is the "source" field this command stamps into the cache,
// distinguishing our writer from abtop's (which wrote "claude").
const statuslineSource = "claudemux"

// statuslinePayload is the subset of Claude Code's statusline JSON we consume.
// Pointers throughout: rate_limits is absent entirely for non-subscribers, and
// each window is independently optional, so "absent" must be distinguishable
// from "zero" — a zeroed cache would render a confident 0% meter.
type statuslinePayload struct {
	RateLimits *struct {
		FiveHour *statuslineWindow `json:"five_hour"`
		SevenDay *statuslineWindow `json:"seven_day"`
	} `json:"rate_limits"`
}

type statuslineWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
}

// runStatusline implements `claudemux-head statusline`, registered in the
// statusLine slot of ~/.claude/settings.json. It reads the payload Claude Code
// writes to stdin and caches the rate-limit windows for the head panes.
//
// It ALWAYS exits 0 and ALWAYS stays silent on stdout. Whatever a statusline
// command prints becomes the user's status line, and any non-zero exit is
// surfaced to them as a broken statusline — neither is an acceptable outcome
// for a cache write that only claudemux cares about.
//
// Hence the signature: stdin and an exit code, and nothing else. The other
// subcommands here take (args, stdout, stderr) because they parse flags and
// report; this one takes no flags (Claude Code invokes it with none) and has
// nowhere to report TO, so carrying those parameters for symmetry would only
// have advertised an ability to speak that this command must never use.
func runStatusline(stdin io.Reader) int {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return 0
	}
	var payload statuslinePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return 0
	}
	if payload.RateLimits == nil {
		// Not a Claude.ai subscriber session (API key, Bedrock, Vertex), or no
		// API response yet. Leave any previous cache alone rather than
		// overwriting real numbers with zeros.
		return 0
	}

	out := rawRateLimitsFile{Source: statuslineSource, UpdatedAt: time.Now().Unix()}
	if w := payload.RateLimits.FiveHour; w != nil {
		out.FiveHour.UsedPercentage = w.UsedPercentage
		out.FiveHour.ResetsAt = w.ResetsAt
	}
	if w := payload.RateLimits.SevenDay; w != nil {
		out.SevenDay.UsedPercentage = w.UsedPercentage
		out.SevenDay.ResetsAt = w.ResetsAt
	}

	path := defaultStatuslineCachePath()
	if path == "" {
		return 0
	}
	var prev *rawRateLimitsFile
	if data, err := os.ReadFile(path); err == nil {
		var p rawRateLimitsFile
		if json.Unmarshal(data, &p) == nil {
			prev = &p
		}
	}
	merged, write := mergeRateLimitWrite(prev, out, time.Now())
	if !write {
		return 0
	}
	if err := writeJSONAtomic(path, merged); err != nil {
		return 0
	}
	return 0
}

// statuslineLagWindow is how long a higher percentage outranks a lower one
// for the same reset. Every running Claude Code renders its statusline with
// the utilization IT last saw, so the cache is written by N processes that
// are each a few seconds to a minute behind the others, and the file
// flip-flopped between adjacent points (21, 22, 21, 22...) as they took
// turns. A genuine drop — a rescaled limit — persists past this window and
// lands; a lag never lasts this long.
const statuslineLagWindow = 60 * time.Second

// mergeRateLimitWrite decides what the cache should hold given what it holds
// now (prev, nil when absent) and what this render reports (in). Usage cannot
// fall inside a window, so for each window whose reset time is unchanged, a
// lower percentage arriving within statuslineLagWindow of prev's write is a
// lagging process and prev's value is kept. The second return is false when
// the result is prev exactly, so a lagging write does not touch the file —
// in particular does not refresh updated_at, which would let a run of lagging
// writes hold a real drop off indefinitely.
func mergeRateLimitWrite(prev *rawRateLimitsFile, in rawRateLimitsFile, now time.Time) (rawRateLimitsFile, bool) {
	if prev == nil {
		return in, true
	}
	out := in
	age := now.Sub(time.Unix(prev.UpdatedAt, 0))
	if age < 0 {
		age = -age
	}
	if age < statuslineLagWindow {
		if in.FiveHour.ResetsAt == prev.FiveHour.ResetsAt && in.FiveHour.UsedPercentage < prev.FiveHour.UsedPercentage {
			out.FiveHour = prev.FiveHour
		}
		if in.SevenDay.ResetsAt == prev.SevenDay.ResetsAt && in.SevenDay.UsedPercentage < prev.SevenDay.UsedPercentage {
			out.SevenDay = prev.SevenDay
		}
	}
	if out.FiveHour == prev.FiveHour && out.SevenDay == prev.SevenDay {
		return *prev, false
	}
	return out, true
}

// writeJSONAtomic marshals v and installs it at path via a temp file and
// rename, so a head reading the cache mid-write never sees a partial file.
func writeJSONAtomic(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	blob, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// defaultStatuslineCachePath is where this command writes and the head reads.
// CLAUDEMUX_RATE_LIMITS_PATH overrides both halves in step, which is what lets
// the tests point the pair at a temp dir.
func defaultStatuslineCachePath() string {
	if p := os.Getenv("CLAUDEMUX_RATE_LIMITS_PATH"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "claudemux", "rate-limits.json")
}
