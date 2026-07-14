package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// configLookup resolves a dotted path ("onepassword.accounts.acme") against cfg
// and returns its value as a string, or ok=false if the path is absent or does
// not land on a scalar.
//
// It round-trips cfg through YAML rather than reflecting over the struct: the
// YAML tags already define the exact key names a user writes in config.yml, so
// marshalling gives us that namespace for free and cannot drift from it. This
// runs once per process invocation, so the cost is irrelevant.
func configLookup(cfg Config, dotted string) (string, bool) {
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return "", false
	}
	var tree map[string]any
	if err := yaml.Unmarshal(raw, &tree); err != nil {
		return "", false
	}

	var cur any = tree
	for _, part := range strings.Split(dotted, ".") {
		node, ok := cur.(map[string]any)
		if !ok {
			return "", false // walked into a scalar with path left to go
		}
		cur, ok = node[part]
		if !ok {
			return "", false
		}
	}

	switch v := cur.(type) {
	case string:
		return v, true
	case bool:
		return strconv.FormatBool(v), true
	case int:
		return strconv.Itoa(v), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	default:
		// Maps and sequences have no single printable value.
		return "", false
	}
}

// runConfigGet implements `claude-head config get <dotted.path>` and returns the
// process exit code.
//
// The exit codes are a contract with bin/claude-env, which calls this on every
// launch — via its ch_config_get helper, which inspects the code rather than
// discarding it:
//
//	0 — found; value on stdout
//	1 — absent key; NOTHING on stdout or stderr. An org the user has not
//	    configured is the normal case, not an error worth printing, and
//	    ch_config_get stays silent on it.
//	2 — usage error (wrong argument count)
//	3 — config.yml exists but does not parse (or the config dir cannot be
//	    resolved at all). Distinct from 1 precisely so the caller can tell a
//	    broken config apart from an unconfigured key: ch_config_get re-emits
//	    this one's stderr, so a malformed config surfaces instead of degrading
//	    into a silent, secrets-less launch. Collapsing 3 into 1 would restore
//	    that bug — no test covers this seam, so it is on you.
func runConfigGet(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: claude-head config get <dotted.path>")
		return 2
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "claude-head: %v\n", err)
		return 3
	}

	v, ok := configLookup(cfg, args[0])
	if !ok {
		return 1
	}
	fmt.Fprintln(stdout, v)
	return 0
}
