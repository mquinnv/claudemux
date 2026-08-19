package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Match tiers, best first. A query only ever competes within one tier: any
// exact hit beats every prefix hit, and any prefix hit beats every substring
// hit, no matter where in the roots they sit. That ordering is what keeps a
// short query like "core" from being answered with "coreutils-fork" while a
// directory literally named "core" exists.
const (
	matchExact = iota
	matchPrefix
	matchSubstring
	matchNone
)

// projectMatchDepth is how far below a root a project may sit. 1 is
// <root>/<project>; 2 is <root>/<org>/<project>, the layout people end up with
// once they group repos by GitHub org. 3 would be inside a project — its
// source directories, and .claude/worktrees — which are not launch targets.
const projectMatchDepth = 2

// findProject returns the best project directory under roots for query, or
// ok=false when nothing matches.
//
// This is the last step of bin/claudemux's resolve_dir, reached only when the
// query is neither a real directory nor a zoxide hit. It must therefore be
// decisive: it either names one directory to launch into, or says nothing at
// all. Ambiguity is resolved rather than reported — see projectCandidate.less
// for the ordering, which is total, so the same query always resolves to the
// same project.
func findProject(roots []string, query string) (string, bool) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", false
	}
	q := strings.ToLower(query)

	var best *projectCandidate
	for i, root := range roots {
		walkProjectRoot(root, 1, func(path, name string, depth int) {
			tier := matchTier(strings.ToLower(name), q)
			if tier == matchNone {
				return
			}
			c := projectCandidate{path: path, tier: tier, depth: depth, root: i}
			if best == nil || c.less(*best) {
				best = &c
			}
		})
	}
	if best == nil {
		return "", false
	}
	return best.path, true
}

// walkProjectRoot calls visit for every non-hidden directory from depth down to
// projectMatchDepth below dir.
//
// A root that cannot be read — moved, unmounted, never created — is silently
// skipped rather than failing the search: the other roots are still worth
// looking in, and a launcher is not the place to report a stale config line.
func walkProjectRoot(dir string, depth int, visit func(path, name string, depth int)) {
	if depth > projectMatchDepth {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		// Dotted directories are infrastructure, never projects. Skipping them
		// here rather than only at match time also prunes the descent, which is
		// what keeps .claude/worktrees out of a depth-2 walk.
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !e.IsDir() {
			// A symlinked project directory reports as a link, not a dir; ask
			// the filesystem what it points at rather than dropping it.
			if info, serr := os.Stat(filepath.Join(dir, name)); serr != nil || !info.IsDir() {
				continue
			}
		}
		path := filepath.Join(dir, name)
		visit(path, name, depth)
		walkProjectRoot(path, depth+1, visit)
	}
}

// matchTier scores an already-lowercased directory name against an
// already-lowercased query.
func matchTier(name, query string) int {
	switch {
	case name == query:
		return matchExact
	case strings.HasPrefix(name, query):
		return matchPrefix
	case strings.Contains(name, query):
		return matchSubstring
	}
	return matchNone
}

// projectCandidate is one directory that matched, with everything the ordering
// needs: root is the index of the root it was found under, so the order the
// user wrote their roots in survives into the tie-break.
type projectCandidate struct {
	path  string
	tier  int
	depth int
	root  int
}

// less reports whether c is the better answer than other. The comparisons run
// in priority order and end on the path, which is unique — so the ordering is
// total and the winner never depends on the order directories happened to be
// read in.
func (c projectCandidate) less(other projectCandidate) bool {
	switch {
	case c.tier != other.tier:
		return c.tier < other.tier
	case c.depth != other.depth:
		// A project sitting directly under a root beats a same-named one buried
		// under an org directory: the shallower path is the more prominent one.
		return c.depth < other.depth
	case c.root != other.root:
		return c.root < other.root
	default:
		return c.path < other.path
	}
}

// runProjectFind implements `claudemux-head project find <query>` and returns
// the process exit code.
//
// The codes mirror `config get`'s, because bin/claudemux reads them the same
// way:
//
//	0 — found; the absolute directory on stdout
//	1 — no match; NOTHING on stdout or stderr. Every call that gets here is a
//	    query zoxide already missed, so "no project either" is the ordinary
//	    outcome and the launcher prints its own message about it.
//	2 — usage error (wrong argument count)
//	3 — config.yml exists but does not parse. Kept distinct from 1 so a broken
//	    config surfaces instead of looking like an unmatched query.
func runProjectFind(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: claudemux-head project find <query>")
		return 2
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head: %v\n", err)
		return 3
	}

	dir, ok := findProject(cfg.Launch.ProjectDirs, args[0])
	if !ok {
		return 1
	}
	fmt.Fprintln(stdout, dir)
	return 0
}
