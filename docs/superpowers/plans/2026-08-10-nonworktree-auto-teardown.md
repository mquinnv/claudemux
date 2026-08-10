# Non-Worktree Auto-Teardown Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `/done` typed in a non-worktree session auto-arms teardown, gated on the wrap-up's own success criteria (turn ended + clean tree + nothing unpushed) instead of the worktree-gone evidence such sessions can never produce.

**Architecture:** `autoArmTeardown` stops declining non-worktree sessions. The teardown probe, for auto-armed non-worktree teardowns, checks git cleanliness instead of worktree-goneness; the probe message carries a human-readable block reason (`""` = clean). The gate for that path opens only on turn-ended + clean; blocked reasons render in the status chip and never kill. Manual (`x`) and worktree flows are byte-for-byte unchanged. Spec: `docs/superpowers/specs/2026-08-10-nonworktree-auto-teardown-design.md`.

**Tech Stack:** Go 1.26, existing teardown machinery in `cmd/claudemux-head/teardown.go` + `tui.go`.

## Global Constraints

- Git subprocess probes carry the existing `teardownTmuxTimeout` (2s) deadline and run off the Update loop (inside `tea.Cmd` funcs only).
- A probe that FAILS (git missing, timeout, not a repo) blocks — it never opens the gate. No upstream configured also blocks (reason `no upstream`): a branch that cannot have been pushed is not provably wrapped up.
- The manual `x` flow and worktree-session behavior must not change: `teardownGateOpen` keeps its exact current semantics for those paths.
- `gofmt -l cmd/claudemux-head` silent before every commit; full `go test ./...` green (known environmental exclusion: `TestWorktreeHookSilentWithoutMarker` fails in sessions carrying the claudemux worktree marker — ignore that one failure only).
- Comments explain why/constraints, not what the next line does.

---

### Task 1: Cleanliness probe + auto gate (`teardown.go`)

**Files:**
- Modify: `cmd/claudemux-head/teardown.go` (near `teardownProbeMsg` ~line 338, `teardownProbeCmd` ~line 391, `teardownGateOpen` ~line 184)
- Test: `cmd/claudemux-head/teardown_test.go` (append)

**Interfaces:**
- Consumes: `teardownTmuxTimeout`, `worktreeIsGone` (existing), `teardownTurnEnded`.
- Produces:
  - `teardownProbeMsg` gains field `cleanReason string` (meaningful only when the probe was asked to check cleanliness; `""` = clean or not-checked).
  - `func gitCleanReason(ctx context.Context, dir string) string` — `""` clean; else `"dirty tree"`, `"unpushed"`, `"no upstream"`, or `"probe failed"`.
  - `func teardownProbeCmd(workDir, mainCheckout string, checkClean bool) tea.Cmd` — signature change; existing callers updated in Task 2.
  - `func teardownAutoGateOpen(kind StateKind, inWorktree, worktreeGone bool, cleanReason string) bool`.

- [ ] **Step 1: Write the failing tests** (append to `teardown_test.go`)

```go
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

func TestGitCleanReason(t *testing.T) {
	ctx := context.Background()

	mk := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		run := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
				"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		run("init", "-q", "-b", "main")
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "f")
		run("commit", "-q", "-m", "c")
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
		cmd := exec.Command("git", "clone", "-q", dir, clone)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("clone: %v\n%s", err, out)
		}
		if got := gitCleanReason(ctx, clone); got != "" {
			t.Errorf("got %q, want clean", got)
		}
	})

	t.Run("unpushed commit blocks", func(t *testing.T) {
		dir := mk(t)
		clone := t.TempDir()
		cmd := exec.Command("git", "clone", "-q", dir, clone)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("clone: %v\n%s", err, out)
		}
		if err := os.WriteFile(filepath.Join(clone, "h"), []byte("z"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", "h"}, {"commit", "-q", "-m", "local"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = clone
			cmd.Env = append(os.Environ(),
				"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
				"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		if got := gitCleanReason(ctx, clone); got != "unpushed" {
			t.Errorf("got %q, want %q", got, "unpushed")
		}
	})

	t.Run("not a repo blocks as probe failure", func(t *testing.T) {
		if got := gitCleanReason(ctx, t.TempDir()); got != "probe failed" {
			t.Errorf("got %q, want %q", got, "probe failed")
		}
	})
}
```

Add imports to the test file as needed: `context`, `os`, `os/exec`, `path/filepath`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestTeardownAutoGateOpen|TestGitCleanReason' -v`
Expected: FAIL — `undefined: teardownAutoGateOpen`.

- [ ] **Step 3: Implement in `teardown.go`**

3a. Extend the message type (replace the existing one-field struct at ~line 339):

```go
// teardownProbeMsg carries one ready-gate observation. cleanReason is set
// only by probes asked to check git cleanliness (auto-armed non-worktree
// teardowns): "" means clean, anything else is the human-readable reason the
// gate must stay shut.
type teardownProbeMsg struct {
	worktreeGone bool
	cleanReason  string
}
```

3b. Add the cleanliness reader, near `worktreeIsGone`:

```go
// gitCleanReason reports why dir's checkout is not provably wrapped up: a
// dirty tree, commits the upstream has not seen, or no upstream at all. ""
// means clean-and-pushed — the same bar `/done` itself holds work to. Every
// failure mode (git missing, timeout, not a repo) returns a blocking reason
// rather than "": this feeds a gate that kills a session, and a probe that
// cannot tell must never open it.
func gitCleanReason(ctx context.Context, dir string) string {
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
```

Add `"bytes"` to the imports.

3c. Change `teardownProbeCmd` (~line 396) to take and act on the mode:

```go
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
			return teardownProbeMsg{cleanReason: gitCleanReason(ctx, workDir)}
		}
		return teardownProbeMsg{worktreeGone: worktreeIsGone(ctx, workDir, mainCheckout)}
	}
}
```

3d. Add the auto gate next to `teardownGateOpen` (~line 184), leaving the existing function untouched:

```go
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
```

Note: this task changes `teardownProbeCmd`'s signature, so the build breaks at its two call sites until they are updated. Update them mechanically NOW to keep the package compiling — pass `false` at both (`tui.go` ~line 836 `teardownProbeCmd(m.teardownWorkDir, m.mainCheckout)` and any other caller found via `grep -n 'teardownProbeCmd(' cmd/claudemux-head/`); Task 2 gives the call sites their real values. Existing tests asserting on `teardownProbeMsg{worktreeGone: ...}` literals still compile (the new field is zero-valued).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/claudemux-head/ -run 'TestTeardownAutoGateOpen|TestGitCleanReason' -v` then `go test ./...`
Expected: new tests PASS; suite green (modulo the known environmental exclusion).

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -l cmd/claudemux-head
git add cmd/claudemux-head/teardown.go cmd/claudemux-head/teardown_test.go cmd/claudemux-head/tui.go
git commit -m "feat(head): git-cleanliness probe and auto gate for teardown"
```

---

### Task 2: Arm everywhere, wire the auto gate (`tui.go`)

**Files:**
- Modify: `cmd/claudemux-head/tui.go` — `autoArmTeardown` (~line 1634), the `teardownSent` probe dispatch (~line 836), the `teardownProbeMsg` handler (~line 977), model struct (blocked-reason field), and the chip call (~line 1143).
- Modify: `cmd/claudemux-head/teardown.go` — `teardownChip` signature (add the reason).
- Test: `cmd/claudemux-head/tui_test.go` (append; existing teardown tests must keep passing).

**Interfaces:**
- Consumes: `teardownAutoGateOpen`, `teardownProbeCmd(workDir, mainCheckout, checkClean)`, `teardownProbeMsg.cleanReason` (Task 1); model fields `teardownAuto`, `teardownInWorktree`, `teardownSubmitted`.
- Produces: non-worktree sessions auto-arm; model field `teardownBlockReason string`; the blocked chip shows the reason for auto non-worktree blocks.

- [ ] **Step 1: Write the failing tests** (append to `tui_test.go`; mirror the style of the existing `TestTeardownAutoArm*` tests and reuse their `teardownTestModel()` helper — read those first)

```go
func TestTeardownAutoArmsOutsideWorktree(t *testing.T) {
	m := teardownTestModel()
	// teardownTestModel puts the session in a worktree; force the
	// non-worktree shape: sessionCwd outside .claude/worktrees.
	m.sessionCwd = "/tmp/plain-project"
	prev := m.lastTyped
	m.lastTyped = "/done"
	m.autoArmTeardown(prev, time.Now())
	if m.teardown != teardownSent {
		t.Fatalf("teardown = %v, want teardownSent (auto-arm must no longer decline non-worktree sessions)", m.teardown)
	}
	if !m.teardownAuto {
		t.Error("auto-armed teardown must set teardownAuto")
	}
	if m.teardownInWorktree {
		t.Error("captured target must record non-worktree")
	}
}

func TestTeardownProbeMsgAutoNonWorktree(t *testing.T) {
	arm := func() model {
		m := teardownTestModel()
		m.sessionCwd = "/tmp/plain-project"
		prev := m.lastTyped
		m.lastTyped = "/done"
		m.autoArmTeardown(prev, time.Now())
		m.teardownSubmitted = true
		m.state = State{Kind: StateIdle, Since: time.Now()}
		return m
	}

	t.Run("clean opens the gate", func(t *testing.T) {
		m := arm()
		next, _ := m.Update(teardownProbeMsg{cleanReason: ""})
		got := next.(model)
		if got.teardown != teardownReady {
			t.Errorf("teardown = %v, want teardownReady", got.teardown)
		}
	})

	t.Run("dirty blocks with reason and never kills", func(t *testing.T) {
		m := arm()
		next, _ := m.Update(teardownProbeMsg{cleanReason: "dirty tree"})
		got := next.(model)
		if got.teardown != teardownSent {
			t.Errorf("teardown = %v, want still teardownSent", got.teardown)
		}
		if !got.teardownBlocked || got.teardownBlockReason != "dirty tree" {
			t.Errorf("blocked=%v reason=%q, want blocked with dirty tree", got.teardownBlocked, got.teardownBlockReason)
		}
	})
}
```

Adapt the arm/state plumbing to what `teardownTestModel()` actually provides — the assertions are the contract, the setup lines are a sketch. If `autoArmTeardown` requires more preconditions (e.g. `selfPane`), satisfy them the way the existing auto-arm tests do.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/claudemux-head/ -run 'TestTeardownAutoArmsOutsideWorktree|TestTeardownProbeMsgAutoNonWorktree' -v`
Expected: FAIL — the first test hits the not-a-worktree decline; the second, `teardownBlockReason` undefined.

- [ ] **Step 3: Implement**

3a. `autoArmTeardown` (~line 1648): delete the `if !m.teardownInWorktree { ... return }` decline block (keep `captureTeardownTarget()`); replace with a log line so the arm is traceable:

```go
	m.captureTeardownTarget()
	teardownLogf("auto-arm prompt=%q worktree=%v cwd=%s",
		m.lastTyped, m.teardownInWorktree, m.teardownWorkDir)
```

Update the function's doc comment: the "Only for worktree sessions, which is the load-bearing restriction" paragraph is now false — rewrite it to describe the auto gate split (worktree evidence vs. git cleanliness, pointing at `teardownAutoGateOpen`).

3b. Model struct: add next to `teardownBlocked`:

```go
	// teardownBlockReason is why an auto-armed non-worktree gate is shut
	// ("dirty tree", "unpushed", ...). Rendered beside the blocked chip;
	// empty for worktree blocks, whose reason is always the same (the
	// worktree still exists).
	teardownBlockReason string
```

Clear it wherever `teardownBlocked` is reset (`abortTeardown`, the reset in `teardownKey`/arm paths — grep `teardownBlocked = false`).

3c. Probe dispatch (`teardownSent` case, ~line 836): pass the mode:

```go
	cmds = append(cmds, teardownProbeCmd(m.teardownWorkDir, m.mainCheckout,
		m.teardownAuto && !m.teardownInWorktree))
```

3d. `teardownProbeMsg` handler (~line 977): branch per path, keeping the existing worktree logic verbatim:

```go
	case teardownProbeMsg:
		m.teardownProbing = false
		if m.teardown != teardownSent {
			return m, nil
		}
		if m.teardownAuto && !m.teardownInWorktree {
			// Auto-armed without worktree evidence: the gate needs the
			// wrap-up's own success bar. Same freshness requirement as the
			// worktree path below.
			if m.teardownSubmitted && teardownAutoGateOpen(m.state.Kind, false, false, msg.cleanReason) {
				m.teardown = teardownReady
				m.teardownAt = time.Now()
				m.teardownBlocked = false
				m.teardownBlockReason = ""
				return m, nil
			}
			m.teardownBlocked = m.teardownSubmitted && teardownTurnEnded(m.state.Kind) && msg.cleanReason != ""
			if m.teardownBlocked {
				m.teardownBlockReason = msg.cleanReason
			}
			return m, nil
		}
		[existing worktree-path body unchanged]
```

3e. Chip: extend `teardownChip` to accept and render the reason (e.g. append `" ("+reason+")"` to the blocked text when non-empty), update the call at ~line 1143 to pass `m.teardownBlockReason`, and update the chip's existing tests for the new parameter (passing `""` preserves old expectations).

- [ ] **Step 4: Run tests + full suite**

Run: `go test ./cmd/claudemux-head/ -run 'TestTeardown' -v` then `go test ./...` and `go vet ./...`
Expected: new tests PASS, all existing teardown tests PASS unchanged (except chip tests mechanically updated for the added parameter), suite green.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -l cmd/claudemux-head
git add cmd/claudemux-head/tui.go cmd/claudemux-head/teardown.go cmd/claudemux-head/tui_test.go cmd/claudemux-head/teardown_test.go
git commit -m "feat(head): auto-arm teardown outside worktrees behind a cleanliness gate"
```
