package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const transcriptTextLimit = 200

// condenseTranscript flattens the event ring into the plain text the summarizer
// reads: the session's first prompt (which anchors the topic even after the ring
// has truncated the head away), then the last maxEvents CONTENT events as user
// text, assistant text, and tool names.
//
// events is the raw JSONL event ring, in which bookkeeping records (attachment,
// mode, permission-mode, agent-color, system, ...) outnumber content records
// several to one. So maxEvents budgets content events and the tail is taken
// AFTER filtering: slicing the raw tail first would spend most of the window on
// records that say nothing about the session, starving the `now` line — the one
// line most sensitive to recency.
func condenseTranscript(firstPrompt string, events []Event, maxEvents int) string {
	var b strings.Builder
	if firstPrompt != "" {
		b.WriteString("first prompt: " + truncateRunes(collapseWhitespace(firstPrompt), transcriptTextLimit) + "\n")
	}

	if maxEvents < 0 {
		maxEvents = 0
	}

	var kept []Event
	for _, e := range events {
		switch {
		case genuinePrompt(e):
			// Claude Code can write the same prompt as several last-prompt
			// records in a row; a repeat of the line just kept duplicates a
			// user line and spends a window slot on nothing. Genuine `user`
			// turns are never collapsed — the same text sent twice is two
			// real prompts.
			if e.Type == "last-prompt" && len(kept) > 0 {
				if prev := kept[len(kept)-1]; genuinePrompt(prev) &&
					collapseWhitespace(prev.UserText) == collapseWhitespace(e.UserText) {
					continue
				}
			}
			kept = append(kept, e)
		case e.Type == "assistant" && (e.UserText != "" || len(e.ToolUses) > 0):
			kept = append(kept, e)
		}
	}
	if len(kept) > maxEvents {
		kept = kept[len(kept)-maxEvents:]
	}

	for _, e := range kept {
		if e.Type == "assistant" {
			if e.UserText != "" {
				b.WriteString("assistant: " + truncateRunes(collapseWhitespace(e.UserText), transcriptTextLimit) + "\n")
			}
			for _, tu := range e.ToolUses {
				b.WriteString("tool: " + tu.Name + "\n")
			}
			continue
		}
		b.WriteString("user: " + truncateRunes(collapseWhitespace(e.UserText), transcriptTextLimit) + "\n")
	}
	return b.String()
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

const (
	summarizeToolName = "summarize"
	summaryMaxEvents  = 30
	summaryMaxTokens  = 200
)

const summarySystemPrompt = `You label a live coding session for a two-line terminal status display and a short tab title.

Call the summarize tool with these fields:

- topic: what this session is FOR — the goal the human is pursuing.
  Derive it PRIMARILY from the "first prompt" line: that is the human's original
  ask and the best evidence of the session's purpose. The rest of the transcript
  shows where the work has wandered, not what it is for — a debugging detour, a
  review round, a merge step, or a tooling problem is never the topic. If the
  first prompt asked for a feature, the topic stays that feature hours later,
  while the work is deep inside some sub-step of building it.

- now: what it is doing RIGHT NOW — the current step. Derive it from the END of
  the transcript.

- tab: a 2-5 word tab label — the shortest phrase naming the project or task.
  lowercase, 32 characters maximum, no punctuation. Derive it from the same
  durable goal as ` + "`topic`" + ` (never the current step), so the tab stays steady
  while ` + "`now`" + ` changes.

Each line: lowercase, under 60 characters, no trailing period, no quotes.
Name concrete things (files, commands, features) — never "working on the task".

Never answer with a placeholder like <UNKNOWN>, "unknown", or "n/a". If the
transcript is thin, describe whatever it does show, however small.

If a previous topic is given, KEEP IT VERBATIM unless the human has genuinely
changed goals. A stable topic is worth more than a fresh one.`

// summaryRequestTimeout bounds a single Summarize attempt. This runs in a poll loop,
// so a hung request must not accumulate indefinitely across retries.
const summaryRequestTimeout = 10 * time.Second

// errPlaceholderSummary marks a reply that failed because the transcript was
// too thin to describe, not because the call couldn't be made. Callers use it
// to decide whether retrying could possibly help: it can't — only new
// transcript events can, and those arrive with their own busy→idle edge.
var errPlaceholderSummary = errors.New("summarize returned an empty or placeholder line")

// placeholderLine reports whether a summary line is a non-answer rather than a
// summary. The tool call is forced, so on a transcript too thin to describe the
// model cannot decline — it emits "<UNKNOWN>" or "n/a" instead. Rendering that
// would put a literal "<UNKNOWN>" on the status line where the raw prompt would
// have said more, so a placeholder must fail and fall back.
//
// Empty and whitespace-only count: a blank line satisfies promptRows' non-empty
// test and renders as the "—" placeholder rather than falling back.
func placeholderLine(s string) bool {
	t := strings.ToLower(strings.TrimSpace(s))
	if strings.HasPrefix(t, "<") && strings.HasSuffix(t, ">") {
		return true
	}
	switch t {
	case "", "-", "?", "n/a", "na", "none", "unknown", "unclear", "no topic":
		return true
	}
	return false
}

// Summary is the topic/now pair the TUI renders as its two status lines.
type Summary struct {
	Topic string `json:"topic"`
	Now   string `json:"now"`
	Tab   string `json:"tab"`
}

// Summarizer turns a session transcript into the topic/now status lines.
type Summarizer struct {
	client anthropic.Client
	model  string
}

// summarizerEnvOptions returns the client options newSummarizer derives from its
// configuration, or nil when no API key can be found — which disables the feature
// entirely rather than letting the SDK resolve some other credential.
//
// The key comes from the process environment, or failing that from the file named
// by summary.api_key_file (see claudeHeadEnvPaths). It deliberately does not read
// the monitored project's env: claudemux-head watches sessions across many repos and
// must not inherit their secrets, nor care which repo a pane was launched from.
//
// ANTHROPIC_BASE_URL is honored explicitly because the unconditional
// option.WithoutEnvironmentDefaults() in newSummarizer suppresses the SDK's own
// reading of it along with the credential autoload. Without this, a user behind a
// gateway — whose ANTHROPIC_API_KEY is a *proxy* token — would have that token
// sent to api.anthropic.com: a secret disclosed to a host they never chose.
func summarizerEnvOptions(cfg SummaryConfig) []option.RequestOption {
	key := configValue(cfg.APIKeyFile, "ANTHROPIC_API_KEY")
	if key == "" {
		return nil
	}
	opts := []option.RequestOption{option.WithAPIKey(key)}
	if base := configValue(cfg.APIKeyFile, "ANTHROPIC_BASE_URL"); base != "" {
		opts = append(opts, option.WithBaseURL(base))
	}
	return opts
}

// configValue prefers the process environment, then the configured key file.
// The process environment winning is what makes `op run -- claudemux-head` work
// with no file on disk at all.
func configValue(keyFile, key string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return strings.TrimSpace(envFileValue(keyFile, key))
}

// newSummarizer returns nil when there is no API key and no explicit options, which
// disables the feature and leaves the pane on its raw-prompt fallback. Every client
// this returns carries an unconditional base of option.WithoutEnvironmentDefaults(),
// option.WithMaxRetries(1), and option.WithRequestTimeout(summaryRequestTimeout),
// applied BEFORE any caller options (so a caller can still override them — SDK
// caller options are applied after the base, per client.go:214). This means:
//   - no combination of caller options can let the SDK fall back to its own
//     environment/profile credential resolution — which would silently authenticate
//     against the local Claude Code OAuth profile and spend the very subscription
//     rate limit this TUI exists to display;
//   - retries are always capped and every request always carries a timeout, since
//     this runs in a poll loop and must not block indefinitely when the API is down
//     or hangs. Without an explicit timeout here, a caller-options client that omits
//     its own WithHTTPClient would fall back to http.DefaultClient, which has none.
func newSummarizer(cfg SummaryConfig, opts ...option.RequestOption) *Summarizer {
	// An explicitly disabled summarizer constructs no client at all, even when a
	// key is present: the feature is billable, and "off" must mean off.
	if !cfg.Enabled {
		return nil
	}
	if len(opts) == 0 {
		if opts = summarizerEnvOptions(cfg); opts == nil {
			return nil
		}
	}
	opts = append([]option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithMaxRetries(1),
		option.WithRequestTimeout(summaryRequestTimeout),
	}, opts...)
	return &Summarizer{client: anthropic.NewClient(opts...), model: cfg.Model}
}

func (s *Summarizer) Summarize(ctx context.Context, firstPrompt string, events []Event, prevTopic string) (Summary, error) {
	transcript := condenseTranscript(firstPrompt, events, summaryMaxEvents)

	prompt := "Transcript:\n" + transcript
	if prevTopic != "" {
		prompt = fmt.Sprintf("Previous topic: %s\n\n%s", prevTopic, prompt)
	}

	tool := anthropic.ToolParam{
		Name:        summarizeToolName,
		Description: anthropic.String("Report the session's topic and current activity."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"topic": map[string]any{
					"type":        "string",
					"description": "What the session is for. Durable across turns.",
				},
				"now": map[string]any{
					"type":        "string",
					"description": "What the session is doing right now.",
				},
				"tab": map[string]any{
					"type":        "string",
					"description": "A 2-5 word lowercase tab label, 32 characters max, no punctuation. Same durable goal as topic, compressed.",
				},
			},
			Required: []string{"topic", "now", "tab"},
		},
	}

	resp, err := s.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:      anthropic.Model(s.model),
		MaxTokens:  summaryMaxTokens,
		System:     []anthropic.TextBlockParam{{Text: summarySystemPrompt}},
		Tools:      []anthropic.ToolUnionParam{{OfTool: &tool}},
		ToolChoice: anthropic.ToolChoiceParamOfTool(summarizeToolName),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return Summary{}, err
	}

	for _, block := range resp.Content {
		tu, ok := block.AsAny().(anthropic.ToolUseBlock)
		if !ok || tu.Name != summarizeToolName {
			continue
		}
		var out Summary
		if err := json.Unmarshal([]byte(tu.JSON.Input.Raw()), &out); err != nil {
			return Summary{}, fmt.Errorf("summarize tool input: %w", err)
		}
		if placeholderLine(out.Topic) || placeholderLine(out.Now) || placeholderLine(out.Tab) {
			return Summary{}, errPlaceholderSummary
		}
		return out, nil
	}
	return Summary{}, errors.New("no summarize tool call in response")
}
