package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// tmuxRunner runs one tmux invocation and returns its stdout. runDefer takes
// it as a parameter so tests can fake the fleet and record the writes.
type tmuxRunner func(args ...string) (string, error)

// execTmux is the real tmuxRunner, with the usual hard deadline so a wedged
// tmux cannot hang an agent's shell call.
func execTmux(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	return string(out), err
}

// runDefer is `claudemux-head defer`: how an agent marks its own session as
// waiting on another one (and clears it again). It exists so the handoff skill
// no longer hand-writes tmux option commands: the link needs the blocker's
// creation stamp, the user's own defer must be left alone, and the session
// lookup must be by exact name — tmux prefix-matches -t targets, so `phenix`
// would silently resolve to `phenix-2`.
func runDefer(args []string, stdout, stderr io.Writer, getenv func(string) string, tmux tmuxRunner) int {
	fs := flag.NewFlagSet("defer", flag.ContinueOnError)
	fs.SetOutput(stderr)
	waitOn := fs.String("wait-on", "", "session this one is waiting on")
	reason := fs.String("reason", "", "what this session is waiting for")
	doClear := fs.Bool("clear", false, "clear a wait mark")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *doClear == (*waitOn != "") || (*doClear && *reason != "") {
		fmt.Fprintln(stderr, "usage: claudemux-head defer --wait-on <session> [--reason <text>] | --clear")
		return 2
	}

	pane := getenv("TMUX_PANE")
	if pane == "" {
		fmt.Fprintln(stderr, "claudemux-head defer: not running in a tmux pane ($TMUX_PANE is empty)")
		return 1
	}

	// A failed read is an error, never "no mark": writing over a user's
	// defer because tmux hiccuped would be the worst outcome here.
	out, err := tmux("display-message", "-p", "-t", pane, deferMarkFormat())
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head defer: cannot read this session's mark: %v\n", err)
		return 1
	}
	mark := parseDeferMark(out)
	if mark.Raw == deferOnValue {
		fmt.Fprintln(stdout, "skipped: this session is deferred by the user")
		return 0
	}

	if *doClear {
		if mark.Raw != deferWaitValue {
			return 0
		}
		if _, err := tmux(deferArgs(pane, false, "")...); err != nil {
			fmt.Fprintf(stderr, "claudemux-head defer: %v\n", err)
			return 1
		}
		return 0
	}

	self, err := tmux("display-message", "-p", "-t", pane, "#{session_name}")
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head defer: cannot read this session's name: %v\n", err)
		return 1
	}
	if strings.TrimSpace(self) == *waitOn {
		fmt.Fprintln(stderr, "claudemux-head defer: a session cannot wait on itself")
		return 1
	}
	fleet, err := tmux("list-sessions", "-F", "#{session_name}\t#{session_created}")
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head defer: cannot list sessions: %v\n", err)
		return 1
	}
	created := ""
	for _, line := range strings.Split(fleet, "\n") {
		if name, stamp, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t"); ok && name == *waitOn {
			created = strings.TrimSpace(stamp)
			break
		}
	}
	link := deferLink{On: *waitOn, Created: created}
	if !link.set() {
		fmt.Fprintf(stderr, "claudemux-head defer: no live tmux session named %q\n", *waitOn)
		return 1
	}

	why := *reason
	if sanitizeDeferReason(why) == "" {
		why = "waiting on " + *waitOn
	}
	if _, err := tmux(deferLinkedArgs(pane, deferWaitValue, why, link)...); err != nil {
		fmt.Fprintf(stderr, "claudemux-head defer: %v\n", err)
		return 1
	}
	return 0
}
