package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestGitCmdIsLockFree pins the two properties that keep a timed-out probe from
// stranding .git/index.lock in a user's checkout.
func TestGitCmdIsLockFree(t *testing.T) {
	cmd := gitCmd(context.Background(), "/some/dir", "status", "--porcelain")
	want := []string{"git", "--no-optional-locks", "-C", "/some/dir", "status", "--porcelain"}
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("args = %q, want %q", cmd.Args, want)
	}
	if cmd.Cancel == nil {
		t.Error("Cancel is nil: a timeout would SIGKILL git, which cannot clean up its lockfiles")
	}
	if cmd.WaitDelay <= 0 {
		t.Error("WaitDelay unset: a git ignoring SIGTERM could block the probe forever")
	}
}

// TestNoGitOutsideGitCmd fails if any non-test source runs git without going
// through gitCmd, so a new probe cannot quietly bring the lock back.
func TestNoGitOutsideGitCmd(t *testing.T) {
	direct := regexp.MustCompile(`exec\.Command(Context)?\([^)]*"git"`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "gitcmd.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if loc := direct.FindIndex(src); loc != nil {
			line := 1 + strings.Count(string(src[:loc[0]]), "\n")
			t.Errorf("%s:%d runs git directly; use gitCmd", f, line)
		}
	}
}

// TestGitStatusProbeLeavesIndexAlone runs the real probe against a repo whose
// index is stale. A plain `git status` would rewrite the index (taking
// index.lock to do it); the lock-free probe must leave it byte-for-byte alone.
func TestGitStatusProbeLeavesIndexAlone(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	tracked := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "tracked.txt")
	git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init")

	// Same content, new mtime: the index's cached stat is now stale, which is
	// exactly what makes status want to refresh and write it back.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(tracked, later, later); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(dir, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, ok := mainStatusSnapshot(ctx, dir); !ok {
		t.Fatal("probe failed")
	}

	after, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, after) {
		t.Error("git status probe rewrote the index; it must run with --no-optional-locks")
	}
}

// TestGitCmdTimeoutSendsSIGTERM checks that a probe hitting its deadline asks
// git to stop with a signal it can catch, rather than SIGKILL.
func TestGitCmdTimeoutSendsSIGTERM(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "got-term")
	fake := "#!/bin/sh\ntrap 'echo term > \"" + marker + "\"; exit 143' TERM\nwhile :; do sleep 0.05; done\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = gitCmd(ctx, t.TempDir(), "status").Run()

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("fake git never saw SIGTERM on timeout: %v", err)
	}
}
