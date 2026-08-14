package main

import (
	"os"
	"path/filepath"
	"time"
)

// Heads and the switchboard run for days while `go install` (or a release
// upgrade) replaces the binary under them, so the running fleet quietly
// diverges from the code on disk — the switchboard's conductor not knowing
// the then-new "Asking" state is exactly how this was noticed. binwatch
// detects "the binary on disk is no longer the one I am" so each TUI can
// re-exec itself (restartSelf) at a safe moment instead of waiting for a
// human to press R.

// binSettle is how long a changed binary's mtime must be in the past before
// the change is acted on. `go install` writes the file in place, and exec'ing
// a half-written binary would kill the pane.
const binSettle = 2 * time.Second

// binStamp identifies one on-disk build of this binary.
type binStamp struct {
	path  string
	mtime time.Time
	size  int64
}

// launchBinStamp stamps the binary this process is running, symlinks resolved
// first for the same reason siblingOfExecutable does. ok=false when anything
// fails; callers then never auto-restart, the safe default.
func launchBinStamp() (binStamp, bool) {
	exe, err := os.Executable()
	if err != nil {
		return binStamp{}, false
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return binStamp{}, false
	}
	return binStampOf(resolved)
}

func binStampOf(path string) (binStamp, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return binStamp{}, false
	}
	return binStamp{path: path, mtime: fi.ModTime(), size: fi.Size()}, true
}

// binChanged reports whether the binary at launch.path has been replaced and
// settled. A failed stat (deleted, or mid-replace) reads as unchanged — the
// caller polls, so the next tick re-checks.
func binChanged(launch binStamp, now time.Time) bool {
	cur, ok := binStampOf(launch.path)
	if !ok {
		return false
	}
	return binChangedStamps(launch, cur, now)
}

// binChangedStamps is the pure comparison: replaced means mtime or size
// moved; settled means the new mtime is at least binSettle in the past.
func binChangedStamps(launch, cur binStamp, now time.Time) bool {
	if cur.mtime.Equal(launch.mtime) && cur.size == launch.size {
		return false
	}
	return now.Sub(cur.mtime) >= binSettle
}
