package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

const prLinkLine = `{"type":"pr-link","sessionId":"x","prNumber":32,"prUrl":"https://github.com/ameriglide/it-admin/pull/32","prRepository":"ameriglide/it-admin","timestamp":"2026-11-03T18:03:07.850Z"}`

func TestParseEventReadsPRLink(t *testing.T) {
	e, ok := parseEvent(prLinkLine)
	if !ok {
		t.Fatal("pr-link line did not parse")
	}
	want := PRLink{Number: 32, Repo: "ameriglide/it-admin", URL: "https://github.com/ameriglide/it-admin/pull/32"}
	if e.PR != want {
		t.Errorf("PR = %+v, want %+v", e.PR, want)
	}
	// An ordinary record carries no PR.
	if e, _ := parseEvent(`{"type":"user","message":{"role":"user","content":"hi"}}`); e.PR.Number != 0 {
		t.Errorf("user record has PR %+v", e.PR)
	}
}

func TestLastPRLinkNewestWinsAndKeepsPrev(t *testing.T) {
	prev := PRLink{Number: 1, Repo: "a/b"}
	if got := lastPRLink([]Event{{Type: "user"}}, prev); got != prev {
		t.Errorf("batch with no pr-link: got %+v, want prev %+v", got, prev)
	}
	evs := []Event{{PR: PRLink{Number: 5, Repo: "a/b"}}, {Type: "user"}, {PR: PRLink{Number: 9, Repo: "a/b"}}}
	if got := lastPRLink(evs, prev); got.Number != 9 {
		t.Errorf("got #%d, want the newest, #9", got.Number)
	}
}

// A PR linked early in a long session must survive the seed's ring
// truncation, and a later Tail must move to a newer PR.
func TestEventReaderPRSurvivesRingAndTails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	var lines []string
	lines = append(lines, prLinkLine)
	for range 50 {
		lines = append(lines, `{"type":"user","message":{"role":"user","content":"hi"}}`)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newEventReader(path)
	r.SeedFromEnd(5)
	if got := r.PR(); got.Number != 32 {
		t.Fatalf("after seed PR = %+v, want #32", got)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(strings.Replace(prLinkLine, `"prNumber":32`, `"prNumber":40`, 1) + "\n")
	f.Close()
	if _, err := r.Tail(); err != nil {
		t.Fatal(err)
	}
	if got := r.PR(); got.Number != 40 {
		t.Errorf("after tail PR = %+v, want #40", got)
	}
}

func TestPROptionRoundTrip(t *testing.T) {
	v := prOptionValue(PRLink{Number: 32, Repo: "ameriglide/it-admin"})
	if v != "ameriglide/it-admin#32" {
		t.Fatalf("option value = %q", v)
	}
	repo, num, ok := parsePROption(v)
	if !ok || repo != "ameriglide/it-admin" || num != 32 {
		t.Errorf("parse = %q %d %v", repo, num, ok)
	}
	if prOptionValue(PRLink{}) != "" {
		t.Error("no PR must publish empty")
	}
	for _, bad := range []string{"", "nohash", "a/b#", "a/b#x", "a/b#0"} {
		if _, _, ok := parsePROption(bad); ok {
			t.Errorf("parsePROption(%q) ok, want malformed", bad)
		}
	}
	if got := prURL("a/b", 7); got != "https://github.com/a/b/pull/7" {
		t.Errorf("prURL = %q", got)
	}
	if prURL("", 7) != "" {
		t.Error("prURL with no repo must be empty")
	}
}

func TestBuildSwSnapshotParsesPR(t *testing.T) {
	base := []string{"s", "Idle", "100", "10", "sum", "prm", "m", "", "", "", "", "", "d", "", "777", "", ""}
	withPR := strings.Join(append(slices.Clone(base), "ameriglide/it-admin#32"), "\t")
	without := slices.Clone(base)
	without[0] = "t"
	paneOut := "s\t%1\tclaudemux-head\ttopic\nt\t%2\tclaudemux-head\ttopic"
	snap := buildSwSnapshot(withPR+"\n"+strings.Join(without, "\t"), paneOut, "", "")
	if len(snap.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(snap.Sessions))
	}
	for _, s := range snap.Sessions {
		switch s.Name {
		case "s":
			if s.PRRepo != "ameriglide/it-admin" || s.PRNumber != 32 || s.Created != 777 {
				t.Errorf("18-field line: %+v", s)
			}
		case "t":
			if s.PRNumber != 0 || s.Created != 777 {
				t.Errorf("17-field line: %+v", s)
			}
		}
	}
}

func TestSwViewShowsPRColumnOnlyWhenAnySessionHasOne(t *testing.T) {
	m := swTestModel()
	plain := m.View()
	if strings.Contains(plain, "#") {
		t.Fatalf("no PRs, yet the view shows one:\n%s", plain)
	}
	m.snap.Sessions[1].PRRepo, m.snap.Sessions[1].PRNumber = "a/b", 1234
	view := m.View()
	if !strings.Contains(view, "#1234") {
		t.Fatalf("view missing the PR:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > m.width {
			t.Errorf("line is %d cells, wider than the %d-cell pane: %q", w, m.width, line)
		}
	}
	v := buildWebFleetView(m.snap, RateLimits{}, false, nil, time.Now(), webHeadline{})
	for _, s := range v.Sessions {
		if s.Name == "web" && (s.PR != "#1234" || s.PRURL != "https://github.com/a/b/pull/1234") {
			t.Errorf("web session PR = %q %q", s.PR, s.PRURL)
		}
	}
}
