package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeF(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readObj(t *testing.T, p string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func syncEnv(t *testing.T) (src, dst, state string) {
	d := t.TempDir()
	return filepath.Join(d, "src.json"), filepath.Join(d, "acct", ".claude.json"), filepath.Join(d, "st", "mcp-sync-x.json")
}

func TestSyncMCPAddsAndSourceWins(t *testing.T) {
	src, dst, state := syncEnv(t)
	writeF(t, src, `{"mcpServers":{"a":{"command":"new"},"b":{"command":"b"}}}`)
	writeF(t, dst, `{"mcpServers":{"a":{"command":"old"}}}`)
	added, removed, err := syncMCP(src, dst, state)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(added, []string{"b"}) || len(removed) != 0 {
		t.Fatalf("added=%v removed=%v", added, removed)
	}
	s := readObj(t, dst)["mcpServers"].(map[string]any)
	if s["a"].(map[string]any)["command"] != "new" || s["b"] == nil {
		t.Fatalf("servers=%v", s)
	}
}

func TestSyncMCPRemovesOnlySynced(t *testing.T) {
	src, dst, state := syncEnv(t)
	writeF(t, src, `{"mcpServers":{"a":{"command":"a"}}}`)
	writeF(t, dst, `{"mcpServers":{"local":{"command":"l"}}}`)
	if _, _, err := syncMCP(src, dst, state); err != nil {
		t.Fatal(err)
	}
	writeF(t, src, `{"mcpServers":{}}`)
	_, removed, err := syncMCP(src, dst, state)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(removed, []string{"a"}) {
		t.Fatalf("removed=%v", removed)
	}
	s := readObj(t, dst)["mcpServers"].(map[string]any)
	if s["a"] != nil || s["local"] == nil {
		t.Fatalf("servers=%v", s)
	}
}

func TestSyncMCPPreservesOtherKeys(t *testing.T) {
	src, dst, state := syncEnv(t)
	writeF(t, src, `{"mcpServers":{"a":{"command":"a"}}}`)
	orig := `{"oauthAccount":{"emailAddress":"x@y.z","n":1.50},"projects":{"/p":{"mcpServers":{"q":{}},"allowedTools":[]}},"numStartups":7}`
	writeF(t, dst, orig)
	if _, _, err := syncMCP(src, dst, state); err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	_ = json.Unmarshal([]byte(orig), &want)
	got := readObj(t, dst)
	for _, k := range []string{"oauthAccount", "projects", "numStartups"} {
		if !reflect.DeepEqual(got[k], want[k]) {
			t.Fatalf("%s changed: %v vs %v", k, got[k], want[k])
		}
	}
	if st, _ := os.Stat(dst); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}

func TestSyncMCPMissingDst(t *testing.T) {
	src, dst, state := syncEnv(t)
	writeF(t, src, `{"mcpServers":{"a":{"command":"a"}}}`)
	added, _, err := syncMCP(src, dst, state)
	if err != nil || !reflect.DeepEqual(added, []string{"a"}) {
		t.Fatalf("added=%v err=%v", added, err)
	}
	if readObj(t, dst)["mcpServers"].(map[string]any)["a"] == nil {
		t.Fatal("a missing")
	}
	// missing source: no-op
	src2, dst2, state2 := syncEnv(t)
	if a, r, err := syncMCP(src2, dst2, state2); err != nil || len(a)+len(r) != 0 {
		t.Fatalf("%v %v %v", a, r, err)
	}
	if _, err := os.Stat(dst2); err == nil {
		t.Fatal("dst created for missing source")
	}
}

func TestSyncMCPLeavesProjectServers(t *testing.T) {
	src, dst, state := syncEnv(t)
	writeF(t, src, `{"mcpServers":{"a":{"command":"a"}},"projects":{"/p":{"mcpServers":{"srcproj":{}}}}}`)
	writeF(t, dst, `{"projects":{"/p":{"mcpServers":{"dstproj":{}}}}}`)
	if _, _, err := syncMCP(src, dst, state); err != nil {
		t.Fatal(err)
	}
	p := readObj(t, dst)["projects"].(map[string]any)["/p"].(map[string]any)["mcpServers"].(map[string]any)
	if p["dstproj"] == nil || p["srcproj"] != nil || len(p) != 1 {
		t.Fatalf("projects=%v", p)
	}
}
