package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseGitHubOrigin(t *testing.T) {
	cases := []struct {
		in, owner, repo string
		ok              bool
	}{
		{"git@github.com:mquinnv/menuops.git\n", "mquinnv", "menuops", true},
		{"https://github.com/mquinnv/claudemux", "mquinnv", "claudemux", true},
		{"https://github.com/ameriglide/beejax.git/", "ameriglide", "beejax", true},
		{"ssh://git@github.com/phenixcrm/clean.git", "phenixcrm", "clean", true},
		{"git@gitlab.com:mquinnv/x.git", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		o, r, ok := parseGitHubOrigin(c.in)
		if o != c.owner || r != c.repo || ok != c.ok {
			t.Errorf("parseGitHubOrigin(%q) = %q, %q, %v; want %q, %q, %v", c.in, o, r, ok, c.owner, c.repo, c.ok)
		}
	}
}

// fakePrivacy wires a webPrivacy to canned sessions, origins and answers.
func fakePrivacy(orgs []string, paths, origins map[string]string, private map[string]bool, fail map[string]bool) (*webPrivacy, *int) {
	calls := 0
	p := newWebPrivacy(orgs)
	p.listSessions = func(context.Context) (map[string]string, error) { return paths, nil }
	p.originOf = func(_ context.Context, dir string) string { return origins[dir] }
	p.isPrivate = func(_ context.Context, owner, repo string) (bool, error) {
		calls++
		k := owner + "/" + repo
		if fail[k] {
			return true, errors.New("rate limited")
		}
		return private[k], nil
	}
	return p, &calls
}

func names(ss []swSession) string {
	var n []string
	for _, s := range ss {
		n = append(n, s.Name)
	}
	return strings.Join(n, ",")
}

func TestWebPrivacyHidesOnlyPrivateReposOfListedOrgs(t *testing.T) {
	p, _ := fakePrivacy([]string{"MQuinnV"},
		map[string]string{"menu": "/m", "mux": "/c", "work": "/w", "loose": "/l", "down": "/d"},
		map[string]string{
			"/m": "git@github.com:mquinnv/menuops.git",
			"/c": "git@github.com:mquinnv/claudemux.git",
			"/w": "git@github.com:ameriglide/beejax.git", // private, but not a listed org
			"/l": "",                                     // not a git checkout
			"/d": "git@github.com:mquinnv/down.git",
		},
		map[string]bool{"mquinnv/menuops": true, "ameriglide/beejax": true},
		map[string]bool{"mquinnv/down": true},
	)
	fleet := []swSession{{Name: "menu"}, {Name: "mux"}, {Name: "work"}, {Name: "loose"}, {Name: "down"}, {Name: "new"}}

	// Before any pass every session is unresolved, so nothing shows.
	if got := names(p.visible(fleet)); got != "" {
		t.Fatalf("before a pass visible = %q, want none (fail closed)", got)
	}
	p.pass()
	// down's lookup failed → hidden; new was never listed → hidden.
	if got, want := names(p.visible(fleet)), "mux,work,loose"; got != want {
		t.Fatalf("visible = %q, want %q", got, want)
	}
}

func TestWebPrivacyCachesAnswers(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	p, calls := fakePrivacy([]string{"mquinnv"},
		map[string]string{"a": "/m", "b": "/m"},
		map[string]string{"/m": "git@github.com:mquinnv/menuops.git"},
		map[string]bool{"mquinnv/menuops": true}, nil)
	p.now = func() time.Time { return now }
	p.pass()
	p.pass()
	if *calls != 1 {
		t.Fatalf("lookups = %d, want 1 within the TTL", *calls)
	}
	now = now.Add(webVisTTL + time.Second)
	p.pass()
	if *calls != 2 {
		t.Fatalf("lookups = %d, want 2 after the TTL", *calls)
	}
}

func TestWebPrivacyFailedLookupIsNotCached(t *testing.T) {
	p, calls := fakePrivacy([]string{"mquinnv"},
		map[string]string{"a": "/m"},
		map[string]string{"/m": "git@github.com:mquinnv/menuops.git"},
		nil, map[string]bool{"mquinnv/menuops": true})
	p.pass()
	p.pass()
	if *calls != 2 {
		t.Fatalf("lookups = %d, want a retry on every pass after a failure", *calls)
	}
}

func TestWebPrivacyInactiveIsNoOp(t *testing.T) {
	fleet := []swSession{{Name: "x"}}
	var nilP *webPrivacy
	if got := names(nilP.visible(fleet)); got != "x" {
		t.Fatalf("nil privacy visible = %q", got)
	}
	if got := names(newWebPrivacy(nil).visible(fleet)); got != "x" {
		t.Fatalf("no-org privacy visible = %q", got)
	}
}

func TestWebFleetPublishFiltersBeforeHeadline(t *testing.T) {
	p, _ := fakePrivacy([]string{"mquinnv"},
		map[string]string{"secret": "/m", "open": "/c"},
		map[string]string{"/m": "git@github.com:mquinnv/menuops.git", "/c": "git@github.com:mquinnv/claudemux.git"},
		map[string]bool{"mquinnv/menuops": true}, nil)
	p.pass()
	f := newWebFleet()
	f.privacy = p
	f.publish(swSnapshot{Sessions: []swSession{{Name: "secret", Context: -1}, {Name: "open", Context: -1}}}, RateLimits{}, false, nil, time.Now())
	if got := names(f.sessions()); got != "open" {
		t.Fatalf("headline sessions = %q, want only open", got)
	}
	if v := f.view(); v.Counts.Sessions != 1 || len(v.Sessions) != 1 || v.Sessions[0].Name != "open" {
		t.Fatalf("view = %+v, want only open", v)
	}
}

func TestRenderWebPageTitle(t *testing.T) {
	if got := string(renderWebPage("")); !strings.Contains(got, "<h1>claudemux fleet</h1>") {
		t.Fatal("default title missing")
	}
	got := string(renderWebPage(`What <Michael> is Working On`))
	for _, want := range []string{
		"<title>What &lt;Michael&gt; is Working On</title>",
		"<h1>What &lt;Michael&gt; is Working On</h1>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered page lacks %q", want)
		}
	}
}

func TestWebViewMeterAndModelFields(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	rl := RateLimits{}
	rl.FiveHour.UsedPercent = 90
	rl.FiveHour.ResetsAt = now.Add(4 * time.Hour) // 20% elapsed, 90% used → red
	rl.SevenDay.UsedPercent = 10
	rl.SevenDay.ResetsAt = now.Add(24 * time.Hour) // 6/7 elapsed, 10% used → green
	snap := swSnapshot{Sessions: []swSession{{Name: "a", Context: 90, Model: "claude-opus-5-5"}}}
	v := buildWebFleetView(snap, rl, true, nil, now, webHeadline{})
	if v.Budget.FiveHour.Color != thresholdColor(100) {
		t.Errorf("5h color = %q, want red", v.Budget.FiveHour.Color)
	}
	if v.Budget.Weekly.Color != thresholdColor(0) {
		t.Errorf("week color = %q, want green", v.Budget.Weekly.Color)
	}
	s := v.Sessions[0]
	if s.CtxColor != thresholdColor(90) {
		t.Errorf("ctx color = %q, want red", s.CtxColor)
	}
	if s.Model != "opus 5.5" {
		t.Errorf("model = %q, want %q", s.Model, "opus 5.5")
	}
}
