package main

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// CLAUDEMUX_TEARDOWN_LOG names a file that receives a line per teardown-relevant
// event. Unset (the default) makes every call here a no-op, so this costs a
// single env lookup per process and nothing per event.
//
// This exists because the only user-visible trace of a teardown abort is the
// status chip, which clears after teardownNoteTTL (5s). A wrap-up that runs for
// minutes can abort while nobody is looking at the pane, leaving "it gave up at
// some point" as the entire bug report. The log turns that into a timestamped
// reason.
const teardownLogEnv = "CLAUDEMUX_TEARDOWN_LOG"

var (
	teardownLogOnce sync.Once
	teardownLogFile *os.File
	teardownLogMu   sync.Mutex
)

// teardownLogf appends one timestamped line. It is safe from any goroutine:
// tea.Cmds run off the Update loop, and the poll command logs from there.
//
// Every failure is silent. A diagnostic that can crash the head, or write to
// stdout and corrupt the TUI's frame, is worse than no diagnostic at all.
func teardownLogf(format string, args ...any) {
	teardownLogOnce.Do(func() {
		path := os.Getenv(teardownLogEnv)
		if path == "" {
			return
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		teardownLogFile = f
	})
	if teardownLogFile == nil {
		return
	}
	teardownLogMu.Lock()
	defer teardownLogMu.Unlock()
	fmt.Fprintf(teardownLogFile, "%s %s\n",
		time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
}
