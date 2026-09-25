package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// skillPath is where hook ensure installs the shipped skill under $HOME.
func skillPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.Getenv("HOME"), ".claude", "skills", "claudemux-handoff", "SKILL.md")
}

func shippedSkill(t *testing.T) []byte {
	t.Helper()
	b, err := fs.ReadFile(shippedSkills, "skills/claudemux-handoff/SKILL.md")
	if err != nil {
		t.Fatalf("skill not embedded: %v", err)
	}
	return b
}

func TestHookEnsureInstallsSkill(t *testing.T) {
	writeSettings(t, "")
	var out, errb bytes.Buffer
	if rc := runHookEnsure([]string{"--script", stubScript(t)}, &out, &errb); rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb.String())
	}
	got, err := os.ReadFile(skillPath(t))
	if err != nil {
		t.Fatalf("skill not installed: %v", err)
	}
	if !bytes.Equal(got, shippedSkill(t)) {
		t.Error("installed skill differs from the embedded copy")
	}
}

// hook ensure runs on every launch, so an up-to-date skill must not be
// rewritten each time.
func TestHookEnsureLeavesCurrentSkillAlone(t *testing.T) {
	writeSettings(t, "")
	script := stubScript(t)
	var out, errb bytes.Buffer
	if rc := runHookEnsure([]string{"--script", script}, &out, &errb); rc != 0 {
		t.Fatalf("first run rc=%d stderr=%s", rc, errb.String())
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(skillPath(t), old, old); err != nil {
		t.Fatal(err)
	}
	if rc := runHookEnsure([]string{"--script", script}, &out, &errb); rc != 0 {
		t.Fatalf("second run rc=%d stderr=%s", rc, errb.String())
	}
	info, err := os.Stat(skillPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Error("an unchanged skill was rewritten")
	}
}

// An upgrade ships a new skill; the stale installed copy is replaced.
func TestHookEnsureReplacesStaleSkill(t *testing.T) {
	writeSettings(t, "")
	p := skillPath(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("old version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if rc := runHookEnsure([]string{"--script", stubScript(t)}, &out, &errb); rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errb.String())
	}
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, shippedSkill(t)) {
		t.Error("stale skill was not replaced")
	}
}

// The exit-3 contract is "NOTHING is written"; that covers skills too.
func TestHookEnsureMalformedSettingsInstallsNoSkill(t *testing.T) {
	writeSettings(t, "{not json")
	var out, errb bytes.Buffer
	if rc := runHookEnsure([]string{"--script", stubScript(t)}, &out, &errb); rc != 3 {
		t.Fatalf("rc=%d, want 3", rc)
	}
	if _, err := os.Stat(skillPath(t)); !os.IsNotExist(err) {
		t.Errorf("skill written despite malformed settings (stat err=%v)", err)
	}
}

// A skill that cannot be written must not cost the hooks: they are registered
// first, and the failure is still reported as an I/O error.
func TestHookEnsureSkillFailureKeepsHooks(t *testing.T) {
	settingsPath := writeSettings(t, "")
	// A regular file where the skills directory belongs makes MkdirAll fail.
	if err := os.WriteFile(filepath.Join(filepath.Dir(settingsPath), "skills"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if rc := runHookEnsure([]string{"--script", stubScript(t)}, &out, &errb); rc != 4 {
		t.Fatalf("rc=%d, want 4", rc)
	}
	if len(hookCommands(t, readSettings(t, settingsPath), "SessionStart")) == 0 {
		t.Error("hooks were not registered when the skill install failed")
	}
}
