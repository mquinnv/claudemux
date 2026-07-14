package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// hookScriptName is the hook's filename, identical wherever it is installed.
const hookScriptName = "claudemux-map.sh"

// hookEvents are the two Claude Code events the pane map depends on.
// SessionStart records the mapping when a pane first opens; UserPromptSubmit
// keeps it current across /clear, resume, and compaction, which rotate the
// transcript file underneath a live session. Registering only one leaves the
// map stale in exactly the cases users notice.
var hookEvents = []string{"SessionStart", "UserPromptSubmit"}

// hookScriptSource finds the claudemux-map.sh that shipped with this binary:
// every install channel lays the binary and the scripts down as siblings.
// Symlinks are resolved first because Homebrew puts the real files in libexec
// and symlinks only the binaries onto PATH.
func hookScriptSource() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(resolved), hookScriptName), nil
}

// runHookEnsure implements `claudemux-head hook ensure`.
//
// Exit codes are a contract with install.sh and with bin/claudemux, which calls
// this on every launch:
//
//	0 — already present (no write), or installed
//	2 — usage error
//	3 — settings.json exists but does not parse; NOTHING is written
//	4 — I/O failure
func runHookEnsure(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hook ensure", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scriptFlag := fs.String("script", "", "path to claudemux-map.sh (defaults to the copy beside this binary)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	src := *scriptFlag
	if src == "" {
		var err error
		if src, err = hookScriptSource(); err != nil {
			fmt.Fprintf(stderr, "claudemux: locating %s: %v\n", hookScriptName, err)
			return 4
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "claudemux: resolving home dir: %v\n", err)
		return 4
	}
	claudeDir := filepath.Join(home, ".claude")
	hooksDir := filepath.Join(claudeDir, "hooks")
	settingsPath := filepath.Join(claudeDir, "settings.json")

	// The registered command points HERE, not at wherever the package was
	// installed: a Homebrew libexec path would be baked into settings.json and
	// break on the next upgrade.
	dst := filepath.Join(hooksDir, hookScriptName)

	// Read settings BEFORE copying anything, so a malformed file leaves the
	// whole operation a no-op rather than a half-done one.
	settings := map[string]any{}
	existing, err := os.ReadFile(settingsPath)
	switch {
	case err == nil:
		if err := json.Unmarshal(existing, &settings); err != nil {
			fmt.Fprintf(stderr, "claudemux: %s does not parse (%v); refusing to touch it\n", settingsPath, err)
			return 3
		}
	case errors.Is(err, os.ErrNotExist):
		existing = nil
	default:
		fmt.Fprintf(stderr, "claudemux: reading %s: %v\n", settingsPath, err)
		return 4
	}

	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "claudemux: creating %s: %v\n", hooksDir, err)
		return 4
	}
	if err := copyExecutable(src, dst); err != nil {
		fmt.Fprintf(stderr, "claudemux: installing hook script: %v\n", err)
		return 4
	}

	if !addHookEntries(settings, dst) {
		return 0 // already registered on both events: no write, stay silent
	}

	if existing != nil {
		backup := settingsPath + ".bak-" + strconv.FormatInt(time.Now().Unix(), 10)
		if err := os.WriteFile(backup, existing, 0o644); err != nil {
			fmt.Fprintf(stderr, "claudemux: writing backup %s: %v\n", backup, err)
			return 4
		}
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "claudemux: encoding settings: %v\n", err)
		return 4
	}
	out = append(out, '\n')
	if err := os.WriteFile(settingsPath, out, 0o644); err != nil {
		fmt.Fprintf(stderr, "claudemux: writing %s: %v\n", settingsPath, err)
		return 4
	}

	fmt.Fprintf(stdout, "claudemux: registered the pane-map hook in %s\n", settingsPath)
	return 0
}

// addHookEntries adds our command to any hookEvents that lack it, mutating
// settings in place. Reports whether anything changed.
//
// It walks the generic map rather than a typed struct so that every key we do
// not model — the user's permissions, model, statusLine, other tools' hooks —
// round-trips untouched.
func addHookEntries(settings map[string]any, command string) bool {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	changed := false
	for _, event := range hookEvents {
		groups, _ := hooks[event].([]any)
		if hasHookCommand(groups, command) {
			continue
		}
		// Append; never replace. Another tool's hook on this event must survive.
		groups = append(groups, map[string]any{
			"hooks": []any{
				map[string]any{"type": "command", "command": command},
			},
		})
		hooks[event] = groups
		changed = true
	}

	if changed {
		settings["hooks"] = hooks
	}
	return changed
}

func hasHookCommand(groups []any, command string) bool {
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		inner, _ := gm["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if c, _ := hm["command"].(string); c == command {
				return true
			}
		}
	}
	return false
}

func copyExecutable(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o755)
}
