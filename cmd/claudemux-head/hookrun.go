package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The three Claude Code hooks claudemux registers, run as `claudemux-head hook
// <name>`. They were bash scripts (hooks/claudemux-{map,ask,worktree}.sh) that
// spawned jq, find and mv on every prompt; these are ports with the same
// stdout, exit codes and files for every well-formed payload. The scripts'
// history lives in their commit log and in the comments below, which carry
// over what each one explained.
//
// Every hook here reads one JSON payload on stdin. Exit codes follow the
// scripts: 0 on every path that ends normally, including the silent ones, and
// 1 when a write fails (bash's `set -e`). Never 2 — Claude Code treats exit 2
// as "block this prompt / tool call", which no failure of ours should cause.
// runHookCommand turns an unknown hook name or a panic into 1 as well; only an
// unrecoverable runtime fatal (out of memory, a concurrent map write) can
// still exit 2.

// hookStaleAge is how long a pane-map file, ask marker or worktree-ask counter
// may go unwritten before a hook run prunes it. The scripts used
// `find -mtime +7`, which on macOS (BSD find rounds the age UP to whole days)
// means older than seven days.
const hookStaleAge = 7 * 24 * time.Hour

// hookRunners maps `claudemux-head hook <name>` to its implementation.
var hookRunners = map[string]func(stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int{
	"map":      runHookMap,
	"ask":      runHookAsk,
	"worktree": runHookWorktree,
}

// runHookCommand implements `claudemux-head hook ...`; args are the words
// after `hook`. `ensure` is the installer (hook.go); every other name is a
// Claude Code hook from hookRunners, run with the event payload on stdin.
//
// Only a bare `hook` is a usage error with exit 2 — Claude Code never runs
// that. A name this binary does not know is exit 1: settings.json written by
// a newer claudemux can name a hook an older copy of this binary lacks, and
// exit 2 there would block every prompt. A panic in a runner is caught for
// the same reason; left alone, the Go runtime exits 2.
func runHookCommand(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) (rc int) {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: claudemux-head hook ensure | claudemux-head hook map|ask|worktree < payload")
		return 2
	}
	name := args[0]
	if name == "ensure" {
		return runHookEnsure(args[1:], stdout, stderr)
	}
	run, ok := hookRunners[name]
	if !ok {
		fmt.Fprintf(stderr, "claudemux hook: unknown hook %q (this binary may be older than settings.json)\n", name)
		return 1
	}
	defer func() {
		if v := recover(); v != nil {
			fmt.Fprintf(stderr, "claudemux hook %s: internal error: %v\n", name, v)
			rc = 1
		}
	}()
	return run(stdin, stdout, stderr, getenv)
}

// claudemuxStateDir is ~/.claude/claudemux, where every hook keeps its files.
func claudemuxStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "claudemux"), nil
}

// decodeHookPayload parses the first JSON value on stdin as an object, the way
// `jq` reading the payload did: a second value or trailing garbage after the
// first is ignored (jq printed the first result before failing, and the
// scripts used it), while anything that is not a JSON object fails, as a
// field lookup on it did in jq. A JSON null reads as an object with no
// fields, matching `null | .field` in jq.
func decodeHookPayload(stdin io.Reader) (map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(stdin)
	dec.UseNumber()
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, false
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return map[string]json.RawMessage{}, true
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false
	}
	return obj, true
}

// payloadText renders a payload field the way the scripts' `jq -r '.f // ""'`
// did: absent, null and false become "", a string is itself, and a number or
// true is its literal text. ok is false for an object or array — callers treat
// that as an unparseable payload. That matches the ask script, whose join()
// refused them, but not the worktree script, whose bare `jq -r '.cwd // empty'`
// printed one as JSON. The difference is kept deliberately: no real payload
// carries an object or array in these fields.
func payloadText(obj map[string]json.RawMessage, key string) (s string, ok bool) {
	raw, present := obj[key]
	if !present {
		return "", true
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	switch x := v.(type) {
	case nil:
		return "", true
	case bool:
		if x {
			return "true", true
		}
		return "", true
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	default:
		return "", false
	}
}

// safeSessionFilename reports whether a session_id can be used as a filename
// inside a hook's directory: nothing that could climb out of it, or hide.
// The scripts refused "/" and a leading "."; on Windows "\" is a separator
// too. Elsewhere it is an ordinary filename byte, and refusing it would
// change what the scripts accepted.
func safeSessionFilename(sid string) bool {
	seps := "/"
	if runtime.GOOS == "windows" {
		seps = `/\`
	}
	return sid != "" && !strings.ContainsAny(sid, seps) && !strings.HasPrefix(sid, ".")
}

// marshalJQ encodes v the way jq printed it: no HTML escaping of <, > and &
// (Go's default), and either compact (`jq -c`) or two-space indented (`jq -n`
// without -c), always followed by a newline.
func marshalJQ(v any, indent bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeHookFile writes blob to path through path.tmp.<pid> and a rename, the
// scripts' `> "$f.tmp.$$" && mv` — a reader never sees half a file. The tmp
// name matches the scripts' so pruneStale reaps a crashed run's leftovers the
// same way.
func writeHookFile(path string, blob []byte) error {
	tmp := path + ".tmp." + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, blob, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// pruneStale deletes the regular files in dir whose names satisfy match and
// that have not been written for hookStaleAge: the scripts' `find "$dir" -name ... -mtime +7 -delete`. Errors are ignored, as
// the scripts' `2>/dev/null || true` ignored them; a prune that fails costs
// disk, never the hook. Only dir's own entries are visited — the hooks never
// create subdirectories, so find's recursion had nothing to find.
func pruneStale(dir string, match func(name string) bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-hookStaleAge)
	for _, e := range entries {
		if !e.Type().IsRegular() || !match(e.Name()) {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// jsonFileName matches the scripts' `find -name '*.json*'`: the files
// themselves and any `.json.tmp.<pid>` a crashed run left behind.
func jsonFileName(name string) bool { return strings.Contains(name, ".json") }

// runHookMap implements `claudemux-head hook map` (SessionStart +
// UserPromptSubmit): record which session lives in which tmux pane so
// claudemux-head can follow its sibling pane's transcript. Keyed by
// $TMUX_PANE, inherited from the claude process.
//
// MUST stay silent on stdout: UserPromptSubmit stdout is injected into the
// model's context.
func runHookMap(stdin io.Reader, _, stderr io.Writer, getenv func(string) string) int {
	pane := getenv("TMUX_PANE")
	if pane == "" {
		return 0
	}
	state, err := claudemuxStateDir()
	if err != nil {
		fmt.Fprintf(stderr, "claudemux hook map: %v\n", err)
		return 1
	}
	dir := filepath.Join(state, "panes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(stderr, "claudemux hook map: %v\n", err)
		return 1
	}

	obj, ok := decodeHookPayload(stdin)
	if !ok {
		return 0
	}
	// Only write when transcript_path is a non-empty string.
	var transcript string
	if raw, present := obj["transcript_path"]; !present || json.Unmarshal(raw, &transcript) != nil || transcript == "" {
		return 0
	}

	// The three fields in this order, compact, a missing one as null: what
	// `jq -c '{session_id: .session_id, transcript_path: ..., cwd: .cwd}'`
	// wrote, and what panemap.go reads back.
	var out bytes.Buffer
	out.WriteByte('{')
	for i, key := range []string{"session_id", "transcript_path", "cwd"} {
		if i > 0 {
			out.WriteByte(',')
		}
		k, _ := marshalJQ(key, false)
		out.Write(bytes.TrimSuffix(k, []byte("\n")))
		out.WriteByte(':')
		out.Write(compactJQ(obj[key]))
	}
	out.WriteString("}\n")

	f := filepath.Join(dir, strings.TrimPrefix(pane, "%")+".json")
	if err := writeHookFile(f, out.Bytes()); err != nil {
		fmt.Fprintf(stderr, "claudemux hook map: %v\n", err)
		return 1
	}

	// Prune map files (and orphaned .json.tmp.<pid> files from a crashed run
	// between the write and the rename) for panes that haven't written in a
	// week: dead panes. Pane ids restart when the tmux server restarts, so
	// stale files can shadow.
	pruneStale(dir, jsonFileName)
	return 0
}

// compactJQ re-encodes one payload value compactly, as `jq -c` did: strings
// without HTML escaping, everything else with insignificant whitespace
// removed. An absent value is null.
func compactJQ(raw json.RawMessage) []byte {
	if raw == nil {
		return []byte("null")
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		b, _ := marshalJQ(s, false)
		return bytes.TrimSuffix(b, []byte("\n"))
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return []byte("null")
	}
	return buf.Bytes()
}

// runHookAsk implements `claudemux-head hook ask` (PreToolUse + PostToolUse +
// PermissionRequest + UserPromptSubmit): track pending AskUserQuestion calls
// so claudemux-head can tell "asking the human" apart from Idle/Thinking.
// Claude Code does NOT flush the assistant tool_use event to the transcript
// while a question is on screen — it lands only when the question is answered
// — so the transcript alone reads as Idle or Thinking for the whole time the
// session is blocked on the user. This hook is the only signal that fires at
// ask time.
//
// PreToolUse on AskUserQuestion writes a marker keyed by session_id;
// PostToolUse on AskUserQuestion (the answer arrived) removes it, and so does
// UserPromptSubmit (a new prompt means no question is pending — this covers
// an Esc'd question, where PostToolUse never fires). The head additionally
// ignores markers older than the transcript's newest conversation event, so a
// marker this hook failed to remove cannot wedge the state.
//
// hook.go registers PreToolUse/PostToolUse with a matcher on AskUserQuestion,
// so this process only spawns for that tool to begin with; the tool-name
// check below is defense in depth (e.g. a hand-edited settings.json), not the
// primary filter.
//
// PermissionRequest covers the other dialog that blocks on the human: "Do you
// want to proceed?" for any tool. There the tool_use IS already in the
// transcript and reads as a tool that is running. The event fires only when a
// dialog is about to open, so it needs no matcher. It writes a second marker,
// <session_id>.permit.json, naming the tool. Nothing here removes that marker
// when the dialog is answered — no hook fires on approval — so the head
// confirms against the claude pane that the dialog is still up (permit.go);
// the marker only tells it when to look. UserPromptSubmit clears it.
//
// MUST stay silent on stdout: UserPromptSubmit stdout is injected into the
// model's context, and JSON on PermissionRequest's stdout can answer the
// dialog.
func runHookAsk(stdin io.Reader, _, stderr io.Writer, getenv func(string) string) int {
	// No sibling pane can be reading this session's marker outside tmux — same
	// reasoning, and same position, as the pane-map hook.
	if getenv("TMUX_PANE") == "" {
		return 0
	}
	obj, ok := decodeHookPayload(stdin)
	if !ok {
		return 0
	}
	event, ok1 := payloadText(obj, "hook_event_name")
	tool, ok2 := payloadText(obj, "tool_name")
	sid, ok3 := payloadText(obj, "session_id")
	if !ok1 || !ok2 || !ok3 {
		return 0
	}
	// session_id becomes a filename; refuse anything that could escape the dir.
	if !safeSessionFilename(sid) {
		return 0
	}

	state, err := claudemuxStateDir()
	if err != nil {
		fmt.Fprintf(stderr, "claudemux hook ask: %v\n", err)
		return 1
	}
	dir := filepath.Join(state, "asking")
	marker := filepath.Join(dir, sid+".json")
	permit := filepath.Join(dir, sid+".permit.json")

	// writeMarker is the scripts' `jq -n --arg ... > tmp && mv`: an indented
	// object, never string interpolation, so a session_id holding a quote or
	// a backslash still lands as valid JSON.
	writeMarker := func(path string, v any) int {
		blob, err := marshalJQ(v, true)
		if err == nil {
			err = os.MkdirAll(dir, 0o755)
		}
		if err == nil {
			err = writeHookFile(path, blob)
		}
		if err != nil {
			fmt.Fprintf(stderr, "claudemux hook ask: %v\n", err)
			return 1
		}
		return 0
	}

	switch event {
	case "PreToolUse":
		if tool != "AskUserQuestion" {
			return 0
		}
		if rc := writeMarker(marker, struct {
			SessionID string `json:"session_id"`
		}{sid}); rc != 0 {
			return rc
		}
	case "PostToolUse":
		if tool != "AskUserQuestion" {
			return 0
		}
		os.Remove(marker)
	case "PermissionRequest":
		// AskUserQuestion has its own marker above; one dialog, one name.
		if tool == "AskUserQuestion" {
			return 0
		}
		if rc := writeMarker(permit, struct {
			SessionID string `json:"session_id"`
			ToolName  string `json:"tool_name"`
		}{sid, tool}); rc != 0 {
			return rc
		}
	case "UserPromptSubmit":
		os.Remove(marker)
		os.Remove(permit)
	}

	// Prune markers (and orphaned .tmp.<pid> files from a crashed run) for
	// sessions that stopped writing long ago, same policy as the pane map.
	pruneStale(dir, jsonFileName)
	return 0
}

// worktreeAskCap is how many times one session is told about its pending
// worktree. See runHookWorktree.
const worktreeAskCap = 2

// worktreeInstruction is what runHookWorktree injects into the model's
// context. The wording is deliberate on two points. "Do not enter one now"
// comes first because the model reads the tool name and reaches for it; the
// prohibition has to land before the tool does. And the trigger is "changes
// git state", not "edits a file": a new plan document or a commit is a change
// too, and a gitignored scratch file is not.
const worktreeInstruction = `<claudemux-worktree>
This session is configured to work in a git worktree, and is not in one yet.
Do not enter one now. Investigate, read, search, review, and answer in the
current checkout. Call EnterWorktree only immediately before your first action
that changes this repository's git state (editing a tracked file, adding a new
file such as a plan or design document, committing) -- and if the task never
changes anything, never call it. When you do, derive ` + "`name`" + ` from the user's
request: 2-5 words, lowercase, dash-separated, naming the durable goal rather
than the first step (e.g. "rename-worktrees-on-topic"). Use only letters,
digits, dots, underscores and dashes, up to 64 characters total.
</claudemux-worktree>
`

// runHookWorktree implements `claudemux-head hook worktree` (UserPromptSubmit):
// ask the model to create a worktree named after the task, for sessions
// bin/claudemux marked as wanting one — and to create it no sooner than its
// first change.
//
// claudemux no longer passes `claude --worktree` — that named the worktree
// during startup, before any prompt existed to name it from, which is how
// every worktree ended up called "lovely-wandering-lovelace". Instead the
// launcher exports CLAUDEMUX_WORKTREE_PENDING and this hook asks the model to
// make the worktree itself, when the task IS known.
//
// WHEN the model makes it matters as much as what it is called. The first
// version of this instruction said "before any other tool call", and the
// model obeyed: every session got a worktree on its first response,
// including the many that only ever read code, answered a question, or
// investigated a bug and changed nothing. Those worktrees were pure cost — a
// branch and a directory to clean up afterwards. So the instruction defers
// the call to the moment right before the first change that touches git
// state. Investigation happens in the current checkout; a session that never
// changes anything never gets a worktree.
//
// UNLIKE the map and ask hooks, this one SPEAKS on stdout: UserPromptSubmit
// stdout is injected into the model's context, which is the whole delivery
// mechanism. Every path that is not the one asking must still print nothing.
func runHookWorktree(stdin io.Reader, stdout, _ io.Writer, getenv func(string) string) int {
	// No marker: this session was never promised a worktree (-W, a feature
	// branch, a non-repo). Nothing to say, on this or any later prompt.
	if getenv("CLAUDEMUX_WORKTREE_PENDING") == "" {
		return 0
	}
	obj, ok := decodeHookPayload(stdin)
	if !ok {
		return 0
	}
	cwd, ok := payloadText(obj, "cwd")
	if !ok || cwd == "" {
		return 0
	}

	// Already inside a worktree: the model made one on an earlier prompt (or
	// the user entered one by hand). This is the cheap exit, and it covers the
	// whole success path with no bookkeeping at all. On Windows the same path
	// arrives with backslashes.
	if strings.Contains(cwd, "/.claude/worktrees/") ||
		(runtime.GOOS == "windows" && strings.Contains(cwd, `\.claude\worktrees\`)) {
		return 0
	}

	// The cwd check above only ends the injection when EnterWorktree
	// SUCCEEDS. Every other path leaves the cwd where it was: the session is
	// still reading and has not needed a worktree yet, the user answers "no,
	// just work here", the tool refuses on a dirty tree, the model declines.
	// Without a cap, this hook would re-inject the same instruction on every
	// prompt for the rest of the session — nagging that the old
	// `claude --worktree` never did, because a rejected flag failed once,
	// loudly, at launch.
	//
	// So: say it at most worktreeAskCap times per session. Two, not one,
	// because a single statement is easy to lose and the second is cheap. Two
	// is also enough: the instruction is a standing rule ("enter one right
	// before your first change"), not a request that is consumed by being
	// obeyed, and each injection stays in the transcript for the rest of the
	// session, so a session that investigates for many prompts before its
	// first edit still carries the rule when that edit comes. After two, the
	// model has the rule twice and the human has watched the session run
	// without a worktree; a third would only be noise.
	//
	// Keyed by session_id so a resumed or /clear'd session gets a fresh
	// budget — that is a new task, and it deserves a new worktree. When no
	// usable session_id is available there is nothing to key on, so fall
	// through and ask: losing the feature is worse than an occasional extra
	// ask. Every failure to count is likewise ignored, never fatal.
	sid, _ := payloadText(obj, "session_id")
	if state, err := claudemuxStateDir(); err == nil && safeSessionFilename(sid) {
		asksDir := filepath.Join(state, "worktree-asks")
		_ = os.MkdirAll(asksDir, 0o755)
		// Prune counters for sessions that ended days ago, the same way the
		// pane map prunes its own files — this directory must not grow
		// without bound.
		pruneStale(asksDir, func(string) bool { return true })

		counter := filepath.Join(asksDir, sid)
		asked := 0
		if info, err := os.Stat(counter); err == nil && info.Mode().IsRegular() {
			if b, err := os.ReadFile(counter); err == nil {
				// A corrupt counter must not disable the feature. Digits only,
				// as the script's `case` allowed: no sign, no spaces.
				s := strings.TrimRight(string(b), "\n")
				if s != "" && strings.Trim(s, "0123456789") == "" {
					if n, err := strconv.Atoi(s); err == nil {
						asked = n
					}
				}
			}
		}
		if asked >= worktreeAskCap {
			return 0
		}
		_ = os.WriteFile(counter, []byte(strconv.Itoa(asked+1)), 0o644)
	}

	io.WriteString(stdout, worktreeInstruction)
	return 0
}
