package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// Haiku-drafted replies for the phone remote. One call per WAITING EPISODE
// (a session's state + since): the screen of a session parked on a question
// does not change until it is answered, so the episode is the right cache key
// — it costs nothing to compute, never churns on a spinner or a clock, and a
// new question is by definition a new episode.

// remoteSuggestion is one reply chip. Exactly one of Text and Key is set:
// Text is typed as a prompt and submitted; Key is pressed in an open dialog
// (an option's number).
type remoteSuggestion struct {
	Label string `json:"label"`
	Text  string `json:"text,omitempty"`
	Key   string `json:"key,omitempty"`
}

// remoteSuggester drafts replies from what the session shows. *Summarizer
// implements it, sharing the summaries' key, model and switch.
type remoteSuggester interface {
	SuggestReplies(ctx context.Context, sess swSession, screen string, fixed []string) ([]remoteSuggestion, error)
}

const (
	suggestToolName     = "suggest"
	suggestMaxTokens    = 600
	suggestMax          = 4
	suggestLabelRunes   = 40
	suggestTextRunes    = 300
	suggestScreenRunes  = 6000
	suggestTimeout      = 20 * time.Second
	suggestRetryAfter   = 30 * time.Second
	suggestMaxInFlight  = 2
	suggestStatusOff    = "off"
	suggestStatusWait   = "pending"
	suggestStatusReady  = "ready"
	suggestStatusFailed = "error"
)

const suggestSystemPrompt = `You draft one-tap replies that a developer sends from their PHONE to a Claude Code session that is waiting on them. You see the bottom of the session's terminal and its state.

Call the suggest tool with up to 4 replies, the most likely first.

When the state is a dialog (a question with numbered options, or a permission prompt), every reply is a KEY: the digit of an option exactly as numbered on screen, with a short label naming that option. Prefer the options a careful developer would pick; leave out "Type something" / "Chat about this" style entries.

Otherwise the session finished a turn and is waiting for the next prompt. Each reply is TEXT the developer would type next:
- short, imperative, concrete: name the file, command, option or next step the screen is about
- answers the assistant's last question directly if it asked one ("use postgres", "yes, both"), never asks a question back
- moves the work forward; varied, not four phrasings of the same thing
- no pleasantries, no quotes, no trailing period
The label is the reply shortened to a few words.

Never repeat one of the fixed replies you are given; they are already on screen.`

// SuggestReplies asks the model for reply chips. Errors when the call fails
// or nothing usable comes back.
func (s *Summarizer) SuggestReplies(ctx context.Context, sess swSession, screen string, fixed []string) ([]remoteSuggestion, error) {
	if strings.TrimSpace(screen) == "" {
		return nil, errors.New("empty screen")
	}
	dialog := remoteDialog(sess.State)
	var b strings.Builder
	fmt.Fprintf(&b, "State: %s\n", webStateWord(sess.State))
	if dialog {
		b.WriteString("This is a DIALOG: reply with option keys only.\n")
	}
	if sess.Topic != "" {
		fmt.Fprintf(&b, "Session topic: %s\n", sess.Topic)
	}
	if sess.Prompt != "" {
		fmt.Fprintf(&b, "Developer's last prompt: %s\n", truncateRunes(collapseWhitespace(sess.Prompt), 300))
	}
	if len(fixed) > 0 {
		fmt.Fprintf(&b, "Fixed replies (do not repeat): %s\n", strings.Join(fixed, " | "))
	}
	b.WriteString("\nScreen (bottom of the terminal):\n")
	b.WriteString(tailRunes(screen, suggestScreenRunes))

	tool := anthropic.ToolParam{
		Name:        suggestToolName,
		Description: anthropic.String("Offer one-tap replies for the waiting session."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"replies": map[string]any{
					"type":     "array",
					"maxItems": suggestMax,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"label": map[string]any{"type": "string", "description": "A few words for the button."},
							"text":  map[string]any{"type": "string", "description": "The prompt to type. Omit for a dialog key."},
							"key":   map[string]any{"type": "string", "description": "A dialog option's digit, 1-9. Omit for a typed reply."},
						},
						"required": []string{"label"},
					},
				},
			},
			Required: []string{"replies"},
		},
	}
	resp, err := s.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:      anthropic.Model(s.model),
		MaxTokens:  suggestMaxTokens,
		System:     []anthropic.TextBlockParam{{Text: suggestSystemPrompt}},
		Tools:      []anthropic.ToolUnionParam{{OfTool: &tool}},
		ToolChoice: anthropic.ToolChoiceParamOfTool(suggestToolName),
		Messages:   []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(b.String()))},
	})
	if err != nil {
		return nil, err
	}
	for _, block := range resp.Content {
		tu, ok := block.AsAny().(anthropic.ToolUseBlock)
		if !ok || tu.Name != suggestToolName {
			continue
		}
		var out struct {
			Replies []remoteSuggestion `json:"replies"`
		}
		if err := json.Unmarshal([]byte(tu.JSON.Input.Raw()), &out); err != nil {
			return nil, fmt.Errorf("suggest tool input: %w", err)
		}
		clean := cleanSuggestions(out.Replies, dialog, fixed)
		if len(clean) == 0 {
			return nil, errors.New("no usable suggestions")
		}
		return clean, nil
	}
	return nil, errors.New("no suggest tool call in response")
}

var suggestKeyRE = regexp.MustCompile(`^[1-9]$`)

// cleanSuggestions enforces what the prompt asks for, since a model can
// ignore it: dialogs get keys only and prompts get text only; no questions
// back; no duplicates of each other or of the fixed replies; bounded
// lengths; at most suggestMax.
func cleanSuggestions(in []remoteSuggestion, dialog bool, fixed []string) []remoteSuggestion {
	seen := map[string]bool{}
	for _, f := range fixed {
		seen[strings.ToLower(strings.TrimSpace(f))] = true
	}
	var out []remoteSuggestion
	for _, s := range in {
		label := collapseWhitespace(s.Label)
		text := strings.TrimSpace(s.Text)
		key := strings.TrimSpace(s.Key)
		var c remoteSuggestion
		if dialog {
			if !suggestKeyRE.MatchString(key) || seen["key:"+key] {
				continue
			}
			seen["key:"+key] = true
			c = remoteSuggestion{Label: label, Key: key}
			if c.Label == "" {
				c.Label = "option " + key
			}
		} else {
			text = strings.TrimRight(collapseWhitespace(text), ".")
			if text == "" || strings.HasSuffix(text, "?") || placeholderLine(text) {
				continue
			}
			k := strings.ToLower(text)
			if seen[k] {
				continue
			}
			seen[k] = true
			// Too long is dropped, not clipped: a chip SENDS its text, and a
			// clipped prompt is a different prompt.
			if len([]rune(text)) > suggestTextRunes {
				continue
			}
			c = remoteSuggestion{Label: label, Text: text}
			if c.Label == "" {
				c.Label = text
			}
		}
		c.Label = truncateRunes(c.Label, suggestLabelRunes)
		out = append(out, c)
		if len(out) == suggestMax {
			break
		}
	}
	return out
}

// tailRunes keeps the last n runes of s.
func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// remoteSuggestState is what the page gets: Status is off (no key or
// summaries disabled), pending, ready or error.
type remoteSuggestState struct {
	Status string             `json:"status"`
	Items  []remoteSuggestion `json:"items,omitempty"`
}

type remoteSuggestEntry struct {
	episode string
	status  string
	items   []remoteSuggestion
	at      time.Time
}

// remoteSuggestions caches one entry per session, for its current episode,
// and runs at most suggestMaxInFlight calls at once.
type remoteSuggestions struct {
	gen     remoteSuggester
	mu      sync.Mutex
	entries map[string]*remoteSuggestEntry
	sem     chan struct{}
	now     func() time.Time
}

func newRemoteSuggestions(gen remoteSuggester) *remoteSuggestions {
	return &remoteSuggestions{
		gen:     gen,
		entries: map[string]*remoteSuggestEntry{},
		sem:     make(chan struct{}, suggestMaxInFlight),
		now:     time.Now,
	}
}

func (c *remoteSuggestions) enabled() bool { return c != nil && c.gen != nil }

// get is the state for sess's current episode.
func (c *remoteSuggestions) get(sess swSession) remoteSuggestState {
	if !c.enabled() {
		return remoteSuggestState{Status: suggestStatusOff}
	}
	if !isWaiting(sess.State) {
		return remoteSuggestState{Status: suggestStatusOff}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[sess.Name]
	if !ok || e.episode != remoteEpisode(sess) {
		return remoteSuggestState{Status: suggestStatusWait}
	}
	return remoteSuggestState{Status: e.status, Items: append([]remoteSuggestion(nil), e.items...)}
}

// ensure starts a call for sess's episode unless one is cached, in flight,
// or failed less than suggestRetryAfter ago. screen is read only when a
// call actually starts, in the call's goroutine. Never blocks.
func (c *remoteSuggestions) ensure(sess swSession, fixed []string, screen func() string) {
	if !c.enabled() {
		return
	}
	ep := remoteEpisode(sess)
	c.mu.Lock()
	if e, ok := c.entries[sess.Name]; ok && e.episode == ep {
		if e.status != suggestStatusFailed || c.now().Sub(e.at) < suggestRetryAfter {
			c.mu.Unlock()
			return
		}
	}
	c.entries[sess.Name] = &remoteSuggestEntry{episode: ep, status: suggestStatusWait, at: c.now()}
	c.mu.Unlock()

	go func() {
		defer func() {
			if p := recover(); p != nil {
				teardownLogf("remote: suggest panic for %s: %v", sess.Name, p)
				c.finish(sess.Name, ep, nil, errors.New("panic"))
			}
		}()
		c.sem <- struct{}{}
		defer func() { <-c.sem }()
		ctx, cancel := context.WithTimeout(context.Background(), suggestTimeout)
		defer cancel()
		items, err := c.gen.SuggestReplies(ctx, sess, screen(), fixed)
		c.finish(sess.Name, ep, items, err)
	}()
}

func (c *remoteSuggestions) finish(name, ep string, items []remoteSuggestion, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[name]
	if !ok || e.episode != ep {
		return // the session moved on while the call ran
	}
	e.at = c.now()
	if err != nil {
		teardownLogf("remote: suggest for %s: %v", name, err)
		e.status = suggestStatusFailed
		return
	}
	e.status = suggestStatusReady
	e.items = items
}

// prune drops entries for sessions that are gone.
func (c *remoteSuggestions) prune(live map[string]bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for name := range c.entries {
		if !live[name] {
			delete(c.entries, name)
		}
	}
}
