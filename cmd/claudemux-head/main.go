package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

// version is stamped by the release workflow's -ldflags. "dev" for local builds.
var version = "dev"

func main() {
	// Subcommand dispatch must precede flag.Parse(): `config` is a bare first
	// arg, not a flag, and flag.Parse() would stop at it and silently ignore the
	// rest. bin/claudemux depends on this path.
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		os.Exit(0)
	}

	if len(os.Args) > 1 && os.Args[1] == "config" {
		if len(os.Args) > 2 && os.Args[2] == "get" {
			os.Exit(runConfigGet(os.Args[3:], os.Stdout, os.Stderr))
		}
		if len(os.Args) > 2 && os.Args[2] == "set" {
			os.Exit(runConfigSet(os.Args[3:], os.Stdout, os.Stderr))
		}
		fmt.Fprintln(os.Stderr, "usage: claudemux-head config get <dotted.path> | claudemux-head config set <dotted.path> <value>")
		os.Exit(2)
	}

	// `boot` holds a pane and then execs its trailing command, so it must be
	// dispatched before flag.Parse() for the same reason `config` is: the
	// command after `--` is not this binary's to parse.
	if len(os.Args) > 1 && os.Args[1] == "boot" {
		os.Exit(runBoot(os.Args[2:], os.Stderr))
	}

	if len(os.Args) > 1 && os.Args[1] == "hook" {
		if len(os.Args) > 2 && os.Args[2] == "ensure" {
			os.Exit(runHookEnsure(os.Args[3:], os.Stdout, os.Stderr))
		}
		fmt.Fprintln(os.Stderr, "usage: claudemux-head hook ensure [--script <path>]")
		os.Exit(2)
	}

	if len(os.Args) > 1 && os.Args[1] == "switchboard" {
		os.Exit(runSwitchboard(os.Stderr))
	}

	// `onboard` is the layout picker, run by bin/claudemux on a session's
	// first launch and by `claudemux setup` any time after. Dispatched
	// before flag.Parse() alongside every other subcommand here.
	if len(os.Args) > 1 && os.Args[1] == "onboard" {
		os.Exit(runOnboard(os.Stderr))
	}

	// `banner` runs inside the switchboard's display-popup (see banner.go).
	if len(os.Args) > 1 && os.Args[1] == "banner" {
		os.Exit(runBanner(os.Args[2:], os.Stdout, os.Stderr))
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

	encodedPath := encodeProjectPath(cwd)

	sessionID, err := resolveSession(claudeProjectsDir, cwd, *sessionFlag)
	var jsonlPath string
	if err != nil {
		// No transcript yet. On a brand-new project this is a startup race,
		// not an error: claudemux launches this head and the claude pane in
		// the same second, and Claude Code creates its .jsonl only after it
		// finishes booting. Exiting here killed the head pane on every fresh
		// project (dead pane, status 1). Start waiting instead — sessionID ""
		// puts the model in waiting mode and the follow-active rotation
		// adopts the first transcript that appears. Only auto-detection can
		// fail (an explicit --session resolves unconditionally), so waiting
		// mode always has follow enabled and cannot wait forever on a pin.
		jsonlPath = waitingTranscript(filepath.Join(claudeProjectsDir, encodedPath))
		sessionID = ""
	} else {
		jsonlPath = filepath.Join(claudeProjectsDir, encodedPath, sessionID+".jsonl")
	}

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

	final, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// `R` asks for a re-exec. It happens HERE, not in Update, so the terminal
	// is already restored when the replacement starts — see restartSelf.
	// restartSelf only returns if the exec failed, and it has already said
	// why; exiting non-zero then keeps the pane open (remain-on-exit=failed)
	// so the message is readable instead of vanishing with the pane.
	if fm, ok := final.(model); ok && fm.restart {
		restartSelf(os.Stderr)
		os.Exit(1)
	}
}
