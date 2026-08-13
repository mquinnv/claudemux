# Background Detection & Conductor Starvation Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop sessions with running background agents from flapping to Idle (and being escorted into), and stop the conductor starving genuinely idle sessions behind permanent snoozes and stale published timestamps.

**Architecture:** The head's `bgTracker` becomes kind-aware (shells vs async agents) and replaces the flat 30-minute expiry with a liveness probe against the agent's own transcript file for agents; it is seeded from the transcript at head start and session rotation, and no longer wiped by a typed prompt. On the switchboard side, snoozes become time-bounded, and the head republishes `@claudemux_state_since` whenever an anchored Since changes even if the state value didn't.

**Tech Stack:** Go (stdlib only, no new deps), Bubble Tea TUI, tmux user options as the pub/sub layer. Tests: `go test ./cmd/claudemux-head/`.

**Spec:** `docs/superpowers/specs/2026-08-11-background-work-state-design.md` (amended by Tasks 1 and 3), plus the Diagnosis section below — this plan is a bugfix pass over that design, argued from measurements taken 2026-08-13.

## Diagnosis (why each change exists)

Measured against the live Remix fleet on 2026-08-13:

1. **`bgMaxAge = 30min` expires still-running agents.** In the newest Remix transcript, 19 of 109 completed background tasks ran longer than 30 minutes (median 11m, max 11h). An expired-but-running agent flips the session `Background:n → Idle`; the conductor reads Idle as "waiting on the human" and escorts the user into a session with nothing to do. Ground truth exists for agents: an async agent writes `<transcript dir>/<session id>/subagents/agent-<agentId>.jsonl` and its mtime advances while it runs. No such file exists for background shells, so shells keep an age cap.
2. **The tracker is never seeded.** `newModel` and `switchSession` build an empty `bgTracker` and only live polls feed it, so launches predating a head start or a session-file rotation are invisible (the same transcript held 250 completion ids but only 109 launches — launches routinely live in a prior file).
3. **A typed prompt wipes the tracker.** Any genuine prompt (slash commands included) clears all tracked work, so a session with four running agents reads Idle the moment the user types once. The wipe's original safety role (missed completions never retiring) is superseded by the liveness probe + caps.
4. **Snoozes never expire while a session stays idle.** Walking away from an escorted session snoozes that exact episode (keyed on published `Since`), and an idle session's Since only changes on a real state transition — observed live: 4 sessions publishing Idle, lobby saying "1 waiting" (3 snoozed indefinitely).
5. **`@claudemux_state_since` goes stale on value-identical transitions.** `maybePublishState` republishes only when the state *value* changes; a busy blip between polls leaves the old Since pinned, which breaks snooze pruning (the stale Since keeps matching) and queue ordering (falsely-old sessions jump the queue front).

Deliberately NOT changed: the escort-hold (conductor waits while the escortee stays Idle and the user sits in it) — that is correct ferry behavior; leaving is one keystroke and now self-heals via the snooze TTL.

## Global Constraints

- Go stdlib only; no new module dependencies.
- All code in `cmd/claudemux-head/`; tests run with `go test ./cmd/claudemux-head/` from the repo root and must pass in full after every task.
- `gofmt` clean; comments follow the repo's style: explain *why* and the constraint the code can't show, not what the next line does.
- Fixture events in tests are built as transcript LINES run through `parseEvent` (use the existing `bgToolUseLine` / `bgResultLine` / `bgMarshalLine` / `bgParse` helpers in `bgwork_test.go`), never hand-assembled `Event` literals — the harness signal under test lives at the top level of the line.
- Timestamps in tests are fixed `time.Date(...)` values, never wall-clock-dependent, except where a test exercises `newModel` (which stamps `time.Now()` internally) — there, write fixture timestamps relative to `time.Now()`.
- Names introduced here and reused across tasks: `bgTask`, `bgLaunch`, `bgShellMaxAge`, `bgAgentMaxAge`, `bgAgentStallAge`, `bgAgentSpawnGrace`, `subagentsDirFor`, `State.Anchored`, `model.publishedSince`, `swSnooze`, `swSnoozeTTL`. Use them exactly.

---

### Task 1: Kind-aware bgTracker with agent liveness expiry

**Files:**
- Modify: `cmd/claudemux-head/bgwork.go`
- Modify: `cmd/claudemux-head/bgwork_test.go`
- Modify: `docs/superpowers/specs/2026-08-11-background-work-state-design.md` (Expiry section)

**Interfaces:**
- Consumes: `Event.BgTaskID` / `Event.BgAgentID` (unchanged, from `events.go`).
- Produces:
  - `type bgLaunch struct { ID string; Agent bool }`; `func bgLaunches(e Event) []bgLaunch`
  - `type bgTask struct { at time.Time; agent bool }`
  - `bgTracker` gains field `subagentsDir string`; `tasks` becomes `map[string]bgTask`
  - Constants: `bgShellMaxAge = 30 * time.Minute`, `bgAgentMaxAge = 24 * time.Hour`, `bgAgentStallAge = 15 * time.Minute`, `bgAgentSpawnGrace = 2 * time.Minute` (the old `bgMaxAge` is renamed `bgShellMaxAge`, value unchanged)
  - `func subagentsDirFor(jsonlPath string) string`
  - `outstanding(now)` signature unchanged: `(int, time.Time)`.

- [ ] **Step 1: Write the failing tests**

Add to `bgwork_test.go`. First a fixture helper for agent launches (mirrors `bgShellLaunch`):

```go
// bgAgentResult is the harness record on an async agent launch's tool_result.
func bgAgentResult(id string) map[string]any {
	return map[string]any{"isAsync": true, "agentId": id}
}

// bgAgentLaunch is one complete async-agent launch: the Agent tool_use, then
// its acknowledgement carrying the harness's isAsync/agentId record.
func bgAgentLaunch(t *testing.T, id, ts string) []Event {
	t.Helper()
	use := "toolu_" + id
	return bgParse(t,
		bgToolUseLine(t, use, "Agent", ts, map[string]any{
			"description": "test agent", "prompt": "do things",
		}),
		bgResultLine(t, use, "Async agent launched: "+id, ts, bgAgentResult(id)),
	)
}

// bgTouchAgentFile creates/updates the agent's transcript in dir with the
// given mtime, creating the subagents layout the way Claude Code does.
func bgTouchAgentFile(t *testing.T, dir, id string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-"+id+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}
```

Then the behavior tests:

```go
// A running agent's transcript keeps advancing; while it does, the launch
// must keep counting far past the old 30-minute cliff.
func TestBgAgentAliveFilePastOldCap(t *testing.T) {
	launch := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	now := launch.Add(2 * time.Hour)
	b := newBgTracker()
	b.subagentsDir = t.TempDir()
	b.observe(bgAgentLaunch(t, "agentaaa", "2026-08-13T10:00:00Z"), launch)
	bgTouchAgentFile(t, b.subagentsDir, "agentaaa", now.Add(-1*time.Minute))
	if n, _ := b.outstanding(now); n != 1 {
		t.Errorf("outstanding = %d, want 1: a live agent must count past 30m", n)
	}
}

// An agent whose transcript stopped advancing died without notifying; it must
// stop counting after the stall threshold so the session isn't hidden forever.
func TestBgAgentStalledFileExpires(t *testing.T) {
	launch := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	now := launch.Add(2 * time.Hour)
	b := newBgTracker()
	b.subagentsDir = t.TempDir()
	b.observe(bgAgentLaunch(t, "agentbbb", "2026-08-13T10:00:00Z"), launch)
	bgTouchAgentFile(t, b.subagentsDir, "agentbbb", now.Add(-bgAgentStallAge-time.Minute))
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0: a stalled agent transcript means the agent is gone", n)
	}
}

// No transcript file yet: normal for a just-spawned agent (grace), stale for
// anything older — a seeded pre-restart launch whose agent is long gone.
func TestBgAgentMissingFileGraceThenDrop(t *testing.T) {
	launch := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.subagentsDir = t.TempDir()
	b.observe(bgAgentLaunch(t, "agentccc", "2026-08-13T10:00:00Z"), launch)
	if n, _ := b.outstanding(launch.Add(1 * time.Minute)); n != 1 {
		t.Errorf("within spawn grace: outstanding = %d, want 1", n)
	}
	if n, _ := b.outstanding(launch.Add(bgAgentSpawnGrace + time.Minute)); n != 0 {
		t.Errorf("past spawn grace with no file: outstanding = %d, want 0", n)
	}
}

// With no subagentsDir configured there is no liveness source; agents must
// fall back to the shell cap rather than counting forever.
func TestBgAgentNoLivenessDirFallsBackToShellCap(t *testing.T) {
	launch := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe(bgAgentLaunch(t, "agentddd", "2026-08-13T10:00:00Z"), launch)
	if n, _ := b.outstanding(launch.Add(bgShellMaxAge + time.Minute)); n != 0 {
		t.Errorf("outstanding = %d, want 0: no liveness dir means the old cap applies", n)
	}
}

// The hard cap backstops a file that keeps advancing forever (e.g. a wedged
// agent looping): even alive-looking agents stop counting after bgAgentMaxAge.
func TestBgAgentHardCap(t *testing.T) {
	launch := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	now := launch.Add(bgAgentMaxAge + time.Hour)
	b := newBgTracker()
	b.subagentsDir = t.TempDir()
	b.observe(bgAgentLaunch(t, "agenteee", "2026-08-12T10:00:00Z"), launch)
	bgTouchAgentFile(t, b.subagentsDir, "agenteee", now.Add(-1*time.Minute))
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0: the hard cap must win over liveness", n)
	}
}

func TestSubagentsDirFor(t *testing.T) {
	got := subagentsDirFor("/home/u/.claude/projects/-p/abc-123.jsonl")
	want := filepath.Join("/home/u/.claude/projects/-p", "abc-123", "subagents")
	if got != want {
		t.Errorf("subagentsDirFor = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestBgAgent|TestSubagentsDirFor' -v`
Expected: FAIL — `bgAgentStallAge`, `subagentsDir`, `subagentsDirFor` undefined.

- [ ] **Step 3: Implement the tracker changes in `bgwork.go`**

Replace the `bgMaxAge` constant, `bgLaunches`, the tracker types, and `outstanding` (leave `observe`'s genuine-prompt block alone — Task 3 removes it):

```go
// Expiry, per kind. A task that never notifies — killed, crashed, launched
// before a head restart — must not mark the session busy forever: the
// conductor would never visit it again, a worse bug than a stale count.
//
// Async agents have ground truth on disk: the harness writes each agent's own
// transcript at <transcript dir>/<session id>/subagents/agent-<id>.jsonl and
// its mtime advances while the agent runs (longest observed gap between
// writes is a single long tool call, bounded by Bash's 10-minute cap).
// bgAgentStallAge past the last write means the agent is gone however it
// died; bgAgentMaxAge backstops a wedged agent that keeps writing forever;
// bgAgentSpawnGrace covers the moment between the launch record and the
// file's creation. Fleet measurement 2026-08-13: real agents run hours (max
// observed 11h), which is why a flat 30-minute cap cannot work for them.
//
// Background shells leave no per-task file, so they keep the flat cap. It
// stays at 30 minutes deliberately: a backgrounded dev server runs (and
// stays silent) indefinitely, and a longer cap would hide its session from
// the conductor for the whole overrun.
const (
	bgShellMaxAge     = 30 * time.Minute
	bgAgentMaxAge     = 24 * time.Hour
	bgAgentStallAge   = 15 * time.Minute
	bgAgentSpawnGrace = 2 * time.Minute
)

// bgLaunch is one background launch as the harness recorded it: the id the
// completion notification will carry back, and which kind of work it is —
// the kinds expire differently (see the constants above).
type bgLaunch struct {
	ID    string
	Agent bool
}

// bgLaunches returns the background work this event started. An entry
// carries at most one.
func bgLaunches(e Event) []bgLaunch {
	var ids []bgLaunch
	if e.BgTaskID != "" {
		ids = append(ids, bgLaunch{ID: e.BgTaskID})
	}
	if e.BgAgentID != "" {
		ids = append(ids, bgLaunch{ID: e.BgAgentID, Agent: true})
	}
	return ids
}

// bgTask is one tracked launch: when it started and which expiry regime
// applies to it.
type bgTask struct {
	at    time.Time
	agent bool
}

// bgTracker holds the background tasks a session has launched and not yet
// seen finish, keyed by task id.
//
// Accumulated from observed events rather than recomputed from the event
// ring: allEvents is capped, so a busy session scrolls a long-running task's
// launch out while it is still running, and a recompute would silently call
// the session Idle again.
type bgTracker struct {
	tasks map[string]bgTask
	// subagentsDir is where this session's async agents write their own
	// transcripts (see subagentsDirFor). Empty means no liveness source
	// (tests, unexpected layout): agents then fall back to the shell cap
	// rather than counting forever.
	subagentsDir string
}

// subagentsDirFor maps a session transcript path to the directory holding
// that session's per-agent transcripts, as Claude Code lays them out:
// <dir>/<session id>/subagents/.
func subagentsDirFor(jsonlPath string) string {
	base := strings.TrimSuffix(filepath.Base(jsonlPath), ".jsonl")
	return filepath.Join(filepath.Dir(jsonlPath), base, "subagents")
}
```

In `observe`, the launch loop becomes:

```go
		for _, l := range bgLaunches(e) {
			b.tasks[l.ID] = bgTask{at: at, agent: l.Agent}
		}
```

And `outstanding` / the new `alive` helper:

```go
// outstanding reports how many launches are still counting and when the
// oldest of them started. Dead entries are dropped as they are found, so a
// stale tracker heals itself without a separate sweep.
func (b *bgTracker) outstanding(now time.Time) (int, time.Time) {
	var oldest time.Time
	for id, task := range b.tasks {
		if !b.alive(id, task, now) {
			delete(b.tasks, id)
			continue
		}
		if oldest.IsZero() || task.at.Before(oldest) {
			oldest = task.at
		}
	}
	return len(b.tasks), oldest
}

// alive decides whether an unfinished launch still counts — see the expiry
// constants for the regime rationale. The os.Stat here runs per outstanding
// agent per poll (~1/s), a handful of stats at most.
func (b *bgTracker) alive(id string, task bgTask, now time.Time) bool {
	if !task.agent {
		return now.Sub(task.at) <= bgShellMaxAge
	}
	if now.Sub(task.at) > bgAgentMaxAge {
		return false
	}
	if b.subagentsDir == "" {
		return now.Sub(task.at) <= bgShellMaxAge
	}
	fi, err := os.Stat(filepath.Join(b.subagentsDir, "agent-"+id+".jsonl"))
	if err != nil {
		return now.Sub(task.at) <= bgAgentSpawnGrace
	}
	return now.Sub(fi.ModTime()) <= bgAgentStallAge
}
```

Add `"os"` and `"path/filepath"` to bgwork.go's imports. Fix the two existing compile sites that used the old shapes: `observe`'s launch loop (shown above) and any test helper calling `bgLaunches` (`TestBgLaunchesInertUnderEveryCarrier` and neighbors assert `len(...) == 0` — those compile unchanged; ones asserting specific ids change from `[]string{"x"}` comparisons to `[]bgLaunch{{ID: "x"}}` / `{ID: "x", Agent: true}`).

- [ ] **Step 4: Run the full package tests**

Run: `go test ./cmd/claudemux-head/`
Expected: PASS. `TestBgTrackerExpiresStaleLaunches` (shell at 31m) must still pass against `bgShellMaxAge`.

- [ ] **Step 5: Amend the spec's Expiry section**

In `docs/superpowers/specs/2026-08-11-background-work-state-design.md`, rewrite the "Expiry" section (around line 179): replace the "a launch older than 30 minutes stops counting" rule with the two-regime rule (shells: flat 30m cap; agents: transcript-mtime liveness with 15m stall threshold, 2m spawn grace, 24h hard cap), and note the 2026-08-13 measurement that motivated it (19/109 tasks over 30m, max 11h).

- [ ] **Step 6: Commit**

```bash
git add cmd/claudemux-head/bgwork.go cmd/claudemux-head/bgwork_test.go docs/superpowers/specs/2026-08-11-background-work-state-design.md
git commit -m "fix(head): expire background agents by transcript liveness, not a 30m cap"
```

---

### Task 2: Seed the tracker at head start and session rotation

**Files:**
- Modify: `cmd/claudemux-head/tui.go` (`newModel`, `switchSession`)
- Test: `cmd/claudemux-head/tui_test.go`

**Interfaces:**
- Consumes: `bgTracker.observe`, `subagentsDirFor` (Task 1), `EventReader.Seeded()`.
- Produces: no new names — behavioral change only: after `newModel` or `switchSession`, launches present in the seeded events are tracked.

- [ ] **Step 1: Write the failing tests**

Add to `tui_test.go`. Fixture timestamps are relative to `time.Now()` because `newModel` stamps its own clock:

```go
// bgSeedTranscript writes a minimal transcript whose tail is: a background
// shell launch, then an assistant text turn (so classifyState lands on Idle
// and only the tracker can upgrade it to Background).
func bgSeedTranscript(t *testing.T, dir, id string, at time.Time) string {
	t.Helper()
	ts := at.UTC().Format(time.RFC3339)
	use := "toolu_" + id
	lines := []string{
		bgToolUseLine(t, use, "Bash", ts, map[string]any{
			"command": "sleep 300", "run_in_background": true,
		}),
		bgResultLine(t, use, "Command running in background with ID: "+id, ts, bgShellResult(id)),
		bgMarshalLine(t, map[string]any{
			"type": "assistant", "timestamp": ts,
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "launched, waiting"},
			}},
		}),
	}
	path := filepath.Join(dir, "seedsess.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A launch already on disk when the head starts must count: heads restart
// and sessions rotate while work is out, and an unseeded tracker calls the
// session Idle — sending the conductor into a busy session.
func TestNewModelSeedsBgTracker(t *testing.T) {
	path := bgSeedTranscript(t, t.TempDir(), "seedaaa", time.Now().Add(-2*time.Minute))
	m := newModel(Config{}, path, "seedsess", false)
	if m.state.Kind != StateBackground {
		t.Errorf("state = %v (%s), want StateBackground: seeded launches must count",
			m.state.Kind, m.state.Label())
	}
}

func TestSwitchSessionSeedsBgTracker(t *testing.T) {
	dir := t.TempDir()
	first := bgSeedTranscript(t, dir, "seedbbb", time.Now().Add(-2*time.Minute))
	m := newModel(Config{}, first, "seedsess", true)
	next := filepath.Join(dir, "rotated.jsonl")
	// Reuse the same fixture shape under the rotated path.
	data, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(next, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = m.switchSession(next, time.Now())
	if m.state.Kind != StateBackground {
		t.Errorf("state after rotation = %v, want StateBackground", m.state.Kind)
	}
}

// The tracker must consult the ROTATED session's subagents dir, not the old
// one's — otherwise agent liveness stats the wrong directory forever.
func TestSwitchSessionRetargetsSubagentsDir(t *testing.T) {
	dir := t.TempDir()
	first := bgSeedTranscript(t, dir, "seedccc", time.Now().Add(-2*time.Minute))
	m := newModel(Config{}, first, "seedsess", true)
	next := filepath.Join(dir, "rotated.jsonl")
	if err := os.WriteFile(next, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = m.switchSession(next, time.Now())
	if want := subagentsDirFor(next); m.bg.subagentsDir != want {
		t.Errorf("subagentsDir = %q, want %q", m.bg.subagentsDir, want)
	}
}
```

If `tui_test.go` lacks the imports `os`, `path/filepath`, `strings`, add them.

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestNewModelSeedsBg|TestSwitchSessionSeeds|TestSwitchSessionRetargets' -v`
Expected: FAIL — state is `StateIdle` (tracker empty), `subagentsDir` not set.

- [ ] **Step 3: Seed in both construction paths**

In `newModel` (tui.go, the `m := model{...}` literal currently sets `bg: newBgTracker()`), after the literal and before `m.recomputeFromEvents(time.Now())`:

```go
	// The tracker starts from what the transcript already shows: heads
	// restart and rotate while background work is out, and an unseeded
	// tracker would call such a session Idle — the conductor then escorts
	// the user into a session that is busy. Replaying the seed is sound:
	// completions always postdate their launches, so any launch inside the
	// end-anchored seed window has its completion inside it too (or still
	// pending), and expiry drops anything genuinely stale.
	m.bg.subagentsDir = subagentsDirFor(jsonlPath)
	m.bg.observe(seeded, time.Now())
```

In `switchSession`, replace the bare `m.bg = newBgTracker()` line with:

```go
	m.bg = newBgTracker()
	// Same seeding rationale as newModel: the rotated-to session may have
	// work out that only its transcript knows about.
	m.bg.subagentsDir = subagentsDirFor(jsonlPath)
	m.bg.observe(seeded, now)
```

- [ ] **Step 4: Run the full package tests**

Run: `go test ./cmd/claudemux-head/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/tui.go cmd/claudemux-head/tui_test.go
git commit -m "fix(head): seed the background tracker from the transcript at start and rotation"
```

---

### Task 3: Stop wiping the tracker on a typed prompt

**Files:**
- Modify: `cmd/claudemux-head/bgwork.go` (`observe`)
- Modify: `cmd/claudemux-head/bgwork_test.go`
- Modify: `docs/superpowers/specs/2026-08-11-background-work-state-design.md`

**Interfaces:**
- Consumes: nothing new.
- Produces: behavioral change only — `observe` no longer clears `tasks` on `genuinePrompt`.

- [ ] **Step 1: Rewrite the wipe test into its inverse**

In `bgwork_test.go`, replace `TestBgTrackerClearedByGenuinePrompt` (around line 609) with:

```go
// A typed prompt does NOT retire running work. The old wipe made a session
// with four running agents read Idle the moment the human typed once, and
// the conductor then treated it as waiting. Completions retire tasks;
// liveness/caps expire the stale — the wipe's safety role is gone.
func TestBgTrackerSurvivesGenuinePrompt(t *testing.T) {
	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe(bgShellLaunch(t, "aaa", "2026-08-13T10:00:00Z"), now)
	b.observe([]Event{{Type: "user", UserText: "what's up?"}}, now)
	if n, _ := b.outstanding(now); n != 1 {
		t.Errorf("outstanding = %d, want 1: typing must not erase running work", n)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./cmd/claudemux-head/ -run TestBgTrackerSurvivesGenuinePrompt -v`
Expected: FAIL — outstanding = 0 (the wipe fired).

- [ ] **Step 3: Remove the wipe from `observe`**

Delete the `if genuinePrompt(e) { ... continue }` block (and its comment) from `bgTracker.observe` in `bgwork.go`, leaving:

```go
func (b *bgTracker) observe(events []Event, now time.Time) {
	if b.tasks == nil {
		b.tasks = map[string]bgTask{}
	}
	for _, e := range events {
		at := parseTimestampOr(e.Timestamp, now)
		for _, id := range bgCompletions(e) {
			delete(b.tasks, id)
		}
		for _, l := range bgLaunches(e) {
			b.tasks[l.ID] = bgTask{at: at, agent: l.Agent}
		}
	}
}
```

`TestBgTrackerNotificationTurnIsNotAPrompt` still passes (it asserted a notification retires only its own task — true with or without the wipe); if its name/comment now reads oddly, rename it `TestBgTrackerNotificationRetiresOnlyItsOwnTask` and trim the comment to the retained claim.

- [ ] **Step 4: Run the full package tests**

Run: `go test ./cmd/claudemux-head/`
Expected: PASS.

- [ ] **Step 5: Amend the spec**

In `docs/superpowers/specs/2026-08-11-background-work-state-design.md`, remove "a genuine user prompt clears everything" from the rules summary (around line 250) and add a sentence where the Expiry section now describes staleness handling: retirement is by completion notification; staleness by liveness/caps; a typed prompt has no effect on the tracker (the wipe was removed 2026-08-13 — it made busy sessions read Idle after one keystroke).

- [ ] **Step 6: Commit**

```bash
git add cmd/claudemux-head/bgwork.go cmd/claudemux-head/bgwork_test.go docs/superpowers/specs/2026-08-11-background-work-state-design.md
git commit -m "fix(head): a typed prompt no longer erases tracked background work"
```

---

### Task 4: Republish an anchored Since even when the state value repeats

**Files:**
- Modify: `cmd/claudemux-head/state.go` (`State`, `classifyState`, `bgOverride`)
- Modify: `cmd/claudemux-head/statepub.go` (`maybePublishState`)
- Modify: `cmd/claudemux-head/tui.go` (model field `publishedSince`)
- Test: `cmd/claudemux-head/state_test.go`, `cmd/claudemux-head/statepub_test.go`

**Interfaces:**
- Consumes: `model.publishedState` (existing).
- Produces:
  - `State.Anchored bool` — true when `Since` came from an event timestamp (or a launch time), false when it is a `now` fallback.
  - `model.publishedSince time.Time` — the Since of the last publish.
  - `maybePublishState` republishes when the value changed OR (`Anchored` and Since differs from `publishedSince`).

- [ ] **Step 1: Write the failing tests**

In `state_test.go`:

```go
func TestClassifyAnchoredSince(t *testing.T) {
	now := time.Now()
	// Event-stamped Idle: anchored.
	events := []Event{{Type: "assistant", UserText: "done", Timestamp: "2026-08-13T10:00:00Z"}}
	if got := classifyState(events, 0, time.Time{}, now); !got.Anchored {
		t.Errorf("event-stamped Idle must be Anchored")
	}
	// Empty stream: Since is a now-fallback, not anchored — publishing it as
	// a change every poll is exactly the flap the publish guard must avoid.
	if got := classifyState(nil, 0, time.Time{}, now); got.Anchored {
		t.Errorf("empty-stream Idle must not be Anchored")
	}
	// Background inherits anchoring from the oldest launch time.
	events = []Event{{Type: "assistant", UserText: "launched", Timestamp: "2026-08-13T10:00:00Z"}}
	got := classifyState(events, 2, time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC), now)
	if got.Kind != StateBackground || !got.Anchored {
		t.Errorf("got %v anchored=%v, want StateBackground anchored", got.Kind, got.Anchored)
	}
}
```

In `statepub_test.go`:

```go
// A new waiting episode with the same value ("Idle" again after a busy blip
// the poll never saw) must still republish _since: the conductor's snoozes
// and queue order key on it, and a pinned stale Since starves the session.
func TestMaybePublishStateRepublishesAnchoredSinceChange(t *testing.T) {
	now := time.Now()
	m := &model{selfPane: "%1"}
	m.state = State{Kind: StateIdle, Since: time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC), Anchored: true}
	if cmd := m.maybePublishState(now); cmd == nil {
		t.Fatal("first publish must fire")
	}
	m.state.Since = m.state.Since.Add(5 * time.Minute) // same value, new episode
	if cmd := m.maybePublishState(now); cmd == nil {
		t.Error("anchored Since change with an unchanged value must republish")
	}
}

// The no-flap invariant the old comment guarded: an unanchored Since is a
// now-fallback that differs every tick, and must NOT trigger republishes.
func TestMaybePublishStateUnanchoredSinceDoesNotFlap(t *testing.T) {
	now := time.Now()
	m := &model{selfPane: "%1"}
	m.state = State{Kind: StateIdle, Since: now, Anchored: false}
	if cmd := m.maybePublishState(now); cmd == nil {
		t.Fatal("first publish must fire")
	}
	m.state.Since = now.Add(time.Second) // next tick's fallback
	if cmd := m.maybePublishState(now.Add(time.Second)); cmd != nil {
		t.Error("unanchored Since drift must not republish every tick")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestClassifyAnchoredSince|TestMaybePublishState' -v`
Expected: FAIL — `Anchored` undefined.

- [ ] **Step 3: Implement**

`state.go` — add the field and set it at every return site. Add to `State`:

```go
	// Anchored: Since came from an event timestamp (or a launch time), not a
	// now-fallback. Only an anchored Since is a real episode boundary the
	// publisher may key change-detection on; an unanchored one differs every
	// poll by construction.
	Anchored bool
```

Rework `classifyState`'s return sites to compute anchoring explicitly (the existing `parseTimestampOr` collapses "parsed" and "fell back", so switch those sites to `parseTimestamp` + zero-check):

```go
	if len(events) == 0 {
		return State{Kind: StateIdle, Since: now}
	}
```
(unchanged — Anchored stays false), and for the tool loop:

```go
			since := parseTimestamp(e.Timestamp)
			anchored := !since.IsZero()
			if !anchored {
				since = now
			}
			return State{Kind: StateTool, ToolName: tu.Name, Since: since, Anchored: anchored}
```

Apply the same three-line pattern to the `"assistant"` (both branches) and `"user"` cases, and leave the final `bgOverride(State{Kind: StateIdle, Since: now}, ...)` unanchored. In `bgOverride`:

```go
func bgOverride(s State, count int, oldest time.Time) State {
	if s.Kind != StateIdle || count <= 0 {
		return s
	}
	since, anchored := oldest, true
	if since.IsZero() {
		since, anchored = s.Since, s.Anchored
	}
	return State{Kind: StateBackground, Since: since, BgCount: count, Anchored: anchored}
}
```

`tui.go` — add `publishedSince time.Time` next to the existing `publishedState` field.

`statepub.go` — replace `maybePublishState`'s guard and rewrite its comment (the old comment's "don't fix this into a per-tick republish" warning is superseded by the anchored guard, which preserves the same invariant):

```go
// maybePublishState returns a cmd publishing the current state when its
// machine form changed since the last publish — or when the state's Since
// moved to a new ANCHORED value under the same machine form. The second
// clause is what keeps @claudemux_state_since honest across value-identical
// transitions (Idle -> busy blip between polls -> Idle): the conductor's
// snooze matching and queue ordering key on _since, and a pinned stale value
// starves the session. Anchored-only, because an unanchored Since is a
// now-fallback that differs every tick — republishing on it would set a tmux
// option once a second for every session with an empty transcript.
func (m *model) maybePublishState(now time.Time) tea.Cmd {
	v := statePublishValue(m.state)
	if v == m.publishedState && (!m.state.Anchored || m.state.Since.Equal(m.publishedSince)) {
		return nil
	}
	m.publishedState = v
	m.publishedSince = m.state.Since
	return publishStateCmd(m.selfPane, m.state, now)
}
```

- [ ] **Step 4: Run the full package tests**

Run: `go test ./cmd/claudemux-head/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/state.go cmd/claudemux-head/statepub.go cmd/claudemux-head/tui.go cmd/claudemux-head/state_test.go cmd/claudemux-head/statepub_test.go
git commit -m "fix(head): republish state_since when an anchored episode changes under the same value"
```

---

### Task 5: Time-bound snoozes and snooze visibility

**Files:**
- Modify: `cmd/claudemux-head/swconductor.go`
- Modify: `cmd/claudemux-head/switchboard.go` (`waitingQueue` lives there? No — it is in `swconductor.go`; only touch `swconductor.go`)
- Modify: `cmd/claudemux-head/switchboardtui.go` (call sites of `step`/`statusLine`/`waitingQueue`, snoozed row marker)
- Test: `cmd/claudemux-head/swconductor_test.go`, `cmd/claudemux-head/switchboardtui_test.go`

**Interfaces:**
- Consumes: `swSession.Since` (existing).
- Produces:
  - `type swSnooze struct { since time.Time; at time.Time }`; `conductor.snoozed` becomes `map[string]swSnooze`
  - `const swSnoozeTTL = 10 * time.Minute`
  - Signature changes (every caller updates in this task): `waitingQueue(snoozed map[string]swSnooze, now time.Time)`, `pruneSnoozes(s swSnapshot, now time.Time)`, `step(s swSnapshot, now time.Time)`, `statusLine(s swSnapshot, now time.Time)`
  - `func (c *conductor) isSnoozed(sess swSession, now time.Time) bool` for the lobby row marker.

- [ ] **Step 1: Write the failing tests**

In `swconductor_test.go` (the existing helpers `snapAt`/`waiting`/`busy` stay as they are):

```go
// A snooze means "not this episode RIGHT NOW", not "never again": an idle
// session's episode can last hours (its Since only moves on a real state
// transition), and an unexpiring snooze starves it — observed live
// 2026-08-13: 4 sessions publishing Idle, lobby saying "1 waiting".
func TestSnoozeExpiresAfterTTL(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	s := snapAt("switchboard", waiting("a", 100))
	sn := map[string]swSnooze{"a": {since: time.Unix(100, 0), at: now}}
	if q := s.waitingQueue(sn, now.Add(swSnoozeTTL-time.Minute)); len(q) != 0 {
		t.Errorf("inside TTL: queue = %v, want empty", q)
	}
	if q := s.waitingQueue(sn, now.Add(swSnoozeTTL+time.Minute)); len(q) != 1 {
		t.Errorf("past TTL: queue = %v, want the session back", q)
	}
}

func TestExpiredSnoozeIsPruned(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.snoozed["a"] = swSnooze{since: time.Unix(100, 0), at: now.Add(-swSnoozeTTL - time.Minute)}
	c.pruneSnoozes(snapAt("switchboard", waiting("a", 100)), now)
	if _, ok := c.snoozed["a"]; ok {
		t.Error("expired snooze must be pruned")
	}
}

func TestStatusLineShowsSnoozedCount(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	c.snoozed["a"] = swSnooze{since: time.Unix(100, 0), at: now}
	got := c.statusLine(snapAt("switchboard", waiting("a", 100), waiting("b", 200)), now)
	if want := "conducting · 1 waiting · 1 snoozed"; got != want {
		t.Errorf("statusLine = %q, want %q", got, want)
	}
}

func TestStatusLineOmitsZeroSnoozed(t *testing.T) {
	now := time.Unix(1_754_700_000, 0)
	c := newConductor()
	got := c.statusLine(snapAt("switchboard", waiting("a", 100)), now)
	if want := "conducting · 1 waiting"; got != want {
		t.Errorf("statusLine = %q, want %q", got, want)
	}
}
```

Existing tests that call `c.step(snap)`, `s.waitingQueue(nil)`, `s.waitingQueue(map[string]time.Time{...})`, or `c.statusLine(s)` are updated mechanically in this step: add a fixed `now := time.Unix(1_754_700_000, 0)` and pass it; snooze literals become `swSnooze{since: time.Unix(100, 0), at: now}` (an `at` of `now` keeps them inside the TTL so the tests' original claims still hold).

- [ ] **Step 2: Run the package tests to verify the new ones fail and the rest compile**

Run: `go test ./cmd/claudemux-head/ -run 'TestSnooze|TestExpiredSnooze|TestStatusLine' -v`
Expected: FAIL — `swSnooze`, `swSnoozeTTL` undefined (compile errors first; fix call-site compilation as part of Step 1's mechanical updates, then see the assertions fail).

- [ ] **Step 3: Implement in `swconductor.go`**

```go
// swSnooze records a waiting episode the user deliberately walked away from:
// which episode (the session's published Since) and when they left.
type swSnooze struct {
	since time.Time
	at    time.Time
}

// swSnoozeTTL bounds a snooze. It exists so a skip cannot become forever:
// an idle session's episode lasts until its state actually transitions —
// hours, for a session the user is done with — and an unexpiring snooze
// starves it behind sessions that were never skipped. Ten minutes keeps the
// original anti-bounce purpose (leaving a session must not ping-pong the
// client straight back) while guaranteeing every waiting session resurfaces
// within one sitting.
const swSnoozeTTL = 10 * time.Minute
```

`conductor.snoozed` becomes `map[string]swSnooze` (update `newConductor`). `waitingQueue`:

```go
func (s swSnapshot) waitingQueue(snoozed map[string]swSnooze, now time.Time) []swSession {
	var q []swSession
	for _, sess := range s.Sessions {
		if !isWaiting(sess.State) {
			continue
		}
		if sn, ok := snoozed[sess.Name]; ok && sn.since.Equal(sess.Since) && now.Sub(sn.at) < swSnoozeTTL {
			continue
		}
		q = append(q, sess)
	}
	sort.SliceStable(q, func(i, j int) bool {
		if !q[i].Since.Equal(q[j].Since) {
			return q[i].Since.Before(q[j].Since)
		}
		return q[i].Name < q[j].Name
	})
	return q
}
```

`pruneSnoozes` gains the TTL clause:

```go
func (c *conductor) pruneSnoozes(s swSnapshot, now time.Time) {
	for name, sn := range c.snoozed {
		sess, ok := s.session(name)
		if !ok || !isWaiting(sess.State) || !sess.Since.Equal(sn.since) || now.Sub(sn.at) >= swSnoozeTTL {
			delete(c.snoozed, name)
		}
	}
}
```

`step` takes `now time.Time`, passes it to `pruneSnoozes` and `waitingQueue`, and the snooze-set site in the escort branch becomes:

```go
				c.snoozed[c.escortee] = swSnooze{since: sess.Since, at: now}
```

`statusLine` takes `now`, computes `n := len(s.waitingQueue(c.snoozed, now))` and appends the snoozed suffix:

```go
	suffix := ""
	if z := len(c.snoozed); z > 0 {
		suffix = fmt.Sprintf(" · %d snoozed", z)
	}
```

appended to both the escorting and conducting lines (`"escorting → %s · %d waiting%s"` / `"conducting · %d waiting%s"`; the paused line stays as is). Add the helper for the lobby:

```go
// isSnoozed reports whether sess's current episode is snoozed right now —
// the lobby dims such rows so "waiting but deliberately skipped" is visible
// instead of looking like a conductor bug.
func (c *conductor) isSnoozed(sess swSession, now time.Time) bool {
	sn, ok := c.snoozed[sess.Name]
	return ok && sn.since.Equal(sess.Since) && now.Sub(sn.at) < swSnoozeTTL
}
```

- [ ] **Step 4: Update `switchboardtui.go` call sites and the row marker**

In `Update`'s `swSnapshotMsg` case: `m.cond.step(m.snap)` → `m.cond.step(m.snap, time.Now())`. In `View`: `m.cond.statusLine(m.snap)` → `m.cond.statusLine(m.snap, now)` and the standby line's `len(m.snap.waitingQueue(m.cond.snoozed))` → `len(m.snap.waitingQueue(m.cond.snoozed, now))` (View already computes `now := time.Now()`). The waiting marker in the row loop becomes:

```go
		marker := "  "
		if isWaiting(sess.State) {
			if m.cond.isSnoozed(sess, now) {
				marker = swUnknownStyle.Render("● ") // waiting, deliberately skipped
			} else {
				marker = swWaitStyle.Render("● ")
			}
		}
```

- [ ] **Step 5: Run the full package tests**

Run: `go test ./cmd/claudemux-head/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/claudemux-head/swconductor.go cmd/claudemux-head/switchboardtui.go cmd/claudemux-head/swconductor_test.go cmd/claudemux-head/switchboardtui_test.go
git commit -m "fix(switchboard): snoozes expire after 10m and are visible in the lobby"
```

---

### Task 6: Live verification against the running fleet

**Files:**
- No source changes — this is the release gate. (If anything below fails, stop and debug with `superpowers:systematic-debugging`; do not patch forward.)

- [ ] **Step 1: Build and install the head binary the way the repo ships it**

Run: `go build ./... && go vet ./cmd/claudemux-head/`
Expected: clean. Then install per the repo's own flow (see `install.sh` / `RELEASING.md` — whichever the repo's docs designate for a local rollout) and restart one head pane to pick it up.

- [ ] **Step 2: Verify Background survives past 30 minutes**

In a session with a long-running async agent (the Remix fleet qualifies), confirm after >30m of agent runtime:

Run: `tmux list-sessions -F '#{session_name}|#{@claudemux_state}'`
Expected: the session publishes `Background:n` (not `Idle`) while the agent's `subagents/agent-<id>.jsonl` mtime keeps advancing.

- [ ] **Step 3: Verify a head restart keeps the count**

Kill and restart that session's head pane mid-agent-run.
Expected: after the head re-seeds, the session returns to `Background:n` without a new launch event.

- [ ] **Step 4: Verify snooze expiry in the lobby**

From the lobby, get escorted to an idle session, jump back to the lobby (snoozing it), and confirm the status line counts it (`… · 1 snoozed`), the row's dot dims, and within ~10 minutes the session re-enters the waiting queue and gets dispatched again.

- [ ] **Step 5: Commit any doc-only fixups and merge per repo convention**

```bash
git add -A && git commit -m "docs: record live verification of background/conductor fixes" --allow-empty
```

---

## Self-Review (completed at write time)

- **Spec coverage:** Diagnosis items 1→Task 1, 2→Task 2, 3→Task 3, 5→Task 4, 4→Task 5; live confirmation→Task 6. The escort-hold is a documented non-change.
- **Type consistency:** `bgLaunch{ID, Agent}` (Task 1) is what Task 3's `observe` loop consumes; `swSnooze{since, at}` field names match between Task 5's tests and implementation; `State.Anchored` set in Task 4 is read only by `maybePublishState`.
- **Known ripple:** Task 1 changes `bgLaunches`' return type and Task 5 changes four conductor signatures — both tasks explicitly include updating every caller and existing test in the same commit, so the package compiles at every task boundary.
