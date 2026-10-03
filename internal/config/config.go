// Package config loads rocket-chat's configuration file, written in YAML or,
// when the file name ends in .json, JSON.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
	// UI configures the interactive chat.
	UI UI `yaml:"ui"`

	// fromJSON records that the file was JSON, whose error messages must
	// not carry line numbers from the YAML it is converted to.
	fromJSON bool
}

// UI is the ui config section.
type UI struct {
	// Theme is "auto" (default, follows the terminal background), "dark"
	// or "light".
	Theme string `yaml:"theme"`
}

// ThemeName returns "dark", "light", or "" for auto.
func (u UI) ThemeName() (string, error) {
	switch u.Theme {
	case "", "auto":
		return "", nil
	case "dark", "light":
		return u.Theme, nil
	}
	return "", fmt.Errorf("ui.theme must be auto, dark or light, not %q", u.Theme)
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

// fileNames are the config file names looked for in the config directory.
var fileNames = []string{"config.yaml", "config.yml", "config.json"}

// Path returns the config file location: $ROCKET_CHAT_CONFIG if set,
// otherwise whichever of config.yaml, config.yml or config.json exists in
// rocket-chat under the user config directory (config.yaml when none does).
// More than one of them is an error, so it is clear which one is used.
func Path() (string, error) {
	if p := os.Getenv(EnvConfigPath); p != "" {
		return p, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "rocket-chat")
	var found []string
	for _, name := range fileNames {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return filepath.Join(dir, fileNames[0]), nil
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("found more than one config file (%s); keep one", strings.Join(found, ", "))
}

// Load reads the config file at path and applies environment overrides. A
// missing file is not an error; the defaults are used instead. Files whose
// name ends in .json are JSON; all others are YAML.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return Config{}, err
	default:
		if err := decode(path, data, &cfg); err != nil {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
	}
	if b := os.Getenv(EnvBackend); b != "" {
		cfg.DefaultBackend = b
	}
	return cfg, nil
}

// IsJSON reports whether path names a JSON config file.
func IsJSON(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".json")
}

func decode(path string, data []byte, cfg *Config) error {
	if IsJSON(path) {
		converted, err := jsonToYAML(data)
		if err != nil {
			return err
		}
		data, cfg.fromJSON = converted, true
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return cfg.cleanError(err)
	}
	return nil
}

// jsonToYAML checks that data is one JSON object and re-encodes it as YAML,
// so both formats share one decoder and every backend's settings work the
// same way in either. An empty file is an empty config.
func jsonToYAML(data []byte) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, jsonError(data, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("unexpected data after the JSON object at %s", position(data, dec.InputOffset()))
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, errors.New("the JSON config must be an object ({ ... })")
	}
	return yaml.Marshal(v)
}

// jsonError adds the line and column to JSON syntax and type errors.
func jsonError(data []byte, err error) error {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		// Offset counts the bytes read, including the offending one.
		return fmt.Errorf("invalid JSON at %s: %w", position(data, syntax.Offset-1), err)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.New("invalid JSON: unexpected end of file")
	}
	return fmt.Errorf("invalid JSON: %w", err)
}

// position turns a byte offset into "line L, column C".
func position(data []byte, offset int64) string {
	offset = min(max(offset, 0), int64(len(data)))
	before := data[:offset]
	line := bytes.Count(before, []byte("\n")) + 1
	col := int(offset) - bytes.LastIndexByte(before, '\n')
	return fmt.Sprintf("line %d, column %d", line, col)
}

var yamlLine = regexp.MustCompile(`line \d+: `)

// cleanError drops line numbers from decoding errors for JSON files, where
// they would point into the converted YAML rather than the file.
func (c Config) cleanError(err error) error {
	if !c.fromJSON {
		return err
	}
	return errors.New(yamlLine.ReplaceAllString(err.Error(), ""))
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
			return fmt.Errorf("config backends.%s: %w", name, c.cleanError(err))
		}
		return nil
	}
}
