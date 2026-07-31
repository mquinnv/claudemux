package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
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

// projectDeclaredName reads the `name:` field from a .project.yml, or "" when
// the file is absent, unparseable, or declares no name. A .project.yml is
// gitignored, so worktrees and fresh clones legitimately have none — this is a
// missing value, never an error.
//
// bin/claudemux reads the same field with sed. The two agree on the simple
// key: value files this project uses; yaml.v3 is the stricter of the pair.
func projectDeclaredName(projectYMLPath string) string {
	b, err := os.ReadFile(projectYMLPath)
	if err != nil {
		return ""
	}
	var meta struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal(b, &meta); err != nil {
		return ""
	}
	return strings.TrimSpace(meta.Name)
}

// restoreName picks the window name a reset restores.
//
// The declared name cannot be used bare: `claudemux -n` clones a session as
// <dir>-2, <dir>-3, ... while the work directory — and so .project.yml, and so
// both the declared name and the project color — stays the same. Four sessions
// on one project would all restore to one name on four identically colored
// tabs. When the session name carries a clone suffix, the suffix rides along
// onto the declared name ("Remix 2"). With no declared name the session name is
// already unique and is used unchanged.
func restoreName(declaredName, sessionName, workDir string) string {
	if declaredName == "" {
		return sessionName
	}
	base := filepath.Base(workDir)
	if sessionName == base {
		return declaredName
	}
	if suffix := strings.TrimPrefix(sessionName, base+"-"); suffix != sessionName && suffix != "" {
		return declaredName + " " + suffix
	}
	// The session was renamed to something unrelated to its directory; there is
	// no suffix to derive, and inventing one would be a guess.
	return sessionName
}

// parseProjectStyle splits resolve_project_style's "<hex> <fg>" line. Anything
// that is not exactly a 6-digit hex plus a foreground is rejected, so a
// resolver that errors or a shell that prints a warning cannot reach tmux.
func parseProjectStyle(out string) (hex, fg string, ok bool) {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) != 2 {
		return "", "", false
	}
	if !isHex6(fields[0]) || fields[1] == "" {
		return "", "", false
	}
	return fields[0], fields[1], true
}

func isHex6(s string) bool {
	if len(s) != 6 {
		return false
	}
	_, err := strconv.ParseUint(s, 16, 32)
	return err == nil
}

// tabResetTmuxArgs builds the tmux command lines a reset runs, in order. Each
// group is omitted when its inputs are missing rather than emitting a partial
// command: outside tmux there is no pane to rename, and a project without a
// color still gets its title back.
func tabResetTmuxArgs(pane, session, name, hex, fg string) [][]string {
	var cmds [][]string
	if pane != "" && name != "" {
		cmds = append(cmds, []string{"rename-window", "-t", pane, name})
	}
	if session != "" && hex != "" && fg != "" {
		cmds = append(cmds,
			[]string{"set", "-t", session, "status-style", "bg=#" + hex + ",fg=" + fg},
			// pane-active-border-style is a window option; sessions here are
			// single-window.
			[]string{"set", "-w", "-t", session, "pane-active-border-style", "fg=#" + hex},
		)
	}
	return cmds
}

// itermTabColorBytes renders the iTerm2 tab-color escape sequence for hex —
// byte-for-byte what bin/claudemux:apply_tab_color writes at attach. nil for
// anything that is not a 6-digit hex, so a bad value writes nothing at all.
func itermTabColorBytes(hex string) []byte {
	if !isHex6(hex) {
		return nil
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return nil
	}
	return []byte(fmt.Sprintf(
		"\033]6;1;bg;red;brightness;%d\a\033]6;1;bg;green;brightness;%d\a\033]6;1;bg;blue;brightness;%d\a",
		(v>>16)&0xff, (v>>8)&0xff, v&0xff))
}
