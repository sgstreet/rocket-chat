package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/backend/fake"
)

// memKeys is an in-memory Keys.
type memKeys struct {
	saved map[string]string
	env   string
}

func (k *memKeys) Names() []string { return []string{"a", "b"} }

func (k *memKeys) Status(name string) string {
	if k.saved[name] != "" {
		return "saved"
	}
	return "not set"
}

func (k *memKeys) Set(name, key string) error {
	if strings.Contains(key, "bad") {
		return errors.New("bad key")
	}
	k.saved[name] = key
	return nil
}

func (k *memKeys) Overridden(string) string { return k.env }

func TestKeyCommand(t *testing.T) {
	keys := &memKeys{saved: map[string]string{}}
	opened := 0
	h := newHarnessWith(t, map[string]*fake.Backend{"a": {}, "b": {}}, Options{Backend: "a", Keys: keys})
	open := h.m.opts.Open
	h.m.opts.Open = func(name string) (backend.Backend, error) {
		opened++
		return open(name)
	}

	h.typeAndSend("/key")
	if list := h.last(entryNotice).text; !strings.Contains(list, "a        not set") || !strings.Contains(list, "b        not set") {
		t.Errorf("list:\n%s", list)
	}

	h.typeAndSend("/key nope")
	if !strings.Contains(h.last(entryError).text, "nope takes no API key") {
		t.Error("unknown backend accepted")
	}
	h.typeAndSend("/key a secret123")
	if keys.saved["a"] != "" || !strings.Contains(h.last(entryError).text, "hidden prompt") {
		t.Error("key typed on the command line was accepted")
	}

	// Hidden entry: the key is never on screen, and the backend is reopened.
	h.typeAndSend("/key a")
	if h.m.keyFor != "a" || !strings.Contains(h.view(), "a API key:") || !strings.Contains(h.view(), "esc cancel") {
		t.Fatalf("not in key entry; view:\n%s", h.view())
	}
	h.send(tea.PasteMsg{Content: "sk-"})
	for _, r := range "secret" {
		h.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if v := h.view(); strings.Contains(v, "secret") || strings.Contains(v, "sk-") || !strings.Contains(v, "•••") {
		t.Errorf("key visible:\n%s", v)
	}
	h.send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if keys.saved["a"] != "sk-secret" || h.m.keyFor != "" || opened != 1 {
		t.Errorf("saved %q, keyFor %q, reopened %d", keys.saved["a"], h.m.keyFor, opened)
	}
	if !strings.Contains(h.last(entryNotice).text, "Saved the a API key.") {
		t.Errorf("notice %q", h.last(entryNotice).text)
	}
	if slices.ContainsFunc(h.m.entries, func(e *entry) bool { return strings.Contains(e.text, "secret") }) {
		t.Error("key in the transcript")
	}
	if h.m.input.Value() != "" || !h.m.input.Focused() {
		t.Error("chat input not restored")
	}

	// Esc cancels; the other backend is not reopened but suggested.
	h.typeAndSend("/key b")
	h.send(tea.KeyPressMsg{Code: 'x', Text: "x"})
	h.send(tea.KeyPressMsg{Code: tea.KeyEscape})
	if keys.saved["b"] != "" || h.m.keyFor != "" || h.last(entryNotice).text != "No key saved." {
		t.Error("esc did not cancel")
	}
	keys.env = "B_KEY"
	h.typeAndSend("/key b")
	h.send(tea.PasteMsg{Content: "bkey"})
	h.send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if n := h.last(entryNotice).text; keys.saved["b"] != "bkey" || opened != 1 || !strings.Contains(n, "/backend b") || !strings.Contains(n, "$B_KEY") {
		t.Errorf("saved %q, opened %d, notice %q", keys.saved["b"], opened, n)
	}

	h.typeAndSend("/key b clear")
	if keys.saved["b"] != "" || !strings.Contains(h.last(entryNotice).text, "Removed the saved b API key.") {
		t.Error("clear")
	}
	h.typeAndSend("/key a")
	h.send(tea.PasteMsg{Content: "bad"})
	h.send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(h.last(entryError).text, "Key not saved: bad key") {
		t.Error("save error not shown")
	}
}

func TestKeyCommandWithoutKeys(t *testing.T) {
	h := newHarness(t, map[string]*fake.Backend{"a": {}}, "a")
	h.typeAndSend("/key")
	if !strings.Contains(h.last(entryError).text, "cannot be changed") {
		t.Error("/key without Keys")
	}
}
