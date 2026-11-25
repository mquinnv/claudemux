package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestParsePaneDialogAskSingle(t *testing.T) {
	d := parsePaneDialog(readFixture(t, "dialog_ask_single.txt"))
	want := paneDialog{
		Kind:   dialogAsk,
		Prompt: "Which colour do you choose?",
		Rows: []dialogRow{
			{1, "Red", false},
			{2, "Green", false},
			{3, "Blue", false},
			{4, "Type something.", true},
			{5, "Chat about this", true},
		},
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("got %+v\nwant %+v", d, want)
	}
}

func TestParsePaneDialogAskComplex(t *testing.T) {
	for _, name := range []string{"dialog_ask_multi.txt", "dialog_ask_two_questions.txt"} {
		d := parsePaneDialog(readFixture(t, name))
		if d.Kind != dialogAskComplex {
			t.Errorf("%s: kind %v, want dialogAskComplex", name, d.Kind)
		}
		if len(d.Rows) != 0 {
			t.Errorf("%s: complex dialogs must offer no rows, got %+v", name, d.Rows)
		}
	}
}

func TestParsePaneDialogPermitBash(t *testing.T) {
	d := parsePaneDialog(readFixture(t, "dialog_permit_bash.txt"))
	if d.Kind != dialogPermit {
		t.Fatalf("kind %v, want dialogPermit", d.Kind)
	}
	if len(d.Rows) != 4 {
		t.Fatalf("rows %+v, want 4", d.Rows)
	}
	for i, r := range d.Rows {
		if r.N != i+1 || r.FreeText {
			t.Errorf("row %d: %+v", i, r)
		}
	}
	if d.Rows[0].Label != "Yes" || d.Rows[3].Label != "No" {
		t.Errorf("rows 1/4: %+v / %+v", d.Rows[0], d.Rows[3])
	}
	wantTwo := "Yes, and always allow access to /private/tmp/sample-000/-home-user-Projects-examplexxxxxx/00000000-0000-4000-8000" +
		" -000000000000/scratchpad/g2spike/proj from this project"
	if d.Rows[1].Label != wantTwo {
		t.Errorf("row 2 label %q, want %q", d.Rows[1].Label, wantTwo)
	}
	if !strings.HasPrefix(d.Rows[2].Label, "Yes, and switch to auto mode") {
		t.Errorf("row 3 label %q", d.Rows[2].Label)
	}
	for _, r := range d.Rows {
		if strings.Contains(r.Label, "Tip:") {
			t.Errorf("Tip in row %+v", r)
		}
	}
	if strings.Contains(d.Prompt, "Tip:") {
		t.Errorf("Tip in prompt %q", d.Prompt)
	}
	for _, s := range []string{"Bash command", "Create empty marker file", "touch spike-marker.txt", "Do you want to proceed?"} {
		if !strings.Contains(d.Prompt, s) {
			t.Errorf("prompt %q lacks %q", d.Prompt, s)
		}
	}
	if strings.Contains(d.Prompt, "╌") || strings.Contains(d.Prompt, "Esc to cancel") {
		t.Errorf("prompt carries chrome: %q", d.Prompt)
	}
}

func TestParsePaneDialogPermitEdit(t *testing.T) {
	d := parsePaneDialog(readFixture(t, "dialog_permit_edit.txt"))
	if d.Kind != dialogPermit || len(d.Rows) != 3 {
		t.Fatalf("got %+v", d)
	}
	if d.Rows[2].N != 3 || d.Rows[2].Label != "No" || d.Rows[2].FreeText {
		t.Errorf("row 3 %+v", d.Rows[2])
	}
	if !strings.Contains(d.Prompt, "notes.txt") || !strings.Contains(d.Prompt, "1 +goodbye") {
		t.Errorf("prompt %q", d.Prompt)
	}
}

func TestParsePaneDialogPlan(t *testing.T) {
	d := parsePaneDialog(readFixture(t, "dialog_plan.txt"))
	if d.Kind != dialogPlan {
		t.Fatalf("kind %v", d.Kind)
	}
	want := []dialogRow{
		{1, "Yes, and use auto mode", false},
		{2, "Yes, manually approve edits", false},
		{3, "Tell Claude what to change", true},
	}
	if !reflect.DeepEqual(d.Rows, want) {
		t.Errorf("rows %+v, want %+v", d.Rows, want)
	}
}

func TestParsePaneDialogNone(t *testing.T) {
	for _, name := range []string{"prompt_empty.txt", "prompt_bare.txt", "prompt_draft.txt", "prompt_normal.txt", "prompt_suggestion.txt", "prompt_trydraft.txt"} {
		raw := readFixture(t, name)
		for label, c := range map[string]string{"raw": raw, "stripped": ansi.Strip(raw)} {
			if d := parsePaneDialog(c); d.Kind != dialogNone || len(d.Rows) != 0 {
				t.Errorf("%s (%s): got %+v, want dialogNone", name, label, d)
			}
		}
	}
	if d := parsePaneDialog(""); d.Kind != dialogNone {
		t.Errorf("empty capture: %+v", d)
	}
}

// The older question fixture has the same layout as dialog_ask_single.
func TestParsePaneDialogOlderAskFixture(t *testing.T) {
	d := parsePaneDialog(readFixture(t, "prompt_asking.txt"))
	if d.Kind != dialogAsk || d.Prompt != "Do you like tea?" || len(d.Rows) != 4 {
		t.Errorf("got %+v", d)
	}
}

func TestParsePaneDialogUnknown(t *testing.T) {
	rule := strings.Repeat("─", 40)
	permitTail := "\n\n Esc to cancel · Tab to amend\n"
	cases := map[string]string{
		"esc to cancel, no rows":         "some output\n" + rule + "\n Do the thing\n\n Esc to cancel\n",
		"trust-style dialog, no numbers": "output\n" + rule + "\n ❯ No, exit\n   Yes, I trust this folder\n\n Enter to confirm · Esc to cancel\n",
		"no opening rule":                " Do you want to proceed?\n ❯ 1. Yes\n   2. No" + permitTail,
		"ask with text row focused": strings.Replace(readFixture(t, "dialog_ask_single.txt"),
			"Enter to select · ↑/↓ to navigate · Esc to cancel",
			"-- INSERT -- · Enter to select · ↑/↓ to navigate · ctrl+g to edit in Vim", 1),
		"permit without a No row":   "x\n" + rule + "\n Bash command\n Do you want to proceed?\n ❯ 1. Yes\n   2. Maybe" + permitTail,
		"permit without question":   "x\n" + rule + "\n Bash command\n ❯ 1. Yes\n   2. No" + permitTail,
		"permit rows skip a number": "x\n" + rule + "\n Bash command\n Do you want to proceed?\n ❯ 1. Yes\n   3. No" + permitTail,
		"permit rows start at 2":    "x\n" + rule + "\n Bash command\n Do you want to proceed?\n ❯ 2. Yes\n   3. No" + permitTail,
		"permit with one row":       "x\n" + rule + "\n Bash command\n Do you want to proceed?\n ❯ 1. No" + permitTail,
		"two digit row": "x\n" + rule + "\n Bash command\n Do you want to proceed?\n" +
			" ❯ 1. Yes\n   2. a\n   3. a\n   4. a\n   5. a\n   6. a\n   7. a\n   8. a\n   9. a\n   10. No" + permitTail,
		"ask without the two trailing rows": "x\n" + rule + "\n ☐ Colour\n\nWhich?\n\n❯ 1. Red\n  2. Green\n\nEnter to select · ↑/↓ to navigate · Esc to cancel\n",
		"ask header without box":            "x\n" + rule + "\n Colour\n\nWhich?\n\n❯ 1. Red\n  2. Type something.\n" + rule + "\n  3. Chat about this\n\nEnter to select · ↑/↓ to navigate · Esc to cancel\n",
		"plan without its question":         "x\n" + rule + "\n   ❯ 1. Yes\n     2. No\n\n   ctrl+g to edit in Vim · ~/.claude/plans/x.md\n",
	}
	for name, c := range cases {
		if d := parsePaneDialog(c); d.Kind != dialogUnknown || len(d.Rows) != 0 {
			t.Errorf("%s: got %+v, want dialogUnknown with no rows", name, d)
		}
	}
}

// A Tab-amended row (footer reduced to "Esc to cancel", the row reading
// "..., and tell Claude ...") is a text field: the dialog is not answerable.
func TestParsePaneDialogPermitAmendedIsUnknown(t *testing.T) {
	edit := readFixture(t, "dialog_permit_edit.txt")
	amended := strings.Replace(edit, "1. Yes\n", "1. Yes, and tell Claude what to do next\n", 1)
	amended = strings.Replace(amended, "3. No\n", "3. No, and tell Claude what to do differently\n", 1)
	for name, c := range map[string]string{
		"amended rows and footer":   strings.Replace(amended, " Esc to cancel · Tab to amend", " Esc to cancel", 1),
		"amended rows, old footer":  amended,
		"plain rows, amend footer":  strings.Replace(edit, " Esc to cancel · Tab to amend", " Esc to cancel", 1),
		"plain rows, footer + more": strings.Replace(edit, " Esc to cancel · Tab to amend", " Esc to cancel · ctrl+e to explain", 1),
	} {
		if d := parsePaneDialog(c); d.Kind != dialogUnknown || len(d.Rows) != 0 {
			t.Errorf("%s: got %+v, want dialogUnknown", name, d)
		}
	}
	for _, l := range []string{"Yes, and tell Claude what to do next", "No, and tell Claude what to do differently"} {
		if !freeTextLabel(l) {
			t.Errorf("%q is not read as free text", l)
		}
	}
}

// A permit dialog opens with a known title line; anything else (another
// tool's layout, or a rule line inside a command hiding the real opener) is
// not answerable.
func TestParsePaneDialogPermitNeedsKnownTitle(t *testing.T) {
	rule := strings.Repeat("─", 40)
	tail := "\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel · Tab to amend\n"
	for _, title := range []string{"Bash command", "Edit file", "Create file"} {
		if d := parsePaneDialog("x\n" + rule + "\n " + title + "\n echo hi" + tail); d.Kind != dialogPermit {
			t.Errorf("%s: got %+v, want dialogPermit", title, d)
		}
	}
	for name, c := range map[string]string{
		"unknown title":        "x\n" + rule + "\n Tool use\n echo hi" + tail,
		"blank before title":   "x\n" + rule + "\n\n Bash command\n echo hi" + tail,
		"rule inside command":  "x\n" + rule + "\n Bash command\n echo start\n" + rule + "\n echo end" + tail,
		"title not first line": "x\n" + rule + "\n echo hi\n Bash command" + tail,
	} {
		if d := parsePaneDialog(c); d.Kind != dialogUnknown || len(d.Rows) != 0 {
			t.Errorf("%s: got %+v, want dialogUnknown", name, d)
		}
	}
}

// The permit prompt is the whole parsed text, not trimmed, ordered title,
// command, description, question.
func TestParsePaneDialogPermitPromptOrder(t *testing.T) {
	d := parsePaneDialog(readFixture(t, "dialog_permit_bash.txt"))
	want := "Bash command\ntouch spike-marker.txt\nCreate empty marker file\nDo you want to proceed?"
	if d.Prompt != want {
		t.Errorf("prompt %q, want %q", d.Prompt, want)
	}
	if d.Essential != "Bash command\ntouch spike-marker.txt" {
		t.Errorf("essential %q", d.Essential)
	}
	// Edit and Write: the file name goes right after the title, before the
	// diff, so the wearer always sees which file.
	e := parsePaneDialog(readFixture(t, "dialog_permit_edit.txt"))
	if want := "Edit file\nnotes.txt\n1 -hello\n1 +goodbye\nDo you want to make this edit to notes.txt?"; e.Prompt != want {
		t.Errorf("edit prompt %q, want %q", e.Prompt, want)
	}
	if e.Essential != "Edit file\nnotes.txt\n1 -hello\n1 +goodbye" {
		t.Errorf("edit essential %q", e.Essential)
	}
}

func TestParsePaneDialogIgnoresScrollback(t *testing.T) {
	rule := strings.Repeat("─", 40)
	scroll := "⏺ Here are the steps:\n  1. Do the first thing\n  2. Do the second thing\n  3. Do the third thing\n\n"
	dialog := rule + "\n Edit file\n notes.txt\n Do you want to make this edit to notes.txt?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel · Tab to amend\n"
	d := parsePaneDialog(scroll + dialog)
	want := []dialogRow{{1, "Yes", false}, {2, "No", false}}
	if d.Kind != dialogPermit || !reflect.DeepEqual(d.Rows, want) {
		t.Errorf("got %+v", d)
	}
	if strings.Contains(d.Prompt, "first thing") {
		t.Errorf("scrollback in prompt: %q", d.Prompt)
	}
	// Numbered text above an idle prompt is not a dialog at all.
	idle := scroll + rule + "\n❯ \n" + rule + "\n\n  -- INSERT -- ⏵⏵ auto mode on (shift+tab to cycle)\n"
	if d := parsePaneDialog(idle); d.Kind != dialogNone {
		t.Errorf("idle prompt: got %+v", d)
	}
}

func TestParsePaneDialogCommandWithNumberedLines(t *testing.T) {
	rule := strings.Repeat("─", 40)
	dash := strings.Repeat("╌", 16)
	c := rule + "\n Bash command\n" + dash + "\n echo\n    1. fake\n    2. fake\n" + dash +
		"\n Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel · Tab to amend\n"
	d := parsePaneDialog(c)
	want := []dialogRow{{1, "Yes", false}, {2, "No", false}}
	if d.Kind != dialogPermit || !reflect.DeepEqual(d.Rows, want) {
		t.Errorf("got %+v", d)
	}
}
