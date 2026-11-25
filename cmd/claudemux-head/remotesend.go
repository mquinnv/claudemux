package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	errRemoteChanged = errors.New("changed")        // pane no longer shows that episode; nothing sent
	errRemoteStuck   = errors.New("stuck")          // key sent, dialog still up
	errRemoteDraft   = errors.New("draft")          // prompt not empty; nothing typed
	errRemoteNoOpt   = errors.New("unknown option") // not offered by the rebuilt detail
)

// remoteKeySettle is how long a pressed digit gets to take effect before the
// pane is read again.
const remoteKeySettle = 400 * time.Millisecond

// remoteIO is everything sendRemoteAnswer touches outside itself, so tests
// never run tmux.
type remoteIO struct {
	Rebuild func() (remoteDetail, error) // re-capture and rebuild the detail now
	SendKey func(pane, key string) error // tmux send-keys -t pane key
	Nudge   func(pane, text string) bool // nudgeClaudePane
	Sleep   func(time.Duration)
}

// sendRemoteAnswer sends the answer for optionID into pane, but only if the
// pane still shows the episode the glasses were looking at and that exact
// option is offered by it. A digit key is sent at most once and never
// followed by Enter. Text options go through Nudge, which refuses unless the
// prompt is provably empty.
func sendRemoteAnswer(pane, episode, optionID string, rio remoteIO) error {
	d, err := rio.Rebuild()
	if err != nil {
		return fmt.Errorf("rebuild: %w", err)
	}
	if d.Episode != episode {
		return errRemoteChanged
	}
	var opt *remoteOption
	for i := range d.Options {
		if d.Options[i].ID == optionID {
			opt = &d.Options[i]
			break
		}
	}
	if opt == nil {
		return errRemoteNoOpt
	}

	switch {
	case strings.HasPrefix(optionID, "key:"):
		key := strings.TrimPrefix(optionID, "key:")
		if len(key) != 1 || key[0] < '1' || key[0] > '9' {
			return errRemoteNoOpt
		}
		if err := rio.SendKey(pane, key); err != nil {
			return fmt.Errorf("send key: %w", err)
		}
		rio.Sleep(remoteKeySettle)
		after, err := rio.Rebuild()
		if err != nil {
			return fmt.Errorf("rebuild after key: %w", err)
		}
		if after.Episode == episode {
			return errRemoteStuck
		}
		return nil
	case strings.HasPrefix(optionID, "reply:"), strings.HasPrefix(optionID, "suggest:"):
		if !rio.Nudge(pane, opt.Label) {
			return errRemoteDraft
		}
		return nil
	}
	return errRemoteNoOpt
}
