package main

import (
	"os"
	"path/filepath"
)

// The per-project config file claudemux reads from a project's root.
//
// projectConfigName is the current, namespaced name. legacyProjectConfigName is
// what it was called before: an un-namespaced `.project.yml` claimed the generic
// name in the root of every repo claudemux was launched in. Both are read,
// current first, so the rename needs no migration step — a project keeps working
// on its old file until someone renames it.
//
// bin/project-color-resolve.sh applies the same preference while walking
// ancestors for a color. Keep the two in step.
const (
	projectConfigName       = ".claudemux.yml"
	legacyProjectConfigName = ".project.yml"
)

// projectConfigPath returns the project config file to read from dir: the
// current name when it exists, else the legacy one when THAT exists, else the
// current name again.
//
// The no-file case returns a path rather than "" because every caller reads the
// result and treats a missing file as "nothing declared" — projectDeclaredName
// already does exactly that — so there is no second code path to write.
func projectConfigPath(dir string) string {
	legacy := filepath.Join(dir, legacyProjectConfigName)
	current := filepath.Join(dir, projectConfigName)

	if st, err := os.Stat(current); err == nil && !st.IsDir() {
		return current
	}
	if st, err := os.Stat(legacy); err == nil && !st.IsDir() {
		return legacy
	}
	return current
}
