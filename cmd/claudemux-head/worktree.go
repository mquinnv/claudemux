package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// gitRevParseCalls counts actual `git rev-parse` subprocesses, so tests can
// prove the cache below is doing its job.
var gitRevParseCalls atomic.Int64

// worktreeCache memoizes cwd → worktree name. A directory's worktree identity
// is fixed for the life of that directory, so entries never expire; a cwd that
// moves (the session cd's elsewhere) simply becomes a different key. Negative
// results are cached too — most panes sit in a main worktree and would
// otherwise fork git on every one-second poll.
var worktreeCache sync.Map // string -> string

// worktreeNameForCwd reports the worktree name to show for a live cwd, or ""
// when the cwd is not inside a linked worktree. Claude Code's native layout
// (".claude/worktrees/<name>") is recognized from the path alone; anything else
// — notably plain `git worktree add` siblings like "~/Projects/remix-crm-567",
// which carry no marker in their path — is resolved by asking git.
func worktreeNameForCwd(cwd string) string {
	if cwd == "" {
		return ""
	}
	if name := worktreeNameFromCwd(cwd); name != "" {
		return name
	}
	return gitWorktreeName(cwd)
}

// gitWorktreeName returns the directory name of the linked git worktree
// containing cwd, or "" when cwd is in a repo's main worktree, is not in a repo
// at all, or git can't be run. A linked worktree is identified by its per-
// worktree git dir differing from the repo's common git dir; the name reported
// is the basename of the worktree's own root, which is what the user named it.
// Results are cached (see worktreeCache).
func gitWorktreeName(cwd string) string {
	if cwd == "" {
		return ""
	}
	if v, ok := worktreeCache.Load(cwd); ok {
		return v.(string)
	}
	name := resolveGitWorktreeName(cwd)
	worktreeCache.Store(cwd, name)
	return name
}

// worktreeRoot is one linked worktree of a repo: its absolute root path and the
// short name shown in the chip (the root's basename).
type worktreeRoot struct {
	path string
	name string
}

type worktreeListEntry struct {
	roots []worktreeRoot
	at    time.Time
}

// worktreeListCache memoizes a repo's linked-worktree set, keyed by the query
// cwd. Unlike a directory's own worktree identity (fixed forever, cached in
// worktreeCache), the SET of a repo's worktrees changes as the user adds and
// removes them, so entries expire after worktreeListTTL.
var worktreeListCache sync.Map // string(cwd) -> worktreeListEntry

const worktreeListTTL = 10 * time.Second

// linkedWorktreeRoots returns the linked (non-main) worktrees of the repo
// containing cwd, briefly cached. `git worktree list` enumerates both plain
// `git worktree add` siblings and Claude's native ".claude/worktrees/*" trees,
// so this one call is the single source of truth for what worktree roots exist
// — no path-shape guessing. Empty when cwd isn't in a repo or git fails.
func linkedWorktreeRoots(cwd string, now time.Time) []worktreeRoot {
	if cwd == "" {
		return nil
	}
	if v, ok := worktreeListCache.Load(cwd); ok {
		e := v.(worktreeListEntry)
		if now.Sub(e.at) < worktreeListTTL {
			return e.roots
		}
	}
	roots := resolveLinkedWorktreeRoots(cwd)
	worktreeListCache.Store(cwd, worktreeListEntry{roots: roots, at: now})
	return roots
}

func resolveLinkedWorktreeRoots(cwd string) []worktreeRoot {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	gitRevParseCalls.Add(1)
	out, err := exec.CommandContext(ctx, "git", "-C", cwd,
		"worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil
	}
	// Porcelain lists the main worktree first, then each linked one, each as a
	// block beginning "worktree <abs-path>". Drop the first (main); the rest are
	// the linked trees we can attribute commands to.
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			paths = append(paths, p)
		}
	}
	if len(paths) <= 1 {
		return nil
	}
	roots := make([]worktreeRoot, 0, len(paths)-1)
	for _, p := range paths[1:] {
		roots = append(roots, worktreeRoot{path: p, name: filepath.Base(p)})
	}
	return roots
}

// dominantWorktree attributes each command in the window to at most one linked
// worktree (the longest root path it mentions, so a nested file path isn't
// mis-split) and returns the clear leader, or "" when no worktree stands out.
// "Clear" means: referenced by at least minWorktreeRefs commands and by more
// than twice any rival — a lone stray `git -C other` is ignored, and a genuine
// tie clears rather than flicker.
func dominantWorktree(commands []string, roots []worktreeRoot) string {
	if len(roots) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, c := range commands {
		best := ""
		bestLen := 0
		for _, r := range roots {
			if len(r.path) > bestLen && strings.Contains(c, r.path) {
				best, bestLen = r.name, len(r.path)
			}
		}
		if best != "" {
			counts[best]++
		}
	}
	topName, topCount, secondCount := "", 0, 0
	for name, n := range counts {
		if n > topCount {
			topName, secondCount, topCount = name, topCount, n
		} else if n > secondCount {
			secondCount = n
		}
	}
	if topCount >= minWorktreeRefs && topCount > 2*secondCount {
		return topName
	}
	return ""
}

const minWorktreeRefs = 3

// commandWorktree resolves the worktree a session is driving at arm's length:
// its cwd stays in the main repo while its commands reach into a linked
// worktree by explicit path. It scans the recent command window (main-session
// tool_use inputs) against the repo's known worktree roots. "" when the session
// isn't focused on any one worktree.
func commandWorktree(sessionCwd string, events []Event, now time.Time) string {
	if sessionCwd == "" {
		return ""
	}
	roots := linkedWorktreeRoots(sessionCwd, now)
	if len(roots) == 0 {
		return ""
	}
	return dominantWorktree(recentCommandStrings(events, worktreeCmdWindow), roots)
}

const worktreeCmdWindow = 30

// recentCommandStrings returns up to n most-recent main-session (non-sidechain)
// tool_use inputs, each folded into one string of its string-valued fields, in
// newest-first order. Events without a tool_use are skipped, so the window
// counts commands, not turns.
func recentCommandStrings(events []Event, n int) []string {
	out := make([]string, 0, n)
	for i := len(events) - 1; i >= 0 && len(out) < n; i-- {
		e := events[i]
		if e.IsSidechain || len(e.ToolUses) == 0 {
			continue
		}
		var b strings.Builder
		for _, tu := range e.ToolUses {
			for _, v := range tu.Input {
				if s, ok := v.(string); ok {
					b.WriteString(s)
					b.WriteByte('\n')
				}
			}
		}
		out = append(out, b.String())
	}
	return out
}

func resolveGitWorktreeName(cwd string) string {
	// The poll loop reaches this on a cache miss; a wedged git (a repo on a
	// stalled network mount) must never hang it.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	gitRevParseCalls.Add(1)
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse",
		"--path-format=absolute", "--git-dir", "--git-common-dir", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 {
		return ""
	}
	gitDir, commonDir, toplevel := lines[0], lines[1], lines[2]
	if gitDir == commonDir {
		return "" // main worktree
	}
	return filepath.Base(toplevel)
}
