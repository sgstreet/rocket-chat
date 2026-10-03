package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// backendSettings has the kinds of fields backend settings use.
type backendSettings struct {
	Host        string         `yaml:"host"`
	Temperature *float64       `yaml:"temperature"`
	NumCtx      int            `yaml:"num_ctx"`
	KeepAlive   *time.Duration `yaml:"keep_alive"`
	Search      struct {
		Enabled    *bool         `yaml:"enabled"`
		MaxResults int           `yaml:"max_results"`
		Since      time.Duration `yaml:"since"`
	} `yaml:"search"`
}

const fullJSON = `{
	"default_backend": "gemini",
	"ui": {"theme": "light"},
	"sessions": {"save": false, "dir": "~/chats"},
	"backends": {
		"ollama": {
			"host": "http://127.0.0.1:11434",
			"temperature": 0.7,
			"num_ctx": 32768,
			"keep_alive": "10m",
			"search": {"enabled": true, "max_results": 3, "since": "168h"}
		}
	}
}`

func TestLoadJSON(t *testing.T) {
	t.Setenv(EnvBackend, "")
	cfg, err := Load(writeFile(t, "config.json", fullJSON))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultBackend != "gemini" || cfg.UI.Theme != "light" || cfg.Sessions.SaveEnabled() || cfg.Sessions.Dir != "~/chats" {
		t.Errorf("cfg = %+v", cfg)
	}
	var s backendSettings
	if err := cfg.Decoder("ollama")(&s); err != nil {
		t.Fatal(err)
	}
	if s.Host != "http://127.0.0.1:11434" || s.Temperature == nil || *s.Temperature != 0.7 || s.NumCtx != 32768 ||
		s.KeepAlive == nil || *s.KeepAlive != 10*time.Minute ||
		s.Search.Enabled == nil || !*s.Search.Enabled || s.Search.MaxResults != 3 || s.Search.Since != 168*time.Hour {
		t.Errorf("settings = %+v", s)
	}
}

func TestJSONMatchesYAML(t *testing.T) {
	t.Setenv(EnvBackend, "")
	yamlCfg, err := Load(writeFile(t, "config.yaml", `
default_backend: gemini
ui: {theme: light}
sessions: {save: false, dir: ~/chats}
backends:
  ollama:
    host: http://127.0.0.1:11434
    temperature: 0.7
    num_ctx: 32768
    keep_alive: 10m
    search: {enabled: true, max_results: 3, since: 168h}
`))
	if err != nil {
		t.Fatal(err)
	}
	jsonCfg, err := Load(writeFile(t, "config.json", fullJSON))
	if err != nil {
		t.Fatal(err)
	}
	var fromYAML, fromJSON backendSettings
	_ = yamlCfg.Decoder("ollama")(&fromYAML)
	_ = jsonCfg.Decoder("ollama")(&fromJSON)
	if *fromYAML.KeepAlive != *fromJSON.KeepAlive || *fromYAML.Temperature != *fromJSON.Temperature ||
		fromYAML.Search.Since != fromJSON.Search.Since || fromYAML.NumCtx != fromJSON.NumCtx {
		t.Errorf("yaml %+v != json %+v", fromYAML, fromJSON)
	}
}

func TestJSONErrors(t *testing.T) {
	tests := []struct {
		name, body string
		want       []string
	}{
		{"trailing comma", "{\n  \"default_backend\": \"ollama\",\n}", []string{"invalid JSON at line 3, column 1"}},
		{"unclosed", "{\n  \"ui\": {\"theme\": \"dark\"}\n", []string{"unexpected end of file"}},
		{"unknown field", `{"default_backends": "ollama"}`, []string{"field default_backends not found"}},
		{"wrong type", `{"default_backend": 3, "ui": {"theme": ["dark"]}}`, []string{"cannot unmarshal"}},
		{"not an object", `["ollama"]`, []string{"must be an object"}},
		{"two objects", "{}\n{}", []string{"unexpected data after the JSON object at line 2"}},
		{"yaml in a json file", "default_backend: ollama\n", []string{"invalid JSON at line 1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeFile(t, "config.json", tt.body))
			if err == nil {
				t.Fatal("no error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
			if strings.Contains(err.Error(), "yaml:") && strings.Contains(err.Error(), "line ") && !strings.Contains(err.Error(), "invalid JSON") {
				t.Errorf("error %q points at YAML lines", err)
			}
		})
	}
}

func TestJSONBackendTypeError(t *testing.T) {
	cfg, err := Load(writeFile(t, "config.json", `{"backends": {"ollama": {"num_ctx": "big"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var s backendSettings
	err = cfg.Decoder("ollama")(&s)
	if err == nil || !strings.Contains(err.Error(), "config backends.ollama") || strings.Contains(err.Error(), "line ") {
		t.Errorf("err = %v", err)
	}
}

func TestEmptyJSONFile(t *testing.T) {
	t.Setenv(EnvBackend, "")
	cfg, err := Load(writeFile(t, "config.json", "  \n"))
	if err != nil || cfg.DefaultBackend != Default().DefaultBackend {
		t.Errorf("cfg %+v, err %v", cfg, err)
	}
}

func TestPathFindsConfigFile(t *testing.T) {
	t.Setenv(EnvConfigPath, "")
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	dir := filepath.Join(base, "rocket-chat")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if p, err := Path(); err != nil || p != filepath.Join(dir, "config.yaml") {
		t.Errorf("no file: %q, %v", p, err)
	}
	jsonPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(jsonPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, err := Path(); err != nil || p != jsonPath {
		t.Errorf("json only: %q, %v", p, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Path(); err == nil || !strings.Contains(err.Error(), "more than one config file") {
		t.Errorf("both files: %v", err)
	}
}
