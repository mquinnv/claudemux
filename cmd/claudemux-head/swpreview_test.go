package main

import (
	"strings"
	"testing"
)

func TestPreviewTail(t *testing.T) {
	tests := []struct {
		name    string
		capture string
		n       int
		want    []string
	}{
		{"tail of a long capture", "a\nb\nc\nd\ne\n", 3, []string{"c", "d", "e"}},
		{"shorter than requested", "a\nb\n", 5, []string{"a", "b"}},
		{"exactly enough", "a\nb\n", 2, []string{"a", "b"}},
		// A claude pane sitting at its input box ends in blank rows; an
		// untrimmed tail would be mostly empty.
		{"trailing blanks trimmed", "a\nb\n\n   \n\n", 2, []string{"a", "b"}},
		// tmux emits colored blanks: a line that is nothing but an SGR reset
		// is still blank.
		{"ansi-only lines are blank", "a\nb\n\x1b[39m\n\x1b[0m   \n", 2, []string{"a", "b"}},
		{"blank interior lines kept", "a\n\nb\n", 3, []string{"a", "", "b"}},
		{"all blank", "\n\n   \n", 3, nil},
		{"empty", "", 3, nil},
		{"zero lines requested", "a\nb\n", 0, nil},
		{"negative lines requested", "a\nb\n", -1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := previewTail(tt.capture, tt.n)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("previewTail(%q, %d) = %q, want %q", tt.capture, tt.n, got, tt.want)
			}
		})
	}
}
