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

// hookScript is one script this tool installs and the Claude Code events it
// registers on.
type hookScript struct {
	name   string
	events []string
}

// hookScripts are every script claudemux installs.
//
// claudemux-map.sh records which session lives in which tmux pane. SessionStart
// records the mapping when a pane first opens; UserPromptSubmit keeps it
// current across /clear, resume, and compaction, which rotate the transcript
// file underneath a live session. Registering only one leaves the map stale in
// exactly the cases users notice.
//
// claudemux-worktree.sh asks the model to create a task-named worktree. It is
// UserPromptSubmit ONLY: at SessionStart there is no prompt yet, so there is
// nothing to name a worktree after — which is the entire problem it exists to
// fix.
var hookScripts = []hookScript{
	{name: "claudemux-map.sh", events: []string{"SessionStart", "UserPromptSubmit"}},
	{name: "claudemux-worktree.sh", events: []string{"UserPromptSubmit"}},
}

// hookScriptName is the pane-map script's filename, which `--script` overrides.
const hookScriptName = "claudemux-map.sh"

// siblingOfExecutable joins name onto the directory holding this binary, with
// symlinks resolved first because Homebrew puts the real files in libexec and
// symlinks only the binaries onto PATH.
func siblingOfExecutable(name string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(resolved), name), nil
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
	scriptFlag := fs.String("script", "", "path to claudemux-map.sh; its directory is also where every other shipped script (including claudemux-worktree.sh) is resolved from, so pointing this at a lone claudemux-map.sh silently no-ops the rest (defaults to the copy beside this binary)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// srcFor returns where to read a shipped script from. --script overrides
	// only claudemux-map.sh, so existing callers (and tests) that point it at a
	// stub keep working while the other scripts resolve as siblings.
	//
	// Unqualified lookups go through shippedScriptPath, which tries BOTH install
	// layouts — beside this binary (what install.sh and the Homebrew formula
	// produce) and beside the claudemux on PATH with symlinks resolved (the
	// development layout, where the head is a `go install` output in GOBIN while
	// the scripts stay in the repo). This used to try only the first, which was
	// survivable while a missing script merely skipped one hook; it stopped being
	// survivable when the validate-all-before-copy-any pass below made any single
	// miss fatal. A dev checkout then resolved claudemux-worktree.sh into GOBIN,
	// found nothing, and registered NOTHING — losing the pane-map hook too.
	srcFor := func(name string) (string, error) {
		if name == hookScriptName && *scriptFlag != "" {
			return *scriptFlag, nil
		}
		if *scriptFlag != "" {
			return filepath.Join(filepath.Dir(*scriptFlag), name), nil
		}
		if p, ok := shippedScriptPath(name); ok {
			return p, nil
		}
		return "", fmt.Errorf("not found beside %s or beside the claudemux on PATH", filepath.Base(os.Args[0]))
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "claudemux: resolving home dir: %v\n", err)
		return 4
	}
	claudeDir := filepath.Join(home, ".claude")
	hooksDir := filepath.Join(claudeDir, "hooks")
	settingsPath := filepath.Join(claudeDir, "settings.json")

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

	// Resolve and validate every source BEFORE copying any of them. Without
	// this pass, a later script failing to resolve (e.g. a shipped script
	// missing from the install prefix) would leave an earlier one already
	// copied to hooksDir while settings.json stays untouched — a half-done
	// install the old single-script code could not produce, since it had
	// nothing to be "half" of.
	srcs := make([]string, len(hookScripts))
	for i, hs := range hookScripts {
		src, err := srcFor(hs.name)
		if err != nil {
			fmt.Fprintf(stderr, "claudemux: locating %s: %v\n", hs.name, err)
			return 4
		}
		if _, err := os.Stat(src); err != nil {
			fmt.Fprintf(stderr, "claudemux: locating %s: %v\n", hs.name, err)
			return 4
		}
		srcs[i] = src
	}

	changed := false
	for i, hs := range hookScripts {
		// The registered command points HERE, not at wherever the package was
		// installed: a Homebrew libexec path would be baked into settings.json
		// and break on the next upgrade.
		dst := filepath.Join(hooksDir, hs.name)
		if err := copyExecutable(srcs[i], dst); err != nil {
			fmt.Fprintf(stderr, "claudemux: installing %s: %v\n", hs.name, err)
			return 4
		}
		if addHookEntries(settings, dst, hs.events) {
			changed = true
		}
	}
	if !changed {
		return 0 // every script registered on every event: no write, stay silent
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

	fmt.Fprintf(stdout, "claudemux: registered claudemux hooks in %s\n", settingsPath)
	return 0
}

// addHookEntries adds our command to any events that lack it, mutating
// settings in place. Reports whether anything changed.
//
// It walks the generic map rather than a typed struct so that every key we do
// not model — the user's permissions, model, statusLine, other tools' hooks —
// round-trips untouched.
func addHookEntries(settings map[string]any, command string, events []string) bool {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	changed := false
	for _, event := range events {
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
