package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "state.json")
	if s, err := LoadState(path); err != nil || !reflect.DeepEqual(s, State{}) {
		t.Fatalf("missing file: %+v, %v", s, err)
	}
	want := State{Role: RoleCustom, Prompt: "Answer in French.", Backend: "zai", Models: map[string]string{"zai": "glm-5.3"}}
	if err := SaveState(path, want); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadState(path); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, %v", got, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	if err := os.WriteFile(path, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(path); err == nil {
		t.Error("bad JSON accepted")
	}
}

func TestDefaultStatePath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	if p, _ := DefaultStatePath(); p != filepath.Join("/data", "rocket-chat", "state.json") {
		t.Errorf("path = %q", p)
	}
}
