package main

import (
	"reflect"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// tmux's #{pane_start_command} is the pane's argv re-quoted by tmux's own
// stringifier, so the launcher's `exec <cmd>` comes back double-quoted with
// backslashes and quotes escaped. The fallback has to undo exactly that.
func TestClaudeCommandFromStart(t *testing.T) {
	tests := []struct {
		name, start, want string
	}{
		{
			name:  "launcher command, double-quoted by tmux",
			start: `"exec env CLAUDEMUX_WORKTREE_PENDING=1 /opt/homebrew/bin/claude --permission-mode auto /color\\ purple"`,
			want:  `env CLAUDEMUX_WORKTREE_PENDING=1 /opt/homebrew/bin/claude --permission-mode auto /color\ purple`,
		},
		{
			name:  "boot holder still in place: the command after -- is what runs",
			start: `"exec /Users/x/go/bin/claudemux-head boot --project foo --label 'Unlocking 1Password environment' --expected 25s -- /opt/homebrew/bin/claude --permission-mode auto -n foo"`,
			want:  `/opt/homebrew/bin/claude --permission-mode auto -n foo`,
		},
		{
			name:  "escaped double quote inside",
			start: `"exec claude -n \"my project\""`,
			want:  `claude -n "my project"`,
		},
		{
			name:  "bare, unquoted",
			start: `exec claude`,
			want:  `claude`,
		},
		{
			name:  "no exec prefix",
			start: `claude --permission-mode auto`,
			want:  `claude --permission-mode auto`,
		},
		{
			name:  "single-quoted",
			start: `'exec claude -n x'`,
			want:  `claude -n x`,
		},
		{name: "empty", start: "", want: ""},
		{name: "whitespace only", start: "  \n", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := claudeCommandFromStart(tt.start); got != tt.want {
				t.Errorf("claudeCommandFromStart(%q) = %q, want %q", tt.start, got, tt.want)
			}
		})
	}
}

func TestRespawnArgs(t *testing.T) {
	got, ok := respawnArgs("%3", "env A=1 claude --permission-mode auto", "")
	if !ok {
		t.Fatal("respawnArgs refused a valid fresh restart")
	}
	want := []string{"respawn-pane", "-k", "-t", "%3", "exec env A=1 claude --permission-mode auto"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("respawnArgs() = %v, want %v", got, want)
	}

	got, ok = respawnArgs("%3", "claude --permission-mode auto", "0f6c1b1e-4c3a-4c9b-9e1a-1234567890ab")
	if !ok {
		t.Fatal("respawnArgs refused a valid resume")
	}
	want = []string{"respawn-pane", "-k", "-t", "%3",
		"exec claude --permission-mode auto --resume 0f6c1b1e-4c3a-4c9b-9e1a-1234567890ab"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("respawnArgs() = %v, want %v", got, want)
	}
}

// The command is spliced into a shell line, so a session id that is not a
// plain token must be refused rather than quoted around — ids are UUIDs and
// anything else is not an id.
func TestRespawnArgsRefuses(t *testing.T) {
	for _, tt := range []struct{ pane, cmd, id string }{
		{"", "claude", ""},
		{"%3", "", ""},
		{"%3", "claude", "abc; rm -rf /"},
		{"%3", "claude", "a b"},
	} {
		if _, ok := respawnArgs(tt.pane, tt.cmd, tt.id); ok {
			t.Errorf("respawnArgs(%q, %q, %q) accepted; want refusal", tt.pane, tt.cmd, tt.id)
		}
	}
}

func TestClaudeRestartChip(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name      string
		armed     bool
		canResume bool
		note      string
		noteAt    time.Time
		want      string
	}{
		{name: "idle", want: ""},
		{name: "armed with a session to resume", armed: true, canResume: true, want: "↻ restart claude? c resume · n new"},
		{name: "armed, nothing to resume", armed: true, want: "↻ restart claude? n new"},
		{name: "fresh note", note: "restarting claude…", noteAt: now.Add(-time.Second), want: "↻ restarting claude…"},
		{name: "expired note", note: "restarting claude…", noteAt: now.Add(-2 * teardownNoteTTL), want: ""},
		{name: "armed outranks a note", armed: true, note: "no claude pane", noteAt: now, want: "↻ restart claude? n new"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := claudeRestartChip(tt.armed, tt.canResume, tt.note, tt.noteAt, now); got != tt.want {
				t.Errorf("claudeRestartChip() = %q, want %q", got, tt.want)
			}
		})
	}
}

func pressKey(m model, key string) (model, tea.Cmd) {
	var msg tea.KeyMsg
	switch key {
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	next, cmd := m.Update(msg)
	return next.(model), cmd
}

// `c` arms; `esc` disarms without firing.
func TestClaudeRestartKeyArmsAndEscCancels(t *testing.T) {
	m := teardownTestModel()
	m.sessionID = "abc"

	m, cmd := pressKey(m, "c")
	if !m.claudeRestartArmed {
		t.Fatal("first c must arm")
	}
	if cmd != nil {
		t.Error("arming must not run anything")
	}
	if got := m.statusChip(time.Now()); got != "↻ restart claude? c resume · n new" {
		t.Errorf("chip while armed = %q", got)
	}

	m, cmd = pressKey(m, "esc")
	if m.claudeRestartArmed {
		t.Error("esc must disarm")
	}
	if cmd != nil {
		t.Error("esc from an armed restart must not quit")
	}
}

// The second press fires: `c` resumes the followed session, `n` starts a new
// one. Both disarm and return a command.
func TestClaudeRestartKeyFires(t *testing.T) {
	for _, tt := range []struct {
		key      string
		wantFire bool
	}{
		{"c", true},
		{"n", true},
	} {
		m := teardownTestModel()
		m.sessionID = "abc"
		m, _ = pressKey(m, "c")
		m, cmd := pressKey(m, tt.key)
		if m.claudeRestartArmed {
			t.Errorf("%s while armed must disarm", tt.key)
		}
		if (cmd != nil) != tt.wantFire {
			t.Errorf("%s while armed: cmd != nil is %v, want %v", tt.key, cmd != nil, tt.wantFire)
		}
	}
}

// With no session to resume (waiting mode), `c` while armed is a no-op: the
// chip does not offer it, so the key must not silently start a fresh session
// instead.
func TestClaudeRestartResumeNeedsSession(t *testing.T) {
	m := teardownTestModel()
	m.sessionID = ""
	m, _ = pressKey(m, "c")
	if !m.claudeRestartArmed {
		t.Fatal("c must still arm with nothing to resume")
	}
	m, cmd := pressKey(m, "c")
	if !m.claudeRestartArmed || cmd != nil {
		t.Error("second c with no session must neither fire nor disarm")
	}
	m, cmd = pressKey(m, "n")
	if m.claudeRestartArmed || cmd == nil {
		t.Error("n must fire a fresh restart")
	}
}

// The restart and teardown ladders never cross: `c` does nothing while a
// teardown is in flight, and `x`/`X` do nothing while a restart is armed.
func TestClaudeRestartAndTeardownAreDisjoint(t *testing.T) {
	m := teardownTestModel()
	m.teardown = teardownDirect
	m, _ = pressKey(m, "c")
	if m.claudeRestartArmed {
		t.Error("c must not arm while a teardown is armed")
	}

	m = teardownTestModel()
	m, _ = pressKey(m, "c")
	m, cmd := pressKey(m, "x")
	if m.teardown != teardownIdle || cmd != nil {
		t.Error("x must do nothing while a restart is armed")
	}
	m, cmd = pressKey(m, "X")
	if m.teardown != teardownIdle || cmd != nil {
		t.Error("X must do nothing while a restart is armed")
	}
	if !m.claudeRestartArmed {
		t.Error("stray keys must not disarm the restart")
	}
}

// Outside tmux there is no pane to respawn, so `c` never arms.
func TestClaudeRestartKeyOutsideTmux(t *testing.T) {
	m := teardownTestModel()
	m.selfPane = ""
	m, _ = pressKey(m, "c")
	if m.claudeRestartArmed {
		t.Error("c must not arm outside tmux")
	}
}

// The outcome message lands on the chip as a timed note.
func TestClaudeRestartMsgSetsNote(t *testing.T) {
	m := teardownTestModel()
	next, _ := m.Update(claudeRestartMsg{note: "no claude pane"})
	m = next.(model)
	if got := m.statusChip(time.Now()); got != "↻ no claude pane" {
		t.Errorf("chip after failure = %q", got)
	}
}

// A poll that saw the claude pane records it, and a poll that did not (a
// dead pane is filtered out of the listing) leaves the last sighting alone —
// that is precisely the pane a restart of a crashed claude has to respawn.
func TestClaudePaneIsSticky(t *testing.T) {
	m := teardownTestModel()
	next, _ := m.Update(dataMsg{time: time.Now(), claudePane: "%7"})
	m = next.(model)
	if m.claudePane != "%7" {
		t.Fatalf("claudePane = %q, want %%7", m.claudePane)
	}
	next, _ = m.Update(dataMsg{time: time.Now()})
	m = next.(model)
	if m.claudePane != "%7" {
		t.Errorf("claudePane = %q after a poll without a pane, want %%7 kept", m.claudePane)
	}
}

// Outside tmux the command reports the failure rather than shelling out.
func TestRestartClaudeCmdNoPane(t *testing.T) {
	msg := restartClaudeCmd("", "/nonexistent", "", "")()
	got, ok := msg.(claudeRestartMsg)
	if !ok {
		t.Fatalf("got %T, want claudeRestartMsg", msg)
	}
	if got.note != "no claude pane" {
		t.Errorf("note = %q, want %q", got.note, "no claude pane")
	}
}
