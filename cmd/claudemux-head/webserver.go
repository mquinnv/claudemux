package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
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
)

// webHandler routes the two paths. Method patterns make the mux answer 405
// for a POST to a known path and 404 for everything else; "/{$}" matches
// the root only, so "/index.html" is not quietly the page.
func webHandler(f *webFleet) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(webPageHTML)
	})
	mux.HandleFunc("GET /api/fleet", func(w http.ResponseWriter, r *http.Request) {
		body, err := json.Marshal(f.view())
		if err != nil {
			teardownLogf("web: encoding fleet: %v", err)
			http.Error(w, "encoding fleet failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	})
	return webRecover(mux)
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
	go func() { _ = srv.Serve(ln) }()
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
