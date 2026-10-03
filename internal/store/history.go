package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// DefaultHistoryMax is how many inputs a history keeps.
const DefaultHistoryMax = 1000

// History is the list of inputs typed in the chat, oldest first, kept in a
// file with one JSON string per line so multi-line inputs survive.
type History struct {
	mu      sync.Mutex
	path    string
	limit   int
	entries []string
}

// DefaultHistoryPath returns $XDG_DATA_HOME/rocket-chat/history, falling
// back to ~/.local/share like DefaultDir.
func DefaultHistoryPath() (string, error) {
	sessions, err := DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(sessions), "history"), nil
}

// OpenHistory loads the history at path, keeping the newest limit entries
// (DefaultHistoryMax when limit is 0). A missing file is an empty history.
// Lines that do not parse are skipped.
func OpenHistory(path string, limit int) (*History, error) {
	if limit <= 0 {
		limit = DefaultHistoryMax
	}
	h := &History{path: path, limit: limit}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var s string
		if json.Unmarshal(sc.Bytes(), &s) == nil && s != "" {
			h.entries = append(h.entries, s)
		}
	}
	if len(h.entries) > limit {
		h.entries = h.entries[len(h.entries)-limit:]
		// Rewrite the file so it does not grow without bound.
		if err := h.rewrite(); err != nil {
			return nil, err
		}
	}
	return h, nil
}

// Entries returns a copy of the history, oldest first.
func (h *History) Entries() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.entries...)
}

// Add appends an entry and saves it, skipping empty entries and repeats of
// the previous one.
func (h *History) Add(entry string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if entry == "" || (len(h.entries) > 0 && h.entries[len(h.entries)-1] == entry) {
		return nil
	}
	h.entries = append(h.entries, entry)
	if len(h.entries) > h.limit {
		h.entries = h.entries[len(h.entries)-h.limit:]
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(h.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	line, _ := json.Marshal(entry)
	_, err = f.Write(append(line, '\n'))
	return errors.Join(err, f.Close())
}

// rewrite replaces the file with the current entries.
func (h *History) rewrite() error {
	var buf bytes.Buffer
	for _, e := range h.entries {
		line, _ := json.Marshal(e)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	tmp, err := os.CreateTemp(filepath.Dir(h.path), ".history-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), h.path)
}
