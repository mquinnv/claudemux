package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The switchboard's HTTP surface: one embedded page and one JSON route,
// both GET-only and read-only, served from a webFleet (webfleet.go). The
// lobby is a TUI, so nothing here may write to stdout or stderr — the
// server's own logger is discarded and diagnostics go to teardownLogf.

//go:embed webpage.html
var webPageHTML []byte

const (
	webReadHeaderTimeout = 5 * time.Second
	webWriteTimeout      = 10 * time.Second
	webShutdownBudget    = time.Second

	// webContentSecurityPolicy locks the page route to its own origin: no
	// external scripts, styles or images (data: URIs excepted for the
	// gauges), matching the spec's "no external requests" rule.
	webContentSecurityPolicy = "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data:"
)

// webHandler routes the two paths. Method patterns make the mux answer 405
// for a POST to a known path and 404 for everything else; "/{$}" matches
// the root only, so "/index.html" is not quietly the page. allow is the
// tailnet's MagicDNS suffix and this node's own short name (weblisten.go);
// see webGuard.
func webHandler(f *webFleet, allow webAllow) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", webContentSecurityPolicy)
		_, _ = w.Write(webPageHTML)
	})
	mux.HandleFunc("GET /api/fleet", func(w http.ResponseWriter, r *http.Request) {
		// Headers go on before the marshal so an error response still
		// carries Cache-Control: no-store; http.Error overwrites
		// Content-Type itself, which is fine.
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		body, err := json.Marshal(f.view())
		if err != nil {
			teardownLogf("web: encoding fleet: %v", err)
			http.Error(w, "encoding fleet failed", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
	})
	return webRecover(webGuard(mux, allow))
}

// webTailscaleCGNAT and webTailscaleULA are the two address ranges Tailscale
// (and this tailnet's Headscale control server) assigns node addresses
// from. A RemoteAddr outside both, and outside loopback, is refused before
// it ever reaches the mux.
var (
	webTailscaleCGNAT = mustParseCIDR("100.64.0.0/10")
	webTailscaleULA   = mustParseCIDR("fd7a:115c:a1e0::/48")
)

func mustParseCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// webGuard is the tailnet-only gate the design promises ("reachable on the
// tailnet only"): it is the only thing standing between a hostile page's
// DNS-rebinding request (or a stray port-forward) and /api/fleet, which
// carries raw prompts. Two independent checks, both must pass, regardless
// of what address the server is bound to — LAN exposure is a spec
// non-goal, not something the guard assumes away.
func webGuard(next http.Handler, allow webAllow) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !webAllowedRemote(r.RemoteAddr) {
			teardownLogf("web: refused %s %s: remote %q is not on the tailnet", r.Method, r.URL.Path, r.RemoteAddr)
			http.Error(w, "forbidden: not on the tailnet", http.StatusForbidden)
			return
		}
		if !webAllowedHost(r.Host, allow) {
			teardownLogf("web: refused %s %s: host %q not recognised", r.Method, r.URL.Path, r.Host)
			http.Error(w, "forbidden: unrecognised host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// webAllowedRemote parses the connection's source IP out of RemoteAddr
// ("ip:port", possibly "[v6]:port") and allows loopback and the two
// Tailscale ranges only. A RemoteAddr that fails to parse (should not
// happen — net/http always sets it from the accepted connection) is
// refused, not let through.
func webAllowedRemote(remoteAddr string) bool {
	ip := webAddrIP(remoteAddr)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || webTailscaleCGNAT.Contains(ip) || webTailscaleULA.Contains(ip)
}

// webAllowedHost checks the request's Host header: an IP literal or
// "localhost" always passes; a name passes when it equals, or ends with,
// "." + allow.Suffix (case-insensitive, a trailing dot on the request host
// tolerated); and a single-label host (no dots) passes when it equals
// allow.ShortName — the bare node name a teammate types because the
// tailnet's MagicDNS suffix is a DNS search domain. No other single-label
// name passes. An empty allow.Suffix or allow.ShortName (no tailscale, or
// a plain 127.0.0.1 bind) means that form of match never succeeds — the
// design's explicitly acceptable fallback.
func webAllowedHost(host string, allow webAllow) bool {
	h := strings.ToLower(strings.TrimSuffix(webHostWithoutPort(host), "."))
	if h == "" {
		return false
	}
	if h == "localhost" || net.ParseIP(h) != nil {
		return true
	}
	if !strings.Contains(h, ".") && allow.ShortName != "" && h == allow.ShortName {
		return true
	}
	suffix := strings.ToLower(strings.Trim(allow.Suffix, "."))
	if suffix == "" {
		return false
	}
	return h == suffix || strings.HasSuffix(h, "."+suffix)
}

// webHostWithoutPort strips a ":port" from a Host or RemoteAddr value,
// including the brackets around an IPv6 literal that carries no port (Go's
// net.SplitHostPort only strips brackets when a port is present).
func webHostWithoutPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return host[1 : len(host)-1]
	}
	return host
}

// webAddrIP parses the IP out of a RemoteAddr; nil when it does not parse.
func webAddrIP(remoteAddr string) net.IP {
	return net.ParseIP(webHostWithoutPort(remoteAddr))
}

// webRecover keeps a handler panic on its own connection. net/http already
// recovers per request, but it logs the trace to the server's ErrorLog,
// which is discarded here — so the recover is explicit and the log is ours.
func webRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				teardownLogf("web: panic serving %s %s: %v", r.Method, r.URL.Path, p)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type webServer struct {
	srv      *http.Server
	bound    string
	stopOnce sync.Once
}

// startWebServer binds addr and serves h in the background. The bind is
// synchronous so the caller learns about a taken port or a bad address
// right here, before the TUI starts.
func startWebServer(addr string, h http.Handler) (*webServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: webReadHeaderTimeout,
		WriteTimeout:      webWriteTimeout,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			teardownLogf("web: Serve: %v", err)
		}
	}()
	return &webServer{srv: srv, bound: ln.Addr().String()}, nil
}

// addr is the address actually bound (a ":0" port resolved).
func (s *webServer) addr() string {
	if s == nil {
		return ""
	}
	return s.bound
}

// stop closes the listener, giving in-flight responses a short budget.
// Nil-safe and idempotent: the quit path defers it and the restart path
// calls it before exec, and both may run.
func (s *webServer) stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), webShutdownBudget)
		defer cancel()
		if err := s.srv.Shutdown(ctx); err != nil {
			_ = s.srv.Close()
		}
	})
}

// swWeb is everything the lobby owns for the page: the holder its polls
// publish to, the worker it pokes, and the server. Built once in
// runSwitchboard, stopped on every exit path.
type swWeb struct {
	fleet  *webFleet
	worker *webHeadlineWorker
	server *webServer
}

// startSwitchboardWeb reads web.listen and stands the page up. nil, nil
// means the page is off. An error means the page could not start — the
// lobby shows it and runs on without one. statusAllow resolves the
// tailnet's MagicDNS suffix and this node's own short name for webGuard's
// host check (weblisten.go); its own failure is not fatal to the page —
// the guard just falls back to IP literals and localhost, same as a node
// with no tailscale at all.
func startSwitchboardWeb(cfg Config, tailscaleIP func() (string, error), statusAllow func() (webAllow, error)) (*swWeb, error) {
	l, on, err := parseWebListen(cfg.Web.Listen)
	if err != nil || !on {
		return nil, err
	}
	addr, err := resolveWebListen(l, tailscaleIP)
	if err != nil {
		return nil, err
	}
	allow, err := statusAllow()
	if err != nil {
		teardownLogf("web: tailscale MagicDNS status unavailable, host check falls back to IP/localhost only: %v", err)
		allow = webAllow{}
	}
	fleet := newWebFleet()
	server, err := startWebServer(addr, webHandler(fleet, allow))
	if err != nil {
		return nil, err
	}
	worker := startHeadlineWorker(fleet, newSummarizer(cfg.Summary), cfg.Web.HeadlineInterval.Duration)
	return &swWeb{fleet: fleet, worker: worker, server: server}, nil
}

func (w *swWeb) addr() string {
	if w == nil {
		return ""
	}
	return w.server.addr()
}

func (w *swWeb) stop() {
	if w == nil {
		return
	}
	w.server.stop()
	w.worker.stop()
}
