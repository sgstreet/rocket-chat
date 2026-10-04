package tui

import (
	"strings"
	"testing"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/backend/fake"
)

// replyWith scripts a reply of text whose request the backend counted as
// contextTokens.
func replyWith(text string, contextTokens int) []backend.Event {
	return []backend.Event{
		{Kind: backend.EventTextDelta, Text: text},
		{Kind: backend.EventUsage, Usage: &backend.Usage{InputTokens: contextTokens, OutputTokens: 5, ContextTokens: contextTokens}},
		{Kind: backend.EventDone},
	}
}

func TestContextEstimatesThenMeasures(t *testing.T) {
	b := &fake.Backend{Window: 1000, Script: replyWith("twelve chars", 200)}
	h := newHarnessWith(t, map[string]*fake.Backend{"a": b}, Options{Backend: "a", System: strings.Repeat("s", 400)})

	// Before any reply: estimated from the text (400 characters of prompt).
	h.typeAndSend("/context")
	got := h.last(entryNotice).text
	if !strings.Contains(got, "Context: ~100 of 1,000 tokens (10%)") || !strings.Contains(got, "system prompt  ~100 tokens") {
		t.Errorf("before a reply:\n%s", got)
	}

	// After a reply: the backend's count plus the reply's estimate.
	h.typeAndSend("hello")
	h.typeAndSend("/context")
	got = h.last(entryNotice).text
	if !strings.Contains(got, "Context: 203 of 1,000 tokens (20%)") || !strings.Contains(got, "2 messages (1 questions, 1 replies)") {
		t.Errorf("after a reply:\n%s", got)
	}
	if strings.Contains(h.view(), "context 2") {
		t.Error("status warns at 20%")
	}
}

func TestContextWarnings(t *testing.T) {
	b := &fake.Backend{Window: 202, Script: replyWith("ok", 200)}
	h := newHarnessWith(t, map[string]*fake.Backend{"ollama": b}, Options{Backend: "ollama"})
	h.typeAndSend("hello")
	if !strings.Contains(h.view(), "context 99%") {
		t.Errorf("no status warning:\n%s", h.view())
	}
	h.typeAndSend("/context")
	if got := h.last(entryNotice).text; strings.Contains(got, "no longer fits") || !strings.Contains(got, "nearly fills") {
		t.Errorf("at 99%%:\n%s", got)
	}
	// A conversation larger than the window: Ollama cut it, so its count
	// is low, but the estimate from the text shows the overflow.
	h.m.entries[len(h.m.entries)-2].msg.Text = strings.Repeat("x", 2000)
	h.typeAndSend("/context")
	if got := h.last(entryNotice).text; !strings.Contains(got, "Ollama drops the start of it") || !strings.Contains(got, "~") {
		t.Errorf("no truncation warning:\n%s", got)
	}

	b2 := &fake.Backend{Window: 250, Script: replyWith("ok", 200)}
	h = newHarnessWith(t, map[string]*fake.Backend{"other": b2}, Options{Backend: "other"})
	h.typeAndSend("hello")
	h.typeAndSend("/context")
	if got := h.last(entryNotice).text; !strings.Contains(got, "nearly fills the window") || strings.Contains(got, "Ollama") {
		t.Errorf("near-full warning:\n%s", got)
	}
}

func TestContextWindowUnknown(t *testing.T) {
	h := newHarnessWith(t, map[string]*fake.Backend{"zai": {}}, Options{Backend: "zai"})
	h.typeAndSend("/context")
	if got := h.last(entryNotice).text; !strings.Contains(got, "window not known: set backends.zai.context_window") {
		t.Errorf("got:\n%s", got)
	}
}

func TestThousands(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1048576: "1,048,576"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q", n, got)
		}
	}
}
