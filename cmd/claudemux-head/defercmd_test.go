package main

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// fakeTmux answers the reads runDefer makes and records every other
// invocation, so tests can assert on the exact set-option argv.
type fakeTmux struct {
	self, mark, fleet string
	calls             [][]string
}

func (f *fakeTmux) run(args ...string) (string, error) {
	switch args[0] {
	case "display-message":
		if strings.Contains(args[len(args)-1], "session_name") {
			return f.self + "\n", nil
		}
		return f.mark + "\n", nil
	case "list-sessions":
		return f.fleet, nil
	}
	f.calls = append(f.calls, args)
	return "", nil
}

func newFakeTmux(mark string) *fakeTmux {
	return &fakeTmux{self: "claudemux", mark: mark,
		fleet: "claudemux\t1762000000\nphenix\t1762999999\nphenix-2\t1763000000\n"}
}

func paneEnv(k string) string {
	if k == "TMUX_PANE" {
		return "%7"
	}
	return ""
}

func runDeferT(f *fakeTmux, env func(string) string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	rc := runDefer(args, &out, &errb, env, f.run)
	return rc, out.String(), errb.String()
}

func TestDeferCmdWaitOn(t *testing.T) {
	f := newFakeTmux("")
	rc, _, errs := runDeferT(f, paneEnv, "--wait-on", "phenix-2", "--reason", "waiting on phenix-2: fix")
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errs)
	}
	want := deferLinkedArgs("%7", deferWaitValue, "waiting on phenix-2: fix", deferLink{"phenix-2", "1763000000"})
	if len(f.calls) != 1 || !reflect.DeepEqual(f.calls[0], want) {
		t.Errorf("calls = %q, want one call %q", f.calls, want)
	}
}

func TestDeferCmdDefaultReason(t *testing.T) {
	f := newFakeTmux("")
	if rc, _, errs := runDeferT(f, paneEnv, "--wait-on", "phenix"); rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errs)
	}
	want := deferLinkedArgs("%7", deferWaitValue, "waiting on phenix", deferLink{"phenix", "1762999999"})
	if len(f.calls) != 1 || !reflect.DeepEqual(f.calls[0], want) {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
}

func TestDeferCmdRespectsUserMark(t *testing.T) {
	for _, args := range [][]string{{"--wait-on", "phenix"}, {"--clear"}} {
		f := newFakeTmux(deferOnValue)
		rc, out, _ := runDeferT(f, paneEnv, args...)
		if rc != 0 || !strings.Contains(out, "skipped: this session is deferred by the user") {
			t.Errorf("%v: rc=%d out=%q", args, rc, out)
		}
		if len(f.calls) != 0 {
			t.Errorf("%v: touched tmux: %q", args, f.calls)
		}
	}
}

func TestDeferCmdClear(t *testing.T) {
	f := newFakeTmux(deferWaitValue)
	if rc, _, errs := runDeferT(f, paneEnv, "--clear"); rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errs)
	}
	want := deferArgs("%7", false, "")
	if len(f.calls) != 1 || !reflect.DeepEqual(f.calls[0], want) {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
}

func TestDeferCmdUnknownSession(t *testing.T) {
	f := newFakeTmux("")
	rc, _, _ := runDeferT(f, paneEnv, "--wait-on", "nope")
	if rc != 1 || len(f.calls) != 0 {
		t.Errorf("rc=%d calls=%q", rc, f.calls)
	}
}

func TestDeferCmdSelf(t *testing.T) {
	f := newFakeTmux("")
	rc, _, _ := runDeferT(f, paneEnv, "--wait-on", "claudemux")
	if rc != 1 || len(f.calls) != 0 {
		t.Errorf("rc=%d calls=%q", rc, f.calls)
	}
}

func TestDeferCmdNoPane(t *testing.T) {
	f := newFakeTmux("")
	rc, _, _ := runDeferT(f, func(string) string { return "" }, "--wait-on", "phenix")
	if rc != 1 || len(f.calls) != 0 {
		t.Errorf("rc=%d calls=%q", rc, f.calls)
	}
}

func TestDeferCmdSessionNameWithPrefixSibling(t *testing.T) {
	f := newFakeTmux("")
	if rc, _, errs := runDeferT(f, paneEnv, "--wait-on", "phenix"); rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errs)
	}
	want := deferLinkedArgs("%7", deferWaitValue, "waiting on phenix", deferLink{"phenix", "1762999999"})
	if !reflect.DeepEqual(f.calls[0], want) {
		t.Errorf("got %q, want %q", f.calls[0], want)
	}
}

func TestDeferCmdBadUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"--wait-on"}, {"--wait-on", "a", "--clear"}, {"--bogus"}} {
		f := newFakeTmux("")
		if rc, _, _ := runDeferT(f, paneEnv, args...); rc != 2 || len(f.calls) != 0 {
			t.Errorf("%v: rc=%d calls=%q", args, rc, f.calls)
		}
	}
}

func TestDeferCmdTmuxFailureWritesNothing(t *testing.T) {
	var calls int
	rc := runDefer([]string{"--clear"}, &bytes.Buffer{}, &bytes.Buffer{}, paneEnv,
		func(args ...string) (string, error) {
			if args[0] == "set-option" {
				calls++
			}
			return "", errors.New("boom")
		})
	if rc != 1 || calls != 0 {
		t.Errorf("rc=%d set-option calls=%d, want 1 and 0", rc, calls)
	}
}
