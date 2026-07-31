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
