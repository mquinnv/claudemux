package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// seedFixture is a made-up ~/.claude.json: identity/login keys next to
// machine/user state that a new account should inherit.
const seedFixture = `{
  "oauthAccount": {"emailAddress": "x@example.com", "organizationUuid": "o"},
  "userID": "u1",
  "anonymousId": "a1",
  "customApiKeyResponses": {"approved": ["k"]},
  "primaryApiKey": "sk-test",
  "hasAvailableSubscription": true,
  "subscriptionNoticeCount": 3,
  "s1mAccessCache": {"a": 1},
  "groveConfigCache": {"b": 2},
  "passesEligibilityCache": {},
  "overageCreditGrantCache": {},
  "cachedExtraUsageDisabledReason": "x",
  "orgModelDefaultCache": {},
  "bridgeOauthDeadFailCount": 1,
  "cachedGrowthBookFeatures": {"f": true},
  "hasCompletedOnboarding": true,
  "lastOnboardingVersion": "2.1.0",
  "theme": "dark",
  "projects": {"/p/x": {"hasTrustDialogAccepted": true, "allowedTools": ["Bash(<ls>)"]}},
  "mcpServers": {"s": {"command": "run", "args": ["a&b"]}},
  "numStartups": 42,
  "tipsHistory": {"t": 1}
}`

var seedKept = []string{"hasCompletedOnboarding", "lastOnboardingVersion", "theme", "projects", "mcpServers", "numStartups", "tipsHistory"}

func compactJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestSeedAccountClaudeJSON(t *testing.T) {
	d := t.TempDir()
	src, dst := filepath.Join(d, "home.json"), filepath.Join(d, "acct", ".claude.json")
	writeF(t, src, seedFixture)
	seeded, err := seedAccountClaudeJSON(src, dst)
	if err != nil || !seeded {
		t.Fatalf("seeded=%v err=%v", seeded, err)
	}
	var srcObj, dstObj map[string]json.RawMessage
	json.Unmarshal([]byte(seedFixture), &srcObj)
	b, _ := os.ReadFile(dst)
	if err := json.Unmarshal(b, &dstObj); err != nil {
		t.Fatal(err)
	}
	for k := range srcObj {
		_, has := dstObj[k]
		if slices.Contains(seedKept, k) {
			if !has {
				t.Errorf("kept key %s missing", k)
			} else if compactJSON(t, dstObj[k]) != compactJSON(t, srcObj[k]) {
				t.Errorf("%s = %s, want %s", k, dstObj[k], srcObj[k])
			}
		} else if has {
			t.Errorf("login/identity key %s must be dropped", k)
		}
	}
	if !strings.Contains(string(b), `"a&b"`) || !strings.Contains(string(b), `"Bash(<ls>)"`) {
		t.Errorf("seed escaped HTML characters: %s", b)
	}
	st, _ := os.Stat(dst)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}

	// An existing dst is never touched, whatever it holds.
	for _, existing := range []string{`{"oauthAccount":{"emailAddress":"keep"}}`, ``} {
		writeF(t, dst, existing)
		seeded, err := seedAccountClaudeJSON(src, dst)
		if err != nil || seeded {
			t.Errorf("existing %q: seeded=%v err=%v", existing, seeded, err)
		}
		if got, _ := os.ReadFile(dst); string(got) != existing {
			t.Errorf("existing dst changed to %q", got)
		}
	}

	// No source: nothing to seed, not an error.
	if seeded, err := seedAccountClaudeJSON(filepath.Join(d, "none.json"), filepath.Join(d, "other", ".claude.json")); err != nil || seeded {
		t.Errorf("missing source: seeded=%v err=%v", seeded, err)
	}
	// A broken source is an error and writes nothing.
	writeF(t, src, `{"projects":`)
	bad := filepath.Join(d, "bad", ".claude.json")
	if _, err := seedAccountClaudeJSON(src, bad); err == nil {
		t.Error("broken source must be an error")
	}
	if _, err := os.Lstat(bad); err == nil {
		t.Error("broken source must write nothing")
	}
}

func TestSetupAccountDirLinksKeybindingsAndOutputStyles(t *testing.T) {
	home := acctHome(t, "keybindings.json")
	if err := os.MkdirAll(filepath.Join(home, ".claude", "output-styles"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "acct")
	created, err := setupAccountDir(home, dir)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(created)
	if !slices.Equal(created, []string{"keybindings.json", "output-styles"}) {
		t.Errorf("created = %v", created)
	}
	// Absent in ~/.claude: not linked.
	home2 := acctHome(t)
	if c, _ := setupAccountDir(home2, filepath.Join(home2, "acct")); len(c) != 0 {
		t.Errorf("created %v from an empty ~/.claude", c)
	}
}
