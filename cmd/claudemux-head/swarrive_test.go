package main

import "testing"

func TestSwArrivedFrom(t *testing.T) {
	fleet := []swSession{{Name: "api"}, {Name: "web"}}
	snap := func(clients, last map[string]string) swSnapshot {
		return swSnapshot{Sessions: fleet, Lobby: "switchboard", Clients: clients, ClientLast: last}
	}
	tests := []struct {
		name       string
		prev, next swSnapshot
		preferred  string
		want       string
	}{
		{"arrives from last session",
			snap(map[string]string{"c1": "api"}, nil),
			snap(map[string]string{"c1": "switchboard"}, map[string]string{"c1": "web"}), "", "web"},
		{"falls back to the session prev saw",
			snap(map[string]string{"c1": "api"}, nil),
			snap(map[string]string{"c1": "switchboard"}, nil), "", "api"},
		{"already on the lobby is not an arrival",
			snap(map[string]string{"c1": "switchboard"}, nil),
			snap(map[string]string{"c1": "switchboard"}, map[string]string{"c1": "web"}), "", ""},
		{"first poll counts as an arrival",
			swSnapshot{},
			snap(map[string]string{"c1": "switchboard"}, map[string]string{"c1": "web"}), "", "web"},
		{"origin outside the fleet is ignored",
			snap(map[string]string{"c1": "gone"}, nil),
			snap(map[string]string{"c1": "switchboard"}, map[string]string{"c1": "gone"}), "", ""},
		{"client elsewhere is not an arrival",
			snap(map[string]string{"c1": "api"}, nil),
			snap(map[string]string{"c1": "web"}, map[string]string{"c1": "api"}), "", ""},
		{"preferred client wins",
			snap(map[string]string{"a": "api", "b": "web"}, nil),
			snap(map[string]string{"a": "switchboard", "b": "switchboard"}, map[string]string{"a": "api", "b": "web"}), "b", "web"},
	}
	for _, tt := range tests {
		if got := swArrivedFrom(tt.prev, tt.next, tt.preferred); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestBuildSwSnapshotClientLastSession(t *testing.T) {
	s := buildSwSnapshot("", "", "c1\tswitchboard\tapi\nc2\tweb\t\n", "")
	if s.Clients["c1"] != "switchboard" || s.ClientLast["c1"] != "api" {
		t.Errorf("c1: clients=%q last=%q", s.Clients["c1"], s.ClientLast["c1"])
	}
	if _, ok := s.ClientLast["c2"]; ok || s.Clients["c2"] != "web" {
		t.Errorf("c2: clients=%q last=%v", s.Clients["c2"], s.ClientLast)
	}
}
