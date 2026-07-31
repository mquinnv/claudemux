package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// resolverPathForTest locates the repo's project-color-resolve.sh from the
// package directory. Tests run with the package dir as cwd.
func resolverPathForTest(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "bin", "project-color-resolve.sh"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("resolver not found at %s: %v", p, err)
	}
	return p
}

// runResolveStyle invokes resolve_project_style the same way tabreset.go does:
// sourced, with arguments passed positionally so nothing reaches a shell parser.
func runResolveStyle(t *testing.T, dir string) (string, error) {
	t.Helper()
	out, err := exec.Command("bash", "-c",
		`. "$1"; resolve_project_style "$2"`,
		"_", resolverPathForTest(t), dir).Output()
	return strings.TrimSpace(string(out)), err
}

// A dark project color must pair with a white foreground.
func TestResolveProjectStyleDarkColorPicksWhite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".project.yml"), []byte("color: purple\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := runResolveStyle(t, dir)
	if err != nil {
		t.Fatalf("resolve_project_style: %v", err)
	}
	if got != "b34dff #ffffff" {
		t.Errorf("style = %q, want %q", got, "b34dff #ffffff")
	}
}

// A light project color must pair with a black foreground. yellow is ffd24d,
// luminance 199, comfortably over the 150 threshold.
func TestResolveProjectStyleLightColorPicksBlack(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".project.yml"), []byte("color: yellow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := runResolveStyle(t, dir)
	if err != nil {
		t.Fatalf("resolve_project_style: %v", err)
	}
	if got != "ffd24d #000000" {
		t.Errorf("style = %q, want %q", got, "ffd24d #000000")
	}
}

// No project color anywhere up the tree: non-zero exit, no output. t.TempDir()
// is under /var/folders, which has no .project.yml ancestors.
func TestResolveProjectStyleNoColor(t *testing.T) {
	got, err := runResolveStyle(t, t.TempDir())
	if err == nil {
		t.Error("err = nil, want non-zero exit when no color is configured")
	}
	if got != "" {
		t.Errorf("output = %q, want empty", got)
	}
}

// The installed layout: everything beside the binary. First directory wins.
func TestFindShippedScriptPrefersBinaryDir(t *testing.T) {
	exists := func(p string) bool { return p == "/opt/claudemux/project-color-resolve.sh" }
	got, ok := findShippedScript("project-color-resolve.sh",
		[]string{"/opt/claudemux", "/repo/bin"}, exists)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got != "/opt/claudemux/project-color-resolve.sh" {
		t.Errorf("path = %q, want the binary-dir copy", got)
	}
}

// The development layout: the script is not beside the binary, only beside the
// claudemux symlink's target. Without this fallback the reset loses its colors
// on the machine of the person who asked for the feature.
func TestFindShippedScriptFallsBackToClaudemuxDir(t *testing.T) {
	exists := func(p string) bool { return p == "/repo/bin/project-color-resolve.sh" }
	got, ok := findShippedScript("project-color-resolve.sh",
		[]string{"/Users/x/go/bin", "/repo/bin"}, exists)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got != "/repo/bin/project-color-resolve.sh" {
		t.Errorf("path = %q, want the repo copy", got)
	}
}

// Neither route finds it: the caller treats this as "no project color".
func TestFindShippedScriptMissing(t *testing.T) {
	exists := func(string) bool { return false }
	if _, ok := findShippedScript("project-color-resolve.sh",
		[]string{"/a", "/b"}, exists); ok {
		t.Error("ok = true with no copy anywhere, want false")
	}
}

// An empty candidate directory is skipped, not joined into a bare "/name".
// os.Executable and exec.LookPath can each fail independently.
func TestFindShippedScriptSkipsEmptyDirs(t *testing.T) {
	var probed []string
	exists := func(p string) bool { probed = append(probed, p); return false }
	findShippedScript("s.sh", []string{"", "/b"}, exists)
	for _, p := range probed {
		if p == "/s.sh" {
			t.Error("probed /s.sh, want empty dirs skipped")
		}
	}
}

// siblingOfExecutable resolves a name against the directory holding the test
// binary itself, which is what gives hookScriptSource its meaning.
func TestSiblingOfExecutable(t *testing.T) {
	got, err := siblingOfExecutable("some-script.sh")
	if err != nil {
		t.Fatalf("siblingOfExecutable: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Skipf("EvalSymlinks unavailable: %v", err)
	}
	if want := filepath.Join(filepath.Dir(resolved), "some-script.sh"); got != want {
		t.Errorf("siblingOfExecutable = %q, want %q", got, want)
	}
	if filepath.Base(got) != "some-script.sh" {
		t.Errorf("basename = %q, want the requested name", filepath.Base(got))
	}
}
