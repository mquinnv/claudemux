package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func goodClaudeConfig() ClaudeConfig {
	return ClaudeConfig{
		DefaultAccount: "ameriglide",
		Accounts: map[string]ClaudeAccount{
			"ameriglide": {ConfigDir: "~/.claude-accounts/ameriglide"},
			"bespoke":    {},
		},
	}
}

func TestClaudeConfigImplicit(t *testing.T) {
	var c ClaudeConfig
	if got := c.Names(); !reflect.DeepEqual(got, []string{"default"}) {
		t.Errorf("Names() = %v", got)
	}
	if c.Multi() {
		t.Error("Multi() = true")
	}
	if c.Default() != "default" {
		t.Errorf("Default() = %q", c.Default())
	}
	if d, err := c.ConfigDir("default"); err != nil || d != "" {
		t.Errorf("ConfigDir(default) = %q, %v", d, err)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v", err)
	}
}

func TestClaudeConfigConfigDirExpandsHome(t *testing.T) {
	t.Setenv("HOME", "/h")
	d, err := goodClaudeConfig().ConfigDir("ameriglide")
	if err != nil || d != "/h/.claude-accounts/ameriglide" {
		t.Errorf("ConfigDir = %q, %v", d, err)
	}
	if d, err := goodClaudeConfig().ConfigDir("bespoke"); err != nil || d != "" {
		t.Errorf("ConfigDir(bespoke) = %q, %v", d, err)
	}
}

func TestClaudeConfigUnknownAccount(t *testing.T) {
	_, err := goodClaudeConfig().ConfigDir("nope")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("err = %v, want one naming nope", err)
	}
}

func TestForConfigDir(t *testing.T) {
	t.Setenv("HOME", "/h")
	c := goodClaudeConfig()
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "bespoke", true},
		{"/h/.claude", "bespoke", true},
		{"/h/.claude/", "bespoke", true},
		{"/h/.claude-accounts/ameriglide/", "ameriglide", true},
		{"/elsewhere", "", false},
	}
	for _, tc := range cases {
		got, ok := c.ForConfigDir(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ForConfigDir(%q) = %q,%v want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestForConfigDirSymlink(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	c := ClaudeConfig{DefaultAccount: "a", Accounts: map[string]ClaudeAccount{"a": {ConfigDir: real}, "b": {}}}
	if got, ok := c.ForConfigDir(link); !ok || got != "a" {
		t.Errorf("ForConfigDir(symlink) = %q,%v want a,true", got, ok)
	}
}

func TestClaudeConfigValidate(t *testing.T) {
	if err := goodClaudeConfig().Validate(); err != nil {
		t.Fatalf("good config: %v", err)
	}
	bad := map[string]ClaudeConfig{
		"missing default": {DefaultAccount: "zzz", Accounts: map[string]ClaudeAccount{"a": {}}},
		"bad name":        {DefaultAccount: "Bad Name", Accounts: map[string]ClaudeAccount{"Bad Name": {}}},
		"two without dir": {DefaultAccount: "a", Accounts: map[string]ClaudeAccount{"a": {}, "b": {}}},
		"duplicate dir": {DefaultAccount: "a", Accounts: map[string]ClaudeAccount{
			"a": {ConfigDir: "/x"}, "b": {ConfigDir: "/x/"}}},
	}
	for name, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want error", name)
		}
	}
}
