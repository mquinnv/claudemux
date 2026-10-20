package main

import "strings"

// The #stay marker: a one-shot, per-prompt opt-out of the conductor's escort.
// A prompt whose first or last word is #stay hands the session to Claude
// WITHOUT freeing the conductor to carry the human on — they stay where they
// are for that turn. The next prompt typed without the marker behaves
// normally again.
//
// "stay" is the word the status pane already uses for standby (⏸ stay): the
// marker is the same promise, scoped to one submission instead of the fleet.
//
// Matching is deliberately narrow so real prompt text cannot trip it:
//   - a whole whitespace-delimited token, case-insensitive: "#stay", "#Stay",
//     "#STAY" — but not "#stayed", "#stay-alive", "#stay.", "foo#stay" (a URL
//     fragment) or "`#stay`" (quoting it in backticks is how you talk about
//     the marker without triggering it);
//   - "#" glued to the word, so a markdown heading ("# Stay") never matches;
//   - only as the prompt's first or last token, so a pasted log, document or
//     code block that happens to contain the tag mid-text does not match.
//
// Claude sees the marker too — Claude Code gives hooks no way to rewrite a
// prompt, only to add context — and reads a trailing hashtag as the tag it
// is. The end of the prompt is the recommended spot.
const stayMarker = "#stay"

// stayMarkerOption carries whether the session's current turn stays (see
// turnStays: a #stay-marked prompt or a "Chat about this" answer): "1" when
// it does, unset otherwise. It is published in the same tmux
// invocation as @claudemux_state (statePublishArgs) because the marked prompt
// and the busy state it causes come from the same transcript record: publishing
// them separately would let a lobby poll land between the two and read the
// busy state as an unmarked hand-back — escorting the user away before the
// mark arrived.
const stayMarkerOption = "@claudemux_stay"

// stayMarked reports whether prompt carries the #stay marker as its first or
// last whitespace-delimited token.
func stayMarked(prompt string) bool {
	f := strings.Fields(prompt)
	if len(f) == 0 {
		return false
	}
	return strings.EqualFold(f[0], stayMarker) || strings.EqualFold(f[len(f)-1], stayMarker)
}

// clarifyResultPrefix opens the tool_result Claude Code writes when the user
// answers an AskUserQuestion with "Chat about this": the call is rejected
// (is_error) and the model resumes the SAME turn to ask what they want to
// discuss. To the conductor that looks exactly like a hand-back — the session
// goes from Asking to busy with no typed prompt in between — so without this
// the human is escorted away at the very moment they asked to talk.
const clarifyResultPrefix = "The user wants to clarify these questions."

// turnStays reports whether the turn the session is on was started by
// something that asked the conductor to leave the human here: a #stay-marked
// typed prompt, or a "Chat about this" answer to an AskUserQuestion. Whichever
// of the two is newer decides, so the next typed prompt (unmarked) ends a
// clarify stay exactly as it ends a #stay one.
//
// The clarify result only counts when its tool_use_id names an
// AskUserQuestion call: any tool's error output could carry the same words,
// but only the harness writes them for this tool.
func turnStays(events []Event) bool {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Type != "user" {
			continue
		}
		for _, tr := range e.ToolResults {
			if tr.IsError && strings.Contains(tr.Content, clarifyResultPrefix) && askToolUse(events[:i], tr.ToolUseID) {
				return true
			}
		}
		if genuinePrompt(e) {
			return stayMarked(e.UserText)
		}
	}
	return false
}

// askToolUse reports whether events contains an AskUserQuestion tool_use
// with the given id, searching newest-first since the call sits just before
// its result.
func askToolUse(events []Event, id string) bool {
	if id == "" {
		return false
	}
	for i := len(events) - 1; i >= 0; i-- {
		for _, tu := range events[i].ToolUses {
			if tu.ID == id {
				return tu.Name == "AskUserQuestion"
			}
		}
	}
	return false
}
