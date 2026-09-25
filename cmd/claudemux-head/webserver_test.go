package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// webTestAllowSuffix is the MagicDNS suffix most handler tests build with,
// so a name under it (not just loopback/IP literals) is reachable in tests
// that want to exercise the ordinary "teammate on the tailnet" path.
const webTestAllowSuffix = "nodes.headscale.mage.net"

func webTestHandler(t *testing.T) http.Handler {
	t.Helper()
	f := newWebFleet()
	f.publish(webTestSnapshot(), RateLimits{}, false, nil, time.Now())
	return webHandler(f, webAllow{Suffix: webTestAllowSuffix})
}

// webTestRequest builds a request that passes webGuard by default (loopback
// remote, localhost host), so tests that are not about the guard itself
// don't have to think about it. Guard-specific tests set RemoteAddr/Host
// explicitly.
func webTestRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = "127.0.0.1:9999"
	r.Host = "localhost"
	return r
}

func TestWebHandlerServesFleetJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	webTestHandler(t).ServeHTTP(rec, webTestRequest("GET", "/api/fleet"))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	var v webFleetView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("body is not webFleetView JSON: %v\n%s", err, rec.Body)
	}
	if len(v.Sessions) != 3 || v.Sessions[0].Prompt != "run <b>the</b> tests & go" {
		t.Errorf("sessions = %+v", v.Sessions)
	}
}

func TestWebHandlerServesPage(t *testing.T) {
	rec := httptest.NewRecorder()
	webTestHandler(t).ServeHTTP(rec, webTestRequest("GET", "/"))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != webContentSecurityPolicy {
		t.Errorf("Content-Security-Policy = %q, want %q", got, webContentSecurityPolicy)
	}
	body := rec.Body.String()
	for _, want := range []string{"<title>", "/api/fleet", "read-only", "tailnet"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// Every fleet string reaches the DOM through textContent. innerHTML with
	// a prompt in it would let a session's own text run as script on a
	// teammate's browser.
	if strings.Contains(body, "innerHTML") {
		t.Error("page must not use innerHTML")
	}
	if strings.Contains(body, "http://") || strings.Contains(body, "https://") {
		t.Error("page must not reference any external origin")
	}
}

func TestWebHandlerRejectsOtherMethodsAndPaths(t *testing.T) {
	h := webTestHandler(t)
	cases := []struct {
		method, path string
		want         int
	}{
		{"POST", "/", 405},
		{"POST", "/api/fleet", 405},
		{"GET", "/other", 404},
		{"GET", "/api/", 404},
		{"GET", "/index.html", 404},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, webTestRequest(c.method, c.path))
		if rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}
}

func TestWebRecoverTurnsPanicInto500(t *testing.T) {
	h := webRecover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestStartWebServerBindsAndStops(t *testing.T) {
	s, err := startWebServer("127.0.0.1:0", webTestHandler(t))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + s.addr() + "/api/fleet")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"sessions"`) {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}

	// The port is taken while the server is up: the second lobby's bind
	// fails with a reason, which Task 7 shows in the title row.
	if _, err := startWebServer(s.addr(), webTestHandler(t)); err == nil {
		t.Error("second bind on a live port must fail")
	}

	s.stop()
	s.stop() // idempotent
	var nilServer *webServer
	nilServer.stop() // nil-safe
	if _, err := http.Get("http://" + s.addr() + "/api/fleet"); err == nil {
		t.Error("GET after stop must fail")
	}
}

func TestStartSwitchboardWebOffWhenUnset(t *testing.T) {
	w, err := startSwitchboardWeb(defaultConfig(),
		func() (string, error) { return "100.64.0.15", nil },
		func() (webAllow, error) { return webAllow{}, nil })
	if err != nil || w != nil {
		t.Fatalf("got %+v, %v; want nil, nil for an empty web.listen", w, err)
	}
	w.stop() // nil-safe
	if w.addr() != "" {
		t.Error("nil swWeb must have no address")
	}
}

func TestStartSwitchboardWebBindsAndReportsResolveFailure(t *testing.T) {
	cfg := defaultConfig()
	cfg.Web.Listen = "127.0.0.1:0"
	cfg.Summary.Enabled = false // no headline worker without a key
	noSuffix := func() (webAllow, error) { return webAllow{}, nil }
	w, err := startSwitchboardWeb(cfg, func() (string, error) { return "", errors.New("unused") }, noSuffix)
	if err != nil || w == nil {
		t.Fatalf("got %+v, %v", w, err)
	}
	defer w.stop()
	if !strings.HasPrefix(w.addr(), "127.0.0.1:") {
		t.Errorf("addr = %q", w.addr())
	}
	if w.worker != nil {
		t.Error("summaries disabled must mean no headline worker")
	}
	resp, err := http.Get("http://" + w.addr() + "/api/fleet")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	cfg.Web.Listen = "tailscale:0"
	_, err = startSwitchboardWeb(cfg, func() (string, error) { return "", errors.New("tailscale ip -4: not running") }, noSuffix)
	if err == nil || !strings.Contains(err.Error(), "tailscale") {
		t.Fatalf("err = %v, want a tailscale resolve failure", err)
	}
}

// TestStartSwitchboardWebMagicDNSFailureIsNotFatal covers the design's
// "unavailable is not fatal" rule for the MagicDNS suffix lookup: the page
// still starts, just with the guard's host check falling back to IP
// literals and localhost only.
func TestStartSwitchboardWebMagicDNSFailureIsNotFatal(t *testing.T) {
	cfg := defaultConfig()
	cfg.Web.Listen = "127.0.0.1:0"
	cfg.Summary.Enabled = false
	w, err := startSwitchboardWeb(cfg,
		func() (string, error) { return "100.64.0.15", nil },
		func() (webAllow, error) { return webAllow{}, errors.New("tailscale status --json: exec: \"tailscale\": not found") })
	if err != nil || w == nil {
		t.Fatalf("a MagicDNS lookup failure must not be fatal: got %+v, %v", w, err)
	}
	w.stop()
}

// TestWebGuard covers webGuard end to end: a request must pass both the
// RemoteAddr check (loopback or a Tailscale range) and the Host check (an
// IP literal, "localhost", or a name under the tailnet's MagicDNS suffix)
// to reach the mux at all.
func TestWebGuard(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		host       string
		allow      webAllow
		want       int
	}{
		{"loopback remote, IP host", "127.0.0.1:5555", "127.0.0.1", webAllow{}, 200},
		{"tailscale v4 remote, magicdns host", "100.64.0.15:1234", "michaelsmacbookpro2-q6uplpux.nodes.headscale.mage.net", webAllow{Suffix: "nodes.headscale.mage.net"}, 200},
		{"same, trailing dot on the request host", "100.64.0.15:1234", "michaelsmacbookpro2-q6uplpux.nodes.headscale.mage.net.", webAllow{Suffix: "nodes.headscale.mage.net"}, 200},
		{"tailscale v6 ULA remote, localhost host", "[fd7a:115c:a1e0::f]:1234", "localhost", webAllow{}, 200},
		{"lan remote refused", "192.168.1.20:1234", "127.0.0.1", webAllow{}, 403},
		{"tailnet remote, unrecognised host refused", "100.64.0.15:1234", "evil.example.com", webAllow{Suffix: "nodes.headscale.mage.net"}, 403},
		{"empty suffix, dns-name host refused", "127.0.0.1:5555", "some-name.example.com", webAllow{}, 403},
		{"bare short name, ShortName set", "100.64.0.15:1234", "michaels-claudes", webAllow{Suffix: "nodes.headscale.mage.net", ShortName: "michaels-claudes"}, 200},
		{"bare short name, upper-case with port", "100.64.0.15:1234", "MICHAELS-CLAUDES:7474", webAllow{Suffix: "nodes.headscale.mage.net", ShortName: "michaels-claudes"}, 200},
		{"different single-label name refused", "100.64.0.15:1234", "other-node", webAllow{Suffix: "nodes.headscale.mage.net", ShortName: "michaels-claudes"}, 403},
		{"bare short name, empty ShortName refused", "100.64.0.15:1234", "michaels-claudes", webAllow{Suffix: "nodes.headscale.mage.net"}, 403},
		{"unrecognised host still refused even with ShortName set", "100.64.0.15:1234", "evil.example.com", webAllow{Suffix: "nodes.headscale.mage.net", ShortName: "michaels-claudes"}, 403},
		{"full node name under suffix still 200 with ShortName set", "100.64.0.15:1234", "michaels-claudes.nodes.headscale.mage.net", webAllow{Suffix: "nodes.headscale.mage.net", ShortName: "michaels-claudes"}, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newWebFleet()
			f.publish(webTestSnapshot(), RateLimits{}, false, nil, time.Now())
			h := webHandler(f, c.allow)
			req := httptest.NewRequest("GET", "/api/fleet", nil)
			req.RemoteAddr = c.remoteAddr
			req.Host = c.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, c.want, rec.Body)
			}
		})
	}
}

func TestWebAllowedRemote(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:9", true},
		{"[::1]:9", true},
		{"[::1]", true}, // no port
		{"100.64.0.15:1234", true},
		{"100.127.255.254:1", true},
		{"[fd7a:115c:a1e0::f]:1", true},
		{"100.63.0.1:1", false},  // just outside the CGNAT /10
		{"100.128.0.1:1", false}, // just outside the CGNAT /10
		{"192.168.1.20:1234", false},
		{"8.8.8.8:53", false},
		{"not-an-addr", false},
		{"", false},
	}
	for _, c := range cases {
		if got := webAllowedRemote(c.addr); got != c.want {
			t.Errorf("webAllowedRemote(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestWebAllowedHost(t *testing.T) {
	cases := []struct {
		host  string
		allow webAllow
		want  bool
	}{
		{"127.0.0.1", webAllow{}, true},
		{"127.0.0.1:7474", webAllow{}, true},
		{"[::1]", webAllow{}, true},
		{"[::1]:7474", webAllow{}, true},
		{"localhost", webAllow{}, true},
		{"LOCALHOST:7474", webAllow{}, true},
		{"node.nodes.headscale.mage.net", webAllow{Suffix: "nodes.headscale.mage.net"}, true},
		{"node.nodes.headscale.mage.net.", webAllow{Suffix: "nodes.headscale.mage.net"}, true}, // trailing dot
		{"NODE.NODES.HEADSCALE.MAGE.NET", webAllow{Suffix: "nodes.headscale.mage.net"}, true},  // case-insensitive
		{"nodes.headscale.mage.net", webAllow{Suffix: "nodes.headscale.mage.net"}, true},       // the suffix itself
		{"evil-nodes.headscale.mage.net", webAllow{Suffix: "nodes.headscale.mage.net"}, false},
		{"node.nodes.headscale.mage.net.evil.com", webAllow{Suffix: "nodes.headscale.mage.net"}, false},
		{"evil.example.com", webAllow{Suffix: "nodes.headscale.mage.net"}, false},
		{"some-name.example.com", webAllow{}, false},
		{"", webAllow{}, false},
		// The bare short name: a single-label host equal to allow.ShortName.
		{"michaels-claudes", webAllow{Suffix: "nodes.headscale.mage.net", ShortName: "michaels-claudes"}, true},
		{"MICHAELS-CLAUDES:7474", webAllow{Suffix: "nodes.headscale.mage.net", ShortName: "michaels-claudes"}, true},
		{"other-node", webAllow{Suffix: "nodes.headscale.mage.net", ShortName: "michaels-claudes"}, false},
		{"michaels-claudes", webAllow{Suffix: "nodes.headscale.mage.net"}, false}, // ShortName empty
		{"michaels-claudes", webAllow{}, false},                                  // ShortName empty, no suffix either
		{"michaels-claudes.nodes.headscale.mage.net", webAllow{Suffix: "nodes.headscale.mage.net", ShortName: "michaels-claudes"}, true},
	}
	for _, c := range cases {
		if got := webAllowedHost(c.host, c.allow); got != c.want {
			t.Errorf("webAllowedHost(%q, %+v) = %v, want %v", c.host, c.allow, got, c.want)
		}
	}
}
