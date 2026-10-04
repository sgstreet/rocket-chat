// Package config loads rocket-chat's JSON configuration file.
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
	"strings"
	"time"

	"github.com/sgstreet/rocket-chat/internal/roles"
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
	DefaultBackend string `json:"default_backend"`
	// Backends holds each backend's own section, decoded by the backend.
	Backends map[string]json.RawMessage `json:"backends"`
	// Sessions controls saving interactive chats.
	Sessions Sessions `json:"sessions"`
	// UI configures the interactive chat.
	UI UI `json:"ui"`
	// Roles adds named system prompts to the built-in ones, or replaces
	// built-ins with the same ID.
	Roles map[string]roles.Config `json:"roles"`
	// DefaultRole is the role used when neither --role nor -s is given.
	DefaultRole string `json:"default_role"`
	// Compact configures summarizing long chats.
	Compact Compact `json:"compact"`
}

// Compact is the compact config section.
type Compact struct {
	// Auto compacts a chat before sending once it takes this share of the
	// model's context window, from 0.5 to 0.95; 0 (the default) turns it
	// off.
	Auto float64 `json:"auto"`
	// Keep is how many of the latest messages compaction keeps as they are
	// (default 4).
	Keep int `json:"keep"`
}

// Validate checks the compact settings.
func (c Compact) Validate() error {
	if c.Auto != 0 && (c.Auto < 0.5 || c.Auto > 0.95) {
		return fmt.Errorf("compact.auto must be from 0.5 to 0.95, or 0 to turn it off, not %g", c.Auto)
	}
	if c.Keep < 0 || c.Keep > 100 {
		return fmt.Errorf("compact.keep must be from 1 to 100, not %d", c.Keep)
	}
	return nil
}

// Sessions is the sessions config section.
type Sessions struct {
	// Save turns saving interactive chats on or off (default on).
	Save *bool `json:"save"`
	// Dir is where sessions are saved (default
	// $XDG_DATA_HOME/rocket-chat/sessions).
	Dir string `json:"dir"`
}

// SaveEnabled reports whether interactive chats are saved.
func (s Sessions) SaveEnabled() bool { return s.Save == nil || *s.Save }

// UI is the ui config section.
type UI struct {
	// Theme is "auto" (default, follows the terminal background), "dark"
	// or "light".
	Theme string `json:"theme"`
	// Mouse turns on mouse wheel scrolling (default on).
	Mouse *bool `json:"mouse"`
	// History saves typed inputs between runs for Up/Down (default on).
	History *bool `json:"history"`
	// Markdown renders finished replies as Markdown (default on); off
	// shows the model's text as it is.
	Markdown *bool `json:"markdown"`
	// InputLines is how many lines the input box shows, 1 to 20 (default
	// 3).
	InputLines int `json:"input_lines"`
}

// InputLinesValue checks ui.input_lines, returning 0 when it is unset.
func (u UI) InputLinesValue() (int, error) {
	if u.InputLines < 0 || u.InputLines > 20 {
		return 0, fmt.Errorf("ui.input_lines must be from 1 to 20, not %d", u.InputLines)
	}
	return u.InputLines, nil
}

// MarkdownEnabled reports whether replies are rendered as Markdown.
func (u UI) MarkdownEnabled() bool { return u.Markdown == nil || *u.Markdown }

// MouseEnabled reports whether the mouse is captured.
func (u UI) MouseEnabled() bool { return u.Mouse == nil || *u.Mouse }

// HistoryEnabled reports whether typed inputs are saved.
func (u UI) HistoryEnabled() bool { return u.History == nil || *u.History }

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

// Duration is a time.Duration written in the config as a string with a
// unit, such as "10m" or "168h".
type Duration time.Duration

// UnmarshalJSON accepts a duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string with a unit such as \"10m\", not %s", b)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: use a number with a unit such as \"30s\", \"10m\" or \"168h\"", s)
	}
	*d = Duration(v)
	return nil
}

// MarshalJSON writes the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Default returns the configuration used when no file exists.
func Default() Config {
	return Config{DefaultBackend: "ollama"}
}

// FileName is the config file's name in the config directory.
const FileName = "config.json"

// Path returns the config file location: $ROCKET_CHAT_CONFIG if set,
// otherwise rocket-chat/config.json under the user config directory. A YAML
// config left there from an older version is an error rather than being
// silently ignored.
func Path() (string, error) {
	if p := os.Getenv(EnvConfigPath); p != "" {
		return p, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "rocket-chat")
	path := filepath.Join(dir, FileName)
	if !fileExists(path) {
		for _, old := range []string{"config.yaml", "config.yml"} {
			if p := filepath.Join(dir, old); fileExists(p) {
				return "", fmt.Errorf("%s: YAML config files are no longer supported; convert it to %s", p, path)
			}
		}
	}
	return path, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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
		if err := decode(data, &cfg, true); err != nil {
			if ext := strings.ToLower(filepath.Ext(path)); ext == ".yaml" || ext == ".yml" {
				err = fmt.Errorf("YAML config files are no longer supported; write the config as JSON (%w)", err)
			}
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
	}
	if b := os.Getenv(EnvBackend); b != "" {
		cfg.DefaultBackend = b
	}
	return cfg, nil
}

// decode reads one JSON object into v, rejecting unknown fields. Empty
// input leaves v unchanged. withPos adds line and column numbers to errors;
// they are only meaningful when data is the whole file.
func decode(data []byte, v any, withPos bool) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] != '{' {
		return errors.New("the config must be a JSON object ({ ... })")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return jsonError(data, err, withPos)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("unexpected data after the JSON object at %s", position(data, dec.InputOffset()))
	}
	return nil
}

// jsonError rewords JSON errors, adding the line and column when withPos
// is set.
func jsonError(data []byte, err error, withPos bool) error {
	var syntax *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		// Offset counts the bytes read, including the offending one.
		return fmt.Errorf("invalid JSON at %s: %w", position(data, syntax.Offset-1), err)
	case errors.As(err, &typeErr):
		msg := fmt.Sprintf("%s must be %s, not %s", typeErr.Field, typeErr.Type, typeErr.Value)
		if withPos {
			msg = fmt.Sprintf("at %s: %s", position(data, typeErr.Offset), msg)
		}
		return errors.New(msg)
	case errors.Is(err, io.ErrUnexpectedEOF):
		return errors.New("invalid JSON: unexpected end of file")
	}
	// Unknown-field errors carry no type; drop the package prefix.
	if msg, ok := strings.CutPrefix(err.Error(), "json: "); ok {
		return errors.New(msg)
	}
	return err
}

// position turns a byte offset into "line L, column C".
func position(data []byte, offset int64) string {
	offset = min(max(offset, 0), int64(len(data)))
	before := data[:offset]
	line := bytes.Count(before, []byte("\n")) + 1
	col := int(offset) - bytes.LastIndexByte(before, '\n')
	return fmt.Sprintf("line %d, column %d", line, col)
}

// Decoder returns a function that decodes the named backend's section into
// a settings struct, as expected by backend.Factory. Unknown fields are
// errors. It leaves the struct untouched when the section is absent.
func (c Config) Decoder(name string) func(v any) error {
	return func(v any) error {
		raw, ok := c.Backends[name]
		if !ok {
			return nil
		}
		if err := decode(raw, v, false); err != nil {
			return fmt.Errorf("config backends.%s: %w", name, err)
		}
		return nil
	}
}
