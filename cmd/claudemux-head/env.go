package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// envFileTimeout bounds a SINGLE attempt at reading an env file. The file may be
// a FIFO mounted by a secret manager (1Password Environments, for one), and
// opening a FIFO with no writer blocks forever — so a locked or absent secret
// agent would otherwise hang the whole TUI at startup, not merely the
// summarizer. This is also the worst-case stall envFileValue can incur for a
// given path: a timed-out attempt means "no writer at all," which will never
// resolve by retrying, so envFileValue stops after the first one (see
// readEnvFileValue's timedOut return and envFileValue below). A retried
// attempt only ever pays envFileTimeout once per path.
//
// Not a const: tests override it with a short duration to exercise the
// timeout path (writerless FIFO, "stop retrying after one timeout") without
// waiting on the real 2s production value.
var envFileTimeout = 2 * time.Second

// claudeHeadEnvPaths lists the files claudemux-head reads its OWN secrets from,
// highest precedence first. Deliberately not the monitored project's env:
// claudemux-head watches sessions across many repos and must never inherit their
// secrets, nor depend on which repo a pane happened to launch from.
//
// keyFile is summary.api_key_file, already resolved to an absolute path by
// loadConfig. It is passed in rather than re-derived so config.yml stays the
// single place that decides where the secret comes from.
//
// CLAUDEMUX_ENV, when set, is the ONLY path consulted — it overrides both the
// configured path and the default outright rather than merely being tried first,
// so a caller can point claudemux-head at a specific file and be certain nothing
// else is read.
func claudeHeadEnvPaths(keyFile string) []string {
	if p := os.Getenv("CLAUDEMUX_ENV"); p != "" {
		return []string{p}
	}
	if keyFile != "" {
		return []string{keyFile}
	}
	// No configured path: callers that never went through loadConfig (tests,
	// mostly) still get the documented default rather than nothing.
	dir, err := configDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(dir, "env")}
}

// envFileRetryDelays are the backoffs between attempts to read one env file.
//
// A FIFO-backed env file (e.g. a mounted 1Password Environment) serves one reader
// at a time: when several claudemux-head panes start at once they race on the open,
// and the losers see an empty pipe — not an error, just no key, which would
// silently disable the summarizer for most panes. Contention is transient (a loser
// succeeds milliseconds later), so retry. The first attempt is immediate; these
// are the waits before each retry.
//
// These retries exist ONLY for contention (an empty-but-not-timed-out read).
// A read that times out means there is no writer at all — a locked or absent
// secret agent — and every subsequent attempt would time out identically, so
// envFileValue does not retry that case; see readEnvFileValue's timedOut
// return and the timedOut check in envFileValue below.
var envFileRetryDelays = []time.Duration{
	50 * time.Millisecond,
	150 * time.Millisecond,
	400 * time.Millisecond,
}

// envFileValue returns key from the first env file that yields it, or "" if no
// file provides it. Values are unquoted (a FIFO-backed source like a 1Password
// Environment serves them bare).
func envFileValue(keyFile, key string) string {
	for _, path := range claudeHeadEnvPaths(keyFile) {
		// Retry only a file that EXISTS but yielded nothing — that is the contended
		// FIFO. A path that simply isn't there will never produce a key, and sleeping
		// on it would tax startup for every user who has no env file at all.
		if _, err := os.Stat(path); err != nil {
			continue
		}
		for attempt := 0; ; attempt++ {
			v, timedOut := readEnvFileValue(path, key, envFileTimeout)
			if v != "" {
				return v
			}
			// A timeout means no writer ever showed up (a locked or absent secret
			// agent), not contention — every further attempt on this path would
			// block for the full envFileTimeout again with no better odds of
			// success. Stop now rather than paying envFileTimeout 1+len(delays)
			// times: that is the difference between an ~8.6s startup stall and a
			// ~2s one.
			if timedOut {
				break
			}
			if attempt >= len(envFileRetryDelays) {
				break
			}
			time.Sleep(envFileRetryDelays[attempt])
		}
	}
	return ""
}

// envFileReadAttemptDone, when non-nil, is called exactly once per completed
// attempt — one signal per attempt, whatever its outcome, whether that means
// an explicit f.Close() (key found, or scan reached EOF with no match) or the
// open itself failing (nothing to close). Left nil in production — it exists
// solely so a test can learn precisely when an attempt has finished and
// released whatever fd it held, rather than inferring it from timing.
// Without this seam, a test simulating a transiently-empty FIFO has no way to
// know when it is safe for a second writer to open the same path: opening
// too early pairs with the still-registered (but logically finished) first
// reader instead of the retry's fresh one, silently orphaning the write. See
// TestEnvFileValueRetriesTransientlyEmptyFIFO.
var envFileReadAttemptDone func()

// readEnvFileValue reads key from path, bounded by timeout. It returns
// timedOut=true when the bound was hit before the read completed (the path is
// a FIFO with no writer), and timedOut=false for every other outcome —
// including "file opened fine but the key isn't in it" and "open failed
// outright" — so callers can tell "no writer at all" (pointless to retry)
// apart from "read completed and simply found nothing" (worth retrying, per
// envFileRetryDelays above).
func readEnvFileValue(path, key string, timeout time.Duration) (value string, timedOut bool) {
	// The read runs in a goroutine because a FIFO open is uninterruptible: if no
	// writer ever appears, this goroutine parks for the life of the process. That
	// leak is bounded: envFileValue stops retrying a path the instant one attempt
	// times out (see above), so a path contributes at most one parked goroutine,
	// not one per retry. summarizerEnvOptions does two lookups (API key, then
	// base URL) against the single default path, so the worst case at startup is
	// 2 parked goroutines total, not 2 per lookup.
	ch := make(chan string, 1)
	go func() {
		f, err := os.Open(path)
		if err != nil {
			if envFileReadAttemptDone != nil {
				envFileReadAttemptDone()
			}
			ch <- ""
			return
		}

		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok || strings.TrimSpace(k) != key {
				continue
			}
			// Close before signaling completion (not via defer, which would
			// only run after this goroutine returns — after the value is
			// already on the channel and the caller may have moved on). A
			// caller must be able to rely on the fd being released the
			// instant it observes this attempt's outcome.
			f.Close()
			if envFileReadAttemptDone != nil {
				envFileReadAttemptDone()
			}
			ch <- strings.TrimSpace(v)
			return
		}
		f.Close()
		if envFileReadAttemptDone != nil {
			envFileReadAttemptDone()
		}
		ch <- ""
	}()

	select {
	case v := <-ch:
		return v, false
	case <-time.After(timeout):
		return "", true
	}
}
