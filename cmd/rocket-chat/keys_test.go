package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgstreet/rocket-chat/internal/config"
)

func clearKeyEnv(t *testing.T) {
	for _, env := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "OLLAMA_API_KEY"} {
		t.Setenv(env, "")
	}
}

func savedKey(t *testing.T, path, name string) string {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.APIKey(name)
}

func TestSetKeyFromStdin(t *testing.T) {
	clearKeyEnv(t)
	path := filepath.Join(t.TempDir(), "config.json")
	key := "  AIza-test_key\n"
	r := cli(t, t.Context(), &key, "--config", path, "--set-key", "gemini")
	if r.code != exitOK || !strings.Contains(r.out, "Saved the gemini API key in "+path) {
		t.Fatalf("got %+v", r)
	}
	if got := savedKey(t, path, "gemini"); got != "AIza-test_key" {
		t.Errorf("saved %q", got)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}

	t.Setenv("GEMINI_API_KEY", "from-env")
	r = cli(t, t.Context(), &key, "--config", path, "--set-key", "gemini")
	if r.code != exitOK || !strings.Contains(r.errOut, "GEMINI_API_KEY is set and takes precedence") {
		t.Errorf("env override not reported: %+v", r)
	}

	r = cli(t, t.Context(), nil, "--config", path, "--remove-key", "gemini")
	if r.code != exitOK || !strings.Contains(r.out, "Removed the gemini API key") || savedKey(t, path, "gemini") != "" {
		t.Errorf("remove: %+v", r)
	}
	r = cli(t, t.Context(), nil, "--config", path, "--remove-key", "gemini")
	if r.code != exitOK || !strings.Contains(r.out, "No gemini API key is saved") {
		t.Errorf("remove again: %+v", r)
	}
}

func TestSetKeyErrors(t *testing.T) {
	clearKeyEnv(t)
	key := "k"
	if r := cli(t, t.Context(), &key, "--set-key", "fake"); r.code != exitUsage || !strings.Contains(r.errOut, "takes a backend name (gemini, ollama)") {
		t.Errorf("not a backend with a key: %+v", r)
	}
	if r := cli(t, t.Context(), nil, "--set-key", "gemini"); r.code != exitUsage || !strings.Contains(r.errOut, "reads the key from stdin") {
		t.Errorf("no input: %+v", r)
	}
	bad := "two words"
	if r := cli(t, t.Context(), &bad, "--set-key", "gemini"); r.code != exitError || !strings.Contains(r.errOut, "spaces") {
		t.Errorf("bad key: %+v", r)
	}
	empty := "\n"
	if r := cli(t, t.Context(), &empty, "--set-key", "gemini"); r.code != exitError || !strings.Contains(r.errOut, "empty") {
		t.Errorf("empty key: %+v", r)
	}
	if r := cli(t, t.Context(), &key, "--set-key", "gemini", "--remove-key", "gemini"); r.code != exitUsage {
		t.Errorf("both flags: %+v", r)
	}
}

func TestSetKeyFromTerminal(t *testing.T) {
	clearKeyEnv(t)
	path := filepath.Join(t.TempDir(), "config.json")
	var prompt string
	var out, errOut bytes.Buffer
	e := env{stdout: &out, stderr: &errOut, readSecret: func(_ context.Context, p string) (string, error) {
		prompt = p
		return "ollama-key", nil
	}}
	if code := run(t.Context(), []string{"--config", path, "--set-key", "ollama"}, e); code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(prompt, "hidden") || savedKey(t, path, "ollama") != "ollama-key" {
		t.Errorf("prompt %q, saved %q", prompt, savedKey(t, path, "ollama"))
	}
}

func TestPromptForKey(t *testing.T) {
	clearKeyEnv(t)
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Default()
	keys := &keyStore{cfg: &cfg, path: path}
	asked := 0
	var errOut bytes.Buffer
	e := env{stderr: &errOut, readSecret: func(context.Context, string) (string, error) {
		asked++
		return "pasted", nil
	}}

	// Ollama's key is optional, so it is never asked for.
	if err := promptForKey(t.Context(), e, keys, "ollama"); err != nil || asked != 0 {
		t.Errorf("ollama: asked %d, %v", asked, err)
	}
	if err := promptForKey(t.Context(), e, keys, "gemini"); err != nil || asked != 1 {
		t.Fatalf("gemini: asked %d, %v", asked, err)
	}
	if cfg.APIKey("gemini") != "pasted" || savedKey(t, path, "gemini") != "pasted" {
		t.Error("key not saved")
	}
	if !strings.Contains(errOut.String(), "aistudio.google.com") {
		t.Errorf("no hint where to get a key: %q", errOut.String())
	}
	// Now a key is saved, so there is nothing to ask.
	if err := promptForKey(t.Context(), e, keys, "gemini"); err != nil || asked != 1 {
		t.Errorf("asked again: %d, %v", asked, err)
	}
	if got := keys.Status("gemini"); got != "saved in "+path {
		t.Errorf("status = %q", got)
	}
	t.Setenv("GEMINI_API_KEY", "x")
	if got := keys.Status("gemini"); got != "from $GEMINI_API_KEY" {
		t.Errorf("status = %q", got)
	}
}

func TestSetKeyAsFlagValue(t *testing.T) {
	clearKeyEnv(t)
	const secret = "AQ.Ab8-test_KEY"
	path := filepath.Join(t.TempDir(), "config.json")

	r := cli(t, t.Context(), nil, "--config", path, "--backend", "gemini", "--set-key", secret)
	if r.code != exitOK || savedKey(t, path, "gemini") != secret {
		t.Fatalf("got %+v, saved %q", r, savedKey(t, path, "gemini"))
	}
	if !strings.Contains(r.errOut, "shell history") || strings.Contains(r.out+r.errOut, secret) {
		t.Errorf("want a history note and no echo of the key: %+v", r)
	}

	// Errors never repeat a value that may be a key.
	for _, args := range [][]string{
		{"--set-key", secret},
		{"-b", "fake", "--set-key", secret},
		{"--remove-key", secret},
	} {
		r := cli(t, t.Context(), nil, args...)
		if r.code != exitUsage || strings.Contains(r.out+r.errOut, secret) {
			t.Errorf("%v: %+v", args, r)
		}
	}
	if r := cli(t, t.Context(), nil, "-b", "fake", "--set-key", secret); !strings.Contains(r.errOut, "fake takes no API key") {
		t.Errorf("backend without keys: %+v", r)
	}
}
