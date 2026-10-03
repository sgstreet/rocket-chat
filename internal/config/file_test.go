package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "config.json")
	if err := Create(path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("default file does not load: %v", err)
	}
	if cfg.DefaultBackend != "ollama" || !cfg.Sessions.SaveEnabled() || cfg.APIKey("gemini") != "" ||
		!cfg.UI.MouseEnabled() || !cfg.UI.HistoryEnabled() || !cfg.UI.MarkdownEnabled() {
		t.Errorf("default config = %+v", cfg)
	}
	if err := Create(path); err == nil {
		t.Error("Create overwrote an existing file")
	}
}

func TestSetAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	orig := `{"ui": {"theme": "dark"}, "backends": {"gemini": {"search": {"enabled": false}, "model": "m"}}, "default_backend": "gemini"}`
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetAPIKey(path, "gemini", `k"1`); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetAPIKey(path, "ollama", "k2"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	want := `{
  "ui": {
    "theme": "dark"
  },
  "backends": {
    "gemini": {
      "search": {
        "enabled": false
      },
      "model": "m",
      "api_key": "k\"1"
    },
    "ollama": {
      "api_key": "k2"
    }
  },
  "default_backend": "gemini"
}
`
	if string(data) != want {
		t.Errorf("file =\n%s\nwant\n%s", data, want)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	if cfg.APIKey("gemini") != `k"1` || cfg.APIKey("ollama") != "k2" {
		t.Error("in-memory config not updated")
	}
	reloaded, err := Load(path)
	if err != nil || reloaded.APIKey("gemini") != `k"1` {
		t.Errorf("reload: %v, key %q", err, reloaded.APIKey("gemini"))
	}

	if err := cfg.SetAPIKey(path, "gemini", ""); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), `k\"1`) || !strings.Contains(string(data), `"model": "m"`) {
		t.Errorf("after removing:\n%s", data)
	}
	if cfg.APIKey("gemini") != "" {
		t.Error("removed key still in memory")
	}
}

func TestSetAPIKeyNewFileAndSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.json")
	link := filepath.Join(dir, "config.json")
	if err := os.WriteFile(target, []byte(`{"backends": null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	var cfg Config
	if err := cfg.SetAPIKey(link, "gemini", "k"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink was replaced")
	}
	if got, _ := Load(link); got.APIKey("gemini") != "k" {
		t.Error("key not saved through the symlink")
	}

	missing := filepath.Join(dir, "sub", "missing.json")
	if err := cfg.SetAPIKey(missing, "ollama", "k"); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(missing); got.APIKey("ollama") != "k" {
		t.Error("key not saved to a new file")
	}
}

func TestSetAPIKeyBadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"backends": {"gemini": 3}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := cfg.SetAPIKey(path, "gemini", "k"); err == nil || !strings.Contains(err.Error(), "backends.gemini") {
		t.Errorf("err = %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != `{"backends": {"gemini": 3}}` {
		t.Error("bad file was changed")
	}
}
