package main

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// restartArgv builds the argv for re-execing this binary.
//
// argv[0] becomes exe — the path os.Executable() resolves to right now —
// because picking up a rebuilt binary is the whole point of a restart, and the
// launcher passes a bare name that was resolved through PATH at startup. Every
// other argument is carried through verbatim: a head launched with --session is
// pinned to one transcript deliberately, and dropping the flag would quietly
// turn the restarted head into a follow-active one watching something else.
func restartArgv(exe string, args []string) []string {
	if len(args) == 0 {
		return []string{exe}
	}
	return append([]string{exe}, args[1:]...)
}

// restartSelf replaces this process with a fresh copy of the binary on disk.
//
// Called from main AFTER the Bubble Tea program has returned, never from
// inside Update: the TUI holds the terminal in raw mode on the alternate
// screen, and exec'ing out from under it would hand the new process a terminal
// nobody restored. Letting p.Run() unwind first means the replacement starts
// on a clean terminal, exactly as it would at launch.
//
// syscall.Exec only returns on failure, so everything after it is the error
// path. It reports the failure and returns; the caller exits non-zero, which
// leaves the pane on screen (remain-on-exit=failed) with the reason visible
// rather than closing it as if the user had asked to quit.
func restartSelf(stderr io.Writer) {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head: cannot restart: %v\n", err)
		return
	}
	err = syscall.Exec(exe, restartArgv(exe, os.Args), os.Environ())
	fmt.Fprintf(stderr, "claudemux-head: restart failed: %v\n", err)
}
