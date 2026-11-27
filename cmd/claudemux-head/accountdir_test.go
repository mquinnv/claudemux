package main

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// acctHome makes a HOME whose ~/.claude holds the named entries
// (files, except "projects" and "rules" which are dirs).
func acctHome(t *testing.T, entries ...string) string {
	t.Helper()
	home := t.TempDir()
	cd := filepath.Join(home, ".claude")
	if err := os.MkdirAll(cd, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		p := filepath.Join(cd, e)
		if e == "projects" || e == "rules" {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func TestSetupAccountDirCreatesLinks(t *testing.T) {
	home := acctHome(t, "settings.json", "projects", "team-kernel.foo.md")
	dir := filepath.Join(home, "acct")
	created, err := setupAccountDir(home, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"projects", "settings.json", "team-kernel.foo.md"}
	got := slices.Clone(created)
	slices.Sort(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("created = %v, want %v", got, want)
	}
	for _, e := range want {
		tgt, err := os.Readlink(filepath.Join(dir, e))
		if err != nil || tgt != filepath.Join(home, ".claude", e) {
			t.Errorf("%s -> %q, %v", e, tgt, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "CLAUDE.md")); err == nil {
		t.Error("CLAUDE.md missing in ~/.claude must be skipped")
	}
}

func TestSetupAccountDirIdempotent(t *testing.T) {
	home := acctHome(t, "settings.json", "rules")
	dir := filepath.Join(home, "acct")
	if _, err := setupAccountDir(home, dir); err != nil {
		t.Fatal(err)
	}
	created, err := setupAccountDir(home, dir)
	if err != nil || len(created) != 0 {
		t.Fatalf("second run created %v, %v", created, err)
	}
}

func TestSetupAccountDirKeepsClaudeJSON(t *testing.T) {
	home := acctHome(t, "settings.json")
	dir := filepath.Join(home, "acct")
	os.MkdirAll(dir, 0o755)
	body := []byte(`{"userID":"abc"}`)
	cj := filepath.Join(dir, ".claude.json")
	os.WriteFile(cj, body, 0o600)
	if _, err := setupAccountDir(home, dir); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cj)
	if string(got) != string(body) {
		t.Fatalf(".claude.json changed: %q", got)
	}
}

func TestSetupAccountDirNeverOverwritesReal(t *testing.T) {
	home := acctHome(t, "settings.json")
	dir := filepath.Join(home, "acct")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte("mine"), 0o644)
	created, err := setupAccountDir(home, dir)
	if err != nil || len(created) != 0 {
		t.Fatalf("created %v, %v", created, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "settings.json")); string(b) != "mine" {
		t.Fatalf("real file overwritten: %q", b)
	}
}

func TestCheckAccountDirDetectsRealFile(t *testing.T) {
	home := acctHome(t, "settings.json")
	dir := filepath.Join(home, "acct")
	setupAccountDir(home, dir)
	p := filepath.Join(dir, "settings.json")
	os.Remove(p)
	os.WriteFile(p, []byte("real"), 0o644)
	probs, err := checkAccountDir(home, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []dirProblem{{Entry: "settings.json", Want: filepath.Join(home, ".claude", "settings.json"), Got: "real-file"}}
	if !reflect.DeepEqual(probs, want) {
		t.Fatalf("probs = %+v, want %+v", probs, want)
	}
}

func TestCheckAccountDirWrongTargetMissingAndRealDir(t *testing.T) {
	home := acctHome(t, "settings.json", "projects", "rules")
	dir := filepath.Join(home, "acct")
	setupAccountDir(home, dir)
	os.Remove(filepath.Join(dir, "settings.json"))
	os.Symlink("/elsewhere", filepath.Join(dir, "settings.json"))
	os.Remove(filepath.Join(dir, "projects"))
	os.Mkdir(filepath.Join(dir, "projects"), 0o755)
	os.Remove(filepath.Join(dir, "rules"))
	probs, _ := checkAccountDir(home, dir)
	got := map[string]string{}
	for _, p := range probs {
		got[p.Entry] = p.Got
	}
	want := map[string]string{"settings.json": "wrong-target", "projects": "real-dir", "rules": "missing"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestAccountCheckCmd(t *testing.T) {
	if accountCheckCmd(nil) != nil {
		t.Fatal("no account dirs: nothing must run")
	}
	home := acctHome(t, "settings.json")
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "acct")
	os.MkdirAll(dir, 0o755)
	msg := accountCheckCmd(map[string]string{"bespoke": dir})().(swAccountWarnMsg)
	if msg.warn != "account bespoke: 1 setup problems - run claudemux-head account check" {
		t.Fatalf("warn = %q", msg.warn)
	}
	setupAccountDir(home, dir)
	if msg := accountCheckCmd(map[string]string{"bespoke": dir})().(swAccountWarnMsg); msg.warn != "" {
		t.Fatalf("clean dir warned: %q", msg.warn)
	}
}

func TestAccountLoginCommand(t *testing.T) {
	env := []string{"A=1", "CLAUDE_CONFIG_DIR=/old", "B=2"}
	argv, out := accountLoginCommand("/bin/claude", "/x/acct", env)
	if !reflect.DeepEqual(argv, []string{"/bin/claude"}) {
		t.Fatalf("argv = %v", argv)
	}
	if !reflect.DeepEqual(out, []string{"A=1", "B=2", "CLAUDE_CONFIG_DIR=/x/acct"}) {
		t.Fatalf("env = %v", out)
	}
	_, out = accountLoginCommand("/bin/claude", "", env)
	if !reflect.DeepEqual(out, []string{"A=1", "B=2"}) {
		t.Fatalf("~/.claude env = %v", out)
	}
}
