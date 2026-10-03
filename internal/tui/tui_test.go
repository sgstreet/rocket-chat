package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/backend/fake"
	"github.com/sgstreet/rocket-chat/internal/chat"
)

// harness drives a model the way a Bubble Tea program would, running
// commands synchronously.
type harness struct {
	t        *testing.T
	m        *model
	backends map[string]*fake.Backend
}

func newHarness(t *testing.T, backends map[string]*fake.Backend, start string) *harness {
	t.Helper()
	return newHarnessWith(t, backends, Options{Backend: start})
}

func newHarnessWith(t *testing.T, backends map[string]*fake.Backend, opts Options) *harness {
	t.Helper()
	h := &harness{t: t, backends: backends}
	names := make([]string, 0, len(backends))
	for n := range backends {
		names = append(names, n)
	}
	slices.Sort(names)
	opts.Backends = names
	opts.Open = func(name string) (backend.Backend, error) {
		b, ok := backends[name]
		if !ok {
			return nil, fmt.Errorf("unknown backend %q", name)
		}
		return b, nil
	}
	m, err := newModel(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	h.m = m
	h.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	return h
}

// send delivers msg and runs the resulting commands until none are left.
func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	queue := []tea.Msg{msg}
	deadline := time.Now().Add(5 * time.Second)
	for len(queue) > 0 {
		if time.Now().After(deadline) {
			h.t.Fatal("commands did not settle")
		}
		msg, queue = queue[0], queue[1:]
		switch msg := msg.(type) {
		case nil, spinner.TickMsg:
			continue
		case tea.BatchMsg:
			for _, c := range msg {
				queue = append(queue, run(c))
			}
			continue
		}
		_, cmd := h.m.Update(msg)
		queue = append(queue, run(cmd))
	}
}

func run(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

func (h *harness) typeAndSend(text string) {
	h.t.Helper()
	h.m.input.SetValue(text)
	h.send(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// view returns the screen without styling.
func (h *harness) view() string {
	return ansi.Strip(h.m.View().Content)
}

func (h *harness) last(kind entryKind) *entry {
	for i := len(h.m.entries) - 1; i >= 0; i-- {
		if h.m.entries[i].kind == kind {
			return h.m.entries[i]
		}
	}
	h.t.Fatalf("no entry of kind %d", kind)
	return nil
}

func TestAskAndAnswer(t *testing.T) {
	echo := &fake.Backend{}
	h := newHarness(t, map[string]*fake.Backend{"fake": echo}, "fake")
	h.typeAndSend("hello there")

	reply := h.last(entryAssistant)
	if !reply.done || reply.msg.Text != "hello there" || reply.msg.Backend != "fake" {
		t.Errorf("reply = %+v", reply)
	}
	if h.m.streaming || h.m.input.Value() != "" {
		t.Errorf("streaming %v, input %q", h.m.streaming, h.m.input.Value())
	}

	h.typeAndSend("second")
	reqs := echo.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	var texts []string
	for _, m := range reqs[1].Messages {
		texts = append(texts, string(m.Role)+":"+m.Text)
	}
	if want := []string{"user:hello there", "assistant:hello there", "user:second"}; !slices.Equal(texts, want) {
		t.Errorf("history = %v, want %v", texts, want)
	}

	v := h.view()
	for _, want := range []string{"You", "hello there", "fake", "default model", "/help"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q", want)
		}
	}
}

func TestGroundedReplyRendering(t *testing.T) {
	b := &fake.Backend{
		Caps: backend.Capabilities{WebSearch: true, SearchByDefault: true, InlineCitations: true},
		Script: []backend.Event{
			{Kind: backend.EventThinkingDelta, Text: "let me look this up"},
			{Kind: backend.EventSearchStarted, Query: "euro 2024 winner"},
			{Kind: backend.EventTextDelta, Text: "Spain won **Euro 2024**."},
			{Kind: backend.EventGrounding, Grounding: &chat.Grounding{
				Sources: []chat.Source{{Title: "uefa.com", URL: "https://uefa.example/final", Cited: true}},
				Spans:   []chat.Span{{Start: 0, End: 24, SourceIndexes: []int{0}}},
				Queries: []string{"euro 2024 winner"},
				Suggestions: &chat.Suggestions{Links: []chat.Link{
					{Text: "euro 2024 winner", URL: "https://www.google.com/search?q=euro+2024+winner"},
				}},
			}},
			{Kind: backend.EventUsage, Usage: &backend.Usage{InputTokens: 10, OutputTokens: 5, SearchQueries: 1}},
			{Kind: backend.EventDone},
		},
	}
	h := newHarness(t, map[string]*fake.Backend{"gemini": b}, "gemini")
	h.typeAndSend("who won?")

	v := h.view()
	for _, want := range []string{
		"web: on",
		"Searching: euro 2024 winner",
		"▸ thinking (5 words",
		"Euro 2024",
		"[1]",
		"Sources:",
		"https://uefa.example/final",
		"Google Search suggestions:",
		"1 searches",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q\n%s", want, v)
		}
	}
	if strings.Contains(v, "**Euro") {
		t.Error("markdown not rendered")
	}

	h.send(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if v := h.view(); !strings.Contains(v, "let me look this up") {
		t.Error("ctrl+t did not show thinking")
	}
}

func TestCancel(t *testing.T) {
	slow := &fake.Backend{Delay: 200 * time.Millisecond}
	h := newHarness(t, map[string]*fake.Backend{"fake": slow}, "fake")
	h.m.input.SetValue("one two three four five")
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !h.m.streaming {
		t.Fatal("not streaming after enter")
	}
	h.send(tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.m.streaming {
		t.Fatal("still streaming after esc")
	}
	reply := h.last(entryAssistant)
	if !reply.interrupted || !reply.done {
		t.Errorf("reply = %+v", reply)
	}
	// Whatever the old stream still delivers is ignored.
	h.send(run(cmd))
	if h.last(entryAssistant).msg.Text != "" || !strings.Contains(h.view(), "(stopped)") {
		t.Errorf("stale events changed the reply: %+v", h.last(entryAssistant))
	}
}

func TestSubmitWhileStreaming(t *testing.T) {
	slow := &fake.Backend{Delay: time.Second}
	h := newHarness(t, map[string]*fake.Backend{"fake": slow}, "fake")
	h.m.input.SetValue("first")
	h.m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	h.m.input.SetValue("second")
	h.m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.m.input.Value() != "second" || !strings.Contains(h.last(entryNotice).text, "Still answering") {
		t.Errorf("second message should wait: input %q", h.m.input.Value())
	}
	h.m.cancelReply()
}

func TestBackendError(t *testing.T) {
	failing := &fake.Backend{Err: fmt.Errorf("model exploded")}
	h := newHarness(t, map[string]*fake.Backend{"fake": failing}, "fake")
	h.typeAndSend("q")
	if v := h.view(); !strings.Contains(v, "Error: model exploded") {
		t.Errorf("view missing error\n%s", v)
	}
	if len(h.m.history()) != 1 {
		t.Errorf("failed reply should not enter history: %+v", h.m.history())
	}
}

func TestCommands(t *testing.T) {
	a := &fake.Backend{
		Caps:      backend.Capabilities{WebSearch: true},
		ModelList: []backend.ModelInfo{{Name: "small", Description: "4B"}, {Name: "large"}},
	}
	other := &fake.Backend{}
	h := newHarness(t, map[string]*fake.Backend{"a": a, "other": other}, "a")

	h.typeAndSend("/model")
	if n := h.last(entryNotice).text; !strings.Contains(n, " 1. small  4B") || !strings.Contains(n, " 2. large") {
		t.Errorf("model list = %q", n)
	}
	h.typeAndSend("/model 2")
	if h.m.modelName != "large" {
		t.Errorf("model = %q", h.m.modelName)
	}
	h.typeAndSend("/model 9")
	if !strings.Contains(h.last(entryError).text, "No model number 9") {
		t.Error("bad model number accepted")
	}
	h.typeAndSend("/model custom:tag")
	if h.m.modelName != "custom:tag" {
		t.Errorf("model = %q", h.m.modelName)
	}

	h.typeAndSend("/search on")
	if !h.m.searchOn() || !strings.Contains(h.view(), "web: on") {
		t.Error("/search on")
	}
	h.typeAndSend("/search default")
	if h.m.search != nil || h.m.searchOn() {
		t.Error("/search default")
	}

	h.typeAndSend("/system be brief")
	h.typeAndSend("question")
	if r := a.Requests(); r[len(r)-1].System != "be brief" || r[len(r)-1].Model != "custom:tag" {
		t.Errorf("request = %+v", r[len(r)-1])
	}
	h.typeAndSend("/system clear")
	if h.m.system != "" {
		t.Error("/system clear")
	}

	h.typeAndSend("/retry")
	if r := a.Requests(); len(r) != 2 || r[1].Messages[len(r[1].Messages)-1].Text != "question" {
		t.Errorf("retry requests = %+v", r)
	}
	if n := len(h.m.history()); n != 2 {
		t.Errorf("history after retry has %d messages, want 2", n)
	}

	h.typeAndSend("/backend")
	if n := h.last(entryNotice).text; !strings.Contains(n, "* a") || !strings.Contains(n, "  other") {
		t.Errorf("backend list = %q", n)
	}
	h.typeAndSend("/backend nope")
	if !strings.Contains(h.last(entryError).text, "Cannot switch to nope") {
		t.Error("bad backend accepted")
	}
	h.typeAndSend("/backend other")
	if h.m.backendName != "other" || h.m.modelName != "" {
		t.Errorf("backend %q model %q", h.m.backendName, h.m.modelName)
	}
	h.typeAndSend("/search on")
	if !strings.Contains(h.last(entryError).text, "cannot search") {
		t.Error("/search on a backend without search")
	}

	h.typeAndSend("/new")
	if len(h.m.history()) != 0 {
		t.Error("/new kept history")
	}
	h.typeAndSend("/bogus")
	if !strings.Contains(h.last(entryError).text, "Unknown command /bogus") {
		t.Error("unknown command")
	}
	h.typeAndSend("/help")
	if !strings.Contains(h.last(entryNotice).text, "/backend [name]") {
		t.Error("/help")
	}
}

func TestGroundedTurnsStayWithGemini(t *testing.T) {
	gemini := &fake.Backend{Script: []backend.Event{
		{Kind: backend.EventTextDelta, Text: "grounded answer"},
		{Kind: backend.EventGrounding, Grounding: &chat.Grounding{Queries: []string{"q"}}},
		{Kind: backend.EventDone},
	}}
	ollama := &fake.Backend{}
	h := newHarness(t, map[string]*fake.Backend{chat.GroundedBackend: gemini, "ollama": ollama}, chat.GroundedBackend)
	h.typeAndSend("search this")
	h.typeAndSend("/backend ollama")
	if !strings.Contains(h.last(entryNotice).text, "not sent to other backends") {
		t.Errorf("switch notice = %q", h.last(entryNotice).text)
	}
	h.typeAndSend("follow up")
	msgs := ollama.Requests()[0].Messages
	if len(msgs) != 1 || msgs[0].Text != "follow up" {
		t.Errorf("ollama got %+v, want only the follow-up", msgs)
	}
}

func TestDefaultModelShown(t *testing.T) {
	b := &fake.Backend{Caps: backend.Capabilities{DefaultModel: "qwen3:4b"}}
	h := newHarness(t, map[string]*fake.Backend{"fake": b}, "fake")
	h.typeAndSend("hi")
	if v := h.view(); !strings.Contains(v, "fake · qwen3:4b") || !strings.Contains(v, "fake/qwen3:4b") {
		t.Errorf("view does not show the default model\n%s", v)
	}
}

func TestQuit(t *testing.T) {
	h := newHarness(t, map[string]*fake.Backend{"fake": {}}, "fake")
	_, cmd := h.m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if _, ok := run(cmd).(tea.QuitMsg); !ok {
		t.Error("ctrl+c did not quit")
	}
}
