package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration to accept a Go duration string ("20s") in YAML.
// yaml.v3 will not do this on its own: time.Duration is an int64, so a bare
// `min_interval: 20s` fails to decode. MarshalYAML is the other half — the
// `config get` subcommand round-trips Config through YAML to resolve dotted
// paths, and without it a Duration would serialize as a raw nanosecond count.
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string like \"20s\": %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalYAML() (any, error) {
	return d.String(), nil
}

// Config is claudemux-head's non-secret configuration. Secrets are deliberately
// NOT here: they live in a separate line-oriented env file (see env.go) that a
// secret manager can serve over a FIFO, which YAML cannot be.
type Config struct {
	Summary     SummaryConfig     `yaml:"summary"`
	OnePassword OnePasswordConfig `yaml:"onepassword"`
	Launch      LaunchConfig      `yaml:"launch"`
	Teardown    TeardownConfig    `yaml:"teardown"`
}

type SummaryConfig struct {
	Enabled bool   `yaml:"enabled"`
	Model   string `yaml:"model"`
	// MinInterval is the floor between summary calls. Each call bills the user's
	// own API key, so this is the only thing bounding what an active session
	// costs. Zero is a legal opt-out — it means no floor, and every busy→idle
	// edge and session rotation may fire a call. Negative is rejected outright:
	// it reads as an attempt to set a floor and silently produces the opposite.
	MinInterval Duration `yaml:"min_interval"`
	// APIKeyFile is where the Anthropic credential is read from. It is a path,
	// not the key itself, and that is deliberate: the file is read as a stream,
	// so it can be a FIFO mounted by a secret manager (1Password Environments)
	// and the key never lands on disk. A YAML scalar could not be served that
	// way, which is why the secret lives outside this file and is only pointed
	// at from it. Format is dotenv (KEY=value per line).
	//
	// Empty means the default: <config dir>/env. loadConfig resolves it to an
	// absolute path, so a caller never has to re-derive the default.
	APIKeyFile string `yaml:"api_key_file"`
	// TabTitle renames the session's tmux window to the Haiku `tab` label, which
	// tmux propagates to the terminal tab. Default true. Independent of Enabled:
	// with Enabled true and this false, the status pane still summarizes but the
	// window/tab is left alone.
	TabTitle bool `yaml:"tab_title"`
}

// OnePasswordConfig is consumed by bin/claudemux via `claudemux-head config get`,
// not by the TUI. It ships empty: Accounts maps a GitHub org to the 1Password
// account that holds its secrets, and any built-in mapping would be one user's
// employer structure baked into everyone's binary.
type OnePasswordConfig struct {
	DefaultAccount string            `yaml:"default_account"`
	Accounts       map[string]string `yaml:"accounts"`
}

// LaunchConfig is consumed by bin/claudemux via `claudemux-head config get`,
// not by the TUI. AutoWorktree makes the launcher pass `--worktree` to claude
// when the launch directory is a repo's main checkout sitting on its default
// branch (the launcher owns that heuristic and its overrides — see
// bin/claudemux worktree_requested). Default false: launching into a surprise
// worktree must be something the user asked for.
type LaunchConfig struct {
	AutoWorktree bool `yaml:"auto_worktree"`
	// Layout is the pane arrangement bin/claudemux builds for a new session,
	// chosen by `claudemux-head onboard` (or hand-set). Empty means the user
	// has never chosen: the launcher treats it as shell-right AND knows to
	// offer onboarding. Values: shell-right, no-shell, shell-bottom,
	// head-bottom.
	Layout string `yaml:"layout"`
	// ProjectDirs are the roots bin/claudemux searches when a launch query is
	// neither a real directory nor a zoxide hit — the last resort before the
	// launcher gives up. Ships empty, which keeps resolution exactly as it was
	// before this existed: a query zoxide misses still fails.
	//
	// Searched in the order written, and that order is meaningful — it breaks
	// ties between equally good matches (see findProject). loadConfig expands
	// a leading `~`, so a caller never has to.
	ProjectDirs []string `yaml:"project_dirs"`
}

// legalLayouts are the pane arrangements bin/claudemux knows how to build.
// Kept as a slice (not a map) because validate() also uses it to render the
// list of legal values in its error message.
var legalLayouts = []string{"shell-right", "no-shell", "shell-bottom", "head-bottom"}

// TeardownConfig configures the `x` key in the status pane, which wraps a
// session up and kills its tmux session.
//
// Command is typed into the claude pane on the first press. It defaults to
// "/done" because that is the wrap-up slash command this tool was built
// around, but it is a *command name in someone else's TUI* — a user without
// that skill points it at their own, and "" opts out of the step entirely,
// making the key a gated exit-and-kill. Empty is therefore a real value, not
// "unset": loadConfig decodes into defaults, so an explicit `command: ""`
// overrides the default rather than being ignored.
type TeardownConfig struct {
	Command string `yaml:"command"`
}

func defaultConfig() Config {
	return Config{
		Summary: SummaryConfig{
			Enabled:     true,
			Model:       "claude-haiku-4-5",
			MinInterval: Duration{20 * time.Second},
			TabTitle:    true,
		},
		Teardown: TeardownConfig{
			Command: "/done",
		},
	}
}

// configDir resolves the package's config directory per the XDG basedir spec:
// $XDG_CONFIG_HOME replaces the default when set, rather than being searched
// before it.
//
// Named for the package (claudemux), not for this binary (claudemux-head). The two
// ship together and share one configuration: claudemux reads the same file via
// `claudemux-head config get`, so a second directory would only be a second place to
// look.
//
// Hand-rolled on purpose. os.UserConfigDir() returns ~/Library/Application
// Support on macOS, which is not where a terminal tool's dotfile-style config
// belongs and not where this project's users keep it.
func configDir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "claudemux"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "claudemux"), nil
}

// loadConfig reads config.yml, returning defaults for the keys it does not name.
//
// A missing file is not an error — that is a fresh install, and the tool must
// run. A file that exists but does not parse IS an error: continuing on defaults
// would silently discard exactly what the user sat down to configure.
func loadConfig() (Config, error) {
	cfg := defaultConfig()

	dir, err := configDir()
	if err != nil {
		return defaultConfig(), fmt.Errorf("resolving config dir: %w", err)
	}
	path := filepath.Join(dir, "config.yml")

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			resolveAPIKeyFile(&cfg, dir)
			resolveProjectDirs(&cfg)
			return cfg, nil
		}
		return defaultConfig(), fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	// Decoding INTO cfg (already holding defaults) is what gives partial files
	// their semantics: yaml.v3 assigns only the fields the document names, so an
	// absent `enabled` keeps its default rather than becoming false.
	dec := yaml.NewDecoder(f)
	// Reject unknown fields so a typo ("sumary:") fails loudly instead of being
	// silently ignored, which would look exactly like the setting not working.
	dec.KnownFields(true)

	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return defaultConfig(), nil // empty file: defaults
		}
		return defaultConfig(), fmt.Errorf("parsing %s: %w", path, err)
	}

	if err := cfg.validate(); err != nil {
		return defaultConfig(), fmt.Errorf("%s: %w", path, err)
	}
	resolveAPIKeyFile(&cfg, dir)
	resolveProjectDirs(&cfg)
	return cfg, nil
}

// resolveAPIKeyFile turns summary.api_key_file into an absolute path: the
// default (<config dir>/env) when unset, and a `~`-expanded path otherwise.
//
// Resolved here, once, rather than at each read: `claudemux-head config get
// summary.api_key_file` should print the file actually consulted, not a blank
// that the reader has to know how to expand.
func resolveAPIKeyFile(cfg *Config, dir string) {
	p := strings.TrimSpace(cfg.Summary.APIKeyFile)
	if p == "" {
		cfg.Summary.APIKeyFile = filepath.Join(dir, "env")
		return
	}
	cfg.Summary.APIKeyFile = expandTilde(p)
}

// expandTilde turns a leading `~` into the user's home directory, leaving every
// other path (absolute, relative, or `~user`, which this deliberately does not
// understand) alone. A home directory that cannot be resolved leaves the path
// as written rather than mangling it.
func expandTilde(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}

// resolveProjectDirs expands and tidies launch.project_dirs in place, so
// findProject only ever walks absolute paths — and, more to the point, only
// ever ANSWERS with one. The launcher hands what it resolves to `tmux
// new-session -c`, and the tmux *server* resolves a relative path against its
// own working directory, not the launcher's: a relative root would open the
// session's panes somewhere else entirely.
//
// Blank entries are dropped: a stray `- ""` would otherwise absolutize to the
// process's working directory and quietly offer its children as projects.
func resolveProjectDirs(cfg *Config) {
	dirs := cfg.Launch.ProjectDirs[:0]
	for _, d := range cfg.Launch.ProjectDirs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		d = expandTilde(d)
		// A path that cannot be absolutized (an unreadable working directory)
		// is kept as written rather than dropped: it may still not match
		// anything, but silently discarding a configured root would be worse.
		if abs, err := filepath.Abs(d); err == nil {
			d = abs
		}
		dirs = append(dirs, d)
	}
	cfg.Launch.ProjectDirs = dirs
}

// validate rejects configurations that parse but cannot mean what they say.
func (c Config) validate() error {
	// A negative floor is always a mistake, and a costly one: canSummarize
	// compares elapsed >= MinInterval, so any negative value is unconditionally
	// true and removes the rate limit entirely — the opposite of what someone
	// typing a floor intends. Zero is left alone: it is the honest way to say
	// "no floor", and refusing it would deny a legitimate opt-out.
	if c.Summary.MinInterval.Duration < 0 {
		return fmt.Errorf("summary.min_interval is %s: a negative floor removes the rate limit on billable API calls instead of setting one; use 0 to disable it deliberately",
			c.Summary.MinInterval.Duration)
	}
	// Empty is legal (the user has never chosen a layout); anything else must
	// be a name bin/claudemux actually knows how to build, or the launcher
	// would fall back to shell-right without any indication why.
	if c.Launch.Layout != "" && !slices.Contains(legalLayouts, c.Launch.Layout) {
		return fmt.Errorf("launch.layout is %q: must be one of %s",
			c.Launch.Layout, strings.Join(legalLayouts, ", "))
	}
	return nil
}
