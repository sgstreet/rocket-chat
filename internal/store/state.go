package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// State is what rocket-chat remembers between runs that is not a chat or
// a setting: currently the role last chosen with /role.
type State struct {
	// Role is the last role chosen: a role ID, RoleCustom (Prompt holds
	// the text) or RoleOff. Empty means none was chosen yet.
	Role   string `json:"role,omitempty"`
	Prompt string `json:"prompt,omitempty"`
}

// Values of State.Role that are not role IDs.
const (
	RoleCustom = "custom"
	RoleOff    = "off"
)

// DefaultStatePath returns $XDG_DATA_HOME/rocket-chat/state.json, falling
// back to ~/.local/share like DefaultDir.
func DefaultStatePath() (string, error) {
	sessions, err := DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(sessions), "state.json"), nil
}

// LoadState reads the state file. A missing file is an empty state.
func LoadState(path string) (State, error) {
	var s State
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

// SaveState replaces the state file, readable only by its owner.
func SaveState(path string, s State) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*")
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
