package main

import (
	"os/exec"
	"testing"
)

// account_env_prefix is a pure helper in the bash launcher; source just that
// function out of the script and run it.
func accountEnvPrefix(t *testing.T, dir string) string {
	t.Helper()
	script := `source <(sed -n '/^account_env_prefix()/,/^}/p' ` + launcherPath + `); account_env_prefix "$1"`
	out, err := exec.Command("bash", "-c", script, "bash", dir).Output()
	if err != nil {
		t.Fatalf("account_env_prefix(%q): %v", dir, err)
	}
	return string(out)
}

func TestAccountEnvPrefix(t *testing.T) {
	cases := []struct{ dir, want string }{
		{"/h/.claude-accounts/ameriglide", "CLAUDE_CONFIG_DIR=/h/.claude-accounts/ameriglide "},
		{"", "env -u CLAUDE_CONFIG_DIR "},
		{"/h/my dir/acct", `CLAUDE_CONFIG_DIR=/h/my\ dir/acct `},
	}
	for _, c := range cases {
		if got := accountEnvPrefix(t, c.dir); got != c.want {
			t.Errorf("account_env_prefix(%q) = %q, want %q", c.dir, got, c.want)
		}
	}
}
