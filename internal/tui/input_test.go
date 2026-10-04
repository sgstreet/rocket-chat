package tui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/backend/fake"
)

// memHistory is an in-memory History.
type memHistory struct{ entries []string }

func (h *memHistory) Entries() []string { return slices.Clone(h.entries) }

func (h *memHistory) Add(e string) error {
	h.entries = append(h.entries, e)
	return nil
}

func press(k string) tea.KeyPressMsg {
	switch k {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "ctrl+p":
		return tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}
	case "ctrl+n":
		return tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}

func TestHistoryBrowseAndEdit(t *testing.T) {
	store := &memHistory{entries: []string{"old question"}}
	h := newHarnessWith(t, map[string]*fake.Backend{"fake": {}}, Options{Backend: "fake", History: store})

	h.typeAndSend("first")
	h.typeAndSend("/role technical")
	if !slices.Equal(store.entries, []string{"old question", "first", "/role technical"}) {
		t.Fatalf("saved %q", store.entries)
	}

	h.m.input.SetValue("draft")
	h.send(press("up"))
	if h.m.input.Value() != "/role technical" {
		t.Fatalf("up = %q", h.m.input.Value())
	}
	h.send(press("up"))
	h.send(press("up"))
	if h.m.input.Value() != "old question" {
		t.Fatalf("up x3 = %q", h.m.input.Value())
	}
	h.send(press("up")) // already at the oldest
	if h.m.input.Value() != "old question" {
		t.Errorf("past the oldest = %q", h.m.input.Value())
	}

	// Edit a recalled entry, move away and back: the edit is kept.
	h.m.input.InsertString("!")
	h.send(press("down"))
	if h.m.input.Value() != "first" {
		t.Errorf("down = %q", h.m.input.Value())
	}
	h.send(press("ctrl+p"))
	if h.m.input.Value() != "old question!" {
		t.Errorf("edit lost: %q", h.m.input.Value())
	}
	// Down past the newest restores the draft.
	for range 3 {
		h.send(press("ctrl+n"))
	}
	if h.m.input.Value() != "draft" {
		t.Errorf("draft = %q", h.m.input.Value())
	}

	// Sending an edited entry adds it; the original stays as it was.
	h.send(press("up"))
	h.send(press("up"))
	h.m.input.InsertString("?")
	h.send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := store.entries[len(store.entries)-1]; got != "first?" || store.entries[1] != "first" {
		t.Errorf("entries after edit: %q", store.entries)
	}
	if h.m.hist.pos != -1 {
		t.Error("still browsing after sending")
	}
}

func TestHistoryMultilineInput(t *testing.T) {
	h := newHarnessWith(t, map[string]*fake.Backend{"fake": {}}, Options{Backend: "fake", History: &memHistory{entries: []string{"earlier"}}})
	h.m.input.SetValue("line one\nline two")
	// The cursor is on the last line: Up moves it rather than recalling.
	// (Update only: the textarea's cursor blink would never settle.)
	h.m.Update(press("up"))
	if h.m.input.Value() != "line one\nline two" || h.m.input.Line() != 0 {
		t.Fatalf("value %q line %d", h.m.input.Value(), h.m.input.Line())
	}
	h.send(press("up"))
	if h.m.input.Value() != "earlier" {
		t.Errorf("up on the first line = %q", h.m.input.Value())
	}
	h.send(press("down"))
	if h.m.input.Value() != "line one\nline two" {
		t.Errorf("draft not restored: %q", h.m.input.Value())
	}
}

func TestHistorySkipsKeys(t *testing.T) {
	for text, keep := range map[string]bool{
		"/key gemini":         true,
		"/key gemini clear":   true,
		"/key gemini AIzaXYZ": false,
		"/keys ollama sk-1 2": false,
		"/key gemini clear x": false,
		"how do keys work?":   true,
		"   ":                 false,
		"/model qwen3:4b":     true,
	} {
		if got := worthKeeping(text); got != keep {
			t.Errorf("worthKeeping(%q) = %v", text, got)
		}
	}
	store := &memHistory{}
	h := newHarnessWith(t, map[string]*fake.Backend{"fake": {}}, Options{Backend: "fake", History: store, Keys: &memKeys{saved: map[string]string{}}})
	h.typeAndSend("/key a secret123")
	if len(store.entries) != 0 || len(h.m.hist.entries) != 0 {
		t.Errorf("key kept in history: %q", store.entries)
	}
}

type failingHistory struct{ memHistory }

func (failingHistory) Add(string) error { return fmt.Errorf("disk full") }

func TestHistorySaveError(t *testing.T) {
	h := newHarnessWith(t, map[string]*fake.Backend{"fake": {}}, Options{Backend: "fake", History: &failingHistory{}})
	h.typeAndSend("/thinking")
	h.typeAndSend("/thinking")
	errs := 0
	for _, e := range h.m.entries {
		if e.kind == entryError && strings.Contains(e.text, "disk full") {
			errs++
		}
	}
	if errs != 1 {
		t.Errorf("%d history errors shown, want 1", errs)
	}
	h.send(press("up"))
	if h.m.input.Value() != "/thinking" {
		t.Errorf("unsaved entry not kept in memory: %q", h.m.input.Value())
	}
}

func TestTabCompletion(t *testing.T) {
	h := newHarnessWith(t, map[string]*fake.Backend{"fake": {}, "gemini": {}}, Options{Backend: "fake", Keys: &memKeys{saved: map[string]string{}}})
	tab := func(start string, keys ...string) string {
		h.m.input.SetValue(start)
		h.m.compl = nil
		for _, k := range keys {
			h.send(press(k))
		}
		return h.m.input.Value()
	}

	if got := tab("/ba", "tab"); got != "/backend " {
		t.Errorf("/ba = %q", got)
	}
	if got := tab("/backend g", "tab"); got != "/backend gemini " {
		t.Errorf("/backend g = %q", got)
	}
	if got := tab("/role show te", "tab"); got != "/role show technical " {
		t.Errorf("/role show te = %q", got)
	}
	// Several matches with nothing more in common: Tab cycles through them.
	if got := tab("/search o", "tab"); got != "/search on" || !strings.Contains(h.view(), "tab: [on]  off") {
		t.Errorf("/search o = %q; view:\n%s", got, h.view())
	}
	if got := tab("/search o", "tab", "tab"); got != "/search off" {
		t.Errorf("cycle = %q", got)
	}
	if got := tab("/search o", "tab", "tab", "tab"); got != "/search on" {
		t.Errorf("cycle twice = %q", got)
	}
	if got := tab("/search o", "shift+tab"); got != "/search off" {
		t.Errorf("shift+tab = %q", got)
	}
	if got := tab("/r", "tab", "tab"); got != "/retry" || !strings.Contains(h.view(), "role  [retry]  resume") {
		t.Errorf("/r tab tab = %q; view:\n%s", got, h.view())
	}
	if got := tab("/key a ", "tab"); got != "/key a clear " {
		t.Errorf("/key a = %q", got)
	}
	if got := tab("/zzz", "tab"); got != "/zzz" || !strings.Contains(h.view(), "no completions") {
		t.Errorf("/zzz = %q", got)
	}
	// Typing anything else ends the completion and its hint.
	h.m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if h.m.complHint != "" || strings.Contains(h.view(), "no completions") {
		t.Error("hint not cleared")
	}
	// Not a command: Tab is left to the input box.
	h.m.input.SetValue("hello")
	if _, handled := h.m.handleKey(press("tab")); handled || h.m.complHint != "" {
		t.Error("Tab completed plain text")
	}

	// /model completes from the last /model list.
	h.m.models = []backend.ModelInfo{{Name: "qwen3:4b"}, {Name: "qwen3:0.6b"}, {Name: "llama3.2"}}
	if got := tab("/model ll", "tab"); got != "/model llama3.2 " {
		t.Errorf("/model ll = %q", got)
	}
	// A shared prefix is filled in first, then Tab cycles.
	if got := tab("/model q", "tab"); got != "/model qwen3:" || !strings.Contains(h.view(), "tab: qwen3:4b  qwen3:0.6b") {
		t.Errorf("/model q = %q", got)
	}
	if got := tab("/model q", "tab", "tab"); got != "/model qwen3:4b" {
		t.Errorf("/model q tab tab = %q", got)
	}
}

func TestMouse(t *testing.T) {
	h := newHarnessWith(t, map[string]*fake.Backend{"fake": {}}, Options{Backend: "fake", Mouse: true})
	if h.m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("mouse not captured")
	}
	for i := range 40 {
		h.m.notice("line %d", i)
	}
	bottom := h.m.viewport.YOffset()
	h.send(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if h.m.viewport.YOffset() >= bottom {
		t.Errorf("wheel up did not scroll: %d -> %d", bottom, h.m.viewport.YOffset())
	}
	h.send(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if h.m.viewport.YOffset() != bottom {
		t.Errorf("wheel down: %d, want %d", h.m.viewport.YOffset(), bottom)
	}

	h.typeAndSend("/mouse off")
	if !strings.Contains(h.last(entryError).text, "Unknown command /mouse") {
		t.Error("/mouse should be gone; the mouse is set with ui.mouse")
	}
	off := newHarnessWith(t, map[string]*fake.Backend{"fake": {}}, Options{Backend: "fake"})
	if off.m.View().MouseMode != tea.MouseModeNone {
		t.Error("mouse captured with Mouse off")
	}
}

// modelsFail is a backend whose model list cannot be fetched.
type modelsFail struct{ *fake.Backend }

func (modelsFail) Models(context.Context) ([]backend.ModelInfo, error) {
	return nil, errors.New("server down")
}

func TestModelCompletionFetchesModels(t *testing.T) {
	a := &fake.Backend{ModelList: []backend.ModelInfo{{Name: "qwen3:4b"}, {Name: "qwen3:0.6b"}, {Name: "llama3.2"}}}
	b := &fake.Backend{ModelList: []backend.ModelInfo{{Name: "gemini-flash-latest"}, {Name: "gemini-pro-latest"}}}
	h := newHarnessWith(t, map[string]*fake.Backend{"a": a, "b": b}, Options{Backend: "a"})
	tab := func(start string) string {
		h.m.input.SetValue(start)
		h.m.compl = nil
		h.send(press("tab"))
		return h.m.input.Value()
	}

	// No /model run yet: Tab fetches the list and completes.
	if h.m.models != nil {
		t.Fatal("models loaded before /model or Tab")
	}
	if got := tab("/model ll"); got != "/model llama3.2 " {
		t.Errorf("/model ll = %q", got)
	}
	if got := tab("/model q"); got != "/model qwen3:" || !strings.Contains(h.view(), "qwen3:4b  qwen3:0.6b") {
		t.Errorf("/model q = %q", got)
	}
	if got := tab("/model "); got != "/model qwen3:4b" {
		t.Errorf("/model <tab> = %q", got)
	}
	// Nothing was printed into the conversation.
	for _, e := range h.m.entries {
		if strings.Contains(e.text, "models (choose") {
			t.Error("Tab printed the model list")
		}
	}

	// After switching backend the list is fetched again, for that backend.
	h.typeAndSend("/backend b")
	if got := tab("/model gemini-p"); got != "/model gemini-pro-latest " {
		t.Errorf("after /backend: %q", got)
	}

	// A list that cannot be fetched says why.
	h.m.b, h.m.models = modelsFail{b}, nil
	if got := tab("/model g"); got != "/model g" || !strings.Contains(h.view(), "cannot list models: server down") {
		t.Errorf("failed list: %q; view:\n%s", got, h.view())
	}

	// A list that arrives after the input changed only stores the models.
	h.m.b, h.m.models = b, nil
	h.m.input.SetValue("/model g")
	cmd, _ := h.m.complete(1)
	h.m.input.SetValue("something else")
	h.send(cmd())
	if h.m.input.Value() != "something else" || len(h.m.models) != 2 {
		t.Errorf("late list: input %q, models %d", h.m.input.Value(), len(h.m.models))
	}
}

func TestPartialModelList(t *testing.T) {
	h := newHarness(t, map[string]*fake.Backend{"a": {}}, "a")
	partial := &backend.PartialList{Err: errors.New("cloud models not listed: offline")}
	models := []backend.ModelInfo{{Name: "qwen3:0.6b"}, {Name: "kimi-k3:cloud", Description: "cloud"}}

	h.send(modelsMsg{backend: "a", models: models, err: partial})
	got := h.last(entryNotice).text
	if !strings.Contains(got, "kimi-k3:cloud  cloud") || !strings.Contains(got, "(cloud models not listed: offline)") {
		t.Errorf("listing = %q", got)
	}

	// Tab completion uses a partial list too.
	h.m.models = nil
	h.send(modelsMsg{backend: "a", models: models, err: partial, forCompletion: true})
	if len(h.m.models) != 2 {
		t.Errorf("completion list = %v", h.m.models)
	}
}

func TestSwitchingReleasesTheOldModel(t *testing.T) {
	a := &fake.Backend{Caps: backend.Capabilities{DefaultModel: "a-default"}}
	b := &fake.Backend{}
	h := newHarnessWith(t, map[string]*fake.Backend{"a": a, "b": b}, Options{Backend: "a"})

	h.typeAndSend("/model m1")
	if got := a.Released(); !slices.Equal(got, []string{"a-default"}) {
		t.Fatalf("after /model m1: released %q, want the default model", got)
	}
	if !strings.Contains(h.last(entryNotice).text, "Unloaded a-default from a.") {
		t.Errorf("notice = %q", h.last(entryNotice).text)
	}

	// Choosing the model already in use releases nothing.
	h.typeAndSend("/model m1")
	if got := a.Released(); len(got) != 1 {
		t.Errorf("same model released %q", got)
	}

	// Leaving the backend releases its model there.
	h.typeAndSend("/backend b")
	if got := a.Released(); !slices.Equal(got, []string{"a-default", "m1"}) {
		t.Errorf("after /backend b: released %q", got)
	}
	// b has no default model, so there is nothing to release when leaving it.
	h.typeAndSend("/backend a")
	if got := b.Released(); len(got) != 0 {
		t.Errorf("b released %q", got)
	}
}

func TestRememberedBackendAndModel(t *testing.T) {
	a := &fake.Backend{Caps: backend.Capabilities{DefaultModel: "a-default"}}
	b := &fake.Backend{Caps: backend.Capabilities{DefaultModel: "b-default"}}
	var backends []string
	models := map[string]string{}
	opts := Options{
		Backend: "a",
		Models:  map[string]string{"a": "a1", "b": "b1"},
		RememberBackend: func(name string) error {
			backends = append(backends, name)
			return nil
		},
		RememberModel: func(backend, model string) error {
			models[backend] = model
			return nil
		},
	}
	h := newHarnessWith(t, map[string]*fake.Backend{"a": a, "b": b}, opts)
	if h.m.modelName != "a1" {
		t.Fatalf("starting model = %q, want the remembered a1", h.m.modelName)
	}

	h.typeAndSend("/model a2")
	h.typeAndSend("/backend b")
	if h.m.modelName != "b1" || !slices.Equal(backends, []string{"b"}) {
		t.Errorf("after /backend b: model %q, remembered backends %q", h.m.modelName, backends)
	}
	h.typeAndSend("/model default")
	if h.m.effectiveModel() != "b-default" || !slices.ContainsFunc(h.m.entries, func(e *entry) bool {
		return strings.Contains(e.text, "default model, b-default")
	}) {
		t.Errorf("/model default: model %q", h.m.effectiveModel())
	}
	// Back on a, the model chosen there earlier is used again.
	h.typeAndSend("/backend a")
	if h.m.modelName != "a2" {
		t.Errorf("back on a: model %q, want a2", h.m.modelName)
	}
	if want := map[string]string{"a": "a2", "b": ""}; !maps.Equal(models, want) {
		t.Errorf("remembered models %v, want %v", models, want)
	}

	// -m wins over the remembered model.
	opts.Model = "flag-model"
	h = newHarnessWith(t, map[string]*fake.Backend{"a": a, "b": b}, opts)
	if h.m.modelName != "flag-model" {
		t.Errorf("with -m: model %q", h.m.modelName)
	}
}

func TestFallbackBackend(t *testing.T) {
	ok := &fake.Backend{}
	h := newHarnessWith(t, map[string]*fake.Backend{"ok": ok}, Options{Backend: "gone", Fallback: "ok"})
	if h.m.backendName != "ok" || !strings.Contains(h.last(entryError).text, "Cannot open gone") {
		t.Errorf("backend %q", h.m.backendName)
	}
}
