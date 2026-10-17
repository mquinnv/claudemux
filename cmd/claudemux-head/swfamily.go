package main

import (
	"sort"
)

// Handoff families: a session launched by another Claude session carries the
// parent's name and session_created in two tmux options, and the lobby draws
// it indented under that parent. This file is the arrangement — pure data in,
// data out, so the TUI and the web page can both render the result without
// re-deriving the tree.
// Design: docs/superpowers/specs/2026-10-16-lobby-handoff-families-design.md.

const (
	// parentOption names the launching session. It is a name, and tmux names
	// get reused, so it is never trusted alone — see parentCreatedOption.
	parentOption = "@claudemux_parent"
	// parentCreatedOption is the parent's #{session_created} at launch. A link
	// holds only while the live session of that name still has this value; a
	// recycled name was born later, so the child reads as an orphan instead of
	// being adopted by a stranger.
	parentCreatedOption = "@claudemux_parent_created"
)

// Tree glyphs, each exactly 2 cells so a prefix is always 2*Depth cells wide.
const (
	swGlyphTee   = "├ "
	swGlyphElbow = "└ "
	swGlyphBar   = "│ "
	swGlyphBlank = "  "
)

// swArrangeFamilies reorders the fleet so each family is one contiguous block,
// parents above children, and fills in the tree fields (Depth, TreePrefix,
// FamilyGroup, From) the renderers read. The input order is tmux's
// list-sessions order and is the tie-break everywhere a rule leaves a choice,
// so a session joining or leaving a family moves that block and leaves every
// other row where the user last saw it.
//
// The group rule: a family sits in the lowest-numbered group any member
// belongs to (active < closing < deferred). A deferred parent with a live
// child therefore stays up with the work instead of sinking out of sight with
// its children, while a family that is entirely deferred sinks as one block.
// swGroupOf still describes a single session; FamilyGroup is where its row is
// actually drawn, which is what the dividers must follow.
//
// It returns a new slice and leaves the input alone.
func swArrangeFamilies(sessions []swSession) []swSession {
	n := len(sessions)
	byName := make(map[string]int, n)
	for i, s := range sessions {
		byName[s.Name] = i
	}

	// parent[i] is i's parent index, -1 for a root. A link holds only when the
	// parent is live, is not the session itself, and was born when the child
	// said it was (see parentCreatedOption).
	parent := make([]int, n)
	from := make([]string, n)
	for i, s := range sessions {
		parent[i] = -1
		if s.Parent == "" {
			continue
		}
		if p, ok := byName[s.Parent]; ok && p != i && sessions[p].Created == s.ParentCreated {
			parent[i] = p
			continue
		}
		from[i] = s.Parent
	}

	// Break cycles. Matching on created times makes a cycle unlikely but not
	// impossible (hand-set options, a clock tie), and a cycle has no root, so
	// its members would silently vanish from the lobby. Walk each node's
	// ancestors; the node at which the walk would revisit one becomes a root.
	for i := range sessions {
		seen := map[int]bool{i: true}
		for cur := i; parent[cur] >= 0; cur = parent[cur] {
			if seen[parent[cur]] {
				from[cur] = sessions[cur].Parent
				parent[cur] = -1
				break
			}
			seen[parent[cur]] = true
		}
	}

	// Children, ordered by launch time so a family reads in the order it
	// was spawned; the name settles a same-second tie.
	kids := make([][]int, n)
	var roots []int
	for i := range sessions {
		if parent[i] < 0 {
			roots = append(roots, i)
			continue
		}
		kids[parent[i]] = append(kids[parent[i]], i)
	}
	for _, ks := range kids {
		sort.SliceStable(ks, func(a, b int) bool {
			sa, sb := sessions[ks[a]], sessions[ks[b]]
			if sa.Created != sb.Created {
				return sa.Created < sb.Created
			}
			return sa.Name < sb.Name
		})
	}

	// The family group of a root is the lowest group in its subtree.
	var famGroup func(i int) swGroup
	famGroup = func(i int) swGroup {
		g := swGroupOf(sessions[i])
		for _, k := range kids[i] {
			if kg := famGroup(k); kg < g {
				g = kg
			}
		}
		return g
	}
	groups := make([]swGroup, n)
	for _, r := range roots {
		groups[r] = famGroup(r)
	}
	// Stable: roots are already in input (list-sessions) order.
	sort.SliceStable(roots, func(a, b int) bool { return groups[roots[a]] < groups[roots[b]] })

	out := make([]swSession, 0, n)
	// prefix is this row's own glyph string; cont is what its children inherit
	// ahead of their tee or elbow (a bar while this row has later siblings, a
	// blank once it is the last, so no line hangs below a family's final row).
	var emit func(i, depth int, prefix, cont string, g swGroup)
	emit = func(i, depth int, prefix, cont string, g swGroup) {
		s := sessions[i]
		s.Depth = depth
		s.FamilyGroup = g
		s.From = from[i]
		s.TreePrefix = prefix
		out = append(out, s)
		for j, k := range kids[i] {
			glyph, next := swGlyphTee, cont+swGlyphBar
			if j == len(kids[i])-1 {
				glyph, next = swGlyphElbow, cont+swGlyphBlank
			}
			emit(k, depth+1, cont+glyph, next, g)
		}
	}
	for _, r := range roots {
		emit(r, 0, "", "", groups[r])
	}
	return out
}
