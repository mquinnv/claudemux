package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func headlineResponse(text string) string {
	input, _ := json.Marshal(map[string]string{"text": text})
	return toolUseResponseRaw(headlineToolName, json.RawMessage(input))
}

func TestBuildHeadlinePromptOmitsPromptsKeepsBlockers(t *testing.T) {
	sessions := append(webTestSnapshot().Sessions, swSession{Name: "fresh", Context: -1})
	p := buildHeadlinePrompt(sessions)
	if strings.Contains(p, "run <b>the</b> tests") {
		t.Errorf("the raw prompt must never reach the headline model:\n%s", p)
	}
	for _, want := range []string{"api", "Idle", "build fixes", "fixing the build", "web", "Thinking", "blocked", "waiting on review"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Index(p, "api") > strings.Index(p, "web") || strings.Index(p, "web") > strings.Index(p, "blocked") {
		t.Errorf("sessions must keep lobby order:\n%s", p)
	}
	if !strings.Contains(p, "unknown") {
		t.Errorf("an unpublished state must read as unknown, not blank:\n%s", p)
	}
}

func TestHeadlineParsesToolCall(t *testing.T) {
	d := &fakeDoer{body: headlineResponse("Fixing the build while a deploy waits on review.")}
	got, err := testSummarizer(d).Headline(context.Background(), webTestSnapshot().Sessions)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Fixing the build while a deploy waits on review." {
		t.Errorf("got %q", got)
	}
	tools, _ := d.gotReq["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("request tools = %v, want exactly the headline tool", d.gotReq["tools"])
	}
	if choice, _ := d.gotReq["tool_choice"].(map[string]any); choice["name"] != headlineToolName {
		t.Errorf("tool_choice = %v, want forced %q", d.gotReq["tool_choice"], headlineToolName)
	}
}

func TestHeadlineRejectsPlaceholderAndTextOnly(t *testing.T) {
	for name, body := range map[string]string{"placeholder": headlineResponse("n/a"), "text-only": textOnlyResponse()} {
		d := &fakeDoer{body: body}
		if _, err := testSummarizer(d).Headline(context.Background(), webTestSnapshot().Sessions); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestHeadlineGate(t *testing.T) {
	t0 := time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)
	g := headlineGate{interval: 2 * time.Minute}
	if !g.due("a", t0) {
		t.Fatal("first snapshot must be due without waiting the interval")
	}
	if g.due("a", t0.Add(time.Second)) {
		t.Error("same fingerprint must never be due")
	}
	if g.due("b", t0.Add(time.Minute)) {
		t.Error("a change inside the interval must wait")
	}
	if !g.due("b", t0.Add(2*time.Minute)) {
		t.Error("the same change must fire once the interval has passed")
	}
	if g.due("b", t0.Add(10*time.Minute)) {
		t.Error("after firing, the fingerprint is recorded and must not fire again")
	}
	zero := headlineGate{}
	if !zero.due("a", t0) || !zero.due("b", t0) || zero.due("b", t0) {
		t.Error("a zero interval fires on every change and only on change")
	}
}

func TestHeadlineGateRetryAfterFailure(t *testing.T) {
	t0 := time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)

	// Zero interval ("no floor"): a failed call still gets a minimum
	// backoff, so a dead API is not hammered on every poll.
	g := headlineGate{}
	if !g.due("a", t0) {
		t.Fatal("first call must be due")
	}
	g.onFailure()
	if g.due("a", t0.Add(time.Second)) {
		t.Error("a retry before the minimum backoff must not be due")
	}
	if g.due("a", t0.Add(29*time.Second)) {
		t.Error("a retry just under the minimum backoff must not be due")
	}
	if !g.due("a", t0.Add(30*time.Second)) {
		t.Error("the same fingerprint must be due again once the minimum backoff has passed")
	}

	// A configured interval already exceeding the minimum backoff is left
	// alone: onFailure must not shorten it.
	g2 := headlineGate{interval: 2 * time.Minute}
	if !g2.due("a", t0) {
		t.Fatal("first call must be due")
	}
	g2.onFailure()
	if g2.due("a", t0.Add(35*time.Second)) {
		t.Error("a configured interval longer than the minimum backoff must still be honored")
	}
	if !g2.due("a", t0.Add(2*time.Minute)) {
		t.Error("the retry must be due once the configured interval has passed")
	}
}

func waitForHeadline(t *testing.T, f *webFleet, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v := f.view(); v.Headline != nil && v.Headline.Text == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("headline never became %q; view = %+v", want, f.view().Headline)
}

func TestHeadlineWorkerWritesHeadlineOnPoke(t *testing.T) {
	f := newWebFleet()
	f.publish(webTestSnapshot(), RateLimits{}, false, nil, time.Now())
	d := &fakeDoer{body: headlineResponse("Fixing the build.")}
	w := startHeadlineWorker(f, testSummarizer(d), 0)
	defer w.stop()
	w.poke()
	waitForHeadline(t, f, "Fixing the build.")
	if v := f.view(); v.Headline.Stale {
		t.Error("a headline written from the current fleet must not be stale")
	}
	// Same fleet again: no second call.
	w.poke()
	time.Sleep(50 * time.Millisecond)
	if d.calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 — an unchanged fleet must not spend a call", d.calls.Load())
	}
}

func TestHeadlineWorkerKeepsPreviousOnError(t *testing.T) {
	f := newWebFleet()
	f.publish(webTestSnapshot(), RateLimits{}, false, nil, time.Now())
	d := &fakeDoer{body: headlineResponse("First.")}
	w := startHeadlineWorker(f, testSummarizer(d), 0)
	defer w.stop()
	w.poke()
	waitForHeadline(t, f, "First.")

	d.setErr(errors.New("api down"))
	snap := webTestSnapshot()
	snap.Sessions[0].Summary = "opening the PR"
	f.publish(snap, RateLimits{}, false, nil, time.Now())
	w.poke()
	time.Sleep(100 * time.Millisecond)
	v := f.view()
	if v.Headline == nil || v.Headline.Text != "First." {
		t.Fatalf("a failed call must keep the previous headline, got %+v", v.Headline)
	}
	if !v.Headline.Stale {
		t.Error("the kept headline must read as stale against the changed fleet")
	}
}

func TestHeadlineWorkerSkipsEmptyFleet(t *testing.T) {
	f := newWebFleet()
	d := &fakeDoer{body: headlineResponse("Nothing.")}
	w := startHeadlineWorker(f, testSummarizer(d), 0)
	defer w.stop()
	w.poke()
	time.Sleep(50 * time.Millisecond)
	if d.calls.Load() != 0 {
		t.Errorf("calls = %d, want 0 — an empty fleet has nothing to headline", d.calls.Load())
	}
}

func TestHeadlineWorkerNilSummarizerIsInert(t *testing.T) {
	var w *webHeadlineWorker = startHeadlineWorker(newWebFleet(), nil, 0)
	if w != nil {
		t.Fatal("no summarizer means no worker")
	}
	w.poke() // nil-safe
	w.stop() // nil-safe
}

func TestHeadlineWorkerStopIsIdempotent(t *testing.T) {
	f := newWebFleet()
	w := startHeadlineWorker(f, testSummarizer(&fakeDoer{body: headlineResponse("x")}), 0)
	w.stop()
	w.stop() // the lobby stops the page from both its quit and restart paths
}

// waitForCalls polls d.calls until it reaches at least n, or fails the test.
// The worker runs the call in its own goroutine, so the test has to wait for
// it to start rather than assume it already has.
func waitForCalls(t *testing.T, d *fakeDoer, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if d.calls.Load() >= int64(n) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("calls never reached %d, got %d", n, d.calls.Load())
}

func TestHeadlineWorkerRetriesAfterFailure(t *testing.T) {
	f := newWebFleet()
	f.publish(webTestSnapshot(), RateLimits{}, false, nil, time.Now())
	d := &fakeDoer{body: headlineResponse("Recovered."), err: errors.New("api down")}
	// A small nonzero interval keeps this deterministic without a fake
	// clock: the backoff after a failure is exactly the interval.
	w := startHeadlineWorker(f, testSummarizer(d), 30*time.Millisecond)
	defer w.stop()

	w.poke()
	waitForCalls(t, d, 1)
	if v := f.view(); v.Headline != nil {
		t.Fatalf("a failed call must not publish a headline: %+v", v.Headline)
	}

	// Same (unchanged) fleet, poked again immediately: the gate must not
	// have latched the fingerprint as already delivered, but it also must
	// not retry before the backoff.
	w.poke()
	time.Sleep(10 * time.Millisecond)
	if d.calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 — must wait the backoff before retrying an unchanged fleet", d.calls.Load())
	}

	// Once the backoff has passed and the API recovers, the same
	// (unchanged) fingerprint must be retried and succeed.
	d.setErr(nil)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w.poke()
		if v := f.view(); v.Headline != nil && v.Headline.Text == "Recovered." {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("headline never recovered after the backoff elapsed")
}

func TestHeadlineWorkerStopCancelsInFlightCall(t *testing.T) {
	f := newWebFleet()
	f.publish(webTestSnapshot(), RateLimits{}, false, nil, time.Now())
	d := &fakeDoer{block: true}
	w := startHeadlineWorker(f, testSummarizer(d), 0)
	w.poke()
	waitForCalls(t, d, 1) // the call is now blocked inside Do, waiting on its context

	stopped := make(chan struct{})
	go func() {
		w.stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop() must cancel the in-flight call's context and return promptly, not wait out summaryRequestTimeout")
	}
}
