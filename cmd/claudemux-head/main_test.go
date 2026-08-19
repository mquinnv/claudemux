package main

import "testing"

// Every subcommand main() dispatches must be recognized here, or the guard
// would reject a command that actually works. This is the half of the pairing
// that a `go test` run enforces; the other half — a subcommand added to
// knownSubcommands but never dispatched — is only a stale error message.
func TestKnownSubcommandsAreAccepted(t *testing.T) {
	for _, sub := range knownSubcommands {
		if got, ok := unknownSubcommand([]string{"claudemux-head", sub}); ok {
			t.Errorf("unknownSubcommand(%q) = %q, true; want it recognized", sub, got)
		}
	}
}

func TestUnknownSubcommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string // "" means "must be accepted"
	}{
		// The head itself: no args at all, or only flags, is the session
		// monitor and must never be turned into a usage error.
		{"bare head", []string{"claudemux-head"}, ""},
		{"pinned session", []string{"claudemux-head", "--session", "abc"}, ""},
		{"single-dash flag", []string{"claudemux-head", "-session", "abc"}, ""},
		{"flag terminator", []string{"claudemux-head", "--"}, ""},
		// A subcommand's own arguments are its business, not this guard's.
		{"subcommand with args", []string{"claudemux-head", "config", "get", "launch.layout"}, ""},
		{"project find", []string{"claudemux-head", "project", "find", "core"}, ""},

		// The case this exists for: an installed binary older than
		// bin/claudemux, asked for a subcommand it has never heard of.
		{"unknown word", []string{"claudemux-head", "onboarrd"}, "onboarrd"},
		{"typo", []string{"claudemux-head", "swtichboard"}, "swtichboard"},
		// An empty first argument is not a subcommand; flag parsing handles it.
		{"empty arg", []string{"claudemux-head", ""}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := unknownSubcommand(tc.args)
			if tc.want == "" {
				if ok {
					t.Errorf("unknownSubcommand(%q) = %q, true; want accepted", tc.args[1:], got)
				}
				return
			}
			if !ok || got != tc.want {
				t.Errorf("unknownSubcommand(%q) = %q, %v; want %q, true", tc.args[1:], got, ok, tc.want)
			}
		})
	}
}
