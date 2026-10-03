package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/backend/fake"
)

func TestCodeBlocks(t *testing.T) {
	text := "Intro\n```go\nfmt.Println(1)\n\nx := 2\n```\nmiddle\n  ~~~\n  indented\n  ~~~\n```sh\nunclosed"
	want := []string{"fmt.Println(1)\n\nx := 2", "  indented", "unclosed"}
	if got := codeBlocks(text); !slices.Equal(got, want) {
		t.Errorf("codeBlocks() = %q, want %q", got, want)
	}
	if got := codeBlocks("no code"); got != nil {
		t.Errorf("got %q", got)
	}
}

func TestCopy(t *testing.T) {
	b := &fake.Backend{Script: []backend.Event{
		{Kind: backend.EventTextDelta, Text: "Two blocks:\n```go\nfirst()\n```\nand\n```\nsecond()\n```"},
		{Kind: backend.EventDone},
	}}
	h := newHarness(t, map[string]*fake.Backend{"fake": b}, "fake")
	h.typeAndSend("/copy")
	if !strings.Contains(h.last(entryError).text, "No reply to copy") {
		t.Error("copy with no reply")
	}
	h.typeAndSend("q")

	copied := func(arg string) string {
		t.Helper()
		text, _, _ := h.m.copyText(arg)
		return text
	}
	if got := copied(""); !strings.Contains(got, "Two blocks:") {
		t.Errorf("/copy = %q", got)
	}
	if got := copied("code"); got != "second()" {
		t.Errorf("/copy code = %q", got)
	}
	if got := copied("1"); got != "first()" {
		t.Errorf("/copy 1 = %q", got)
	}
	h.typeAndSend("/copy 1")
	if !strings.Contains(h.last(entryNotice).text, "Copied code block 1 (1 line)") {
		t.Errorf("notice = %q", h.last(entryNotice).text)
	}
	if copied("3"); !strings.Contains(h.last(entryError).text, "has 2 code blocks") {
		t.Error("/copy 3")
	}
	if copied("x"); !strings.Contains(h.last(entryError).text, "Usage: /copy") {
		t.Error("/copy x")
	}
}
