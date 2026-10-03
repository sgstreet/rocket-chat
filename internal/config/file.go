package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// DefaultFile is written when no config file exists. It holds the default
// settings, plus empty API keys to show where they go.
const DefaultFile = `{
  "default_backend": "ollama",
  "default_role": "",
  "backends": {
    "ollama": {
      "api_key": "",
      "search": {
        "enabled": false
      },
      "serve": {
        "auto_start": true
      }
    },
    "gemini": {
      "api_key": "",
      "search": {
        "enabled": true
      }
    }
  },
  "sessions": {
    "save": true
  },
  "ui": {
    "theme": "auto",
    "mouse": true,
    "history": true,
    "markdown": true
  },
  "roles": {}
}
`

// Create writes DefaultFile to path, creating its directory. It fails if
// the file already exists. The file is readable only by its owner because
// it may come to hold API keys.
func Create(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(DefaultFile)
	return errors.Join(err, f.Close())
}

// APIKey returns the api_key setting from the named backend's section.
func (c Config) APIKey(backend string) string {
	var s struct {
		APIKey string `json:"api_key"`
	}
	if raw, ok := c.Backends[backend]; ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s.APIKey
}

// SetAPIKey saves key as backends.<backend>.api_key in the config file at
// path, or removes the setting when key is empty, and updates c to match.
// Other settings and their order are kept; the file is re-indented and
// made readable only by its owner.
func (c *Config) SetAPIKey(path, backend, key string) error {
	// Write through a symlink rather than replacing it.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	root, err := parseObject(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	backends, err := parseObject(root.get("backends"))
	if err != nil {
		return fmt.Errorf("%s: backends: %w", path, err)
	}
	section, err := parseObject(backends.get(backend))
	if err != nil {
		return fmt.Errorf("%s: backends.%s: %w", path, backend, err)
	}
	if key == "" {
		section.remove("api_key")
	} else {
		v, _ := json.Marshal(key)
		section.set("api_key", v)
	}
	backends.set(backend, section.bytes())
	root.set("backends", backends.bytes())

	var out bytes.Buffer
	if err := json.Indent(&out, root.bytes(), "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	if err := replaceFile(path, out.Bytes()); err != nil {
		return err
	}
	if c.Backends == nil {
		c.Backends = map[string]json.RawMessage{}
	}
	c.Backends[backend] = section.bytes()
	return nil
}

// replaceFile replaces path with data atomically, mode 0600.
func replaceFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// object is a JSON object that keeps its members in order.
type object struct {
	members []member
}

type member struct {
	key   string
	value json.RawMessage
}

// parseObject reads a JSON object; empty input is an empty object.
func parseObject(data []byte) (*object, error) {
	o := &object{}
	if t := bytes.TrimSpace(data); len(t) == 0 || string(t) == "null" {
		return o, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		o.set(t.(string), v)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the JSON object")
	}
	return o, nil
}

func (o *object) get(key string) json.RawMessage {
	for _, m := range o.members {
		if m.key == key {
			return m.value
		}
	}
	return nil
}

// set replaces the value of key, or appends it.
func (o *object) set(key string, value json.RawMessage) {
	for i, m := range o.members {
		if m.key == key {
			o.members[i].value = value
			return
		}
	}
	o.members = append(o.members, member{key, value})
}

func (o *object) remove(key string) {
	for i, m := range o.members {
		if m.key == key {
			o.members = append(o.members[:i], o.members[i+1:]...)
			return
		}
	}
}

// bytes encodes the object compactly.
func (o *object) bytes() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o.members {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(m.key)
		b.Write(k)
		b.WriteByte(':')
		_ = json.Compact(&b, m.value)
	}
	b.WriteByte('}')
	return b.Bytes()
}
