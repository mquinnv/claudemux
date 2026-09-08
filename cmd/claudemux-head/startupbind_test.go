package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A head launched by bin/claudemux always sits next to a FRESH claude
// session, so in an existing project the newest transcript in the project dir
// is the previous session's, never the sibling pane's. Binding to it at
// startup showed yesterday's prompt, model, and context gauge until the new
// session wrote its first line — and fired a summary call on it. In follow
// mode inside tmux the head must start waiting and let the pane map bind it;
// pinned (--session) or outside tmux, where no map ever comes, it keeps the
// MRA binding.
func TestLaunchWaitsInTmuxFollowMode(t *testing.T) {
	if !launchWaits(true, "%3") {
		t.Error("launchWaits(follow, in tmux) = false, want true")
	}
	if launchWaits(false, "%3") {
		t.Error("launchWaits(pinned, in tmux) = true, want false")
	}
	if launchWaits(true, "") {
		t.Error("launchWaits(follow, outside tmux) = true, want false")
	}
}

// While waiting, the no-claude-pane fallback (a booting claude pane is not a
// candidate yet) must not adopt a transcript that predates the wait: in an
// existing project that is the previous session. Only a transcript written
// after the wait began can be the one the head is waiting for.
func TestWaitingFallbackIgnoresTranscriptsOlderThanTheWait(t *testing.T) {
	dir := t.TempDir()
	placeholder := waitingTranscript(dir)
	old := filepath.Join(dir, "old-sess.jsonl")
	if err := os.WriteFile(old, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	since := time.Now()

	if got := followFallback(placeholder, since); got != "" {
		t.Fatalf("followFallback adopted %q, want \"\" (older than the wait)", got)
	}

	fresh := filepath.Join(dir, "new-sess.jsonl")
	if err := os.WriteFile(fresh, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := since.Add(time.Second)
	if err := os.Chtimes(fresh, future, future); err != nil {
		t.Fatal(err)
	}
	if got := followFallback(placeholder, since); got != fresh {
		t.Fatalf("followFallback = %q, want %q (written after the wait)", got, fresh)
	}
}

// Outside waiting mode the fallback is the plain MRA rotation it always was:
// a bound head with no claude pane in sight follows whatever was touched last.
func TestFollowFallbackOutsideWaitingIsPlainMRA(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "cur.jsonl")
	old := filepath.Join(dir, "older.jsonl")
	for _, p := range []string{old, cur} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if got := followFallback(old, time.Time{}); got != cur {
		t.Fatalf("followFallback = %q, want %q", got, cur)
	}
	if got := followFallback(cur, time.Time{}); got != "" {
		t.Fatalf("followFallback on the newest file = %q, want \"\"", got)
	}
}

// followTarget is pollData's whole rotation decision. A claude pane with no
// map yet (SessionStart not fired, or the hook missing) reports mapped "" and
// keeps the binding — except while waiting, where the same newer-than-the-wait
// fallback applies, so a head is never stuck on Starting for want of a hook,
// yet still never adopts the previous session.
func TestFollowTargetWaitingWithUnmappedPane(t *testing.T) {
	dir := t.TempDir()
	placeholder := waitingTranscript(dir)
	old := filepath.Join(dir, "old-sess.jsonl")
	if err := os.WriteFile(old, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	since := time.Now()

	if next, _ := followTarget("", true, placeholder, since, nil, t.TempDir()); next != "" {
		t.Fatalf("unmapped pane while waiting adopted %q, want \"\" (predates the wait)", next)
	}

	fresh := filepath.Join(dir, "new-sess.jsonl")
	if err := os.WriteFile(fresh, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := since.Add(time.Second)
	if err := os.Chtimes(fresh, future, future); err != nil {
		t.Fatal(err)
	}
	if next, via := followTarget("", true, placeholder, since, nil, t.TempDir()); next != fresh || via != "mru-fallback" {
		t.Fatalf("followTarget = (%q, %q), want (%q, mru-fallback)", next, via, fresh)
	}
	// Bound (not waiting), an unmapped pane keeps the binding as it always has.
	if next, _ := followTarget("", true, old, time.Time{}, nil, t.TempDir()); next != "" {
		t.Fatalf("unmapped pane while bound adopted %q, want \"\"", next)
	}
	// A mapped transcript wins outright, waiting or not.
	if next, via := followTarget(fresh, true, placeholder, since, nil, t.TempDir()); next != fresh || via != "mapped" {
		t.Fatalf("followTarget(mapped) = (%q, %q), want (%q, mapped)", next, via, fresh)
	}
	// No claude pane at all: the fallback, narrowed by the wait.
	if next, via := followTarget("", false, placeholder, since, nil, t.TempDir()); next != fresh || via != "mru-fallback" {
		t.Fatalf("followTarget(no pane) = (%q, %q), want (%q, mru-fallback)", next, via, fresh)
	}
}

// Rotation can land back in waiting mode: the sibling pane's map names a
// session whose transcript does not exist yet (a fresh claude in the same
// pane, or a recycled pane id). Adopting the placeholder must mean "waiting"
// — no session id, an anchored StateWaiting, and no summary seed for a
// transcript that has nothing in it.
func TestSwitchSessionToPlaceholderEntersWaiting(t *testing.T) {
	dir := t.TempDir()
	bound := filepath.Join(dir, "old-sess.jsonl")
	if err := os.WriteFile(bound, []byte(`{"type":"user","timestamp":"2026-08-13T15:09:30Z","message":{"content":"hello"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newModel(defaultConfig(), bound, "old-sess", true)
	m.summarizer = &Summarizer{}
	m.contextPct = 42
	m.modelName = "claude-x"
	now := time.Now()

	if cmd := m.switchSession(waitingTranscript(dir), now); cmd != nil {
		t.Errorf("switchSession to the placeholder returned a command; want nil (nothing to summarize)")
	}
	if m.sessionID != "" {
		t.Errorf("sessionID = %q, want \"\" (waiting mode)", m.sessionID)
	}
	if !m.waitingSince.Equal(now) {
		t.Errorf("waitingSince = %v, want %v", m.waitingSince, now)
	}
	if m.state.Kind != StateWaiting {
		t.Errorf("state.Kind = %v, want StateWaiting", m.state.Kind)
	}
	if m.summarizing {
		t.Errorf("summarizing held on the placeholder: no call was issued, so it would never clear")
	}
	if m.contextPct != 0 || m.modelName != "" {
		t.Errorf("contextPct/modelName = %v/%q, want 0/\"\" (old session's gauge must not linger)", m.contextPct, m.modelName)
	}
}

// A map that names a session with no transcript on disk yet is a fresh
// claude booting in that pane: SessionStart wrote the map, the first prompt
// has not written the .jsonl. Reporting "" here kept the head on whatever it
// was following — the previous session — until that first prompt landed. It
// must instead hand back the waiting placeholder for the recorded transcript's
// project dir, so the head shows Starting and adopts the real file when it
// appears.
func TestBindCandidatesWaitsOnMappedSessionWithoutTranscript(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(t.TempDir(), "-Users-x-proj")
	body := `{"session_id":"new-sess","transcript_path":"` +
		filepath.Join(projectDir, "new-sess.jsonl") + `","cwd":"/Users/x/proj"}`
	if err := os.WriteFile(filepath.Join(dir, "7.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{"%7": "/Users/x/proj"}
	transcript, cwd, pane := bindCandidates([]string{"%7"}, paths, dir, t.TempDir())
	if want := waitingTranscript(projectDir); transcript != want {
		t.Errorf("transcript = %q, want placeholder %q", transcript, want)
	}
	if cwd != "/Users/x/proj" || pane != "%7" {
		t.Errorf("cwd/pane = %q/%q, want /Users/x/proj/%%7", cwd, pane)
	}
}

// The existing contract: a mapped session whose transcript exists binds to
// that transcript, wherever under projectsDir it currently lives.
func TestBindCandidatesFollowsMappedTranscript(t *testing.T) {
	dir := t.TempDir()
	projectsDir := t.TempDir()
	live := filepath.Join(projectsDir, "-Users-x-proj--claude-worktrees-w", "sess-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `{"session_id":"sess-1","transcript_path":"` +
		filepath.Join(projectsDir, "-Users-x-proj", "sess-1.jsonl") + `","cwd":"/Users/x/proj"}`
	if err := os.WriteFile(filepath.Join(dir, "7.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	transcript, _, pane := bindCandidates([]string{"%7"}, map[string]string{"%7": "/w"}, dir, projectsDir)
	if transcript != live || pane != "%7" {
		t.Errorf("transcript/pane = %q/%q, want %q/%%7", transcript, pane, live)
	}
}

// No candidate has a map yet: the preferred pane's cwd and id are still
// reported, with no transcript to follow.
func TestBindCandidatesNoMapReportsPreferredPane(t *testing.T) {
	transcript, cwd, pane := bindCandidates([]string{"%3", "%4"}, map[string]string{"%3": "/a", "%4": "/b"}, t.TempDir(), t.TempDir())
	if transcript != "" || cwd != "/a" || pane != "%3" {
		t.Errorf("= (%q, %q, %q), want (\"\", /a, %%3)", transcript, cwd, pane)
	}
}
