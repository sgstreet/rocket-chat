package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	t.Setenv(EnvBackend, "")
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultBackend != Default().DefaultBackend {
		t.Errorf("DefaultBackend = %q, want %q", cfg.DefaultBackend, Default().DefaultBackend)
	}
}

func TestLoadEmptyFile(t *testing.T) {
	t.Setenv(EnvBackend, "")
	cfg, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultBackend != Default().DefaultBackend {
		t.Errorf("DefaultBackend = %q", cfg.DefaultBackend)
	}
}

func TestLoadBackendSection(t *testing.T) {
	t.Setenv(EnvBackend, "")
	cfg, err := Load(writeConfig(t, `
default_backend: gemini
backends:
  fake:
    delay: 25ms
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultBackend != "gemini" {
		t.Errorf("DefaultBackend = %q, want gemini", cfg.DefaultBackend)
	}
	var s struct {
		Delay time.Duration `yaml:"delay"`
	}
	if err := cfg.Decoder("fake")(&s); err != nil {
		t.Fatal(err)
	}
	if s.Delay != 25*time.Millisecond {
		t.Errorf("Delay = %v, want 25ms", s.Delay)
	}

	s.Delay = time.Second
	if err := cfg.Decoder("absent")(&s); err != nil || s.Delay != time.Second {
		t.Errorf("absent section changed settings: %v, %v", s.Delay, err)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	if _, err := Load(writeConfig(t, "default_backends: gemini\n")); err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestEnvOverridesBackend(t *testing.T) {
	t.Setenv(EnvBackend, "fake")
	cfg, err := Load(writeConfig(t, "default_backend: gemini\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultBackend != "fake" {
		t.Errorf("DefaultBackend = %q, want fake", cfg.DefaultBackend)
	}
}

func TestPathFromEnv(t *testing.T) {
	t.Setenv(EnvConfigPath, "/x/y.yaml")
	if p, err := Path(); err != nil || p != "/x/y.yaml" {
		t.Errorf("Path() = %q, %v", p, err)
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
