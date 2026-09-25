package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// The fleet headline: one sentence over every session, for the web page's
// header. It rides on the Summarizer so summary.enabled, the key file and
// the base-URL rule all apply unchanged, and it is gated (headlineGate) so
// a fleet whose only movement is timers never spends a call.

const (
	headlineToolName  = "headline"
	headlineMaxTokens = 120
)

const headlineSystemPrompt = `You write the one-line headline for a status page that shows every live coding session one engineer is running with Claude Code.

You are given the sessions: each has a name, a state (Idle means it is waiting on the engineer; Thinking or Tool:* means Claude is working), what the session is for, what it is doing right now, and whether it is deferred (blocked on something outside the sessions) and on what.

Report one sentence, present tense, under 140 characters, that says what the engineer is working on overall and what, if anything, is waiting on them or blocked. Name the work, not the tool. Do not list every session. Do not start with "The engineer" or "Michael". No preamble, no quotes.`

// buildHeadlinePrompt is the user message: one line per session, lobby
// order, no prompts. The raw prompt is the one field that can carry a
// customer's name or a pasted secret, and the headline does not need it.
func buildHeadlinePrompt(sessions []swSession) string {
	var b strings.Builder
	b.WriteString("Sessions:\n")
	for _, s := range sessions {
		fmt.Fprintf(&b, "- %s — %s", s.Name, webStateWord(s.State))
		if s.Topic != "" {
			fmt.Fprintf(&b, " — for: %s", s.Topic)
		}
		if s.Summary != "" {
			fmt.Fprintf(&b, " — now: %s", s.Summary)
		}
		if s.Deferred {
			b.WriteString(" — deferred")
			if r := sanitizeDeferReason(s.DeferReason); r != "" {
				fmt.Fprintf(&b, ", blocked on: %s", r)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Headline asks the model for the fleet's one-liner via a forced tool call,
// the same shape as Summarize, so the reply is a field and not prose.
func (s *Summarizer) Headline(ctx context.Context, sessions []swSession) (string, error) {
	tool := anthropic.ToolParam{
		Name:        headlineToolName,
		Description: anthropic.String("Report the one-sentence headline for the fleet."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "One sentence, present tense, under 140 characters: what is being worked on overall and what is waiting.",
				},
			},
			Required: []string{"text"},
		},
	}
	resp, err := s.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:      anthropic.Model(s.model),
		MaxTokens:  headlineMaxTokens,
		System:     []anthropic.TextBlockParam{{Text: headlineSystemPrompt}},
		Tools:      []anthropic.ToolUnionParam{{OfTool: &tool}},
		ToolChoice: anthropic.ToolChoiceParamOfTool(headlineToolName),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(buildHeadlinePrompt(sessions))),
		},
	})
	if err != nil {
		return "", err
	}
	for _, block := range resp.Content {
		tu, ok := block.AsAny().(anthropic.ToolUseBlock)
		if !ok || tu.Name != headlineToolName {
			continue
		}
		var out struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(tu.JSON.Input.Raw()), &out); err != nil {
			return "", fmt.Errorf("headline tool input: %w", err)
		}
		out.Text = strings.TrimSpace(out.Text)
		if placeholderLine(out.Text) {
			return "", errPlaceholderSummary
		}
		return out.Text, nil
	}
	return "", errors.New("no headline tool call in response")
}

// headlineMinRetryBackoff is the floor applied to a retry after a failed
// call when headline_interval is 0 ("no floor"). Without it, a dead API
// would be hammered on every poll (as often as every snapshot publish)
// instead of on the interval a nonzero config would impose.
const headlineMinRetryBackoff = 30 * time.Second

// headlineGate decides whether a snapshot is worth a call: only when its
// fingerprint differs from the one last called with, and not within
// interval of that call. A change that arrives during the cooldown is not
// lost — the fingerprint stays different, so the next poll after the
// cooldown fires it. due records the call when it says yes.
//
// A failed call must not permanently mark its fingerprint as delivered —
// onFailure clears lastFP (keeping lastCall, so the normal interval/backoff
// still applies) so the same fingerprint becomes due again once enough time
// has passed, instead of being silently skipped forever because it happens
// to match what a failed attempt already "consumed".
type headlineGate struct {
	interval time.Duration
	lastFP   string
	lastCall time.Time
	failed   bool
}

func (g *headlineGate) due(fp string, now time.Time) bool {
	if fp == g.lastFP {
		return false
	}
	wait := g.interval
	if g.failed && wait == 0 {
		wait = headlineMinRetryBackoff
	}
	if !g.lastCall.IsZero() && now.Sub(g.lastCall) < wait {
		return false
	}
	g.lastFP = fp
	g.lastCall = now
	g.failed = false
	return true
}

// onFailure records that the call due() just approved did not succeed, so
// the fingerprint it was called with becomes due again after the backoff
// rather than being treated as already-delivered.
func (g *headlineGate) onFailure() {
	g.lastFP = ""
	g.failed = true
}

// webHeadlineWorker is the goroutine that turns fleet changes into
// headline calls. The lobby pokes it after every publish; it decides, via
// the gate, whether that beat is worth a call. One goroutine, one call at a
// time: a slow API stretches the interval rather than stacking requests.
type webHeadlineWorker struct {
	fleet    *webFleet
	s        *Summarizer
	gate     headlineGate
	wake     chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// startHeadlineWorker returns nil when there is no summarizer (summaries
// disabled or no key): the page then shows counts in place of a headline.
func startHeadlineWorker(f *webFleet, s *Summarizer, interval time.Duration) *webHeadlineWorker {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &webHeadlineWorker{
		fleet:  f,
		s:      s,
		gate:   headlineGate{interval: interval},
		wake:   make(chan struct{}, 1),
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go w.run()
	return w
}

// poke wakes the worker. The channel holds one pending wake, so a burst of
// polls collapses into one look at the fleet and the caller never blocks.
func (w *webHeadlineWorker) poke() {
	if w == nil {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// stop is nil-safe and idempotent: the lobby stops the page from both its
// quit path (a deferred call) and its restart path (an explicit call before
// exec), and both may run. Cancelling ctx (rather than only signalling the
// select loop) also cancels any in-flight Headline call's context, since
// each call's context is derived from it — stop() no longer has to wait out
// summaryRequestTimeout for a call that was already in flight.
func (w *webHeadlineWorker) stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { w.cancel() })
	<-w.done
}

func (w *webHeadlineWorker) run() {
	defer close(w.done)
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.wake:
		}
		sessions := w.fleet.sessions()
		if len(sessions) == 0 {
			continue
		}
		fp := webFingerprint(sessions)
		if !w.gate.due(fp, time.Now()) {
			continue
		}
		ctx, cancel := context.WithTimeout(w.ctx, summaryRequestTimeout)
		text, err := w.s.Headline(ctx, sessions)
		cancel()
		if err != nil {
			teardownLogf("web: headline: %v", err)
			w.gate.onFailure()
			continue
		}
		w.fleet.setHeadline(webHeadline{Text: text, At: time.Now(), Fingerprint: fp})
	}
}
