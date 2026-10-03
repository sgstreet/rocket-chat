package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/backend/fake"
	"github.com/sgstreet/rocket-chat/internal/chat"
	"github.com/sgstreet/rocket-chat/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestAutosaveAndResume(t *testing.T) {
	st := openStore(t)
	backends := map[string]*fake.Backend{
		"a": {Caps: backend.Capabilities{DefaultModel: "m1"}},
		"b": {},
	}
	h := newHarnessWith(t, backends, Options{Backend: "a", Store: st})
	h.typeAndSend("/role custom be brief")
	h.typeAndSend("first chat question")
	h.typeAndSend("second message")

	list, err := st.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("saved %d sessions, %v", len(list), err)
	}
	saved, err := st.Load(list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Title != "first chat question" || len(saved.Messages) != 4 || saved.System != "be brief" || saved.Backend != "a" {
		t.Errorf("saved = %+v", saved)
	}
	if saved.Messages[1].Model != "m1" {
		t.Errorf("assistant model = %q, want m1", saved.Messages[1].Model)
	}

	// A new chat on another backend becomes a second session.
	h.typeAndSend("/new")
	h.typeAndSend("/backend b")
	h.typeAndSend("/role off")
	h.typeAndSend("other topic")
	if list, _ := st.List(); len(list) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(list))
	}

	h.typeAndSend("/sessions")
	n := h.last(entryNotice).text
	if !strings.Contains(n, " 1. ") || !strings.Contains(n, "other topic") || !strings.Contains(n, "first chat question (4 messages)") {
		t.Errorf("sessions list = %q", n)
	}
	h.typeAndSend("/resume 2")
	if h.m.backendName != "a" || h.m.system != "be brief" || len(h.m.history()) != 4 {
		t.Errorf("after resume: backend %q system %q history %d", h.m.backendName, h.m.system, len(h.m.history()))
	}
	if !strings.Contains(h.view(), "a/m1") || !strings.Contains(h.last(entryNotice).text, `Resumed "first chat question"`) {
		t.Errorf("resumed view\n%s", h.view())
	}

	// Continuing updates the same session rather than making a new one.
	h.typeAndSend("third message")
	if list, _ := st.List(); len(list) != 2 {
		t.Errorf("continuing made a new session: %d", len(list))
	}
	if again, _ := st.Load(saved.ID); len(again.Messages) != 6 {
		t.Errorf("resumed session has %d messages, want 6", len(again.Messages))
	}

	h.typeAndSend("/resume 9")
	if !strings.Contains(h.last(entryError).text, "No session number 9") {
		t.Error("bad session number accepted")
	}
}

func TestResumeOption(t *testing.T) {
	st := openStore(t)
	sess := &store.Session{ID: "s1", Backend: "b", Model: "big", Messages: []chat.Message{
		{Role: chat.RoleUser, Text: "old question"},
		{Role: chat.RoleAssistant, Text: "old answer", Backend: "b", Model: "big"},
	}}
	h := newHarnessWith(t, map[string]*fake.Backend{"a": {}, "b": {}}, Options{Backend: "a", Store: st, Resume: sess})
	if h.m.backendName != "b" || h.m.modelName != "big" {
		t.Errorf("backend %q model %q", h.m.backendName, h.m.modelName)
	}
	if v := h.view(); !strings.Contains(v, "old answer") || !strings.Contains(v, "b/big") {
		t.Errorf("view\n%s", v)
	}
}

func TestExport(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, map[string]*fake.Backend{"fake": {}}, "fake")
	h.typeAndSend("/export " + filepath.Join(dir, "x.md"))
	if !strings.Contains(h.last(entryError).text, "Nothing to export") {
		t.Error("empty export")
	}
	h.typeAndSend("hello world")
	path := filepath.Join(dir, "chat.md")
	h.typeAndSend("/export " + path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "# hello world\n\n## You\n\nhello world\n\n## fake\n\nhello world\n"; string(data) != want {
		t.Errorf("export =\n%s\nwant\n%s", data, want)
	}
}

func TestSessionsOff(t *testing.T) {
	h := newHarness(t, map[string]*fake.Backend{"fake": {}}, "fake")
	h.typeAndSend("hi")
	h.typeAndSend("/sessions")
	if !strings.Contains(h.last(entryError).text, "turned off") {
		t.Error("/sessions with no store")
	}
}
