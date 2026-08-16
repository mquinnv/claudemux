package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// runConfigSet implements `claudemux-head config set <dotted.path> <value>`
// and returns the process exit code.
//
// Unlike config get, there is no "absent key" case here — writing a path
// that doesn't exist yet is the whole point. Exit codes:
//
//	0 — written
//	2 — usage error (wrong argument count)
//	3 — config.yml exists but does not parse, the resulting config fails
//	    validate(), or the write itself fails (message on stderr)
func runConfigSet(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: claudemux-head config set <dotted.path> <value>")
		return 2
	}

	if err := configSet(args[0], args[1]); err != nil {
		fmt.Fprintf(stderr, "claudemux-head: %v\n", err)
		return 3
	}
	return 0
}

// configSet writes a single dotted-path key into config.yml, preserving every
// comment and unrelated key already there. This is the write path both
// `config set` and the layout-picker TUI use — the TUI calls it directly
// rather than shelling out to argv, so this is the one place the
// atomic-write and validate-before-write guarantees have to live.
//
// It parses the file as a yaml.Node tree rather than decoding into Config and
// re-marshalling: a Config round-trip would drop every comment and reorder
// keys to the struct's field order, which is exactly what this must not do.
func configSet(dotted, value string) error {
	dir, err := configDir()
	if err != nil {
		return fmt.Errorf("resolving config dir: %w", err)
	}
	path := filepath.Join(dir, "config.yml")

	root, err := readConfigNode(path)
	if err != nil {
		return err
	}
	if err := setDottedPath(root.Content[0], strings.Split(dotted, "."), value); err != nil {
		return err
	}

	out, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshalling config: %w", err)
	}

	// Never write a config.yml the next launch can't load: decode it back
	// through the same strict path loadConfig uses (KnownFields + validate)
	// before anything touches disk.
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(out))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	return writeFileAtomic(dir, path, out)
}

// readConfigNode parses config.yml into a yaml.Node document whose top-level
// node is a mapping ready to mutate. A missing or empty file starts a fresh
// mapping rather than erroring — config set is allowed to create config.yml.
func readConfigNode(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return emptyConfigNode(), nil
		}
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return emptyConfigNode(), nil
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if len(root.Content) == 0 {
		return emptyConfigNode(), nil
	}
	if root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: top level is not a mapping", path)
	}
	return &root, nil
}

func emptyConfigNode() *yaml.Node {
	return &yaml.Node{
		Kind:    yaml.DocumentNode,
		Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}},
	}
}

// setDottedPath walks mapNode by parts, creating intermediate mappings that
// don't exist yet, and sets the final part as a string scalar. Only string
// scalar leaves are supported — every value config set can target
// (launch.layout today) is a string, and the picker TUI has no use for
// anything richer.
func setDottedPath(mapNode *yaml.Node, parts []string, value string) error {
	for _, key := range parts[:len(parts)-1] {
		child := findMapValue(mapNode, key)
		if child == nil {
			child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			mapNode.Content = append(mapNode.Content, scalarKey(key), child)
		} else if child.Kind != yaml.MappingNode {
			return fmt.Errorf("%s is not a mapping in config.yml", key)
		}
		mapNode = child
	}

	leaf := parts[len(parts)-1]
	if v := findMapValue(mapNode, leaf); v != nil {
		v.SetString(value)
		return nil
	}
	var v yaml.Node
	v.SetString(value)
	mapNode.Content = append(mapNode.Content, scalarKey(leaf), &v)
	return nil
}

// findMapValue returns the value node for key in a mapping node's Content
// (interleaved key, value, key, value, ...), or nil if key is absent.
func findMapValue(mapNode *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapNode.Content); i += 2 {
		if mapNode.Content[i].Value == key {
			return mapNode.Content[i+1]
		}
	}
	return nil
}

func scalarKey(key string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
}

// writeFileAtomic writes data to a temp file in dir and renames it onto path,
// so a crash or a concurrent reader never observes a partially written
// config.yml.
func writeFileAtomic(dir, path string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".config-*.yml.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming into %s: %w", path, err)
	}
	return nil
}
