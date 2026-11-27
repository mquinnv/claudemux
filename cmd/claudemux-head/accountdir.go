package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// sharedClaudeEntries are the names under ~/.claude that a non-default
// account's CLAUDE_CONFIG_DIR symlinks back, so transcripts, settings,
// plugins etc. stay in one place. .claude.json and the keychain login stay
// per account (never linked). Any team-kernel.*.md match is added too, see
// sharedEntriesFor.
//
// Investigation (claude 2.1.332, throwaway CLAUDE_CONFIG_DIR with symlinked
// settings.json, history.jsonl and projects/):
//   - `claude -p hi` (logged out) left all three symlinks intact; it created
//     backups/, plugins/ and sessions/ as real dirs and no settings.local.json.
//   - `claude mcp add --scope user` wrote <dir>/.claude.json (a real per-account
//     file) and did not touch the settings.json link. So MCP servers and the
//     login/onboarding state are per account by design.
//   - There is no `claude config set` subcommand in this version, so a
//     settings.json write could not be triggered headlessly; whether an
//     in-session /config edit replaces the symlink with a real file is
//     UNDETERMINED. history.jsonl: only survival across `-p` was observed.
//   - settings.local.json was not created in the config dir and may hold
//     machine-local permissions, so it is not linked (stays per account).
//
// Safe default: link every entry anyway and rely on `account check` to
// report a link Claude Code later replaced with a real file.
var sharedClaudeEntries = []string{
	"projects", "settings.json", "CLAUDE.md", "rules", "skills", "agents",
	"commands", "plugins", "hooks", "claudemux", "memory.jsonl", "team.json",
	"plans", "specs", "scripts", "file-history", "history.jsonl", "todos",
	"session-env", "shell-snapshots",
}

// dirProblem is one entry of an account dir that is not the expected link.
// Got is missing|real-file|real-dir|wrong-target.
type dirProblem struct{ Entry, Want, Got string }

// sharedEntriesFor is sharedClaudeEntries plus the team-kernel.*.md files
// present in home/.claude, sorted for stable output.
func sharedEntriesFor(home string) []string {
	out := append([]string(nil), sharedClaudeEntries...)
	if m, err := filepath.Glob(filepath.Join(home, ".claude", "team-kernel.*.md")); err == nil {
		var extra []string
		for _, p := range m {
			extra = append(extra, filepath.Base(p))
		}
		sort.Strings(extra)
		out = append(out, extra...)
	}
	return out
}

// existsOrLink reports whether p exists, counting a dangling symlink.
func existsOrLink(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// classifyEntry compares dir/entry with the wanted link to want; "" is ok.
func classifyEntry(dir, entry, want string) string {
	path := filepath.Join(dir, entry)
	fi, err := os.Lstat(path)
	if err != nil {
		return "missing"
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		if got, err := os.Readlink(path); err == nil && filepath.Clean(got) == filepath.Clean(want) {
			return ""
		}
		return "wrong-target"
	}
	if fi.IsDir() {
		return "real-dir"
	}
	return "real-file"
}

// checkAccountDir reports every shared entry that exists in home/.claude but
// is not a link to it inside dir.
func checkAccountDir(home, dir string) ([]dirProblem, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	var probs []dirProblem
	for _, e := range sharedEntriesFor(home) {
		want := filepath.Join(home, ".claude", e)
		if !existsOrLink(want) {
			continue
		}
		if got := classifyEntry(dir, e, want); got != "" {
			probs = append(probs, dirProblem{Entry: e, Want: want, Got: got})
		}
	}
	return probs, nil
}

// setupAccountDir creates dir and any missing links. Idempotent. It never
// overwrites a real file or folder (or a wrong link): those are left for
// checkAccountDir to report. .claude.json is never touched.
func setupAccountDir(home, dir string) (created []string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	for _, e := range sharedEntriesFor(home) {
		want := filepath.Join(home, ".claude", e)
		if !existsOrLink(want) || classifyEntry(dir, e, want) != "missing" {
			continue
		}
		if err := os.Symlink(want, filepath.Join(dir, e)); err != nil {
			return created, err
		}
		created = append(created, e)
	}
	return created, nil
}

// problemHint says how to fix one problem.
func problemHint(dir string, p dirProblem) string {
	path := filepath.Join(dir, p.Entry)
	switch p.Got {
	case "missing":
		return "run: claudemux-head account setup <name>"
	case "wrong-target":
		return fmt.Sprintf("remove it (rm %q), then re-run setup", path)
	default:
		return fmt.Sprintf("move it aside (mv %q %q), then re-run setup", path, path+".bak")
	}
}

// accountWarning is the one-line lobby notice, "" when there are no problems.
func accountWarning(name string, probs []dirProblem) string {
	if len(probs) == 0 {
		return ""
	}
	return fmt.Sprintf("account %s: %d setup problems - run claudemux-head account check", name, len(probs))
}

// accountLoginCommand builds the argv and env for `claude` on an account:
// CLAUDE_CONFIG_DIR set to dir, or removed for the ~/.claude account.
func accountLoginCommand(claudePath, dir string, environ []string) (argv, env []string) {
	for _, kv := range environ {
		if !strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			env = append(env, kv)
		}
	}
	if dir != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+dir)
	}
	return []string{claudePath}, env
}

// execAccountLogin replaces this process with claude on the account, run from
// $TMPDIR so no project's settings or trust prompt interferes.
func execAccountLogin(dir string) error {
	path, err := exec.LookPath("claude")
	if err != nil {
		return err
	}
	if err := os.Chdir(os.TempDir()); err != nil {
		return err
	}
	argv, env := accountLoginCommand(path, dir, os.Environ())
	return syscall.Exec(path, argv, env)
}
