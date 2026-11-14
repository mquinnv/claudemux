package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The dialog as Claude Code 2.1.323 draws it in a folder it has never opened,
// captured from a real pane.
const trustDialogCapture = `────────────────────────────────────────────────────────────
 Accessing workspace:

 /Users/x/Projects/system-notes

 Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project, or work from your team). If not,
 take a moment to review what's in this folder first.

 Claude Code'll be able to read, edit, and execute files here.

 Security guide

 ❯ No, exit
   Yes, I trust this folder

 Enter to confirm · Esc to cancel



`

func TestTrustDialogUp(t *testing.T) {
	if !trustDialogUp(trustDialogCapture) {
		t.Error("the trust dialog was not recognized")
	}
}

// A permission dialog shares the footer but is not the trust dialog, and a
// conversation that merely mentions trusting a folder is no dialog at all.
func TestTrustDialogUpRejectsLookalikes(t *testing.T) {
	for name, capture := range map[string]string{
		"permission dialog": "Bash command\n\n  rm -rf build\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel · Tab to amend\n",
		"conversation":      "⏺ Answer \"Yes, I trust this folder\" in that pane.\n\n────────\n❯ \n────────\n  -- INSERT --\n",
		"empty":             "",
	} {
		if trustDialogUp(capture) {
			t.Errorf("%s read as the trust dialog", name)
		}
	}
}

// A pane id is only unique within one tmux server: a map file written before
// this server started belongs to some other session that once held the id.
// While the trust dialog is up no hook has run to overwrite it, so binding to
// it showed that stranger's transcript — Idle — for as long as the dialog sat.
func TestBindCandidatesIgnoresMapFromEarlierServer(t *testing.T) {
	dir, projectsDir := t.TempDir(), t.TempDir()
	projectDir := filepath.Join(projectsDir, "-Users-x-other")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(projectDir, "old.jsonl")
	if err := os.WriteFile(stale, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mapFile := filepath.Join(dir, "7.json")
	body := `{"session_id":"old","transcript_path":"` + stale + `","cwd":"/Users/x/other"}`
	if err := os.WriteFile(mapFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	serverStart := time.Now().Add(-time.Hour)
	before := serverStart.Add(-time.Hour)
	if err := os.Chtimes(mapFile, before, before); err != nil {
		t.Fatal(err)
	}

	transcript, cwd, pane := bindCandidates([]string{"%7"}, map[string]string{"%7": "/new"}, dir, projectsDir, serverStart)
	if transcript != "" || cwd != "/new" || pane != "%7" {
		t.Errorf("= (%q, %q, %q), want (\"\", /new, %%7)", transcript, cwd, pane)
	}

	// The same file written after the server started is this server's own.
	after := serverStart.Add(time.Minute)
	if err := os.Chtimes(mapFile, after, after); err != nil {
		t.Fatal(err)
	}
	if transcript, _, _ := bindCandidates([]string{"%7"}, nil, dir, projectsDir, serverStart); transcript != stale {
		t.Errorf("transcript = %q, want %q", transcript, stale)
	}
}

func TestTrustWatchProbesOnlyAnUnmappedWaitingPane(t *testing.T) {
	now := time.Unix(1794621765, 0)
	for name, c := range map[string]struct {
		waiting, unmapped bool
		pane              string
		want              bool
	}{
		"waiting, unmapped":        {true, true, "%7", true},
		"hook already wrote a map": {true, false, "%7", false},
		"bound to a transcript":    {false, true, "%7", false},
		"no claude pane":           {true, true, "", false},
	} {
		var w trustWatch
		if got := w.probeDue(c.waiting, c.unmapped, c.pane, now); got != c.want {
			t.Errorf("%s: probeDue = %v, want %v", name, got, c.want)
		}
	}
}

func TestTrustWatchPacesAndSerializesProbes(t *testing.T) {
	now := time.Unix(1794621765, 0)
	var w trustWatch
	if !w.probeDue(true, true, "%7", now) {
		t.Fatal("first probe not due")
	}
	w.started(now)
	if w.probeDue(true, true, "%7", now.Add(trustProbeEvery)) {
		t.Error("probe due while one is in flight")
	}
	w.observe(true, true, now)
	if w.probeDue(true, true, "%7", now.Add(trustProbeEvery/2)) {
		t.Error("probe due before the interval elapsed")
	}
	if !w.probeDue(true, true, "%7", now.Add(trustProbeEvery)) {
		t.Error("probe not due after the interval")
	}
}

// The dialog's verdict holds only while the pane is still unmapped and
// waiting: SessionStart writing the map is the dialog having been answered.
func TestTrustWatchClearsWhenAnswered(t *testing.T) {
	now := time.Unix(1794621765, 0)
	var w trustWatch
	w.started(now)
	w.observe(true, true, now)
	if !w.up || !w.since.Equal(now) {
		t.Fatalf("up/since = %v/%v after a hit", w.up, w.since)
	}
	// A later hit keeps the first sighting as the episode start.
	w.started(now.Add(2 * time.Second))
	w.observe(true, true, now.Add(2*time.Second))
	if !w.since.Equal(now) {
		t.Errorf("since moved to %v", w.since)
	}
	// A failed capture says nothing.
	w.started(now.Add(4 * time.Second))
	w.observe(false, false, now.Add(4*time.Second))
	if !w.up {
		t.Error("a failed capture cleared the dialog")
	}
	w.probeDue(true, false, "%7", now.Add(6*time.Second))
	if w.up {
		t.Error("still up after the pane was mapped")
	}
}

func TestTrustOverride(t *testing.T) {
	since := time.Unix(1794621765, 0)
	starting := State{Kind: StateWaiting, Since: since.Add(-time.Minute), Anchored: true}
	got := trustOverride(starting, trustWatch{up: true, since: since})
	if got.Kind != StateAwaiting || !got.Since.Equal(since) || !got.Anchored {
		t.Errorf("= %+v, want Awaiting since the first sighting", got)
	}
	if v := statePublishValue(got); !isWaiting(v) {
		t.Errorf("published %q, which the switchboard does not read as waiting on the human", v)
	}
	if got := trustOverride(starting, trustWatch{}); got.Kind != StateWaiting {
		t.Errorf("no dialog: = %+v, want Starting", got)
	}
	// Only Starting is upgraded: a bound session has a transcript to believe.
	idle := State{Kind: StateIdle, Since: since}
	if got := trustOverride(idle, trustWatch{up: true, since: since}); got.Kind != StateIdle {
		t.Errorf("bound session: = %+v, want Idle", got)
	}
}
