package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveAccount(t *testing.T) {
	multi := ClaudeConfig{
		DefaultAccount: "ameriglide",
		Accounts: map[string]ClaudeAccount{
			"ameriglide": {ConfigDir: "/a"},
			"bespoke":    {},
		},
	}
	rec := func(acct string, found bool) func(string) (string, bool) {
		return func(string) (string, bool) { return acct, found }
	}
	proj := func(p string) func(string) string { return func(string) string { return p } }

	tests := []struct {
		name         string
		cfg          ClaudeConfig
		flag, resume string
		rec          func(string) (string, bool)
		proj         string
		want         accountChoice
		wantErr      []string
	}{
		{name: "nothing set", cfg: multi, rec: rec("", false),
			want: accountChoice{"ameriglide", "/a", "default"}},
		{name: "project", cfg: multi, proj: "bespoke", rec: rec("", false),
			want: accountChoice{"bespoke", "", "project"}},
		{name: "flag beats project", cfg: multi, flag: "bespoke", proj: "ameriglide", rec: rec("", false),
			want: accountChoice{"bespoke", "", "flag"}},
		{name: "record", cfg: multi, resume: "s1", proj: "ameriglide", rec: rec("bespoke", true),
			want: accountChoice{"bespoke", "", "record"}},
		{name: "legacy record", cfg: multi, resume: "s1", rec: rec("", true),
			want: accountChoice{"bespoke", "", "legacy-record"}},
		{name: "flag conflicts with record", cfg: multi, resume: "s1", flag: "ameriglide", rec: rec("bespoke", true),
			wantErr: []string{"bespoke", "ameriglide"}},
		{name: "flag matches record", cfg: multi, resume: "s1", flag: "bespoke", rec: rec("bespoke", true),
			want: accountChoice{"bespoke", "", "record"}},
		{name: "resume not found", cfg: multi, resume: "s1", proj: "bespoke", rec: rec("", false),
			want: accountChoice{"bespoke", "", "project"}},
		{name: "unknown project account", cfg: multi, proj: "nope", rec: rec("", false),
			wantErr: []string{"nope"}},
		{name: "unknown flag", cfg: multi, flag: "nope", rec: rec("", false),
			wantErr: []string{"nope"}},
		{name: "implicit config", cfg: ClaudeConfig{}, rec: rec("", false),
			want: accountChoice{"default", "", "default"}},
		{name: "legacy record with no ~/.claude account", rec: rec("", true),
			cfg: ClaudeConfig{DefaultAccount: "a", Accounts: map[string]ClaudeAccount{
				"a": {ConfigDir: "/a"}, "b": {ConfigDir: "/b"}}},
			resume: "s1", wantErr: []string{"s1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveAccount(tc.cfg, "/proj", tc.flag, tc.resume, tc.rec, proj(tc.proj))
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				for _, s := range tc.wantErr {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q lacks %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func TestAccountResolveCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := os.MkdirAll(filepath.Join(xdg, "claudemux"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "claude:\n  default_account: ameriglide\n  accounts:\n    ameriglide:\n      config_dir: /a\n    bespoke: {}\n"
	if err := os.WriteFile(filepath.Join(xdg, "claudemux", "config.yml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, ".claudemux.yml"), []byte("claude_account: \"bespoke\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := runAccount(append([]string{"resolve"}, args...), &out, &errb)
		return code, out.String(), errb.String()
	}

	code, out, _ := run("--dir", proj)
	if code != 0 || out != "bespoke\t\tproject\n" {
		t.Errorf("project: code=%d out=%q", code, out)
	}
	code, out, _ = run("--dir", t.TempDir())
	if code != 0 || out != "ameriglide\t/a\tdefault\n" {
		t.Errorf("default: code=%d out=%q", code, out)
	}
	code, out, errs := run("--dir", proj, "--account", "nope")
	if code != 1 || out != "" || !strings.Contains(errs, "nope") {
		t.Errorf("unknown: code=%d out=%q err=%q", code, out, errs)
	}

	// Resume reads the raw `account` key from a session record.
	recDir := filepath.Join(home, ".claude", "claudemux", "sessions")
	if err := os.MkdirAll(recDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := `{"session_name":"x","session_id":"sid1","account":"bespoke"}`
	if err := os.WriteFile(filepath.Join(recDir, "x.json"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ = run("--dir", t.TempDir(), "--resume", "sid1")
	if code != 0 || out != "bespoke\t\trecord\n" {
		t.Errorf("record: code=%d out=%q", code, out)
	}
}

func TestProjectAccountForIgnoresUnconfigured(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".project.yml"), []byte("claude_account: bespoke\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	multi := ClaudeConfig{DefaultAccount: "a", Accounts: map[string]ClaudeAccount{"a": {ConfigDir: "/a"}, "bespoke": {}}}
	var buf bytes.Buffer
	if got := projectAccountFor(multi, dir, &buf); got != "bespoke" || buf.Len() != 0 {
		t.Errorf("configured: got %q, stderr %q", got, buf.String())
	}
	for name, cfg := range map[string]ClaudeConfig{
		"implicit":    {},
		"other accts": {DefaultAccount: "a", Accounts: map[string]ClaudeAccount{"a": {}}},
	} {
		buf.Reset()
		if got := projectAccountFor(cfg, dir, &buf); got != "" {
			t.Errorf("%s: got %q, want \"\"", name, got)
		}
		if !strings.Contains(buf.String(), `"bespoke"`) || !strings.Contains(buf.String(), ".project.yml") {
			t.Errorf("%s: warning %q", name, buf.String())
		}
	}
}
