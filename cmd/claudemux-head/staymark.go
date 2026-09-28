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

// stayMarkerOption carries whether the session's newest typed prompt is
// marked: "1" when it is, unset otherwise. It is published in the same tmux
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
