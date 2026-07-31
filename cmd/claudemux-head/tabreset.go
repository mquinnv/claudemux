package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

// projectColorResolver is the shared shell resolver's filename, identical
// wherever it is installed.
const projectColorResolver = "project-color-resolve.sh"

// findShippedScript returns the first dirs entry that contains name. exists is
// injected so the search order is testable without a filesystem. Empty
// directories are skipped: either OS lookup feeding this can fail on its own,
// and joining name onto "" would probe a bogus absolute path.
func findShippedScript(name string, dirs []string, exists func(string) bool) (string, bool) {
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if exists(p) {
			return p, true
		}
	}
	return "", false
}

// shippedScriptPath locates a script that ships with claudemux, trying two
// layouts in order:
//
//  1. Beside this binary — what install.sh produces, all four files siblings.
//  2. Beside the claudemux found on PATH, symlinks resolved — the development
//     layout, where the head binary is a `go build` output in GOBIN while the
//     shell scripts stay in the repo and only claudemux is symlinked onto PATH.
//
// bin/claudemux already survives the second layout because resolve_self follows
// its own symlink back into the repo; this gives the head the same reach.
func shippedScriptPath(name string) (string, bool) {
	var dirs []string

	if p, err := siblingOfExecutable(name); err == nil {
		dirs = append(dirs, filepath.Dir(p))
	}

	if cm, err := exec.LookPath("claudemux"); err == nil {
		if resolved, err := filepath.EvalSymlinks(cm); err == nil {
			dirs = append(dirs, filepath.Dir(resolved))
		}
	}

	return findShippedScript(name, dirs, func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && !st.IsDir()
	})
}
