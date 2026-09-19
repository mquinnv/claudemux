package main

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// The launcher is a sibling script, not Go, but this is the only test runner
// the repo has — and the bug this guards is worth a CI gate rather than a
// user noticing a pane is the wrong height.
//
// tmux resolves a bare `-t NAME` by trying a window-name PREFIX match in the
// CALLER's session before it tries session names. bin/claudemux runs as a
// child of the lobby pane whenever the lobby's `n` key creates a session, and
// the lobby's window is called "claudemux-head" — so a project directory named
// "claudemux" matched the lobby's window and every bare-target command in the
// launcher's tail configured the LOBBY instead of the new session. A target
// written "NAME:" can only be a session. See
// docs/superpowers/specs/2026-09-17-tmux-target-resolution-and-lobby-restart.md.
const launcherPath = "../../bin/claudemux"

// bareSessionTarget matches `-t "$session_name"`/`-t "${session_name}"` and
// `-t "$session"`/`-t "${session}"` without the disambiguating colon — every
// variable name this script uses to hold a SESSION name (session_name is the
// name create_session and its callees use; inject_op_env, called with a
// session name as $1, assigns it to the local `session`). The list is by
// variable name, not a blanket ban on `-t "$var"`: pane-id variables
// (claude_pane, shell_pane, head_pane, and the bare $1 in pane-only
// functions) hold tmux pane ids, which are globally unique and unambiguous
// with no colon needed, and must stay allowed.
var bareSessionTarget = regexp.MustCompile(`-t "\$\{?(session_name|session)\}?"`)

func TestLauncherSessionNameTargetsAreUnambiguous(t *testing.T) {
	src, err := os.ReadFile(launcherPath)
	if err != nil {
		t.Fatalf("read launcher: %v", err)
	}
	for i, line := range strings.Split(string(src), "\n") {
		if bareSessionTarget.MatchString(line) {
			t.Errorf("%s:%d: bare session target, append the disambiguating \":\": %s",
				launcherPath, i+1, strings.TrimSpace(line))
		}
	}
}

// attach() and apply_status_color() take a SESSION name as $1, so inside those
// two functions a `-t "$1"` is a session target and needs the colon too. Every
// other `-t "$1"` in the script is a pane id (run_in_pane, pane_cmd,
// run_shell_in_pane) and is unambiguous as written.
func TestLauncherDollarOneSessionTargets(t *testing.T) {
	src, err := os.ReadFile(launcherPath)
	if err != nil {
		t.Fatalf("read launcher: %v", err)
	}
	for _, fn := range []string{"attach", "apply_status_color"} {
		body, ok := shellFuncBody(string(src), fn)
		if !ok {
			t.Fatalf("function %s() not found in %s", fn, launcherPath)
		}
		for _, line := range strings.Split(body, "\n") {
			if !strings.Contains(line, "tmux ") {
				continue
			}
			if strings.Contains(line, `-t "$1"`) {
				t.Errorf("%s(): session target written bare, use \"$1:\": %s",
					fn, strings.TrimSpace(line))
			}
		}
	}
}

// shellFuncBody returns the text between `name() {` and the first line that is
// exactly "}" — the script writes every function that way.
func shellFuncBody(src, name string) (string, bool) {
	lines := strings.Split(src, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, name+"() {") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", false
	}
	for i := start; i < len(lines); i++ {
		if lines[i] == "}" {
			return strings.Join(lines[start:i], "\n"), true
		}
	}
	return "", false
}

// The resume id is spliced into claude's command line unquoted, so the
// launcher must reject anything outside the UUID alphabet before it does any
// work — checked before tmux or the hook are touched, which is also what
// makes it testable here.
func TestLauncherRejectsBadResumeID(t *testing.T) {
	cmd := exec.Command("bash", launcherPath, "-d", "-r", "bad id;rm", "/tmp")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("launcher accepted a bad resume id; output: %s", out)
	}
	if !strings.Contains(string(out), "invalid resume id") {
		t.Errorf("output %q does not mention the invalid resume id", out)
	}
}

func TestLauncherDeclaresRestoreOptions(t *testing.T) {
	src, err := os.ReadFile(launcherPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, `getopts "ndwWr:N:C:"`) {
		t.Error(`getopts does not declare r:, N:, C:`)
	}
	body, ok := shellFuncBody(s, "create_session")
	if !ok {
		t.Fatal("create_session not found")
	}
	for _, want := range []string{`--resume $RESUME_ID`, `EXACT_NAME`, `CLAUDE_DIR`} {
		if !strings.Contains(body, want) {
			t.Errorf("create_session does not use %s", want)
		}
	}
	rip, ok := shellFuncBody(s, "run_in_pane")
	if !ok || !strings.Contains(rip, `-c "$3"`) {
		t.Error("run_in_pane does not accept a directory as $3")
	}
}

func TestLauncherSyntax(t *testing.T) {
	if out, err := exec.Command("bash", "-n", launcherPath).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}
