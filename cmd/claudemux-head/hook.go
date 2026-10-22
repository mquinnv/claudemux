package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// hookEvent is one Claude Code event a hook registers on, with an optional
// matcher restricting which tool calls trigger it. An empty matcher means
// "every invocation of this event" — the only mode the map and worktree hooks
// need, since neither is tool-specific.
type hookEvent struct {
	event   string
	matcher string
}

// claudemuxHook is one `claudemux-head hook <name>` subcommand (hookrun.go)
// and the Claude Code events it registers on. legacyScript is the bash script
// it replaced, whose entries hook ensure migrates away from.
type claudemuxHook struct {
	name         string
	legacyScript string
	events       []hookEvent
}

// claudemuxHooks are every hook claudemux registers.
//
// map records which session lives in which tmux pane. SessionStart records the
// mapping when a pane first opens; UserPromptSubmit keeps it current across
// /clear, resume, and compaction, which rotate the transcript file underneath
// a live session. Registering only one leaves the map stale in exactly the
// cases users notice.
//
// worktree asks the model to create a task-named worktree. It is
// UserPromptSubmit ONLY: at SessionStart there is no prompt yet, so there is
// nothing to name a worktree after — which is the entire problem it exists to
// fix.
//
// ask tracks pending AskUserQuestion calls. It needs all three events:
// PreToolUse to set the marker, PostToolUse to clear it on answer, and
// UserPromptSubmit to clear it when a question was Esc'd (no PostToolUse ever
// fires for those). PreToolUse and PostToolUse carry a matcher: without one,
// Claude Code runs this hook on EVERY tool call of EVERY session on the
// machine, blocking each PreToolUse call on a process spawn just to discover
// the tool isn't AskUserQuestion. UserPromptSubmit has no matcher — matchers
// only apply to tool-call events. PermissionRequest takes a tool-name matcher
// too but deliberately has none: it fires only when a permission dialog is
// about to open, which is rare and is the event itself, whatever the tool.
var claudemuxHooks = []claudemuxHook{
	{name: "map", legacyScript: "claudemux-map.sh", events: []hookEvent{{event: "SessionStart"}, {event: "UserPromptSubmit"}}},
	{name: "worktree", legacyScript: "claudemux-worktree.sh", events: []hookEvent{{event: "UserPromptSubmit"}}},
	{name: "ask", legacyScript: "claudemux-ask.sh", events: []hookEvent{
		{event: "PreToolUse", matcher: "AskUserQuestion"},
		{event: "PostToolUse", matcher: "AskUserQuestion"},
		{event: "PermissionRequest"},
		{event: "UserPromptSubmit"},
	}},
}

// hookBinaryName is the copy of this binary in ~/.claude/hooks/ that every
// hook command and the statusLine run. A copy rather than the claudemux-head
// on PATH: a Homebrew libexec path baked into settings.json would break on the
// next upgrade, and a `go install` output moves with GOBIN.
const hookBinaryName = "claudemux-head"

// legacyStatuslineName is what that copy used to be called, when the
// statusline was the only thing that ran it.
const legacyStatuslineName = "claudemux-statusline"

// abtopStatuslineName is the shim we are replacing, matched by basename so the
// user's install prefix does not matter.
const abtopStatuslineName = "abtop-statusline.sh"

// hookCommandFor is the settings.json command for hook h, run from bin.
// Claude Code runs it through a shell, so bin is quoted: a HOME with a space
// in it would otherwise break every hook.
func hookCommandFor(bin string, h claudemuxHook) string {
	return shQuote(bin) + " hook " + h.name
}

// unquotedHookCommandFor is what hookCommandFor registered before it quoted
// bin. Those entries are ours, and are migrated like the bash scripts'.
func unquotedHookCommandFor(bin string, h claudemuxHook) string {
	return bin + " hook " + h.name
}

// shQuote single-quotes s for a POSIX shell; a quote inside it is closed,
// escaped and reopened.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

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
//
// The shipped skills (skills.go) are installed last, and only when the hooks
// got that far: a skill that cannot be written is reported as 4 but never
// costs the hook registration, which matters far more. A legacy shim that
// cannot be written (see ensureHooks) is reported as 4 too, but only after
// the skills, since the registration it follows has already landed.
func runHookEnsure(args []string, stdout, stderr io.Writer) int {
	rc, shimErrs := ensureHooks(args, stdout, stderr)
	if rc != 0 {
		return rc
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "claudemux: resolving home dir: %v\n", err)
		return 4
	}
	if err := installSkills(filepath.Join(home, ".claude")); err != nil {
		fmt.Fprintf(stderr, "claudemux: installing skills: %v\n", err)
		rc = 4
	}
	if len(shimErrs) > 0 {
		fmt.Fprintf(stderr, "claudemux: hooks are registered in settings.json, but Claude Code sessions "+
			"started before this upgrade still run the old hook files, and these could not be redirected:\n")
		for _, err := range shimErrs {
			fmt.Fprintf(stderr, "  %v\n", err)
		}
		rc = 4
	}
	return rc
}

// ensureHooks installs this binary into ~/.claude/hooks and registers its hook
// commands and its statusline in settings.json: runHookEnsure minus the
// skills, with the same exit codes. Legacy shims that could not be written do
// not change the exit code here; they come back as shimErrs for runHookEnsure
// to report once everything else is done.
//
// It also migrates installs from before the hooks were Go: entries naming the
// old claudemux-*.sh scripts are removed (the Go commands are added in their
// place), and the old statusline command is taken over. The old files in
// ~/.claude/hooks are not deleted but rewritten as one-line shims into this
// binary, because Claude Code snapshots hook commands when a session starts:
// every session already running when this upgrade lands keeps invoking the
// old paths until it restarts, and a deleted script would fail every one of
// its prompts.
func ensureHooks(args []string, stdout, stderr io.Writer) (rc int, shimErrs []error) {
	fs := flag.NewFlagSet("hook ensure", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "claudemux: resolving home dir: %v\n", err)
		return 4, nil
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
			return 3, nil
		}
	case errors.Is(err, os.ErrNotExist):
		existing = nil
	default:
		fmt.Fprintf(stderr, "claudemux: reading %s: %v\n", settingsPath, err)
		return 4, nil
	}

	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "claudemux: creating %s: %v\n", hooksDir, err)
		return 4, nil
	}

	selfExe, err := os.Executable()
	if err == nil {
		selfExe, err = filepath.EvalSymlinks(selfExe)
	}
	if err != nil {
		fmt.Fprintf(stderr, "claudemux: locating this binary: %v\n", err)
		return 4, nil
	}
	bin := filepath.Join(hooksDir, hookBinaryName)
	if err := copyExecutableIfChanged(selfExe, bin); err != nil {
		fmt.Fprintf(stderr, "claudemux: installing %s: %v\n", bin, err)
		return 4, nil
	}

	changed := false
	for _, h := range claudemuxHooks {
		unquoted := unquotedHookCommandFor(bin, h)
		if removeHookEntries(settings, func(c string) bool {
			return filepath.Base(c) == h.legacyScript || c == unquoted
		}) {
			changed = true
		}
		if addHookEntries(settings, hookCommandFor(bin, h), h.events) {
			changed = true
		}
	}
	legacyStatusline := filepath.Join(hooksDir, legacyStatuslineName) + " statusline"
	if setStatusLine(settings, shQuote(bin)+" statusline", stderr, legacyStatusline, bin+" statusline") {
		changed = true
	}

	if changed {
		// A dotfiles user's settings.json is a symlink; write its target, so
		// the rename below replaces the real file and leaves the link alone.
		target, err := settingsWriteTarget(settingsPath)
		if err != nil {
			fmt.Fprintf(stderr, "claudemux: %s is a symlink that cannot be followed: %v; leaving it alone\n", settingsPath, err)
			return 4, nil
		}
		// The file's own mode, for the write and the backup alike: a user who
		// locked it down (its env block can hold tokens) keeps it that way.
		mode := os.FileMode(0o644)
		if info, err := os.Stat(target); err == nil {
			mode = info.Mode().Perm()
		}
		backup := ""
		if existing != nil {
			backup = settingsPath + ".bak-" + strconv.FormatInt(time.Now().Unix(), 10)
			if err := os.WriteFile(backup, existing, mode); err != nil {
				fmt.Fprintf(stderr, "claudemux: writing backup %s: %v\n", backup, err)
				return 4, nil
			}
		}
		out, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "claudemux: encoding settings: %v\n", err)
			return 4, nil
		}
		out = append(out, '\n')
		// Atomically, so a crash or a full disk mid-write cannot leave Claude
		// Code a truncated settings.json.
		if err := writeAtomic(target, out, mode); err != nil {
			if backup != "" {
				fmt.Fprintf(stderr, "claudemux: writing %s: %v (the previous version is in %s)\n", settingsPath, err, backup)
			} else {
				fmt.Fprintf(stderr, "claudemux: writing %s: %v\n", settingsPath, err)
			}
			return 4, nil
		}
		fmt.Fprintf(stdout, "claudemux: registered claudemux hooks in %s\n", settingsPath)
	}

	// After settings.json, so a shim that cannot be written never costs the
	// registration itself.
	return 0, shimLegacyHookFiles(hooksDir, bin)
}

// settingsWriteTarget follows path's symlinks to the file a write should
// replace. Unlike filepath.EvalSymlinks it accepts a dangling final link, as
// on a fresh machine whose dotfiles target does not exist yet, returning the
// target to create, but only when that target's directory exists.
func settingsWriteTarget(path string) (string, error) {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	p := path
	for range 40 {
		info, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			if p == path {
				return p, nil
			}
			if dir, err := os.Stat(filepath.Dir(p)); err != nil || !dir.IsDir() {
				return "", fmt.Errorf("its target %s has no directory to be created in", p)
			}
			return p, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return p, nil
		}
		link, err := os.Readlink(p)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(link) {
			// From the directory the link really lives in, as the kernel
			// resolves it: a textual Join would strip ".." across a
			// symlinked parent such as ~/.claude itself.
			dir, err := filepath.EvalSymlinks(filepath.Dir(p))
			if err != nil {
				return "", err
			}
			link = filepath.Join(dir, link)
		}
		p = link
	}
	return "", errors.New("too many levels of symbolic links")
}

// legacyShim is what an old hook file in ~/.claude/hooks becomes: a pass-
// through into the Go command that replaced it, stdin, stdout and exit code
// included. bin is quoted for the shell with shQuote.
func legacyShim(bin string, args string) []byte {
	quoted := shQuote(bin)
	return []byte("#!/bin/sh\n" +
		"# Left by claudemux hook ensure for Claude Code sessions started before the\n" +
		"# hooks became claudemux-head subcommands. Safe to delete once they exit.\n" +
		"exec " + quoted + " " + args + "\n")
}

// shimLegacyHookFiles rewrites each pre-Go hook file still present in
// hooksDir as a legacyShim. A file that is absent stays absent — this exists
// for sessions that are running the old commands, and a fresh install has
// none — and one that already holds the shim is not rewritten. One file that
// cannot be rewritten does not stop the rest; every failure is returned, in
// file-name order.
func shimLegacyHookFiles(hooksDir, bin string) []error {
	targets := map[string]string{legacyStatuslineName: `"$@"`}
	for _, h := range claudemuxHooks {
		targets[h.legacyScript] = "hook " + h.name
	}
	names := make([]string, 0, len(targets))
	for name := range targets {
		names = append(names, name)
	}
	sort.Strings(names)
	var errs []error
	for _, name := range names {
		args := targets[name]
		path := filepath.Join(hooksDir, name)
		cur, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		want := legacyShim(bin, args)
		if err == nil && bytes.Equal(cur, want) {
			continue
		}
		if err := writeExecutableAtomic(path, want); err != nil {
			errs = append(errs, fmt.Errorf("replacing %s with a shim: %w", path, err))
		}
	}
	return errs
}

// removeHookEntries deletes, from every event, each hook whose command match
// accepts — for the bash scripts, by basename, so whatever directory or
// interpreter prefix it was registered with — and reports whether anything
// changed. A group left with no hooks is dropped, and an event left with no
// groups is deleted, so no empty husk is left behind. Other hooks sharing a
// group with the one removed stay where they are.
func removeHookEntries(settings map[string]any, match func(command string) bool) bool {
	hooks, _ := settings["hooks"].(map[string]any)
	changed := false
	for event, v := range hooks {
		groups, ok := v.([]any)
		if !ok {
			continue
		}
		keptGroups := groups[:0:0]
		for _, g := range groups {
			gm, ok := g.(map[string]any)
			inner, innerOK := gm["hooks"].([]any)
			if !ok || !innerOK {
				keptGroups = append(keptGroups, g)
				continue
			}
			kept := inner[:0:0]
			for _, h := range inner {
				hm, _ := h.(map[string]any)
				if c, _ := hm["command"].(string); c != "" && match(c) {
					changed = true
					continue
				}
				kept = append(kept, h)
			}
			if len(kept) == len(inner) {
				keptGroups = append(keptGroups, g)
				continue
			}
			if len(kept) > 0 {
				gm["hooks"] = kept
				keptGroups = append(keptGroups, gm)
			}
		}
		if len(keptGroups) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = keptGroups
		}
	}
	return changed
}

// addHookEntries adds our command to any events that lack it, mutating
// settings in place. Reports whether anything changed.
//
// It walks the generic map rather than a typed struct so that every key we do
// not model — the user's permissions, model, statusLine, other tools' hooks —
// round-trips untouched.
func addHookEntries(settings map[string]any, command string, events []hookEvent) bool {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	changed := false
	for _, he := range events {
		groups, _ := hooks[he.event].([]any)
		if hasHookCommand(groups, command) {
			continue
		}
		group := map[string]any{
			"hooks": []any{
				map[string]any{"type": "command", "command": command},
			},
		}
		// Omit the key entirely rather than writing "matcher": "" — Claude
		// Code's schema treats an empty string differently from an absent
		// key in some versions, and an absent key is what every hook this
		// tool shipped before the ask hook has always produced.
		if he.matcher != "" {
			group["matcher"] = he.matcher
		}
		// Append; never replace. Another tool's hook on this event must survive.
		groups = append(groups, group)
		hooks[he.event] = groups
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

// writeExecutableAtomic installs blob at path as an executable via a temp file
// in the same directory and a rename, the way writeJSONAtomic installs the
// rate-limit cache.
//
// Rename rather than overwrite because these files are being EXECUTED while we
// replace them. Opening a running executable for writing fails with ETXTBSY,
// and the installed copy of this whole ~16MB binary is what Claude Code runs
// on every statusline render and every hook of every session on the machine —
// so an
// in-place rewrite is a coin flip that hook ensure loses at exactly the moment
// the user is using Claude Code most. Losing it used to cost more than the
// copy: ensureHooks returns 4 on that error, before settings.json is
// written, so a single ETXTBSY silently dropped that launch's entire hook
// registration. A rename needs write permission on the
// directory, not on the file, cannot be observed half-done by a reader, and
// leaves any process already executing the old inode running it untouched.
func writeExecutableAtomic(path string, blob []byte) error {
	return writeAtomic(path, blob, 0o755)
}

// copyExecutableIfChanged copies src to dst through writeExecutableAtomic,
// skipping the write when dst already holds byte-identical content. The
// installed copy is this whole binary, and hook ensure runs on every launch
// of bin/claudemux, so an unconditional copy would rewrite a multi-megabyte
// file on every single launch. Size is compared first via
// os.Stat since it is cheap and rules out almost every real change (a new
// build); content is compared only when sizes match. Any error reading dst
// (most commonly: it does not exist yet) is treated as "not identical" so
// the copy proceeds — this must never change observable behavior, only skip
// redundant writes. The skip removes MOST of the exposure to a concurrent
// statusline render or hook, and the rename removes the rest.
func copyExecutableIfChanged(src, dst string) error {
	srcBytes, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if dstInfo, err := os.Stat(dst); err == nil && dstInfo.Size() == int64(len(srcBytes)) {
		if dstBytes, err := os.ReadFile(dst); err == nil && bytes.Equal(srcBytes, dstBytes) {
			return nil
		}
	}
	return writeExecutableAtomic(dst, srcBytes)
}

// setStatusLine points settings["statusLine"] at our command, mutating settings
// in place. Reports whether anything changed.
//
// Unlike hooks.<event>, which is an append-only list every tool can add to,
// statusLine is a single exclusive string: claiming it takes it away from
// whoever had it. So the claim is narrow — an empty slot, abtop's shim (what
// this feature exists to remove), or a slot we already own, which includes
// each legacy command: the one from before the installed copy was renamed
// from claudemux-statusline, and the one from before its path was quoted.
// Taking a slot over sets type and command on the existing object, so any
// other key the user set there, such as padding, survives. Anything else is a
// statusline the user chose, and we decline and say so rather than clobbering
// it; the meters simply stay dark, which is recoverable, whereas a silently
// destroyed statusline is not.
func setStatusLine(settings map[string]any, command string, stderr io.Writer, legacy ...string) bool {
	existing, _ := settings["statusLine"].(map[string]any)
	if existing == nil {
		settings["statusLine"] = map[string]any{"type": "command", "command": command}
		return true
	}
	cur, _ := existing["command"].(string)
	switch {
	case cur == command:
		return false // already ours
	case cur != "" && !slices.Contains(legacy, cur) && filepath.Base(cur) != abtopStatuslineName:
		fmt.Fprintf(stderr, "claudemux: leaving your statusLine (%s) alone; "+
			"account meters need claudemux's own statusline command, so they will stay unavailable\n", cur)
		return false
	}
	existing["type"] = "command"
	existing["command"] = command
	return true
}
