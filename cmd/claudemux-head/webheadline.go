package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// headlineGate decides whether a snapshot is worth a call: only when its
// fingerprint differs from the one last called with, and not within
// interval of that call. A change that arrives during the cooldown is not
// lost — the fingerprint stays different, so the next poll after the
// cooldown fires it. due records the call when it says yes.
type headlineGate struct {
	interval time.Duration
	lastFP   string
	lastCall time.Time
}

func (g *headlineGate) due(fp string, now time.Time) bool {
	if fp == g.lastFP {
		return false
	}
	if !g.lastCall.IsZero() && now.Sub(g.lastCall) < g.interval {
		return false
	}
	g.lastFP = fp
	g.lastCall = now
	return true
}

// webHeadlineWorker is the goroutine that turns fleet changes into
// headline calls. The lobby pokes it after every publish; it decides, via
// the gate, whether that beat is worth a call. One goroutine, one call at a
// time: a slow API stretches the interval rather than stacking requests.
type webHeadlineWorker struct {
	fleet *webFleet
	s     *Summarizer
	gate  headlineGate
	wake  chan struct{}
	quit  chan struct{}
	done  chan struct{}
}

// startHeadlineWorker returns nil when there is no summarizer (summaries
// disabled or no key): the page then shows counts in place of a headline.
func startHeadlineWorker(f *webFleet, s *Summarizer, interval time.Duration) *webHeadlineWorker {
	if s == nil {
		return nil
	}
	w := &webHeadlineWorker{
		fleet: f,
		s:     s,
		gate:  headlineGate{interval: interval},
		wake:  make(chan struct{}, 1),
		quit:  make(chan struct{}),
		done:  make(chan struct{}),
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

func (w *webHeadlineWorker) stop() {
	if w == nil {
		return
	}
	close(w.quit)
	<-w.done
}

func (w *webHeadlineWorker) run() {
	defer close(w.done)
	for {
		select {
		case <-w.quit:
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
		ctx, cancel := context.WithTimeout(context.Background(), summaryRequestTimeout)
		text, err := w.s.Headline(ctx, sessions)
		cancel()
		if err != nil {
			teardownLogf("web: headline: %v", err)
			continue
		}
		w.fleet.setHeadline(webHeadline{Text: text, At: time.Now(), Fingerprint: fp})
	}
}
