package main

import (
	"slices"
	"strings"
	"testing"
)

func fam(name string, created int64, parent string, pcreated int64) swSession {
	return swSession{Name: name, Created: created, Parent: parent, ParentCreated: pcreated}
}

func withDefer(s swSession) swSession { s.Deferred = true; return s }

func famNames(ss []swSession) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.Name)
	}
	return out
}

func famPrefixes(ss []swSession) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.TreePrefix)
	}
	return out
}

func TestArrangeFamiliesNestsAndKeepsActive(t *testing.T) {
	// iai-admin is deferred but its children usm/ag are active, so the whole
	// block rides in the active group at iai-admin's list position.
	in := []swSession{
		fam("remix-3", 10, "", 0),
		withDefer(fam("iai-admin", 20, "", 0)),
		fam("remix-6", 30, "", 0),
		withDefer(fam("digiscreener", 5, "", 0)),
		withDefer(fam("beejax", 40, "iai-admin", 20)),
		fam("usm", 41, "iai-admin", 20),
		fam("ag", 42, "iai-admin", 20),
	}
	got := swArrangeFamilies(in)
	wantNames := []string{"remix-3", "iai-admin", "beejax", "usm", "ag", "remix-6", "digiscreener"}
	if !slices.Equal(famNames(got), wantNames) {
		t.Fatalf("order = %v, want %v", famNames(got), wantNames)
	}
	wantPrefix := []string{"", "", "├ ", "├ ", "└ ", "", ""}
	if !slices.Equal(famPrefixes(got), wantPrefix) {
		t.Errorf("prefixes = %q, want %q", famPrefixes(got), wantPrefix)
	}
	wantDepth := []int{0, 0, 1, 1, 1, 0, 0}
	for i, s := range got {
		if s.Depth != wantDepth[i] {
			t.Errorf("%s depth = %d, want %d", s.Name, s.Depth, wantDepth[i])
		}
		wantG := swGroupActive
		if s.Name == "digiscreener" {
			wantG = swGroupDeferred
		}
		if s.FamilyGroup != wantG {
			t.Errorf("%s FamilyGroup = %v, want %v", s.Name, s.FamilyGroup, wantG)
		}
		if s.From != "" {
			t.Errorf("%s From = %q, want empty", s.Name, s.From)
		}
	}
}

func TestArrangeFamiliesAllDeferredSinks(t *testing.T) {
	in := []swSession{
		withDefer(fam("p", 1, "", 0)),
		withDefer(fam("c", 2, "p", 1)),
		fam("other", 3, "", 0),
	}
	got := swArrangeFamilies(in)
	if want := []string{"other", "p", "c"}; !slices.Equal(famNames(got), want) {
		t.Fatalf("order = %v, want %v", famNames(got), want)
	}
	for _, s := range got[1:] {
		if s.FamilyGroup != swGroupDeferred {
			t.Errorf("%s FamilyGroup = %v, want deferred", s.Name, s.FamilyGroup)
		}
	}
	if got[0].FamilyGroup != swGroupActive {
		t.Errorf("other FamilyGroup = %v, want active", got[0].FamilyGroup)
	}
}

func TestArrangeFamiliesGrandchildren(t *testing.T) {
	in := []swSession{
		fam("a", 1, "", 0),
		fam("d", 4, "a", 1),
		fam("c", 3, "b", 2),
		fam("b", 2, "a", 1),
	}
	got := swArrangeFamilies(in)
	if want := []string{"a", "b", "c", "d"}; !slices.Equal(famNames(got), want) {
		t.Fatalf("order = %v, want %v", famNames(got), want)
	}
	if want := []string{"", "├ ", "│ └ ", "└ "}; !slices.Equal(famPrefixes(got), want) {
		t.Errorf("prefixes = %q, want %q", famPrefixes(got), want)
	}
	wantDepth := []int{0, 1, 2, 1}
	for i, s := range got {
		if s.Depth != wantDepth[i] {
			t.Errorf("%s depth = %d, want %d", s.Name, s.Depth, wantDepth[i])
		}
	}
}

func TestArrangeFamiliesNameReuseIsOrphan(t *testing.T) {
	in := []swSession{
		fam("beejax-2", 200, "", 0),
		fam("kid", 300, "beejax-2", 100),
	}
	got := swArrangeFamilies(in)
	if want := []string{"beejax-2", "kid"}; !slices.Equal(famNames(got), want) {
		t.Fatalf("order = %v, want %v", famNames(got), want)
	}
	if got[1].Depth != 0 || got[1].TreePrefix != "" || got[1].From != "beejax-2" {
		t.Errorf("kid = depth %d prefix %q from %q, want top-level orphan of beejax-2", got[1].Depth, got[1].TreePrefix, got[1].From)
	}
}

func TestArrangeFamiliesMissingParentIsOrphan(t *testing.T) {
	got := swArrangeFamilies([]swSession{fam("kid", 5, "gone", 1)})
	if len(got) != 1 || got[0].Depth != 0 || got[0].From != "gone" {
		t.Fatalf("got %+v, want one top-level orphan from gone", got)
	}
}

func TestArrangeFamiliesCycle(t *testing.T) {
	in := []swSession{
		fam("a", 1, "b", 2),
		fam("b", 2, "a", 1),
		fam("c", 3, "c", 3),
	}
	got := swArrangeFamilies(in)
	names := famNames(got)
	slices.Sort(names)
	if want := []string{"a", "b", "c"}; !slices.Equal(names, want) {
		t.Fatalf("names = %v, want each exactly once", names)
	}
	// c is its own parent: an orphan of itself, never a child of itself.
	for _, s := range got {
		if s.Name == "c" && (s.Depth != 0 || s.From != "c") {
			t.Errorf("c = depth %d from %q, want top-level orphan", s.Depth, s.From)
		}
	}
}

func TestArrangeFamiliesChildOrderByCreated(t *testing.T) {
	in := []swSession{
		fam("p", 1, "", 0),
		fam("ag", 42, "p", 1),
		fam("usm", 41, "p", 1),
	}
	got := swArrangeFamilies(in)
	if want := []string{"p", "usm", "ag"}; !slices.Equal(famNames(got), want) {
		t.Fatalf("order = %v, want %v", famNames(got), want)
	}
}

func TestBuildSwSnapshotParsesParentFields(t *testing.T) {
	base := []string{"s", "Idle", "100", "10", "sum", "prm", "m", "", "", "", "", "", "d", ""}
	long := strings.Join(append(slices.Clone(base), "777", "boss", "555"), "\t")
	shortF := slices.Clone(base[:12])
	shortF[0] = "t"
	short := strings.Join(shortF, "\t")
	paneOut := "s\t%1\tclaudemux-head\ttopic\nt\t%2\tclaudemux-head\ttopic"
	snap := buildSwSnapshot(long+"\n"+short, paneOut, "", "")
	if len(snap.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(snap.Sessions))
	}
	var s, u swSession
	for _, x := range snap.Sessions {
		if x.Name == "s" {
			s = x
		} else {
			u = x
		}
	}
	if s.Created != 777 || s.Parent != "boss" || s.ParentCreated != 555 {
		t.Errorf("long line: %+v", s)
	}
	if u.Name != "t" || u.Created != 0 || u.Parent != "" || u.ParentCreated != 0 {
		t.Errorf("short line: %+v", u)
	}
}
