package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

var (
	errRemoteChanged    = errors.New("changed")        // pane no longer shows that episode; nothing sent
	errRemoteStuck      = errors.New("stuck")          // key sent, dialog still up
	errRemoteDraft      = errors.New("draft")          // prompt not empty; nothing typed
	errRemoteNoOpt      = errors.New("unknown option") // not offered by the rebuilt detail
	errRemoteUnsafe     = errors.New("unsafe")         // label not safe to type; nothing sent
	errRemoteTyped      = errors.New("typed")          // text sits unsubmitted in the prompt
	errRemoteUnverified = errors.New("unverified")     // key sent, outcome unknown
)

// remoteKeySettle is how long a pressed digit gets to take effect before the
// pane is read again.
const remoteKeySettle = 400 * time.Millisecond

// remoteIO is everything sendRemoteAnswer touches outside itself, so tests
// never run tmux.
type remoteIO struct {
	Rebuild func() (remoteDetail, error)         // re-capture and rebuild the detail now
	SendKey func(pane, key string) error         // tmux send-keys -t pane key
	Nudge   func(pane, text string) nudgeOutcome // nudgeClaudePaneOutcome
	Sleep   func(time.Duration)
}

// unsafeNudgeLabel reports whether a label must not be typed into a prompt:
// empty, or containing a control, line/paragraph separator or format rune.
// A suggested reply is model-written text, so it also may not start a slash
// or bang command.
func unsafeNudgeLabel(label string, suggested bool) bool {
	if label == "" {
		return true
	}
	for _, r := range label {
		if unicode.IsControl(r) || r == ' ' || r == ' ' || unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	if suggested && (label[0] == '!' || label[0] == '/') {
		return true
	}
	return false
}

// sendRemoteAnswer sends the answer for optionID into pane, but only if the
// pane still shows the episode the glasses were looking at and that exact
// option is offered by it. A digit key is sent at most once and never
// followed by Enter. Text options go through Nudge, which refuses unless the
// prompt is provably empty.
//
// Errors before anything is sent (errRemoteChanged, errRemoteNoOpt,
// errRemoteUnsafe, errRemoteDraft, a first Rebuild failure) mean nothing
// happened. errRemoteUnverified means a key was sent and its effect is
// unknown; errRemoteTyped means text is in the prompt but not submitted.
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
			return fmt.Errorf("%w: send key: %w", errRemoteUnverified, err)
		}
		rio.Sleep(remoteKeySettle)
		after, err := rio.Rebuild()
		if err != nil {
			return fmt.Errorf("%w: rebuild after key: %w", errRemoteUnverified, err)
		}
		if after.Episode == episode {
			return errRemoteStuck
		}
		return nil
	case strings.HasPrefix(optionID, "reply:"), strings.HasPrefix(optionID, "suggest:"):
		if unsafeNudgeLabel(opt.Label, strings.HasPrefix(optionID, "suggest:")) {
			return errRemoteUnsafe
		}
		switch rio.Nudge(pane, opt.Label) {
		case nudgeSent:
			return nil
		case nudgeTyped:
			return errRemoteTyped
		default:
			return errRemoteDraft
		}
	}
	return errRemoteNoOpt
}
