package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func webTestHandler(t *testing.T) http.Handler {
	t.Helper()
	f := newWebFleet()
	f.publish(webTestSnapshot(), RateLimits{}, false, nil, time.Now())
	return webHandler(f)
}

func TestWebHandlerServesFleetJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	webTestHandler(t).ServeHTTP(rec, httptest.NewRequest("GET", "/api/fleet", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
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
	webTestHandler(t).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
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
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
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
