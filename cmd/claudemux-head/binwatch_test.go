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
	launch, ok := binStampOf(p)
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
