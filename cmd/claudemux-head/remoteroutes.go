package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The /remote/ routes: what the Even G2 glasses app polls and presses. They
// sit inside webHandler's mux, so webGuard (tailnet only) runs first; then
// every request but a CORS preflight needs the bearer token. Nothing a request
// carries is ever typed into a pane: an answer names an option ID, and
// sendRemoteAnswer types only what the freshly rebuilt detail offers under
// that ID.

const (
	// remoteDeferReason is the blocker recorded for a defer from the glasses.
	remoteDeferReason = "deferred from glasses"
	// remoteBodyMax caps a POST body; the largest legitimate one is an
	// answer of three short strings.
	remoteBodyMax = 4 << 10
	// remoteTranscriptEvents is how far back the transcript is read for the
	// last assistant message.
	remoteTranscriptEvents = 200
	// remoteDeferTimeout bounds the wait for the lobby's Update to take a
	// defer; well inside the server's write timeout.
	remoteDeferTimeout = 5 * time.Second
)

// remoteHub is the remote's state and its seams to tmux, the lobby and the
// model. mu guards cursor, busy, pending, answered and deferFn. suggest and
// texts carry their own locks. gather, rio and suggestFn are set once at
// construction (tests replace them before serving).
type remoteHub struct {
	fleet *webFleet
	cfg   RemoteConfig
	token string

	mu       sync.Mutex
	cursor   remoteCursor
	busy     map[string]bool      // one answer in flight per session
	pending  map[string]bool      // suggestion generations in flight, by remoteSuggestKey
	answered map[string]time.Time // session name -> the Since it was last answered in
	deferFn  func(swSession, string) error

	suggest   suggestCache
	texts     remoteTextCache
	gather    func(swSession) (remoteInputs, error)                            // reads transcript and marker, captures the pane last
	rio       func(pane string, rebuild func() (remoteDetail, error)) remoteIO // the sender's tmux calls, bound to pane
	suggestFn func(lastText string) ([]string, error)                          // nil: no suggestions
}

// newRemoteHub builds a hub wired to the live pane, transcript and model.
// sum may be nil (summaries off), which means no suggestions.
func newRemoteHub(fleet *webFleet, cfg RemoteConfig, token string, sum *Summarizer) *remoteHub {
	h := &remoteHub{
		fleet:    fleet,
		cfg:      cfg,
		token:    token,
		busy:     map[string]bool{},
		pending:  map[string]bool{},
		answered: map[string]time.Time{},
		texts:    remoteTextCache{read: lastAssistantText},
		rio:      liveRemoteIO,
	}
	h.gather = func(s swSession) (remoteInputs, error) { return gatherRemote(s, h.texts.get) }
	if sum != nil {
		h.suggestFn = func(last string) ([]string, error) {
			return sum.SuggestReplies(context.Background(), last)
		}
	}
	return h
}

// setDeferFn installs the lobby's defer path once the tea.Program exists.
// Nil-safe, so the lobby can call it whether or not the remote is on.
func (h *remoteHub) setDeferFn(fn func(swSession, string) error) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.deferFn = fn
}

// remoteDeferMsg asks the lobby's Update to defer sess; the result goes to
// done, which must be buffered.
type remoteDeferMsg struct {
	sess   swSession
	reason string
	done   chan error
}

// remoteDeferVia is the hub's deferFn in the lobby: it hands the defer to
// Update through send (tea.Program.Send) and waits up to timeout for it.
func remoteDeferVia(send func(tea.Msg), timeout time.Duration) func(swSession, string) error {
	return func(sess swSession, reason string) error {
		done := make(chan error, 1)
		// Send blocks until the program reads it; after the program has
		// exited it returns at once. Either way the wait below is bounded.
		go send(remoteDeferMsg{sess: sess, reason: reason, done: done})
		select {
		case err := <-done:
			return err
		case <-time.After(timeout):
			return errors.New("the lobby did not take the defer")
		}
	}
}

// remoteDeferCmd is Update's handling of remoteDeferMsg: the same toggle the
// lobby's own defer key writes, reported on done.
func remoteDeferCmd(msg remoteDeferMsg) tea.Cmd {
	return func() tea.Msg {
		setDeferCmd(swDeferTarget(msg.sess), true, msg.reason)()
		msg.done <- nil
		return nil
	}
}

// Wire shapes; glasses/src/types.ts mirrors them.
type remoteQueue struct {
	Pos   int `json:"pos"`
	Total int `json:"total"`
}

type remoteNextView struct {
	TakenAt string        `json:"taken_at"`
	Queue   remoteQueue   `json:"queue"`
	Working int           `json:"working"`
	Detail  *remoteDetail `json:"detail"`
}

type remoteFleetSession struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	State    string `json:"state"`
	Waiting  bool   `json:"waiting"`
	Deferred bool   `json:"deferred"`
}

type remoteFleetView struct {
	Sessions []remoteFleetSession `json:"sessions"`
}

type remoteResult struct {
	Result string `json:"result"`
}

// remoteHandler serves /remote/. CORS headers go on every response (the app
// is served from a phone-local origin), a preflight is answered without a
// token, and everything else needs the bearer token.
func remoteHandler(h *remoteHub) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /remote/next", h.serveNext)
	mux.HandleFunc("GET /remote/fleet", h.serveFleet)
	mux.HandleFunc("POST /remote/answer", h.serveAnswer)
	mux.HandleFunc("POST /remote/skip", h.serveSkip)
	mux.HandleFunc("POST /remote/defer", h.serveDefer)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Access-Control-Allow-Origin", "*")
		hd.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		hd.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		hd.Set("Access-Control-Max-Age", "600")
		hd.Set("Cache-Control", "no-store")
		hd.Set("X-Content-Type-Options", "nosniff")
		if h.token == "" {
			// Never built this way; refuse rather than accept any token.
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !h.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// authorized compares the bearer token in constant time.
func (h *remoteHub) authorized(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(h.token)) == 1
}

func writeRemoteJSON(w http.ResponseWriter, code int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		teardownLogf("remote: encoding response: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

// decodeRemoteBody reads one JSON object into v: size-limited, unknown fields
// refused, nothing after it. It writes the error response itself.
func decodeRemoteBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, remoteBodyMax))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		if dec.Decode(&struct{}{}) != io.EOF {
			err = errors.New("trailing data after the JSON object")
		}
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeRemoteJSON(w, http.StatusRequestEntityTooLarge, remoteResult{"too large"})
		} else {
			writeRemoteJSON(w, http.StatusBadRequest, remoteResult{"bad request"})
		}
		return false
	}
	return true
}

// remoteSnapshot is the unfiltered fleet and when it was taken, read
// together. The remote is the owner's own view, so private sessions show.
func (w *webFleet) remoteSnapshot() ([]swSession, time.Time) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return append([]swSession(nil), w.raw...), w.taken
}

func (h *remoteHub) lookup(name string) (swSession, bool) {
	sessions, _ := h.fleet.remoteSnapshot()
	return swSnapshot{Sessions: sessions}.session(name)
}

// detail builds what the glasses show for sess. Suggestions come only from
// the cache, never the model, so the same episode rebuilds to the same
// options; that is what lets an answer's Rebuild match the episode the
// glasses saw.
func (h *remoteHub) detail(sess swSession) (remoteDetail, remoteInputs, error) {
	in := remoteInputs{Sess: sess}
	if sess.ClaudePane != "" {
		var err error
		if in, err = h.gather(sess); err != nil {
			return remoteDetail{}, in, err
		}
	}
	in.Replies = append([]string{}, h.cfg.Replies...)
	in.Suggested = nil
	if h.cfg.Suggest {
		in.Suggested, _ = h.suggest.get(sess.Name, sess.Since)
	}
	return buildRemoteDetail(in), in, nil
}

func remoteSuggestKey(name string, since time.Time) string {
	return fmt.Sprintf("%s\x00%d", name, since.UnixNano())
}

// fillSuggestions starts at most one background generation per session
// episode, for an idle session with an empty prompt whose suggestions are not
// cached yet. The result lands in the cache for a later poll. A failure is
// cached as no suggestions, so a broken model is not asked again every poll.
func (h *remoteHub) fillSuggestions(d remoteDetail, in remoteInputs) {
	if !h.cfg.Suggest || h.suggestFn == nil || d.Kind != "idle" || d.Note != "" {
		return
	}
	name, since := in.Sess.Name, in.Sess.Since
	if _, ok := h.suggest.get(name, since); ok {
		return
	}
	key := remoteSuggestKey(name, since)
	h.mu.Lock()
	if h.pending[key] {
		h.mu.Unlock()
		return
	}
	h.pending[key] = true
	h.mu.Unlock()
	last := strings.TrimSpace(in.LastText)
	go func() {
		var v []string
		if last != "" {
			var err error
			if v, err = h.suggestFn(last); err != nil {
				teardownLogf("remote: suggesting replies for %s: %v", name, err)
				v = nil
			}
		}
		h.suggest.put(name, since, v)
		h.mu.Lock()
		delete(h.pending, key)
		h.mu.Unlock()
	}()
}

func (h *remoteHub) serveNext(w http.ResponseWriter, r *http.Request) {
	sessions, taken := h.fleet.remoteSnapshot()
	now := time.Now()
	h.mu.Lock()
	if name := r.URL.Query().Get("session"); name != "" {
		h.cursor.open(name)
	}
	sess, pos, total, ok := h.cursor.pick(sessions, now)
	h.pruneAnsweredLocked(sessions)
	sent := ok && h.answeredLocked(sess)
	h.mu.Unlock()

	v := remoteNextView{TakenAt: webTime(taken), Queue: remoteQueue{Pos: pos, Total: total}}
	for _, s := range sessions {
		if !isWaiting(s.State) {
			v.Working++
		}
	}
	if ok {
		d, in, err := h.detail(sess)
		switch {
		case err != nil:
			// Still shown, so the wearer can skip or defer it.
			teardownLogf("remote: reading %s: %v", sess.Name, err)
			d = remoteDeskDetail(sess, noteCannotRead)
		case sent:
			// Answered moments ago; the snapshot has not caught up. Offering
			// the same options again would let a second tap repeat the answer.
			d.Note, d.Options = noteSent, []remoteOption{}
			d.Episode = remoteEpisode(sess.Since, d.Kind, noteSent+"\x00"+d.Body, nil)
		default:
			h.fillSuggestions(d, in)
		}
		v.Detail = &d
	}
	writeRemoteJSON(w, http.StatusOK, v)
}

// remoteDeskDetail is a detail that offers nothing, with note saying why.
func remoteDeskDetail(sess swSession, note string) remoteDetail {
	return remoteDetail{
		Session: sess.Name,
		Title:   remoteTitle(sess),
		Kind:    "desk",
		Note:    note,
		Options: []remoteOption{},
		Episode: remoteEpisode(sess.Since, "desk", note, nil),
	}
}

// answeredLocked reports whether sess was answered in its current episode.
// The caller holds mu.
func (h *remoteHub) answeredLocked(sess swSession) bool {
	since, ok := h.answered[sess.Name]
	return ok && since.Equal(sess.Since)
}

// pruneAnsweredLocked forgets answers whose session left or whose Since
// moved on. The caller holds mu.
func (h *remoteHub) pruneAnsweredLocked(sessions []swSession) {
	for name, since := range h.answered {
		s, ok := swSnapshot{Sessions: sessions}.session(name)
		if !ok || !s.Since.Equal(since) {
			delete(h.answered, name)
		}
	}
}

func (h *remoteHub) serveFleet(w http.ResponseWriter, r *http.Request) {
	sessions, _ := h.fleet.remoteSnapshot()
	v := remoteFleetView{Sessions: []remoteFleetSession{}}
	for _, s := range sessions {
		v.Sessions = append(v.Sessions, remoteFleetSession{
			Name:     s.Name,
			Title:    remoteTitle(s),
			State:    s.State,
			Waiting:  isWaiting(s.State),
			Deferred: s.Deferred,
		})
	}
	writeRemoteJSON(w, http.StatusOK, v)
}

// remoteAnswerStatus maps sendRemoteAnswer's error to the response. Order
// matters: an unverified error wraps the rebuild failure that caused it, which
// may itself be errRemoteChanged.
func remoteAnswerStatus(err error) (int, string) {
	switch {
	case err == nil:
		return http.StatusOK, "sent"
	case errors.Is(err, errRemoteUnverified):
		return http.StatusConflict, "unverified"
	case errors.Is(err, errRemoteTyped):
		return http.StatusConflict, "typed"
	case errors.Is(err, errRemoteUnsafe):
		return http.StatusConflict, "unsafe"
	case errors.Is(err, errRemoteStuck):
		return http.StatusConflict, "stuck"
	case errors.Is(err, errRemoteDraft):
		return http.StatusConflict, "draft"
	case errors.Is(err, errRemoteChanged), errors.Is(err, errRemoteNoOpt):
		// An option the rebuilt detail does not offer: the glasses' view is
		// stale. Nothing was sent.
		return http.StatusConflict, "changed"
	}
	return http.StatusInternalServerError, "error"
}

func (h *remoteHub) serveAnswer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string `json:"session"`
		Episode string `json:"episode"`
		Option  string `json:"option"`
	}
	if !decodeRemoteBody(w, r, &req) {
		return
	}
	if req.Session == "" || req.Episode == "" || req.Option == "" {
		writeRemoteJSON(w, http.StatusBadRequest, remoteResult{"bad request"})
		return
	}
	sess, ok := h.lookup(req.Session)
	if !ok || sess.ClaudePane == "" {
		writeRemoteJSON(w, http.StatusNotFound, remoteResult{"gone"})
		return
	}
	h.mu.Lock()
	if h.answeredLocked(sess) {
		// Already answered in this episode; what the glasses saw is stale.
		h.mu.Unlock()
		writeRemoteJSON(w, http.StatusConflict, remoteResult{"changed"})
		return
	}
	if h.busy[sess.Name] {
		h.mu.Unlock()
		writeRemoteJSON(w, http.StatusConflict, remoteResult{"busy"})
		return
	}
	h.busy[sess.Name] = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.busy, sess.Name)
		h.mu.Unlock()
	}()

	// sess is fixed for the whole answer, so Rebuild reads the same pane
	// SendKey and Nudge act on, and both rebuilds hash the same Since.
	pane := sess.ClaudePane
	rebuild := func() (remoteDetail, error) {
		d, _, err := h.detail(sess)
		return d, err
	}
	err := sendRemoteAnswer(pane, req.Episode, req.Option, h.rio(pane, rebuild))
	code, result := remoteAnswerStatus(err)
	if code == http.StatusInternalServerError {
		teardownLogf("remote: answering %s: %v", sess.Name, err)
	}
	switch result {
	case "sent", "unverified", "typed":
		// Something reached the pane: remember the episode as answered, and
		// let an opened session go so the glasses move on to the queue.
		h.mu.Lock()
		h.answered[sess.Name] = sess.Since
		h.cursor.release(sess.Name)
		h.mu.Unlock()
	}
	writeRemoteJSON(w, code, remoteResult{result})
}

type remoteSessionReq struct {
	Session string `json:"session"`
}

func (h *remoteHub) serveSkip(w http.ResponseWriter, r *http.Request) {
	var req remoteSessionReq
	if !decodeRemoteBody(w, r, &req) {
		return
	}
	if req.Session == "" {
		writeRemoteJSON(w, http.StatusBadRequest, remoteResult{"bad request"})
		return
	}
	sess, ok := h.lookup(req.Session)
	if !ok {
		writeRemoteJSON(w, http.StatusNotFound, remoteResult{"gone"})
		return
	}
	h.mu.Lock()
	h.cursor.skip(sess.Name, sess.Since, time.Now())
	h.mu.Unlock()
	writeRemoteJSON(w, http.StatusOK, remoteResult{"skipped"})
}

func (h *remoteHub) serveDefer(w http.ResponseWriter, r *http.Request) {
	var req remoteSessionReq
	if !decodeRemoteBody(w, r, &req) {
		return
	}
	if req.Session == "" {
		writeRemoteJSON(w, http.StatusBadRequest, remoteResult{"bad request"})
		return
	}
	sess, ok := h.lookup(req.Session)
	if !ok {
		writeRemoteJSON(w, http.StatusNotFound, remoteResult{"gone"})
		return
	}
	h.mu.Lock()
	fn := h.deferFn
	h.mu.Unlock()
	if fn == nil {
		writeRemoteJSON(w, http.StatusServiceUnavailable, remoteResult{"error"})
		return
	}
	if err := fn(sess, remoteDeferReason); err != nil {
		teardownLogf("remote: deferring %s: %v", sess.Name, err)
		writeRemoteJSON(w, http.StatusInternalServerError, remoteResult{"error"})
		return
	}
	h.mu.Lock()
	h.cursor.release(sess.Name)
	h.mu.Unlock()
	writeRemoteJSON(w, http.StatusOK, remoteResult{"deferred"})
}

// gatherRemote reads what buildRemoteDetail needs for sess. The transcript
// and marker reads come first and the pane captures last, the plain one
// (which decides the keys) at the very end: the time between that capture and
// a keypress is the window in which the dialog can change. lastText reads a
// transcript's last assistant text (the hub's cache).
func gatherRemote(sess swSession, lastText func(path string) string) (remoteInputs, error) {
	pane := sess.ClaudePane
	if pane == "" {
		return remoteInputs{}, errors.New("session has no claude pane")
	}
	in := remoteInputs{Sess: sess}
	dir := paneMapDir()
	if path, ok := remoteTranscriptPath(dir, claudeProjectsPath(), pane); ok {
		in.LastText = lastText(path)
	}
	if id, ok := readPaneSession(dir, pane); ok {
		in.Marker, in.MarkerOK = readAskMarker(askMarkerDir(), id)
	}
	home, _ := os.UserHomeDir()
	in.VimMode = claudeVimMode(os.Getenv, home)

	capture := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
		defer cancel()
		return swTmux(ctx, append([]string{"capture-pane"}, args...)...)
	}
	var err error
	if in.CaptureE, err = capture("-e", "-p", "-t", pane); err != nil {
		return remoteInputs{}, fmt.Errorf("capture -e %s: %w", pane, err)
	}
	if in.Capture, err = capture("-p", "-t", pane); err != nil {
		return remoteInputs{}, fmt.Errorf("capture %s: %w", pane, err)
	}
	return in, nil
}

// remoteTranscriptPath finds pane's current transcript the way the head does:
// by the session id the hook recorded, under projectsDir, because the map's
// transcript_path goes stale when a session moves into a worktree. The map's
// own path is the fallback.
func remoteTranscriptPath(dir, projectsDir, pane string) (string, bool) {
	if id, ok := readPaneSession(dir, pane); ok {
		if path, found := transcriptForSession(projectsDir, id); found {
			return path, true
		}
	}
	if path, _, ok := readPaneMap(dir, pane); ok {
		return path, true
	}
	return "", false
}

// remoteTextCacheMax bounds the cache: one entry per transcript polled, and
// a long-lived lobby sees many.
const remoteTextCacheMax = 64

// remoteTextCache keeps the last assistant text per transcript, keyed by the
// file's size and mtime: a poll of an unchanged transcript costs one os.Stat.
type remoteTextCache struct {
	mu   sync.Mutex
	m    map[string]remoteTextEntry
	read func(path string) string
}

type remoteTextEntry struct {
	size int64
	mod  time.Time
	text string
}

func (c *remoteTextCache) get(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	c.mu.Lock()
	e, ok := c.m[path]
	c.mu.Unlock()
	if ok && e.size == fi.Size() && e.mod.Equal(fi.ModTime()) {
		return e.text
	}
	text := c.read(path)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= remoteTextCacheMax {
		c.m = map[string]remoteTextEntry{}
	}
	c.m[path] = remoteTextEntry{size: fi.Size(), mod: fi.ModTime(), text: text}
	return text
}

// lastAssistantText is the newest main-chain assistant text in the
// transcript at path, "" when there is none or it cannot be read.
func lastAssistantText(path string) string {
	r := newEventReader(path)
	r.SeedFromEnd(remoteTranscriptEvents)
	events, err := r.Seeded()
	if err != nil {
		return ""
	}
	for i := len(events) - 1; i >= 0; i-- {
		if mainChainText(events[i]) {
			return events[i].UserText
		}
	}
	return ""
}

// liveRemoteIO is the sender's tmux seam for pane. SendKey refuses any other
// pane, so a key can only land where Rebuild looked.
func liveRemoteIO(pane string, rebuild func() (remoteDetail, error)) remoteIO {
	return remoteIO{
		Rebuild: rebuild,
		SendKey: func(p, key string) error {
			if p != pane {
				return fmt.Errorf("send key: pane %s is not the answered pane %s", p, pane)
			}
			ctx, cancel := context.WithTimeout(context.Background(), teardownTmuxTimeout)
			defer cancel()
			_, err := swTmux(ctx, "send-keys", "-t", pane, "-l", "--", key)
			return err
		},
		Nudge: func(p, text string) nudgeOutcome {
			if p != pane {
				return nudgeRefused
			}
			return nudgeClaudePaneOutcome(p, text)
		},
		Sleep: time.Sleep,
	}
}
