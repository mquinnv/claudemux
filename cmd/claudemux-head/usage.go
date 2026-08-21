package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// ModelWindow is one per-model weekly limit — "Fable", "Opus", "Sonnet" —
// from the usage endpoint's limits[] array.
type ModelWindow struct {
	Name        string
	UsedPercent int
	ResetsAt    time.Time
}

// PlanUsage is the subset of a get_usage response the meters render.
type PlanUsage struct {
	// Available mirrors rate_limits_available: false for API key, Bedrock,
	// Vertex, or a missing profile scope. It is a verdict on the CREDENTIALS
	// OF THE PROCESS THAT ASKED, not on the machine — see Fetched.
	Available bool
	Models    []ModelWindow
	FetchedAt time.Time
	// Fetched marks a value this process obtained from its own spawn, as
	// opposed to one it read out of the machine-global cache. It is in-memory
	// provenance only and is deliberately absent from rawUsageCache: nothing
	// read back off disk may ever claim to be ours.
	//
	// It exists because Available is credential-scoped. fetchPlanUsage
	// inherits the spawning pane's environment, so a pane with
	// CLAUDE_CODE_USE_BEDROCK=1 or ANTHROPIC_API_KEY set gets
	// rate_limits_available:false while the pane beside it, on the same
	// machine and the same account, gets true. Only the pane that actually
	// paid for the answer may act on an unavailable verdict; a pane reading
	// someone else's cached one must not.
	Fetched bool
}

// usageTimeout bounds one poll. The measured spawn is ~2.2s including the
// user's SessionStart hooks, so this leaves generous headroom while still
// guaranteeing the poll goroutine returns.
const usageTimeout = 20 * time.Second

// fetchPlanUsage spawns a short-lived Claude Code and asks it, over the SDK
// stdio control protocol, for the structured /usage data.
//
// Claude Code holds the OAuth credential, so we never touch the keychain: the
// endpoint behind this (GET /api/oauth/usage) is gated on the OAuth account and
// is unreachable with the platform API key the summarizer uses. The call runs
// no inference — the probe that established this returned total_cost_usd 0 and
// an empty model_usage — so it spends no tokens and none of the limit it
// reports.
func fetchPlanUsage(ctx context.Context, claudeBin string, now time.Time) (PlanUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, usageTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, claudeBin,
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--no-session-persistence",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
	)
	// TMUX_PANE must not reach the child: claudemux-map.sh keys its pane-map
	// file on it and exits immediately when it is absent. Without this, a poll
	// spawned from a head pane writes a phantom session into that pane's map
	// entry. Everything else in the environment is kept — the OAuth profile
	// resolution depends on HOME and CLAUDE_CONFIG_DIR.
	cmd.Env = filterEnv(os.Environ(), "TMUX_PANE")
	// Run the child in its own process group so a timeout can kill the whole
	// tree, not just the immediate child. Without this, a shelled-out claude
	// wrapper (or the fake claude our tests use) can fork a descendant that
	// keeps the stdout pipe open after the immediate child is killed, and the
	// caller waits out the descendant's full runtime instead of ctx's deadline.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// Safety net: if the group kill above doesn't fully land, force the I/O
	// pipes closed after a short grace period so Wait cannot block forever.
	cmd.WaitDelay = 2 * time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return PlanUsage{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return PlanUsage{}, err
	}
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return PlanUsage{}, err
	}
	// cancel() must run before Wait() on every return path, including a
	// successful parse: nothing else stops the child from lingering after it
	// has answered, and Wait() alone would just block until it exits on its
	// own. Calling cancel() first fires the exec package's ctx-cancellation
	// watchdog, which invokes cmd.Cancel above (the process-group SIGKILL),
	// so by the time Wait() runs here the child is already being torn down
	// instead of left to exit in its own time.
	defer func() {
		cancel()
		_ = cmd.Wait()
	}()

	// initialize must precede any other control request; get_usage follows
	// immediately, and the response is matched by request_id rather than by
	// arrival order because hook lifecycle events interleave freely.
	for _, req := range []string{
		`{"type":"control_request","request_id":"init-1","request":{"subtype":"initialize"}}`,
		`{"type":"control_request","request_id":"usage-1","request":{"subtype":"get_usage"}}`,
	} {
		if _, err := fmt.Fprintln(stdin, req); err != nil {
			stdin.Close()
			return PlanUsage{}, err
		}
	}
	// Nothing more is ever sent: close stdin now so a child that reads until
	// EOF before answering (real claude in stream-json mode, and the fake
	// claude our tests use) isn't left blocked waiting for input we'll never
	// send. Deferring this close would deadlock against such a child, since
	// the read loop below can't return until the child produces output or
	// exits, and the child can't exit until it sees stdin close.
	stdin.Close()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // responses embed a transcript scan
	for scanner.Scan() {
		var env struct {
			Type     string `json:"type"`
			Response struct {
				Subtype   string          `json:"subtype"`
				RequestID string          `json:"request_id"`
				Response  json.RawMessage `json:"response"`
				Error     string          `json:"error"`
			} `json:"response"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
			continue // hook events and other stream noise
		}
		if env.Type != "control_response" || env.Response.RequestID != "usage-1" {
			continue
		}
		if env.Response.Subtype != "success" {
			return PlanUsage{}, fmt.Errorf("get_usage failed: %s", env.Response.Error)
		}
		return parseUsageResponse(env.Response.Response, now)
	}
	if err := ctx.Err(); err != nil {
		return PlanUsage{}, err
	}
	if err := scanner.Err(); err != nil {
		return PlanUsage{}, err
	}
	return PlanUsage{}, errors.New("claude exited without answering get_usage")
}

// filterEnv returns environ minus any entry whose name is key.
func filterEnv(environ []string, key string) []string {
	out := environ[:0:0]
	prefix := key + "="
	for _, kv := range environ {
		if len(kv) >= len(prefix) && kv[:len(prefix)] == prefix {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// parseUsageResponse extracts the per-model weekly windows from a get_usage
// response body.
//
// The rows come from limits[], NOT from the named seven_day_opus /
// seven_day_sonnet keys: on a live Max account both named keys were null while
// limits[] carried the Fable row. session and weekly_all rows are skipped —
// the 5h and wk gauges already cover them from the free statusline path.
//
// The shape is documented Experimental, so every field is decoded into its own
// tolerant type and a mismatch yields zero rows rather than an error. Only a
// body that is not JSON at all fails.
func parseUsageResponse(blob []byte, now time.Time) (PlanUsage, error) {
	var body struct {
		Available  bool `json:"rate_limits_available"`
		RateLimits *struct {
			Limits []struct {
				Kind     string   `json:"kind"`
				Percent  *float64 `json:"percent"`
				ResetsAt string   `json:"resets_at"`
				Scope    *struct {
					Model *struct {
						DisplayName string `json:"display_name"`
					} `json:"model"`
				} `json:"scope"`
			} `json:"limits"`
		} `json:"rate_limits"`
	}
	if err := json.Unmarshal(blob, &body); err != nil {
		// A limits[] that changed type (array → string, say) fails the whole
		// decode. That is a shape change, not a broken response: report no
		// rows and let the 5h/wk gauges carry on.
		var probe map[string]json.RawMessage
		if json.Unmarshal(blob, &probe) != nil {
			return PlanUsage{}, err
		}
		var avail bool
		if raw, ok := probe["rate_limits_available"]; ok {
			_ = json.Unmarshal(raw, &avail)
		}
		return PlanUsage{Available: avail, FetchedAt: now}, nil
	}

	usage := PlanUsage{Available: body.Available, FetchedAt: now}
	if body.RateLimits == nil {
		return usage, nil
	}
	for _, l := range body.RateLimits.Limits {
		if l.Kind != "weekly_scoped" || l.Scope == nil || l.Scope.Model == nil {
			continue
		}
		name := l.Scope.Model.DisplayName
		if name == "" || l.Percent == nil {
			continue
		}
		w := ModelWindow{Name: name, UsedPercent: int(*l.Percent + 0.5)}
		if t, err := time.Parse(time.RFC3339, l.ResetsAt); err == nil {
			w.ResetsAt = t
		}
		usage.Models = append(usage.Models, w)
	}
	return usage, nil
}

// usageTTL is how long a cached usage read stays authoritative.
//
// Fifteen minutes because a poll is not free: it spawns a Claude Code (~2.2s)
// and fires every SessionStart hook the user has installed. Suppressing those
// hooks means isolating CLAUDE_CONFIG_DIR, which also loses the OAuth profile
// and so loses the answer. Weekly windows move slowly enough that this costs
// nothing in freshness, and the fast-moving 5h meter does not come from here at
// all — it rides the free statusline path.
const usageTTL = 15 * time.Minute

// usageLockStale is when a lock file is presumed abandoned by a crashed poller.
const usageLockStale = 2 * time.Minute

// rawUsageCache is the on-disk shape. Timestamps are Unix seconds so the file
// stays readable by eye and by jq.
type rawUsageCache struct {
	FetchedAt int64 `json:"fetched_at"`
	Available bool  `json:"available"`
	Models    []struct {
		Name        string `json:"name"`
		UsedPercent int    `json:"used_percent"`
		ResetsAt    int64  `json:"resets_at"`
	} `json:"models"`
}

// usageClaudeBin is the Claude Code the poller spawns. Resolved from PATH by
// default; the env override is what lets a dev point the poller at a specific
// build without touching PATH.
func usageClaudeBin() string {
	if p := os.Getenv("CLAUDEMUX_CLAUDE_BIN"); p != "" {
		return p
	}
	return "claude"
}

func defaultUsageCachePath() string {
	if p := os.Getenv("CLAUDEMUX_USAGE_CACHE_PATH"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "claudemux", "usage.json")
}

func writeUsageCache(path string, u PlanUsage) error {
	raw := rawUsageCache{FetchedAt: u.FetchedAt.Unix(), Available: u.Available}
	for _, m := range u.Models {
		var resets int64
		if !m.ResetsAt.IsZero() {
			resets = m.ResetsAt.Unix()
		}
		raw.Models = append(raw.Models, struct {
			Name        string `json:"name"`
			UsedPercent int    `json:"used_percent"`
			ResetsAt    int64  `json:"resets_at"`
		}{Name: m.Name, UsedPercent: m.UsedPercent, ResetsAt: resets})
	}
	return writeJSONAtomic(path, raw)
}

// readUsageCache loads the cache. The second return reports whether it is
// within the TTL; the rows are returned either way, because a fifteen-minute-old
// Fable percentage is far more useful than a missing meter.
func readUsageCache(path string, now time.Time) (PlanUsage, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PlanUsage{}, false
	}
	var raw rawUsageCache
	if err := json.Unmarshal(data, &raw); err != nil {
		return PlanUsage{}, false
	}
	u := PlanUsage{Available: raw.Available, FetchedAt: time.Unix(raw.FetchedAt, 0)}
	for _, m := range raw.Models {
		w := ModelWindow{Name: m.Name, UsedPercent: m.UsedPercent}
		if m.ResetsAt != 0 {
			w.ResetsAt = time.Unix(m.ResetsAt, 0)
		}
		u.Models = append(u.Models, w)
	}
	return u, usageStampFresh(u.FetchedAt, now)
}

// usageStampFresh reports whether stamp is inside the TTL window measured in
// EITHER direction from now. It is the single rule behind both of this file's
// time windows: the cache's freshness and the post-failure backoff.
//
// They used to disagree about the same clock. The backoff took the absolute
// value; the cache read required age >= 0 and called anything stamped in the
// future infinitely stale. A wall clock that steps BACKWARDS — suspend/resume,
// an NTP correction — is exactly what leaves a cache written moments ago
// sitting in the future, so the same skew that the backoff shrugged off made
// every cache read report stale and re-spawn a Claude Code for an answer
// already on disk.
//
// Symmetric, and BOUNDED in the future rather than unbounded, so neither
// window can be wedged by a nonsense timestamp — a clock that jumped forward,
// a hand-edited file. Past the window in either direction the cache is
// refetched and rewritten with the current clock, and the marker is ignored;
// both halves self-heal rather than latching. What the symmetry costs is at
// most one TTL of extra staleness on a weekly window, which is nothing.
func usageStampFresh(stamp, now time.Time) bool {
	age := now.Sub(stamp)
	if age < 0 {
		age = -age
	}
	return age < usageTTL
}

// usageFailMarkerPath is the sentinel a failed poll leaves beside the cache.
//
// It exists because usageTTL is measured off the CACHE's timestamp, and a
// failure writes no cache: without this marker, a missing `claude` or an
// erroring get_usage would put every pane back to spawning on every check
// interval, forever, each attempt burning up to usageTimeout. The lock does
// not help there — panes tick on their own staggered schedules and never
// collide, so N panes means N spawns per interval rather than one.
//
// It is a SEPARATE file, and the cache is never touched on failure, so a
// transient outage cannot clear or stale-date rows a good poll already stored.
// The marker's mtime is the clock, set explicitly from the caller's `now` so
// the window is testable without waiting on it.
func usageFailMarkerPath(path string) string { return path + ".failed" }

// usageBackoffActive reports whether a recent failed poll should suppress this
// spawn. The window is usageStampFresh's symmetric one: a marker stamped
// further than usageTTL into the FUTURE is ignored rather than trusted, so a
// clock jump cannot disable polling indefinitely.
func usageBackoffActive(path string, now time.Time) bool {
	st, err := os.Stat(usageFailMarkerPath(path))
	if err != nil {
		return false
	}
	return usageStampFresh(st.ModTime(), now)
}

// markUsageFailure arms the backoff. Best effort: if the marker cannot be
// written we are no worse off than before it existed.
func markUsageFailure(path string, now time.Time) {
	marker := usageFailMarkerPath(path)
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return
	}
	f.Close()
	// Explicit rather than relying on the create/truncate to move mtime:
	// truncating an already-empty file need not touch it on every filesystem,
	// which would pin the window to the FIRST failure instead of the last.
	_ = os.Chtimes(marker, now, now)
}

// clearUsageFailure disarms the backoff after a poll that actually worked, so
// recovery is immediate rather than waiting out the remainder of the window.
func clearUsageFailure(path string) { _ = os.Remove(usageFailMarkerPath(path)) }

// refreshUsageCache fetches and caches, taking a lock first so that N head
// panes on one machine cause one spawn rather than N. A caller that loses the
// race returns the current cache immediately instead of waiting. A caller
// arriving inside the post-failure backoff window does the same, without
// spawning anything.
func refreshUsageCache(ctx context.Context, path, claudeBin string, now time.Time) (PlanUsage, error) {
	lock := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return PlanUsage{}, err
	}
	// Checked BEFORE the lock, deliberately: the panes this has to hold back
	// are the staggered ones that would never contend for the lock at all.
	// Reported as a cache read rather than an error — there is nothing for the
	// caller to do about it, and the rows on disk (if any) are still the best
	// answer available.
	if usageBackoffActive(path, now) {
		cached, _ := readUsageCache(path, now)
		return cached, nil
	}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		// Someone else holds it — unless they crashed and left it behind.
		if st, statErr := os.Stat(lock); statErr == nil && now.Sub(st.ModTime()) > usageLockStale {
			os.Remove(lock)
		}
		cached, _ := readUsageCache(path, now)
		return cached, nil
	}
	f.Close()
	defer os.Remove(lock)

	usage, err := fetchPlanUsage(ctx, claudeBin, now)
	if err != nil {
		markUsageFailure(path, now)
		return PlanUsage{}, err
	}
	usage.Fetched = true
	clearUsageFailure(path)
	if !usage.Available {
		// NOT written to the cache, deliberately. rate_limits_available:false
		// is a statement about the credentials this particular spawn
		// inherited, and the cache is machine-global: a single pane started
		// with CLAUDE_CODE_USE_BEDROCK=1 or ANTHROPIC_API_KEY set would
		// otherwise win the lock, stamp available:false over rows a
		// subscriber pane fetched, and take the model meters away from every
		// other pane on the machine. The caller gets the verdict via the
		// return value (Fetched is set), which is exactly the scope it
		// applies to: this process.
		return usage, nil
	}
	if writeErr := writeUsageCache(path, usage); writeErr != nil {
		// The fetch succeeded; a cache we could not persist still serves this
		// process for this tick. It arms the backoff all the same: nothing
		// landed on disk, so the next check would otherwise find no fresh
		// cache and spawn again immediately — the same storm a failed fetch
		// would cause.
		markUsageFailure(path, now)
		return usage, nil
	}
	return usage, nil
}
