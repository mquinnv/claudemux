package main

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

// gitWaitDelay is how long a cancelled git gets to clean up after SIGTERM
// before exec escalates to SIGKILL.
const gitWaitDelay = 1 * time.Second

// gitCmd builds a read-only `git -C dir args...` for the head's background
// probes. Every git call in this binary goes through it, because the head polls
// git in the user's own checkouts on a short deadline and must never leave
// anything behind there.
//
// --no-optional-locks: `git status` otherwise takes .git/index.lock to write a
// refreshed index back. A probe killed mid-refresh left that lock on disk, and
// since status still exits 0 when it skips the optional write, nothing noticed
// until the user's next real write failed with "index.lock exists".
//
// Cancel sends SIGTERM rather than exec's default SIGKILL, so a timed-out git
// that does hold a lockfile can remove it on the way out; WaitDelay still
// bounds the wait if it ignores the signal.
func gitCmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-C", dir}, args...)...)
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = gitWaitDelay
	return cmd
}
