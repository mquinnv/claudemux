package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
}

// OnePasswordConfig is consumed by bin/claudemux via `claudemux-head config get`,
// not by the TUI. It ships empty: Accounts maps a GitHub org to the 1Password
// account that holds its secrets, and any built-in mapping would be one user's
// employer structure baked into everyone's binary.
type OnePasswordConfig struct {
	DefaultAccount string            `yaml:"default_account"`
	Accounts       map[string]string `yaml:"accounts"`
}

func defaultConfig() Config {
	return Config{
		Summary: SummaryConfig{
			Enabled:     true,
			Model:       "claude-haiku-4-5",
			MinInterval: Duration{20 * time.Second},
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
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	cfg.Summary.APIKeyFile = p
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
	return nil
}
