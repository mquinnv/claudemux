package main

import (
	"errors"
	"testing"
	"time"
)

type sendFake struct {
	details  []remoteDetail // returned by successive Rebuild calls; last repeats
	rebuilds int
	keys     []string
	nudges   []string
	nudgeOut nudgeOutcome
	rebErrs  map[int]error // per-call Rebuild errors, by zero-based call index
	keyErr   error
	rebErr   error
	sleeps   []time.Duration
}

func (f *sendFake) io() remoteIO {
	return remoteIO{
		Rebuild: func() (remoteDetail, error) {
			i := f.rebuilds
			f.rebuilds++
			if e := f.rebErrs[i]; e != nil {
				return remoteDetail{}, e
			}
			if f.rebErr != nil {
				return remoteDetail{}, f.rebErr
			}
			if i >= len(f.details) {
				i = len(f.details) - 1
			}
			return f.details[i], nil
		},
		SendKey: func(pane, key string) error {
			f.keys = append(f.keys, pane+":"+key)
			return f.keyErr
		},
		Nudge: func(pane, text string) nudgeOutcome {
			f.nudges = append(f.nudges, pane+":"+text)
			return f.nudgeOut
		},
		Sleep: func(d time.Duration) { f.sleeps = append(f.sleeps, d) },
	}
}

func threeRows(ep string) remoteDetail {
	return remoteDetail{Episode: ep, Options: []remoteOption{
		{ID: "key:1", Label: "Yes"}, {ID: "key:2", Label: "Yes, always"}, {ID: "key:3", Label: "No"},
	}}
}

func TestSendRemoteAnswerKey(t *testing.T) {
	f := &sendFake{details: []remoteDetail{threeRows("a"), {Episode: "b"}}}
	if err := sendRemoteAnswer("%1", "a", "key:2", f.io()); err != nil {
		t.Fatal(err)
	}
	if len(f.keys) != 1 || f.keys[0] != "%1:2" {
		t.Fatalf("keys = %v", f.keys)
	}
	if len(f.sleeps) != 1 || f.sleeps[0] != 400*time.Millisecond {
		t.Fatalf("sleeps = %v", f.sleeps)
	}
}

func TestSendRemoteAnswerChanged(t *testing.T) {
	f := &sendFake{details: []remoteDetail{threeRows("other")}}
	err := sendRemoteAnswer("%1", "a", "key:2", f.io())
	if !errors.Is(err, errRemoteChanged) || len(f.keys) != 0 || len(f.nudges) != 0 {
		t.Fatalf("err=%v keys=%v nudges=%v", err, f.keys, f.nudges)
	}
}

func TestSendRemoteAnswerStuck(t *testing.T) {
	f := &sendFake{details: []remoteDetail{threeRows("a")}}
	err := sendRemoteAnswer("%1", "a", "key:1", f.io())
	if !errors.Is(err, errRemoteStuck) || len(f.keys) != 1 {
		t.Fatalf("err=%v keys=%v", err, f.keys)
	}
}

func TestSendRemoteAnswerReply(t *testing.T) {
	d := remoteDetail{Episode: "a", Options: []remoteOption{{ID: "reply:0", Label: "Yes, go ahead"}}}
	f := &sendFake{details: []remoteDetail{d}, nudgeOut: nudgeSent}
	if err := sendRemoteAnswer("%1", "a", "reply:0", f.io()); err != nil {
		t.Fatal(err)
	}
	if len(f.nudges) != 1 || f.nudges[0] != "%1:Yes, go ahead" || len(f.keys) != 0 {
		t.Fatalf("nudges=%v keys=%v", f.nudges, f.keys)
	}
}

func TestSendRemoteAnswerSuggest(t *testing.T) {
	d := remoteDetail{Episode: "a", Options: []remoteOption{{ID: "suggest:1", Label: "run it"}}}
	f := &sendFake{details: []remoteDetail{d}, nudgeOut: nudgeSent}
	if err := sendRemoteAnswer("%1", "a", "suggest:1", f.io()); err != nil {
		t.Fatal(err)
	}
	if len(f.nudges) != 1 || f.nudges[0] != "%1:run it" {
		t.Fatalf("nudges=%v", f.nudges)
	}
}

func TestSendRemoteAnswerDraft(t *testing.T) {
	d := remoteDetail{Episode: "a", Options: []remoteOption{{ID: "reply:0", Label: "Continue"}}}
	f := &sendFake{details: []remoteDetail{d}, nudgeOut: nudgeRefused}
	err := sendRemoteAnswer("%1", "a", "reply:0", f.io())
	if !errors.Is(err, errRemoteDraft) {
		t.Fatalf("err=%v", err)
	}
}

func TestSendRemoteAnswerUnknownOption(t *testing.T) {
	for _, id := range []string{"key:9", "", "bogus", "reply:0", "key:", "key:12", "key:0", "key:2\n"} {
		f := &sendFake{details: []remoteDetail{threeRows("a")}}
		err := sendRemoteAnswer("%1", "a", id, f.io())
		if !errors.Is(err, errRemoteNoOpt) || len(f.keys) != 0 || len(f.nudges) != 0 {
			t.Fatalf("id %q: err=%v keys=%v nudges=%v", id, err, f.keys, f.nudges)
		}
	}
}

func TestSendRemoteAnswerRejectsBadDigitEvenIfOffered(t *testing.T) {
	for _, id := range []string{"key:12", "key:0", "key:x", "key:"} {
		d := remoteDetail{Episode: "a", Options: []remoteOption{{ID: id, Label: "odd"}}}
		f := &sendFake{details: []remoteDetail{d}}
		err := sendRemoteAnswer("%1", "a", id, f.io())
		if !errors.Is(err, errRemoteNoOpt) || len(f.keys) != 0 {
			t.Fatalf("id %q: err=%v keys=%v", id, err, f.keys)
		}
	}
}

func TestSendRemoteAnswerNeverOffersFreeText(t *testing.T) {
	// A free-text row 4 is never in Options, so its ID is refused.
	f := &sendFake{details: []remoteDetail{threeRows("a")}}
	err := sendRemoteAnswer("%1", "a", "key:4", f.io())
	if !errors.Is(err, errRemoteNoOpt) || len(f.keys) != 0 {
		t.Fatalf("err=%v keys=%v", err, f.keys)
	}
}

func TestSendRemoteAnswerRebuildError(t *testing.T) {
	boom := errors.New("boom")
	f := &sendFake{rebErr: boom}
	err := sendRemoteAnswer("%1", "a", "key:1", f.io())
	if !errors.Is(err, boom) || len(f.keys) != 0 {
		t.Fatalf("err=%v keys=%v", err, f.keys)
	}
}

func TestSendRemoteAnswerSendKeyError(t *testing.T) {
	boom := errors.New("tmux gone")
	f := &sendFake{details: []remoteDetail{threeRows("a")}, keyErr: boom}
	err := sendRemoteAnswer("%1", "a", "key:1", f.io())
	if !errors.Is(err, boom) || len(f.keys) != 1 || f.rebuilds != 1 {
		t.Fatalf("err=%v keys=%v rebuilds=%d", err, f.keys, f.rebuilds)
	}
}
