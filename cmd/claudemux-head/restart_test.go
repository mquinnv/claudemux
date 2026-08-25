package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The re-exec must carry the ORIGINAL flags. A head launched with --session is
// pinned to one transcript on purpose; dropping the flag on restart would
// silently convert it into a follow-active head watching a different session.
func TestRestartArgvKeepsFlags(t *testing.T) {
	got := restartArgv("/new/claudemux-head",
		[]string{"/old/claudemux-head", "--session", "abc123"})
	want := []string{"/new/claudemux-head", "--session", "abc123"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("restartArgv() = %v, want %v", got, want)
	}
}

// argv[0] is replaced rather than reused: os.Executable() resolves to the
// binary on disk NOW, which is the point of restarting — picking up a rebuild.
// The old argv[0] may be a stale path or a bare name that was resolved through
// PATH at launch.
func TestRestartArgvReplacesArgv0(t *testing.T) {
	got := restartArgv("/usr/local/bin/claudemux-head", []string{"claudemux-head"})
	want := []string{"/usr/local/bin/claudemux-head"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("restartArgv() = %v, want %v", got, want)
	}
}

// An empty os.Args cannot happen under a real exec, but restartArgv must not
// index into it and panic if it ever does.
func TestRestartArgvEmptyArgs(t *testing.T) {
	got := restartArgv("/bin/head", nil)
	want := []string{"/bin/head"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("restartArgv() = %v, want %v", got, want)
	}
}

func TestShouldAutoRestart(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	stamp, ok := launchBinStampOf(p)
	if !ok {
		t.Fatal("stampOf failed")
	}
	m := &model{launchBin: stamp, launchBinOK: true}
	now := time.Now()
	if m.shouldAutoRestart(now) {
		t.Error("unchanged binary must not restart")
	}
	if err := os.WriteFile(p, []byte("v2-longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-time.Minute)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if !m.shouldAutoRestart(now) {
		t.Error("replaced binary must restart an idle head")
	}
	m.teardown = teardownSent
	if m.shouldAutoRestart(now) {
		t.Error("must not restart mid-teardown")
	}
	m.teardown = teardownIdle
	m.launchBinOK = false
	if m.shouldAutoRestart(now) {
		t.Error("must not restart when the launch stamp failed")
	}
}
