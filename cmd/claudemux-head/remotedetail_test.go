package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

var detailSince = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

func detailSess(state string) swSession {
	return swSession{Name: "proj", State: state, Since: detailSince, Topic: "colours", Description: "Example project"}
}

func optionIDs(d remoteDetail) []string {
	ids := []string{}
	for _, o := range d.Options {
		ids = append(ids, o.ID)
	}
	return ids
}

func askMarkerFor(labels ...string) askMarker {
	var opts []askOption
	for _, l := range labels {
		opts = append(opts, askOption{Label: l})
	}
	return askMarker{SessionID: "s", Questions: []askQuestion{{Question: "Which colour do you choose?", Header: "Colour", Options: opts}}}
}

func TestRemoteDetailAskFromMarker(t *testing.T) {
	d := buildRemoteDetail(remoteInputs{
		Sess:     detailSess("Asking"),
		Capture:  readFixture(t, "dialog_ask_single.txt"),
		Marker:   askMarkerFor("Red", "Green", "Blue"),
		MarkerOK: true,
	})
	if d.Kind != "ask" || d.Note != "" {
		t.Fatalf("kind %q note %q", d.Kind, d.Note)
	}
	if d.Body != "Which colour do you choose?" {
		t.Errorf("body %q", d.Body)
	}
	if got, want := optionIDs(d), []string{"key:1", "key:2", "key:3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("options %v, want %v", got, want)
	}
	if d.Options[2].Label != "Blue" || d.Options[0].Broad {
		t.Errorf("options %+v", d.Options)
	}
	if d.Title != "Example project · colours" || d.Session != "proj" {
		t.Errorf("title %q session %q", d.Title, d.Session)
	}
}

func TestRemoteDetailAskNoMarkerUsesPane(t *testing.T) {
	for name, in := range map[string]remoteInputs{
		"no marker":       {},
		"marker no q":     {Marker: askMarker{SessionID: "s"}, MarkerOK: true},
		"marker not read": {Marker: askMarkerFor("x"), MarkerOK: false},
	} {
		in.Sess, in.Capture = detailSess("Asking"), readFixture(t, "dialog_ask_single.txt")
		d := buildRemoteDetail(in)
		if d.Kind != "ask" || d.Body != "Which colour do you choose?" {
			t.Errorf("%s: kind %q body %q", name, d.Kind, d.Body)
		}
		if got := optionIDs(d); !reflect.DeepEqual(got, []string{"key:1", "key:2", "key:3"}) {
			t.Errorf("%s: options %v", name, got)
		}
	}
}

func TestRemoteDetailAskLongLabelPrefix(t *testing.T) {
	full := "Red, the colour of a very long and winding explanation"
	c := strings.Replace(readFixture(t, "dialog_ask_single.txt"), "1. Red", "1. Red,", 1)
	d := buildRemoteDetail(remoteInputs{
		Sess: detailSess("Asking"), Capture: c, Marker: askMarkerFor(full, "Green", "Blue"), MarkerOK: true,
	})
	if d.Kind != "ask" || d.Options[0].Label != full {
		t.Fatalf("kind %q options %+v", d.Kind, d.Options)
	}
}

func TestRemoteDetailAskMismatchIsDesk(t *testing.T) {
	cases := map[string]askMarker{
		"count":  askMarkerFor("Red", "Green"),
		"label":  askMarkerFor("Red", "Green", "Purple"),
		"order":  askMarkerFor("Green", "Red", "Blue"),
		"multi":  {SessionID: "s", Questions: []askQuestion{{Question: "q", MultiSelect: true, Options: askMarkerFor("Red", "Green", "Blue").Questions[0].Options}}},
		"two qs": {SessionID: "s", Questions: append(askMarkerFor("Red", "Green", "Blue").Questions, askMarkerFor("A", "B").Questions...)},
	}
	for name, m := range cases {
		d := buildRemoteDetail(remoteInputs{Sess: detailSess("Asking"), Capture: readFixture(t, "dialog_ask_single.txt"), Marker: m, MarkerOK: true})
		if d.Kind != "desk" || d.Note != "answer at the desk" || len(d.Options) != 0 {
			t.Errorf("%s: %+v", name, d)
		}
	}
}

func TestRemoteDetailAskComplexIsDesk(t *testing.T) {
	for _, f := range []string{"dialog_ask_multi.txt", "dialog_ask_two_questions.txt"} {
		d := buildRemoteDetail(remoteInputs{Sess: detailSess("Asking"), Capture: readFixture(t, f)})
		if d.Kind != "desk" || d.Note != "answer at the desk" || len(d.Options) != 0 {
			t.Errorf("%s: %+v", f, d)
		}
		if d.Options == nil {
			t.Errorf("%s: Options must be non-nil for JSON", f)
		}
	}
}

func TestRemoteDetailUnknownIsDesk(t *testing.T) {
	c := "something\n\nEsc to cancel\n"
	d := buildRemoteDetail(remoteInputs{Sess: detailSess("Awaiting"), Capture: c})
	if d.Kind != "desk" || len(d.Options) != 0 {
		t.Errorf("%+v", d)
	}
}

func TestRemoteDetailWaitingWithoutDialogIsDesk(t *testing.T) {
	d := buildRemoteDetail(remoteInputs{Sess: detailSess("Asking"), Capture: ansi.Strip(readFixture(t, "prompt_empty.txt"))})
	if d.Kind != "desk" || len(d.Options) != 0 {
		t.Errorf("%+v", d)
	}
}

func TestRemoteDetailPermit(t *testing.T) {
	d := buildRemoteDetail(remoteInputs{Sess: detailSess("Awaiting"), Capture: readFixture(t, "dialog_permit_bash.txt")})
	if d.Kind != "permit" {
		t.Fatalf("kind %q", d.Kind)
	}
	if got, want := optionIDs(d), []string{"key:1", "key:2", "key:3", "key:4"}; !reflect.DeepEqual(got, want) {
		t.Errorf("options %v", got)
	}
	if !strings.Contains(d.Body, "touch spike-marker.txt") || !strings.Contains(d.Body, "Do you want to proceed?") {
		t.Errorf("body %q", d.Body)
	}
}

func TestRemoteDetailPermitBodyKeepsHead(t *testing.T) {
	long := "echo START " + strings.Repeat("word ", 200) + "END"
	c := readFixture(t, "dialog_permit_bash.txt")
	at := strings.LastIndex(c, "touch spike-marker.txt")
	c = c[:at] + long + c[at+len("touch spike-marker.txt"):]
	d := buildRemoteDetail(remoteInputs{Sess: detailSess("Awaiting"), Capture: c})
	if d.Kind != "permit" {
		t.Fatalf("kind %q", d.Kind)
	}
	if n := len([]rune(d.Body)); n > remoteBodyRunes+3 {
		t.Errorf("body is %d runes", n)
	}
	if !strings.Contains(d.Body, "Bash command") || !strings.Contains(d.Body, "echo START") || !strings.HasSuffix(d.Body, "...") {
		t.Errorf("body %q", d.Body)
	}
	if len(d.Options) != 4 {
		t.Errorf("options %+v", d.Options)
	}
}

func TestRemoteDetailPlan(t *testing.T) {
	d := buildRemoteDetail(remoteInputs{Sess: detailSess("Awaiting"), Capture: readFixture(t, "dialog_plan.txt")})
	if d.Kind != "plan" {
		t.Fatalf("kind %q", d.Kind)
	}
	if got, want := optionIDs(d), []string{"key:1", "key:2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("options %v (free-text row must not be offered)", got)
	}
	if !d.Options[0].Broad || d.Options[1].Broad {
		t.Errorf("broad flags %+v", d.Options)
	}
}

func TestRemoteDetailIdle(t *testing.T) {
	e := readFixture(t, "prompt_empty.txt")
	d := buildRemoteDetail(remoteInputs{
		Sess: detailSess("Idle"), Capture: ansi.Strip(e), CaptureE: e,
		LastText: "All done.", Suggested: []string{"commit it"}, Replies: []string{"Continue", "Stop"},
		VimMode: true,
	})
	if d.Kind != "idle" || d.Body != "All done." || d.Note != "" {
		t.Fatalf("%+v", d)
	}
	// The fixed replies come first, so suggestions arriving later never
	// move a fixed reply out from under the glasses' cursor.
	if got, want := optionIDs(d), []string{"reply:0", "reply:1", "suggest:0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("options %v", got)
	}
	if d.Options[0].Suggested || d.Options[1].Suggested || !d.Options[2].Suggested {
		t.Errorf("suggested flags %+v", d.Options)
	}
}

// Suggestions land in the cache after the glasses first saw the session; the
// episode must not change when they do, or a tap on a fixed reply made just
// after would be refused. Fixed replies still count.
func TestRemoteDetailIdleEpisodeIgnoresSuggestions(t *testing.T) {
	e := readFixture(t, "prompt_empty.txt")
	in := remoteInputs{
		Sess: detailSess("Idle"), Capture: ansi.Strip(e), CaptureE: e,
		LastText: "All done.", Replies: []string{"Continue", "Stop"}, VimMode: true,
	}
	without := buildRemoteDetail(in)
	in.Suggested = []string{"commit it", "open the PR"}
	with := buildRemoteDetail(in)
	if len(with.Options) != 4 || without.Episode != with.Episode {
		t.Errorf("episode %q without suggestions, %q with (options %v)", without.Episode, with.Episode, optionIDs(with))
	}
	in.Replies = []string{"Continue"}
	if buildRemoteDetail(in).Episode == with.Episode {
		t.Error("a change to the fixed replies must change the episode")
	}
}

func TestRemoteDetailIdleEmptyRepliesMeansNone(t *testing.T) {
	e := readFixture(t, "prompt_empty.txt")
	d := buildRemoteDetail(remoteInputs{Sess: detailSess("Idle"), Capture: ansi.Strip(e), CaptureE: e, VimMode: true, Replies: []string{}})
	if d.Kind != "idle" || len(d.Options) != 0 {
		t.Errorf("%+v", d)
	}
}

func TestRemoteDetailIdleBodyTail(t *testing.T) {
	e := readFixture(t, "prompt_empty.txt")
	text := "HEAD " + strings.Repeat("filler ", 100) + "the end"
	d := buildRemoteDetail(remoteInputs{Sess: detailSess("Idle"), Capture: ansi.Strip(e), CaptureE: e, VimMode: true, LastText: text})
	if n := len([]rune(d.Body)); n > remoteBodyRunes {
		t.Errorf("body is %d runes", n)
	}
	if strings.Contains(d.Body, "HEAD") || !strings.HasSuffix(d.Body, "the end") || !strings.HasPrefix(d.Body, "filler") {
		t.Errorf("body %q", d.Body)
	}
}

func TestRemoteDetailIdleBodyFallsBackToCapture(t *testing.T) {
	e := readFixture(t, "prompt_empty.txt")
	d := buildRemoteDetail(remoteInputs{Sess: detailSess("Idle"), Capture: "one\n\ntwo\n\n", CaptureE: e, VimMode: true})
	if d.Body != "one\n\ntwo" {
		t.Errorf("body %q", d.Body)
	}
}

func TestRemoteDetailIdleDraft(t *testing.T) {
	e := readFixture(t, "prompt_draft.txt")
	d := buildRemoteDetail(remoteInputs{
		Sess: detailSess("Idle"), Capture: ansi.Strip(e), CaptureE: e, VimMode: true,
		LastText: "x", Suggested: []string{"a"}, Replies: []string{"b"},
	})
	if d.Kind != "idle" || d.Note != "prompt has a draft" || len(d.Options) != 0 {
		t.Errorf("%+v", d)
	}
}

func TestRemoteDetailBusyOpened(t *testing.T) {
	e := readFixture(t, "prompt_empty.txt")
	d := buildRemoteDetail(remoteInputs{
		Sess: detailSess("Tool:Bash"), Capture: ansi.Strip(e), CaptureE: e, VimMode: true,
		LastText: "working on it", Replies: []string{"Continue"},
	})
	if d.Kind != "idle" || d.Note != "working" || len(d.Options) != 0 || d.Options == nil {
		t.Errorf("%+v", d)
	}
}

func TestRemoteDetailTitleFallback(t *testing.T) {
	d := buildRemoteDetail(remoteInputs{Sess: swSession{Name: "proj", State: "Idle"}, CaptureE: readFixture(t, "prompt_empty.txt"), VimMode: true})
	if d.Title != "proj" {
		t.Errorf("title %q", d.Title)
	}
}

func TestRemoteEpisodeStable(t *testing.T) {
	a := remoteEpisode(detailSince, "ask", "body", []string{"Red", "Blue"})
	b := remoteEpisode(detailSince, "ask", "body", []string{"Red", "Blue"})
	if a != b || len(a) != 16 {
		t.Errorf("%q vs %q", a, b)
	}
	if remoteEpisode(detailSince.Add(time.Second), "ask", "body", []string{"Red", "Blue"}) == a {
		t.Error("since must change the episode")
	}
	if remoteEpisode(detailSince, "permit", "body", []string{"Red", "Blue"}) == a {
		t.Error("kind must change the episode")
	}
	if remoteEpisode(detailSince, "ask", "body2", []string{"Red", "Blue"}) == a {
		t.Error("body must change the episode")
	}
}

func TestRemoteEpisodeChangesWithLabels(t *testing.T) {
	a := remoteEpisode(detailSince, "ask", "b", []string{"Red", "Blue"})
	for name, l := range map[string][]string{
		"changed":   {"Red", "Green"},
		"reordered": {"Blue", "Red"},
		"fewer":     {"Red"},
		"joined":    {"RedBlue"},
	} {
		if remoteEpisode(detailSince, "ask", "b", l) == a {
			t.Errorf("%s labels gave the same episode", name)
		}
	}
}

func TestBroadChoice(t *testing.T) {
	firstOf := func(d paneDialog, i int) bool {
		for j := 0; j < i; j++ {
			if !d.Rows[j].FreeText {
				return false
			}
		}
		return true
	}
	bash := parsePaneDialog(readFixture(t, "dialog_permit_bash.txt"))
	for i, want := range []bool{false, true, true, false} {
		if got := broadChoice(dialogPermit, bash.Rows[i], firstOf(bash, i)); got != want {
			t.Errorf("bash row %d: got %v want %v", i+1, got, want)
		}
	}
	edit := parsePaneDialog(readFixture(t, "dialog_permit_edit.txt"))
	for i, want := range []bool{false, true, false} {
		if got := broadChoice(dialogPermit, edit.Rows[i], firstOf(edit, i)); got != want {
			t.Errorf("edit row %d: got %v want %v", i+1, got, want)
		}
	}
	plan := parsePaneDialog(readFixture(t, "dialog_plan.txt"))
	for i, want := range []bool{true, false} {
		if got := broadChoice(dialogPlan, plan.Rows[i], firstOf(plan, i)); got != want {
			t.Errorf("plan row %d: got %v want %v", i+1, got, want)
		}
	}
	if broadChoice(dialogAsk, dialogRow{N: 2, Label: "x"}, false) {
		t.Error("ask rows are never broad")
	}
}

// A Tab-amended permission row is a focused text field; what a digit does
// there was never measured, so such a dialog is answered at the desk.
func TestRemoteDetailAmendedPermitIsDesk(t *testing.T) {
	edit := readFixture(t, "dialog_permit_edit.txt")
	amendedYes := strings.Replace(edit, "1. Yes\n", "1. Yes, and tell Claude what to do next\n", 1)
	amendedNo := strings.Replace(edit, " 3. No\n", " 3. No, and tell Claude what to do differently\n", 1)
	cases := map[string]string{
		"amended first row":                  strings.Replace(amendedYes, " Esc to cancel · Tab to amend", " Esc to cancel", 1),
		"amended last row":                   strings.Replace(amendedNo, " Esc to cancel · Tab to amend", " Esc to cancel", 1),
		"free-text row, footer still amends": amendedYes,
		"plain rows, footer lost Tab":        strings.Replace(edit, " Esc to cancel · Tab to amend", " Esc to cancel", 1),
	}
	for name, c := range cases {
		d := buildRemoteDetail(remoteInputs{Sess: detailSess("Awaiting"), Capture: c})
		if d.Kind != "desk" || d.Note != noteAtDesk || len(d.Options) != 0 {
			t.Errorf("%s: %+v", name, d)
		}
	}
}

// keyOptions takes each option's digit from its row number, never from its
// position among the offered rows, and a free-text row next to No does not
// make No broad.
func TestKeyOptionsUseRowNumbers(t *testing.T) {
	dlg := paneDialog{Kind: dialogPermit, Prompt: "Edit file\nDo you want to make this edit?", Rows: []dialogRow{
		{1, "Yes, and tell Claude what to do next", true},
		{2, "Yes, and switch to accept edits", false},
		{3, "No", false},
		{4, "No, and tell Claude what to do differently", true},
	}}
	opts := keyOptions(dlg)
	ids := make([]string, len(opts))
	for i, o := range opts {
		ids[i] = o.ID
	}
	if want := []string{"key:2", "key:3"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("options %v, want %v", ids, want)
	}
	if !opts[0].Broad || opts[1].Broad {
		t.Errorf("broad flags %+v", opts)
	}
}

// The permit body leads with the title and the command, so a long
// model-written description cannot push the command off the glasses.
func TestRemoteDetailPermitBodyOrder(t *testing.T) {
	desc := "Create empty marker file"
	long := strings.Repeat("A very long explanation of the command. ", 20)
	c := strings.Replace(readFixture(t, "dialog_permit_bash.txt"), " "+desc+"\n", " "+long+"\n", 1)
	d := buildRemoteDetail(remoteInputs{Sess: detailSess("Awaiting"), Capture: c})
	if d.Kind != "permit" {
		t.Fatalf("kind %q", d.Kind)
	}
	if !strings.HasPrefix(d.Body, "Bash command\ntouch spike-marker.txt\n") {
		t.Errorf("body %q", d.Body)
	}
	short := buildRemoteDetail(remoteInputs{Sess: detailSess("Awaiting"), Capture: readFixture(t, "dialog_permit_bash.txt")})
	want := "Bash command\ntouch spike-marker.txt\nCreate empty marker file\nDo you want to proceed?"
	if short.Body != want {
		t.Errorf("body %q, want %q", short.Body, want)
	}
	edit := buildRemoteDetail(remoteInputs{Sess: detailSess("Awaiting"), Capture: readFixture(t, "dialog_permit_edit.txt")})
	if !strings.HasPrefix(edit.Body, "Edit file\n1 -hello\n1 +goodbye\nnotes.txt\n") {
		t.Errorf("edit body %q", edit.Body)
	}
}

// Two prompts that differ only past the trimmed body are two episodes.
func TestRemoteDetailPermitEpisodeCoversWholePrompt(t *testing.T) {
	base := readFixture(t, "dialog_permit_bash.txt")
	cmd := "echo " + strings.Repeat("word ", 120)
	at := strings.LastIndex(base, "touch spike-marker.txt") // the dialog's copy, not the scrollback's
	a := base[:at] + cmd + "AAA" + base[at+len("touch spike-marker.txt"):]
	b := base[:at] + cmd + "BBB" + base[at+len("touch spike-marker.txt"):]
	da := buildRemoteDetail(remoteInputs{Sess: detailSess("Awaiting"), Capture: a})
	db := buildRemoteDetail(remoteInputs{Sess: detailSess("Awaiting"), Capture: b})
	if da.Kind != "permit" || db.Kind != "permit" {
		t.Fatalf("kinds %q %q", da.Kind, db.Kind)
	}
	if da.Body != db.Body {
		t.Fatalf("bodies differ; the test needs the difference past the cut:\n%q\n%q", da.Body, db.Body)
	}
	if da.Episode == db.Episode {
		t.Error("prompts differing past the body cut share an episode")
	}
}

// A prompt longer than the glasses can show makes the plain Yes a second
// tap too: the wearer cannot have read all of it.
func TestRemoteDetailLongPermitYesIsBroad(t *testing.T) {
	base := readFixture(t, "dialog_permit_edit.txt")
	short := buildRemoteDetail(remoteInputs{Sess: detailSess("Awaiting"), Capture: base})
	if short.Options[0].Label != "Yes" || short.Options[0].Broad {
		t.Fatalf("short prompt: %+v", short.Options)
	}
	long := strings.Replace(base, " 1 +goodbye\n", " 1 +goodbye\n"+strings.Repeat(" 2 +more text in the diff\n", 6), 1)
	d := buildRemoteDetail(remoteInputs{Sess: detailSess("Awaiting"), Capture: long})
	if d.Kind != "permit" {
		t.Fatalf("kind %q", d.Kind)
	}
	for i, want := range []bool{true, true, false} {
		if d.Options[i].Broad != want {
			t.Errorf("option %s broad %v, want %v", d.Options[i].Label, d.Options[i].Broad, want)
		}
	}
}

// Plan rows are offered only when they say Yes.
func TestRemoteDetailPlanOffersOnlyYesRows(t *testing.T) {
	c := strings.Replace(readFixture(t, "dialog_plan.txt"), "2. Yes, manually approve edits", "2. Keep planning", 1)
	d := buildRemoteDetail(remoteInputs{Sess: detailSess("Awaiting"), Capture: c})
	if d.Kind != "plan" {
		t.Fatalf("kind %q", d.Kind)
	}
	if got, want := optionIDs(d), []string{"key:1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("options %v, want %v", got, want)
	}
}
