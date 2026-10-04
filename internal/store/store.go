// Package store saves chat sessions as one JSON file each.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sgstreet/rocket-chat/internal/chat"
)

// Session is a saved conversation.
type Session struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Backend string    `json:"backend"`
	Model   string    `json:"model,omitempty"`
	System  string    `json:"system,omitempty"`
	// Role is the ID of the role whose prompt System holds, if any.
	Role     string         `json:"role,omitempty"`
	Messages []chat.Message `json:"messages"`
	// Summary stands in for the first Compacted messages when the chat is
	// sent to a model; the messages are kept for reading.
	// SummaryRestricted marks a summary of grounded Gemini answers, which
	// is only sent to Gemini.
	Summary           string `json:"summary,omitempty"`
	Compacted         int    `json:"compacted,omitempty"`
	SummaryRestricted bool   `json:"summary_restricted,omitempty"`
}

// Summary describes a saved session without its messages.
type Summary struct {
	ID       string
	Title    string
	Updated  time.Time
	Backend  string
	Messages int
}

// Store keeps sessions in a directory.
type Store struct {
	dir string
}

// DefaultDir returns $XDG_DATA_HOME/rocket-chat/sessions, falling back to
// ~/.local/share/rocket-chat/sessions.
func DefaultDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "rocket-chat", "sessions"), nil
}

// Open returns a store for dir, creating it if needed.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Dir returns the store's directory.
func (s *Store) Dir() string { return s.dir }

// NewID returns a sortable, unique session ID.
func NewID(now time.Time) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

var validID = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

func (s *Store) path(id string) (string, error) {
	if !validID.MatchString(id) {
		return "", fmt.Errorf("invalid session ID %q", id)
	}
	return filepath.Join(s.dir, id+".json"), nil
}

// Save writes the session, replacing any earlier version. It sets the
// title from the first user message when the session has none.
func (s *Store) Save(sess *Session) error {
	if sess.Title == "" {
		sess.Title = Title(sess.Messages)
	}
	path, err := s.path(sess.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".tmp-"+sess.ID+"-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ErrNotFound is returned when no session matches.
var ErrNotFound = errors.New("session not found")

// Load reads the session with the given ID, or the most recent one when id
// is "last".
func (s *Store) Load(id string) (*Session, error) {
	if id == "last" {
		list, err := s.List()
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			return nil, ErrNotFound
		}
		id = list[0].ID
	}
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &sess, nil
}

// List returns the saved sessions, most recently updated first. Files that
// cannot be read are skipped.
func (s *Store) List() ([]Summary, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []Summary
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || !validID.MatchString(id) {
			continue
		}
		sess, err := s.Load(id)
		if err != nil {
			continue
		}
		out = append(out, Summary{
			ID:       sess.ID,
			Title:    sess.Title,
			Updated:  sess.Updated,
			Backend:  sess.Backend,
			Messages: len(sess.Messages),
		})
	}
	slices.SortFunc(out, func(a, b Summary) int { return b.Updated.Compare(a.Updated) })
	return out, nil
}

// Title makes a short title from the first user message.
func Title(messages []chat.Message) string {
	for _, m := range messages {
		if m.Role != chat.RoleUser {
			continue
		}
		t := strings.Join(strings.Fields(m.Text), " ")
		if r := []rune(t); len(r) > 60 {
			t = strings.TrimSpace(string(r[:59])) + "…"
		}
		return t
	}
	return "(empty)"
}
