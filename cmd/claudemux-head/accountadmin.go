package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// swAccountWarnMsg carries the startup account-dir check result to the lobby.
type swAccountWarnMsg struct{ warn string }

// accountCheckCmd checks each configured account dir once, off the Update
// loop. nil (nothing runs) when no account has a config dir.
func accountCheckCmd(dirs map[string]string) tea.Cmd {
	if len(dirs) == 0 {
		return nil
	}
	return func() tea.Msg {
		home, _ := os.UserHomeDir()
		names := make([]string, 0, len(dirs))
		for n := range dirs {
			names = append(names, n)
		}
		sort.Strings(names)
		var warns []string
		for _, n := range names {
			probs, err := checkAccountDir(home, dirs[n])
			if err != nil {
				warns = append(warns, fmt.Sprintf("account %s: %v - run claudemux-head account setup %s", n, err, n))
				continue
			}
			if w := accountWarning(n, probs); w != "" {
				warns = append(warns, w)
			}
		}
		return swAccountWarnMsg{warn: strings.Join(warns, "; ")}
	}
}

// accountDirsOf maps account name to config dir for accounts that have one.
func accountDirsOf(c ClaudeConfig) map[string]string {
	out := map[string]string{}
	for _, n := range c.Names() {
		if d, err := c.ConfigDir(n); err == nil && d != "" {
			out[n] = d
		}
	}
	return out
}

func (m swModel) hasNoticeRow() bool { return m.lastErr != "" || m.accountWarn != "" }

func (m swModel) noticeText() string {
	if m.lastErr != "" {
		return "tmux: " + m.lastErr
	}
	return "⚠ " + m.accountWarn
}

// accountTarget loads the config and resolves name to its config dir.
func accountTarget(name string, stderr io.Writer) (string, bool) {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head: %v\n", err)
		return "", false
	}
	dir, err := cfg.Claude.ConfigDir(name)
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head: %v\n", err)
		return "", false
	}
	return dir, true
}

func runAccountSetup(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: claudemux-head account setup <name>")
		return 2
	}
	dir, ok := accountTarget(args[0], stderr)
	if !ok {
		return 1
	}
	if dir == "" {
		fmt.Fprintf(stdout, "account %s uses ~/.claude: nothing to set up\n", args[0])
		return 0
	}
	home, _ := os.UserHomeDir()
	created, err := setupAccountDir(home, dir)
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head: %v\n", err)
		return 1
	}
	for _, e := range created {
		fmt.Fprintf(stdout, "linked %s\n", e)
	}
	fmt.Fprintf(stdout, "%s: %d links created\n", dir, len(created))
	seeded, err := seedAccountClaudeJSON(filepath.Join(home, ".claude.json"), filepath.Join(dir, ".claude.json"))
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head: seeding .claude.json: %v\n", err)
		return 1
	}
	if seeded {
		fmt.Fprintf(stdout, "seeded .claude.json from ~/.claude.json (login and account keys left out); run: claudemux account login %s\n", args[0])
	}
	return reportAccountProblems(dir, home, stdout)
}

// reportAccountProblems prints dir's problems with fixes; 1 when any.
func reportAccountProblems(dir, home string, w io.Writer) int {
	probs, err := checkAccountDir(home, dir)
	if err != nil {
		fmt.Fprintf(w, "%s: %v\n", dir, err)
		return 1
	}
	for _, p := range probs {
		fmt.Fprintf(w, "problem: %s is %s (want link to %s); %s\n", p.Entry, p.Got, p.Want, problemHint(dir, p))
	}
	if len(probs) > 0 {
		return 1
	}
	return 0
}

func runAccountCheck(args []string, stdout, stderr io.Writer) int {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head: %v\n", err)
		return 3
	}
	names := cfg.Claude.Names()
	if len(args) > 0 {
		names = args[:1]
	}
	home, _ := os.UserHomeDir()
	rc := 0
	for _, n := range names {
		dir, err := cfg.Claude.ConfigDir(n)
		if err != nil {
			fmt.Fprintf(stderr, "claudemux-head: %v\n", err)
			return 1
		}
		if dir == "" {
			fmt.Fprintf(stdout, "%s: ~/.claude, ok\n", n)
			continue
		}
		if reportAccountProblems(dir, home, stdout) != 0 {
			rc = 1
		} else {
			fmt.Fprintf(stdout, "%s: %s, ok\n", n, dir)
		}
	}
	return rc
}

func runAccountLogin(args []string, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: claudemux-head account login <name>")
		return 2
	}
	dir, ok := accountTarget(args[0], stderr)
	if !ok {
		return 1
	}
	fmt.Fprintf(stderr, "logging in account %s: run /login, then /exit\n", args[0])
	err := execAccountLogin(dir)
	fmt.Fprintf(stderr, "claudemux-head: %v\n", err)
	return 1
}

func runAccountList(stdout, stderr io.Writer) int {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head: %v\n", err)
		return 3
	}
	for _, n := range cfg.Claude.Names() {
		dir, _ := cfg.Claude.ConfigDir(n)
		if dir == "" {
			dir = "~/.claude"
		}
		mark := ""
		if n == cfg.Claude.Default() {
			mark = "\t(default)"
		}
		fmt.Fprintf(stdout, "%s\t%s%s\n", n, dir, mark)
	}
	return 0
}
