package main

import (
	"testing"
	"time"
)

func TestBgLaunches(t *testing.T) {
	tests := []struct {
		name string
		ev   Event
		want string
	}{
		{
			"background shell",
			Event{ToolResults: []ToolResult{{Content: "Command running in background with ID: boigiwsir. Output is being written to: /tmp/x"}}},
			"boigiwsir",
		},
		{
			"async agent",
			Event{ToolResults: []ToolResult{{Content: "Async agent launched successfully. (…)\nagentId: afbbf7a8f9ee52e81 (internal ID - do not mention)"}}},
			"afbbf7a8f9ee52e81",
		},
		{
			"an ordinary tool result launches nothing",
			Event{ToolResults: []ToolResult{{Content: "total 42\ndrwxr-xr-x  bin"}}},
			"",
		},
		// Both fixtures below are real shapes that fooled the old unanchored
		// search: a Grep hit or a file read that merely quotes a marker id
		// registered a phantom launch. Both must yield no launches now.
		{
			"a Grep hit quoting agentId is not a launch",
			Event{ToolResults: []ToolResult{{Content: "src/agent.ts:42:  const opts = { agentId: agentRecord.id };"}}},
			"",
		},
		{
			"the shell marker mid-sentence, not at line start, is not a launch",
			Event{ToolResults: []ToolResult{{Content: "As documented: Command running in background with ID: someid appears mid-paragraph."}}},
			"",
		},
		{"no tool results", Event{Type: "assistant"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bgLaunches(tt.ev)
			if tt.want == "" {
				if len(got) != 0 {
					t.Errorf("bgLaunches = %q, want none", got)
				}
				return
			}
			if len(got) != 1 || got[0] != tt.want {
				t.Errorf("bgLaunches = %q, want [%s]", got, tt.want)
			}
		})
	}
}

func TestBgCompletions(t *testing.T) {
	const payload = "<task-notification>\n<task-id>boigiwsir</task-id>\n" +
		"<tool-use-id>toolu_01VSdCK</tool-use-id>\n<status>completed</status>\n</task-notification>"

	t.Run("queue-operation form", func(t *testing.T) {
		got := bgCompletions(Event{Type: "queue-operation", QueueText: payload})
		if len(got) != 1 || got[0] != "boigiwsir" {
			t.Errorf("bgCompletions = %q, want [boigiwsir]", got)
		}
	})

	t.Run("delivered user turn form", func(t *testing.T) {
		got := bgCompletions(Event{Type: "user", UserText: payload})
		if len(got) != 1 || got[0] != "boigiwsir" {
			t.Errorf("bgCompletions = %q, want [boigiwsir]", got)
		}
	})

	// The literal string appears in ordinary skill documentation. Matching it
	// mid-text would retire tasks that never completed.
	t.Run("prose mentioning the tag is not a completion", func(t *testing.T) {
		prose := "Monitor fires `<task-notification>` messages and wakes this loop. " +
			"See <task-id>boigiwsir</task-id> in the docs."
		if got := bgCompletions(Event{Type: "user", UserText: prose}); len(got) != 0 {
			t.Errorf("bgCompletions = %q, want none: prose is not a notification", got)
		}
	})

	t.Run("notification without a task id", func(t *testing.T) {
		if got := bgCompletions(Event{Type: "user", UserText: "<task-notification>\n<status>failed</status>"}); len(got) != 0 {
			t.Errorf("bgCompletions = %q, want none", got)
		}
	})
}

func bgLaunchEvent(id, ts string) Event {
	return Event{
		Type:        "user",
		Timestamp:   ts,
		ToolResults: []ToolResult{{Content: "Command running in background with ID: " + id + ". Output is being written to: /tmp/x"}},
	}
}

func bgDoneEvent(id string) Event {
	return Event{Type: "queue-operation", QueueText: "<task-notification>\n<task-id>" + id + "</task-id>\n<status>completed</status>"}
}

func TestBgTrackerPairsLaunchAndCompletion(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe([]Event{bgLaunchEvent("aaa", "2026-08-11T10:00:00Z")}, now)
	if n, _ := b.outstanding(now); n != 1 {
		t.Fatalf("outstanding = %d, want 1 after a launch", n)
	}
	b.observe([]Event{bgDoneEvent("aaa")}, now)
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0 after its completion", n)
	}
}

func TestBgTrackerCountsAndOldest(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 10, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe([]Event{
		bgLaunchEvent("aaa", "2026-08-11T10:00:00Z"),
		bgLaunchEvent("bbb", "2026-08-11T10:05:00Z"),
	}, now)
	n, oldest := b.outstanding(now)
	if n != 2 {
		t.Errorf("outstanding = %d, want 2", n)
	}
	if want := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC); !oldest.Equal(want) {
		t.Errorf("oldest = %v, want %v: the duration must read as how long work has been out", oldest, want)
	}
	// Retiring the older one moves the clock to the survivor.
	b.observe([]Event{bgDoneEvent("aaa")}, now)
	if _, oldest = b.outstanding(now); !oldest.Equal(time.Date(2026, 8, 11, 10, 5, 0, 0, time.UTC)) {
		t.Errorf("oldest = %v, want the surviving launch", oldest)
	}
}

// A task that never notifies must not mark the session busy forever — that
// would make the conductor refuse to ever visit it.
func TestBgTrackerExpiresStaleLaunches(t *testing.T) {
	b := newBgTracker()
	b.observe([]Event{bgLaunchEvent("aaa", "2026-08-11T10:00:00Z")}, time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC))
	late := time.Date(2026, 8, 11, 10, 31, 0, 0, time.UTC)
	if n, _ := b.outstanding(late); n != 0 {
		t.Errorf("outstanding = %d, want 0: a launch past the cap stops counting", n)
	}
}

// If the human typed at the session, whatever it was tracking is moot.
func TestBgTrackerClearedByGenuinePrompt(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe([]Event{bgLaunchEvent("aaa", "2026-08-11T10:00:00Z")}, now)
	b.observe([]Event{{Type: "user", UserText: "what's up?"}}, now)
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0 after a real prompt", n)
	}
}

// The delivered notification turn is a user event, but it is not the human
// typing — it must not be mistaken for one.
func TestBgTrackerNotificationTurnIsNotAPrompt(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe([]Event{
		bgLaunchEvent("aaa", "2026-08-11T10:00:00Z"),
		bgLaunchEvent("bbb", "2026-08-11T10:00:00Z"),
	}, now)
	b.observe([]Event{{Type: "user", UserText: "<task-notification>\n<task-id>aaa</task-id>"}}, now)
	if n, _ := b.outstanding(now); n != 1 {
		t.Errorf("outstanding = %d, want 1: the notification retires its own task, not the set", n)
	}
}

// The spec requires this to be harmless: the same task id can arrive as
// both completion forms (the queue-operation that lands the moment the task
// ends, and the delivered user turn once the session wakes to consume it).
// Retiring it twice must not error or resurrect it.
func TestBgTrackerIdempotentAcrossCompletionForms(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe([]Event{bgLaunchEvent("aaa", "2026-08-11T10:00:00Z")}, now)
	b.observe([]Event{bgDoneEvent("aaa")}, now)                                                                                  // queue-operation form
	b.observe([]Event{{Type: "user", UserText: "<task-notification>\n<task-id>aaa</task-id>\n<status>completed</status>"}}, now) // delivered form, same id
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0: the same id retired by both forms must stay retired", n)
	}
}

// A completion for an id this tracker never launched (e.g. from before a
// head restart) must be a harmless no-op delete, not a crash or a negative
// count.
func TestBgTrackerCompletionForUnknownIDIsHarmless(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe([]Event{bgDoneEvent("neverlaunched")}, now)
	if n, _ := b.outstanding(now); n != 0 {
		t.Errorf("outstanding = %d, want 0", n)
	}
}

// The same id launched twice (a retried tool call, say) must not double
// count — the tracker keys on id, so the second launch just re-stamps it.
func TestBgTrackerSameIDLaunchedTwice(t *testing.T) {
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	b := newBgTracker()
	b.observe([]Event{bgLaunchEvent("aaa", "2026-08-11T10:00:00Z")}, now)
	b.observe([]Event{bgLaunchEvent("aaa", "2026-08-11T10:05:00Z")}, now)
	if n, _ := b.outstanding(now); n != 1 {
		t.Errorf("outstanding = %d, want 1: relaunching the same id must not double-count", n)
	}
}
