package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The lobby's half of reboot restore: find the sessions a reboot killed,
// offer them once, and relaunch the chosen ones through the launcher.
// See docs/superpowers/specs/2026-09-19-reboot-restore-design.md.

// swRestoreOffer is the offer strip's state. nil on swModel means no strip.
type swRestoreOffer struct {
	lost    []lostSession
	cluster []string // every record path in the cluster: lost + excluded + deduped
	cutoff  int64    // names the archive dir
	newest  int64    // shown as "before the reboot (Sep 18 13:52)"

	picking bool
	checked []bool
	cursor  int

	busy   bool
	queue  []lostSession // still to launch, in order
	done   int
	total  int
	failed []string // "name: reason"
}

type swRestoreScanMsg struct{ offer *swRestoreOffer }

type swRestoreStepMsg struct {
	name string
	err  error
}

func (o *swRestoreOffer) startPicking() {
	o.picking = true
	o.cursor = 0
	o.checked = make([]bool, len(o.lost))
	for i := range o.checked {
		o.checked[i] = true
	}
}

func (o *swRestoreOffer) toggle() {
	if o.cursor >= 0 && o.cursor < len(o.checked) {
		o.checked[o.cursor] = !o.checked[o.cursor]
	}
}

func (o *swRestoreOffer) moveCursor(d int) {
	o.cursor += d
	if o.cursor >= len(o.lost) {
		o.cursor = len(o.lost) - 1
	}
	if o.cursor < 0 {
		o.cursor = 0
	}
}

// selected is what Enter restores: the checked rows while picking,
// everything otherwise.
func (o *swRestoreOffer) selected() []lostSession {
	if !o.picking {
		return o.lost
	}
	var out []lostSession
	for i, l := range o.lost {
		if i < len(o.checked) && o.checked[i] {
			out = append(out, l)
		}
	}
	return out
}

// archiveRestoreOffer moves every record in the offer's cluster out of the
// live dir — not just the ones offered: records excluded because their
// session was already live, and duplicates selectLost dropped, are archived
// too, so a later lobby run in the same boot doesn't re-offer them. The
// offer is a one-time decision: unchecked rows in the picker are archived
// as well.
func archiveRestoreOffer(o *swRestoreOffer) {
	_ = archiveSessionRecords(sessionRecordDir(), o.cutoff, o.cluster)
}

// swRestoreScanCmd looks for lost sessions. It runs once per lobby, on the
// first successful snapshot — the live fleet is needed to exclude sessions
// already running again. A nil offer means nothing to restore.
func swRestoreScanCmd(sessions []swSession) tea.Cmd {
	liveNames := make(map[string]bool, len(sessions))
	var panes []string
	for _, s := range sessions {
		liveNames[s.Name] = true
		if s.ClaudePane != "" {
			panes = append(panes, s.ClaudePane)
		}
	}
	return func() tea.Msg {
		dir := sessionRecordDir()
		pruneSessionRecords(dir, time.Now(), sessionRecordMaxAge)
		recs := readSessionRecords(dir)
		if len(recs) == 0 {
			return swRestoreScanMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		bootOut, bootErr := exec.CommandContext(ctx, "sysctl", "-n", "kern.boottime").Output()
		boot, bootOK := int64(0), false
		if bootErr == nil {
			boot, bootOK = parseBootTime(string(bootOut))
		}
		startOut, startErr := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#{start_time}").Output()
		start, startOK := int64(0), false
		if startErr == nil {
			if v, err := strconv.ParseInt(strings.TrimSpace(string(startOut)), 10, 64); err == nil && v > 0 {
				start, startOK = v, true
			}
		}
		cutoff, ok := restoreCutoff(boot, bootOK, start, startOK)
		if !ok {
			return swRestoreScanMsg{}
		}
		liveIDs := map[string]bool{}
		for _, p := range panes {
			if pm, ok := readPaneRecord(paneMapDir(), p); ok {
				liveIDs[pm.SessionID] = true
			}
		}
		lost, cluster, newest := selectLost(recs, cutoff, liveNames, liveIDs)
		if len(lost) == 0 {
			return swRestoreScanMsg{}
		}
		return swRestoreScanMsg{offer: &swRestoreOffer{lost: lost, cluster: cluster, cutoff: cutoff, newest: newest}}
	}
}

// restoreArgs is the launcher argv (after "claudemux") for one session.
// -W: the session already has whatever worktree it had. -C only when the
// recorded cwd still exists and differs from the launch dir.
func restoreArgs(l lostSession, dirExists func(string) bool) ([]string, error) {
	r := l.Rec
	if !resumeIDOK(r.SessionID) || r.SessionID == "" {
		return nil, fmt.Errorf("bad session id %q", r.SessionID)
	}
	if r.LaunchDir == "" || !dirExists(r.LaunchDir) {
		return nil, errors.New("launch dir gone")
	}
	args := []string{"-d", "-W", "-N", r.SessionName, "-r", r.SessionID}
	if r.ClaudeCwd != "" && r.ClaudeCwd != r.LaunchDir && dirExists(r.ClaudeCwd) {
		args = append(args, "-C", r.ClaudeCwd)
	}
	return append(args, "--", r.LaunchDir), nil
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// swRestoreOneCmd relaunches one session. Same timeout and error reporting
// as swCreateCmd: the launcher's last stderr line says why it failed.
func swRestoreOneCmd(l lostSession) tea.Cmd {
	return func() tea.Msg {
		name := l.Rec.SessionName
		args, err := restoreArgs(l, dirExists)
		if err != nil {
			return swRestoreStepMsg{name: name, err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "claudemux", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			if detail := swLastLine(stderr.String()); detail != "" {
				err = errors.New(detail)
			}
			return swRestoreStepMsg{name: name, err: err}
		}
		return swRestoreStepMsg{name: name}
	}
}

// startRestore archives the offer and queues the selected sessions. Returns
// the first launch command, or nil when nothing was selected (the offer is
// then finished).
func (m *swModel) startRestore() tea.Cmd {
	o := m.restore
	sel := o.selected()
	m.restoreArchive(o)
	if len(sel) == 0 {
		m.restore = nil
		return nil
	}
	o.picking = false
	o.busy = true
	o.total = len(sel)
	o.done = 0
	o.queue = sel[1:]
	return swRestoreOneCmd(sel[0])
}

// swRestoreStripText is the one-line strip above the fleet list.
func swRestoreStripText(o *swRestoreOffer) string {
	switch {
	case o.busy:
		return fmt.Sprintf("⏻ restoring %d/%d…", o.done+1, o.total)
	case o.total > 0:
		return fmt.Sprintf("⏻ restored %d/%d · failed: %s · x dismiss",
			o.total-len(o.failed), o.total, strings.Join(o.failed, ", "))
	}
	n := len(o.lost)
	noun := "sessions were"
	if n == 1 {
		noun = "session was"
	}
	when := time.Unix(o.newest, 0).Format("Jan 2 15:04")
	return fmt.Sprintf("⏻ %d %s running before the reboot (%s) · r restore all · s select · x dismiss", n, noun, when)
}

// swRestorePickerLines renders the checklist, one line per lost session.
func swRestorePickerLines(o *swRestoreOffer, now time.Time, width int) []string {
	lines := []string{"restore which sessions?  space toggle · enter restore · esc back"}
	for i, l := range o.lost {
		cur := "  "
		if i == o.cursor {
			cur = "▸ "
		}
		box := "[ ]"
		if i < len(o.checked) && o.checked[i] {
			box = "[x]"
		}
		age := formatDuration(now.Sub(time.Unix(l.Rec.LastSeen, 0)))
		line := fmt.Sprintf("%s%s %s  %s  %s", cur, box, l.Rec.SessionName, age, l.Rec.Topic)
		if l.Interrupted {
			line += "  ⚡ interrupted"
		}
		if width > 0 {
			line = clipLine(line, width)
		}
		lines = append(lines, line)
	}
	return lines
}
