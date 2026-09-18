package main

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"gopkg.in/yaml.v3"
)

// emojiCellW is the display width of every emoji slot in every layout — the
// project badge and the action symbol alike.
//
// Two is the width of a typical emoji, but "typical" is exactly what cannot be
// assumed: a bare dingbat like ⚠ measures one cell, a VS16-qualified ⚠️ measures
// one or two depending on the terminal, and ZWJ sequences vary further. Since
// this codebase builds its columns out of measured cells (swCell, clipLine,
// swTopicW), an emoji whose width is assumed rather than measured shears every
// row beneath it. So nothing renders an emoji directly — everything goes
// through emojiCell, which pads to this width by measuring.
const emojiCellW = 2

// validProjectEmoji reports whether s is usable as a project badge: exactly one
// grapheme cluster that fits the slot.
//
// The grapheme count is what makes "🧵x" and "🧵🎸" invalid while keeping the
// single-grapheme multi-rune cases — ⚠️ (VS16), 👨‍💻 (ZWJ), 🇺🇸 (regional
// indicator pair) — valid. A rune count would reject all three.
//
// A rejected value is treated as "nothing declared" rather than as an error,
// matching how isHex6 guards `color:`: .claudemux.yml is read leniently, and a
// mistyped badge should cost you the badge, not the launch.
func validProjectEmoji(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if uniseg.GraphemeClusterCount(s) != 1 {
		return false
	}
	w := lipgloss.Width(s)
	return w > 0 && w <= emojiCellW
}

// emojiCell renders s as a fixed-width cell of exactly emojiCellW display
// cells: padded when it measures narrow, truncated when it measures wide, and
// blank (never collapsed) when empty. A project that declares no badge must
// still occupy its slot, or the columns of the projects beside it shift.
func emojiCell(s string) string {
	if w := lipgloss.Width(s); w > emojiCellW {
		s = ansi.Truncate(s, emojiCellW, "")
	}
	if n := emojiCellW - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// projectDeclaredEmoji reads the `emoji:` field from a project config file (see
// projectConfigPath for which one), or "" when the file is absent, unparseable,
// declares no badge, or declares one validProjectEmoji rejects.
//
// It is projectDeclaredName's twin and shares its contract: that file is
// gitignored, so worktrees and fresh clones legitimately have none — a missing
// value, never an error.
//
// Unlike `name:` and `color:`, bin/claudemux never reads this field: the badge
// reaches every surface through the head (window name, state line, and the
// @claudemux_emoji option the lobby reads), so there is no second parser to
// keep in step.
func projectDeclaredEmoji(configPath string) string {
	b, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	var meta struct {
		Emoji string `yaml:"emoji"`
	}
	if err := yaml.Unmarshal(b, &meta); err != nil {
		return ""
	}
	e := strings.TrimSpace(meta.Emoji)
	if !validProjectEmoji(e) {
		return ""
	}
	return e
}

// badgedTab puts the project emoji in front of a window label. It returns the
// label unchanged when there is no emoji, and "" when there is no label — a
// window renamed to a bare emoji is worse than one left alone, the same reason
// tabRenameArgs refuses a blank label.
//
// The badge lives in the window name only, never in bin/claudemux's
// set-titles-string prefix: the titlebar is "<project> · #W", so a badge in
// both would print twice. Here it reaches the tmux window list and the
// titlebar from one place.
func badgedTab(emoji, tab string) string {
	if tab == "" || emoji == "" {
		return tab
	}
	return emoji + " " + tab
}

// projectBadge is the head state line's project part: the emoji, then the
// declared name when there is one. No emoji, no badge — the name on its own is
// not what this part is for, and the head already sits inside its project.
func projectBadge(emoji, name string) string {
	if emoji == "" {
		return ""
	}
	if name == "" {
		return emoji
	}
	return emoji + " " + name
}

// stateEmoji returns the action symbol for kind, already padded to a cell.
// It replaces the colored dot stateDot used to return: an emoji carries its own
// color, so the tint the dot existed to provide is now intrinsic to the glyph.
//
// Shared by renderStateLine, renderStatusbar and the lobby's marker column, so
// no two surfaces can disagree about what an action looks like — the property
// stateDot already had.
//
// Every StateKind has a case. There is a default only because Go requires the
// function to return; a kind added without a case here is caught by
// TestStateEmojiCoversEveryKindDistinctly, not by this fallback.
func stateEmoji(kind StateKind) string {
	switch kind {
	case StateIdle:
		// Blocked on the human — the "come look" green the idle dot carried.
		return emojiCell("🟢")
	case StateThinking:
		return emojiCell("🧠")
	case StateTool:
		return emojiCell("🔧")
	case StateAwaiting:
		return emojiCell("⚠️")
	case StateError:
		return emojiCell("❌")
	case StateCompacting:
		return emojiCell("🗜️")
	case StateBackground:
		// The turn ended but launched work is still running: machinery turning,
		// not a human waiting.
		return emojiCell("⚙️")
	case StateAsking:
		// A question is on screen. Distinct from Idle: both are blocked on the
		// human, but this one names what it wants.
		return emojiCell("🙋")
	case StateWaiting:
		// Claude is booting — something is happening, but not for you yet.
		return emojiCell("⏳")
	case StateUnsure:
		// Not confidently idle. The question mark withholds exactly the claim
		// the green circle would make.
		return emojiCell("❓")
	}
	return emojiCell("❔")
}
