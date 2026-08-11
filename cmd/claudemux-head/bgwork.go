package main

import (
	"regexp"
	"strings"
)

// Tracking work a session launched and then stopped waiting on: async agents
// and run_in_background shells. Both resolve their tool_use at LAUNCH, so the
// main thread's turn ends and classifyState would otherwise call the session
// Idle — which isWaiting reads as "waiting on the human", sending the
// switchboard's conductor into a session that is busy.
//
// Launches are recognized by the text of the tool_result that started them;
// completions by the notification that carries the same id back. See
// docs/superpowers/specs/2026-08-11-background-work-state-design.md for the
// verified event shapes.

var (
	bgShellRe  = regexp.MustCompile(`running in background with ID: ([A-Za-z0-9]+)`)
	bgAgentRe  = regexp.MustCompile(`agentId: ([A-Za-z0-9]+)`)
	bgTaskIDRe = regexp.MustCompile(`<task-id>([A-Za-z0-9]+)</task-id>`)
)

// bgNotificationPrefix opens a task-notification payload. Recognition is a
// PREFIX check, never a substring search: the literal tag appears in ordinary
// prose — skill documentation quotes it — and a substring match would retire
// tasks that are still running.
const bgNotificationPrefix = "<task-notification>"

// bgLaunches returns the ids of background tasks this event launched.
func bgLaunches(e Event) []string {
	var ids []string
	for _, tr := range e.ToolResults {
		for _, re := range []*regexp.Regexp{bgShellRe, bgAgentRe} {
			if m := re.FindStringSubmatch(tr.Content); m != nil {
				ids = append(ids, m[1])
			}
		}
	}
	return ids
}

// bgCompletions returns the ids this event reports as finished. Both delivery
// forms count: the queue-operation that lands the moment the task ends, and the
// user turn carrying the same payload once the session wakes to consume it.
// Retiring an id twice is harmless, and handling both means the feature
// survives either form going away.
func bgCompletions(e Event) []string {
	var ids []string
	for _, text := range []string{e.QueueText, e.UserText} {
		if !strings.HasPrefix(strings.TrimSpace(text), bgNotificationPrefix) {
			continue
		}
		if m := bgTaskIDRe.FindStringSubmatch(text); m != nil {
			ids = append(ids, m[1])
		}
	}
	return ids
}
