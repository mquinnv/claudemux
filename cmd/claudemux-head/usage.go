package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	// Vertex, or a missing profile scope. The poller stops when it sees this.
	Available bool
	Models    []ModelWindow
	FetchedAt time.Time
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
