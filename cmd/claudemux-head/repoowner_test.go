package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeGit puts a `git` first on PATH that prints stdout and stderr and exits
// with code, for the failures a real git cannot be made to produce on demand.
func fakeGit(t *testing.T, stdout, stderr string, code int) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '%s' \"$FAKE_GIT_STDOUT\"\n" +
		"printf '%s' \"$FAKE_GIT_STDERR\" >&2\n" +
		"exit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_GIT_STDOUT", stdout)
	t.Setenv("FAKE_GIT_STDERR", stderr)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// repoOwnerOf's whole job is to keep "git said no" apart from "git could not
// say". Only git's own discovery failure is a "no"; every other way of failing
// — and every answer that is not one absolute path — is "could not say".
func TestRepoOwnerOf(t *testing.T) {
	t.Run("real git", func(t *testing.T) {
		repo, worktree := initRepoWithWorktree(t, "webapp-feature")
		sub := filepath.Join(repo, "cmd")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		outside, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		// t.TempDir may itself sit inside a repo; stop discovery at it.
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(outside))
		tests := []struct {
			name      string
			dir       string
			wantOwner string
			want      repoPlacement
		}{
			{"empty", "", "", repoUnknown},
			{"repo root", repo, repo, repoOwned},
			{"subdirectory", sub, repo, repoOwned},
			{"linked worktree", worktree, repo, repoOwned},
			{"no repository", outside, "", repoNone},
			{"missing directory", filepath.Join(outside, "gone"), "", repoGone},
		}
		for _, tt := range tests {
			owner, got := repoOwnerOf(tt.dir)
			if got != tt.want || owner != tt.wantOwner {
				t.Errorf("%s: repoOwnerOf = (%q, %v), want (%q, %v)", tt.name, owner, got, tt.wantOwner, tt.want)
			}
		}
	})

	failures := []struct {
		name   string
		stdout string
		stderr string
		code   int
	}{
		{"dubious ownership", "", "fatal: detected dubious ownership in repository at '/x'", 128},
		{"dangling worktree pointer", "", "fatal: not a git repository: /main/.git/worktrees/foo", 128},
		{"filesystem boundary", "", "fatal: not a git repository (or any parent up to mount point /mnt)", 128},
		{"git too old for --path-format", "--path-format=absolute\n.git\n", "", 0},
		{"relative answer", ".git\n", "", 0},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			fakeGit(t, tt.stdout, tt.stderr, tt.code)
			if owner, got := repoOwnerOf(dir); got != repoUnknown || owner != "" {
				t.Errorf("repoOwnerOf = (%q, %v), want (\"\", repoUnknown)", owner, got)
			}
		})
	}
}

// A directory is asked afresh every time: the same path can stop being a
// plain directory, or be removed and recreated under another repository.
func TestRepoOwnerOfFollowsTheDirectory(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	if _, got := repoOwnerOf(dir); got != repoNone {
		t.Fatalf("before init: placement = %v, want repoNone", got)
	}
	runGitTest(t, dir, "init", "-q", "-b", "main")
	if owner, got := repoOwnerOf(dir); got != repoOwned || owner != dir {
		t.Fatalf("after init: repoOwnerOf = (%q, %v), want (%q, repoOwned)", owner, got, dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, got := repoOwnerOf(dir); got != repoGone {
		t.Errorf("after removal: placement = %v, want repoGone", got)
	}
}

// A git that fails for a reason other than "no repository here" has not said
// the cwd left the repo, so the target stays where it was.
func TestTeardownTargetKeepsSessionCwdWhenGitErrors(t *testing.T) {
	launch, worktree := initRepoWithWorktree(t, "webapp-feature")
	m := launchedIn(launch)
	m.sessionCwd = worktree
	fakeGit(t, "", "fatal: detected dubious ownership in repository at '/x'", 128)

	got, _ := pollPrompt(m, "/done", time.Now())

	if got.teardownWorkDir != worktree {
		t.Errorf("gate target = %q, want the session cwd %q", got.teardownWorkDir, worktree)
	}
}

// A plain directory that no longer exists has no tree to judge: the launch
// checkout takes over rather than the gate sitting at "probe failed" for good.
func TestTeardownTargetFallsBackFromDeletedPlainDirectory(t *testing.T) {
	launch := initPlainRepo(t, "home")
	m := launchedIn(launch)
	m.sessionCwd = filepath.Join(filepath.Dir(launch), "scratch-that-was-removed")

	got, _ := pollPrompt(m, "/done", time.Now())

	if got.teardownWorkDir != launch {
		t.Errorf("gate target = %q, want the launch checkout %q", got.teardownWorkDir, launch)
	}
	if got.teardownInWorktree {
		t.Error("inWorktree = true for a plain launch checkout")
	}
}

// The launch REPO is what must not be left, not the launch directory: a head
// launched in a linked worktree keeps a session cwd anywhere else in that
// same repository.
func TestTeardownTargetKeepsSameRepoCwdWhenLaunchedInWorktree(t *testing.T) {
	mainCheckout, launchWorktree := initRepoWithWorktree(t, "webapp-feature")
	sub := filepath.Join(launchWorktree, "cmd")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{sub, mainCheckout} {
		m := launchedIn(launchWorktree)
		m.sessionCwd = cwd

		got, _ := m.teardownKey()

		if got.teardownWorkDir != cwd {
			t.Errorf("gate target = %q, want the session cwd %q", got.teardownWorkDir, cwd)
		}
	}
}

// With no target known there is no directory to name, and the chip must not
// invent one ("probe failed in .").
func TestTeardownBlockedChipWithoutTargetNamesNoDirectory(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownSent
	m.teardownAuto = true
	m.teardownInWorktree = false
	m.teardownBlocked = true
	m.teardownBlockReason = "probe failed"
	m.teardownWorkDir = ""

	chip := m.teardownChipText(time.Now())

	if !strings.Contains(chip, "(probe failed)") {
		t.Errorf("chip = %q, want the bare reason", chip)
	}
}
