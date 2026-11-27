package main

import (
	"bytes"
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
	// remoteTokenMinLen refuses a token short enough to guess over a tailnet;
	// it counts bytes.
	remoteTokenMinLen = 16
	// remoteMaxReplies and remoteReplyMaxRunes bound web.replies (config.go).
	remoteMaxReplies    = 6
	remoteReplyMaxRunes = 200
	// remoteTextMaxRunes bounds what one send may type.
	remoteTextMaxRunes = 8000
	// remoteDeferReasonMaxRunes bounds a defer reason set from the phone.
	remoteDeferReasonMaxRunes = 200
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

// remoteKeys is every key the API accepts (a superset of what the page's
// buttons send). Digits pick a dialog option;
// the arrows, Tab, BTab, Space and Enter move around one; Escape dismisses a
// dialog or interrupts a turn. No key in this allowlist can signal the pane's
// process (no C-c, no C-z). Typed text is a separate, token-gated path
// (handleSend) that refuses control characters. handleSend additionally
// presses Escape and C-u itself.
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
	if err != nil {
		var stderr []byte
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = ee.Stderr
		}
		return "", tmuxError(args, err, stderr)
	}
	return string(out), nil
}

// tmuxError names the tmux subcommand that failed and carries what tmux said
// on stderr, so a failure reads "tmux send-keys: exit status 1: can't find
// pane: %12" rather than a bare exit status.
func tmuxError(args []string, err error, stderr []byte) error {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	if msg := bytes.TrimSpace(stderr); len(msg) > 0 {
		return fmt.Errorf("tmux %s: %w: %s", name, err, msg)
	}
	return fmt.Errorf("tmux %s: %w", name, err)
}

// typeText sends a single line as literal keys (sendLiteralArgs). A
// multi-line message, or one with a tab, goes through a tmux buffer and a
// BRACKETED paste instead: typed line by line, each newline would be an Enter
// and submit the first line on its own, and a typed tab would trigger
// claude's completion rather than insert a tab.
func (t execRemoteTmux) typeText(pane, text string) error {
	if !strings.ContainsAny(text, "\n\t") {
		args, ok := sendLiteralArgs(pane, text)
		if !ok {
			return errors.New("nothing to type")
		}
		return t.run(args)
	}
	buf := fmt.Sprintf("claudemux-remote-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
	defer cancel()
	loadArgs := []string{"load-buffer", "-b", buf, "-"}
	load := exec.CommandContext(ctx, "tmux", loadArgs...)
	load.Stdin = strings.NewReader(text)
	if out, err := load.CombinedOutput(); err != nil {
		return tmuxError(loadArgs, err, out)
	}
	if err := t.run([]string{"paste-buffer", "-p", "-d", "-b", buf, "-t", pane}); err != nil {
		// -d deletes the buffer only after a successful paste: don't leave
		// the message sitting in the tmux server.
		if derr := t.run([]string{"delete-buffer", "-b", buf}); derr != nil {
			teardownLogf("remote: delete-buffer %s after a failed paste: %v", buf, derr)
		}
		return err
	}
	return nil
}

func (t execRemoteTmux) keys(pane string, keys ...string) error {
	return t.run(append([]string{"send-keys", "-t", pane}, keys...))
}

func (execRemoteTmux) run(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "tmux", args...).CombinedOutput(); err != nil {
		return tmuxError(args, err, out)
	}
	return nil
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
	// answered maps a session name to the episode a send or an answering key
	// (a digit or Enter) last landed in, guarded by writeMu. A second answer
	// to the same episode is refused: the first has not been acted on yet,
	// and two would pile up in the prompt or pick two options.
	answered map[string]string
}

// remoteTokenError is why the remote's API is off. Error is the detail for
// the log (it can name the key file); Public is a path-free reason safe to
// send to a caller who has not authenticated.
type remoteTokenError struct {
	detail string
	public string
}

func (e *remoteTokenError) Error() string  { return e.detail }
func (e *remoteTokenError) Public() string { return e.public }

// remoteSetupFallback is the public reason for a tokenErr that is not a
// *remoteTokenError.
const remoteSetupFallback = "the remote is not set up; see the lobby log"

// remoteToken reads the secret. When it is unset or too short to trust it
// returns "" and a *remoteTokenError saying why.
func remoteToken(cfg Config) (string, error) {
	tok := configValue(cfg.Summary.APIKeyFile, remoteTokenKey)
	if tok == "" {
		missing := remoteTokenKey + " is not set (looked in the environment and the summary key file)"
		if cfg.Summary.APIKeyFile != "" {
			return "", &remoteTokenError{
				detail: fmt.Sprintf("web.control is on but %s was not found in the environment or in %s", remoteTokenKey, cfg.Summary.APIKeyFile),
				public: missing,
			}
		}
		return "", &remoteTokenError{
			detail: fmt.Sprintf("web.control is on but %s was not found in the environment", remoteTokenKey),
			public: missing,
		}
	}
	if len(tok) < remoteTokenMinLen {
		return "", &remoteTokenError{
			detail: fmt.Sprintf("%s is %d bytes: use at least %d", remoteTokenKey, len(tok), remoteTokenMinLen),
			public: fmt.Sprintf("%s is too short (%d bytes; it needs at least %d)", remoteTokenKey, len(tok), remoteTokenMinLen),
		}
	}
	return tok, nil
}

// remoteSetupReason is the path-free reason the setup 503 carries.
func remoteSetupReason(tokenErr error) string {
	var te *remoteTokenError
	if errors.As(tokenErr, &te) && te.Public() != "" {
		return te.Public()
	}
	return remoteSetupFallback
}

func newWebRemote(fleet *webFleet, cfg Config, token string, gen remoteSuggester) *webRemote {
	replies := defaultRemoteReplies
	if len(cfg.Web.Replies) > 0 {
		replies = nil
		for _, r := range cfg.Web.Replies {
			replies = append(replies, strings.TrimSpace(r))
		}
	}
	home, err := os.UserHomeDir()
	vim := claudeVimMode(os.Getenv, home)
	if err != nil {
		teardownLogf("remote: UserHomeDir failed (%v); vim mode detected as %v", err, vim)
	}
	return &webRemote{
		fleet:    fleet,
		token:    token,
		replies:  replies,
		tmux:     execRemoteTmux{},
		suggest:  newRemoteSuggestions(gen),
		vimMode:  vim,
		sleep:    time.Sleep,
		answered: map[string]string{},
	}
}

// register adds the remote's routes to mux. tokenErr, when set, is why the
// API cannot run: the page still loads and shows it, so a missing secret
// reads as a setup step rather than a dead link.
func (r *webRemote) register(mux *http.ServeMux, tokenErr error) {
	setupReason := ""
	if tokenErr != nil {
		// The detail (which can name the key file) goes to the log only:
		// the 503 below reaches callers who have not authenticated, so it
		// carries the path-free reason instead.
		teardownLogf("remote: API disabled: %v", tokenErr)
		setupReason = remoteSetupReason(tokenErr)
	}
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
				remoteError(w, http.StatusServiceUnavailable, "setup", setupReason)
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
		teardownLogf("remote: encoding %T failed: %v", v, err)
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
	// land on the next. A send to a busy (non-waiting) session is allowed on
	// purpose, to queue a follow-up; its episode then guards that working
	// state the same way.
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
	// ScreenError says why Screen is empty when the pane could not be read.
	ScreenError string `json:"screen_error,omitempty"`
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
	captured := false
	if s.ClaudePane != "" {
		raw, err := r.tmux.capture(s.ClaudePane, true, remoteScreenLines)
		if err != nil {
			v.ScreenError = "can't read the screen: " + err.Error()
			teardownLogf("remote: capture %s (%s): %v", s.Name, s.ClaudePane, err)
		} else {
			captured = true
			v.PromptEmpty = promptEmpty(raw, r.vimMode)
			v.Screen = remoteScreenText(raw, remoteScreenLines)
			if v.PromptEmpty {
				v.Screen = remoteTrimPromptBox(v.Screen)
			}
		}
	}
	waiting := isWaiting(s.State)
	switch {
	case waiting && s.ClaudePane == "":
		// Nothing to read, so nothing will ever be drafted: get would say
		// pending forever.
		v.Suggestions = remoteSuggestState{Status: suggestStatusFailed, Error: suggestErrNoPane}
	case waiting && !captured:
		v.Suggestions = remoteSuggestState{Status: suggestStatusFailed, Error: suggestErrNoScreen}
	default:
		if waiting {
			screen := v.Screen
			r.suggest.ensure(s, r.replies, func() string { return screen })
			// Conduction is a walk down the queue, so draft the next stop's
			// replies while this one is being read.
			if next, ok := remoteNextInQueue(queue, s.Name); ok {
				r.prefetch(next)
			}
		}
		v.Suggestions = r.suggest.get(s)
	}
	remoteJSON(w, v)
}

// prefetch drafts next's replies ahead of time. The capture happens here, not
// lazily inside ensure, so a screen that can't be read is skipped (and the
// real error logged) instead of being handed to Haiku as an empty capture;
// the session's own visit retries it. Stops already drafted cost nothing.
func (r *webRemote) prefetch(next swSession) {
	if !r.suggest.needsCall(next) {
		return
	}
	raw, err := r.tmux.capture(next.ClaudePane, false, remoteScreenLines)
	if err != nil {
		teardownLogf("remote: prefetch capture %s (%s): %v", next.Name, next.ClaudePane, err)
		return
	}
	screen := remoteScreenText(raw, remoteScreenLines)
	r.suggest.ensure(next, r.replies, func() string { return screen })
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

// remoteScreenText drops a capture's trailing blank lines, keeps the last n,
// and strips escapes and trailing spaces from each.
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
	// Replace sends C-u first instead of refusing to type over a draft. C-u
	// clears the current input line, so a multi-line draft may not be fully
	// cleared.
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
// the episode the page saw. An empty episode skips the check. handleSend
// always requires one; handleKey allows an empty one only for the Escape key.
// Esc/Interrupt sends none on purpose: the user wants the session stopped or
// the dialog closed, whichever it is now. A stale Esc can therefore interrupt
// a turn that started after the page last polled.
//
// When answer is set (a send, or a digit/Enter key), the write is also
// refused if an earlier answer already landed in this same episode (see
// webRemote.answered). The caller holds writeMu.
func (r *webRemote) target(w http.ResponseWriter, name, episode string, answer bool) (swSession, bool) {
	s, ok := r.lookup(name)
	if !ok {
		delete(r.answered, name)
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
	if answer && episode != "" && r.answered[name] == episode {
		remoteError(w, http.StatusConflict, "moved", "already answered; waiting for the session to move on")
		return s, false
	}
	return s, true
}

// remoteAnswerKey reports a key that answers a question (an option digit or
// Enter), which the answered-episode guard covers. Moving around a dialog
// (Up, Down, Tab, BTab, Space) and Escape are exempt and never recorded.
func remoteAnswerKey(key string) bool {
	if key == "Enter" {
		return true
	}
	return len(key) == 1 && key[0] >= '1' && key[0] <= '9'
}

// markAnswered records that an answer landed in name's episode. The caller
// holds writeMu.
func (r *webRemote) markAnswered(name, episode string) {
	if r.answered == nil {
		r.answered = map[string]string{}
	}
	r.answered[name] = episode
}

// remotePromptState classifies a capture-pane -e of the claude pane before
// the remote types into it. empty is promptEmpty's verdict; code is "" when
// it is empty and otherwise says why not:
//   - "vim_normal": vim mode is on (or the footer names a non-INSERT mode)
//     and the input box is present but not in INSERT mode, so typed text
//     would run as vim commands;
//   - "draft": the input box holds text;
//   - "unrecognized": no input box was found (a dialog, or a layout the
//     remote doesn't know).
func remotePromptState(raw string, vim bool) (empty bool, code string) {
	if promptEmpty(raw, vim) {
		return true, ""
	}
	lines := strings.Split(strings.TrimRight(raw, "\n \t"), "\n")
	for i, l := range lines {
		lines[i] = ansi.Strip(l)
	}
	bottom, top := -1, -1
	for i := len(lines) - 1; i >= 0; i-- {
		if !isRuleLine(lines[i]) {
			continue
		}
		if bottom < 0 {
			bottom = i
			continue
		}
		top = i
		break
	}
	if top < 0 || bottom-top < 2 || len(lines)-1-bottom > promptTailLines {
		return false, "unrecognized"
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[top+1]), "❯") {
		return false, "unrecognized"
	}
	tail := lines[bottom+1:]
	if vimNotInsert(tail) || (vim && !strings.Contains(strings.Join(tail, "\n"), "-- INSERT --")) {
		return false, "vim_normal"
	}
	return false, "draft"
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
	if remoteHasControl(text) {
		// A raw \x03 or \x1b would reach the pane through send-keys -l, and
		// "\x1b[201~" would end a bracketed paste early and run the rest as
		// keystrokes.
		remoteError(w, http.StatusBadRequest, "control", "the message contains control characters")
		return
	}
	if body.Episode == "" {
		remoteError(w, http.StatusBadRequest, "episode", "this send has no episode; reload the page")
		return
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if err := req.Context().Err(); err != nil {
		// The phone gave up while another write held the lock: typing now
		// would land a message nobody is waiting on.
		teardownLogf("remote: send to %s dropped, client gone: %v", body.Name, err)
		return
	}
	s, ok := r.target(w, body.Name, body.Episode, true)
	if !ok {
		return
	}
	pane := s.ClaudePane
	// done names what has already changed in the session, so a later failure
	// says what state it left behind ("partial") rather than a bare 502.
	done := ""
	fail := func(step string, err error) {
		teardownLogf("remote: send to %s failed at %s (after %q): %v", s.Name, step, done, err)
		if done == "" {
			remoteError(w, http.StatusBadGateway, "tmux", "tmux: "+err.Error())
			return
		}
		remoteError(w, http.StatusBadGateway, "partial", done+", then tmux failed: "+err.Error())
	}
	if remoteDialog(s.State) {
		if !body.Escape {
			remoteError(w, http.StatusConflict, "dialog", "a question is open: pick an option, or dismiss it and send this as a prompt")
			return
		}
		if err := r.tmux.keys(pane, "Escape"); err != nil {
			fail("escape", err)
			return
		}
		done = "dialog dismissed"
		r.sleep(remoteEscapeSettle)
	}
	raw, err := r.tmux.capture(pane, true, 0)
	if err != nil {
		fail("capture", err)
		return
	}
	if empty, code := remotePromptState(raw, r.vimMode); !empty {
		if done != "" {
			// The dialog is gone, and with it the episode the page saw: let it
			// refetch and decide about the draft against the new screen.
			teardownLogf("remote: send to %s stopped: dialog dismissed, but the prompt isn't empty (%s)", s.Name, code)
			remoteError(w, http.StatusConflict, "dismissed_draft", "dialog dismissed, but the prompt isn't empty")
			return
		}
		switch code {
		case "draft":
			// Replace is honoured only here, where a box with text in it was
			// positively recognised.
		case "vim_normal":
			remoteError(w, http.StatusConflict, "vim_normal", "claude's prompt is in vim NORMAL mode; switch it to insert mode first")
			return
		default:
			remoteError(w, http.StatusConflict, "unrecognized", "the remote doesn't recognise this screen; nothing was typed")
			return
		}
		if !body.Replace {
			remoteError(w, http.StatusConflict, "draft", "the prompt isn't empty: there is a draft in it")
			return
		}
		if err := r.tmux.keys(pane, "C-u"); err != nil {
			fail("clear", err)
			return
		}
		done = "draft cleared"
		// C-u clears one line: check the whole box is empty before typing,
		// or the message would land on what is left of the draft.
		r.sleep(teardownKeyDelay)
		raw, err = r.tmux.capture(pane, true, 0)
		if err != nil {
			fail("capture after clear", err)
			return
		}
		if empty, code := remotePromptState(raw, r.vimMode); !empty {
			teardownLogf("remote: send to %s stopped: sent C-u, but the prompt still isn't empty (%s)", s.Name, code)
			remoteError(w, http.StatusConflict, "not_cleared", "tried to clear the draft but the prompt still isn't empty; nothing was typed")
			return
		}
	}
	if err := r.tmux.typeText(pane, text); err != nil {
		fail("type", err)
		return
	}
	// Let the typed text settle before the Enter that submits it.
	r.sleep(teardownKeyDelay)
	if err := r.tmux.keys(pane, "Enter"); err != nil {
		teardownLogf("remote: send to %s typed but not submitted: %v", s.Name, err)
		remoteError(w, http.StatusBadGateway, "partial", "typed but not submitted: "+err.Error())
		return
	}
	r.markAnswered(s.Name, body.Episode)
	teardownLogf("remote: sent %d chars to %s", len(text), s.Name)
	remoteJSON(w, remoteOK{OK: true})
}

// remoteHasControl: see hasControlRunes.
func remoteHasControl(text string) bool {
	return hasControlRunes(text)
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
	if body.Episode == "" && body.Key != "Escape" {
		remoteError(w, http.StatusBadRequest, "episode", "this key has no episode; reload the page")
		return
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if err := req.Context().Err(); err != nil {
		teardownLogf("remote: key %s for %s dropped, client gone: %v", body.Key, body.Name, err)
		return
	}
	answer := remoteAnswerKey(body.Key)
	s, ok := r.target(w, body.Name, body.Episode, answer)
	if !ok {
		return
	}
	if err := r.tmux.keys(s.ClaudePane, body.Key); err != nil {
		teardownLogf("remote: key %s in %s failed: %v", body.Key, s.Name, err)
		remoteError(w, http.StatusBadGateway, "tmux", "tmux: "+err.Error())
		return
	}
	if answer {
		r.markAnswered(s.Name, body.Episode)
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
	if hasControlRunes(body.Reason) {
		remoteError(w, http.StatusBadRequest, "control", "the reason contains control characters")
		return
	}
	if rs := []rune(body.Reason); len(rs) > remoteDeferReasonMaxRunes {
		body.Reason = string(rs[:remoteDeferReasonMaxRunes])
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if err := req.Context().Err(); err != nil {
		teardownLogf("remote: defer=%v for %s dropped, client gone: %v", body.On, body.Name, err)
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
		teardownLogf("remote: defer=%v for %s failed: %v", body.On, s.Name, err)
		remoteError(w, http.StatusBadGateway, "tmux", "tmux: "+err.Error())
		return
	}
	teardownLogf("remote: defer=%v for %s", body.On, s.Name)
	remoteJSON(w, remoteOK{OK: true})
}
