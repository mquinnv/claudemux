package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestDeferArgs(t *testing.T) {
	got := deferArgs("api", true)
	want := []string{"set-option", "-t", "api", deferOption, "1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deferArgs(on) = %v, want %v", got, want)
	}
	got = deferArgs("api", false)
	want = []string{"set-option", "-t", "api", "-u", deferOption}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deferArgs(off) = %v, want %v", got, want)
	}
}

func TestDeferChip(t *testing.T) {
	if got := deferChip("1"); !strings.Contains(got, "defer") {
		t.Errorf("deferChip(1) = %q, want a visible defer chip", got)
	}
	for name, raw := range map[string]string{
		"empty":   "",
		"unknown": "0",
		"garbage": "yes",
	} {
		if got := deferChip(raw); got != "" {
			t.Errorf("deferChip(%s) = %q, want empty", name, got)
		}
	}
}
