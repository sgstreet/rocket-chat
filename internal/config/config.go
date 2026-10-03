// Package config loads rocket-chat's YAML configuration file.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// Environment variables that override the config file.
const (
	EnvConfigPath = "ROCKET_CHAT_CONFIG"
	EnvBackend    = "ROCKET_CHAT_BACKEND"
)

// Config is the top-level configuration.
type Config struct {
	// DefaultBackend is the backend used when none is given on the command
	// line.
	DefaultBackend string `yaml:"default_backend"`
	// Backends holds each backend's own section, decoded by the backend.
	Backends map[string]yaml.Node `yaml:"backends"`
	// Sessions controls saving interactive chats.
	Sessions Sessions `yaml:"sessions"`
}

// Sessions is the sessions config section.
type Sessions struct {
	// Save turns saving interactive chats on or off (default on).
	Save *bool `yaml:"save"`
	// Dir is where sessions are saved (default
	// $XDG_DATA_HOME/rocket-chat/sessions).
	Dir string `yaml:"dir"`
}

// SaveEnabled reports whether interactive chats are saved.
func (s Sessions) SaveEnabled() bool { return s.Save == nil || *s.Save }

// Default returns the configuration used when no file exists.
func Default() Config {
	return Config{DefaultBackend: "ollama"}
}

// Path returns the config file location: $ROCKET_CHAT_CONFIG if set,
// otherwise rocket-chat/config.yaml under the user config directory.
func Path() (string, error) {
	if p := os.Getenv(EnvConfigPath); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "rocket-chat", "config.yaml"), nil
}

// Load reads the config file at path and applies environment overrides. A
// missing file is not an error; the defaults are used instead.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return Config{}, err
	default:
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
	}
	if b := os.Getenv(EnvBackend); b != "" {
		cfg.DefaultBackend = b
	}
	return cfg, nil
}

// Decoder returns a function that decodes the named backend's section into
// a settings struct, as expected by backend.Factory. It leaves the struct
// untouched when the section is absent.
func (c Config) Decoder(name string) func(v any) error {
	return func(v any) error {
		node, ok := c.Backends[name]
		if !ok {
			return nil
		}
		if err := node.Decode(v); err != nil {
			return fmt.Errorf("config backends.%s: %w", name, err)
		}
		return nil
	}
}
