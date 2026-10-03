package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sgstreet/rocket-chat/internal/chat"
)

func TestSaveLoadList(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	older := &Session{
		ID: NewID(t0), Created: t0, Updated: t0, Backend: "ollama",
		Messages: []chat.Message{{Role: chat.RoleUser, Text: "first   question\nwith lines"}},
	}
	newer := &Session{
		ID: NewID(t0.Add(time.Hour)), Created: t0, Updated: t0.Add(time.Hour), Backend: "gemini", Model: "m", System: "sys",
		Messages: []chat.Message{
			{Role: chat.RoleUser, Text: strings.Repeat("é", 80)},
			{Role: chat.RoleAssistant, Text: "answer", Backend: "gemini", Grounding: &chat.Grounding{
				Sources: []chat.Source{{Title: "t", URL: "u", Cited: true}},
				Spans:   []chat.Span{{Start: 0, End: 6, SourceIndexes: []int{0}}},
			}},
		},
	}
	for _, sess := range []*Session{older, newer} {
		if err := s.Save(sess); err != nil {
			t.Fatal(err)
		}
	}
	if older.Title != "first question with lines" {
		t.Errorf("title = %q", older.Title)
	}
	if r := []rune(newer.Title); len(r) != 60 || r[59] != '…' {
		t.Errorf("long title = %q", newer.Title)
	}

	got, err := s.Load(newer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.System != "sys" || len(got.Messages) != 2 || got.Messages[1].Grounding.Spans[0].End != 6 {
		t.Errorf("loaded = %+v", got)
	}
	last, err := s.Load("last")
	if err != nil || last.ID != newer.ID {
		t.Errorf("last = %v, %v", last, err)
	}

	// A broken file is skipped by List.
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != newer.ID || list[1].ID != older.ID || list[0].Messages != 2 {
		t.Errorf("list = %+v", list)
	}

	info, err := os.Stat(filepath.Join(dir, newer.ID+".json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, %v", info.Mode(), err)
	}
	if dirInfo, _ := os.Stat(dir); dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", dirInfo.Mode())
	}
}

func TestLoadErrors(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("last"); !errors.Is(err, ErrNotFound) {
		t.Errorf("last on empty store: %v", err)
	}
	if _, err := s.Load("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if _, err := s.Load("../etc/passwd"); err == nil || !strings.Contains(err.Error(), "invalid session ID") {
		t.Errorf("path traversal: %v", err)
	}
}

func TestDefaultDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	if d, _ := DefaultDir(); d != "/data/rocket-chat/sessions" {
		t.Errorf("dir = %q", d)
	}
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/u")
	if d, _ := DefaultDir(); d != "/home/u/.local/share/rocket-chat/sessions" {
		t.Errorf("dir = %q", d)
	}
}
