// Package config defines the profiles.yaml schema, loading and validation.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// CurrentVersion is the only supported schema version.
const CurrentVersion = 1

// Config is the top-level profiles.yaml document.
type Config struct {
	Version        int                 `yaml:"version"`
	StrictIdentity *bool               `yaml:"strict_identity,omitempty"`
	Profiles       map[string]*Profile `yaml:"profiles"`
	Rules          []Rule              `yaml:"rules,omitempty"`
	Scan           *Scan               `yaml:"scan,omitempty"`
}

// Profile is one git identity.
type Profile struct {
	Name       string            `yaml:"name"`
	Email      string            `yaml:"email"`
	SigningKey string            `yaml:"signing_key,omitempty"`
	GPGFormat  string            `yaml:"gpg_format,omitempty"`
	GPGSign    *bool             `yaml:"gpgsign,omitempty"`
	SSHKey     string            `yaml:"ssh_key,omitempty"`
	Extra      map[string]string `yaml:"extra,omitempty"`
	Repos      []string          `yaml:"repos,omitempty"`
}

// Rule maps directories and/or remote URL globs to a profile.
// Later rules override earlier ones.
type Rule struct {
	Profile string   `yaml:"profile"`
	Dirs    []string `yaml:"dirs,omitempty"`
	Remotes []string `yaml:"remotes,omitempty"`
}

// Scan configures repository discovery for `check` and `import`.
type Scan struct {
	Ignore []string `yaml:"ignore,omitempty"`
}

// Strict reports whether user.useConfigOnly should be set (default true).
func (c *Config) Strict() bool {
	return c.StrictIdentity == nil || *c.StrictIdentity
}

// ProfileNames returns the profile names sorted alphabetically; this is the
// canonical, byte-stable iteration order everywhere in gitident.
func (c *Config) ProfileNames() []string {
	names := make([]string, 0, len(c.Profiles))
	for n := range c.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ScanIgnore returns the configured extra ignore patterns.
func (c *Config) ScanIgnore() []string {
	if c.Scan == nil {
		return nil
	}
	return c.Scan.Ignore
}

// ErrNotFound is returned by Load when the config file does not exist.
var ErrNotFound = errors.New("config file not found")

// Load reads and parses the config file at path. It does not validate.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s (run `gitident init` or `gitident import`)", ErrNotFound, path)
	}
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse decodes a profiles.yaml document, rejecting unknown fields.
func Parse(data []byte) (*Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("config is empty")
		}
		return nil, err
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]*Profile{}
	}
	for name, p := range cfg.Profiles {
		if p == nil {
			cfg.Profiles[name] = &Profile{}
		}
	}
	return &cfg, nil
}

// Marshal encodes the config as YAML with two-space indentation.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
