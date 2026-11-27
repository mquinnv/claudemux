package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// seedDropKeys and seedDropSubstrings decide which top-level keys of
// ~/.claude.json a new account's .claude.json must NOT inherit. The rule:
// anything that names, belongs to or was fetched for the logged-in
// principal (login, identity, subscription, organisation, API keys, plan
// entitlements, server-evaluated feature caches) is dropped so the new
// account starts clean and fetches its own; anything that describes the
// user or the machine (onboarding flags, theme, project trust under
// `projects`, user-level mcpServers, tip/usage counters, migrations) is
// kept, so a detached first launch does not stop at onboarding or a trust
// prompt. Over-dropping is safe (Claude Code recreates the key); keeping a
// login key is not, so the substrings are matched case-insensitively and
// deliberately broad.
//
// The names were chosen from the key NAMES (never values) of a real
// ~/.claude.json (claude 2.1.x).
var seedDropKeys = map[string]bool{
	"userID":                           true,
	"anonymousId":                      true,
	"claudeCodeFirstTokenDate":         true,
	"claudeAiMcpEverConnected":         true,
	"additionalModelOptionsAnsweredAt": true,
	"remoteControlMachineId":           true,
	"remoteControlReadyPushKey":        true,
}

var seedDropSubstrings = []string{
	"oauth", "account", "subscription", "org", "apikey", "s1m", "grove",
	"passes", "overage", "extrausage", "principal", "credit", "cache",
	"statsig", "growthbook", "featureflag", "experiment", "entitle",
}

// seedDropped reports whether key is login/identity state (see seedDropKeys).
func seedDropped(key string) bool {
	if seedDropKeys[key] {
		return true
	}
	k := strings.ToLower(key)
	for _, s := range seedDropSubstrings {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// seedAccountClaudeJSON creates dst (an account's .claude.json) from src
// (~/.claude.json) minus the login/identity keys, only when dst does not
// exist. An existing dst — even an empty one — is never touched; a missing
// src is nothing to seed. Values are copied as raw JSON; the file is 0600.
func seedAccountClaudeJSON(src, dst string) (bool, error) {
	if _, err := os.Lstat(dst); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	b, err := os.ReadFile(src)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	in := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &in); err != nil {
		return false, fmt.Errorf("%s: %w", src, err)
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		if !seedDropped(k) {
			out[k] = v
		}
	}
	// Re-check right before writing: never replace a file that appeared
	// meanwhile (e.g. a claude started on the account).
	if _, err := os.Lstat(dst); err == nil {
		return false, nil
	}
	if err := writeJSONObject(dst, out); err != nil {
		return false, err
	}
	return true, nil
}
