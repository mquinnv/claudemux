package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// resolverPathForTest locates the repo's project-color-resolve.sh from the
// package directory. Tests run with the package dir as cwd.
func resolverPathForTest(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "bin", "project-color-resolve.sh"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("resolver not found at %s: %v", p, err)
	}
	return p
}

// runResolveStyle invokes resolve_project_style the same way tabreset.go does:
// sourced, with arguments passed positionally so nothing reaches a shell parser.
func runResolveStyle(t *testing.T, dir string) (string, error) {
	t.Helper()
	out, err := exec.Command("bash", "-c",
		`. "$1"; resolve_project_style "$2"`,
		"_", resolverPathForTest(t), dir).Output()
	return strings.TrimSpace(string(out)), err
}

// A dark project color must pair with a white foreground.
func TestResolveProjectStyleDarkColorPicksWhite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".project.yml"), []byte("color: purple\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := runResolveStyle(t, dir)
	if err != nil {
		t.Fatalf("resolve_project_style: %v", err)
	}
	if got != "b34dff #ffffff" {
		t.Errorf("style = %q, want %q", got, "b34dff #ffffff")
	}
}

// A light project color must pair with a black foreground. yellow is ffd24d,
// luminance 199, comfortably over the 150 threshold.
func TestResolveProjectStyleLightColorPicksBlack(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".project.yml"), []byte("color: yellow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := runResolveStyle(t, dir)
	if err != nil {
		t.Fatalf("resolve_project_style: %v", err)
	}
	if got != "ffd24d #000000" {
		t.Errorf("style = %q, want %q", got, "ffd24d #000000")
	}
}

// The resolver reads the current config name, not only the legacy one.
func TestResolveProjectStyleReadsCurrentConfigName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, projectConfigName), []byte("color: purple\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := runResolveStyle(t, dir)
	if err != nil {
		t.Fatalf("resolve_project_style: %v", err)
	}
	if got != "b34dff #ffffff" {
		t.Errorf("style = %q, want %q", got, "b34dff #ffffff")
	}
}

// A project mid-rename has both files. The current name wins, matching
// projectConfigPath — otherwise the tab and the status bar could disagree about
// a project's color depending on which reader ran.
func TestResolveProjectStylePrefersCurrentConfigName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, projectConfigName), []byte("color: purple\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, legacyProjectConfigName), []byte("color: yellow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := runResolveStyle(t, dir)
	if err != nil {
		t.Fatalf("resolve_project_style: %v", err)
	}
	if got != "b34dff #ffffff" {
		t.Errorf("style = %q, want purple (the current name), got %q", got, got)
	}
}

// A nearer legacy file beats a farther current one: the walk is by distance
// first, name preference second. A worktree carrying its own .project.yml must
// not be overruled by a .claudemux.yml further up the tree.
func TestResolveProjectStyleNearestAncestorWins(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, projectConfigName), []byte("color: purple\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, legacyProjectConfigName), []byte("color: yellow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := runResolveStyle(t, child)
	if err != nil {
		t.Fatalf("resolve_project_style: %v", err)
	}
	if got != "ffd24d #000000" {
		t.Errorf("style = %q, want yellow (the nearer file)", got)
	}
}

// No project color anywhere up the tree: non-zero exit, no output. t.TempDir()
// is under /var/folders, which has no project config ancestors.
func TestResolveProjectStyleNoColor(t *testing.T) {
	got, err := runResolveStyle(t, t.TempDir())
	if err == nil {
		t.Error("err = nil, want non-zero exit when no color is configured")
	}
	if got != "" {
		t.Errorf("output = %q, want empty", got)
	}
}

// The installed layout: everything beside the binary. First directory wins.
func TestFindShippedScriptPrefersBinaryDir(t *testing.T) {
	exists := func(p string) bool { return p == "/opt/claudemux/project-color-resolve.sh" }
	got, ok := findShippedScript("project-color-resolve.sh",
		[]string{"/opt/claudemux", "/repo/bin"}, exists)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got != "/opt/claudemux/project-color-resolve.sh" {
		t.Errorf("path = %q, want the binary-dir copy", got)
	}
}

// The development layout: the script is not beside the binary, only beside the
// claudemux symlink's target. Without this fallback the reset loses its colors
// on the machine of the person who asked for the feature.
func TestFindShippedScriptFallsBackToClaudemuxDir(t *testing.T) {
	exists := func(p string) bool { return p == "/repo/bin/project-color-resolve.sh" }
	got, ok := findShippedScript("project-color-resolve.sh",
		[]string{"/Users/x/go/bin", "/repo/bin"}, exists)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got != "/repo/bin/project-color-resolve.sh" {
		t.Errorf("path = %q, want the repo copy", got)
	}
}

// Neither route finds it: the caller treats this as "no project color".
func TestFindShippedScriptMissing(t *testing.T) {
	exists := func(string) bool { return false }
	if _, ok := findShippedScript("project-color-resolve.sh",
		[]string{"/a", "/b"}, exists); ok {
		t.Error("ok = true with no copy anywhere, want false")
	}
}

// An empty candidate directory is skipped, not joined into a bare "/name".
// os.Executable and exec.LookPath can each fail independently.
func TestFindShippedScriptSkipsEmptyDirs(t *testing.T) {
	var probed []string
	exists := func(p string) bool { probed = append(probed, p); return false }
	findShippedScript("s.sh", []string{"", "/b"}, exists)
	for _, p := range probed {
		if p == "/s.sh" {
			t.Error("probed /s.sh, want empty dirs skipped")
		}
	}
}

// siblingOfExecutable resolves a name against the directory holding the test
// binary itself, which is what gives srcFor's default resolution its meaning.
func TestSiblingOfExecutable(t *testing.T) {
	got, err := siblingOfExecutable("some-script.sh")
	if err != nil {
		t.Fatalf("siblingOfExecutable: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Skipf("EvalSymlinks unavailable: %v", err)
	}
	if want := filepath.Join(filepath.Dir(resolved), "some-script.sh"); got != want {
		t.Errorf("siblingOfExecutable = %q, want %q", got, want)
	}
	if filepath.Base(got) != "some-script.sh" {
		t.Errorf("basename = %q, want the requested name", filepath.Base(got))
	}
}

// A declared name is used as-is when the session is the project's only one.
func TestRestoreNameUsesDeclaredName(t *testing.T) {
	if got := restoreName("Remix", "remix", "/Users/x/Projects/remix"); got != "Remix" {
		t.Errorf("name = %q, want %q", got, "Remix")
	}
}

// `claudemux -n` clones a session as remix-2, remix-3, ... while the work
// directory — and therefore .project.yml — stays the same. Verified live on
// 2026-07-31: four sessions, one .project.yml declaring `name: Remix`, one
// color. Restoring all four to "Remix" would leave four indistinguishable tabs,
// which is the confusion this feature exists to end.
func TestRestoreNameCarriesCloneSuffix(t *testing.T) {
	for _, tc := range []struct{ session, want string }{
		{"remix-2", "Remix 2"},
		{"remix-3", "Remix 3"},
		{"remix-10", "Remix 10"},
	} {
		if got := restoreName("Remix", tc.session, "/Users/x/Projects/remix"); got != tc.want {
			t.Errorf("restoreName(%q) = %q, want %q", tc.session, got, tc.want)
		}
	}
}

// No declared name: the session name is already unique and needs no help.
func TestRestoreNameFallsBackToSession(t *testing.T) {
	if got := restoreName("", "claudemux", "/Users/x/Projects/claudemux"); got != "claudemux" {
		t.Errorf("name = %q, want %q", got, "claudemux")
	}
	if got := restoreName("", "remix-2", "/Users/x/Projects/remix"); got != "remix-2" {
		t.Errorf("name = %q, want %q", got, "remix-2")
	}
}

// A session renamed to something unrelated to the directory cannot have a
// suffix derived from it; the session name stands rather than guessing.
func TestRestoreNameUnrelatedSessionName(t *testing.T) {
	if got := restoreName("Remix", "scratch", "/Users/x/Projects/remix"); got != "scratch" {
		t.Errorf("name = %q, want %q", got, "scratch")
	}
	// A session hand-renamed to <basename>-<word> is not a `claudemux -n` clone:
	// the suffix is not a clone number, so there is nothing to carry onto the
	// declared name and the session name stands.
	if got := restoreName("Remix", "remix-scratch", "/Users/x/Projects/remix"); got != "remix-scratch" {
		t.Errorf("name = %q, want %q", got, "remix-scratch")
	}
}

func TestProjectDeclaredName(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".project.yml")
	// Trailing whitespace is real: the live remix/.project.yml has "name: Remix ".
	if err := os.WriteFile(p, []byte("color: red\nname: Remix \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := projectDeclaredName(p); got != "Remix" {
		t.Errorf("name = %q, want %q", got, "Remix")
	}
}

// A .project.yml is gitignored, so worktrees and fresh clones simply lack one.
// That is not an error; it means "no declared name".
func TestProjectDeclaredNameMissingFileOrKey(t *testing.T) {
	if got := projectDeclaredName(filepath.Join(t.TempDir(), ".project.yml")); got != "" {
		t.Errorf("name = %q, want empty for a missing file", got)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, ".project.yml")
	if err := os.WriteFile(p, []byte("color: red\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := projectDeclaredName(p); got != "" {
		t.Errorf("name = %q, want empty when no name: key", got)
	}
}

func TestParseProjectStyle(t *testing.T) {
	hex, fg, ok := parseProjectStyle("b34dff #ffffff\n")
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if hex != "b34dff" || fg != "#ffffff" {
		t.Errorf("hex/fg = %q/%q, want b34dff/#ffffff", hex, fg)
	}
}

func TestParseProjectStyleRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "\n", "b34dff", "not a color", "zzzzzz #ffffff"} {
		if _, _, ok := parseProjectStyle(in); ok {
			t.Errorf("parseProjectStyle(%q) ok = true, want false", in)
		}
	}
}

// The resolver's contract is "<hex> <fg>" where fg is itself a color. A
// garbage second field (no leading '#', wrong length, non-hex digits) must be
// rejected rather than handed to tmux as a literal fg= value.
func TestParseProjectStyleRejectsGarbageForeground(t *testing.T) {
	for _, in := range []string{
		"b34dff ffffff",   // missing leading #
		"b34dff #fffff",   // 5 digits
		"b34dff #fffffff", // 7 digits
		"b34dff #zzzzzz",  // non-hex digits
		"b34dff #",        // bare hash
	} {
		if _, _, ok := parseProjectStyle(in); ok {
			t.Errorf("parseProjectStyle(%q) ok = true, want false", in)
		}
	}
}

func TestTabResetTmuxArgs(t *testing.T) {
	got := tabResetTmuxArgs("%3", "claudemux", "claudemux", "b34dff", "#ffffff")
	want := [][]string{
		{"rename-window", "-t", "%3", "claudemux"},
		{"set", "-t", "claudemux", "status-style", "bg=#b34dff,fg=#ffffff"},
		{"set", "-w", "-t", "claudemux", "pane-active-border-style", "fg=#b34dff"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d commands, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if strings.Join(got[i], "\x00") != strings.Join(want[i], "\x00") {
			t.Errorf("cmd[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// A project with no color still gets its title back.
func TestTabResetTmuxArgsNoColor(t *testing.T) {
	got := tabResetTmuxArgs("%3", "sonar", "sonar", "", "")
	if len(got) != 1 {
		t.Fatalf("got %d commands, want 1 (rename only): %v", len(got), got)
	}
	if got[0][0] != "rename-window" {
		t.Errorf("cmd[0] = %v, want a rename-window", got[0])
	}
}

// The restored name goes through the same normalization as the summary path
// (tabRenameArgs: collapseWhitespace then truncateWords) — a quoted
// .project.yml name: value containing a newline, or an overlong name, must
// not land verbatim in the tmux rename-window argument.
func TestTabResetTmuxArgsNormalizesName(t *testing.T) {
	got := tabResetTmuxArgs("%3", "claudemux", "claudemux  \n  project", "", "")
	if len(got) != 1 {
		t.Fatalf("got %d commands, want 1: %v", len(got), got)
	}
	want := "claudemux project"
	if got[0][len(got[0])-1] != want {
		t.Errorf("rename target = %q, want %q", got[0][len(got[0])-1], want)
	}
}

// An overlong name is truncated the same way tabRenameArgs truncates a model
// label: at a word boundary, to tabTitleMaxRunes, with an ellipsis.
func TestTabResetTmuxArgsTruncatesOverlongName(t *testing.T) {
	long := strings.Repeat("a", tabTitleMaxRunes+20)
	got := tabResetTmuxArgs("%3", "claudemux", long, "", "")
	if len(got) != 1 {
		t.Fatalf("got %d commands, want 1: %v", len(got), got)
	}
	name := got[0][len(got[0])-1]
	if len([]rune(name)) > tabTitleMaxRunes {
		t.Errorf("rename target has %d runes, want <= %d: %q", len([]rune(name)), tabTitleMaxRunes, name)
	}
	if want := truncateWords(long, tabTitleMaxRunes); name != want {
		t.Errorf("rename target = %q, want %q (truncateWords' own output)", name, want)
	}
}

// Outside tmux there is no pane to rename and no session to style.
func TestTabResetTmuxArgsNoPaneOrSession(t *testing.T) {
	if got := tabResetTmuxArgs("", "claudemux", "claudemux", "b34dff", "#ffffff"); len(got) != 2 {
		t.Errorf("got %d commands with no pane, want 2 style-only: %v", len(got), got)
	}
	if got := tabResetTmuxArgs("%3", "", "claudemux", "b34dff", "#ffffff"); len(got) != 1 {
		t.Errorf("got %d commands with no session, want 1 rename-only: %v", len(got), got)
	}
}

// The exact byte sequence bin/claudemux:apply_tab_color writes, for purple.
func TestItermTabColorBytes(t *testing.T) {
	got := string(itermTabColorBytes("b34dff"))
	want := "\033]6;1;bg;red;brightness;179\a" +
		"\033]6;1;bg;green;brightness;77\a" +
		"\033]6;1;bg;blue;brightness;255\a"
	if got != want {
		t.Errorf("bytes = %q, want %q", got, want)
	}
}

func TestItermTabColorBytesRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "fff", "zzzzzz", "b34dffff"} {
		if got := itermTabColorBytes(in); got != nil {
			t.Errorf("itermTabColorBytes(%q) = %q, want nil", in, got)
		}
	}
}

// tmux is asked for three values in one call, newline-separated, because a
// session name may contain spaces and a space-separated format could not be
// split back apart safely.
func TestParseSessionAndTTY(t *testing.T) {
	session, tty := parseSessionAndTTY("claudemux\n/dev/ttys036\n1\n")
	if session != "claudemux" {
		t.Errorf("session = %q, want %q", session, "claudemux")
	}
	if tty != "/dev/ttys036" {
		t.Errorf("tty = %q, want %q", tty, "/dev/ttys036")
	}
}

// A detached session has NO client of its own, but #{client_tty} does not
// come back empty: tmux falls back to the globally most-recently-active
// client, which belongs to some unrelated attached session. Measured live on
// 2026-07-31 (tmux 3.7b): a detached "cd-receiver" session's
// `display-message -t cd-receiver '#{client_tty}'` printed the tty of the
// client attached to a completely different session ("beejax-2"). The only
// reliable signal that the tty is NOT this session's own client is
// #{session_attached}: it is "1" only when the target session itself has an
// attached client. So detachedness must be read from session_attached, not
// from an empty tty field — an empty tty never actually occurs here.
func TestParseSessionAndTTYDetached(t *testing.T) {
	session, tty := parseSessionAndTTY("cd-receiver\n/dev/ttys028\n0\n")
	if session != "cd-receiver" {
		t.Errorf("session = %q, want %q", session, "cd-receiver")
	}
	if tty != "" {
		t.Errorf("tty = %q, want empty for a detached session even though tmux reported another client's tty", tty)
	}
}

// session_attached can in principle be a count greater than 1 (multiple
// clients attached to the same session); only exactly "1" is treated as "the
// tty belongs to us". Anything else means "not trustworthy, skip the tab
// color write".
func TestParseSessionAndTTYNotExactlyOneAttached(t *testing.T) {
	session, tty := parseSessionAndTTY("claudemux\n/dev/ttys036\n2\n")
	if session != "claudemux" {
		t.Errorf("session = %q, want %q", session, "claudemux")
	}
	if tty != "" {
		t.Errorf("tty = %q, want empty when session_attached != 1", tty)
	}
}

// A session name containing spaces survives the newline-separated format.
func TestParseSessionAndTTYSessionWithSpaces(t *testing.T) {
	session, _ := parseSessionAndTTY("my project\n/dev/ttys001\n1\n")
	if session != "my project" {
		t.Errorf("session = %q, want %q", session, "my project")
	}
}

// Outside tmux the call fails and yields nothing rather than a bogus target.
func TestParseSessionAndTTYEmpty(t *testing.T) {
	session, tty := parseSessionAndTTY("")
	if session != "" || tty != "" {
		t.Errorf("session/tty = %q/%q, want empty/empty", session, tty)
	}
}

// resetTabCmd always returns a runnable command. Even with no pane there is
// still a session style to repair, and the latch in Task 5 appends it
// unconditionally.
func TestResetTabCmdIsNeverNil(t *testing.T) {
	if resetTabCmd("", t.TempDir()) == nil {
		t.Error("resetTabCmd = nil, want a runnable command")
	}
}
