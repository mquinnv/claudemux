package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// lineageRow builds one lineageFormat line.
func lineageRow(name, created, parent, parentCreated, emoji, pname string) string {
	return strings.Join([]string{name, created, parent, parentCreated, emoji, pname}, "\t")
}

func TestParseLineageWalksToTheRoot(t *testing.T) {
	listing := strings.Join([]string{
		lineageRow("beejax", "100", "", "", "🐝", "beejax"),
		lineageRow("beejax-platform", "200", "beejax", "100", "🚢", ""),
		lineageRow("scala3d", "300", "beejax-platform", "200", "", ""),
		lineageRow("unrelated", "50", "", "", "🧵", "claudemux"),
	}, "\n") + "\n"
	got := parseLineage(listing, "scala3d")
	want := []crumb{{"beejax", "🐝", "beejax"}, {"beejax-platform", "🚢", ""}}
	if len(got) != len(want) {
		t.Fatalf("parseLineage = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("crumb %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A link holds only while the parent of that name was born when the child
// recorded — a recycled name, a missing parent, or an unrecorded time ends the
// chain, exactly where the lobby would draw the child as an orphan.
func TestParseLineageStopsAtABrokenLink(t *testing.T) {
	tests := []struct {
		name    string
		listing []string
		want    int
	}{
		{"parent gone", []string{lineageRow("kid", "200", "dad", "100", "", "")}, 0},
		{"name recycled", []string{
			lineageRow("dad", "150", "", "", "", ""),
			lineageRow("kid", "200", "dad", "100", "", ""),
		}, 0},
		{"time never recorded", []string{
			lineageRow("dad", "0", "", "", "", ""),
			lineageRow("kid", "200", "dad", "0", "", ""),
		}, 0},
		{"grandparent recycled", []string{
			lineageRow("gramps", "999", "", "", "", ""),
			lineageRow("dad", "100", "gramps", "50", "", ""),
			lineageRow("kid", "200", "dad", "100", "", ""),
		}, 1},
		{"cycle", []string{
			lineageRow("a", "100", "kid", "200", "", ""),
			lineageRow("kid", "200", "a", "100", "", ""),
		}, 1},
		{"self parent", []string{lineageRow("kid", "200", "kid", "200", "", "")}, 0},
	}
	for _, tt := range tests {
		if got := parseLineage(strings.Join(tt.listing, "\n"), "kid"); len(got) != tt.want {
			t.Errorf("%s: parseLineage = %+v, want %d crumbs", tt.name, got, tt.want)
		}
	}
}

func TestBreadcrumbText(t *testing.T) {
	bee := crumb{"beejax", "🐝", "beejax"}
	ship := crumb{"beejax-platform-2", "🚢", ""}
	plain := crumb{"hooks", "", ""}
	tests := []struct {
		name               string
		ancestors          []crumb
		emoji, pname, sess string
		want               string
	}{
		{"no parent is the plain badge", nil, "🧵", "claudemux", "claudemux-2", "🧵 claudemux"},
		{"no parent, no emoji stays empty", nil, "", "claudemux", "claudemux-2", ""},
		{"one parent", []crumb{bee}, "🧵", "claudemux", "claudemux-2", "🐝 beejax › 🧵 claudemux"},
		{"crumbs fall back to session names", []crumb{plain, ship}, "", "", "kid", "hooks › 🚢 beejax-platform-2 › kid"},
		{"deep chain elides the middle", []crumb{bee, plain, plain, ship}, "🧵", "claudemux", "x",
			"🐝 beejax › … › 🚢 beejax-platform-2 › 🧵 claudemux"},
	}
	for _, tt := range tests {
		if got := breadcrumbText(tt.ancestors, tt.emoji, tt.pname, tt.sess); got != tt.want {
			t.Errorf("%s: breadcrumbText = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestStateLineShowsBreadcrumb(t *testing.T) {
	now := time.Now()
	m := model{ready: true, width: 120, state: State{Kind: StateIdle, Since: now},
		projectEmoji: "🧵", projectName: "claudemux", sessionName: "claudemux-2",
		lineage: []crumb{{"beejax", "🐝", "beejax"}}}
	if got := renderStateLine(m, now); !strings.Contains(got, "🐝 beejax › 🧵 claudemux") {
		t.Errorf("renderStateLine = %q, want the breadcrumb", got)
	}
}

// A failed lineage read keeps the chain the head already has, so one bad poll
// does not blink the breadcrumb away; a successful empty read clears it.
func TestDataMsgLineage(t *testing.T) {
	m := model{lineage: []crumb{{"beejax", "🐝", "beejax"}}, sessionName: "kid"}
	next, _ := m.Update(dataMsg{time: time.Now()})
	if got := next.(model).lineage; len(got) != 1 {
		t.Errorf("failed read: lineage = %+v, want the old chain kept", got)
	}
	next, _ = m.Update(dataMsg{time: time.Now(), lineageOK: true, sessionName: "kid"})
	if got := next.(model).lineage; len(got) != 0 {
		t.Errorf("ok empty read: lineage = %+v, want it cleared", got)
	}
}

func TestParentKeyNeedsALiveParent(t *testing.T) {
	p := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")}
	m := model{selfPane: "%1"}
	if _, cmd := m.Update(p); cmd != nil {
		t.Error("p with no parent returned a command, want a no-op")
	}
	m.lineage = []crumb{{"beejax", "", ""}}
	if _, cmd := m.Update(p); cmd == nil {
		t.Error("p with a parent returned no command, want a switch")
	}
}
