package config

import (
	"encoding/json"
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
	Host        string    `json:"host"`
	Temperature *float64  `json:"temperature"`
	NumCtx      int       `json:"num_ctx"`
	KeepAlive   *Duration `json:"keep_alive"`
	Search      struct {
		Enabled    *bool    `json:"enabled"`
		MaxResults int      `json:"max_results"`
		Since      Duration `json:"since"`
	} `json:"search"`
}

const fullConfig = `{
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

func TestLoad(t *testing.T) {
	t.Setenv(EnvBackend, "")
	cfg, err := Load(writeFile(t, "config.json", fullConfig))
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
		s.KeepAlive == nil || time.Duration(*s.KeepAlive) != 10*time.Minute ||
		s.Search.Enabled == nil || !*s.Search.Enabled || s.Search.MaxResults != 3 || time.Duration(s.Search.Since) != 168*time.Hour {
		t.Errorf("settings = %+v", s)
	}

	s.NumCtx = 1
	if err := cfg.Decoder("absent")(&s); err != nil || s.NumCtx != 1 {
		t.Errorf("absent section changed settings: %v, %v", s.NumCtx, err)
	}
}

func TestLoadMissingOrEmptyFile(t *testing.T) {
	t.Setenv(EnvBackend, "")
	for _, path := range []string{filepath.Join(t.TempDir(), "absent.json"), writeFile(t, "config.json", " \n")} {
		cfg, err := Load(path)
		if err != nil || cfg.DefaultBackend != Default().DefaultBackend {
			t.Errorf("%s: cfg %+v, err %v", path, cfg, err)
		}
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name, file, body, want string
	}{
		{"trailing comma", "config.json", "{\n  \"default_backend\": \"ollama\",\n}", "invalid JSON at line 3, column 1"},
		{"unclosed", "config.json", "{\n  \"ui\": {\"theme\": \"dark\"}\n", "unexpected end of file"},
		{"unknown field", "config.json", `{"default_backends": "ollama"}`, `unknown field "default_backends"`},
		{"wrong type", "config.json", "{\n  \"default_backend\": 3\n}", "at line 2, column 23: default_backend must be string, not number"},
		{"not an object", "config.json", `["ollama"]`, "must be a JSON object"},
		{"two objects", "config.json", "{}\n{}", "unexpected data after the JSON object at line 2"},
		{"yaml content", "config.json", "default_backend: ollama\n", "must be a JSON object"},
		{"yaml file", "config.yaml", "default_backend: ollama\n", "YAML config files are no longer supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeFile(t, tt.file, tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestBackendSectionErrors(t *testing.T) {
	cfg, err := Load(writeFile(t, "config.json", `{"backends": {"ollama": {"num_ctx": "big", "nope": 1}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var s backendSettings
	err = cfg.Decoder("ollama")(&s)
	if err == nil || !strings.Contains(err.Error(), "config backends.ollama: num_ctx must be int, not string") || strings.Contains(err.Error(), "line ") {
		t.Errorf("type error = %v", err)
	}

	cfg, _ = Load(writeFile(t, "config.json", `{"backends": {"ollama": {"hots": "x"}}}`))
	if err := cfg.Decoder("ollama")(&s); err == nil || !strings.Contains(err.Error(), `unknown field "hots"`) {
		t.Errorf("unknown backend field: %v", err)
	}
}

func TestDuration(t *testing.T) {
	var d struct {
		D Duration `json:"d"`
	}
	if err := json.Unmarshal([]byte(`{"d": "1h30m"}`), &d); err != nil || time.Duration(d.D) != 90*time.Minute {
		t.Errorf("parse: %v, %v", time.Duration(d.D), err)
	}
	for _, bad := range []string{`{"d": 5}`, `{"d": "5"}`, `{"d": "soon"}`} {
		if err := json.Unmarshal([]byte(bad), &d); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if b, _ := json.Marshal(Duration(10 * time.Minute)); string(b) != `"10m0s"` {
		t.Errorf("marshal = %s", b)
	}
}

func TestEnvOverridesBackend(t *testing.T) {
	t.Setenv(EnvBackend, "fake")
	cfg, err := Load(writeFile(t, "config.json", `{"default_backend": "gemini"}`))
	if err != nil || cfg.DefaultBackend != "fake" {
		t.Errorf("DefaultBackend = %q, %v", cfg.DefaultBackend, err)
	}
}

func TestPath(t *testing.T) {
	t.Setenv(EnvConfigPath, "/x/y.json")
	if p, err := Path(); err != nil || p != "/x/y.json" {
		t.Errorf("from env: %q, %v", p, err)
	}

	t.Setenv(EnvConfigPath, "")
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	dir := filepath.Join(base, "rocket-chat")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "config.json")
	if p, err := Path(); err != nil || p != want {
		t.Errorf("default: %q, %v", p, err)
	}

	// An old YAML config with no JSON one is reported, not ignored.
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Path(); err == nil || !strings.Contains(err.Error(), "convert it to") {
		t.Errorf("yaml only: %v", err)
	}
	if err := os.WriteFile(want, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, err := Path(); err != nil || p != want {
		t.Errorf("json present: %q, %v", p, err)
	}
}

func TestThemeName(t *testing.T) {
	for in, want := range map[string]string{"": "", "auto": "", "dark": "dark", "light": "light"} {
		if got, err := (UI{Theme: in}).ThemeName(); err != nil || got != want {
			t.Errorf("ThemeName(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := (UI{Theme: "neon"}).ThemeName(); err == nil {
		t.Error("bad theme accepted")
	}
}

func TestInputLines(t *testing.T) {
	for n, ok := range map[int]bool{0: true, 1: true, 3: true, 20: true, 21: false, -1: false} {
		got, err := UI{InputLines: n}.InputLinesValue()
		if ok != (err == nil) || (ok && got != n) {
			t.Errorf("input_lines %d: %d, %v", n, got, err)
		}
	}
}
