package main

import (
	"context"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Handoff breadcrumbs: a session launched by another Claude session names its
// project in the head as the whole chain that led to it — "🐝 beejax › 🚢
// beejax-platform" — and `p` switches to the session one step up. The links
// are the same @claudemux_parent / @claudemux_parent_created pair the lobby
// draws families from (swfamily.go), trusted under the same rule: a link holds
// only while the live session of that name was born when the child said it
// was, so a recycled name never shows up as a stranger's ancestor.

// infoNameOption carries the project's declared `name:` (.claudemux.yml),
// published once at start like the badge, so a child's head can label its
// ancestors the way their own heads label themselves. "" when none is
// declared; the crumb then falls back to the session name.
const infoNameOption = "@claudemux_name"

// lineageMaxDepth bounds the walk up the chain. The created-time match makes a
// cycle unlikely, but the walk also guards against one directly; this is the
// backstop that keeps a pathological option set from costing more than a
// handful of map lookups per poll.
const lineageMaxDepth = 16

// crumbSep joins the breadcrumb. Not " · ": that already separates the state
// line's parts, and the chain must read as one part.
const crumbSep = " › "

// crumb is one session in a handoff chain.
type crumb struct {
	Session string // tmux session name — the switch target
	Emoji   string // published @claudemux_emoji, "" when none
	Name    string // published @claudemux_name, "" when none
}

// label is the crumb as shown: the badge, else the name, else the session name.
// Unlike projectBadge, a crumb without an emoji still shows its name — a link
// in the chain that rendered as nothing would hide where the chain broke.
func (c crumb) label() string {
	name := c.Name
	if name == "" {
		name = c.Session
	}
	if c.Emoji == "" {
		return name
	}
	return c.Emoji + " " + name
}

// lineageFormat is the list-sessions row parseLineage reads.
const lineageFormat = "#{session_name}\t#{session_created}\t#{" + parentOption + "}\t#{" +
	parentCreatedOption + "}\t#{" + infoEmojiOption + "}\t#{" + infoNameOption + "}"

// parseLineage returns self's ancestors in a lineageFormat listing, root first,
// nearest parent last. A broken link — a parent that is gone, or a name now
// held by a session born later — ends the chain there, the same verdict the
// lobby reaches when it draws the child as an orphan.
func parseLineage(listing, self string) []crumb {
	type row struct{ created, parent, parentCreated, emoji, name string }
	rows := map[string]row{}
	for _, line := range strings.Split(listing, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 6 || f[0] == "" {
			continue
		}
		rows[f[0]] = row{strings.TrimSpace(f[1]), f[2], strings.TrimSpace(f[3]), f[4], f[5]}
	}

	var chain []crumb
	seen := map[string]bool{self: true}
	for cur := self; len(chain) < lineageMaxDepth; {
		r, ok := rows[cur]
		if !ok || r.parent == "" || seen[r.parent] {
			break
		}
		p, ok := rows[r.parent]
		// A zero or empty ParentCreated is "never recorded", not a time, so it
		// matches nothing — the rule swArrangeFamilies applies.
		if !ok || r.parentCreated == "" || r.parentCreated == "0" || p.created != r.parentCreated {
			break
		}
		chain = append(chain, crumb{Session: r.parent, Emoji: p.emoji, Name: p.name})
		seen[r.parent] = true
		cur = r.parent
	}
	// Collected nearest first; the breadcrumb reads root first.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// readLineage resolves selfPane's session name and its ancestors. ok is false
// when tmux could not be read, so the caller keeps the chain it already has
// rather than blanking the breadcrumb over one failed poll.
func readLineage(ctx context.Context, selfPane string) (self string, chain []crumb, ok bool) {
	out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "-t", selfPane, "#{session_name}").Output()
	if err != nil {
		return "", nil, false
	}
	self = strings.TrimSpace(string(out))
	if self == "" {
		return "", nil, false
	}
	listing, err := exec.CommandContext(ctx, "tmux", "list-sessions", "-F", lineageFormat).Output()
	if err != nil {
		return "", nil, false
	}
	return self, parseLineage(string(listing), self), true
}

// breadcrumbText is the state line's project part. With no ancestors it is the
// plain badge, unchanged — including nothing at all for a project without an
// emoji. A deep chain keeps its root and the immediate parent and elides the
// middle: the state line's text never shrinks to fit, so the breadcrumb must
// stay bounded on its own.
func breadcrumbText(ancestors []crumb, emoji, name, session string) string {
	own := projectBadge(emoji, name)
	if len(ancestors) == 0 {
		return own
	}
	if own == "" {
		own = crumb{Session: session, Name: name}.label()
	}
	var parts []string
	if len(ancestors) > 2 {
		parts = []string{ancestors[0].label(), "…", ancestors[len(ancestors)-1].label()}
	} else {
		for _, a := range ancestors {
			parts = append(parts, a.label())
		}
	}
	if own != "" {
		parts = append(parts, own)
	}
	return strings.Join(parts, crumbSep)
}

// switchToParentCmd moves the client showing this head to its parent session.
// tmux picks the client the same way it would for a keypress in this pane — the
// one most recently active on this session. Best-effort: a parent that went
// away since the last poll just leaves the user where they are.
func switchToParentCmd(parent string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		defer cancel()
		_ = exec.CommandContext(ctx, "tmux", "switch-client", "-t", swSwitchTarget(parent)).Run()
		return nil
	}
}
