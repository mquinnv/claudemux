package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBinChangedStamps(t *testing.T) {
	base := time.Unix(1_754_700_000, 0)
	launch := binStamp{path: "/x", mtime: base, size: 100}
	cases := []struct {
		name string
		cur  binStamp
		now  time.Time
		want bool
	}{
		{"identical", binStamp{path: "/x", mtime: base, size: 100}, base.Add(time.Hour), false},
		{"newer and settled", binStamp{path: "/x", mtime: base.Add(time.Minute), size: 100}, base.Add(time.Minute + binSettle), true},
		{"newer but unsettled", binStamp{path: "/x", mtime: base.Add(time.Minute), size: 100}, base.Add(time.Minute + time.Second), false},
		{"same mtime different size", binStamp{path: "/x", mtime: base, size: 101}, base.Add(time.Hour), true},
	}
	for _, tc := range cases {
		if got := binChangedStamps(launch, tc.cur, tc.now); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestBinChangedStatsRealFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	launch, ok := launchBinStampOf(p)
	if !ok {
		t.Fatal("stampOf failed")
	}
	// Unreplaced binary: never a change, regardless of clock.
	if binChanged(launch, time.Now().Add(time.Hour)) {
		t.Error("unchanged file reported changed")
	}
	// Replaced binary with an old mtime: changed and settled.
	if err := os.WriteFile(p, []byte("v2-longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if !binChanged(launch, time.Now()) {
		t.Error("replaced+settled file not reported changed")
	}
	// A vanished file (mid-replace) is "not changed"; the next poll re-checks.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if binChanged(launch, time.Now()) {
		t.Error("stat failure must read as unchanged")
	}
}

// TestBinChangedFollowsSymlinkSwap covers a Homebrew-style upgrade: the launch
// path is a symlink into a versioned directory, and upgrading repoints the
// symlink at a NEW file and deletes the old one. Stamping only the resolved
// path made that invisible — the old path's stat failed forever, and "failed
// stat reads as unchanged" meant a brew-installed fleet never restarted (a
// 1.2.0 lobby and head sat under a 1.3.0 symlink, reading a meters file that
// no longer existed).
func TestBinChangedFollowsSymlinkSwap(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "1.2.0", "bin")
	neu := filepath.Join(dir, "1.3.0", "bin")
	for _, p := range []string{old, neu} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(filepath.Base(filepath.Dir(p))), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "claudemux-head")
	if err := os.Symlink(old, link); err != nil {
		t.Fatal(err)
	}
	launch, ok := launchBinStampOf(link)
	if !ok {
		t.Fatal("launch stamp failed")
	}
	now := time.Now()
	if binChanged(launch, now) {
		t.Fatal("unchanged symlink reported as changed")
	}
	// The upgrade: repoint the symlink, delete the old build. The new file's
	// mtime is backdated so the settle window has already passed.
	settled := now.Add(-binSettle - time.Second)
	if err := os.Chtimes(neu, settled, settled); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(neu, link); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(old)); err != nil {
		t.Fatal(err)
	}
	if !binChanged(launch, now) {
		t.Fatal("symlink repointed at a new build was not detected")
	}
	// A dangling symlink (mid-upgrade) still reads as unchanged: the next
	// tick re-checks, and exec'ing nothing would kill the pane.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), link); err != nil {
		t.Fatal(err)
	}
	if binChanged(launch, now) {
		t.Fatal("dangling symlink reported as changed")
	}
}
