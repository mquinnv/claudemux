package main

import (
	"context"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Defer publication: a session-scoped tmux user option, mirroring the shape
// of statepub.go's options rather than conductpub.go's global heartbeat — the
// mark belongs to one session, not the fleet, and it must survive both head
// and lobby restarts by living on the tmux session itself. Value "1" or
// "wait" means deferred; unset (or anything else) means normal. There is
// deliberately no heartbeat/staleness handling like conductOption's: a defer
// mark has no process keeping it alive, so it stays exactly as long as it
// was set.
const deferOption = "@claudemux_defer"

// The mark's two set values. deferOnValue is the user's own defer (`d`).
// deferWaitValue is the claudemux-handoff skill's: the agent handed work to a
// peer session and has nothing to do until it reports, so both surfaces say
// WAIT rather than DEFER. Nothing else differs — a waiting session is
// deferred to the conductor, the grouping and restore alike.
const (
	deferOnValue   = "1"
	deferWaitValue = "wait"
)

// isDeferValue reports whether a raw @claudemux_defer value marks the session
// deferred, by the user or by a handoff.
func isDeferValue(raw string) bool {
	return raw == deferOnValue || raw == deferWaitValue
}

// deferReasonOption holds the blocker the user typed when deferring — what
// the session is waiting on. It lives beside deferOption on the same session
// and only means anything while deferOption is "1"; clearing the defer unsets
// both, so a stale reason can never resurface on a later defer.
const deferReasonOption = "@claudemux_defer_reason"

// deferReasonMaxCells caps the blocker text in the head's chip. The chip sits
// ahead of the worktree chip on a line clipLine truncates from the right, so
// an essay-length blocker would otherwise push everything after it off-screen.
const deferReasonMaxCells = 48

// The link options say which session a defer waits on, so the head can clear
// the defer when that session stops or closes. They live beside deferOption on
// the same tmux session for the same reason it does: they must survive head
// and lobby restarts. deferOnOption is the blocker's session name,
// deferOnCreatedOption its tmux session_created stamp (so a reused name is not
// mistaken for the original), and deferOnSeenOption "1" once the blocker has
// been seen working since the link was made (so a blocker that has not
// started yet does not read as "stopped"; "gone" does not need it).
const (
	deferOnOption        = "@claudemux_defer_on"
	deferOnCreatedOption = "@claudemux_defer_on_created"
	deferOnSeenOption    = "@claudemux_defer_on_seen"
)

// deferLink names the session a defer waits on. The zero value is "no link".
type deferLink struct{ On, Created string }

// set reports whether the link names a blocker precisely enough to track: a
// name alone, or a zero stamp, cannot tell the blocker from a later session
// that reuses its name.
func (l deferLink) set() bool { return l.On != "" && l.Created != "" && l.Created != "0" }

// deferMark is everything published about one session's defer, read in one
// tmux call so a poll never pairs one defer's link with another's mark.
type deferMark struct {
	Raw, Reason string
	Link        deferLink
	Seen        bool
}

// unsetLinkArgs is the ";"-chained unset of all three link options. Every
// clear and every link-less set appends it, so a stale link can never outlive
// the defer it belonged to.
func unsetLinkArgs(target string) []string {
	return []string{
		";", "set-option", "-t", target, "-u", deferOnOption,
		";", "set-option", "-t", target, "-u", deferOnCreatedOption,
		";", "set-option", "-t", target, "-u", deferOnSeenOption,
	}
}

// deferArgs builds the tmux argv toggling target's mark: set to "1" (plus the
// blocker, when one was given) when on, everything unset (-u) when off. target
// is a pane id (as statepub uses) or a session name (as the lobby's row toggle
// uses) — tmux resolves either the same way -t always does. All options go in
// one ";"-chained tmux invocation, so a poll never sees the mark from one
// defer paired with the reason or link from another.
func deferArgs(target string, on bool, reason string) []string {
	return deferValueArgs(target, on, deferOnValue, reason)
}

// deferValueArgs is deferArgs with the set value given, so restore can put a
// handoff's deferWaitValue back as it was instead of turning it into the
// user's own defer. It never carries a link, so it unsets any old one.
func deferValueArgs(target string, on bool, value, reason string) []string {
	if !on {
		args := []string{"set-option", "-t", target, "-u", deferOption,
			";", "set-option", "-t", target, "-u", deferReasonOption}
		return append(args, unsetLinkArgs(target)...)
	}
	return append(deferMarkArgs(target, value, reason), unsetLinkArgs(target)...)
}

// deferMarkArgs sets the mark and the reason (or unsets a stale reason),
// without touching the link options.
func deferMarkArgs(target, value, reason string) []string {
	args := []string{"set-option", "-t", target, deferOption, value, ";"}
	return append(args, deferReasonArgs(target, reason)...)
}

// deferReasonArgs sets the reason alone, or unsets it when nothing is left
// after sanitizing. Editing a deferred session's blocker to free text uses it
// directly: rewriting the mark there would turn a handoff's "wait" into "1",
// and a link-less set would unlink a defer whose blocker merely mentions its
// session ("waiting on phenix-2: <goal>").
func deferReasonArgs(target, reason string) []string {
	if reason = sanitizeDeferReason(reason); reason != "" {
		// tmux reads any argument ending in ";" as a command separator, even
		// from argv, and would drop it; "\;" is its escape for a literal one.
		if strings.HasSuffix(reason, ";") {
			reason = strings.TrimSuffix(reason, ";") + `\;`
		}
		return []string{"set-option", "-t", target, deferReasonOption, reason}
	}
	return []string{"set-option", "-t", target, "-u", deferReasonOption}
}

// deferLinkedArgs sets a defer that waits on link's session. A new link starts
// unseen, so the seen flag is unset in the same invocation. An empty link is a
// plain defer, which keeps callers from needing two code paths.
func deferLinkedArgs(target, value, reason string, link deferLink) []string {
	if !link.set() {
		return deferValueArgs(target, true, value, reason)
	}
	return append(deferMarkArgs(target, value, reason), ";",
		"set-option", "-t", target, deferOnOption, link.On, ";",
		"set-option", "-t", target, deferOnCreatedOption, link.Created, ";",
		"set-option", "-t", target, "-u", deferOnSeenOption)
}

// deferSeenArgs records that the blocker has been seen working, which is what
// lets a later idle blocker read as "stopped" rather than not yet started
// (judgeBlocker; a gone blocker ignores it). It is its own invocation: the
// mark and link are already published and must not be rewritten by a poll.
func deferSeenArgs(target string) []string {
	return []string{"set-option", "-t", target, deferOnSeenOption, "1"}
}

// deferSeenCmd fires deferSeenArgs with the usual hard deadline. Nothing
// waits on it: if it fails, the next poll still reads the blocker unseen and
// asks again.
func deferSeenCmd(target string) tea.Cmd {
	return deferWriteCmd(deferSeenArgs(target))
}

// deferTmuxRun runs one defer write with the usual hard deadline, so a wedged
// tmux can never block either surface's Update loop. A variable so tests can
// see the argv a prompt's cmd writes without a tmux server.
var deferTmuxRun = func(args []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "tmux", args...).Run()
}

// deferWriteCmd fires one defer write, fire-and-forget: nothing reads its
// result, because the next poll reads back whatever landed.
func deferWriteCmd(args []string) tea.Cmd {
	return func() tea.Msg {
		deferTmuxRun(args)
		return nil
	}
}

// sanitizeDeferReason flattens typed text to one trimmed line with no control
// characters. Tabs and newlines matter beyond looks: the lobby reads the
// reason back as a field of a tab-separated list-sessions line.
func sanitizeDeferReason(reason string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, reason))
}

// setDeferCmd fires the toggle, fire-and-forget with the usual hard deadline
// so a wedged tmux can never block either surface's Update loop.
func setDeferCmd(target string, on bool, reason string) tea.Cmd {
	return deferWriteCmd(deferArgs(target, on, reason))
}

// setDeferLinkedCmd is setDeferCmd for a defer that waits on a session, with
// the same hard deadline so a wedged tmux can never block an Update loop.
func setDeferLinkedCmd(target, value, reason string, link deferLink) tea.Cmd {
	return deferWriteCmd(deferLinkedArgs(target, value, reason, link))
}

// setDeferReasonCmd rewrites only a deferred session's blocker text, leaving
// its mark, link and seen flag as they are (see deferReasonArgs).
func setDeferReasonCmd(target, reason string) tea.Cmd {
	return deferWriteCmd(deferReasonArgs(target, reason))
}

// readDeferMark fetches the whole mark for the head's poll in one tmux call.
// ok is false when the read failed: a failed read must stay distinguishable
// from an unset mark, because the link logic may never treat it as evidence.
func readDeferMark(ctx context.Context, target string) (deferMark, bool) {
	out, err := exec.CommandContext(ctx, "tmux", "display-message", "-p", "-t", target, deferMarkFormat()).Output()
	if err != nil {
		return deferMark{}, false
	}
	return parseDeferMark(string(out)), true
}

// deferMarkFormat is the display-message format parseDeferMark reads. One
// definition, so the head's poll and `claudemux-head defer` can never disagree
// on the field order.
func deferMarkFormat() string {
	return "#{" + deferOption + "}\t#{" + deferReasonOption + "}\t#{" + deferOnOption + "}\t#{" +
		deferOnCreatedOption + "}\t#{" + deferOnSeenOption + "}"
}

// parseDeferMark splits readDeferMark's tab-separated line. An unset option
// expands to an empty field, so missing trailing fields are the zero value.
func parseDeferMark(out string) deferMark {
	f := strings.Split(strings.TrimRight(out, "\n"), "\t")
	for len(f) < 5 {
		f = append(f, "")
	}
	for i := range f {
		f[i] = strings.TrimSpace(f[i])
	}
	return deferMark{Raw: f[0], Reason: f[1], Link: deferLink{On: f[2], Created: f[3]}, Seen: f[4] == "1"}
}

// swDeferStyle and swBadgeDeferStyle share one color across both surfaces —
// ANSI 256 "45" (cyan) — deliberately NOT a dim/faint style: a deferred
// session is one that must not be forgotten, so it stays as visually loud as
// a waiting one, just a different hue. Not "39": that's swBusyStyle's
// "Thinking" blue already, and defer needs a hue of its own so a deferred row
// can't be mistaken for a busy one at a glance.
var (
	swDeferStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("45"))
	swBadgeDeferStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("232")).Background(lipgloss.Color("45"))
)

// swDeferBadgeText is the lobby row's DEFER badge (WAIT for a handoff's
// mark), leading separator space included — the exact text the row loop
// appends after the model column. Extracted so swTopicW's reserve calculation
// (switchboardtui.go) and the per-row render measure and emit the identical
// string; if they drifted, a too-small reserve would let clipLine truncate
// the badge again. The reserve measures DEFER, the wider of the two.
func swDeferBadgeText(wait bool) string {
	if wait {
		return " " + swBadgeDeferStyle.Render(" WAIT ")
	}
	return " " + swBadgeDeferStyle.Render(" DEFER ")
}

// deferChip renders the head's defer chip from the raw option value: visible
// only when the mark is actually set, blank otherwise (no lobby-liveness
// gating here, unlike conductChip — the mark means something with or without
// a lobby watching). A recorded blocker rides along after the label, capped
// at deferReasonMaxCells. A handoff's mark says "wait" where the user's says
// "defer".
func deferChip(raw, reason string) string {
	if !isDeferValue(raw) {
		return ""
	}
	label := "◆ defer"
	if raw == deferWaitValue {
		label = "◆ wait"
	}
	if reason = sanitizeDeferReason(reason); reason != "" {
		return swDeferStyle.Render(label + ": " + ansi.Truncate(reason, deferReasonMaxCells, "…"))
	}
	return swDeferStyle.Render(label)
}

// deferPromptText is the text of the blocker prompt both surfaces show while
// `d` is collecting one: the typed input with a cursor after it. linked says
// enter would link the defer to the session the input names, which is worth
// showing because only an exact name links and the difference is otherwise
// invisible until the blocker goes away and nothing happens.
func deferPromptText(input string, linked bool) string {
	text := "◆ blocker: " + input + "▌"
	if linked {
		text += " ↳ links"
	}
	return text
}

// deferInputKey applies one keypress to a blocker being typed, the same
// literal-text rules as the lobby's new-session prompt: printable runes and
// space are typed, backspace deletes a rune, everything else is ignored.
func deferInputKey(input string, msg tea.KeyMsg) string {
	switch {
	case msg.Type == tea.KeyBackspace:
		if r := []rune(input); len(r) > 0 {
			return string(r[:len(r)-1])
		}
	case msg.Type == tea.KeyRunes && !msg.Alt:
		return input + string(msg.Runes)
	case msg.Type == tea.KeySpace:
		return input + " "
	}
	return input
}
