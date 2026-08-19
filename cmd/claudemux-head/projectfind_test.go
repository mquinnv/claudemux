package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkProjects builds a project-root tree from slash-separated relative paths and
// returns the root. Every path is created as a directory: the finder only ever
// looks at directories, and a file with a matching name must not be offered as
// a launch target.
func mkProjects(t *testing.T, paths ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(p)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestFindProjectExactBasename(t *testing.T) {
	root := mkProjects(t, "claudemux", "zulu")

	got, ok := findProject([]string{root}, "claudemux")
	if !ok {
		t.Fatal("findProject() ok = false, want true for a directory sitting directly under the root")
	}
	if want := filepath.Join(root, "claudemux"); got != want {
		t.Errorf("findProject() = %q, want %q", got, want)
	}
}

// The org-nested layout (~/Projects/<org>/<repo>) is half of why this setting
// exists; a depth-1-only walk would find nothing here.
func TestFindProjectMatchesNestedProject(t *testing.T) {
	root := mkProjects(t, "ameriglide/core")

	got, ok := findProject([]string{root}, "core")
	if !ok {
		t.Fatal("findProject() ok = false, want true for a project one level below the root")
	}
	if want := filepath.Join(root, "ameriglide", "core"); got != want {
		t.Errorf("findProject() = %q, want %q", got, want)
	}
}

// Depth 3 is out of range on purpose: it is where a project's own
// subdirectories live (including .claude/worktrees), not where projects live.
func TestFindProjectIgnoresBelowDepthTwo(t *testing.T) {
	root := mkProjects(t, "ameriglide/core/internal")

	if got, ok := findProject([]string{root}, "internal"); ok {
		t.Errorf("findProject() = %q, want no match — depth 3 is inside a project, not a project", got)
	}
}

func TestFindProjectExactBeatsPrefix(t *testing.T) {
	root := mkProjects(t, "muxer", "mux")

	got, ok := findProject([]string{root}, "mux")
	if !ok {
		t.Fatal("findProject() ok = false, want true")
	}
	if want := filepath.Join(root, "mux"); got != want {
		t.Errorf("findProject() = %q, want %q — an exact name must win over a longer one it prefixes", got, want)
	}
}

func TestFindProjectPrefixBeatsSubstring(t *testing.T) {
	root := mkProjects(t, "claudemux", "muxtools")

	got, ok := findProject([]string{root}, "mux")
	if !ok {
		t.Fatal("findProject() ok = false, want true")
	}
	if want := filepath.Join(root, "muxtools"); got != want {
		t.Errorf("findProject() = %q, want %q — a name starting with the query beats one merely containing it", got, want)
	}
}

func TestFindProjectMatchesCaseInsensitively(t *testing.T) {
	root := mkProjects(t, "ClaudeMux")

	got, ok := findProject([]string{root}, "claudemux")
	if !ok {
		t.Fatal("findProject() ok = false, want true — matching is case-insensitive")
	}
	if want := filepath.Join(root, "ClaudeMux"); got != want {
		t.Errorf("findProject() = %q, want %q", got, want)
	}
}

// Within one tier the shallower candidate wins, so a top-level project is never
// shadowed by a same-named one buried under an org directory.
func TestFindProjectPrefersShallowerOnTie(t *testing.T) {
	root := mkProjects(t, "ameriglide/core", "core")

	got, ok := findProject([]string{root}, "core")
	if !ok {
		t.Fatal("findProject() ok = false, want true")
	}
	if want := filepath.Join(root, "core"); got != want {
		t.Errorf("findProject() = %q, want %q — depth breaks ties before anything else", got, want)
	}
}

// Roots are searched in the order the user listed them, and that order is the
// tie-break after depth: the first root is the one they meant first.
func TestFindProjectPrefersEarlierRootOnTie(t *testing.T) {
	first := mkProjects(t, "core")
	second := mkProjects(t, "core")

	got, ok := findProject([]string{first, second}, "core")
	if !ok {
		t.Fatal("findProject() ok = false, want true")
	}
	if want := filepath.Join(first, "core"); got != want {
		t.Errorf("findProject() = %q, want %q — earlier roots win equal matches", got, want)
	}
}

// Two candidates that tie on tier, depth and root must still resolve the same
// way on every run: a launcher that picks a different project each time you
// type the same query is worse than one that picks the wrong one consistently.
func TestFindProjectBreaksRemainingTiesLexically(t *testing.T) {
	root := mkProjects(t, "muxb", "muxa")

	got, ok := findProject([]string{root}, "mux")
	if !ok {
		t.Fatal("findProject() ok = false, want true")
	}
	if want := filepath.Join(root, "muxa"); got != want {
		t.Errorf("findProject() = %q, want %q — the last tie-break is lexical, for determinism", got, want)
	}
}

// Dotted directories are infrastructure, not projects. This is also what keeps
// .claude/worktrees out: its parent is skipped before the walk reaches it.
func TestFindProjectSkipsHiddenDirs(t *testing.T) {
	root := mkProjects(t, ".cache/core", ".core")

	if got, ok := findProject([]string{root}, "core"); ok {
		t.Errorf("findProject() = %q, want no match — dotted directories are not projects", got)
	}
}

func TestFindProjectIgnoresFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "core"), []byte("not a project"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got, ok := findProject([]string{root}, "core"); ok {
		t.Errorf("findProject() = %q, want no match — a file cannot be launched into", got)
	}
}

func TestFindProjectNoMatch(t *testing.T) {
	root := mkProjects(t, "claudemux")

	if got, ok := findProject([]string{root}, "nothinglikethis"); ok {
		t.Errorf("findProject() = %q, want no match", got)
	}
}

// A root the user has moved or not created yet is not an error: the remaining
// roots must still be searched.
func TestFindProjectSkipsMissingRoot(t *testing.T) {
	real := mkProjects(t, "core")
	missing := filepath.Join(t.TempDir(), "gone")

	got, ok := findProject([]string{missing, real}, "core")
	if !ok {
		t.Fatal("findProject() ok = false, want true — a missing root must not abort the search")
	}
	if want := filepath.Join(real, "core"); got != want {
		t.Errorf("findProject() = %q, want %q", got, want)
	}
}

func TestFindProjectNoRootsConfigured(t *testing.T) {
	if got, ok := findProject(nil, "core"); ok {
		t.Errorf("findProject() = %q, want no match with no roots configured", got)
	}
}

// The exit codes below are a contract with bin/claudemux's resolve_dir, which
// reads them the same way ch_config_get reads `config get`'s.
func TestRunProjectFindPrintsPathExitsZero(t *testing.T) {
	projects := mkProjects(t, "claudemux")
	root := t.TempDir()
	dir := filepath.Join(root, "claudemux")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "launch:\n  project_dirs:\n    - " + projects + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", root)

	var stdout, stderr bytes.Buffer
	code := runProjectFind([]string{"claudemux"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("runProjectFind() = %d, want 0; stderr=%q", code, stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), filepath.Join(projects, "claudemux"); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// Every launch that falls through to this call is a query zoxide already
// missed; an unmatched query is the normal case, not noise to print.
func TestRunProjectFindNoMatchExitsOneSilently(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config.yml: no roots

	var stdout, stderr bytes.Buffer
	code := runProjectFind([]string{"core"}, &stdout, &stderr)

	if code != 1 {
		t.Errorf("runProjectFind() = %d, want 1 for an unmatched query", code)
	}
	if stdout.String() != "" || stderr.String() != "" {
		t.Errorf("stdout = %q, stderr = %q, want both empty", stdout.String(), stderr.String())
	}
}

// Same split as `config get`: a broken config.yml must be distinguishable from
// a query that simply did not match, or a typo'd config silently degrades into
// "no such project" on every launch.
func TestRunProjectFindBrokenConfigExitsThree(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "claudemux")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("launch:\n  nope: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", root)

	var stdout, stderr bytes.Buffer
	code := runProjectFind([]string{"core"}, &stdout, &stderr)

	if code != 3 {
		t.Errorf("runProjectFind() = %d, want 3 for a config.yml that does not parse", code)
	}
	if stderr.String() == "" {
		t.Error("stderr empty, want the parse error — a broken config must surface, not degrade silently")
	}
}

func TestRunProjectFindWrongArgCount(t *testing.T) {
	for _, args := range [][]string{nil, {"a", "b"}} {
		var stdout, stderr bytes.Buffer

		if code := runProjectFind(args, &stdout, &stderr); code != 2 {
			t.Errorf("runProjectFind(%v) = %d, want 2 (usage error)", args, code)
		}
		if stderr.String() == "" {
			t.Errorf("runProjectFind(%v): stderr empty, want a usage message", args)
		}
	}
}
