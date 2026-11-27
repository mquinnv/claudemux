package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// implicitAccount names the one account that exists when config.yml has no
// `claude:` block. It has no config_dir, i.e. it is the ~/.claude account.
const implicitAccount = "default"

var accountNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ClaudeAccount is one Claude login. An empty ConfigDir means ~/.claude:
// launches on it unset CLAUDE_CONFIG_DIR rather than pointing it there.
type ClaudeAccount struct {
	ConfigDir string `yaml:"config_dir"`
	Color     string `yaml:"color"`
}

// ClaudeConfig is the `claude:` block. "Default account" (which account a
// project without claude_account gets) is a different idea from "the ~/.claude
// account" (the one with no config_dir); they may or may not coincide.
type ClaudeConfig struct {
	DefaultAccount string                   `yaml:"default_account"`
	Accounts       map[string]ClaudeAccount `yaml:"accounts"`
}

// Names returns the account names, sorted; just the implicit one when no
// accounts are configured.
func (c ClaudeConfig) Names() []string {
	if len(c.Accounts) == 0 {
		return []string{implicitAccount}
	}
	names := make([]string, 0, len(c.Accounts))
	for n := range c.Accounts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Multi reports whether more than one account is configured.
func (c ClaudeConfig) Multi() bool { return len(c.Accounts) > 1 }

// Default is the account a project gets when it names none.
func (c ClaudeConfig) Default() string {
	if len(c.Accounts) == 0 {
		return implicitAccount
	}
	return c.DefaultAccount
}

// ConfigDir returns the account's expanded CLAUDE_CONFIG_DIR, or "" for the
// ~/.claude account. An unknown name is an error, never a fallback.
func (c ClaudeConfig) ConfigDir(name string) (string, error) {
	if len(c.Accounts) == 0 {
		if name == implicitAccount {
			return "", nil
		}
		return "", fmt.Errorf("unknown claude account %q", name)
	}
	acct, ok := c.Accounts[name]
	if !ok {
		return "", fmt.Errorf("unknown claude account %q", name)
	}
	return resolveAccountDir(acct.ConfigDir), nil
}

// resolveAccountDir expands ~ and returns "" for an unset dir.
func resolveAccountDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	return filepath.Clean(expandTilde(dir))
}

// canonicalDir cleans p and resolves symlinks, falling back to the cleaned
// path when it cannot (e.g. the directory does not exist yet).
func canonicalDir(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// ForConfigDir maps a $CLAUDE_CONFIG_DIR value (possibly "") to an account.
// "", ~/.claude and ~/.claude/ map to the account with no config_dir.
func (c ClaudeConfig) ForConfigDir(envDir string) (string, bool) {
	envDir = strings.TrimSpace(envDir)
	home, _ := os.UserHomeDir()
	isDefault := envDir == ""
	if !isDefault && home != "" {
		isDefault = canonicalDir(expandTilde(envDir)) == canonicalDir(filepath.Join(home, ".claude"))
	}

	if len(c.Accounts) == 0 {
		if isDefault {
			return implicitAccount, true
		}
		return "", false
	}
	if isDefault {
		for _, n := range c.Names() {
			if strings.TrimSpace(c.Accounts[n].ConfigDir) == "" {
				return n, true
			}
		}
		return "", false
	}
	want := canonicalDir(expandTilde(envDir))
	for _, n := range c.Names() {
		d := resolveAccountDir(c.Accounts[n].ConfigDir)
		if d != "" && canonicalDir(d) == want {
			return n, true
		}
	}
	return "", false
}

// Validate checks the block is internally consistent.
func (c ClaudeConfig) Validate() error {
	if len(c.Accounts) == 0 {
		if c.DefaultAccount != "" && c.DefaultAccount != implicitAccount {
			return fmt.Errorf("default_account %q is not a configured account", c.DefaultAccount)
		}
		return nil
	}
	for _, n := range c.Names() {
		if !accountNameRE.MatchString(n) {
			return fmt.Errorf("account name %q must match %s", n, accountNameRE)
		}
	}
	if _, ok := c.Accounts[c.DefaultAccount]; !ok {
		return fmt.Errorf("default_account %q is not a configured account", c.DefaultAccount)
	}
	noDir := ""
	dirs := map[string]string{}
	for _, n := range c.Names() {
		d := resolveAccountDir(c.Accounts[n].ConfigDir)
		if d == "" {
			if noDir != "" {
				return fmt.Errorf("accounts %q and %q both lack config_dir; at most one account may use ~/.claude", noDir, n)
			}
			noDir = n
			continue
		}
		if other, dup := dirs[d]; dup {
			return fmt.Errorf("accounts %q and %q share config_dir %s", other, n, d)
		}
		dirs[d] = n
	}
	return nil
}
