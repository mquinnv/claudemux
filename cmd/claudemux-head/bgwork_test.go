package main

import (
	"testing"
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
		{
			"prose about background work is not a launch",
			Event{ToolResults: []ToolResult{{Content: "the job is running in background somewhere, no ID here"}}},
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
