package store

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "history")
	h, err := OpenHistory(path, 3)
	if err != nil || len(h.Entries()) != 0 {
		t.Fatalf("new history: %v %v", h, err)
	}
	for _, e := range []string{"one", "two\nlines", "two\nlines", "", "/role technical"} {
		if err := h.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"one", "two\nlines", "/role technical"}
	if got := h.Entries(); !slices.Equal(got, want) {
		t.Errorf("entries = %q", got)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode: %v %v", fi, err)
	}

	again, err := OpenHistory(path, 3)
	if err != nil || !slices.Equal(again.Entries(), want) {
		t.Fatalf("reloaded %q, %v", again.Entries(), err)
	}
	if err := again.Add("four"); err != nil {
		t.Fatal(err)
	}
	if got := again.Entries(); !slices.Equal(got, []string{"two\nlines", "/role technical", "four"}) {
		t.Errorf("after max: %q", got)
	}

	// Reopening trims the file to max entries; bad lines are skipped.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("not json\n\"five\"\n")
	f.Close()
	trimmed, err := OpenHistory(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := trimmed.Entries(); !slices.Equal(got, []string{"/role technical", "four", "five"}) {
		t.Errorf("trimmed %q", got)
	}
	data, _ := os.ReadFile(path)
	if n := strings.Count(string(data), "\n"); n != 3 {
		t.Errorf("file has %d lines after trimming:\n%s", n, data)
	}
}

func TestDefaultHistoryPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	if p, _ := DefaultHistoryPath(); p != filepath.Join("/data", "rocket-chat", "history") {
		t.Errorf("path = %q", p)
	}
}
