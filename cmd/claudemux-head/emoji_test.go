package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// A project badge is one grapheme that fits the two-cell slot every layout
// reserves for it. Everything else is "nothing declared", the same leniency
// `color:` gets from isHex6 — a junk value renders plain rather than failing a
// launch.
func TestValidProjectEmoji(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"plain emoji", "🧵", true},
		// A variation selector makes a text character into an emoji, and the
		// width that produces is where tmux (2 cells) and iTerm2 (1) disagree
		// — the disagreement that made lobby rows jump a column.
		{"dingbat with variation selector", "⚠️", false},
		{"emoji-able symbol with variation selector", "🛠️", false},
		{"bare dingbat", "⚠", true},
		{"zwj sequence is one grapheme", "👨‍💻", true},
		{"flag is one grapheme", "🇺🇸", true},
		{"ascii letter", "x", true},
		{"empty", "", false},
		{"two graphemes", "🧵🎸", false},
		{"emoji plus letter", "🧵x", false},
		{"whitespace only", "  ", false},
		{"cjk pair overruns the slot", "日本", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validProjectEmoji(tt.in); got != tt.want {
				t.Errorf("validProjectEmoji(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// Every emoji reaching a layout is padded to exactly emojiCellW display cells.
// Width is what the column grid is built from, so a one-cell dingbat and a
// two-cell emoji must measure identically — otherwise a single odd badge
// shears every row under it.
func TestEmojiCellIsAlwaysTheSameWidth(t *testing.T) {
	for _, in := range []string{"", "🧵", "⚠️", "⚠", "👨‍💻", "🇺🇸", "x", "🟢"} {
		if got := lipgloss.Width(emojiCell(in)); got != emojiCellW {
			t.Errorf("width(emojiCell(%q)) = %d, want %d", in, got, emojiCellW)
		}
	}
}

// An empty badge still occupies its slot: a project that declares no emoji must
// not shift the columns of the projects beside it in the lobby.
func TestEmojiCellEmptyIsBlank(t *testing.T) {
	if got, want := emojiCell(""), strings.Repeat(" ", emojiCellW); got != want {
		t.Errorf("emojiCell(\"\") = %q, want %q", got, want)
	}
}

// Every StateKind has its own emoji. A missing case would silently fall through
// to the default and report the wrong action, so this walks the whole set and
// insists the symbols are distinct.
func TestStateEmojiCoversEveryKindDistinctly(t *testing.T) {
	kinds := []StateKind{
		StateIdle, StateThinking, StateTool, StateAwaiting, StateError,
		StateCompacting, StateBackground, StateAsking, StateWaiting, StateUnsure,
	}
	seen := map[string]StateKind{}
	for _, k := range kinds {
		got := stateEmoji(k)
		if got == "" {
			t.Errorf("stateEmoji(%v) is empty", k)
			continue
		}
		if lipgloss.Width(got) != emojiCellW {
			t.Errorf("stateEmoji(%v) = %q, width %d, want %d", k, got, lipgloss.Width(got), emojiCellW)
		}
		if strings.ContainsRune(got, '️') {
			t.Errorf("stateEmoji(%v) = %q needs a variation selector; terminals disagree on its width", k, got)
		}
		if prev, dup := seen[got]; dup {
			t.Errorf("stateEmoji(%v) = %q, already used by %v", k, got, prev)
		}
		seen[got] = k
	}
}

// The badge leads the window name. It never stands alone: renaming a window to
// a bare emoji is worse than leaving it, the same reason tabRenameArgs refuses
// a blank label.
func TestBadgedTab(t *testing.T) {
	tests := []struct{ emoji, tab, want string }{
		{"🧵", "crm bundling", "🧵 crm bundling"},
		{"", "crm bundling", "crm bundling"},
		{"🧵", "", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		if got := badgedTab(tt.emoji, tt.tab); got != tt.want {
			t.Errorf("badgedTab(%q, %q) = %q, want %q", tt.emoji, tt.tab, got, tt.want)
		}
	}
}

// The live summary path and the model's own badge agree: the label the head
// hands tmux carries the project emoji in front.
func TestModelTabTextCarriesBadge(t *testing.T) {
	m := model{projectEmoji: "🧵"}
	if got := m.tabText(Summary{Tab: "crm bundling"}); got != "🧵 crm bundling" {
		t.Errorf("tabText = %q, want %q", got, "🧵 crm bundling")
	}
	m.projectEmoji = ""
	if got := m.tabText(Summary{Tab: "crm bundling"}); got != "crm bundling" {
		t.Errorf("tabText without a badge = %q, want %q", got, "crm bundling")
	}
}

// The head's state line names its project by badge: the emoji, then the
// declared name when there is one. A project without an emoji gets no badge
// part at all — the name alone would be new clutter nobody asked for.
func TestProjectBadge(t *testing.T) {
	tests := []struct{ emoji, name, want string }{
		{"🧵", "claudemux", "🧵 claudemux"},
		{"🧵", "", "🧵"},
		{"", "claudemux", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		if got := projectBadge(tt.emoji, tt.name); got != tt.want {
			t.Errorf("projectBadge(%q, %q) = %q, want %q", tt.emoji, tt.name, got, tt.want)
		}
	}
}

func TestStateLineShowsProjectBadge(t *testing.T) {
	now := time.Now()
	m := model{ready: true, width: 100, state: State{Kind: StateIdle, Since: now},
		projectEmoji: "🧵", projectName: "claudemux"}
	if got := renderStateLine(m, now); !strings.Contains(got, "🧵 claudemux") {
		t.Errorf("renderStateLine = %q, want the project badge", got)
	}
	m.projectEmoji = ""
	if got := renderStateLine(m, now); strings.Contains(got, "claudemux") {
		t.Errorf("renderStateLine = %q, want no badge without an emoji", got)
	}
}

// The badge is read from the same simple key: value file as `name:` and
// `color:`, with the same tolerance for trailing whitespace.
func TestProjectDeclaredEmoji(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, projectConfigName)
	if err := os.WriteFile(p, []byte("color: red\nname: Remix\nemoji: 🧵 \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := projectDeclaredEmoji(p); got != "🧵" {
		t.Errorf("emoji = %q, want %q", got, "🧵")
	}
}

// A missing file, a missing key, an unparseable file, or a value that cannot
// fit the slot all mean the same thing: no badge. A bad value must not cost the
// launch — the same deal isHex6 gives a mistyped `color:`.
func TestProjectDeclaredEmojiMissingOrInvalid(t *testing.T) {
	if got := projectDeclaredEmoji(filepath.Join(t.TempDir(), projectConfigName)); got != "" {
		t.Errorf("emoji = %q, want empty for a missing file", got)
	}
	for _, body := range []string{
		"color: red\n",
		"emoji: \n",
		"emoji: 🧵🎸\n",
		"emoji: nope\n",
		"color: red\n  bad indent: [\n",
	} {
		p := filepath.Join(t.TempDir(), projectConfigName)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := projectDeclaredEmoji(p); got != "" {
			t.Errorf("projectDeclaredEmoji(%q) = %q, want empty", body, got)
		}
	}
}
