package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/sgstreet/rocket-chat/internal/backend/fake"
)

func TestMarkdownToggle(t *testing.T) {
	h := newHarness(t, map[string]*fake.Backend{"fake": {}}, "fake")
	h.typeAndSend("# Plan\n\nSome **bold** words")
	reply := func() string {
		return ansi.Strip(h.m.renderEntry(h.m.current(), 80))
	}
	if r := reply(); strings.Contains(r, "**bold**") || strings.Contains(r, "# Plan") || !strings.Contains(r, "Some bold words") {
		t.Fatalf("rendered reply:\n%s", r)
	}

	h.typeAndSend("/markdown off")
	if r := reply(); !strings.Contains(r, "# Plan") || !strings.Contains(r, "Some **bold** words") {
		t.Errorf("plain reply:\n%s", r)
	}
	if !strings.Contains(h.view(), "**bold**") {
		t.Error("the transcript was not re-rendered")
	}
	h.typeAndSend("/markdown")
	if !strings.Contains(h.last(entryNotice).text, "Markdown rendering is off") {
		t.Error("/markdown status")
	}
	h.typeAndSend("/markdown on")
	if r := reply(); strings.Contains(r, "**bold**") {
		t.Errorf("back on:\n%s", r)
	}
	h.typeAndSend("/markdown loud")
	if !strings.Contains(h.last(entryError).text, "/markdown on or /markdown off") {
		t.Error("bad argument")
	}

	// The setting can start off.
	plain := newHarnessWith(t, map[string]*fake.Backend{"fake": {}}, Options{Backend: "fake", PlainReplies: true})
	plain.typeAndSend("Some **bold** words")
	if r := ansi.Strip(plain.m.renderEntry(plain.m.current(), 80)); !strings.Contains(r, "**bold**") {
		t.Errorf("PlainReplies:\n%s", r)
	}

	// Tab completes the command and its argument.
	h.m.input.SetValue("/mark")
	h.send(press("tab"))
	if h.m.input.Value() != "/markdown " {
		t.Errorf("completion = %q", h.m.input.Value())
	}
	h.m.input.SetValue("/markdown of")
	h.send(press("tab"))
	if h.m.input.Value() != "/markdown off " {
		t.Errorf("argument completion = %q", h.m.input.Value())
	}
}
