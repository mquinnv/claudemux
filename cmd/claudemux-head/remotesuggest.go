package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// Replies for the phone remote, drafted by the summary model (Haiku by
// default). At most one successful call per
// WAITING EPISODE (a session's state + since); a failed call is retried after
// a back-off. The screen of a session parked on a question does not change
// until it is answered, so the episode is the right cache key — it costs
// nothing to compute, never churns on a spinner or a clock, and a new
// question is in practice a new episode (since has 1-second resolution, so
// only two waits in the same state starting within one second would collide).

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
	suggestToolName    = "suggest"
	suggestMaxTokens   = 600
	suggestMax         = 4
	suggestLabelRunes  = 40
	suggestTextRunes   = 300
	suggestScreenRunes = 6000
	suggestTimeout     = 20 * time.Second
	suggestRetryAfter  = 30 * time.Second
	suggestAuthRetry   = 5 * time.Minute
	// suggestNoRepliesRetry is the back-off after a call whose replies were
	// all dropped: the screen of a parked session does not change, so a quick
	// retry would most likely be another billed call with the same outcome.
	suggestNoRepliesRetry = 10 * time.Minute
	suggestMaxInFlight    = 2
)

// suggestStatus is a session's suggestion state as the page sees it.
type suggestStatus string

const (
	suggestStatusOff     suggestStatus = "off"
	suggestStatusPending suggestStatus = "pending"
	suggestStatusReady   suggestStatus = "ready"
	suggestStatusError   suggestStatus = "error"
)

// Failure reasons shown on the page (remoteSuggestState.Error). Short and
// sanitized: the full error goes to the log only.
const (
	suggestErrAuth      = "auth"
	suggestErrRateLimit = "rate limit"
	suggestErrTimeout   = "timeout"
	suggestErrNetwork   = "network"
	suggestErrAPI       = "api error"
	suggestErrNoScreen  = "no screen"
	suggestErrNoPane    = "no pane"
	suggestErrNoReplies = "no usable replies"
	suggestErrBadResp   = "bad response"
	suggestErrInternal  = "internal"
)

// suggestError is a failure whose page reason is known where it happens.
type suggestError struct {
	reason string
	msg    string
}

func (e *suggestError) Error() string { return e.msg }

// suggestReason maps err to one of the suggestErr* reasons.
func suggestReason(err error) string {
	var se *suggestError
	if errors.As(err, &se) {
		return se.reason
	}
	var ae *anthropic.Error
	if errors.As(err, &ae) {
		switch ae.StatusCode {
		case 401, 403:
			return suggestErrAuth
		case 429:
			return suggestErrRateLimit
		case 408, 504:
			return suggestErrTimeout
		}
		return suggestErrAPI
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return suggestErrTimeout
	}
	if errors.Is(err, context.Canceled) {
		return suggestErrInternal
	}
	// Transport failures: the request never got an HTTP answer. A timeout
	// among them stays "timeout".
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return suggestErrTimeout
	}
	var ue *url.Error
	var oe *net.OpError
	var de *net.DNSError
	if errors.As(err, &ue) || errors.As(err, &oe) || errors.As(err, &de) {
		return suggestErrNetwork
	}
	return suggestErrInternal
}

var suggestSystemPrompt = `You draft one-tap replies that a developer sends from their PHONE to a Claude Code session that is waiting on them. You see the bottom of the session's terminal and its state.

Call the suggest tool with up to ` + fmt.Sprint(suggestMax) + ` replies, the most likely first.

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
		return nil, &suggestError{suggestErrNoScreen, "empty screen"}
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
	switch resp.StopReason {
	case anthropic.StopReasonEndTurn, anthropic.StopReasonToolUse, anthropic.StopReasonStopSequence, "":
	default:
		return nil, &suggestError{suggestErrBadResp, fmt.Sprintf("stop reason %s", resp.StopReason)}
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
			return nil, &suggestError{suggestErrBadResp, fmt.Sprintf("suggest tool input: %v", err)}
		}
		clean, drops := cleanSuggestionsCounted(out.Replies, dialog, fixed)
		if n := drops.total(); n > 0 {
			teardownLogf("remote: suggest for %s: model returned %d replies, cleaning dropped %d (%s)", sess.Name, len(out.Replies), n, drops)
		}
		if len(clean) == 0 {
			return nil, &suggestError{suggestErrNoReplies, fmt.Sprintf("no usable suggestions (%d returned, all dropped: %s)", len(out.Replies), drops)}
		}
		return clean, nil
	}
	return nil, &suggestError{suggestErrBadResp, "no suggest tool call in response"}
}

var suggestKeyRE = regexp.MustCompile(`^[1-9]$`)

// Reasons cleanSuggestionsCounted drops a suggestion, for the log.
const (
	suggestDropControl     = "control"
	suggestDropWrongKind   = "wrong kind"
	suggestDropQuestion    = "question"
	suggestDropPlaceholder = "placeholder"
	suggestDropTooLong     = "too long"
	suggestDropDuplicate   = "duplicate"
	suggestDropOverMax     = "over max"
)

// suggestDrops counts dropped suggestions per reason.
type suggestDrops map[string]int

func (d suggestDrops) total() int {
	n := 0
	for _, c := range d {
		n += c
	}
	return n
}

// String lists the non-zero counts in a fixed order, e.g. "question 1, duplicate 2".
func (d suggestDrops) String() string {
	var parts []string
	for _, r := range []string{suggestDropControl, suggestDropWrongKind, suggestDropQuestion, suggestDropPlaceholder, suggestDropTooLong, suggestDropDuplicate, suggestDropOverMax} {
		if d[r] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", r, d[r]))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// normalizeSuggestText is the form typed replies are compared in: whitespace
// collapsed, trailing periods trimmed, lowercased. Fixed replies go through
// it too, so "continue." and "Continue" are one reply.
func normalizeSuggestText(s string) string {
	return strings.ToLower(strings.TrimRight(collapseWhitespace(strings.TrimSpace(s)), "."))
}

// cleanSuggestions is cleanSuggestionsCounted without the drop counts.
func cleanSuggestions(in []remoteSuggestion, dialog bool, fixed []string) []remoteSuggestion {
	out, _ := cleanSuggestionsCounted(in, dialog, fixed)
	return out
}

// cleanSuggestionsCounted enforces what the prompt asks for, since a model
// can ignore it: dialogs get keys only and prompts get text only; no
// questions back; no duplicates of each other or of the fixed replies;
// bounded lengths; no control characters (dropped, since the send path
// refuses them); at most suggestMax. It also counts what it dropped, per
// reason.
func cleanSuggestionsCounted(in []remoteSuggestion, dialog bool, fixed []string) ([]remoteSuggestion, suggestDrops) {
	drops := suggestDrops{}
	seen := map[string]bool{}
	for _, f := range fixed {
		seen[normalizeSuggestText(f)] = true
	}
	var out []remoteSuggestion
	for i, s := range in {
		if len(out) == suggestMax {
			drops[suggestDropOverMax] += len(in) - i
			break
		}
		if hasControlRunes(s.Label) || hasControlRunes(s.Text) {
			drops[suggestDropControl]++
			continue
		}
		label := collapseWhitespace(s.Label)
		text := strings.TrimSpace(s.Text)
		key := strings.TrimSpace(s.Key)
		var c remoteSuggestion
		if dialog {
			if !suggestKeyRE.MatchString(key) {
				drops[suggestDropWrongKind]++
				continue
			}
			if seen["key:"+key] {
				drops[suggestDropDuplicate]++
				continue
			}
			seen["key:"+key] = true
			c = remoteSuggestion{Label: label, Key: key}
			if c.Label == "" {
				c.Label = "option " + key
			}
		} else {
			text = strings.TrimRight(collapseWhitespace(text), ".")
			switch {
			case text == "":
				drops[suggestDropWrongKind]++
				continue
			case strings.HasSuffix(text, "?"):
				drops[suggestDropQuestion]++
				continue
			case placeholderLine(text):
				drops[suggestDropPlaceholder]++
				continue
			}
			// Too long is dropped, not clipped: a chip SENDS its text, and a
			// clipped prompt is a different prompt.
			if len([]rune(text)) > suggestTextRunes {
				drops[suggestDropTooLong]++
				continue
			}
			k := normalizeSuggestText(text)
			if seen[k] {
				drops[suggestDropDuplicate]++
				continue
			}
			seen[k] = true
			c = remoteSuggestion{Label: label, Text: text}
			if c.Label == "" {
				c.Label = text
			}
		}
		c.Label = truncateRunes(c.Label, suggestLabelRunes)
		out = append(out, c)
	}
	return out, drops
}

// hasControlRunes reports C0 controls other than newline and tab, DEL, and
// C1 controls — what the remote's send path rejects. The single source of
// truth for that rule: webremote.go's send and defer paths and config.go's
// replies validation call it.
func hasControlRunes(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return true
		}
	}
	return false
}

// tailRunes keeps the last n runes of s.
func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// remoteSuggestState is what the page gets. Status is one of:
//   - off: no API key, summaries disabled, or the session isn't waiting;
//   - pending: a call is queued or running for the current episode;
//   - ready: Items holds the drafted replies;
//   - error: Error is the short reason, a suggestErr* value. Besides the
//     call's own failures this includes "no pane" and "no screen", which
//     handleSession sets when there is nothing to draft from.
type remoteSuggestState struct {
	Status suggestStatus      `json:"status"`
	Items  []remoteSuggestion `json:"items,omitempty"`
	Error  string             `json:"error,omitempty"`
}

type remoteSuggestEntry struct {
	episode string
	status  suggestStatus
	reason  string
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
		return remoteSuggestState{Status: suggestStatusPending}
	}
	st := remoteSuggestState{Status: e.status, Items: append([]remoteSuggestion(nil), e.items...)}
	if e.status == suggestStatusError {
		st.Error = e.reason
	}
	return st
}

// needsCall reports whether ensure(sess, ...) would start a new call right
// now. Callers use it to skip capturing a screen nobody will read.
func (c *remoteSuggestions) needsCall(sess swSession) bool {
	if !c.enabled() || !isWaiting(sess.State) {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.needsCallLocked(sess, remoteEpisode(sess))
}

// needsCallLocked is the decision ensure and needsCall share: a waiting
// session needs a call unless its episode's entry is ready, pending, or
// failed less than its back-off ago (see suggestBackoff). c.mu must
// be held; the caller has checked enabled and isWaiting.
func (c *remoteSuggestions) needsCallLocked(sess swSession, ep string) bool {
	e, ok := c.entries[sess.Name]
	if !ok || e.episode != ep {
		return true
	}
	return e.status == suggestStatusError && c.now().Sub(e.at) >= suggestBackoff(e.reason)
}

// ensure starts a call for sess's episode when needsCall says one is due.
// screen is read only when a call actually starts, in the call's goroutine;
// current callers pass a closure over an already-captured screen. Never
// blocks.
func (c *remoteSuggestions) ensure(sess swSession, fixed []string, screen func() string) {
	if !c.enabled() || !isWaiting(sess.State) {
		return
	}
	ep := remoteEpisode(sess)
	c.mu.Lock()
	if !c.needsCallLocked(sess, ep) {
		c.mu.Unlock()
		return
	}
	c.entries[sess.Name] = &remoteSuggestEntry{episode: ep, status: suggestStatusPending, at: c.now()}
	c.mu.Unlock()

	go func() {
		defer func() {
			if p := recover(); p != nil {
				teardownLogf("remote: suggest panic for %s: %v", sess.Name, p)
				c.finish(sess.Name, ep, nil, &suggestError{suggestErrInternal, fmt.Sprintf("panic: %v", p)})
			}
		}()
		// The timeout covers the wait for a slot too, so a goroutine never
		// queues forever behind stuck calls: time spent waiting for a slot
		// comes out of the same suggestTimeout budget as the call.
		ctx, cancel := context.WithTimeout(context.Background(), suggestTimeout)
		defer cancel()
		select {
		case c.sem <- struct{}{}:
		case <-ctx.Done():
			c.finish(sess.Name, ep, nil, fmt.Errorf("waiting for a call slot: %w", ctx.Err()))
			return
		}
		defer func() { <-c.sem }()
		items, err := c.gen.SuggestReplies(ctx, sess, screen(), fixed)
		c.finish(sess.Name, ep, items, err)
	}()
}

func (c *remoteSuggestions) finish(name, ep string, items []remoteSuggestion, err error) {
	reason := ""
	if err != nil {
		reason = suggestReason(err)
		// Logged before the episode check, so a failure is never silent even
		// when the session moved on while the call ran.
		teardownLogf("remote: suggest for %s (episode %s) failed (%s): %v", name, ep, reason, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[name]
	if !ok || e.episode != ep {
		return // the session moved on while the call ran
	}
	e.at = c.now()
	if err != nil {
		e.reason = reason
		e.status = suggestStatusError
		e.items = nil
		return
	}
	e.status = suggestStatusReady
	e.reason = ""
	e.items = items
}

// suggestBackoff is how long a failed episode waits before a retry: long
// for an auth failure, which won't fix itself quickly, and longer still for
// "no usable replies", since the unchanged screen would most likely draw the
// same replies again; suggestRetryAfter otherwise.
func suggestBackoff(reason string) time.Duration {
	switch reason {
	case suggestErrAuth:
		return suggestAuthRetry
	case suggestErrNoReplies:
		return suggestNoRepliesRetry
	}
	return suggestRetryAfter
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
