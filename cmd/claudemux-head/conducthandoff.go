package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// The lobby's conductor state — which session it is escorting to, and every
// waiting episode the user deliberately walked away from — lives only in
// memory. That is why shouldAutoRestart used to insist on a parked, snooze-free
// lobby before re-execing into a rebuilt binary: discarding the snoozes would
// send the user straight back to the session they had just left.
//
// In practice that gate almost never opened. A user who works inside sessions
// leaves the conductor escorting or paused with snoozes live, so the lobby sat
// on a binary eight days old while every head around it upgraded — and the
// conductor fix the user had just installed was simply not running.
//
// So carry the state instead of waiting for there to be none. write it
// immediately before restartSelf, read it once on the way up. This is a HANDOFF
// between two processes seconds apart, not a persistence layer — see
// conductHandoffTTL.
const conductHandoffTTL = 30 * time.Second

type rawSnooze struct {
	Since int64 `json:"since"`
	At    int64 `json:"at"`
}

type rawConductHandoff struct {
	At               int64                `json:"at"`
	Phase            int                  `json:"phase"`
	Escortee         string               `json:"escortee"`
	Snoozed          map[string]rawSnooze `json:"snoozed"`
	PausedCur        string               `json:"paused_cur"`
	PausedCurWaiting bool                 `json:"paused_cur_waiting"`
	PausedHandedBack bool                 `json:"paused_handed_back"`
}

// defaultConductHandoffPath matches the other head state files
// (defaultUsageCachePath, defaultStatuslineCachePath), env override included so
// tests never touch the real one.
func defaultConductHandoffPath() string {
	if p := os.Getenv("CLAUDEMUX_CONDUCT_HANDOFF_PATH"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "claudemux", "conductor-handoff.json")
}

// writeConductHandoff records c for the process about to replace this one.
// Best effort by contract: a failure costs the snoozes, not the restart, so
// callers ignore the error.
func writeConductHandoff(path string, c conductor, now time.Time) error {
	if path == "" {
		return nil
	}
	raw := rawConductHandoff{
		At:               now.Unix(),
		Phase:            int(c.phase),
		Escortee:         c.escortee,
		Snoozed:          make(map[string]rawSnooze, len(c.snoozed)),
		PausedCur:        c.pausedCur,
		PausedCurWaiting: c.pausedCurWaiting,
		PausedHandedBack: c.pausedHandedBack,
	}
	for name, sn := range c.snoozed {
		raw.Snoozed[name] = rawSnooze{Since: sn.since.Unix(), At: sn.at.Unix()}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// readConductHandoff consumes the file: it is removed whether or not it was
// usable, so a lobby started fresh minutes later never inherits a phase, and a
// corrupt file cannot wedge every future start. ok=false means "start clean",
// which is always safe — it is the behaviour every lobby had before this
// existed.
//
// The conductor's client is deliberately NOT carried: resolveClient re-adopts
// on the first tick, and a stale client name would be a worse answer than
// looking.
func readConductHandoff(path string, now time.Time) (conductor, bool) {
	if path == "" {
		return conductor{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return conductor{}, false
	}
	_ = os.Remove(path)
	var raw rawConductHandoff
	if err := json.Unmarshal(b, &raw); err != nil {
		return conductor{}, false
	}
	if at := time.Unix(raw.At, 0); now.Sub(at) > conductHandoffTTL || now.Before(at) {
		return conductor{}, false
	}
	c := newConductor()
	c.phase = swPhase(raw.Phase)
	c.escortee = raw.Escortee
	c.pausedCur = raw.PausedCur
	c.pausedCurWaiting = raw.PausedCurWaiting
	c.pausedHandedBack = raw.PausedHandedBack
	for name, sn := range raw.Snoozed {
		c.snoozed[name] = swSnooze{since: time.Unix(sn.Since, 0), at: time.Unix(sn.At, 0)}
	}
	return c, true
}
