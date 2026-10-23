package main

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// web.hide_private_orgs keeps a configured GitHub owner's PRIVATE repos off
// the web status page, while that owner's public repos (and every other
// owner's repos) still show. A session's repo is its tmux session_path's git
// origin; whether the repo is private is asked of GitHub's REST API
// anonymously, where a private repo answers 404 exactly like a missing one —
// so "not visible to an anonymous caller" is the definition, with no token
// to manage.
//
// The filter fails closed: a session in a listed owner's repo stays hidden
// until a lookup has said the repo is public, and any lookup failure (network,
// rate limit, odd status) hides it and retries on the next pass. A session
// the resolver has never seen is hidden too, until the next pass resolves it
// — a brand-new private session never flashes onto a shared page.
//
// Sessions whose directory is not a git checkout, has no origin, or whose
// origin is not on github.com are shown: none of them can be a listed owner's
// GitHub repo.

// webVisTTL is how long a visibility answer is trusted. A repo flipped from
// private to public shows up within this; one flipped the other way leaks
// for at most this long, which is why it is not days.
const webVisTTL = time.Hour

// webVisPass is how often the resolver re-reads tmux's session paths.
const webVisPass = 30 * time.Second

var githubOriginRe = regexp.MustCompile(`github\.com[:/]+([^/]+)/([^/]+?)(?:\.git)?/?$`)

// parseGitHubOrigin returns owner and repo from an origin URL, ok=false when
// the origin is not a github.com repo.
func parseGitHubOrigin(origin string) (owner, repo string, ok bool) {
	m := githubOriginRe.FindStringSubmatch(strings.TrimSpace(origin))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

type webVisEntry struct {
	private bool
	at      time.Time
}

// webPrivacy decides which sessions the page may show.
type webPrivacy struct {
	orgs map[string]bool // lowercased owners whose private repos are hidden

	// Seams for tests.
	listSessions func(ctx context.Context) (map[string]string, error) // name → path
	originOf     func(ctx context.Context, dir string) string
	isPrivate    func(ctx context.Context, owner, repo string) (bool, error)
	now          func() time.Time

	mu     sync.RWMutex
	hidden map[string]bool        // session name → hide; absent = unresolved
	cache  map[string]webVisEntry // "owner/repo" lowercased → answer

	kick chan struct{}
	done chan struct{}
	once sync.Once
}

func newWebPrivacy(orgs []string) *webPrivacy {
	p := &webPrivacy{
		orgs:         map[string]bool{},
		listSessions: tmuxSessionPaths,
		originOf:     gitOrigin,
		isPrivate:    githubRepoPrivate,
		now:          time.Now,
		hidden:       map[string]bool{},
		cache:        map[string]webVisEntry{},
		kick:         make(chan struct{}, 1),
		done:         make(chan struct{}),
	}
	for _, o := range orgs {
		if o = strings.ToLower(strings.TrimSpace(o)); o != "" {
			p.orgs[o] = true
		}
	}
	return p
}

// active reports whether any owner is configured; with none the filter is a
// no-op and no resolver runs.
func (p *webPrivacy) active() bool { return p != nil && len(p.orgs) > 0 }

// visible filters sessions down to the ones the page may show, and pokes the
// resolver when it meets a session it has not resolved yet.
//
// The fleet arrives already arranged into handoff families (swArrangeFamilies:
// pre-order, parents first), and dropping rows must not leave the tree lying
// about what remains. Two repairs, both failing closed:
//   - A row whose parent row was dropped becomes a root (depth 0), and its
//     own descendants nest under it from there. It is not hung on the
//     nearest shown ancestor: that session did not launch it.
//   - From (the orphan tag) names a session the snapshot may not hold — a
//     gone parent, or one whose name was reused — so the filter never saw
//     it. It is published only when that name is resolved and shown; any
//     other answer blanks it, so a private session's name cannot ride onto
//     the page as "← name".
func (p *webPrivacy) visible(sessions []swSession) []swSession {
	if !p.active() {
		return sessions
	}
	p.mu.RLock()
	out := make([]swSession, 0, len(sessions))
	unknown := false
	// anc[d] is the row's ancestor at original depth d: whether it is shown,
	// and the depth it was given if so.
	type ancestor struct {
		shown bool
		depth int
	}
	var anc []ancestor
	for _, s := range sessions {
		hide, known := p.hidden[s.Name]
		if !known {
			unknown = true
		}
		shown := known && !hide
		d := s.Depth
		if d > len(anc) {
			d = len(anc) // malformed depth; treat as a child of the last row
		}
		anc = anc[:d]
		newDepth := 0
		if d > 0 && anc[d-1].shown {
			newDepth = anc[d-1].depth + 1
		}
		anc = append(anc, ancestor{shown: shown, depth: newDepth})
		if !shown {
			continue
		}
		if s.Depth != newDepth {
			// Re-rooted: the glyph string described the old nesting. The
			// page draws by Depth alone and never reads it.
			s.TreePrefix = ""
		}
		s.Depth = newDepth
		if s.From != "" {
			if fh, fk := p.hidden[s.From]; !fk || fh {
				s.From = ""
			}
		}
		out = append(out, s)
	}
	p.mu.RUnlock()
	if unknown {
		p.poke()
	}
	return out
}

func (p *webPrivacy) poke() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

func (p *webPrivacy) start() {
	if !p.active() {
		return
	}
	go p.run()
}

func (p *webPrivacy) stop() {
	if p == nil {
		return
	}
	p.once.Do(func() { close(p.done) })
}

func (p *webPrivacy) run() {
	t := time.NewTicker(webVisPass)
	defer t.Stop()
	for {
		p.pass()
		select {
		case <-p.done:
			return
		case <-t.C:
		case <-p.kick:
		}
	}
}

// pass re-resolves every session. The new map replaces the old one whole, so
// a session that went away stops being remembered.
func (p *webPrivacy) pass() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	paths, err := p.listSessions(ctx)
	if err != nil {
		teardownLogf("web: privacy filter could not list sessions: %v", err)
		return // keep the last answers; unknown sessions stay hidden
	}
	next := make(map[string]bool, len(paths))
	for name, dir := range paths {
		next[name] = p.hide(ctx, dir)
	}
	p.mu.Lock()
	p.hidden = next
	p.mu.Unlock()
}

// hide decides one directory, failing closed for listed owners.
func (p *webPrivacy) hide(ctx context.Context, dir string) bool {
	owner, repo, ok := parseGitHubOrigin(p.originOf(ctx, dir))
	if !ok || !p.orgs[strings.ToLower(owner)] {
		return false
	}
	key := strings.ToLower(owner + "/" + repo)
	now := p.now()
	p.mu.RLock()
	e, cached := p.cache[key]
	p.mu.RUnlock()
	if cached && now.Sub(e.at) < webVisTTL {
		return e.private
	}
	priv, err := p.isPrivate(ctx, owner, repo)
	if err != nil {
		teardownLogf("web: privacy filter could not check %s/%s, hiding it: %v", owner, repo, err)
		return true
	}
	p.mu.Lock()
	p.cache[key] = webVisEntry{private: priv, at: now}
	p.mu.Unlock()
	return priv
}

func tmuxSessionPaths(ctx context.Context) (map[string]string, error) {
	out, err := exec.CommandContext(ctx, "tmux", "list-sessions", "-F", "#{session_name}\t#{session_path}").Output()
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		name, path, ok := strings.Cut(line, "\t")
		if ok && name != "" {
			m[name] = path
		}
	}
	return m, nil
}

func gitOrigin(ctx context.Context, dir string) string {
	if dir == "" {
		return ""
	}
	out, err := gitCmd(ctx, dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

var webVisClient = &http.Client{Timeout: 10 * time.Second}

// githubRepoPrivate asks GitHub anonymously. 200 is public; 404 is private
// (or gone — hidden either way). Anything else is an error, which the caller
// turns into "hidden".
func githubRepoPrivate(ctx context.Context, owner, repo string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://api.github.com/repos/"+owner+"/"+repo, nil)
	if err != nil {
		return true, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "claudemux")
	resp, err := webVisClient.Do(req)
	if err != nil {
		return true, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return false, nil
	case http.StatusNotFound:
		return true, nil
	default:
		return true, fmt.Errorf("github answered %s", resp.Status)
	}
}
