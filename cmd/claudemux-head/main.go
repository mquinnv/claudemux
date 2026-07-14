package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	// Subcommand dispatch must precede flag.Parse(): `config` is a bare first
	// arg, not a flag, and flag.Parse() would stop at it and silently ignore the
	// rest. bin/claudemux depends on this path.
	if len(os.Args) > 1 && os.Args[1] == "config" {
		if len(os.Args) > 2 && os.Args[2] == "get" {
			os.Exit(runConfigGet(os.Args[3:], os.Stdout, os.Stderr))
		}
		fmt.Fprintln(os.Stderr, "usage: claudemux-head config get <dotted.path>")
		os.Exit(2)
	}

	sessionFlag := flag.String("session", "", "Use a specific session ID instead of auto-detecting")
	flag.Parse()

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting working directory: %v\n", err)
		os.Exit(1)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting home directory: %v\n", err)
		os.Exit(1)
	}

	claudeProjectsDir := filepath.Join(homeDir, ".claude", "projects")

	sessionID, err := resolveSession(claudeProjectsDir, cwd, *sessionFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding session: %v\n", err)
		fmt.Fprintf(os.Stderr, "Make sure you're in a directory with an active Claude Code session.\n")
		os.Exit(1)
	}

	encodedPath := encodeProjectPath(cwd)
	jsonlPath := filepath.Join(claudeProjectsDir, encodedPath, sessionID+".jsonl")

	// Follow the most-recently-active session unless the user pinned one with
	// --session. Without this, a long-lived monitor stays frozen on whatever
	// file was newest at launch and goes stale when the session rotates.
	followActive := *sessionFlag == ""

	cfg, err := loadConfig()
	if err != nil {
		// A config file that exists but does not parse is fatal: running on
		// defaults would silently discard what the user configured.
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	m := newModel(cfg, jsonlPath, sessionID, followActive)
	p := tea.NewProgram(m, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
