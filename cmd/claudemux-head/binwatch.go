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
	// exe is the path the process was launched through, symlinks intact.
	// Every check re-resolves it, because an upgrade can replace the binary
	// two ways: `go install` rewrites the file at the resolved path in place,
	// while Homebrew repoints the symlink at a NEW versioned file and deletes
	// the old one. Stamping only the resolved path caught the first and was
	// blind to the second — the old path's stat failed forever, and a
	// brew-installed fleet never restarted.
	exe   string
	path  string // resolved at stamp time
	mtime time.Time
	size  int64
}

// launchBinStamp stamps the binary this process is running. ok=false when
// anything fails; callers then never auto-restart, the safe default.
func launchBinStamp() (binStamp, bool) {
	exe, err := os.Executable()
	if err != nil {
		return binStamp{}, false
	}
	return launchBinStampOf(exe)
}

// launchBinStampOf stamps whatever exe resolves to right now, symlinks
// resolved for the same reason siblingOfExecutable does, and remembers exe
// itself so later checks can follow a repointed link.
func launchBinStampOf(exe string) (binStamp, bool) {
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return binStamp{}, false
	}
	st, ok := binStampOf(resolved)
	if !ok {
		return binStamp{}, false
	}
	st.exe = exe
	return st, true
}

func binStampOf(path string) (binStamp, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return binStamp{}, false
	}
	return binStamp{path: path, mtime: fi.ModTime(), size: fi.Size()}, true
}

// binChanged reports whether the binary behind launch.exe has been replaced
// and settled — rewritten in place, or the link moved to another file. A
// failed resolve or stat (deleted, dangling, or mid-replace) reads as
// unchanged — the caller polls, so the next tick re-checks, and exec'ing a
// path that is not there would kill the pane.
func binChanged(launch binStamp, now time.Time) bool {
	resolved, err := filepath.EvalSymlinks(launch.exe)
	if err != nil {
		return false
	}
	cur, ok := binStampOf(resolved)
	if !ok {
		return false
	}
	return binChangedStamps(launch, cur, now)
}

// binChangedStamps is the pure comparison: replaced means the link now lands
// on a different file, or the same file's mtime or size moved; settled means
// the new mtime is at least binSettle in the past.
func binChangedStamps(launch, cur binStamp, now time.Time) bool {
	if cur.path == launch.path && cur.mtime.Equal(launch.mtime) && cur.size == launch.size {
		return false
	}
	return now.Sub(cur.mtime) >= binSettle
}
