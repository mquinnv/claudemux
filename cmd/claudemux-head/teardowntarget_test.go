package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// initPlainRepo builds a throwaway repo with one commit under root/name and
// returns its symlink-resolved path, so it compares equal to what git prints.
func initPlainRepo(t *testing.T, name string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, name)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "init", "-q", "-b", "main")
	runGitTest(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	return repo
}

// launchedIn is teardownTestModel for a head started in a real directory, the
// way newModel captures it: workDir, mainCheckout and inWorktree all describe
// the launch directory.
func launchedIn(dir string) model {
	m := teardownTestModel()
	m.workDir = dir
	m.mainCheckout = mainCheckoutFor(dir)
	m.inWorktree = worktreeNameForCwd(dir) != ""
	m.sessionCwd = ""
	m.teardownWorkDir = ""
	m.teardownInWorktree = false
	return m
}

// AG-1482. A session launched in one checkout whose shell `cd`'d into an
// unrelated repo must still be judged on its OWN checkout. The transcript cwd
// follows every `cd`, so arming off it pointed the cleanliness gate at
// whatever repo the session last looked at: a clean, pushed session sat at
// "blocked (dirty tree)" because a repo it had merely visited held an
// untracked directory.
func TestTeardownTargetIgnoresCwdDriftedIntoAnotherRepo(t *testing.T) {
	launch := initPlainRepo(t, "home")
	other := initPlainRepo(t, "it-admin-docs")
	if err := os.Mkdir(filepath.Join(other, ".worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, ".worktrees", "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := launchedIn(launch)
	m.sessionCwd = other

	got, _ := pollPrompt(m, "/done", time.Now())

	if got.teardown != teardownSent {
		t.Fatalf("phase = %v, want teardownSent", got.teardown)
	}
	if got.teardownWorkDir != launch {
		t.Errorf("gate target = %q, want the launch checkout %q", got.teardownWorkDir, launch)
	}
	if got.teardownInWorktree {
		t.Error("inWorktree = true for a plain launch checkout")
	}
}

// A cwd outside any repository (a `cd /tmp`) is drift too: there is nothing
// there to be clean or dirty, and the gate would sit at "probe failed".
func TestTeardownTargetIgnoresCwdDriftedOutsideAnyRepo(t *testing.T) {
	launch := initPlainRepo(t, "home")
	elsewhere, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := launchedIn(launch)
	m.sessionCwd = elsewhere

	got, _ := pollPrompt(m, "/done", time.Now())

	if got.teardownWorkDir != launch {
		t.Errorf("gate target = %q, want the launch checkout %q", got.teardownWorkDir, launch)
	}
}

// Another repo's linked worktree is still another repo: its removal says
// nothing about this session's wrap-up, and `git worktree list` run from the
// launch checkout never lists it, which would read as "gone" at once.
func TestTeardownTargetIgnoresAnotherReposWorktree(t *testing.T) {
	launch := initPlainRepo(t, "home")
	_, foreignWorktree := initRepoWithWorktree(t, "webapp-feature")
	m := launchedIn(launch)
	m.sessionCwd = foreignWorktree

	got, _ := m.teardownKey()

	if got.teardownWorkDir != launch {
		t.Errorf("gate target = %q, want the launch checkout %q", got.teardownWorkDir, launch)
	}
	if got.teardownInWorktree {
		t.Error("inWorktree = true off a worktree that belongs to a different repo")
	}
}

// The worktree path is unchanged: a session working in one of the launch
// checkout's own linked worktrees is judged on that worktree.
func TestTeardownTargetKeepsLaunchReposLinkedWorktree(t *testing.T) {
	launch, worktree := initRepoWithWorktree(t, "webapp-feature")
	m := launchedIn(launch)
	m.sessionCwd = worktree

	got, _ := m.teardownKey()

	if got.teardownWorkDir != worktree {
		t.Errorf("gate target = %q, want the session's worktree %q", got.teardownWorkDir, worktree)
	}
	if !got.teardownInWorktree {
		t.Error("inWorktree = false for the launch repo's own linked worktree")
	}
}

// A worktree the wrap-up has already deleted cannot be asked which repo it
// belongs to. Not being able to tell must leave the session cwd in charge:
// redirecting here would turn "worktree gone" evidence into a cleanliness
// check of the main checkout.
func TestTeardownTargetKeepsDeletedWorktree(t *testing.T) {
	launch := initPlainRepo(t, "home")
	gone := filepath.Join(launch, ".claude", "worktrees", "already-removed")
	m := launchedIn(launch)
	m.sessionCwd = gone

	got, _ := m.teardownKey()

	if got.teardownWorkDir != gone {
		t.Errorf("gate target = %q, want the removed worktree path %q", got.teardownWorkDir, gone)
	}
	if !got.teardownInWorktree {
		t.Error("inWorktree = false for a native worktree path")
	}
}

// A subdirectory of the launch checkout is the same repository, not drift.
func TestTeardownTargetKeepsSubdirectoryOfLaunchCheckout(t *testing.T) {
	launch := initPlainRepo(t, "home")
	sub := filepath.Join(launch, "cmd", "tool")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	m := launchedIn(launch)
	m.sessionCwd = sub

	got, _ := pollPrompt(m, "/done", time.Now())

	if got.teardownWorkDir != sub {
		t.Errorf("gate target = %q, want %q (same repo as the launch checkout)", got.teardownWorkDir, sub)
	}
}

// A head launched outside any repo has no checkout of its own to fall back
// on, so the session's cwd stays the target, as before.
func TestTeardownTargetKeepsSessionCwdWhenLaunchDirIsNotARepo(t *testing.T) {
	launchDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := initPlainRepo(t, "project")
	m := launchedIn(launchDir)
	m.sessionCwd = repo

	got, _ := pollPrompt(m, "/done", time.Now())

	if got.teardownWorkDir != repo {
		t.Errorf("gate target = %q, want the session cwd %q", got.teardownWorkDir, repo)
	}
}

// AG-1482, the trigger. A peer session's report that happens to open with the
// wrap-up command's name ("/done completed for PR #126: ...") is not the user
// running the wrap-up here. It reaches the transcript as a peer brief, which
// counts as a genuine prompt for display, and it armed the watch on a session
// that was in the middle of its work.
func TestTeardownDoesNotAutoArmFromPeerMessage(t *testing.T) {
	m := teardownTestModel()
	now := time.Now()
	msg := dataMsg{time: now, rateLimitErr: errors.New("no rate limits in this test")}
	msg.newEvents = []Event{{
		Type:      "user",
		IsMeta:    true,
		PeerBrief: true,
		UserText:  "/done completed for PR #126: worktree and branch removed",
		Timestamp: now.Format(time.RFC3339),
	}}

	next, _ := m.Update(msg)
	got := next.(model)

	if got.teardown != teardownIdle {
		t.Errorf("phase = %v, want teardownIdle: a peer's message is not a typed wrap-up", got.teardown)
	}
}

// The blocked chip names the checkout it is blocked on, so a gate judging a
// directory the user is not looking at explains itself.
func TestTeardownBlockedChipNamesTheDirectory(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownSent
	m.teardownAuto = true
	m.teardownInWorktree = false
	m.teardownBlocked = true
	m.teardownBlockReason = "dirty tree"
	m.teardownWorkDir = "/Users/someone/Projects/it-admin-docs"

	chip := m.teardownChipText(time.Now())

	if !strings.Contains(chip, "dirty tree in it-admin-docs") {
		t.Errorf("chip = %q, want it to name it-admin-docs", chip)
	}
}
