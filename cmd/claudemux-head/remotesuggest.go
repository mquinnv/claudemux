package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
)

// Suggested replies for an idle session, for the glasses remote. Each one is
// typed into a live coding session with a single tap, so the prompt is
// strict about what may be suggested. It rides on the Summarizer like the
// headline does, so summary.enabled, the key file and the base-URL rule
// apply unchanged.

const (
	suggestToolName  = "suggest_replies"
	suggestMaxTokens = 200
	suggestMaxCount  = 3
	suggestMaxRunes  = 60
)

const suggestSystemPrompt = `You suggest what an engineer might say next to a coding assistant (Claude Code) that has just stopped and is waiting on them. You are shown the assistant's last message.

Reply with up to three short suggestions, one per line, most likely first. Each suggestion is typed into the live session with one tap, so it must be safe to send as written.

Rules:
- Each suggestion must be a complete instruction a careful engineer could plausibly send in reply to that message: specific, imperative, under 60 characters.
- Never grant standing permission. Do not suggest anything containing "always", "don't ask again", "do whatever you think", "whatever you want", "anything you need", or any other blanket approval of future actions.
- Never ask a question back to the session. No suggestion may end in a question mark or be phrased as a question.
- Do not suggest approving something the message does not clearly propose. If nothing reasonable fits, give fewer suggestions.
- No numbering, bullets, quotes or commentary.`

// SuggestReplies asks the model for up to three replies to the session's
// last message, via a forced tool call like Headline. A nil Summarizer
// returns nothing and no error: no suggestions is never a failure of the
// detail view.
func (s *Summarizer) SuggestReplies(ctx context.Context, lastMessage string) ([]string, error) {
	if s == nil {
		return nil, nil
	}
	tool := anthropic.ToolParam{
		Name:        suggestToolName,
		Description: anthropic.String("Report up to three suggested replies, one per line."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"text": map[string]any{
					"type":        "string",
					"description": "Up to three replies, one per line, each under 60 characters.",
				},
			},
			Required: []string{"text"},
		},
	}
	ctx, cancel := context.WithTimeout(ctx, summaryRequestTimeout)
	defer cancel()
	resp, err := s.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:      anthropic.Model(s.model),
		MaxTokens:  suggestMaxTokens,
		System:     []anthropic.TextBlockParam{{Text: suggestSystemPrompt}},
		Tools:      []anthropic.ToolUnionParam{{OfTool: &tool}},
		ToolChoice: anthropic.ToolChoiceParamOfTool(suggestToolName),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("The assistant's last message:\n" + lastMessage)),
		},
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
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(tu.JSON.Input.Raw()), &out); err != nil {
			return nil, fmt.Errorf("suggest tool input: %w", err)
		}
		if placeholderLine(out.Text) {
			return nil, errPlaceholderSummary
		}
		return cleanSuggestions(out.Text), nil
	}
	return nil, errors.New("no suggest_replies tool call in response")
}

// cleanSuggestions turns the model's text into at most three replies: one
// per line, bullets, numbering and quotes stripped, empties and lines over
// 60 runes dropped.
func cleanSuggestions(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "-*•")
		line = strings.TrimSpace(line)
		line = stripNumbering(line)
		line = strings.TrimSpace(strings.Trim(line, "\"'`“”‘’"))
		if line == "" || utf8.RuneCountInString(line) > suggestMaxRunes {
			continue
		}
		out = append(out, line)
		if len(out) == suggestMaxCount {
			break
		}
	}
	return out
}

// stripNumbering removes a leading "1." or "1)" and the space after it.
func stripNumbering(s string) string {
	i := 0
	for i < len(s) && unicode.IsDigit(rune(s[i])) {
		i++
	}
	if i == 0 || i >= len(s) || (s[i] != '.' && s[i] != ')') {
		return s
	}
	return strings.TrimSpace(s[i+1:])
}

// suggestCache remembers suggestions per session for as long as its idle
// episode (Since) lasts, so repeated detail fetches cost one model call.
type suggestCache struct {
	mu sync.Mutex
	m  map[string]suggestEntry
}

type suggestEntry struct {
	since time.Time
	v     []string
}

func (c *suggestCache) get(name string, since time.Time) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[name]
	if !ok || !e.since.Equal(since) {
		return nil, false
	}
	return e.v, true
}

func (c *suggestCache) put(name string, since time.Time, v []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]suggestEntry{}
	}
	c.m[name] = suggestEntry{since: since, v: v}
}
