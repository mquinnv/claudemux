# Reboot Restore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After a reboot (or tmux server death), the claudemux switchboard offers to recreate every session that was live at the time, each with claude resumed on its conversation.

**Architecture:** Each session head writes a small heartbeat record (`~/.claude/claudemux/sessions/<name>.json`) every 30s. A pure selection function picks records that were last seen before the boot/tmux-server cutoff and cluster within 10 minutes of the newest one. The lobby shows an offer strip; restoring calls the launcher once per session with three new flags (`-N` exact name, `-r` resume id, `-C` claude dir).

**Tech Stack:** Go 1.27 (Bubble Tea TUI, `cmd/claudemux-head`), bash (`bin/claudemux`), tmux.

**Spec:** `docs/superpowers/specs/2026-09-19-reboot-restore-design.md` — read it before starting any task.

## Global Constraints

- All Go code lives in `package main` under `cmd/claudemux-head/`. Run tests from that directory: `go test ./...`.
- Record dir: `~/.claude/claudemux/sessions/`. Archive dir per boot: `sessions/restored-<cutoff unix>/`.
- Heartbeat interval: 30s (also written immediately when the published state value changes).
- Cluster window: 10 minutes. Prune age: 7 days.
- Interrupted states: `Thinking`, `Compacting`, `Tool:*` except `Tool:AskUserQuestion`, `Background:*`.
- Lobby keys while the offer strip shows: `r` restore all, `s` select (checklist), `x` dismiss. `R` is already taken (lobby restart) — do not rebind it.
- Restored sessions are never auto-prompted.
- Every tmux call from Go uses a context timeout (2s for queries), never blocks Update.
- Match the surrounding code's comment density: every non-trivial function gets a doc comment explaining *why*.
- Commit after each task. Commit messages end with:
  ```
  Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_019XG1n5sRbChajH3gzr4KVk
  ```

## File Structure

- Create `cmd/claudemux-head/sessionrecord.go` (+ `_test.go`) — record type, dir, atomic write, read, prune, archive, remove.
- Create `cmd/claudemux-head/lostsessions.go` (+ `_test.go`) — pure selection, interrupted classification, boot-time parsing, cutoff.
- Modify `cmd/claudemux-head/tui.go` — heartbeat on tick; delete record on teardown kill.
- Create `cmd/claudemux-head/headrecord.go` (+ `_test.go`) — the head-side pieces (due check, record build, write cmd), keeping tui.go edits small.
- Modify `bin/claudemux` — `-r`, `-N`, `-C` options.
- Modify `cmd/claudemux-head/launchertargets_test.go` — launcher option tests.
- Create `cmd/claudemux-head/swrestore.go` (+ `_test.go`) — lobby offer state, scan cmd, restore cmd, strip/picker rendering.
- Modify `cmd/claudemux-head/switchboardtui.go` — wire scan, keys, view.
- Modify `cmd/claudemux-head/swpreview.go` (+ `swpreview_test.go`) — strip row in the layout budget.
- Modify `README.md` — document the feature.

---

### Task 1: Session records on disk

**Files:**
- Create: `cmd/claudemux-head/sessionrecord.go`
- Test: `cmd/claudemux-head/sessionrecord_test.go`

**Interfaces:**
- Produces:
  - `type sessionRecord struct { SessionName, LaunchDir, SessionID, ClaudeCwd, State, Topic string; LastSeen int64 }` (JSON tags below)
  - `type loadedRecord struct { Path string; Rec sessionRecord }`
  - `func sessionRecordDir() string`
  - `func sessionRecordFile(dir, name string) string`
  - `func writeSessionRecord(dir string, r sessionRecord) error`
  - `func readSessionRecords(dir string) []loadedRecord`
  - `func pruneSessionRecords(dir string, now time.Time, maxAge time.Duration)`
  - `func archiveSessionRecords(dir string, cutoff int64, paths []string) error`
  - `func removeSessionRecord(dir, name string)`
  - `const sessionRecordMaxAge = 7 * 24 * time.Hour`

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionRecordRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := sessionRecord{
		SessionName: "remix-2", LaunchDir: "/p/remix", SessionID: "abc-123",
		ClaudeCwd: "/p/remix/.claude/worktrees/foo", State: "Tool:Bash",
		Topic: "Fix idle detection", LastSeen: 1789000000,
	}
	if err := writeSessionRecord(dir, r); err != nil {
		t.Fatal(err)
	}
	got := readSessionRecords(dir)
	if len(got) != 1 || got[0].Rec != r {
		t.Fatalf("got %+v, want one record %+v", got, r)
	}
	if got[0].Path != sessionRecordFile(dir, "remix-2") {
		t.Errorf("path = %q", got[0].Path)
	}
	// No temp files left behind by the atomic write.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want 1", len(entries))
	}
}

func TestSessionRecordFileSanitizes(t *testing.T) {
	cases := map[string]string{
		"remix":   "remix.json",
		"a/b":     "a_b.json",
		".hidden": "_hidden.json",
		"":        "_.json",
	}
	for name, want := range cases {
		if got := filepath.Base(sessionRecordFile("/d", name)); got != want {
			t.Errorf("sessionRecordFile(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestReadSessionRecordsSkipsJunk(t *testing.T) {
	dir := t.TempDir()
	_ = writeSessionRecord(dir, sessionRecord{SessionName: "ok", SessionID: "id1", LastSeen: 1})
	_ = os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte("{nope"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "noid.json"), []byte(`{"session_name":"x"}`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "ok.json.tmp.123"), []byte(`{}`), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "restored-5"), 0o755)
	_ = writeSessionRecord(filepath.Join(dir, "restored-5"), sessionRecord{SessionName: "old", SessionID: "id2"})
	got := readSessionRecords(dir)
	if len(got) != 1 || got[0].Rec.SessionName != "ok" {
		t.Fatalf("got %+v, want only the ok record", got)
	}
}

func TestReadSessionRecordsMissingDir(t *testing.T) {
	if got := readSessionRecords(filepath.Join(t.TempDir(), "nope")); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
	if got := readSessionRecords(""); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestPruneSessionRecords(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	_ = writeSessionRecord(dir, sessionRecord{SessionName: "fresh", SessionID: "a"})
	_ = writeSessionRecord(dir, sessionRecord{SessionName: "stale", SessionID: "b"})
	old := now.Add(-8 * 24 * time.Hour)
	_ = os.Chtimes(sessionRecordFile(dir, "stale"), old, old)
	_ = os.Chtimes(sessionRecordFile(dir, "fresh"), now, now)
	pruneSessionRecords(dir, now, sessionRecordMaxAge)
	if _, err := os.Stat(sessionRecordFile(dir, "stale")); !os.IsNotExist(err) {
		t.Errorf("stale record survived prune")
	}
	if _, err := os.Stat(sessionRecordFile(dir, "fresh")); err != nil {
		t.Errorf("fresh record pruned: %v", err)
	}
}

func TestArchiveSessionRecords(t *testing.T) {
	dir := t.TempDir()
	_ = writeSessionRecord(dir, sessionRecord{SessionName: "a", SessionID: "1"})
	_ = writeSessionRecord(dir, sessionRecord{SessionName: "b", SessionID: "2"})
	paths := []string{sessionRecordFile(dir, "a"), sessionRecordFile(dir, "b")}
	if err := archiveSessionRecords(dir, 42, paths); err != nil {
		t.Fatal(err)
	}
	if got := readSessionRecords(dir); len(got) != 0 {
		t.Errorf("records still live after archive: %+v", got)
	}
	if got := readSessionRecords(filepath.Join(dir, "restored-42")); len(got) != 2 {
		t.Errorf("archive holds %d records, want 2", len(got))
	}
}

func TestRemoveSessionRecord(t *testing.T) {
	dir := t.TempDir()
	_ = writeSessionRecord(dir, sessionRecord{SessionName: "a", SessionID: "1"})
	removeSessionRecord(dir, "a")
	removeSessionRecord(dir, "a") // idempotent
	removeSessionRecord("", "a")  // no dir: no-op
	removeSessionRecord(dir, "")  // no name: no-op
	if got := readSessionRecords(dir); len(got) != 0 {
		t.Errorf("record survived remove: %+v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/claudemux-head && go test -run 'SessionRecord' ./...`
Expected: FAIL — undefined: `sessionRecord`, `writeSessionRecord`, etc.

- [ ] **Step 3: Implement**

```go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Session records: the heartbeat that lets the lobby restore a fleet after a
// reboot.
//
// Nothing tmux or Claude Code keeps survives a reboot in a form that says
// "these sessions were alive when the machine went down": the pane map keeps
// files for panes closed days ago, has no tmux session name, and is keyed by
// pane numbers the next tmux server reuses. So every head writes one record
// for its own session, refreshed every 30s, and the lobby reads the set back
// after a reboot — see lostsessions.go for how the dead cluster is picked out.
// See docs/superpowers/specs/2026-09-19-reboot-restore-design.md.

// sessionRecordMaxAge bounds how long a record for a session that went away
// on its own is kept. Only records inside the 10-minute cluster before a
// reboot are ever offered; this just keeps the directory from growing.
const sessionRecordMaxAge = 7 * 24 * time.Hour

// sessionRecord is one head's heartbeat. The JSON form is an on-disk
// interface read by the lobby, possibly a newer or older binary than the
// head that wrote it — add fields, never rename them.
type sessionRecord struct {
	SessionName string `json:"session_name"`
	LaunchDir   string `json:"launch_dir"`
	SessionID   string `json:"session_id"`
	ClaudeCwd   string `json:"claude_cwd"`
	State       string `json:"state"`
	Topic       string `json:"topic,omitempty"`
	LastSeen    int64  `json:"last_seen"`
}

// loadedRecord pairs a record with the file it came from, so the lobby can
// archive exactly the files it offered.
type loadedRecord struct {
	Path string
	Rec  sessionRecord
}

// sessionRecordDir is ~/.claude/claudemux/sessions, "" when the home dir
// can't be resolved (every caller treats "" as "no records").
func sessionRecordDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "claudemux", "sessions")
}

// sessionRecordFile is the record path for a tmux session name. tmux names
// can hold '/', which would be a subdirectory here, and a leading '.' would
// hide the file; both are replaced. The real name lives inside the record,
// so the filename only has to be unique and safe, not reversible.
func sessionRecordFile(dir, name string) string {
	safe := strings.ReplaceAll(name, "/", "_")
	safe = strings.ReplaceAll(safe, "\x00", "_")
	if safe == "" || strings.HasPrefix(safe, ".") {
		safe = "_" + strings.TrimPrefix(safe, ".")
	}
	return filepath.Join(dir, safe+".json")
}

// writeSessionRecord writes r atomically (temp file + rename, the same
// pattern as hooks/claudemux-map.sh) so a reader never sees half a record —
// and a reboot mid-write leaves the previous record, not a truncated one.
func writeSessionRecord(dir string, r sessionRecord) error {
	if dir == "" {
		return fmt.Errorf("no session record dir")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	path := sessionRecordFile(dir, r.SessionName)
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// readSessionRecords loads every top-level *.json record in dir. Archive
// subdirectories, temp files, unparseable files and records with no session
// name or id are skipped — the last two left in place so they can be looked
// at, never archived or offered.
func readSessionRecords(dir string) []loadedRecord {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []loadedRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var r sessionRecord
		if json.Unmarshal(data, &r) != nil || r.SessionName == "" || r.SessionID == "" {
			continue
		}
		out = append(out, loadedRecord{Path: path, Rec: r})
	}
	return out
}

// pruneSessionRecords deletes top-level records (and stray temp files) whose
// file was last written more than maxAge ago. By mtime rather than LastSeen
// so a corrupt file is also eventually cleared.
func pruneSessionRecords(dir string, now time.Time, maxAge time.Duration) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.Contains(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > maxAge {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// archiveSessionRecords moves the offered records into restored-<cutoff>/,
// so the offer is made once per boot and what was offered stays inspectable.
// A move that fails is reported but does not stop the rest.
func archiveSessionRecords(dir string, cutoff int64, paths []string) error {
	dest := filepath.Join(dir, fmt.Sprintf("restored-%d", cutoff))
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	var firstErr error
	for _, p := range paths {
		if err := os.Rename(p, filepath.Join(dest, filepath.Base(p))); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// removeSessionRecord deletes a session's record — called when a session is
// ended on purpose, so it is never offered back. Missing is fine.
func removeSessionRecord(dir, name string) {
	if dir == "" || name == "" {
		return
	}
	_ = os.Remove(sessionRecordFile(dir, name))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/claudemux-head && go test -run 'SessionRecord' ./... && go vet ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/sessionrecord.go cmd/claudemux-head/sessionrecord_test.go
git commit -m "records: per-session heartbeat files for reboot restore"
```

---

### Task 2: Lost-session selection

**Files:**
- Create: `cmd/claudemux-head/lostsessions.go`
- Test: `cmd/claudemux-head/lostsessions_test.go`

**Interfaces:**
- Consumes: `sessionRecord`, `loadedRecord` (Task 1).
- Produces:
  - `const lostClusterWindow = 10 * time.Minute`
  - `type lostSession struct { loadedRecord; Interrupted bool }`
  - `func selectLost(recs []loadedRecord, cutoff int64, liveNames, liveIDs map[string]bool) (lost []lostSession, newest int64)`
  - `func interruptedState(state string) bool`
  - `func parseBootTime(out string) (int64, bool)`
  - `func restoreCutoff(boot int64, bootOK bool, tmuxStart int64, tmuxOK bool) (int64, bool)`

- [ ] **Step 1: Write the failing tests**

```go
package main

import "testing"

func rec(name, id, state string, lastSeen int64) loadedRecord {
	return loadedRecord{Path: "/d/" + name + ".json", Rec: sessionRecord{
		SessionName: name, SessionID: id, State: state, LastSeen: lastSeen,
	}}
}

func lostNames(l []lostSession) []string {
	var out []string
	for _, s := range l {
		out = append(out, s.Rec.SessionName)
	}
	return out
}

func TestSelectLostCluster(t *testing.T) {
	const cutoff = 100_000
	recs := []loadedRecord{
		rec("a", "1", "Idle", cutoff-60),           // newest pre-cutoff
		rec("b", "2", "Thinking", cutoff-60-599),   // inside 10 min of newest
		rec("c", "3", "Idle", cutoff-60-600),       // exactly on the edge: in
		rec("d", "4", "Idle", cutoff-60-601),       // outside: closed earlier
		rec("e", "5", "Idle", cutoff+5),            // written after cutoff: alive now
	}
	lost, newest := selectLost(recs, cutoff, nil, nil)
	if newest != cutoff-60 {
		t.Errorf("newest = %d, want %d", newest, cutoff-60)
	}
	got := lostNames(lost)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("lost = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("lost = %v, want %v (sorted newest first)", got, want)
		}
	}
	if !lost[1].Interrupted || lost[0].Interrupted {
		t.Errorf("interrupted flags wrong: %+v", lost)
	}
}

func TestSelectLostCutoffBoundary(t *testing.T) {
	// last_seen == cutoff is NOT before the cutoff: that head was alive at boot.
	lost, _ := selectLost([]loadedRecord{rec("a", "1", "Idle", 500)}, 500, nil, nil)
	if len(lost) != 0 {
		t.Fatalf("lost = %v, want none", lostNames(lost))
	}
}

func TestSelectLostExcludesLive(t *testing.T) {
	recs := []loadedRecord{
		rec("a", "1", "Idle", 90),
		rec("b", "2", "Idle", 90),
		rec("c", "3", "Idle", 90),
	}
	lost, _ := selectLost(recs, 100, map[string]bool{"a": true}, map[string]bool{"2": true})
	if got := lostNames(lost); len(got) != 1 || got[0] != "c" {
		t.Fatalf("lost = %v, want [c]", got)
	}
}

func TestSelectLostEmpty(t *testing.T) {
	if lost, newest := selectLost(nil, 100, nil, nil); len(lost) != 0 || newest != 0 {
		t.Fatalf("got %v %d", lost, newest)
	}
	// Every record written after the cutoff: nothing to offer.
	if lost, _ := selectLost([]loadedRecord{rec("a", "1", "Idle", 200)}, 100, nil, nil); len(lost) != 0 {
		t.Fatalf("got %v", lostNames(lost))
	}
}

func TestInterruptedState(t *testing.T) {
	yes := []string{"Thinking", "Compacting", "Tool:Bash", "Tool:Edit", "Background:2"}
	no := []string{"", "Idle", "Awaiting", "Error", "Asking", "Tool:AskUserQuestion", "Starting", "Unsure:1"}
	for _, s := range yes {
		if !interruptedState(s) {
			t.Errorf("interruptedState(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if interruptedState(s) {
			t.Errorf("interruptedState(%q) = true, want false", s)
		}
	}
}

func TestParseBootTime(t *testing.T) {
	sec, ok := parseBootTime("{ sec = 1789750380, usec = 123456 } Thu Sep 18 13:53:00 2026\n")
	if !ok || sec != 1789750380 {
		t.Errorf("got %d %v", sec, ok)
	}
	for _, bad := range []string{"", "garbage", "{ sec = , usec = 1 }", "{ sec = -5, usec = 0 }"} {
		if _, ok := parseBootTime(bad); ok {
			t.Errorf("parseBootTime(%q) ok, want failure", bad)
		}
	}
}

func TestRestoreCutoff(t *testing.T) {
	if c, ok := restoreCutoff(100, true, 200, true); !ok || c != 200 {
		t.Errorf("both: %d %v", c, ok)
	}
	if c, ok := restoreCutoff(300, true, 200, true); !ok || c != 300 {
		t.Errorf("boot later: %d %v", c, ok)
	}
	if c, ok := restoreCutoff(100, true, 0, false); !ok || c != 100 {
		t.Errorf("boot only: %d %v", c, ok)
	}
	if c, ok := restoreCutoff(0, false, 200, true); !ok || c != 200 {
		t.Errorf("tmux only: %d %v", c, ok)
	}
	if _, ok := restoreCutoff(0, false, 0, false); ok {
		t.Errorf("neither: ok, want no offer")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/claudemux-head && go test -run 'SelectLost|InterruptedState|ParseBootTime|RestoreCutoff' ./...`
Expected: FAIL — undefined: `selectLost`, etc.

- [ ] **Step 3: Implement**

```go
package main

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Picking out the sessions a reboot killed.
//
// A reboot stops every head at once, so their records' last_seen values
// cluster just before the cutoff (the boot, or the tmux server's start —
// whichever is later, so a `tmux kill-server` counts too). Sessions closed
// on purpose earlier stopped heartbeating earlier and fall outside the
// cluster. Records written after the cutoff belong to heads alive now.

// lostClusterWindow is how far before the newest pre-cutoff heartbeat a
// record may be and still count as dying in the same event. Heads write
// every 30s; ten minutes also absorbs a machine that slept before the
// update rebooted it, where heads stop at slightly different moments.
const lostClusterWindow = 10 * time.Minute

// lostSession is a record the lobby offers to restore.
type lostSession struct {
	loadedRecord
	// Interrupted: the session was mid-turn when it died. Only flagged in
	// the picker — nothing is sent to it on restore.
	Interrupted bool
}

// selectLost returns the records that died together before cutoff, newest
// first, and the newest pre-cutoff last_seen (0 when there is none).
// liveNames/liveIDs exclude sessions already running again — by tmux name,
// or by claude session id when the user resumed one by hand elsewhere.
func selectLost(recs []loadedRecord, cutoff int64, liveNames, liveIDs map[string]bool) ([]lostSession, int64) {
	var newest int64
	for _, r := range recs {
		if r.Rec.LastSeen < cutoff && r.Rec.LastSeen > newest {
			newest = r.Rec.LastSeen
		}
	}
	if newest == 0 {
		return nil, 0
	}
	floor := newest - int64(lostClusterWindow/time.Second)
	var lost []lostSession
	for _, r := range recs {
		ls := r.Rec.LastSeen
		if ls >= cutoff || ls < floor {
			continue
		}
		if liveNames[r.Rec.SessionName] || liveIDs[r.Rec.SessionID] {
			continue
		}
		lost = append(lost, lostSession{loadedRecord: r, Interrupted: interruptedState(r.Rec.State)})
	}
	sort.SliceStable(lost, func(i, j int) bool { return lost[i].Rec.LastSeen > lost[j].Rec.LastSeen })
	return lost, newest
}

// interruptedState reports whether a published state value (see
// statePublishValue) means claude was working when the record was written.
// An open AskUserQuestion is waiting on the user, not working.
func interruptedState(state string) bool {
	switch {
	case state == "Thinking", state == "Compacting":
		return true
	case state == "Tool:AskUserQuestion":
		return false
	case strings.HasPrefix(state, "Tool:"), strings.HasPrefix(state, "Background:"):
		return true
	}
	return false
}

var bootTimeRe = regexp.MustCompile(`sec = (\d+)`)

// parseBootTime reads `sysctl -n kern.boottime` output:
// "{ sec = 1789750380, usec = 123456 } Thu Sep 18 13:53:00 2026".
func parseBootTime(out string) (int64, bool) {
	m := bootTimeRe.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	sec, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || sec <= 0 {
		return 0, false
	}
	return sec, true
}

// restoreCutoff is the later of the boot time and the tmux server's start.
// With neither known there is no offer: guessing a cutoff could offer to
// "restore" sessions that are merely closed.
func restoreCutoff(boot int64, bootOK bool, tmuxStart int64, tmuxOK bool) (int64, bool) {
	switch {
	case bootOK && tmuxOK:
		if tmuxStart > boot {
			return tmuxStart, true
		}
		return boot, true
	case bootOK:
		return boot, true
	case tmuxOK:
		return tmuxStart, true
	}
	return 0, false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/claudemux-head && go test -run 'SelectLost|InterruptedState|ParseBootTime|RestoreCutoff' ./... && go vet ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/claudemux-head/lostsessions.go cmd/claudemux-head/lostsessions_test.go
git commit -m "restore: pick out the sessions a reboot killed"
```

---

### Task 3: Heads write their heartbeat; teardown removes it

**Files:**
- Create: `cmd/claudemux-head/headrecord.go`
- Test: `cmd/claudemux-head/headrecord_test.go`
- Modify: `cmd/claudemux-head/tui.go` (model struct near line 106; `tickMsg` case near line 1434; `claudeGoneMsg` case near line 1768; add a `recordWrittenMsg` case)

**Interfaces:**
- Consumes: `sessionRecord`, `sessionRecordDir`, `writeSessionRecord`, `removeSessionRecord` (Task 1); `statePublishValue` (statepub.go); model fields `selfPane`, `sessionID`, `sessionCwd`, `workDir`, `state`, `summary`, `teardown`.
- Produces:
  - model fields `recordName string`, `lastRecordAt time.Time`, `lastRecordState string`
  - `const sessionRecordInterval = 30 * time.Second`
  - `func (m model) recordDue(now time.Time) bool`
  - `func (m model) sessionRecordFor(now time.Time) sessionRecord` (SessionName/LaunchDir left empty — filled from tmux by the cmd)
  - `type recordWrittenMsg struct{ name string }`
  - `func writeRecordCmd(selfPane, dir string, r sessionRecord) tea.Cmd`
  - `func parseSessionNamePath(out string) (name, path string, ok bool)`

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"testing"
	"time"
)

func TestRecordDue(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	base := model{selfPane: "%1", sessionID: "abc", state: State{Kind: StateIdle}}

	if !base.recordDue(now) {
		t.Error("never written: want due")
	}
	m := base
	m.lastRecordAt, m.lastRecordState = now.Add(-10*time.Second), "Idle"
	if m.recordDue(now) {
		t.Error("10s since write, same state: want not due")
	}
	m.lastRecordAt = now.Add(-30 * time.Second)
	if !m.recordDue(now) {
		t.Error("30s since write: want due")
	}
	m.lastRecordAt = now.Add(-5 * time.Second)
	m.state = State{Kind: StateThinking}
	if !m.recordDue(now) {
		t.Error("state changed: want due")
	}

	for name, mm := range map[string]model{
		"no pane":    {sessionID: "abc"},
		"no session": {selfPane: "%1"},
		"teardown":   {selfPane: "%1", sessionID: "abc", teardown: teardownExiting},
	} {
		if mm.recordDue(now) {
			t.Errorf("%s: want not due", name)
		}
	}
}

func TestSessionRecordFor(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	m := model{
		sessionID: "abc", sessionCwd: "/p/x/.claude/worktrees/w", workDir: "/p/x",
		state: State{Kind: StateTool, ToolName: "Bash"}, summary: Summary{Topic: "Fix it"},
	}
	r := m.sessionRecordFor(now)
	want := sessionRecord{SessionID: "abc", ClaudeCwd: "/p/x/.claude/worktrees/w",
		State: "Tool:Bash", Topic: "Fix it", LastSeen: now.Unix()}
	if r != want {
		t.Errorf("got %+v, want %+v", r, want)
	}
	m.sessionCwd = ""
	if r := m.sessionRecordFor(now); r.ClaudeCwd != "/p/x" {
		t.Errorf("fallback cwd = %q, want workDir", r.ClaudeCwd)
	}
}

func TestParseSessionNamePath(t *testing.T) {
	name, path, ok := parseSessionNamePath("remix-2\t/Users/m/Projects/remix\n")
	if !ok || name != "remix-2" || path != "/Users/m/Projects/remix" {
		t.Errorf("got %q %q %v", name, path, ok)
	}
	for _, bad := range []string{"", "\t/p", "name-only"} {
		if _, _, ok := parseSessionNamePath(bad); ok {
			t.Errorf("parseSessionNamePath(%q) ok, want failure", bad)
		}
	}
}
```

Before writing the test, confirm the `State` struct's field names (`Kind`, `ToolName`) by reading `state.go`, and that `statePublishValue(State{Kind: StateTool, ToolName: "Bash"})` returns `"Tool:Bash"` (statepub.go). Adjust the literal if the fields differ.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/claudemux-head && go test -run 'RecordDue|SessionRecordFor|ParseSessionNamePath' ./...`
Expected: FAIL — undefined methods/fields.

- [ ] **Step 3: Implement `headrecord.go`**

```go
package main

import (
	"context"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The head's half of reboot restore: keep this session's record fresh.
// See sessionrecord.go for why the record exists.

// sessionRecordInterval is the heartbeat period. It sets how stale the
// interrupted flag can be, and must stay well under lostClusterWindow.
const sessionRecordInterval = 30 * time.Second

// recordWrittenMsg carries the tmux session name the record was written
// under, so teardown can delete exactly that file.
type recordWrittenMsg struct{ name string }

// recordDue reports whether a heartbeat should be written now: inside tmux,
// bound to a session that can be resumed, not tearing down (a write racing
// the teardown's delete would bring the record back), and either the
// interval has passed or the published state changed — so a record never
// says Idle about a session that died mid-turn a few seconds later.
func (m model) recordDue(now time.Time) bool {
	if m.selfPane == "" || m.sessionID == "" || m.teardown != teardownIdle {
		return false
	}
	if m.lastRecordAt.IsZero() || now.Sub(m.lastRecordAt) >= sessionRecordInterval {
		return true
	}
	return statePublishValue(m.state) != m.lastRecordState
}

// sessionRecordFor builds the record from what the head knows. The session
// name and launch dir come from tmux, in writeRecordCmd, off the Update loop.
// ClaudeCwd prefers the cwd the transcript last recorded (a session that
// entered a worktree resumes from there) and falls back to the head's own
// directory.
func (m model) sessionRecordFor(now time.Time) sessionRecord {
	cwd := m.sessionCwd
	if cwd == "" {
		cwd = m.workDir
	}
	return sessionRecord{
		SessionID: m.sessionID,
		ClaudeCwd: cwd,
		State:     statePublishValue(m.state),
		Topic:     m.summary.Topic,
		LastSeen:  now.Unix(),
	}
}

// parseSessionNamePath splits "#{session_name}\t#{session_path}" output.
func parseSessionNamePath(out string) (name, path string, ok bool) {
	name, path, found := strings.Cut(strings.TrimRight(out, "\n"), "\t")
	if !found || name == "" || path == "" {
		return "", "", false
	}
	return name, path, true
}

// writeRecordCmd resolves this pane's session name and launch dir and writes
// the record. Best-effort: any failure is silent (nil message) and the next
// heartbeat tries again — the display never depends on it.
func writeRecordCmd(selfPane, dir string, r sessionRecord) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "-t", selfPane,
			"#{session_name}\t#{session_path}").Output()
		if err != nil {
			return nil
		}
		name, path, ok := parseSessionNamePath(string(out))
		if !ok {
			return nil
		}
		r.SessionName, r.LaunchDir = name, path
		if writeSessionRecord(dir, r) != nil {
			return nil
		}
		return recordWrittenMsg{name: name}
	}
}
```

- [ ] **Step 4: Wire into `tui.go`**

In the `model` struct, next to `publishedState`, add:

```go
	// Reboot-restore heartbeat (headrecord.go). recordName is the tmux
	// session name the last record was written under — what teardown
	// deletes. lastRecordAt/lastRecordState drive recordDue.
	recordName      string
	lastRecordAt    time.Time
	lastRecordState string
```

In `case tickMsg:`, right after `now := time.Time(msg)`, add:

```go
		if m.recordDue(now) {
			m.lastRecordAt = now
			m.lastRecordState = statePublishValue(m.state)
			cmds = append(cmds, writeRecordCmd(m.selfPane, sessionRecordDir(), m.sessionRecordFor(now)))
		}
```

Add a case to `Update`:

```go
	case recordWrittenMsg:
		m.recordName = msg.name
		return m, nil
```

In `case claudeGoneMsg:`, immediately before `return m, killSessionCmd(m.selfPane)`, add:

```go
		// Ended on purpose: never offer this session back after a reboot.
		removeSessionRecord(sessionRecordDir(), m.recordName)
```

- [ ] **Step 5: Run the full suite**

Run: `cd cmd/claudemux-head && go test ./... && go vet ./...`
Expected: PASS (no existing test should change behavior — `recordDue` is false in any test model without `selfPane`).

- [ ] **Step 6: Commit**

```bash
git add cmd/claudemux-head/headrecord.go cmd/claudemux-head/headrecord_test.go cmd/claudemux-head/tui.go
git commit -m "head: heartbeat a session record; teardown deletes it"
```

---

### Task 4: Launcher options `-r`, `-N`, `-C`

**Files:**
- Modify: `bin/claudemux` (header comment ~lines 1-45; getopts ~line 56; `run_in_pane` ~line 577; `create_session` ~lines 788-980)
- Test: `cmd/claudemux-head/launchertargets_test.go`

**Interfaces:**
- Produces (used by Task 5): `claudemux -d -W -N <name> -r <session-id> [-C <dir>] -- <launch_dir>` creates a detached session named exactly `<name>`, prints the name, exits 0; exits 1 with a message on stderr if the id is invalid or the name is taken.

- [ ] **Step 1: Write the failing tests** (append to `launchertargets_test.go`; add `"os/exec"` to its imports)

```go
// The resume id is spliced into claude's command line unquoted, so the
// launcher must reject anything outside the UUID alphabet before it does any
// work — checked before tmux or the hook are touched, which is also what
// makes it testable here.
func TestLauncherRejectsBadResumeID(t *testing.T) {
	cmd := exec.Command("bash", launcherPath, "-d", "-r", "bad id;rm", "/tmp")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("launcher accepted a bad resume id; output: %s", out)
	}
	if !strings.Contains(string(out), "invalid resume id") {
		t.Errorf("output %q does not mention the invalid resume id", out)
	}
}

func TestLauncherDeclaresRestoreOptions(t *testing.T) {
	src, err := os.ReadFile(launcherPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, `getopts "ndwWr:N:C:"`) {
		t.Error(`getopts does not declare r:, N:, C:`)
	}
	body, ok := shellFuncBody(s, "create_session")
	if !ok {
		t.Fatal("create_session not found")
	}
	for _, want := range []string{`--resume $RESUME_ID`, `EXACT_NAME`, `CLAUDE_DIR`} {
		if !strings.Contains(body, want) {
			t.Errorf("create_session does not use %s", want)
		}
	}
	rip, ok := shellFuncBody(s, "run_in_pane")
	if !ok || !strings.Contains(rip, `-c "$3"`) {
		t.Error("run_in_pane does not accept a directory as $3")
	}
}

func TestLauncherSyntax(t *testing.T) {
	if out, err := exec.Command("bash", "-n", launcherPath).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/claudemux-head && go test -run 'Launcher' ./...`
Expected: `TestLauncherRejectsBadResumeID` and `TestLauncherDeclaresRestoreOptions` FAIL.

- [ ] **Step 3: Implement in `bin/claudemux`**

Header comment: change the usage line to `# Usage: claudemux [-n] [-d] [-w|-W] [-N name] [-r session-id] [-C dir] [switch|setup|dir|query ...]` and add, after the `-W` block:

```bash
#   -N   Use exactly this tmux session name instead of the directory basename.
#        Fails if a session by that name exists (no attach, no -2 suffix):
#        the switchboard's reboot restore uses it to bring a session back
#        under its old name, and must never merge into another session.
#   -r   Resume this claude session id (`claude --resume <id>`).
#   -C   Start the claude pane in this directory instead of the launch dir —
#        claude finds a --resume id under the project dir of its cwd, and a
#        session that entered a worktree lives under the worktree's.
```

Replace the option parsing block:

```bash
RESUME_ID=""
EXACT_NAME=""
CLAUDE_DIR=""
while getopts "ndwWr:N:C:" opt; do
  case "$opt" in
    n) FORCE_NEW=true ;;
    d) DETACHED=true ;;
    w) worktree_mode_set force ;;
    W) worktree_mode_set skip ;;
    r) RESUME_ID="$OPTARG" ;;
    N) EXACT_NAME="$OPTARG" ;;
    C) CLAUDE_DIR="$OPTARG" ;;
    *) echo "Usage: claudemux [-n] [-d] [-w|-W] [-N name] [-r session-id] [-C dir] [switch|setup|dir|query ...]" >&2; exit 1 ;;
  esac
done
shift $((OPTIND - 1))

# The id is spliced into claude's command line unquoted (as the head's `c`
# restart does), so it must be a plain session id — same alphabet as
# resumeIDOK in cmd/claudemux-head/claudereboot.go.
if [ -n "$RESUME_ID" ] && ! [[ "$RESUME_ID" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "claudemux: invalid resume id: $RESUME_ID" >&2
  exit 1
fi
```

`run_in_pane` — accept an optional directory:

```bash
run_in_pane() {
  tmux set -p -t "$1" remain-on-exit failed 2>/dev/null || true
  # No -c by default: respawn-pane keeps the pane's working directory, which is
  # the work_dir it was created with (verified) — the same assumption the shell
  # pane's respawn in deferred_launch has always made. A third argument moves
  # the pane first (-C: resume claude from its recorded cwd); later respawns of
  # the same pane without -c keep that directory.
  if [ -n "${3:-}" ]; then
    tmux respawn-pane -k -t "$1" -c "$3" "exec $2"
  else
    tmux respawn-pane -k -t "$1" "exec $2"
  fi
}
```

In `create_session`, replace the start (from `session_name="$(basename "$work_dir")"` through the end of the `if tmux has-session` block) with:

```bash
  session_name="$(basename "$work_dir")"
  if [ -n "$EXACT_NAME" ]; then
    # Restore: the name is the whole point, so a clash is an error rather than
    # an attach or a -2 suffix.
    session_name="$EXACT_NAME"
    if tmux has-session -t "=$session_name" 2>/dev/null; then
      echo "claudemux: session $session_name already exists" >&2
      exit 1
    fi
  elif tmux has-session -t "=$session_name" 2>/dev/null; then
```

(keep the existing body of that `if` — the `FORCE_NEW` suffixing and `attach` — unchanged, now under the `elif`).

After `claude_cmd="$(printf '%q' "${claude_bin:-claude}") --permission-mode auto"`, add:

```bash
  # Before -n and the "/color X" prompt argument: --resume takes a value, and
  # the prompt must stay the last positional.
  if [ -n "$RESUME_ID" ]; then
    claude_cmd+=" --resume $RESUME_ID"
  fi
```

Add a local and resolve it once, near the other `local` declarations in `create_session`:

```bash
  local claude_dir=""
  # Only a directory that still exists; a removed worktree falls back to the
  # launch dir, and a failed resume there shows in the pane (remain-on-exit).
  if [ -n "$CLAUDE_DIR" ] && [ -d "$CLAUDE_DIR" ]; then
    claude_dir="$CLAUDE_DIR"
  fi
```

Pass it to both claude-pane launches:
- `run_in_pane "$claude_pane" "$holder_cmd" "$claude_dir"`
- `run_in_pane "$claude_pane" "$claude_cmd" "$claude_dir"`

Do NOT pass it to the head pane's `run_in_pane`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/claudemux-head && go test -run 'Launcher' ./... && command -v shellcheck && shellcheck ../../bin/claudemux`
Expected: PASS; shellcheck (if installed) reports nothing new versus `git stash`-free baseline — compare with `git show HEAD:bin/claudemux | shellcheck -` if in doubt.

- [ ] **Step 5: Manual check (real tmux, disposable session)**

```bash
id=$(ls -t ~/.claude/projects/*/*.jsonl | head -1 | xargs basename | sed 's/.jsonl$//')
bin/claudemux -d -W -N restore-test -r "$id" -C /tmp -- "$PWD"   # prints restore-test
tmux display-message -p -t restore-test: '#{session_path}'        # $PWD
tmux show -p -t "$(tmux list-panes -t restore-test: -F '#{pane_id} #{pane_current_command}' | awk '$2 ~ /claude|node/ {print $1; exit}')" @claudemux_claude_cmd   # contains --resume <id>
bin/claudemux -d -W -N restore-test -- "$PWD"; echo "exit=$?"     # "already exists", exit=1
tmux kill-session -t restore-test:
```

Record the output in the task report.

- [ ] **Step 6: Commit**

```bash
git add bin/claudemux cmd/claudemux-head/launchertargets_test.go
git commit -m "launcher: -N exact name, -r resume id, -C claude dir"
```

---

### Task 5: Lobby offer, picker and restore

**Files:**
- Create: `cmd/claudemux-head/swrestore.go`
- Test: `cmd/claudemux-head/swrestore_test.go`
- Modify: `cmd/claudemux-head/switchboardtui.go` (swModel struct ~line 253; `swSnapshotMsg` case ~line 647; `tea.KeyMsg` case ~line 812; `View` ~line 1086; `listWindow` ~line 1335)
- Modify: `cmd/claudemux-head/swpreview.go:130` and `cmd/claudemux-head/swpreview_test.go` (2 call sites)

**Interfaces:**
- Consumes: Task 1 (`sessionRecordDir`, `readSessionRecords`, `pruneSessionRecords`, `archiveSessionRecords`, `sessionRecordMaxAge`), Task 2 (`selectLost`, `lostSession`, `parseBootTime`, `restoreCutoff`), Task 4 (launcher flags), `readPaneRecord`/`paneMapDir` (panemap.go), `swSession.Name`/`.ClaudePane`, `swLastLine`.
- Produces:
  - `type swRestoreOffer struct` (fields below)
  - `type swRestoreScanMsg struct{ offer *swRestoreOffer }`
  - `type swRestoreStepMsg struct{ name string; err error }`
  - `func swRestoreScanCmd(sessions []swSession) tea.Cmd`
  - `func restoreArgs(l lostSession, dirExists func(string) bool) ([]string, error)`
  - `func swRestoreOneCmd(l lostSession) tea.Cmd`
  - `func swRestoreStripText(o *swRestoreOffer) string`
  - `func swRestorePickerLines(o *swRestoreOffer, now time.Time, width int) []string`
  - `func (o *swRestoreOffer) selected() []lostSession`
  - `computePreviewLayout(height int, hasErr, hasMeters, hasStrip bool, listWant int) swLayout`

- [ ] **Step 1: Write the failing tests**

```go
package main

import (
	"strings"
	"testing"
	"time"
)

func lostRec(name, id, launch, cwd string, lastSeen int64, interrupted bool) lostSession {
	return lostSession{
		loadedRecord: loadedRecord{Path: "/d/" + name + ".json", Rec: sessionRecord{
			SessionName: name, SessionID: id, LaunchDir: launch, ClaudeCwd: cwd,
			LastSeen: lastSeen, Topic: "topic " + name,
		}},
		Interrupted: interrupted,
	}
}

func TestRestoreArgs(t *testing.T) {
	exists := func(p string) bool { return p != "/gone" }
	args, err := restoreArgs(lostRec("remix-2", "abc", "/p/remix", "/p/remix/wt", 1, false), exists)
	if err != nil {
		t.Fatal(err)
	}
	want := "-d -W -N remix-2 -r abc -C /p/remix/wt -- /p/remix"
	if got := strings.Join(args, " "); got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
	// cwd same as launch dir, or gone: no -C.
	for _, cwd := range []string{"/p/remix", "/gone", ""} {
		args, _ := restoreArgs(lostRec("r", "abc", "/p/remix", cwd, 1, false), exists)
		if strings.Contains(strings.Join(args, " "), "-C") {
			t.Errorf("cwd %q: unexpected -C in %v", cwd, args)
		}
	}
	if _, err := restoreArgs(lostRec("r", "abc", "/gone", "", 1, false), exists); err == nil {
		t.Error("missing launch dir: want error")
	}
	if _, err := restoreArgs(lostRec("r", "bad id", "/p", "", 1, false), exists); err == nil {
		t.Error("bad session id: want error")
	}
}

func TestSwRestoreStripText(t *testing.T) {
	newest := time.Date(2026, 9, 18, 13, 52, 0, 0, time.Local).Unix()
	o := &swRestoreOffer{newest: newest, lost: []lostSession{
		lostRec("a", "1", "/p", "", newest, false),
		lostRec("b", "2", "/p", "", newest, true),
	}}
	got := swRestoreStripText(o)
	for _, want := range []string{"2 sessions were running before the reboot", "Sep 18 13:52", "r restore all", "s select", "x dismiss"} {
		if !strings.Contains(got, want) {
			t.Errorf("strip %q missing %q", got, want)
		}
	}
	o.lost = o.lost[:1]
	if got := swRestoreStripText(o); !strings.Contains(got, "1 session was running") {
		t.Errorf("singular: %q", got)
	}
	o.busy, o.done, o.total = true, 1, 2
	if got := swRestoreStripText(o); !strings.Contains(got, "restoring 2/2") {
		t.Errorf("busy: %q", got)
	}
	o.busy, o.done, o.total, o.failed = false, 2, 2, []string{"b: launch dir gone"}
	if got := swRestoreStripText(o); !strings.Contains(got, "restored 1/2") || !strings.Contains(got, "b: launch dir gone") || !strings.Contains(got, "x dismiss") {
		t.Errorf("failures: %q", got)
	}
}

func TestSwRestorePickerAndSelected(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	o := &swRestoreOffer{lost: []lostSession{
		lostRec("a", "1", "/p", "", now.Unix()-120, false),
		lostRec("b", "2", "/p", "", now.Unix()-60, true),
	}}
	o.startPicking()
	if len(o.selected()) != 2 {
		t.Fatalf("picker starts with everything checked")
	}
	o.toggle() // cursor 0
	if sel := o.selected(); len(sel) != 1 || sel[0].Rec.SessionName != "b" {
		t.Fatalf("selected = %+v", sel)
	}
	lines := swRestorePickerLines(o, now, 80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "[ ] a") || !strings.Contains(joined, "[x] b") || !strings.Contains(joined, "⚡ interrupted") {
		t.Errorf("picker:\n%s", joined)
	}
	o.moveCursor(5)
	if o.cursor != 1 {
		t.Errorf("cursor clamps to last row, got %d", o.cursor)
	}
	o.moveCursor(-5)
	if o.cursor != 0 {
		t.Errorf("cursor clamps to first row, got %d", o.cursor)
	}
}

func TestSwRestoreKeys(t *testing.T) {
	m := newSwModel("%0")
	m.restore = &swRestoreOffer{lost: []lostSession{lostRec("a", "1", "/p", "", 1, false)}}
	m.restoreArchive = func(*swRestoreOffer) {} // no disk in tests

	// x dismisses.
	mm, _ := m.Update(keyRunes("x"))
	if mm.(swModel).restore != nil {
		t.Error("x did not dismiss the offer")
	}
	// s opens the picker; esc backs out; enter with everything unchecked archives and ends.
	mm, _ = m.Update(keyRunes("s"))
	sm := mm.(swModel)
	if !sm.restore.picking {
		t.Fatal("s did not open the picker")
	}
	mm, _ = sm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if mm.(swModel).restore.picking {
		t.Error("esc did not close the picker")
	}
	// r starts a restore: busy, with a command to run.
	mm, cmd := m.Update(keyRunes("r"))
	if !mm.(swModel).restore.busy || cmd == nil {
		t.Error("r did not start restoring")
	}
	// Keys do nothing with no offer.
	m.restore = nil
	mm, _ = m.Update(keyRunes("x"))
	if mm.(swModel).restore != nil {
		t.Error("offer appeared from nowhere")
	}
}

func TestSwRestoreStepAdvances(t *testing.T) {
	m := newSwModel("%0")
	m.restore = &swRestoreOffer{busy: true, total: 2, queue: []lostSession{lostRec("b", "2", "/p", "", 1, false)}}
	mm, cmd := m.Update(swRestoreStepMsg{name: "a"})
	sm := mm.(swModel)
	if sm.restore.done != 1 || cmd == nil || len(sm.restore.queue) != 0 {
		t.Fatalf("after first step: %+v cmd=%v", sm.restore, cmd != nil)
	}
	mm, _ = sm.Update(swRestoreStepMsg{name: "b", err: errString("boom")})
	sm = mm.(swModel)
	if sm.restore == nil || sm.restore.busy || len(sm.restore.failed) != 1 {
		t.Fatalf("after failed last step: %+v", sm.restore)
	}
	// All succeeded: the strip goes away.
	m.restore = &swRestoreOffer{busy: true, total: 1}
	mm, _ = m.Update(swRestoreStepMsg{name: "a"})
	if mm.(swModel).restore != nil {
		t.Error("clean restore left the strip up")
	}
}
```

Before writing these tests: check `switchboardtui_test.go` for an existing helper that builds a rune `tea.KeyMsg` (search for `KeyRunes`); reuse it under its real name instead of `keyRunes` if one exists, otherwise add to `swrestore_test.go`:

```go
func keyRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

type errString string

func (e errString) Error() string { return string(e) }
```

and import `tea "github.com/charmbracelet/bubbletea"`.

Also update the two `computePreviewLayout(...)` calls in `swpreview_test.go` to pass `false` for the new `hasStrip` argument, and add:

```go
func TestComputePreviewLayoutStripCostsARow(t *testing.T) {
	without := computePreviewLayout(40, false, false, false, 100)
	with := computePreviewLayout(40, false, false, true, 100)
	if with.ListRows+with.Content+2 != without.ListRows+without.Content+2-1 {
		t.Errorf("strip did not cost exactly one row: without=%+v with=%+v", without, with)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/claudemux-head && go test -run 'Restore|ComputePreviewLayout' ./...`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Implement `swrestore.go`**

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The lobby's half of reboot restore: find the sessions a reboot killed,
// offer them once, and relaunch the chosen ones through the launcher.
// See docs/superpowers/specs/2026-09-19-reboot-restore-design.md.

// swRestoreOffer is the offer strip's state. nil on swModel means no strip.
type swRestoreOffer struct {
	lost   []lostSession
	cutoff int64 // names the archive dir
	newest int64 // shown as "before the reboot (Sep 18 13:52)"

	picking bool
	checked []bool
	cursor  int

	busy   bool
	queue  []lostSession // still to launch, in order
	done   int
	total  int
	failed []string // "name: reason"
}

type swRestoreScanMsg struct{ offer *swRestoreOffer }

type swRestoreStepMsg struct {
	name string
	err  error
}

func (o *swRestoreOffer) startPicking() {
	o.picking = true
	o.cursor = 0
	o.checked = make([]bool, len(o.lost))
	for i := range o.checked {
		o.checked[i] = true
	}
}

func (o *swRestoreOffer) toggle() {
	if o.cursor >= 0 && o.cursor < len(o.checked) {
		o.checked[o.cursor] = !o.checked[o.cursor]
	}
}

func (o *swRestoreOffer) moveCursor(d int) {
	o.cursor += d
	if o.cursor >= len(o.lost) {
		o.cursor = len(o.lost) - 1
	}
	if o.cursor < 0 {
		o.cursor = 0
	}
}

// selected is what Enter restores: the checked rows while picking,
// everything otherwise.
func (o *swRestoreOffer) selected() []lostSession {
	if !o.picking {
		return o.lost
	}
	var out []lostSession
	for i, l := range o.lost {
		if i < len(o.checked) && o.checked[i] {
			out = append(out, l)
		}
	}
	return out
}

// archiveRestoreOffer moves every offered record out of the live dir. The
// offer is a one-time decision: unchecked rows are archived too.
func archiveRestoreOffer(o *swRestoreOffer) {
	paths := make([]string, 0, len(o.lost))
	for _, l := range o.lost {
		paths = append(paths, l.Path)
	}
	_ = archiveSessionRecords(sessionRecordDir(), o.cutoff, paths)
}

// swRestoreScanCmd looks for lost sessions. It runs once per lobby, on the
// first successful snapshot — the live fleet is needed to exclude sessions
// already running again. A nil offer means nothing to restore.
func swRestoreScanCmd(sessions []swSession) tea.Cmd {
	liveNames := make(map[string]bool, len(sessions))
	var panes []string
	for _, s := range sessions {
		liveNames[s.Name] = true
		if s.ClaudePane != "" {
			panes = append(panes, s.ClaudePane)
		}
	}
	return func() tea.Msg {
		dir := sessionRecordDir()
		pruneSessionRecords(dir, time.Now(), sessionRecordMaxAge)
		recs := readSessionRecords(dir)
		if len(recs) == 0 {
			return swRestoreScanMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		bootOut, bootErr := exec.CommandContext(ctx, "sysctl", "-n", "kern.boottime").Output()
		boot, bootOK := int64(0), false
		if bootErr == nil {
			boot, bootOK = parseBootTime(string(bootOut))
		}
		startOut, startErr := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#{start_time}").Output()
		start, startOK := int64(0), false
		if startErr == nil {
			if v, err := strconv.ParseInt(strings.TrimSpace(string(startOut)), 10, 64); err == nil && v > 0 {
				start, startOK = v, true
			}
		}
		cutoff, ok := restoreCutoff(boot, bootOK, start, startOK)
		if !ok {
			return swRestoreScanMsg{}
		}
		liveIDs := map[string]bool{}
		for _, p := range panes {
			if pm, ok := readPaneRecord(paneMapDir(), p); ok {
				liveIDs[pm.SessionID] = true
			}
		}
		lost, newest := selectLost(recs, cutoff, liveNames, liveIDs)
		if len(lost) == 0 {
			return swRestoreScanMsg{}
		}
		return swRestoreScanMsg{offer: &swRestoreOffer{lost: lost, cutoff: cutoff, newest: newest}}
	}
}

// restoreArgs is the launcher argv (after "claudemux") for one session.
// -W: the session already has whatever worktree it had. -C only when the
// recorded cwd still exists and differs from the launch dir.
func restoreArgs(l lostSession, dirExists func(string) bool) ([]string, error) {
	r := l.Rec
	if !resumeIDOK(r.SessionID) || r.SessionID == "" {
		return nil, fmt.Errorf("bad session id %q", r.SessionID)
	}
	if r.LaunchDir == "" || !dirExists(r.LaunchDir) {
		return nil, errors.New("launch dir gone")
	}
	args := []string{"-d", "-W", "-N", r.SessionName, "-r", r.SessionID}
	if r.ClaudeCwd != "" && r.ClaudeCwd != r.LaunchDir && dirExists(r.ClaudeCwd) {
		args = append(args, "-C", r.ClaudeCwd)
	}
	return append(args, "--", r.LaunchDir), nil
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// swRestoreOneCmd relaunches one session. Same timeout and error reporting
// as swCreateCmd: the launcher's last stderr line says why it failed.
func swRestoreOneCmd(l lostSession) tea.Cmd {
	return func() tea.Msg {
		name := l.Rec.SessionName
		args, err := restoreArgs(l, dirExists)
		if err != nil {
			return swRestoreStepMsg{name: name, err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "claudemux", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			if detail := swLastLine(stderr.String()); detail != "" {
				err = errors.New(detail)
			}
			return swRestoreStepMsg{name: name, err: err}
		}
		return swRestoreStepMsg{name: name}
	}
}

// startRestore archives the offer and queues the selected sessions. Returns
// the first launch command, or nil when nothing was selected (the offer is
// then finished).
func (m *swModel) startRestore() tea.Cmd {
	o := m.restore
	sel := o.selected()
	m.restoreArchive(o)
	if len(sel) == 0 {
		m.restore = nil
		return nil
	}
	o.picking = false
	o.busy = true
	o.total = len(sel)
	o.done = 0
	o.queue = sel[1:]
	return swRestoreOneCmd(sel[0])
}

// swRestoreStripText is the one-line strip above the fleet list.
func swRestoreStripText(o *swRestoreOffer) string {
	switch {
	case o.busy:
		return fmt.Sprintf("⏻ restoring %d/%d…", o.done+1, o.total)
	case o.total > 0:
		return fmt.Sprintf("⏻ restored %d/%d · failed: %s · x dismiss",
			o.total-len(o.failed), o.total, strings.Join(o.failed, ", "))
	}
	n := len(o.lost)
	noun := "sessions were"
	if n == 1 {
		noun = "session was"
	}
	when := time.Unix(o.newest, 0).Format("Jan 2 15:04")
	return fmt.Sprintf("⏻ %d %s running before the reboot (%s) · r restore all · s select · x dismiss", n, noun, when)
}

// swRestorePickerLines renders the checklist, one line per lost session.
func swRestorePickerLines(o *swRestoreOffer, now time.Time, width int) []string {
	lines := []string{"restore which sessions?  space toggle · enter restore · esc back"}
	for i, l := range o.lost {
		cur := "  "
		if i == o.cursor {
			cur = "▸ "
		}
		box := "[ ]"
		if i < len(o.checked) && o.checked[i] {
			box = "[x]"
		}
		age := formatDuration(now.Sub(time.Unix(l.Rec.LastSeen, 0)))
		line := fmt.Sprintf("%s%s %s  %s  %s", cur, box, l.Rec.SessionName, age, l.Rec.Topic)
		if l.Interrupted {
			line += "  ⚡ interrupted"
		}
		if width > 0 {
			line = clipLine(line, width)
		}
		lines = append(lines, line)
	}
	return lines
}
```

Note: `formatDuration` and `clipLine` already exist in `tui.go`; `resumeIDOK` in `claudereboot.go`. If `formatDuration`'s signature differs from `func(time.Duration) string`, adapt the call.

- [ ] **Step 4: Wire into `switchboardtui.go` and `swpreview.go`**

`swpreview.go` — add the parameter and budget the row:

```go
func computePreviewLayout(height int, hasErr, hasMeters, hasStrip bool, listWant int) swLayout {
	chrome := swChromeRows
	if hasErr {
		chrome++
	}
	if hasMeters {
		chrome++
	}
	// The reboot-restore offer strip (swrestore.go) sits under the meters.
	if hasStrip {
		chrome++
	}
```

and update its doc comment on `swChromeRows` to mention the strip. In `listWindow`, pass `m.restore != nil && !m.restore.picking`.

`swModel` struct — add:

```go
	// Reboot restore (swrestore.go). restore is the offer strip, nil when
	// there is none; restoreScanned stops the scan after the first
	// successful snapshot. restoreArchive is archiveRestoreOffer, a field
	// so tests don't touch ~/.claude.
	restore        *swRestoreOffer
	restoreScanned bool
	restoreArchive func(*swRestoreOffer)
```

In `newSwModel`, set `m.restoreArchive = archiveRestoreOffer` before `return m`.

In `case swSnapshotMsg:` — after the snapshot has been applied successfully (after the existing `msg.err` early return, where `m.snap = msg.snap` is assigned), add:

```go
		if !m.restoreScanned {
			m.restoreScanned = true
			cmds = append(cmds, swRestoreScanCmd(m.snap.Sessions))
		}
```

Read the case first: if it does not build a `cmds` slice, batch the scan with whatever it returns (`tea.Batch(existing, swRestoreScanCmd(...))`).

Add cases to `Update`:

```go
	case swRestoreScanMsg:
		m.restore = msg.offer
		return m, nil
	case swRestoreStepMsg:
		o := m.restore
		if o == nil || !o.busy {
			return m, nil
		}
		if msg.err != nil {
			o.failed = append(o.failed, msg.name+": "+msg.err.Error())
		}
		o.done++
		if len(o.queue) > 0 {
			next := o.queue[0]
			o.queue = o.queue[1:]
			return m, swRestoreOneCmd(next)
		}
		o.busy = false
		if len(o.failed) == 0 {
			m.restore = nil
		}
		return m, nil
```

In `case tea.KeyMsg:`, after the `if m.deferring { ... }` block and before `switch msg.String()`, add:

```go
		if o := m.restore; o != nil && !o.busy {
			if o.picking {
				switch msg.String() {
				case "esc":
					o.picking = false
				case "j", "down":
					o.moveCursor(1)
				case "k", "up":
					o.moveCursor(-1)
				case " ", "space":
					o.toggle()
				case "enter":
					return m, m.startRestore()
				case "ctrl+c":
					return m, tea.Quit
				}
				return m, nil
			}
			switch msg.String() {
			case "r":
				if o.total == 0 {
					return m, m.startRestore()
				}
			case "s":
				if o.total == 0 {
					o.startPicking()
					return m, nil
				}
			case "x":
				if o.total == 0 {
					m.restoreArchive(o)
				}
				m.restore = nil
				return m, nil
			}
		}
```

(`o.total > 0` means a restore already ran and the strip only reports failures: `r`/`s` fall through to the normal lobby keys, `x` clears it without re-archiving.) Check that Bubble Tea reports the space key as `" "` or `"space"` in this version — the case lists both.

In `View`, after the meters line block and before `b.WriteString("\n")`:

```go
	if o := m.restore; o != nil {
		strip := swWaitStyle.Render(swRestoreStripText(o))
		if m.width > 0 {
			strip = clipLine(strip, m.width)
		}
		b.WriteString(strip + "\n")
		if o.picking {
			b.WriteString("\n")
			for _, l := range swRestorePickerLines(o, now, m.width) {
				b.WriteString(l + "\n")
			}
			return b.String()
		}
	}
```

(Use whichever existing lipgloss style reads as "attention" — `swWaitStyle` if it exists; check the style vars at the top of `switchboardtui.go`.) The picker replaces the fleet list and preview while open, which is why `listWindow` does not budget the strip in picking mode.

- [ ] **Step 5: Run the full suite**

Run: `cd cmd/claudemux-head && go test ./... && go vet ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/claudemux-head/swrestore.go cmd/claudemux-head/swrestore_test.go cmd/claudemux-head/switchboardtui.go cmd/claudemux-head/swpreview.go cmd/claudemux-head/swpreview_test.go
git commit -m "lobby: offer to restore the sessions a reboot killed"
```

---

### Task 6: README and end-to-end check

**Files:**
- Modify: `README.md` (new section after **Deferring a session** within **The switchboard**; and add `-N`/`-r`/`-C` wherever launcher options are listed — search for `-W`)

- [ ] **Step 1: Write the README section**

```markdown
### After a reboot

Every session head keeps a small record of its session in
`~/.claude/claudemux/sessions/`, refreshed every 30 seconds. When the machine
reboots — an OS update, a crash — or the tmux server dies, the first switchboard
you open finds the sessions that were running at the time and offers them back:

    ⏻ 7 sessions were running before the reboot (Sep 18 13:52) · r restore all · s select · x dismiss

`r` brings them all back, `s` opens a checklist (space toggles, enter restores),
`x` dismisses. Each restored session gets its old name, its project's usual
layout, and claude resumed on the same conversation (`claude --resume`), started
in the directory it was last working in — a worktree, if it had entered one.
Restored sessions wait at their prompt; ones that were mid-turn when the machine
went down are marked `⚡ interrupted` in the checklist so you know which to nudge.

The offer is made once per boot: whatever you choose, the records move to
`sessions/restored-<time>/`. Sessions you ended with the head's teardown are
never offered, and neither are sessions you closed well before the reboot —
only the ones that stopped together, within ten minutes of the last.
```

Also document the launcher flags next to `-w`/`-W` in whatever options list the README has, one line each, matching `bin/claudemux`'s header comment from Task 4.

- [ ] **Step 2: Build and install locally**

Per the repo's dev-link setup: `cd cmd/claudemux-head && go install .` (the launcher in the main checkout is symlinked; this worktree's `bin/claudemux` must be used explicitly for the E2E below, or run the E2E after merge).

- [ ] **Step 3: End-to-end (disposable tmux server via `-L`, so the user's real fleet is untouched)**

Not practical with a separate `-L` socket, because the launcher and lobby talk to the default server. Instead, after merge and install, the user runs this with their consent:

1. Launch two sessions in scratch projects; send one a long-running prompt so it is mid-turn, have the other enter a worktree.
2. Wait 35s (a heartbeat). Confirm two files in `~/.claude/claudemux/sessions/`.
3. `tmux kill-server`.
4. Run `claudemux`. Expect the strip with `2 sessions`; `s` shows the mid-turn one `⚡ interrupted`.
5. `r`. Both sessions reappear under their names; each claude pane shows the resumed conversation; the worktree one's claude pane is in the worktree (`tmux display -p -t <pane> '#{pane_current_path}'`).
6. `ls ~/.claude/claudemux/sessions/restored-*/` holds both records; reopening the lobby shows no strip.

The implementer does NOT run steps 3-6 (they kill the user's live sessions); record them in the task report for the user to run.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: reboot restore"
```
