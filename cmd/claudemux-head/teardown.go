package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

// worktreeListed reports whether `git worktree list --porcelain` output names
// path as a worktree of the repo. Each stanza opens with a "worktree <abs
// path>" line; every other line is ignored.
//
// Paths are compared after filepath.Clean so a trailing slash cannot cause a
// false negative. Callers pass a symlink-resolved path: git prints resolved
// paths, and on macOS a /var/... work dir is really /private/var/..., which
// would otherwise never match.
func worktreeListed(porcelain, path string) bool {
	if path == "" {
		return false
	}
	want := filepath.Clean(path)
	for _, line := range strings.Split(porcelain, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree ")
		if !ok {
			continue
		}
		if filepath.Clean(strings.TrimSpace(rest)) == want {
			return true
		}
	}
	return false
}

// mainCheckoutFromCommonDir turns `git rev-parse --git-common-dir` output into
// the main checkout's path: the common dir is <main>/.git, so its parent is the
// checkout. Empty output yields empty, never ".".
func mainCheckoutFromCommonDir(out string) string {
	common := strings.TrimSpace(out)
	if common == "" {
		return ""
	}
	return filepath.Dir(common)
}

// mainCheckoutFor resolves the main checkout of the repo containing dir, or ""
// when dir is not in a repo, git is missing, or git is too old for
// --path-format (added in 2.31; without it the output could be relative and
// unusable once dir is deleted).
//
// Called once at startup, while dir still exists: by the time a teardown needs
// it, the wrap-up command may have deleted the working directory out from under
// the process, leaving nowhere to run git from. That startup call is also why
// this needs a deadline like every other subprocess in this feature, not
// fewer: a hung git here (a network filesystem, a stalled credential helper)
// would block newModel and the head would never render at all. "" is already
// the safe answer on any failure, so bounding the wait only fixes the hang.
func mainCheckoutFor(dir string) string {
	if dir == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse",
		"--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	return mainCheckoutFromCommonDir(string(out))
}

// worktreeIsGone reports whether the session's worktree has been torn down.
//
// Two ways to be gone, because a wrap-up may do either: the directory is
// deleted (the common case — `git worktree remove`), or the directory survives
// but git no longer registers it. Anything unclear — no main checkout captured,
// git unavailable, a stat error that isn't "not exist" — reports NOT gone. The
// gate this feeds guards a kill-session, so uncertainty must hold it shut.
func worktreeIsGone(ctx context.Context, workDir, mainCheckout string) bool {
	if workDir == "" {
		return false
	}
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		return true
	}
	if mainCheckout == "" {
		return false
	}
	out, err := exec.CommandContext(ctx, "git", "-C", mainCheckout,
		"worktree", "list", "--porcelain").Output()
	if err != nil {
		return false
	}
	return !worktreeListed(string(out), workDir)
}

// gitCleanReason reports why dir's checkout is not provably wrapped up: a
// dirty tree, commits the upstream has not seen, or no upstream at all. ""
// means clean-and-pushed — the same bar `/done` itself holds work to. Every
// failure mode (git missing, timeout, not a repo) returns a blocking reason
// rather than "": this feeds a gate that kills a session, and a probe that
// cannot tell must never open it.
func gitCleanReason(ctx context.Context, dir string) string {
	if dir == "" {
		// `git -C ""` is a no-op, not an error: git silently runs against the
		// head process's OWN cwd instead of the session's, so an empty dir
		// must be refused here rather than falling through to a probe of the
		// wrong repository.
		return "probe failed"
	}
	status, err := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return "probe failed"
	}
	if len(bytes.TrimSpace(status)) > 0 {
		return "dirty tree"
	}
	ahead, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-list", "--count", "@{upstream}..HEAD").Output()
	if err != nil {
		// rev-list fails when no upstream is configured — indistinguishable
		// here from other failures, and both must block, so one reason
		// suffices for the common case and stays honest for the rest.
		return "no upstream"
	}
	if n := strings.TrimSpace(string(ahead)); n != "0" {
		return "unpushed"
	}
	return ""
}

// teardownPhase is where a session teardown has got to. It advances on the `x`
// and `X` keys and on poll ticks; every phase but teardownIdle is visible in
// the status line, because a key that arms a kill-session must never be armed
// silently.
type teardownPhase int

const (
	teardownIdle teardownPhase = iota
	// teardownSent: the wrap-up command has been typed into the claude pane
	// (or skipped, when teardown.command is empty) and the ready gate is
	// being polled.
	teardownSent
	// teardownReady: the gate is open. The next `x` exits claude and kills
	// the session.
	teardownReady
	// teardownExiting: /exit has been sent; waiting for claude to actually be
	// gone before killing the session.
	teardownExiting
	// teardownDirect: `X` armed a teardown that skips the wrap-up entirely —
	// nothing typed into the claude pane, no ready gate to earn. The next `X`
	// exits claude and kills the session.
	//
	// It exists because the gated ladder's evidence is not always obtainable.
	// That ladder only offers the kill once it can prove the wrap-up
	// succeeded (the worktree is gone, or the tree is clean and pushed), and
	// several things legitimately destroy the proof: a transcript rotation
	// mid-wrap-up aborts the watch, a `/done` that finishes its work but
	// leaves the branch unpushed never opens the gate, and a session with no
	// worktree and no upstream cannot open it at all. The user is then left
	// re-running a wrap-up that has already run to coax a chip out of the
	// head. This ladder answers the different question — "I have decided this
	// session is finished, end it" — and so needs no evidence beyond two
	// deliberate presses of a key nothing else uses.
	teardownDirect
)

// teardownNoteTTL is how long an abort reason stays on the status line. Long
// enough to read after glancing away, short enough that it doesn't look like
// persistent state.
const teardownNoteTTL = 5 * time.Second

// teardownSubmitTimeout bounds the wait for evidence that the wrap-up command
// actually reached claude. Injecting keystrokes into someone else's TUI is
// best-effort, so it is checked rather than assumed: a command left typed but
// unsubmitted aborts loudly instead of hanging in "wrapping up…" forever.
const teardownSubmitTimeout = 10 * time.Second

// teardownExitTimeout bounds the wait for claude to exit. On expiry the
// session is left ALIVE — letting claude finish on its own terms is the entire
// reason the kill comes last.
const teardownExitTimeout = 15 * time.Second

// teardownBlockedProbeInterval is how often the ready gate is re-sampled once
// a BLOCKED reading has been seen (turn over, worktree still standing).
//
// Before that, the gate is probed on every one-second tick, because the whole
// point is to offer the second press promptly. But a blocked teardown is a
// resting state that a user may leave on screen indefinitely — a `/done` that
// bailed on unpushed work sits there until they cancel or fix it. At 1 Hz that
// is a `git worktree list` fork every second, ~30k processes overnight, to
// re-answer a question whose answer only changes when the human does something.
// Five seconds is still well inside human reaction time for the moment the
// worktree finally disappears.
//
// A rate limit rather than the 10s cache in worktree.go: that cache memoizes a
// repo's linked-worktree SET keyed by an arbitrary cwd and drops the main
// worktree from its results, so it answers a different question than
// worktreeIsGone's "is this exact path still registered" — and its TTL would
// also blunt the responsive pre-blocked path, which is the one that matters.
const teardownBlockedProbeInterval = 5 * time.Second

// teardownTurnEnded reports whether claude has stopped working.
//
// StateAwaiting counts as ended on purpose: the wrap-up command asking for its
// confirmation is a legitimate resting point, and the gate's second condition
// (the worktree being gone) cannot hold until that question is answered
// anyway. StateCompacting does NOT count — the session is mid-turn and about
// to keep going.
//
// Note that classifyState no longer emits StateAwaiting at all: the 15s
// unresolved-tool_use heuristic that produced it was removed as too
// false-positive-prone (see state.go, "Just report Tool"). The mapping is kept
// because it is the right answer if the classification ever returns, but today
// that branch is unreachable in production — only the table test drives it.
// StateAsking (the hook-driven pending-AskUserQuestion state) counts as ended
// for the same reason Awaiting does: the wrap-up's own confirmation question
// is a resting point, and the worktree-gone condition still gates the second
// press until it is answered.
func teardownTurnEnded(kind StateKind) bool {
	switch kind {
	case StateThinking, StateTool, StateCompacting:
		return false
	}
	return true
}

// teardownGateOpen reports whether the second `x` press should be offered.
//
// For a worktree session the worktree must be gone, which is the whole signal
// that the wrap-up actually succeeded: a /done that bailed on uncommitted or
// unpushed work leaves it standing, and the gate stays shut. A session that
// was never in a worktree has no such evidence available, so it gates on the
// turn ending alone.
func teardownGateOpen(kind StateKind, inWorktree, worktreeGone bool) bool {
	if !teardownTurnEnded(kind) {
		return false
	}
	if !inWorktree {
		return true
	}
	return worktreeGone
}

// teardownAutoGateOpen is teardownGateOpen for auto-armed teardowns. The
// difference is the non-worktree arm: a manual `x` press has the user right
// there watching, so turn-end suffices; an auto-armed teardown acts with
// nobody at the wheel and needs the wrap-up's own success bar — clean tree,
// nothing unpushed — before it may kill the session.
func teardownAutoGateOpen(kind StateKind, inWorktree, worktreeGone bool, cleanReason string) bool {
	if !teardownTurnEnded(kind) {
		return false
	}
	if inWorktree {
		return worktreeGone
	}
	return cleanReason == ""
}

// teardownCommandSegment reduces a slash command to the part that identifies
// it: everything after the last "/" or ":".
//
// Claude Code canonicalizes slash commands to their plugin-qualified form, so
// the `/done` a user types is recorded in the transcript as
// `/ameriglide-core:done`. Comparing whole strings would miss that, and
// comparing prefixes would match `/done-something`. The final segment is the
// command's own name in both spellings.
func teardownCommandSegment(s string) string {
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// teardownCommandTyped reports whether prompt is the user invoking command by
// hand — the signal that a wrap-up is under way that the head did not start.
//
// Both sides are reduced to their last segment (see teardownCommandSegment),
// so `/done`, `/ameriglide-core:done` and `/anyplugin:done` all match a
// configured `/done`, while `/done-something` and `/undone` do not: their
// segments are different command names, not different spellings of the same
// one.
//
// Only the prompt's first whitespace-delimited token is considered, because a
// slash command may carry arguments and `/done --force` is still the wrap-up.
// Both sides must begin with "/" so that ordinary prose can never match: a
// sentence mentioning a path like `scripts/done` reduces to the same segment,
// but it is not a command and must not arm a kill-session.
func teardownCommandTyped(prompt, command string) bool {
	command = strings.TrimSpace(command)
	if !strings.HasPrefix(command, "/") {
		return false // unset, or not a slash command: nothing to recognize
	}
	want := teardownCommandSegment(command)
	if want == "" {
		return false
	}
	prompt = strings.TrimSpace(prompt)
	if !strings.HasPrefix(prompt, "/") {
		return false
	}
	if i := strings.IndexFunc(prompt, unicode.IsSpace); i >= 0 {
		prompt = prompt[:i]
	}
	return teardownCommandSegment(prompt) == want
}

// teardownChip renders the status-line chip for a teardown, or "" when there
// is nothing to show.
//
// An abort note is shown only from teardownIdle: it explains why a teardown
// stopped, so a note left over from an earlier attempt must never shadow a
// live phase.
//
// auto marks a teardown the head armed itself, off a wrap-up command the user
// typed into the claude pane. It gets its own wording for the waiting phase so
// the arming is never invisible: the user did not press `x`, so without it the
// first thing they would learn about the head watching is `press x to tear
// down` appearing unbidden. The later phases read the same either way —
// `press x to tear down` and `exiting claude…` describe the action, not who
// started it, and a blocked reading means the same thing in both paths.
//
// reason is the auto/non-worktree block's cleanliness gripe ("dirty tree",
// "unpushed", ...) — empty for a worktree block, whose reason is always the
// same fixed sentence below. When set it is appended in parens so the chip
// says what's actually holding the gate shut, not just that it is shut.
func teardownChip(p teardownPhase, blocked, auto bool, reason, note string, noteAt, now time.Time) string {
	switch p {
	case teardownSent:
		if blocked {
			if reason != "" {
				return "⏻ blocked (" + reason + ")"
			}
			return "⏻ worktree still present"
		}
		if auto {
			return "⏻ watching your wrap-up…"
		}
		return "⏻ wrapping up…"
	case teardownReady:
		return "⏻ press x to tear down"
	case teardownExiting:
		return "⏻ exiting claude…"
	case teardownDirect:
		// Names its own key: `x` here is a deliberate no-op (see
		// teardownDirectKey), so the chip has to say which press commits.
		// blocked/auto are ignored — nothing was probed, so neither can be
		// anything but stale.
		return "⏻ kill session? press X"
	}
	if note != "" && now.Sub(noteAt) < teardownNoteTTL {
		return "⏻ " + note
	}
	return ""
}

// sendLiteralArgs builds the tmux call that types text into pane verbatim.
//
// -l sends the string literally: without it tmux parses the argument as key
// names, so a configured command containing "Enter" or "C-c" would be
// interpreted rather than typed.
//
// "--" ends tmux's own option parsing. text is user config (teardown.command),
// so a value like "-p something" would otherwise be read as flags to send-keys
// rather than as characters to type.
//
// The Enter that submits is a SEPARATE call (sendEnterArgs) with a delay
// between them — see this file's teardownSendCmd for why.
func sendLiteralArgs(pane, text string) ([]string, bool) {
	if pane == "" || text == "" {
		return nil, false
	}
	return []string{"send-keys", "-t", pane, "-l", "--", text}, true
}

// sendEnterArgs builds the tmux call that submits whatever is in pane's input.
func sendEnterArgs(pane string) ([]string, bool) {
	if pane == "" {
		return nil, false
	}
	return []string{"send-keys", "-t", pane, "Enter"}, true
}

// killSessionArgs builds the tmux call that ends the session.
//
// session is a session ID ("$3"), not a name: tmux's -t resolves a name by
// exact match, then by prefix, then by fnmatch pattern, so a name could select
// a *different* session than the one looked up. An ID is unambiguous by
// construction, which is what the one irreversible call in this feature wants.
//
// An empty session is refused rather than defaulted: `kill-session` with no -t
// kills the *current* session, so a failed lookup would still destroy
// something, just not necessarily the right thing.
func killSessionArgs(session string) ([]string, bool) {
	if session == "" {
		return nil, false
	}
	return []string{"kill-session", "-t", session}, true
}

// teardownKeyDelay separates the literal text from the Enter that submits it.
// Claude Code opens a completion popup as a slash command is typed; an Enter
// arriving in the same burst can be consumed selecting the completion instead
// of submitting the line. A quarter second is imperceptible to the user and
// ample for the TUI to settle.
const teardownKeyDelay = 250 * time.Millisecond

// teardownTmuxTimeout bounds each tmux subprocess, matching the ceiling used
// everywhere else in this package. A wedged tmux server must never block the
// TUI.
const teardownTmuxTimeout = 2 * time.Second

// teardownSentMsg reports the outcome of typing the wrap-up command. note is
// empty on success and an abort reason otherwise — it is rendered verbatim in
// the status chip.
type teardownSentMsg struct{ note string }

// teardownProbeMsg carries one ready-gate observation. checkedClean records
// which question this probe actually answered — worktree-goneness or git
// cleanliness — so the handler can tell a probe of one mode from the zero
// value of the other rather than inferring the mode from the model's current
// fields. Those fields can change between a probe being issued and its
// result arriving (an esc followed by a re-arm, for instance), so a stale
// probe answering the wrong question must be recognizable as such: cleanReason
// is only meaningful when checkedClean is true, and worktreeGone only when it
// is false. cleanReason itself is "" for clean, anything else the
// human-readable reason the gate must stay shut.
type teardownProbeMsg struct {
	worktreeGone bool
	cleanReason  string
	checkedClean bool
}

// claudeGoneMsg reports whether any pane in this session is still running
// claude.
type claudeGoneMsg struct{ gone bool }

// teardownSendCmd types text into the session's claude pane and submits it.
//
// The pane is resolved here rather than cached on the model so it is always
// the pane whose transcript the head currently follows, even if the session
// rotated a moment ago.
func teardownSendCmd(selfPane, paneDir, text string) tea.Cmd {
	return func() tea.Msg {
		_, _, pane, ok := mappedTranscript(selfPane, paneDir)
		if !ok || pane == "" {
			return teardownSentMsg{note: "no claude pane"}
		}
		literal, ok := sendLiteralArgs(pane, text)
		if !ok {
			return teardownSentMsg{note: "wrap-up didn't submit"}
		}
		// Each tmux call gets its OWN deadline, per teardownTmuxTimeout's
		// contract. A single shared context would have to cover both
		// subprocesses plus the teardownKeyDelay sleep between them, leaving
		// the Enter with whatever fraction of the 2s the literal send did not
		// consume — so a slow-but-successful first call could cancel the
		// second one and report a failure to submit that never happened.
		literalCtx, cancelLiteral := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		err := exec.CommandContext(literalCtx, "tmux", literal...).Run()
		cancelLiteral()
		if err != nil {
			return teardownSentMsg{note: "wrap-up didn't submit"}
		}

		time.Sleep(teardownKeyDelay) // see teardownKeyDelay; this runs off the Update loop

		enter, ok := sendEnterArgs(pane)
		if !ok {
			return teardownSentMsg{note: "wrap-up didn't submit"}
		}
		enterCtx, cancelEnter := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		defer cancelEnter()
		if err := exec.CommandContext(enterCtx, "tmux", enter...).Run(); err != nil {
			return teardownSentMsg{note: "wrap-up didn't submit"}
		}
		// Success here means the keystrokes were delivered, NOT that claude
		// accepted them. The model separately watches the transcript for
		// evidence of a submitted prompt and aborts on teardownSubmitTimeout.
		return teardownSentMsg{}
	}
}

// teardownProbeCmd takes one ready-gate reading.
//
// workDir is the SESSION's working directory as captured when the teardown was
// armed (model.teardownWorkDir), not the head process's own cwd — the head is
// launched in the main checkout even for sessions that work in a worktree.
// checkClean selects the auto/non-worktree evidence (git cleanliness) instead
// of worktree-goneness; both run under the same deadline.
func teardownProbeCmd(workDir, mainCheckout string, checkClean bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		defer cancel()
		if checkClean {
			return teardownProbeMsg{cleanReason: gitCleanReason(ctx, workDir), checkedClean: true}
		}
		return teardownProbeMsg{worktreeGone: worktreeIsGone(ctx, workDir, mainCheckout)}
	}
}

// claudeGoneCmd reports whether claude has exited.
//
// This goes through listPanes/claudePaneCandidates directly rather than
// mappedTranscript, because mappedTranscript's ok=false collapses two very
// different situations into one value: a genuinely empty pane listing (every
// candidate exited) and a listPanes call that failed or timed out (a wedged
// tmux server, a transient error) and so returned "" regardless. A live
// session always lists at least this pane, so an empty listing here is never
// evidence of exit — it means the observation itself failed. Only a
// non-empty listing with zero claude/node candidates is confirmed exit.
// Outside tmux nothing is observable either, so it reports not-gone in every
// unobservable case — the exit wait then times out rather than falling
// through to an irreversible kill-session.
func claudeGoneCmd(selfPane string) tea.Cmd {
	return func() tea.Msg {
		if selfPane == "" {
			return claudeGoneMsg{}
		}
		listing := listPanes(selfPane)
		if listing == "" {
			return claudeGoneMsg{}
		}
		return claudeGoneMsg{gone: len(claudePaneCandidates(listing, selfPane)) == 0}
	}
}

// clientsOnSession parses `list-clients -F "#{client_name}\t#{session_id}"`
// output and returns the clients attached to session. Matched by session ID,
// not name, for the same reason killSessionArgs takes one: the clients being
// rescued here are exactly the ones the kill below is about to strand, so
// both calls must resolve the session identically.
func clientsOnSession(listing, session string) []string {
	var clients []string
	for _, line := range strings.Split(listing, "\n") {
		name, sess, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || name == "" || sess != session {
			continue
		}
		clients = append(clients, name)
	}
	return clients
}

// killSessionCmd ends the tmux session this pane lives in. It is the last
// thing the process does: the kill takes the head down with everything else,
// so there is no message to return and no state to render afterwards.
//
// Before the kill, every client attached to the dying session is switched
// away — to its last session first (the switchboard lobby, when the client
// was ferried here from there), then to the switchboard by name as a
// fallback. Without this, tmux's default detach-on-destroy detaches the
// client, which closes the whole terminal window when that terminal exists
// only to run tmux. A client with nowhere to go (attached directly, no
// lobby running) still detaches exactly as before — both switches failing
// is not an error, it is the old behavior.
//
// nil when there is no pane to resolve a session from, so callers can append
// it unconditionally — the same contract as renameTabCmd.
func killSessionCmd(selfPane string) tea.Cmd {
	if selfPane == "" {
		return nil
	}
	return func() tea.Msg {
		// Each tmux call gets its own deadline, per teardownTmuxTimeout's
		// contract (see teardownSendCmd): one shared 2s window across the
		// lookup, a switch per client, and the kill could starve the kill —
		// the one call this command exists to make.
		run := func(args ...string) error {
			ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
			defer cancel()
			return exec.CommandContext(ctx, "tmux", args...).Run()
		}
		outCtx, cancelOut := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		out, err := exec.CommandContext(outCtx, "tmux", "display-message",
			"-p", "-t", selfPane, "#{session_id}").Output()
		cancelOut()
		if err != nil {
			return nil
		}
		session := strings.TrimSpace(string(out))
		args, ok := killSessionArgs(session)
		if !ok {
			return nil
		}

		// Rescue attached clients before the session under them disappears.
		// Best-effort throughout: a failed listing or switch must never hold
		// up the kill the user already confirmed.
		listCtx, cancelList := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		listing, listErr := exec.CommandContext(listCtx, "tmux", "list-clients",
			"-F", "#{client_name}\t#{session_id}").Output()
		cancelList()
		if listErr == nil {
			for _, client := range clientsOnSession(string(listing), session) {
				// `-l` is tmux's own per-client last session — wherever this
				// client was before it landed here. The "=" match on the
				// fallback is exact-name, same as bin/claudemux uses, so a
				// project session merely prefixed "switchboard…" can't be
				// selected by accident.
				if run("switch-client", "-c", client, "-l") != nil {
					_ = run("switch-client", "-c", client, "-t", "=switchboard")
				}
			}
		}

		_ = run(args...)
		return nil
	}
}
