package main

import (
	"os"
	"path/filepath"
	"testing"
)

// touch writes an empty file, failing the test rather than the caller.
func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The current name wins whenever it exists, so a project that has migrated is
// never read from its leftover .project.yml.
func TestProjectConfigPathPrefersCurrentName(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, projectConfigName))
	touch(t, filepath.Join(dir, legacyProjectConfigName))

	if got, want := projectConfigPath(dir), filepath.Join(dir, projectConfigName); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

// The legacy name still resolves on its own: every .project.yml already on disk
// keeps working without being touched.
func TestProjectConfigPathFallsBackToLegacyName(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, legacyProjectConfigName))

	if got, want := projectConfigPath(dir), filepath.Join(dir, legacyProjectConfigName); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

// With neither file present the current name is returned, not "": the callers
// read the path and treat a missing file as "nothing declared", so handing them
// a path keeps them one-liners.
func TestProjectConfigPathWithNoFileReturnsCurrentName(t *testing.T) {
	dir := t.TempDir()

	if got, want := projectConfigPath(dir), filepath.Join(dir, projectConfigName); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

// The name a reset restores comes from the current config file when a project
// has both — the whole point of the preference order, exercised through the
// caller rather than the helper.
func TestProjectDeclaredNameReadsCurrentNameOverLegacy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, projectConfigName), []byte("name: Current\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, legacyProjectConfigName), []byte("name: Legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := projectDeclaredName(projectConfigPath(dir)); got != "Current" {
		t.Errorf("name = %q, want %q", got, "Current")
	}
}
