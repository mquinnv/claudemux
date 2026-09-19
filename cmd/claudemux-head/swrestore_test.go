package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func keyRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

type errString string

func (e errString) Error() string { return string(e) }

func lostRec(name, id, launch, cwd string, lastSeen int64, interrupted bool) lostSession {
	return lostSession{
		loadedRecord: loadedRecord{Path: "/d/" + name + ".json", Rec: sessionRecord{
			SessionName: name, SessionID: id, LaunchDir: launch, ClaudeCwd: cwd,
			LastSeen: lastSeen, Topic: "topic " + name,
		}},
		Interrupted: interrupted,
	}
}

// TestLiveSessionIDs covers finding 4: pane-map files are keyed by pane
// number, which restarts with the tmux server, so a stale pre-reboot map
// file left at a since-reused pane number must not mark a lost session's id
// as "live" just because some pane happens to hold that number again.
func TestLiveSessionIDs(t *testing.T) {
	dir := t.TempDir()
	const cutoff = 1_000

	writePane := func(pane, sessionID string, mtime int64) {
		path := filepath.Join(dir, pane+".json")
		data, _ := json.Marshal(paneMap{SessionID: sessionID, TranscriptPath: "/t", Cwd: "/c"})
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		ts := time.Unix(mtime, 0)
		if err := os.Chtimes(path, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	writePane("1", "fresh-id", cutoff+5)  // written by the current tmux server: live
	writePane("2", "stale-id", cutoff-5)  // pre-reboot leftover: not live
	writePane("3", "onedge-id", cutoff)   // exactly at cutoff: counts as live

	got := liveSessionIDs(dir, []string{"%1", "%2", "%3", "%4"}, cutoff)
	want := map[string]bool{"fresh-id": true, "onedge-id": true}
	if len(got) != len(want) || !got["fresh-id"] || !got["onedge-id"] || got["stale-id"] {
		t.Errorf("got %v, want %v", got, want)
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

// TestArchiveRestoreOfferArchivesCluster covers finding 5: excluded and
// deduped records (in cluster, not in lost) must be archived along with the
// offered ones, or a later lobby run in the same boot re-offers them.
func TestArchiveRestoreOfferArchivesCluster(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := sessionRecordDir()
	_ = writeSessionRecord(dir, sessionRecord{SessionName: "lost-one", SessionID: "1"})
	_ = writeSessionRecord(dir, sessionRecord{SessionName: "already-live", SessionID: "2"})
	_ = writeSessionRecord(dir, sessionRecord{SessionName: "dup-old-name", SessionID: "3"})

	o := &swRestoreOffer{
		lost: []lostSession{lostRec("lost-one", "1", "/p", "", 1, false)},
		cluster: []string{
			sessionRecordFile(dir, "lost-one"),
			sessionRecordFile(dir, "already-live"),
			sessionRecordFile(dir, "dup-old-name"),
		},
		cutoff: 77,
	}
	archiveRestoreOffer(o)

	if got := readSessionRecords(dir); len(got) != 0 {
		t.Errorf("records still live after archive: %+v", got)
	}
	archived := readSessionRecords(filepath.Join(dir, "restored-77"))
	if len(archived) != 3 {
		t.Errorf("archive holds %d records, want 3 (lost + excluded + duplicate)", len(archived))
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

// While picking, the strip's own hint text ("r restore all · s select · x
// dismiss") must not appear above the checklist — those keys do nothing in
// picking mode, and the picker's own header line already says what does.
func TestSwRestoreViewPickingHidesStripText(t *testing.T) {
	m := newSwModel("%0")
	m.width, m.height = 80, 24
	m.snap = swSnapshot{Sessions: []swSession{{Name: "api", State: "Idle"}}}
	m.restore = &swRestoreOffer{lost: []lostSession{lostRec("a", "1", "/p", "", 1, false)}}
	m.restore.startPicking()

	view := m.View()
	if strings.Contains(view, "r restore all") || strings.Contains(view, "s select") {
		t.Errorf("strip hint text must not render while picking:\n%s", view)
	}
	if !strings.Contains(view, "restore which sessions?") {
		t.Errorf("picker header missing:\n%s", view)
	}
}
