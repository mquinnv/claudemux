package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain is the backstop for the whole class of bug this file guards
// against: it defaults CLAUDEMUX_ENV to a path that does not exist, so no
// test can reach claudemux-head's real env file — in production potentially a
// FIFO mounted by a secret manager (1Password Environments, for one) serving
// a live secret — unless it explicitly opts in via t.Setenv. Individual tests
// may still pin CLAUDEMUX_ENV themselves; that stays fine and
// self-documenting, this is just the safety net for any test (present or
// future) that forgets to.
func TestMain(m *testing.M) {
	os.Setenv("CLAUDEMUX_ENV", filepath.Join(os.TempDir(), "claudemux-head-absent-env"))
	os.Exit(m.Run())
}

func writeEnvFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// envFileTimeoutForTest swaps the package-level envFileTimeout for the duration
// of a test and returns a func restoring the previous value. Tests that exercise
// the timeout path need a short bound; waiting on the real 2s production value
// once per attempt would make the suite crawl.
func envFileTimeoutForTest(d time.Duration) func() {
	prev := envFileTimeout
	envFileTimeout = d
	return func() { envFileTimeout = prev }
}

func TestEnvFileValuePlainFile(t *testing.T) {
	t.Setenv("CLAUDEMUX_ENV", writeEnvFile(t, "ANTHROPIC_API_KEY=sk-from-file\n"))

	if got := envFileValue("", "ANTHROPIC_API_KEY"); got != "sk-from-file" {
		t.Errorf("envFileValue() = %q, want %q", got, "sk-from-file")
	}
}

func TestConfigValueProcessEnvWinsOverFile(t *testing.T) {
	t.Setenv("CLAUDEMUX_ENV", writeEnvFile(t, "ANTHROPIC_API_KEY=sk-from-file\n"))
	t.Setenv("ANTHROPIC_API_KEY", "sk-from-process-env")

	if got := configValue("", "ANTHROPIC_API_KEY"); got != "sk-from-process-env" {
		t.Errorf("configValue() = %q, want the process env value %q, not the file's", got, "sk-from-process-env")
	}
}

func TestEnvFileValueCommentsBlankLinesAndWhitespace(t *testing.T) {
	contents := "" +
		"# a comment\n" +
		"\n" +
		"   \n" +
		"# ANTHROPIC_API_KEY=commented-out-should-be-ignored\n" +
		"  ANTHROPIC_API_KEY  =  sk-with-surrounding-space  \n"
	t.Setenv("CLAUDEMUX_ENV", writeEnvFile(t, contents))

	if got := envFileValue("", "ANTHROPIC_API_KEY"); got != "sk-with-surrounding-space" {
		t.Errorf("envFileValue() = %q, want %q", got, "sk-with-surrounding-space")
	}
}

// Values may legitimately contain '=' (e.g. base64-ish API keys/secrets).
// readEnvFileValue splits on the FIRST '=' only via strings.Cut, so the rest
// of the line round-trips intact. This pins that behavior so a future switch
// to strings.Split (which would truncate at every '=') can't silently
// corrupt keys.
func TestEnvFileValueEqualsSignInValueRoundTrips(t *testing.T) {
	t.Setenv("CLAUDEMUX_ENV", writeEnvFile(t, "ANTHROPIC_API_KEY=sk-abc==\n"))

	if got := envFileValue("", "ANTHROPIC_API_KEY"); got != "sk-abc==" {
		t.Errorf("envFileValue() = %q, want %q — a value containing '=' must round-trip intact", got, "sk-abc==")
	}
}

func TestEnvFileValueMissingFile(t *testing.T) {
	t.Setenv("CLAUDEMUX_ENV", filepath.Join(t.TempDir(), "does-not-exist"))

	done := make(chan string, 1)
	go func() { done <- envFileValue("", "ANTHROPIC_API_KEY") }()

	select {
	case got := <-done:
		if got != "" {
			t.Errorf("envFileValue() = %q, want \"\" for a missing file", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("envFileValue() did not return for a missing file — it must not hang")
	}
}

// TestEnvFileValueFIFOTimeout is the most important test in this file: env.go
// reads claudemux-head's config from files that, in production, may be FIFOs
// mounted by a secret manager (1Password Environments, for one). Opening a
// FIFO with no writer blocks forever, so a regression that removed the
// timeout would hang the whole TUI at startup. This test creates a real
// named pipe, never opens a writer, and asserts the read returns within a
// short bounded timeout rather than hanging.
func TestEnvFileValueFIFOTimeout(t *testing.T) {
	fifoPath := filepath.Join(t.TempDir(), "env-fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("Mkfifo() error = %v", err)
	}

	const shortTimeout = 200 * time.Millisecond
	start := time.Now()
	got, timedOut := readEnvFileValue(fifoPath, "ANTHROPIC_API_KEY", shortTimeout)
	elapsed := time.Since(start)

	if got != "" {
		t.Errorf("readEnvFileValue() = %q, want \"\" when no writer ever opens the FIFO", got)
	}
	if !timedOut {
		t.Error("readEnvFileValue() timedOut = false, want true — a writerless FIFO must be distinguishable from a genuinely empty read")
	}
	// Generous slack over shortTimeout so this isn't flaky under CI scheduling
	// jitter, while still catching an unbounded (or vastly larger) hang.
	if elapsed > 5*time.Second {
		t.Errorf("readEnvFileValue() took %v, want it bounded near the %v timeout — an unbounded FIFO open hangs the whole TUI at startup", elapsed, shortTimeout)
	}
}

// TestEnvFileValueWriterlessFIFOTimesOutOnceNotFour is Finding 1's regression
// test: envFileValue used to retry a writerless FIFO 1+len(envFileRetryDelays)
// times, each paying the full per-attempt timeout, so a locked secret agent
// stalled startup by ~4x the documented timeout instead of ~1x. It must now
// stop after the FIRST attempt times out — retries are for transient
// contention (an empty-but-not-timed-out read), not a dead writer.
//
// This drives envFileValue (not readEnvFileValue directly) through
// CLAUDEMUX_ENV, with a short injected timeout via a package-level override
// so the test runs fast and deterministic rather than waiting on the real 2s
// production value.
func TestEnvFileValueWriterlessFIFOTimesOutOnceNotFour(t *testing.T) {
	fifoPath := filepath.Join(t.TempDir(), "env-fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("Mkfifo() error = %v", err)
	}
	t.Setenv("CLAUDEMUX_ENV", fifoPath)

	const shortTimeout = 100 * time.Millisecond
	restoreTimeout := envFileTimeoutForTest(shortTimeout)
	defer restoreTimeout()

	start := time.Now()
	got := envFileValue("", "ANTHROPIC_API_KEY")
	elapsed := time.Since(start)

	if got != "" {
		t.Errorf("envFileValue() = %q, want \"\" when no writer ever opens the FIFO", got)
	}
	// Old behavior paid shortTimeout 1+len(envFileRetryDelays) = 4 times, plus
	// backoffs, well over 3x shortTimeout. The fix must stop after the first
	// timeout, so this stays comfortably under 2x — proving the retries did
	// NOT fire, not just that the test happened to run fast.
	if elapsed >= 2*shortTimeout {
		t.Errorf("envFileValue() took %v, want well under 2x the %v timeout (one attempt, no retry) — "+
			"a timed-out attempt means no writer at all, and retrying it pays the full timeout again for nothing",
			elapsed, shortTimeout)
	}
}

func TestClaudeHeadEnvPathsPrecedence(t *testing.T) {
	explicit := writeEnvFile(t, "ANTHROPIC_API_KEY=sk-from-explicit-path\n")
	t.Setenv("CLAUDEMUX_ENV", explicit)

	paths := claudeHeadEnvPaths("")
	if len(paths) != 1 || paths[0] != explicit {
		t.Fatalf("claudeHeadEnvPaths() = %v, want exactly [%q] when CLAUDEMUX_ENV is set — it must beat both the configured api_key_file and the default, not merely be tried first", paths, explicit)
	}

	if got := envFileValue("", "ANTHROPIC_API_KEY"); got != "sk-from-explicit-path" {
		t.Errorf("envFileValue() = %q, want the value from the CLAUDEMUX_ENV path", got)
	}
}

// A FIFO-backed env file (e.g. a mounted 1Password Environment) serves one
// reader at a time, so concurrently-starting panes race on the open and the
// losers see an empty pipe rather than an error. That is transient, so
// envFileValue retries. Model it with a FIFO whose writer only appears after
// the first attempt has already failed.
func TestEnvFileValueRetriesTransientlyEmptyFIFO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	t.Setenv("CLAUDEMUX_ENV", path)

	// Raise the per-attempt bound for the duration of this test, purely as
	// slack for the first attempt's writer goroutine below to get scheduled
	// to its initial open under load — the production 2s bound occasionally
	// wasn't enough for that alone. This does not paper over either race this
	// test used to have (see the two comments below): both are now closed by
	// construction, not by winning against a longer clock.
	defer func(orig time.Duration) { envFileTimeout = orig }(envFileTimeout)
	envFileTimeout = 30 * time.Second

	// Race #1: opening-then-closing a writer and immediately opening a second
	// one used to race envFileValue's own retry timing. readEnvFileValue
	// signals an attempt's outcome (via its result channel) before its
	// (formerly deferred) f.Close() necessarily ran, so a second writer
	// opened right after the first one closed could pair with that
	// still-registered first reader instead of blocking for the retry's
	// fresh one — silently orphaning the write, after which the retry's
	// reader had no writer left to pair with and blocked until envFileTimeout.
	//
	// envFileReadAttemptDone (env.go) closes this gap: it fires only once an
	// attempt's fd is actually closed (readEnvFileValue now closes it before
	// signaling, specifically to make this hook meaningful), so waiting on it
	// before opening the second writer guarantees zero readers are registered
	// at that point — the second writer's open() then has no choice but to
	// block for the retry's reader, which is the pairing this test requires.
	readerFDClosed := make(chan struct{}, 1)
	origHook := envFileReadAttemptDone
	envFileReadAttemptDone = func() { readerFDClosed <- struct{}{} }
	defer func() { envFileReadAttemptDone = origHook }()

	// Race #2 (found via a -count=300 in-process repro after fixing #1 alone
	// still left a ~1-in-40 flake): when the first writer opened and closed
	// with ZERO bytes ever written, the reader's blocked read occasionally
	// never observed EOF and hung for the full envFileTimeout — a missed
	// read-ready/hangup notification for a FIFO whose writer closes within
	// microseconds of the open rendezvous, reproducible independent of any
	// Go-level goroutine scheduling. Writing a real (non-matching) line
	// before closing avoids that zero-byte-transfer edge case entirely,
	// while still exercising the same "attempt finds no key" scenario
	// envFileValue must retry.
	go func() {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err == nil {
			f.WriteString("OTHER_KEY=noise\n")
			f.Close() // no matching key: reader scans to EOF, finds nothing
		}
		<-readerFDClosed
		f2, err2 := os.OpenFile(path, os.O_WRONLY, 0)
		if err2 == nil {
			f2.WriteString("ANTHROPIC_API_KEY=sk-retried\n")
			f2.Close()
		}
	}()

	if got := envFileValue("", "ANTHROPIC_API_KEY"); got != "sk-retried" {
		t.Errorf("envFileValue() = %q, want %q — a transiently empty FIFO must be retried, "+
			"or a pane that loses the open race silently disables its summarizer", got, "sk-retried")
	}
}

// The env file lives beside config.yml in the XDG config dir. It must NOT be
// looked for in ~/Projects — that path was hardcoded to one developer's machine.
func TestClaudeHeadEnvPathsUsesConfigDir(t *testing.T) {
	t.Setenv("CLAUDEMUX_ENV", "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")

	paths := claudeHeadEnvPaths("")

	want := filepath.Join("/xdg", "claudemux", "env")
	if len(paths) != 1 || paths[0] != want {
		t.Errorf("claudeHeadEnvPaths(%q) = %v, want exactly [%q]", "", paths, want)
	}
	for _, p := range paths {
		if strings.Contains(p, "Projects") {
			t.Errorf("claudeHeadEnvPaths() returned %q — the ~/Projects fallback is hardcoded to one machine and must be gone", p)
		}
	}
}

// A configured api_key_file is used when CLAUDEMUX_ENV is not set — this is
// what lets the secret live somewhere other than the default (e.g. a FIFO a
// secret manager already mounts at a path of its own choosing).
func TestClaudeHeadEnvPathsUsesConfiguredKeyFile(t *testing.T) {
	t.Setenv("CLAUDEMUX_ENV", "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")

	paths := claudeHeadEnvPaths("/elsewhere/claudemux/env")

	if len(paths) != 1 || paths[0] != "/elsewhere/claudemux/env" {
		t.Errorf("claudeHeadEnvPaths() = %v, want exactly [%q] — a configured api_key_file must beat the default", paths, "/elsewhere/claudemux/env")
	}
}

// CLAUDEMUX_ENV still overrides a configured api_key_file outright.
func TestClaudeHeadEnvVarBeatsConfiguredKeyFile(t *testing.T) {
	t.Setenv("CLAUDEMUX_ENV", "/from/env/var")

	paths := claudeHeadEnvPaths("/from/config/file")

	if len(paths) != 1 || paths[0] != "/from/env/var" {
		t.Errorf("claudeHeadEnvPaths() = %v, want exactly [%q] — CLAUDEMUX_ENV must override the configured path, not merely precede it", paths, "/from/env/var")
	}
}
