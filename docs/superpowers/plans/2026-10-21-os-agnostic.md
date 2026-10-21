# claudemux on macOS, Linux and Windows: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** claudemux runs natively on macOS, Linux and Windows (amd64 and arm64 each). It installs through each platform's own channel and behaves the same on all three, except where a terminal lacks a feature, such as tab colours.

**Architecture:** One Go binary does everything. `claudemux-head` takes over the bash launcher's job as well. When it is invoked under the name `claudemux` (argv[0] dispatch), it runs the launcher, so there is still one build artifact, and a `claudemux` copy or symlink beside it serves as the launcher. tmux stays the multiplexer. On Windows it is **psmux**, a native tmux clone that speaks tmux's command language, sets `TMUX`/`TMUX_PANE`, and installs a `tmux` alias. Every multiplexer call goes through one runner. OS differences live in `_unix.go`/`_windows.go`/`_darwin.go`/`_linux.go` files behind small functions. Pane commands become argv (`cmd [arg...]`, with tmux's `-c dir -e K=V`) and are never shell strings. Claude Code hooks are registered in **exec form** (`command` + `args`), so no shell sits between Claude Code and the binary on any OS.

**Tech Stack:** Go (pure, `CGO_ENABLED=0`), `golang.org/x/sys/windows` (Job Objects, `DurationSinceBoot`), tmux ≥ 3.2 (macOS/Linux), psmux ≥ 3.3.8 (Windows), GitHub Actions matrix (macos-latest, ubuntu-latest, windows-latest).

**Spec:** There is no separate design doc. The decisions are in **Decisions** below, and the evidence is in **Facts this plan rests on**. Both travel with this plan. Each phase is shippable on its own. Phases 3–5 are specified at task level and must each be expanded into their own detailed plan before execution, because each depends on what the previous phase actually landed.

## Decisions (recommended; confirm before Phase 3)

1. **Windows multiplexer: psmux, behind the existing tmux command language.** It is the only option that accepts the tmux commands claudemux already sends, which keeps one code path. It is its own Rust server on ConPTY, not a wrapper over a real tmux; its source tree has its own `vt100-psmux` and `portable-pty-psmux` crates, and it uses loopback TCP. The alternatives were weighed. zellij (native Windows since v0.44, x86_64 only) would need a second backend. WezTerm's mux would force a terminal choice. Windows Terminal's `wt.exe` cannot list panes, read them or send keys. Writing our own ConPTY multiplexer would mean writing a VT emulator. WSL2 stays documented as an alternative route, not as the Windows story.
2. **The launcher becomes Go, inside the same binary.** The bash launcher (`bin/claudemux`, 1273 lines) is why Windows needs Git Bash at all. The Go head already shells back into it (`switchboardtui.go:546`, `swrestore.go:205`), and it has 40+ external-tool dependencies. Using argv[0] dispatch instead of a second `cmd/` avoids splitting the 18k-line `package main` into packages first.
3. **No shell strings anywhere a pane is started.** Today's `cd %q && exec CMD`, the `/bin/sh -c` shell-pane wrapper, `respawn-pane "exec …"` and `display-popup` POSIX quoting all assume a POSIX default shell. psmux's default shell is PowerShell. tmux has accepted `cmd [arg...]` executed directly (no `sh -c`) since 3.0, plus `-c`/`-e` on `new-session`, `split-window`, `respawn-pane` and `display-popup`. This was checked against the tmux 3.8 man page.
4. **Packaging on Windows: a zip per arch, `install.ps1`, and our own Scoop bucket.** winget comes later; it needs a reviewed PR to `microsoft/winget-pkgs`. Homebrew and `install.sh` stay as they are.
5. **Tab colours: iTerm2 keeps OSC 6. Windows Terminal is best-effort or off.** Windows Terminal has no RGB tab-colour sequence, only DECAC with a palette index, which Claude Code's `terminalSequence` rejects. Tabs on Windows stay uncoloured in v1, as they already are in WSL today.

## Facts this plan rests on (verified 2026-10-21 unless marked)

- `GOOS=windows go build ./cmd/claudemux-head` fails on exactly `usage.go:85` (`Setpgid`) and `usage.go:87` (`syscall.Kill`). `go test -c` additionally fails on `env_test.go:113,148,197` (`syscall.Mkfifo`). `syscall.Exec` (`boot.go:273`, `restart.go:43`) compiles but returns `EWINDOWS` at runtime.
- The head has no central tmux wrapper: about 46 call sites across 17 files, with `swTmux` (`switchboardtui.go:508`) used by only 8 of them. It also relies on `";"` chained commands in one argv, `=name` exact targets, trailing-`:` session targets, and `%N` pane ids.
- psmux v3.4.0 (2026-10-13, MIT) supports every subcommand and flag claudemux uses except `capture-pane -q/-N` and `display-message -a/-c`. Neither is used. Inside panes it exports `TMUX` and `TMUX_PANE=%N`. **Open issue #845: in 3.4.0, arrow keys never reach Claude Code; 3.3.8 works.** Also open: #597 (SGR mouse not forwarded) and #578 (Agent View clears `TMUX`). Its client/server transport is loopback TCP. UNVERIFIED: whether psmux accepts the multi-argv command form and `-e` on `respawn-pane`. Task 4.1 checks this before anything depends on it.
- Claude Code on Windows has the binary at `%USERPROFILE%\.local\bin\claude.exe` and config at `%USERPROFILE%\.claude`. Git for Windows is optional. Shell-form hooks run via Git Bash, or PowerShell when it is absent. **Exec-form hooks (`command` + `args`) run with no shell and need a real `.exe`.** A failing PreToolUse hook under PowerShell may block the tool call. Payload paths use backslashes; UNVERIFIED for `cwd` and `transcript_path`. Project dirs are named by "every non-alphanumeric byte becomes `-`" (`C:\Users\x\proj` becomes `C--Users-x-proj`), which matches `headrecord.go:116 claudeProjectDirName` and **not** `session.go:14 encodeProjectPath`.
- On Windows, `pane_current_command` will report `claude.exe`/`node.exe`. A running `.exe` cannot be overwritten or deleted, but it can be renamed (the minio/selfupdate pattern). `os.UserHomeDir()` returns `USERPROFILE` and ignores `HOME`. That breaks 17 test overrides across 8 files.
- Existing bugs this plan fixes along the way:
  - `bin/claudemux-tab-color` is never shipped (missing from `release.yml` and `install.sh`).
  - Lost-session restore is dead on Linux, because `sysctl -n kern.boottime` is macOS-only.
  - The two project-dir encoders disagree.

## Global Constraints

- Builds with `CGO_ENABLED=0` for `darwin/{amd64,arm64}`, `linux/{amd64,arm64}` and `windows/{amd64,arm64}`.
- No behavior change on macOS or Linux in Phases 0–2. The existing test suite is the regression gate.
- Never add a runtime dependency on bash, sh, jq, sed or awk. Allowed external tools: `tmux`/`psmux`, `git`, `claude`, plus optional `op`, `zoxide` and `tailscale`, each resolved with `exec.LookPath`.
- Minimum multiplexer versions: tmux 3.2 (`remain-on-exit failed`, pane options), psmux 3.3.8. psmux 3.4.0 gets a warning until #845 is fixed.
- Every `.ps1` file is pure ASCII: no em-dashes, curly quotes or ellipses, in comments or strings. Check with `grep -P '[^\x00-\x7F]' install.ps1` (must print nothing).
- PRs are opened as drafts. Every phase lands as its own PR.
- Hook commands keep the contract from PR #23: stdout and exit codes are byte-identical, and the exit code is never 2.

## Review Focus

1. **A path with a space in it** (`C:\Users\First Last\`, `/Users/a b/`): every hook, statusLine, pane command and launcher re-invocation must survive it. Tasks 1.4 and 2.3 carry tests with a space-and-quote path.
2. **Windows payload paths with backslashes and a drive letter**: slugging, worktree detection and session-record names must give the same answer as Claude Code's own encoding. Task 1.3's table includes `C:\Users\x\proj\.claude\worktrees\fix-it`.
3. **Replacing the installed binary while it runs** (a statusline render or hook mid-flight): on Windows `hook ensure` must not fail or drop registration. Task 1.4 pins the rename-aside path.
4. **psmux 3.4.0 installed**: the user gets a clear warning naming #845, not a silently dead Claude Code. Task 4.2.
5. **A user's existing settings.json from the shell-form era**: migration to exec form must not duplicate entries or touch other tools' hooks. Task 1.5 reuses PR #23's legacy-settings fixture.

---

## Phase 0: Gate (prerequisite)

### Task 0.1: Land PR #23 and add cross-platform CI

PR #23 (hooks as Go subcommands) must be merged first; everything below assumes `hookrun.go` and `claudemuxHooks`.

**Files:**
- Create: `.github/workflows/ci.yml`

- [ ] **Step 1:** Write `ci.yml`, triggered on `pull_request` and on push to `main`.
  - Matrix `os: [macos-latest, ubuntu-latest, windows-latest]`, `go-version-file: go.mod`.
  - Jobs:
    - `go vet ./...`
    - `go build ./...`
    - `go test ./...` on macOS and Linux only for now. Windows is added in Task 0.2.
    - For each `GOOS/GOARCH` in Global Constraints: `GOOS=$os GOARCH=$arch go build -o /dev/null ./cmd/claudemux-head`, marked `continue-on-error: true` for `windows/*` until Task 1.1.
- [ ] **Step 2:** Push the branch. Expected: macOS and Linux are green, and the Windows build is red with exactly the two `usage.go` errors.
- [ ] **Step 3:** Commit: `ci: build and test on macOS, Linux and Windows`.

### Task 0.2: Make the test suite portable

**Files:**
- Create: `cmd/claudemux-head/testhelpers_test.go`
- Modify: the 8 test files with `t.Setenv("HOME", …)`; `env_test.go`; `usage_test.go`; `hook_test.go`; `launchertargets_test.go`; `worktree_test.go`

**Interfaces:**
- Produces:
  - `setTestHome(t *testing.T, dir string)`: sets `HOME` and `USERPROFILE`.
  - `fakeExecutable(t *testing.T, dir, name, script string) string`. On Unix it writes a `#!/bin/sh` script. On Windows it `go build`s `testdata/fakeexe/main.go`, a tiny program that echoes its argv, copies stdin to stdout, and exits with `FAKE_EXIT`, into `name.exe`. It returns the path.

- [ ] **Step 1:** Replace every `t.Setenv("HOME", x)` with `setTestHome(t, x)`.
- [ ] **Step 2:** Move the `syscall.Mkfifo` tests into `env_fifo_test.go` with `//go:build unix`.
- [ ] **Step 3:** Convert the `#!/bin/sh` fakes in `usage_test.go`, `hook_test.go` and `launchertargets_test.go` to `fakeExecutable`. Replace `exec.Command("mkdir","-p")` in `worktree_test.go` with `os.MkdirAll`. Tests that exec `bash bin/claudemux` get `if runtime.GOOS == "windows" { t.Skip("bash launcher; replaced in Phase 3") }`.
- [ ] **Step 4:** Run `GOOS=windows go test -c -o /dev/null ./cmd/claudemux-head`. Expected: it fails only on the two `usage.go` errors, meaning there are no test-file errors left.
- [ ] **Step 5:** Run `go test ./...` on macOS. Expected: PASS, with the same number of tests run as before the change.
- [ ] **Step 6:** Commit: `test: portable HOME, fake executables and fifo tests`.

## Phase 1: Portability seams (no macOS/Linux behavior change; Windows compiles)

### Task 1.1: Process control per OS

**Files:**
- Create: `cmd/claudemux-head/proc_unix.go` (`//go:build unix`), `proc_windows.go`, `proc_test.go`
- Modify: `usage.go:80-92`, `boot.go:266-275`, `restart.go:40-45`

**Interfaces:**
- Produces:
  - `startInOwnGroup(cmd *exec.Cmd) (kill func() error, err error)`.
    - Unix: `Setpgid` + `Kill(-pid, SIGKILL)`, which is today's behavior.
    - Windows: a Job Object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`; `kill` calls `TerminateJobObject`.
  - `becomeProcess(path string, argv []string, env []string) error`.
    - Unix: `syscall.Exec`.
    - Windows: start the child with inherited stdio, forward Ctrl-C, wait, then `os.Exit(child exit code)`. It never returns on success on either OS.

- [ ] **Step 1:** Write `TestStartInOwnGroupKillsGrandchildren`. A fake that spawns a sleeping grandchild is killed via `kill()`, and within 2 s the grandchild is gone. Check this with `os.FindProcess` + `Signal(0)` on Unix, and via the Job Object's process-ID list on Windows.
- [ ] **Step 2:** Write `TestBecomeProcessExitCode`. Using the re-exec-test-binary pattern (`TestMain` with a `BECOME_HELPER` env), the helper calls `becomeProcess(fake, ["fake"], env)` with `FAKE_EXIT=7`. Assert that the parent sees exit 7 and the fake's stdout.
- [ ] **Step 3:** Implement both files. Replace the three call sites.
- [ ] **Step 4:** Run `go test ./cmd/claudemux-head -run 'StartInOwnGroup|BecomeProcess'` on macOS. Then run `GOOS=windows GOARCH=amd64 go build ./cmd/claudemux-head`. Expected: PASS, and a clean Windows build.
- [ ] **Step 5:** In `ci.yml`, drop `continue-on-error` for Windows builds and add `go test ./...` on windows-latest. Push. Expected: green on all three, except the tmux-dependent tests skipped in Task 1.2.
- [ ] **Step 6:** Commit: `head: per-OS process group and exec`.

### Task 1.2: One multiplexer runner

**Files:**
- Create: `cmd/claudemux-head/mux.go`, `mux_test.go`
- Modify: every non-test `exec.Command*(…, "tmux", …)` site: about 46 sites in 17 files (list them with `grep -n '"tmux"' cmd/claudemux-head/*.go | grep -v _test`). Fold in `swTmux` (`switchboardtui.go:508`).

**Interfaces:**
- Produces:
  - `muxBinary() string`. Memoized. Resolution order:
    - `$CLAUDEMUX_TMUX` first, on every OS.
    - Unix: then `tmux` on PATH.
    - Windows: then `psmux` on PATH, and **never a bare `tmux`**. psmux installs a `tmux.exe` alias, but an MSYS2/Cygwin tmux or a stray shim earlier on PATH would answer instead and talk to a different server that has none of our sessions. `tmux -V` cannot tell them apart, because psmux reports a tmux-shaped version; `psmux -V` reports `psmux X.Y.Z`.
  - `muxCmd(ctx context.Context, args ...string) *exec.Cmd`. All call sites use this, so the arg builders (`xxxArgs()`) stay untouched.
  - `muxOutput(ctx context.Context, args ...string) ([]byte, error)`. This replaces `swTmux`.

- [ ] **Step 1:** Write `TestMuxBinaryHonorsOverride` (env set → returned verbatim) and `TestMuxBinaryPrefersPsmuxOverTmuxOnWindows` (skip unless windows; PATH holds a fake `tmux.exe` in an earlier directory and a fake `psmux.exe` in a later one → the psmux path).
- [ ] **Step 2:** Write `TestNoDirectTmuxExec`. Parse every non-test `.go` file in the package with `go/parser` and fail on any `exec.Command`/`exec.CommandContext` call whose first non-ctx argument is the literal `"tmux"`. This is the guard that keeps the seam.
- [ ] **Step 3:** Implement `mux.go` and convert the call sites mechanically.
- [ ] **Step 4:** Run `go test ./...`. Expected: PASS, and `TestNoDirectTmuxExec` passes.
- [ ] **Step 5:** Commit: `head: route every multiplexer call through muxCmd`.

### Task 1.3: Paths and names as Claude Code encodes them

**Files:**
- Create: `cmd/claudemux-head/claudepaths.go`, `claudepaths_test.go`
- Modify:
  - `session.go:14`: delete `encodeProjectPath`; call sites use `claudeProjectDirName`.
  - `headrecord.go:116`: move `claudeProjectDirName` here.
  - `tui.go:2755-2785`: `worktreeName`/`worktreeNameFromCwd`.
  - `hookrun.go`: the worktree marker check.
  - `sessionrecord.go:75`
  - `panemap.go:60-69`, `switchboard.go:146-147`: the `claude`/`node` comparisons.
  - `claudereboot.go:56`

**Interfaces:**
- Produces:
  - `claudeProjectDirName(absPath string) string`: every byte outside `[A-Za-z0-9]` becomes `-`.
  - `worktreeNameFromPath(p string) (string, bool)`: matches `/.claude/worktrees/<name>` or `\.claude\worktrees\<name>` and returns `<name>` up to the next separator of either kind.
  - `safeFileName(s string) string`: replaces `/ \ : * ? " < > |` and control bytes with `-`.
  - `exeBase(p string) string`: `filepath.Base` with a trailing `.exe`, any case, removed.

- [ ] **Step 1:** Write `TestClaudeProjectDirName`, a table test. Each case must equal the slug Claude Code writes on disk:
  - `/Users/x/proj` → `-Users-x-proj`
  - `/Users/x/my.proj` → `-Users-x-my-proj`
  - `C:\Users\x\proj` → `C--Users-x-proj`
  - `C:\Users\x\proj\.claude\worktrees\fix-it` → `C--Users-x-proj--claude-worktrees-fix-it`
- [ ] **Step 2:** Write `TestWorktreeNameFromPath`, `TestSafeFileName` (`a/b\c:d` → `a-b-c-d`) and `TestExeBase` (`C:\x\claude.EXE` → `claude`, `/usr/bin/node` → `node`).
- [ ] **Step 3:** Before changing `session.go`, check the encoder by hand against a real `~/.claude/projects` on macOS: for each project dir whose source path is known, compare its name to `claudeProjectDirName(path)`. Do **not** commit those names; the repo is public. Add any shape that differs from `encodeProjectPath`'s old output (e.g. a dotted directory name) to Step 1's table with a synthetic path, and record it in the commit message. That difference is the bug being fixed.
- [ ] **Step 4:** Implement, then replace every call site. Run `go test ./...`. Expected: PASS.
- [ ] **Step 5:** Commit: `head: one encoder for Claude project dirs; separator-agnostic worktree and exe names`.

### Task 1.4: Installing and invoking the binary on every OS

**Files:**
- Modify: `hook.go` (`hookBinaryName`, `writeExecutableAtomic`, `legacyShim`); `writeAtomic` itself (`skills.go:50`) is unchanged
- Create: `replace_windows.go`, `replace_unix.go`

**Interfaces:**
- Produces:
  - `hookBinaryName` becomes `"claudemux-head" + exeSuffix`, where `exeSuffix` is `".exe"` on Windows and `""` elsewhere.
  - `replaceExecutable(path string, blob []byte) error`.
    - Unix: today's temp-file and rename.
    - Windows: if `path` exists, rename it to `path + ".old-<unixnano>"`, write the new file, then make a best-effort delete of every `*.old-*` beside it (it fails silently while that file is still running).

- [ ] **Step 1:** Write `TestReplaceExecutableWhileOpen`. Hold `path` open (a stand-in for "running"), call `replaceExecutable`, and assert the new bytes are at `path` and the open handle still reads the old bytes. On Windows, additionally assert that one `.old-*` exists and that a second call cleans up the first once the handle is closed.
- [ ] **Step 2:** Implement, then route `writeExecutableAtomic` through it.
- [ ] **Step 3:** Run `go test ./... -run 'ReplaceExecutable|HookEnsure'`. Expected: PASS.
- [ ] **Step 4:** Commit: `head: replace a running binary safely on Windows`.

### Task 1.5: Exec-form hook and statusLine registration

**Files:**
- Modify: `hook.go` (`addHookEntries`, `hasHookCommand`, `removeHookEntries`, `setStatusLine`, `hookCommandFor`), `hook_test.go`, `README.md` (the pane-map hook section)

**Interfaces:**
- Consumes: `claudemuxHooks` and `hookBinaryName` (Task 1.4).
- Produces: hook entries shaped `{"type":"command","command":"<bin>","args":["hook","<name>"]}`. An entry is ours when `command == bin` and `args` equals `["hook", name]`. The PR #23 shell-form entry (`"<bin> hook <name>"`, no `args`) counts as legacy: it is removed like the `.sh` entries.

- [ ] **Step 0 (spike, blocks the rest):** Confirm against the current Claude Code docs (`code.claude.com/docs/en/hooks`, `/statusline`) that exec form is supported for hooks and for `statusLine`, and the minimum Claude Code version. If `statusLine` has no exec form, keep it as a shell command with the path quoted for the platform's shell: POSIX single quotes on Unix, and forward slashes plus double quotes on Windows Git Bash/PowerShell. Record the result in the task's commit message.
- [ ] **Step 1:** Write `TestHookEnsureRegistersExecForm`. On a fresh install every entry has `args` and a `command` naming the installed binary.
- [ ] **Step 2:** Write `TestHookEnsureMigratesShellFormEntries`. Seed PR #23's legacy fixture plus shell-form `"<bin> hook map"` entries. After ensure, only exec-form entries remain, with no duplicates, other tools' hooks untouched, and the second run a no-op.
- [ ] **Step 3:** Write `TestHookEnsureSurvivesSpaceInHome`. With HOME at `…/a b'c/`, run ensure, then for each registered entry run `exec.Command(entry.command, entry.args...)` with a UserPromptSubmit payload. Expect exit 0, and stdout empty except for the worktree hook.
- [ ] **Step 4:** Implement. Run `go test ./...`. Expected: PASS.
- [ ] **Step 5:** Commit: `head: register hooks in exec form`.

### Task 1.6: Boot time per OS

**Files:**
- Create: `boottime_darwin.go` (today's `sysctl kern.boottime` + `parseBootTime`), `boottime_linux.go` (`/proc/stat` `btime`), `boottime_windows.go` (`time.Now().Add(-windows.DurationSinceBoot())`), `boottime_test.go`
- Modify: `swrestore.go:120-135`

**Interfaces:**
- Produces: `bootTime() (time.Time, error)`.

- [ ] **Step 1:** Write `TestBootTimeIsPlausible`: no error, earlier than now, later than now minus 365 days. Also write `TestParseProcStatBtime` (fixture `btime 1729500000` → `time.Unix(1729500000,0)`; a missing line → error).
- [ ] **Step 2:** Implement, and replace the `sysctl` call in `swrestore.go`.
- [ ] **Step 3:** Run `go test ./... -run 'BootTime|ProcStat|Restore'` on macOS, and in CI on Linux and Windows. Expected: PASS on all three. Lost-session restore now works on Linux.
- [ ] **Step 4:** Commit: `head: boot time on Linux and Windows (fixes restore on Linux)`.

## Phase 2: Remove the remaining shell from the Go side

### Task 2.1: Project colour resolver in Go

**Files:**
- Create: `projectstyle.go`, `projectstyle_test.go`
- Modify: `tabreset.go:240-250` (`projectStyleFor`), `statepub.go:209`
- Delete (in Phase 3, once the bash launcher is gone): `bin/project-color-resolve.sh`

**Interfaces:**
- Produces: `resolveProjectStyle(dir string) (bg, fg string, ok bool)`, with the same semantics as `resolve_project_style` in the script:
  - `.claudemux.yml` beats `.project.yml`.
  - The nearest ancestor wins.
  - A dark background gives `fg=white`; a light one gives `fg=black`.

- [ ] **Step 1:** Port the existing `TestResolveProjectStyle*` cases (`tabreset_test.go:36-127`) to call `resolveProjectStyle` directly. While the script still exists, keep one differential test, `TestResolveProjectStyleMatchesScript`, which skips on Windows or without bash. It runs both over the same fixtures and compares.
- [ ] **Step 2:** Implement, and switch `projectStyleFor` to it. Run `go test ./...`. Expected: PASS.
- [ ] **Step 3:** Commit: `head: resolve project colours in Go`.

### Task 2.2: Tab colour as a subcommand, per terminal

**Files:**
- Create: `tabcolor.go`, `tabcolor_test.go`
- Modify: `tabreset.go:186-200,296` (the OSC writer), `main.go` (dispatch `tab-color`; add it to `knownSubcommands`)

**Interfaces:**
- Produces:
  - `claudemux-head tab-color <client_tty> <session_path>`. It replaces `bin/claudemux-tab-color`, which currently never ships.
  - `tabColorSequence(term terminalKind, bg string) []byte`.
    - `termITerm2` gives today's OSC 6 bytes; `TestItermTabColorBytes` stays as the pin.
    - `termWindowsTerminal` and `termOther` give `nil`.
  - `detectTerminal(env func(string) string) terminalKind`.
    - `TERM_PROGRAM=iTerm.app` → `termITerm2`.
    - `WT_SESSION` set → `termWindowsTerminal`.
    - Otherwise `termOther`.

- [ ] **Step 1:** Write `TestDetectTerminal` (the three env cases) and `TestTabColorSequenceOnlyForITerm2`.
- [ ] **Step 2:** Implement. The tty write stays Unix-only (`os.OpenFile(tty)`). On Windows `tab-color` is a no-op that exits 0.
- [ ] **Step 3:** Run `go test ./...`. Expected: PASS.
- [ ] **Step 4:** Commit: `head: tab colour subcommand; per-terminal sequences`.

### Task 2.3: Pane commands as argv, not shell strings

**Files:**
- Create: `panecmd.go`, `panecmd_test.go`
- Modify:
  - `claudereboot.go:98` (`respawn-pane "exec <cmd> --resume id"`)
  - `claudereboot.go:34,131` (re-parsing `pane_start_command`)
  - `banner.go:169-207` (`swShellQuote`, `display-popup -E`)
  - `shellsplit.go:57`
  - `switchboard` lobby creation, if the head creates it

**Interfaces:**
- Produces:
  - `type paneCommand struct{ Dir string; Env []string; Argv []string }`.
  - `(p paneCommand) muxArgs() []string`. It emits `-c Dir`, `-e K=V` for each env entry, then the Argv elements as separate arguments, for appending to `new-session`/`split-window`/`respawn-pane`/`display-popup`.
  - The restart command (`@claudemux_claude_cmd`) is stored as a JSON array of argv, with `Dir`, instead of a shell string.
    - `claudereboot` reads it back with `json.Unmarshal`.
    - For one release, a value that is not JSON is still parsed by the old `shellSplit` path, so sessions launched by the old launcher can still be restarted.

- [ ] **Step 1:** Write `TestPaneCommandMuxArgs` (Dir and two env entries produce `["-c", dir, "-e", "A=1", "-e", "B=2", "claude", "--resume", "id"]`) and `TestPaneCommandSurvivesSpacesAndQuotes` (Argv `["/a b/claude", "-n", "it's"]` round-trips through JSON unchanged).
- [ ] **Step 2:** Write `TestClaudeRebootReadsLegacyShellString`. Today's stored form (`claudereboot_test.go:25`) still produces the same argv.
- [ ] **Step 3:** Implement, then convert each site.
- [ ] **Step 4:** Integration check on macOS with real tmux:
  - Run `go test ./... -run Integration -tags integration` with a new `integration_test.go`, guarded by `exec.LookPath(muxBinary())`.
  - It creates a detached session whose command is a `fakeExecutable` that echoes `$PWD` and an env var, captures the pane and asserts both, then kills the session.
  - Expected: PASS.
- [ ] **Step 5:** Commit: `head: start panes from argv, never a shell string`.

## Phase 3: The launcher in Go (expand into its own detailed plan before starting)

The bash launcher stays the shipped launcher until Task 3.4 proves parity. Its sections (line ranges from the 2026-10-21 inventory) map onto these tasks:

| Task | Ports (bin/claudemux lines) | Produces |
|---|---|---|
| **3.1** CLI surface and dispatch | 57–90 getopts `ndwWr:N:C:` and the resume-id check; 1205–1273 reserved words (`switch`, `setup`, head-subcommand passthrough/refusal) and the per-arg loop | `main()` branch: `exeBase(os.Args[0]) == "claudemux"` → `runLauncher(args []string, env launcherEnv) int`. Flags and exit codes identical. `launchertargets_test.go` is rewritten to call `runLauncher`, and its existing cases become the acceptance tests. |
| **3.2** Resolution and config | 152–222 `resolve_dir` (literal, `zoxide query`, `project find` in-process); 303–533 project fields (via `yaml.v3`, replacing the sed reader), `config get` in-process, worktree heuristics (git), shell size and command, op account | Pure functions, each with a table test copied from the bash behavior. `auto_worktree_wanted` keeps its git ≥ 2.31 degrade path. |
| **3.3** Session creation | 640–1134: `tmux_new_session` (from HOME), layouts (shell-right / no-shell / shell-bottom / head-bottom), head-row hook, titles, `record_parent`, op_env `inject_op_env` and `deferred_launch` (now a goroutine plus a detached `claudemux-head boot`, instead of bash `&`), the shell-pane wrapper (now `paneCommand{Argv: [defaultShell, "-c"|"-Command", cmd]}` per shell), and `attach` / `switch-client` | Every tmux call through `muxCmd`, every pane from `paneCommand`. Integration test (tag `integration`): each layout produces the expected pane count and order and the expected `@claudemux_*` options. |
| **3.4** Cutover | `switchboardtui.go:546` and `swrestore.go:205` call `runLauncher` in-process, or the `claudemux` copy, instead of the bash script on PATH. Ship `claudemux` as a copy of the binary (Windows) or a symlink (Unix). Delete `bin/claudemux`, `bin/project-color-resolve.sh` and `bin/claudemux-tab-color`. Update `install.sh`, `release.yml`, `RELEASING.md` (formula `libexec.install` list), and the README dev-layout notes (the launcher is no longer "live from the repo"; dev installs need `go install` plus a `claudemux` symlink) | A parity checklist run by hand on macOS and Linux: every flag, every layout, op_env, restore, switchboard, `c` restart. Each item is recorded in the PR. |

## Phase 4: Windows bring-up on psmux (expand into its own plan)

| Task | Content | Done when |
|---|---|---|
| **4.1** psmux compatibility spike | On a Windows VM (or the windows-latest runner via `winget install psmux` / `scoop install psmux`), run the Phase 2/3 integration tests against psmux 3.3.8. Verify the UNVERIFIED items: argv-form commands; `-e` on `respawn-pane`/`display-popup`; `;` chaining; `=name` and trailing-`:` targets; `#{session_name}`/`#{client_tty}`/`#{start_time}`; pane user options (`set -p @x`); `remain-on-exit failed`; the `window-resized` / `client-session-changed` hooks | Every gap is written up with a minimal repro, then worked around in `mux.go` (e.g. a `muxQuirks` struct keyed on `muxBinary()` + version) or filed upstream at psmux/psmux |
| **4.2** Version and identity gate | `muxVersion() (name string, major, minor, patch int, err error)` from `<muxBinary()> -V`. The launcher refuses tmux < 3.2 and psmux < 3.3.8, and warns on psmux 3.4.0 naming issue #845 until a fixed release is known. On Windows it also refuses when the resolved binary does not identify as psmux (e.g. `$CLAUDEMUX_TMUX` pointing at an MSYS2 tmux), with a message naming the path it found. The code never parses `$TMUX`; psmux documents its first field as not a socket path | Table test over sample `-V` strings, including `tmux 3.4` from an MSYS2 build on Windows → refused |
| **4.3** Claude Code specifics | `claude.exe` resolution (`%USERPROFILE%\.local\bin`). `exeBase` everywhere `pane_current_command` is compared. Confirm that hook payload `cwd`/`transcript_path` arrive with backslashes, and fixture one. `--permission-mode auto`, `-n`, `/color` | A recorded Windows payload in `testdata/` drives the map, ask and worktree hook tests |
| **4.4** Windows smoke in CI | A `windows-latest` job installs psmux (pinned) and runs `go test -tags integration ./...` | Green |
| **4.5** Manual acceptance (Bespoke Stairlifts pilot) | Windows Terminal + psmux + native Claude Code: launch, all layouts, switchboard, ask/permission states, restart, worktree flow, op_env (with `op.exe`), web page via Tailscale for Windows | Checklist signed off by a pilot user |

## Phase 5: Packaging and docs (expand into its own plan)

| Task | Content |
|---|---|
| **5.1** Release matrix | `release.yml` adds `windows/{amd64,arm64}` with `CGO_ENABLED=0`. Each archive is a zip with `claudemux-head.exe`, `claudemux.exe` (a copy), LICENSE and README. SHA256SUMS covers the zips. |
| **5.2** `install.ps1` | Pure ASCII. Downloads the zip for `$env:PROCESSOR_ARCHITECTURE`, verifies SHA256 against SHA256SUMS (refusing on mismatch, like `install.sh`), unpacks to `$env:LOCALAPPDATA\Programs\claudemux`, offers to add that to the user PATH, runs `claudemux-head.exe hook ensure`, and checks for psmux (suggesting `winget install psmux` if missing). `install.sh`'s Windows message points at it. |
| **5.3** Scoop bucket | New repo `mquinnv/scoop-bucket` with `claudemux.json` (autoupdate from GitHub releases), `depends: psmux` from the extras bucket, and `post_install` running `hook ensure`. RELEASING.md gets a bump step. winget is deferred. |
| **5.4** Docs | README: a "Windows (native)" section next to the existing WSL2 section, a dependencies table per OS (psmux replaces tmux; bash/jq not needed anywhere), and the known psmux caveats (#845, #597). Remove "Windows is not supported" from `install.sh`. |

---

## Not in scope

- A zellij or WezTerm backend. `mux.go` is the seam if one is ever wanted.
- Tab colours on Windows Terminal.
- winget submission.
- Native Windows without psmux.
