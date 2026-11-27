package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// readJSONObject reads a JSON object file into raw members. A missing file
// yields (nil, false, nil).
func readJSONObject(path string) (map[string]json.RawMessage, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	m := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, true, fmt.Errorf("%s: %w", path, err)
		}
	}
	return m, true, nil
}

func readServers(obj map[string]json.RawMessage, path string) (map[string]json.RawMessage, error) {
	servers := map[string]json.RawMessage{}
	if raw, ok := obj["mcpServers"]; ok && string(bytes.TrimSpace(raw)) != "null" {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return nil, fmt.Errorf("%s: mcpServers: %w", path, err)
		}
	}
	return servers, nil
}

// syncMCP merges the top-level mcpServers of srcJSON into dstJSON. Source wins
// on a name clash; names recorded in statePath from a previous sync that the
// source no longer has are removed; names only in dst are kept. Every other
// key of dst is preserved as an opaque JSON value. A missing source is a no-op.
func syncMCP(srcJSON, dstJSON, statePath string) (added, removed []string, err error) {
	src, found, err := readJSONObject(srcJSON)
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, nil, nil
	}
	srcServers, err := readServers(src, srcJSON)
	if err != nil {
		return nil, nil, err
	}
	var prev []string
	if b, rerr := os.ReadFile(statePath); rerr == nil {
		_ = json.Unmarshal(b, &prev) // corrupt state = nothing synced before
	}

	// Read dst as late as possible to minimise the race with a live claude.
	dst, dstFound, err := readJSONObject(dstJSON)
	if err != nil {
		return nil, nil, err
	}
	if dst == nil {
		dst = map[string]json.RawMessage{}
	}
	dstServers, err := readServers(dst, dstJSON)
	if err != nil {
		return nil, nil, err
	}

	changed := !dstFound
	for name, raw := range srcServers {
		old, had := dstServers[name]
		if !had {
			added = append(added, name)
			changed = true
		} else if !jsonEqual(old, raw) {
			changed = true
		}
		dstServers[name] = raw
	}
	for _, name := range prev {
		if _, inSrc := srcServers[name]; inSrc {
			continue
		}
		if _, had := dstServers[name]; had {
			delete(dstServers, name)
			removed = append(removed, name)
			changed = true
		}
	}
	sort.Strings(added)
	sort.Strings(removed)

	if changed {
		sb, err := json.Marshal(dstServers)
		if err != nil {
			return nil, nil, err
		}
		dst["mcpServers"] = sb
		if err := writeJSONObject(dstJSON, dst); err != nil {
			return nil, nil, err
		}
	}

	names := make([]string, 0, len(srcServers))
	for n := range srcServers {
		names = append(names, n)
	}
	sort.Strings(names)
	sb, _ := json.Marshal(names)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return added, removed, err
	}
	if err := atomicWrite(statePath, sb, 0o644); err != nil {
		return added, removed, err
	}
	return added, removed, nil
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return bytes.Equal(xb, yb)
}

func writeJSONObject(path string, obj map[string]json.RawMessage) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(obj); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicWrite(path, buf.Bytes(), mode)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// runAccountSyncMCP implements `claudemux-head account sync-mcp <name>`.
func runAccountSyncMCP(args []string, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: claudemux-head account sync-mcp <name>")
		return 2
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head: %v\n", err)
		return 3
	}
	dir, err := cfg.Claude.ConfigDir(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head: %v\n", err)
		return 1
	}
	if dir == "" {
		return 0 // the ~/.claude account is the source itself
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head: %v\n", err)
		return 1
	}
	state := filepath.Join(home, ".claude", "claudemux", "mcp-sync-"+args[0]+".json")
	added, removed, err := syncMCP(filepath.Join(home, ".claude.json"), filepath.Join(dir, ".claude.json"), state)
	if err != nil {
		fmt.Fprintf(stderr, "claudemux-head: sync-mcp %s: %v\n", args[0], err)
		return 1
	}
	if len(added)+len(removed) > 0 {
		var parts []string
		for _, n := range added {
			parts = append(parts, "+"+n)
		}
		for _, n := range removed {
			parts = append(parts, "-"+n)
		}
		fmt.Fprintf(stderr, "claudemux: synced MCP servers into %s: %s\n", args[0], strings.Join(parts, " "))
	}
	return 0
}
