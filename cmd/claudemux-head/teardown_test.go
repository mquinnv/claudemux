package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestWorktreeListed(t *testing.T) {
	const porcelain = `worktree /Users/me/Projects/app
HEAD abc123
branch refs/heads/main

worktree /Users/me/Projects/app/.claude/worktrees/floating-harp
HEAD def456
branch refs/heads/feature
`
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"main checkout", "/Users/me/Projects/app", true},
		{"linked worktree", "/Users/me/Projects/app/.claude/worktrees/floating-harp", true},
		{"trailing slash still matches", "/Users/me/Projects/app/.claude/worktrees/floating-harp/", true},
		{"removed worktree", "/Users/me/Projects/app/.claude/worktrees/gone", false},
		{"prefix is not a match", "/Users/me/Projects/app/.claude/worktrees/floating", false},
		{"empty path", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := worktreeListed(porcelain, tt.path); got != tt.want {
				t.Errorf("worktreeListed(_, %q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// Empty output means git failed or printed nothing; nothing is listed, and the
// caller must not read that as "the worktree is gone".
func TestWorktreeListedEmptyListing(t *testing.T) {
	if worktreeListed("", "/Users/me/Projects/app") {
		t.Error("empty listing matched a path")
	}
}

func TestMainCheckoutFromCommonDir(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
	}{
		{"trailing newline", "/Users/me/Projects/app/.git\n", "/Users/me/Projects/app"},
		{"no newline", "/Users/me/Projects/app/.git", "/Users/me/Projects/app"},
		{"empty", "", ""},
		{"whitespace only", "  \n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mainCheckoutFromCommonDir(tt.out); got != tt.want {
				t.Errorf("mainCheckoutFromCommonDir(%q) = %q, want %q", tt.out, got, tt.want)
			}
		})
	}
}

// A deleted work dir is gone regardless of what git says — the common case,
// and the one where git can't be run from the work dir at all.
func TestWorktreeIsGoneMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "deleted")
	if !worktreeIsGone(context.Background(), missing, "") {
		t.Error("missing work dir reported as present")
	}
}

// A directory that still exists and is still a registered worktree is present.
// This exercises the real git path end to end.
func TestWorktreeIsGoneLiveWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo, wt := initRepoWithWorktree(t, "wt")

	if got := mainCheckoutFor(wt); got != repo {
		t.Errorf("mainCheckoutFor = %q, want %q", got, repo)
	}
	if worktreeIsGone(context.Background(), wt, repo) {
		t.Error("live worktree reported as gone")
	}

	cmd := exec.Command("git", "-C", repo, "worktree", "remove", "--force", wt)
	cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree remove: %v\n%s", err, out)
	}
	if !worktreeIsGone(context.Background(), wt, repo) {
		t.Error("removed worktree reported as present")
	}
}

func TestTeardownTurnEnded(t *testing.T) {
	tests := []struct {
		kind StateKind
		want bool
	}{
		{StateIdle, true},
		{StateAwaiting, true}, // a wrap-up asking its confirmation is a real pause
		{StateError, true},
		{StateThinking, false},
		{StateTool, false},
		{StateCompacting, false},
	}
	for _, tt := range tests {
		if got := teardownTurnEnded(tt.kind); got != tt.want {
			t.Errorf("teardownTurnEnded(%v) = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestTeardownGateOpen(t *testing.T) {
	tests := []struct {
		name         string
		kind         StateKind
		inWorktree   bool
		worktreeGone bool
		want         bool
	}{
		{"worktree gone, turn ended", StateIdle, true, true, true},
		{"worktree gone but still working", StateTool, true, true, false},
		{"turn ended, worktree survives", StateIdle, true, false, false},
		{"awaiting confirmation, worktree survives", StateAwaiting, true, false, false},
		{"not a worktree, turn ended", StateIdle, false, false, true},
		{"not a worktree, still working", StateThinking, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := teardownGateOpen(tt.kind, tt.inWorktree, tt.worktreeGone)
			if got != tt.want {
				t.Errorf("teardownGateOpen(%v, %v, %v) = %v, want %v",
					tt.kind, tt.inWorktree, tt.worktreeGone, got, tt.want)
			}
		})
	}
}

// The matcher behind auto-arming. It decides whether a prompt in the
// transcript is the configured wrap-up command, and a false positive here arms
// a sequence that ends in kill-session — so the negatives matter as much as
// the positives.
func TestTeardownCommandTyped(t *testing.T) {
	tests := []struct {
		name    string
		prompt  string
		command string
		want    bool
	}{
		// Claude Code canonicalizes a typed /done to its plugin-qualified
		// form, so all three spellings reach the transcript in practice.
		{"bare command", "/done", "/done", true},
		{"plugin-qualified", "/ameriglide-core:done", "/done", true},
		{"some other plugin", "/anyplugin:done", "/done", true},
		{"with arguments", "/ameriglide-core:done --force", "/done", true},
		{"surrounding whitespace", "  /done\n", "/done", true},
		// Different command names that merely start or end the same way.
		{"longer command name", "/done-something", "/done", false},
		{"command ending in the name", "/undone", "/done", false},
		{"prose mentioning it", "please run /done for me", "/done", false},
		{"a path, not a command", "scripts/done", "/done", false},
		{"a different command", "/clear", "/done", false},
		// Nothing configured means nothing to recognize: an empty
		// teardown.command makes `x` a gated exit, with no wrap-up to watch
		// for.
		{"empty command", "/done", "", false},
		{"empty prompt", "", "/done", false},
		{"non-slash command config", "done", "/done", false},
		// A configured command can itself be plugin-qualified.
		{"qualified config, bare prompt", "/done", "/ameriglide-core:done", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := teardownCommandTyped(tt.prompt, tt.command); got != tt.want {
				t.Errorf("teardownCommandTyped(%q, %q) = %v, want %v",
					tt.prompt, tt.command, got, tt.want)
			}
		})
	}
}

func TestTeardownChip(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		phase   teardownPhase
		blocked bool
		auto    bool
		reason  string
		note    string
		noteAt  time.Time
		want    string
	}{
		{"idle, nothing to say", teardownIdle, false, false, "", "", time.Time{}, ""},
		{"sent", teardownSent, false, false, "", "", time.Time{}, "⏻ wrapping up…"},
		{"sent but blocked", teardownSent, true, false, "", "", time.Time{}, "⏻ worktree still present"},
		{"ready", teardownReady, false, false, "", "", time.Time{}, "⏻ press x to tear down"},
		{"exiting", teardownExiting, false, false, "", "", time.Time{}, "⏻ exiting claude…"},
		{"fresh abort note", teardownIdle, false, false, "", "claude didn't exit", now.Add(-2 * time.Second), "⏻ claude didn't exit"},
		{"expired abort note", teardownIdle, false, false, "", "claude didn't exit", now.Add(-6 * time.Second), ""},
		{"note is ignored mid-teardown", teardownSent, false, false, "", "stale", now, "⏻ wrapping up…"},
		// A teardown the head armed itself says so while it waits: the user
		// pressed nothing, so the arming must not be invisible.
		{"auto-armed, waiting", teardownSent, false, true, "", "", time.Time{}, "⏻ watching your wrap-up…"},
		// A bailed wrap-up means the same thing however it was armed.
		{"auto-armed but blocked", teardownSent, true, true, "", "", time.Time{}, "⏻ worktree still present"},
		// Past the wait the chip describes the action, not its provenance.
		{"auto-armed, ready", teardownReady, false, true, "", "", time.Time{}, "⏻ press x to tear down"},
		// An auto/non-worktree block carries a cleanliness reason, and the
		// chip must say what it is rather than the fixed worktree sentence.
		{"auto-armed, blocked with reason", teardownSent, true, true, "dirty tree", "", time.Time{}, "⏻ blocked (dirty tree)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := teardownChip(tt.phase, tt.blocked, tt.auto, tt.reason, tt.note, tt.noteAt, now)
			if got != tt.want {
				t.Errorf("teardownChip() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSendLiteralArgs(t *testing.T) {
	args, ok := sendLiteralArgs("%3", "/done")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	// "--" ends option parsing: teardown.command is user config, and a value
	// starting with "-" must be typed, not read as a flag to send-keys.
	want := []string{"send-keys", "-t", "%3", "-l", "--", "/done"}
	if !slices.Equal(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
}

// Nothing to type, or nowhere to type it, must build no command at all —
// `send-keys -l ""` is a no-op that still costs a subprocess, and a missing
// pane target would send to whatever tmux considers current.
func TestSendLiteralArgsRefusesEmpty(t *testing.T) {
	if _, ok := sendLiteralArgs("", "/done"); ok {
		t.Error("empty pane accepted")
	}
	if _, ok := sendLiteralArgs("%3", ""); ok {
		t.Error("empty text accepted")
	}
}

func TestSendEnterArgs(t *testing.T) {
	args, ok := sendEnterArgs("%3")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{"send-keys", "-t", "%3", "Enter"}
	if !slices.Equal(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
	if _, ok := sendEnterArgs(""); ok {
		t.Error("empty pane accepted")
	}
}

func TestKillSessionArgs(t *testing.T) {
	args, ok := killSessionArgs("claudemux")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{"kill-session", "-t", "claudemux"}
	if !slices.Equal(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}
	if _, ok := killSessionArgs(""); ok {
		t.Error("empty session accepted — would kill the current session")
	}
}

func TestClientsOnSession(t *testing.T) {
	listing := "/dev/ttys001\t$3\n" +
		"/dev/ttys002\t$7\n" +
		"/dev/ttys003\t$3\n" +
		"garbage-line-no-tab\n" +
		"\t$3\n" + // no client name
		"\n"
	got := clientsOnSession(listing, "$3")
	want := []string{"/dev/ttys001", "/dev/ttys003"}
	if !slices.Equal(got, want) {
		t.Errorf("clients = %v, want %v", got, want)
	}
	if got := clientsOnSession(listing, "$9"); got != nil {
		t.Errorf("clients = %v for a session with none attached, want nil", got)
	}
	if got := clientsOnSession("", "$3"); got != nil {
		t.Errorf("clients = %v for an empty listing, want nil", got)
	}
}

// Outside tmux there is no pane to type into; the command must report that
// rather than shelling out or silently succeeding.
func TestTeardownSendCmdNoPane(t *testing.T) {
	msg := teardownSendCmd("", t.TempDir(), "/done")()
	sent, ok := msg.(teardownSentMsg)
	if !ok {
		t.Fatalf("msg = %T, want teardownSentMsg", msg)
	}
	if sent.note != "no claude pane" {
		t.Errorf("note = %q, want %q", sent.note, "no claude pane")
	}
}

// The probe reports gone for a work dir that no longer exists — no tmux, no
// repo, no session required.
func TestTeardownProbeCmdMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "deleted")
	msg := teardownProbeCmd(missing, "", false)()
	probe, ok := msg.(teardownProbeMsg)
	if !ok {
		t.Fatalf("msg = %T, want teardownProbeMsg", msg)
	}
	if !probe.worktreeGone {
		t.Error("worktreeGone = false, want true")
	}
}

func TestTeardownProbeCmdLiveDir(t *testing.T) {
	msg := teardownProbeCmd(t.TempDir(), "", false)()
	if probe := msg.(teardownProbeMsg); probe.worktreeGone {
		t.Error("worktreeGone = true for a directory that still exists")
	}
}

// Outside tmux nothing can be observed, so "gone" must be false: reporting
// gone would let the exit wait fall through to a kill-session.
func TestClaudeGoneCmdOutsideTmux(t *testing.T) {
	msg := claudeGoneCmd("")()
	gone, ok := msg.(claudeGoneMsg)
	if !ok {
		t.Fatalf("msg = %T, want claudeGoneMsg", msg)
	}
	if gone.gone {
		t.Error("gone = true outside tmux")
	}
}

// A kill with no pane to resolve a session from must do nothing at all.
func TestKillSessionCmdNoPane(t *testing.T) {
	if cmd := killSessionCmd(""); cmd != nil {
		t.Error("killSessionCmd(\"\") returned a command")
	}
}

func TestTeardownAutoGateOpen(t *testing.T) {
	cases := []struct {
		name        string
		kind        StateKind
		inWorktree  bool
		gone        bool
		cleanReason string
		want        bool
	}{
		{"worktree gone opens", StateIdle, true, true, "", true},
		{"worktree present holds", StateIdle, true, false, "", false},
		{"non-worktree clean opens", StateIdle, false, false, "", true},
		{"non-worktree dirty holds", StateIdle, false, false, "dirty tree", false},
		{"non-worktree unpushed holds", StateIdle, false, false, "unpushed", false},
		{"non-worktree no-upstream holds", StateIdle, false, false, "no upstream", false},
		{"turn not ended holds even when clean", StateThinking, false, false, "", false},
		{"pending tool holds even when clean", StateTool, false, false, "", false},
	}
	for _, c := range cases {
		if got := teardownAutoGateOpen(c.kind, c.inWorktree, c.gone, c.cleanReason); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// gitTestEnv isolates a test fixture's git calls from this machine's
// global/system gitconfig — e.g. this machine's commit.gpgsign=true, which
// makes these tests pass here only because a working signing key happens to
// be present. A CI runner with signing mandated but no key/agent would hang
// on pinentry or fail outright. GIT_CONFIG_GLOBAL/GIT_CONFIG_SYSTEM pointed
// at /dev/null neutralize both config tiers for every subcommand (init, add,
// commit, clone, status, rev-list alike), so the fixture behaves the same
// everywhere. gitCleanReason itself must NOT use this: it probes the user's
// real repos at runtime and has to see their actual config.
func gitTestEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
}

// runGitTest runs a git fixture command under gitTestEnv, failing the test on
// error. dir is the working directory ("" to run without one, as clone does
// not need one).
func runGitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitTestEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestGitCleanReason(t *testing.T) {
	ctx := context.Background()

	mk := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		runGitTest(t, dir, "init", "-q", "-b", "main")
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, dir, "add", "f")
		runGitTest(t, dir, "commit", "-q", "-m", "c")
		return dir
	}

	t.Run("no upstream blocks", func(t *testing.T) {
		if got := gitCleanReason(ctx, mk(t)); got != "no upstream" {
			t.Errorf("got %q, want %q", got, "no upstream")
		}
	})

	t.Run("dirty tree blocks before upstream is consulted", func(t *testing.T) {
		dir := mk(t)
		if err := os.WriteFile(filepath.Join(dir, "g"), []byte("y"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := gitCleanReason(ctx, dir); got != "dirty tree" {
			t.Errorf("got %q, want %q", got, "dirty tree")
		}
	})

	t.Run("clean with pushed upstream", func(t *testing.T) {
		dir := mk(t)
		// A local branch tracking itself via a file remote: clone the repo and
		// use the clone, whose origin/main equals its HEAD.
		clone := t.TempDir()
		runGitTest(t, "", "clone", "-q", dir, clone)
		if got := gitCleanReason(ctx, clone); got != "" {
			t.Errorf("got %q, want clean", got)
		}
	})

	t.Run("unpushed commit blocks", func(t *testing.T) {
		dir := mk(t)
		clone := t.TempDir()
		runGitTest(t, "", "clone", "-q", dir, clone)
		if err := os.WriteFile(filepath.Join(clone, "h"), []byte("z"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, clone, "add", "h")
		runGitTest(t, clone, "commit", "-q", "-m", "local")
		if got := gitCleanReason(ctx, clone); got != "unpushed" {
			t.Errorf("got %q, want %q", got, "unpushed")
		}
	})

	t.Run("not a repo blocks as probe failure", func(t *testing.T) {
		if got := gitCleanReason(ctx, t.TempDir()); got != "probe failed" {
			t.Errorf("got %q, want %q", got, "probe failed")
		}
	})

	t.Run("empty dir blocks as probe failure rather than probing our own cwd", func(t *testing.T) {
		if got := gitCleanReason(ctx, ""); got != "probe failed" {
			t.Errorf("got %q, want %q", got, "probe failed")
		}
	})
}
