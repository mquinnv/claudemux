package main

import (
	"flag"
	"fmt"
	"io"
)

// accountChoice is the account a session runs on. Source says why:
// flag|record|project|default|legacy-record.
type accountChoice struct {
	Name, ConfigDir string
	Source          string
}

// resolveAccount applies the precedence rules: a resumed session's recorded
// account wins (a conflicting flag is an error), then the flag, then the
// project's claude_account, then the configured default. Every name goes
// through cfg.ConfigDir, so an unknown name is an error, never a fallback.
func resolveAccount(cfg ClaudeConfig, dir, flag, resumeID string,
	recordAccount func(id string) (string, bool), projectAccount func(dir string) string) (accountChoice, error) {
	name, source := "", ""
	if resumeID != "" && recordAccount != nil {
		if acct, found := recordAccount(resumeID); found {
			if acct != "" {
				name, source = acct, "record"
			} else {
				for _, n := range cfg.Names() {
					if d, err := cfg.ConfigDir(n); err == nil && d == "" {
						name, source = n, "legacy-record"
						break
					}
				}
				if name == "" {
					return accountChoice{}, fmt.Errorf("session %s has no recorded account and no account uses ~/.claude", resumeID)
				}
			}
			if flag != "" && flag != name {
				return accountChoice{}, fmt.Errorf("session %s ran on %s; -a %s would switch it", resumeID, name, flag)
			}
		}
	}
	if name == "" {
		proj := ""
		if projectAccount != nil {
			proj = projectAccount(dir)
		}
		switch {
		case flag != "":
			name, source = flag, "flag"
		case proj != "":
			name, source = proj, "project"
		default:
			name, source = cfg.Default(), "default"
		}
	}
	cd, err := cfg.ConfigDir(name)
	if err != nil {
		return accountChoice{}, err
	}
	return accountChoice{Name: name, ConfigDir: cd, Source: source}, nil
}

// recordedAccount scans session records for sessionID and returns the account
// of the first match.
func recordedAccount(dir, sessionID string) (string, bool) {
	for _, lr := range readSessionRecords(dir) {
		if lr.Rec.SessionID == sessionID {
			return lr.Rec.Account, true
		}
	}
	return "", false
}

// runAccount dispatches `claudemux-head account <sub>`.
func runAccount(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "resolve":
			return runAccountResolve(args[1:], stdout, stderr)
		case "setup":
			return runAccountSetup(args[1:], stdout, stderr)
		case "check":
			return runAccountCheck(args[1:], stdout, stderr)
		case "login":
			return runAccountLogin(args[1:], stderr)
		case "list":
			return runAccountList(stdout, stderr)
		}
	}
	fmt.Fprintln(stderr, "usage: claudemux-head account resolve --dir D [--account X] [--resume ID] | setup <name> | check [name] | login <name> | list")
	return 2
}

func runAccountResolve(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("account resolve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "project directory")
	acct := fs.String("account", "", "account requested with -a")
	resume := fs.String("resume", "", "session id being resumed")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head: %v\n", err)
		return 3
	}
	recDir := sessionRecordDir()
	c, err := resolveAccount(cfg.Claude, *dir, *acct, *resume,
		func(id string) (string, bool) { return recordedAccount(recDir, id) },
		func(d string) string { return projectField(d, "claude_account") })
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s\t%s\t%s\n", c.Name, c.ConfigDir, c.Source)
	return 0
}
