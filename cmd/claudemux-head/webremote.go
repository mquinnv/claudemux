package main

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// The phone remote: a page at /remote and a small JSON API under
// /api/remote/ that let the owner of the fleet answer their sessions from a
// phone. It shares the status page's server and its tailnet-only guard
// (webGuard), and adds two gates of its own, because unlike the status page
// it TYPES INTO SESSIONS:
//
//   - web.control must be true, or none of these routes exist;
//   - every API call must carry "Authorization: Bearer <token>", where the
//     token is CLAUDEMUX_REMOTE_TOKEN (environment, then summary.api_key_file).
//     Teammates on the tailnet can read the status page; they cannot drive.
//     The custom header also means a cross-origin page can't forge a request:
//     it would need a CORS preflight, and nothing here answers one.
//
// "Conduction" on the phone mirrors the lobby's conductor: the queue is the
// same waitingQueue order (closing first, then oldest), and the page walks it
// one session at a time. Haiku drafts replies for the session on screen
// (remotesuggest.go); the keyboard is always there too.

//go:embed webremote.html
var webRemoteHTML []byte

const (
	// remoteTokenKey names the secret, looked up like ANTHROPIC_API_KEY.
	remoteTokenKey = "CLAUDEMUX_REMOTE_TOKEN"
	// remoteTokenMinLen refuses a token short enough to guess over a tailnet.
	remoteTokenMinLen = 16
	// remoteMaxReplies and remoteReplyMaxRunes bound web.replies (config.go).
	remoteMaxReplies    = 6
	remoteReplyMaxRunes = 200
	// remoteTextMaxRunes bounds what one send may type.
	remoteTextMaxRunes = 8000
	// remoteScreenLines is how much of the claude pane the page shows.
	remoteScreenLines = 120
	// remoteBodyMax bounds a POST body.
	remoteBodyMax = 64 << 10
	// remoteEscapeSettle is the pause after an Esc that closes a dialog,
	// before typing into the prompt it reveals.
	remoteEscapeSettle = 400 * time.Millisecond
)

// defaultRemoteReplies are the canned answers when web.replies is unset.
var defaultRemoteReplies = []string{"yes, go ahead", "continue", "commit and push"}

// remoteKeys is every key the page may press. Digits pick a dialog option;
// the arrows, Tab and Enter move around one; Escape dismisses a dialog or
// interrupts a turn. Nothing here can reach a shell (no C-c, no C-z).
var remoteKeys = map[string]bool{
	"1": true, "2": true, "3": true, "4": true, "5": true, "6": true, "7": true, "8": true, "9": true,
	"Enter": true, "Escape": true, "Up": true, "Down": true, "Tab": true, "BTab": true, "Space": true,
}

// remoteTmux is the slice of tmux the remote drives, an interface so the
// handlers are testable without a tmux server.
type remoteTmux interface {
	// capture returns the pane's visible text plus up to scrollback lines
	// above it; escapes keeps SGR sequences (promptEmpty needs them).
	capture(pane string, escapes bool, scrollback int) (string, error)
	// typeText types text into the pane WITHOUT submitting it.
	typeText(pane, text string) error
	// keys presses tmux key names, in order.
	keys(pane string, keys ...string) error
	// run runs an arbitrary prepared tmux argv (the defer toggle).
	run(args []string) error
}

type execRemoteTmux struct{}

func (execRemoteTmux) capture(pane string, escapes bool, scrollback int) (string, error) {
	args := []string{"capture-pane", "-p", "-J", "-t", pane}
	if escapes {
		args = append(args, "-e")
	}
	if scrollback > 0 {
		args = append(args, "-S", "-"+strconv.Itoa(scrollback))
	}
	ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	return string(out), err
}

// typeText sends a single line as literal keys (sendLiteralArgs). A
// multi-line message goes through a tmux buffer and a BRACKETED paste
// instead: typed line by line, each newline would be an Enter and submit the
// first line on its own.
func (t execRemoteTmux) typeText(pane, text string) error {
	if !strings.Contains(text, "\n") {
		args, ok := sendLiteralArgs(pane, text)
		if !ok {
			return errors.New("nothing to type")
		}
		return t.run(args)
	}
	buf := fmt.Sprintf("claudemux-remote-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
	defer cancel()
	load := exec.CommandContext(ctx, "tmux", "load-buffer", "-b", buf, "-")
	load.Stdin = strings.NewReader(text)
	if err := load.Run(); err != nil {
		return fmt.Errorf("load-buffer: %w", err)
	}
	return t.run([]string{"paste-buffer", "-p", "-d", "-b", buf, "-t", pane})
}

func (t execRemoteTmux) keys(pane string, keys ...string) error {
	return t.run(append([]string{"send-keys", "-t", pane}, keys...))
}

func (execRemoteTmux) run(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "tmux", args...).Run()
}

// webRemote is the remote's state: where it reads the fleet, how it drives
// tmux, and the suggestion cache.
type webRemote struct {
	fleet   *webFleet
	token   string
	replies []string
	tmux    remoteTmux
	suggest *remoteSuggestions
	vimMode bool
	// sleep is time.Sleep, swapped out by tests.
	sleep func(time.Duration)
	// writeMu serializes everything that types: two phones (or a double
	// tap) must not interleave their keystrokes in one prompt.
	writeMu sync.Mutex
}

// remoteToken reads the secret; "" when unset or too short to trust.
func remoteToken(cfg Config) (string, error) {
	tok := configValue(cfg.Summary.APIKeyFile, remoteTokenKey)
	if tok == "" {
		return "", fmt.Errorf("web.control is on but %s is not set (environment or %s)", remoteTokenKey, cfg.Summary.APIKeyFile)
	}
	if len(tok) < remoteTokenMinLen {
		return "", fmt.Errorf("%s is %d characters: use at least %d", remoteTokenKey, len(tok), remoteTokenMinLen)
	}
	return tok, nil
}

func newWebRemote(fleet *webFleet, cfg Config, token string, gen remoteSuggester) *webRemote {
	replies := defaultRemoteReplies
	if len(cfg.Web.Replies) > 0 {
		replies = nil
		for _, r := range cfg.Web.Replies {
			replies = append(replies, strings.TrimSpace(r))
		}
	}
	home, _ := os.UserHomeDir()
	return &webRemote{
		fleet:   fleet,
		token:   token,
		replies: replies,
		tmux:    execRemoteTmux{},
		suggest: newRemoteSuggestions(gen),
		vimMode: claudeVimMode(os.Getenv, home),
		sleep:   time.Sleep,
	}
}

// register adds the remote's routes to mux. tokenErr, when set, is why the
// API cannot run: the page still loads and shows it, so a missing secret
// reads as a setup step rather than a dead link.
func (r *webRemote) register(mux *http.ServeMux, tokenErr error) {
	mux.HandleFunc("GET /remote", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", webContentSecurityPolicy)
		w.Header().Set("Referrer-Policy", "no-referrer")
		_, _ = w.Write(webRemoteHTML)
	})
	api := func(pattern string, h func(http.ResponseWriter, *http.Request)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			if tokenErr != nil {
				remoteError(w, http.StatusServiceUnavailable, "setup", tokenErr.Error())
				return
			}
			if !r.authorized(req) {
				remoteError(w, http.StatusUnauthorized, "token", "missing or wrong remote token")
				return
			}
			if req.Method == http.MethodPost && !strings.HasPrefix(req.Header.Get("Content-Type"), "application/json") {
				remoteError(w, http.StatusUnsupportedMediaType, "type", "send JSON")
				return
			}
			h(w, req)
		})
	}
	api("GET /api/remote/queue", r.handleQueue)
	api("GET /api/remote/session", r.handleSession)
	api("POST /api/remote/send", r.handleSend)
	api("POST /api/remote/key", r.handleKey)
	api("POST /api/remote/defer", r.handleDefer)
}

func (r *webRemote) authorized(req *http.Request) bool {
	got, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok || r.token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(r.token)) == 1
}

// remoteErrorBody is every non-2xx answer: Code is what the page branches on
// ("draft", "dialog", "moved", ...), Message what it shows.
type remoteErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func remoteError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(remoteErrorBody{Code: code, Message: msg})
}

func remoteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	body, err := json.Marshal(v)
	if err != nil {
		remoteError(w, http.StatusInternalServerError, "encode", "encoding failed")
		return
	}
	_, _ = w.Write(body)
}

// remoteSession is one session as the remote sees it.
type remoteSession struct {
	Name       string `json:"name"`
	Color      string `json:"color,omitempty"`
	Emoji      string `json:"emoji,omitempty"`
	State      string `json:"state"`
	StateEmoji string `json:"state_emoji,omitempty"`
	Waiting    bool   `json:"waiting"`
	// Dialog: a question or permission prompt is open, so the answer is a
	// keypress (an option number), not a typed prompt.
	Dialog bool `json:"dialog"`
	// Episode identifies this waiting spell (state + since). The page echoes
	// it on every write, and a write against an episode the session has
	// moved past is refused: an answer meant for one question must never
	// land on the next.
	Episode     string `json:"episode"`
	Since       string `json:"since,omitempty"`
	Topic       string `json:"topic,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	Description string `json:"description,omitempty"`
	ContextPct  *int   `json:"context_pct,omitempty"`
	Model       string `json:"model,omitempty"`
	Deferred    bool   `json:"deferred"`
	DeferReason string `json:"defer_reason,omitempty"`
	Closing     string `json:"closing,omitempty"`
	// Queue is the 1-based place in the conduct queue, 0 when not queued.
	Queue int  `json:"queue"`
	Pane  bool `json:"pane"`
}

// remoteDialog reports a published state whose screen is a picker.
func remoteDialog(state string) bool {
	return state == "Asking" || state == "Tool:AskUserQuestion" || state == "Awaiting"
}

// remoteEpisode is the opaque episode id for s.
func remoteEpisode(s swSession) string {
	return s.State + "@" + s.Since.UTC().Format(time.RFC3339Nano)
}

func toRemoteSession(s swSession, queue int) remoteSession {
	rs := remoteSession{
		Name:        s.Name,
		Emoji:       s.Emoji,
		State:       webStateWord(s.State),
		Waiting:     isWaiting(s.State),
		Dialog:      remoteDialog(s.State),
		Episode:     remoteEpisode(s),
		Since:       webTime(s.Since),
		Topic:       s.Topic,
		Summary:     s.Summary,
		Prompt:      s.Prompt,
		Description: s.Description,
		Model:       webModelName(s.Model),
		Deferred:    s.Deferred,
		Closing:     s.Closing,
		Queue:       queue,
		Pane:        s.ClaudePane != "",
	}
	if isHex6(s.Color) {
		rs.Color = "#" + s.Color
	}
	if k, ok := publishedStateKind(s.State); ok {
		rs.StateEmoji = strings.TrimSpace(stateEmoji(k))
	}
	if s.Context >= 0 {
		pct := s.Context
		rs.ContextPct = &pct
	}
	if s.Deferred {
		rs.DeferReason = sanitizeDeferReason(s.DeferReason)
	}
	return rs
}

// remoteQueueView is GET /api/remote/queue: every session, the conduct queue
// as names in order, and the canned replies.
type remoteQueueView struct {
	Sessions []remoteSession `json:"sessions"`
	Queue    []string        `json:"queue"`
	Replies  []string        `json:"replies"`
	Suggest  bool            `json:"suggest"`
}

// conductQueue is the lobby's own order (waitingQueue) with no snoozes: the
// phone keeps its own skips, since walking away from a session on the phone
// is not walking away from it at the desk.
func conductQueue(sessions []swSession) []swSession {
	return swSnapshot{Sessions: sessions}.waitingQueue(nil, time.Now())
}

func (r *webRemote) queueView() remoteQueueView {
	all := r.fleet.allSessions()
	q := conductQueue(all)
	pos := map[string]int{}
	v := remoteQueueView{Sessions: []remoteSession{}, Queue: []string{}, Replies: r.replies, Suggest: r.suggest.enabled()}
	for i, s := range q {
		pos[s.Name] = i + 1
		v.Queue = append(v.Queue, s.Name)
	}
	names := map[string]bool{}
	for _, s := range all {
		names[s.Name] = true
		v.Sessions = append(v.Sessions, toRemoteSession(s, pos[s.Name]))
	}
	r.suggest.prune(names)
	return v
}

func (r *webRemote) handleQueue(w http.ResponseWriter, _ *http.Request) {
	remoteJSON(w, r.queueView())
}

func (r *webRemote) lookup(name string) (swSession, bool) {
	for _, s := range r.fleet.allSessions() {
		if s.Name == name {
			return s, true
		}
	}
	return swSession{}, false
}

// remoteSessionView is GET /api/remote/session?name=: the session, the
// bottom of its claude pane, whether its prompt is empty, and Haiku's
// suggestions for the current episode.
type remoteSessionView struct {
	Session     remoteSession      `json:"session"`
	Screen      string             `json:"screen"`
	PromptEmpty bool               `json:"prompt_empty"`
	Suggestions remoteSuggestState `json:"suggestions"`
}

func (r *webRemote) handleSession(w http.ResponseWriter, req *http.Request) {
	name := req.URL.Query().Get("name")
	s, ok := r.lookup(name)
	if !ok {
		remoteError(w, http.StatusNotFound, "gone", "no session named "+strconv.Quote(name))
		return
	}
	queue := conductQueue(r.fleet.allSessions())
	pos := 0
	for i, q := range queue {
		if q.Name == s.Name {
			pos = i + 1
		}
	}
	v := remoteSessionView{Session: toRemoteSession(s, pos)}
	if s.ClaudePane != "" {
		if raw, err := r.tmux.capture(s.ClaudePane, true, remoteScreenLines); err == nil {
			v.PromptEmpty = promptEmpty(raw, r.vimMode)
			v.Screen = remoteScreenText(raw, remoteScreenLines)
			if v.PromptEmpty {
				v.Screen = remoteTrimPromptBox(v.Screen)
			}
		}
	}
	if isWaiting(s.State) && s.ClaudePane != "" {
		r.suggest.ensure(s, r.replies, func() string { return v.Screen })
		// Conduction is a walk down the queue, so draft the next stop's
		// replies while this one is being read.
		if next, ok := remoteNextInQueue(queue, s.Name); ok {
			pane := next.ClaudePane
			r.suggest.ensure(next, r.replies, func() string {
				raw, err := r.tmux.capture(pane, false, remoteScreenLines)
				if err != nil {
					return ""
				}
				return remoteScreenText(raw, remoteScreenLines)
			})
		}
	}
	v.Suggestions = r.suggest.get(s)
	remoteJSON(w, v)
}

// remoteNextInQueue is the queued session after name (the head of the queue
// when name is not in it), skipping sessions with no claude pane.
func remoteNextInQueue(queue []swSession, name string) (swSession, bool) {
	start := 0
	for i, q := range queue {
		if q.Name == name {
			start = i + 1
			break
		}
	}
	for _, q := range queue[start:] {
		if q.Name != name && q.ClaudePane != "" {
			return q, true
		}
	}
	return swSession{}, false
}

// remoteScreenText strips escapes and keeps the last n non-blank-tailed
// lines of a capture, trailing spaces trimmed off each.
func remoteScreenText(raw string, n int) string {
	lines := previewTail(raw, n)
	for i, l := range lines {
		lines[i] = strings.TrimRight(ansi.Strip(l), " ")
	}
	return strings.Join(lines, "\n")
}

// remoteTrimPromptBox cuts an EMPTY input box (and the footer under it) off
// the bottom of a screen: on a phone those rows are noise, and the page has
// its own input. Only called when promptEmpty has already recognised the
// box, so the second-to-last rule is its top edge.
func remoteTrimPromptBox(screen string) string {
	lines := strings.Split(screen, "\n")
	rules := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if !isRuleLine(lines[i]) {
			continue
		}
		if rules++; rules == 2 {
			return strings.TrimRight(strings.Join(lines[:i], "\n"), "\n ")
		}
	}
	return screen
}

type remoteSendReq struct {
	Name    string `json:"name"`
	Episode string `json:"episode"`
	Text    string `json:"text"`
	// Replace clears a draft (C-u) instead of refusing to type over it.
	Replace bool `json:"replace"`
	// Escape dismisses an open dialog first, so the text goes in as a
	// normal prompt instead.
	Escape bool `json:"escape"`
}

type remoteKeyReq struct {
	Name    string `json:"name"`
	Episode string `json:"episode"`
	Key     string `json:"key"`
}

type remoteDeferReq struct {
	Name   string `json:"name"`
	On     bool   `json:"on"`
	Reason string `json:"reason"`
}

type remoteOK struct {
	OK bool `json:"ok"`
}

func decodeRemote(w http.ResponseWriter, req *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, remoteBodyMax))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		remoteError(w, http.StatusBadRequest, "body", "bad request body: "+err.Error())
		return false
	}
	return true
}

// target resolves the session a write is aimed at and checks it is still in
// the episode the page saw. An empty episode skips the check: the page
// always sends one, but a deliberate "type this regardless" stays possible.
func (r *webRemote) target(w http.ResponseWriter, name, episode string) (swSession, bool) {
	s, ok := r.lookup(name)
	if !ok {
		remoteError(w, http.StatusNotFound, "gone", "no session named "+strconv.Quote(name))
		return s, false
	}
	if s.ClaudePane == "" {
		remoteError(w, http.StatusConflict, "nopane", name+" has no claude pane")
		return s, false
	}
	if episode != "" && episode != remoteEpisode(s) {
		remoteError(w, http.StatusConflict, "moved", name+" has moved on since you looked: now "+webStateWord(s.State))
		return s, false
	}
	return s, true
}

func (r *webRemote) handleSend(w http.ResponseWriter, req *http.Request) {
	var body remoteSendReq
	if !decodeRemote(w, req, &body) {
		return
	}
	text := strings.TrimRight(strings.ReplaceAll(body.Text, "\r\n", "\n"), "\n \t")
	if strings.TrimSpace(text) == "" {
		remoteError(w, http.StatusBadRequest, "empty", "nothing to send")
		return
	}
	if len([]rune(text)) > remoteTextMaxRunes {
		remoteError(w, http.StatusBadRequest, "long", fmt.Sprintf("message is over %d characters", remoteTextMaxRunes))
		return
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	s, ok := r.target(w, body.Name, body.Episode)
	if !ok {
		return
	}
	pane := s.ClaudePane
	if remoteDialog(s.State) {
		if !body.Escape {
			remoteError(w, http.StatusConflict, "dialog", "a question is open: pick an option, or dismiss it and send this as a prompt")
			return
		}
		if err := r.tmux.keys(pane, "Escape"); err != nil {
			remoteError(w, http.StatusBadGateway, "tmux", "tmux: "+err.Error())
			return
		}
		r.sleep(remoteEscapeSettle)
	}
	raw, err := r.tmux.capture(pane, true, 0)
	if err != nil {
		remoteError(w, http.StatusBadGateway, "tmux", "tmux: "+err.Error())
		return
	}
	if !promptEmpty(raw, r.vimMode) {
		if !body.Replace {
			remoteError(w, http.StatusConflict, "draft", "the prompt isn't empty (a draft, or a screen the remote doesn't recognise)")
			return
		}
		if err := r.tmux.keys(pane, "C-u"); err != nil {
			remoteError(w, http.StatusBadGateway, "tmux", "tmux: "+err.Error())
			return
		}
	}
	if err := r.tmux.typeText(pane, text); err != nil {
		remoteError(w, http.StatusBadGateway, "tmux", "tmux: "+err.Error())
		return
	}
	r.sleep(teardownKeyDelay) // see teardownKeyDelay
	if err := r.tmux.keys(pane, "Enter"); err != nil {
		remoteError(w, http.StatusBadGateway, "tmux", "tmux: "+err.Error())
		return
	}
	teardownLogf("remote: sent %d chars to %s", len(text), s.Name)
	remoteJSON(w, remoteOK{OK: true})
}

func (r *webRemote) handleKey(w http.ResponseWriter, req *http.Request) {
	var body remoteKeyReq
	if !decodeRemote(w, req, &body) {
		return
	}
	if !remoteKeys[body.Key] {
		remoteError(w, http.StatusBadRequest, "key", "key "+strconv.Quote(body.Key)+" is not allowed")
		return
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	s, ok := r.target(w, body.Name, body.Episode)
	if !ok {
		return
	}
	if err := r.tmux.keys(s.ClaudePane, body.Key); err != nil {
		remoteError(w, http.StatusBadGateway, "tmux", "tmux: "+err.Error())
		return
	}
	teardownLogf("remote: pressed %s in %s", body.Key, s.Name)
	remoteJSON(w, remoteOK{OK: true})
}

// handleDefer sets or clears the session's defer mark, the same toggle as
// the lobby's d key: a deferred session leaves the conduct queue, here and
// at the desk.
func (r *webRemote) handleDefer(w http.ResponseWriter, req *http.Request) {
	var body remoteDeferReq
	if !decodeRemote(w, req, &body) {
		return
	}
	s, ok := r.lookup(body.Name)
	if !ok {
		remoteError(w, http.StatusNotFound, "gone", "no session named "+strconv.Quote(body.Name))
		return
	}
	// A pane id resolves to its session for session-scoped options, and
	// unlike a name it cannot prefix-match some other session.
	target := s.HeadPane
	if target == "" {
		target = s.ClaudePane
	}
	if target == "" {
		remoteError(w, http.StatusConflict, "nopane", s.Name+" has no pane to address")
		return
	}
	if err := r.tmux.run(deferArgs(target, body.On, body.Reason)); err != nil {
		remoteError(w, http.StatusBadGateway, "tmux", "tmux: "+err.Error())
		return
	}
	remoteJSON(w, remoteOK{OK: true})
}
