package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/sgstreet/rocket-chat/internal/backend/fake"
	"github.com/sgstreet/rocket-chat/internal/chat"
)

// click presses and releases the left button at x, y. It calls Update
// directly: focusing the input box starts a cursor blink that would never
// settle in the harness.
func click(h *harness, x, y int) {
	h.m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	h.m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
}

func TestClickFocusesConversation(t *testing.T) {
	store := &memHistory{entries: []string{"earlier"}}
	h := newHarnessWith(t, map[string]*fake.Backend{"fake": {}}, Options{Backend: "fake", Mouse: true, History: store})
	for i := range 60 {
		h.m.notice("line %d", i)
	}
	h.m.input.SetValue("draft")
	bottom := h.m.viewport.YOffset()

	click(h, 5, 3)
	if h.m.focus != focusTranscript || h.m.input.Focused() || !strings.Contains(h.view(), "conversation: ") {
		t.Fatalf("focus %v, input focused %v", h.m.focus, h.m.input.Focused())
	}
	// Up scrolls instead of recalling history.
	h.m.Update(press("up"))
	if h.m.viewport.YOffset() != bottom-1 || h.m.input.Value() != "draft" {
		t.Errorf("up: offset %d (bottom %d), input %q", h.m.viewport.YOffset(), bottom, h.m.input.Value())
	}
	h.m.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	if h.m.viewport.YOffset() != 0 {
		t.Errorf("home: offset %d", h.m.viewport.YOffset())
	}
	h.m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if h.m.viewport.YOffset() != bottom {
		t.Errorf("end: offset %d", h.m.viewport.YOffset())
	}
	// Enter only returns to the input; it does not send.
	h.m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if h.m.focus != focusInput || h.m.input.Value() != "draft" || !h.m.input.Focused() {
		t.Errorf("enter: focus %v, input %q", h.m.focus, h.m.input.Value())
	}

	// Typing returns to the input and the key is typed.
	click(h, 5, 3)
	h.m.input.CursorEnd()
	h.m.Update(tea.KeyPressMsg{Code: '!', Text: "!"})
	if h.m.focus != focusInput || h.m.input.Value() != "draft!" {
		t.Errorf("typing: focus %v, input %q", h.m.focus, h.m.input.Value())
	}

	// Esc returns too, but stops an answer first when one is streaming.
	click(h, 5, 3)
	h.m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.m.focus != focusInput {
		t.Error("esc did not return to the input")
	}
	click(h, 5, 3)
	h.m.entries = append(h.m.entries, &entry{kind: entryAssistant, msg: chat.Message{Role: chat.RoleAssistant, Text: "partial"}})
	h.m.streaming = true
	cancelled := false
	h.m.cancel = func() { cancelled = true }
	h.m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !cancelled || h.m.streaming || h.m.focus != focusTranscript {
		t.Errorf("esc while answering: cancelled %v, focus %v", cancelled, h.m.focus)
	}
}

func TestClickOnLinkKeepsFocus(t *testing.T) {
	var opened []string
	h := mouseHarness(t, &opened)
	r := rowOf(t, h, "https://example.com")
	x := colOf(t, h, r, "example.com")
	h.send(tea.MouseClickMsg{X: x, Y: r, Button: tea.MouseLeft})
	h.send(tea.MouseReleaseMsg{X: x, Y: r, Button: tea.MouseLeft})
	if len(opened) != 1 || h.m.focus != focusInput {
		t.Errorf("opened %q, focus %v", opened, h.m.focus)
	}
	// A drag focuses the conversation.
	h.m.Update(tea.MouseClickMsg{X: 0, Y: r, Button: tea.MouseLeft})
	h.m.Update(tea.MouseMotionMsg{X: 6, Y: r, Button: tea.MouseLeft})
	if h.m.focus != focusTranscript {
		t.Error("drag did not focus the conversation")
	}
}

func TestClickPlacesInputCursor(t *testing.T) {
	h := newHarnessWith(t, map[string]*fake.Backend{"fake": {}}, Options{Backend: "fake", Mouse: true})
	top := h.m.inputTop()
	prompt := len([]rune(h.m.input.Prompt))
	h.m.input.SetValue("hello world\nsecond line\nthird")

	click(h, 5, 3) // focus the conversation first
	click(h, prompt+3, top+1)
	if h.m.focus != focusInput || h.m.input.Line() != 1 || h.m.input.Column() != 3 {
		t.Errorf("row 1: focus %v, line %d col %d", h.m.focus, h.m.input.Line(), h.m.input.Column())
	}
	click(h, prompt+6, top)
	if h.m.input.Line() != 0 || h.m.input.Column() != 6 {
		t.Errorf("row 0: line %d col %d", h.m.input.Line(), h.m.input.Column())
	}
	click(h, prompt+50, top+2) // past the end of "third"
	if h.m.input.Line() != 2 || h.m.input.Column() != 5 {
		t.Errorf("past the end: line %d col %d", h.m.input.Line(), h.m.input.Column())
	}
	click(h, 0, top) // on the prompt
	if h.m.input.Line() != 0 || h.m.input.Column() != 0 {
		t.Errorf("on the prompt: line %d col %d", h.m.input.Line(), h.m.input.Column())
	}

	// A wrapped line: the second screen row is part of logical line 0.
	long := strings.Repeat("abcdefghij ", 14)
	h.m.input.SetValue(long)
	click(h, prompt+4, top+1)
	li := h.m.input.LineInfo()
	if h.m.input.Line() != 0 || li.RowOffset != 1 || h.m.input.Column() != li.StartColumn+4 {
		t.Errorf("wrapped: line %d row %d col %d (row starts at %d)", h.m.input.Line(), li.RowOffset, h.m.input.Column(), li.StartColumn)
	}
}

func TestMiddleClickPastesIntoInput(t *testing.T) {
	h := newHarnessWith(t, map[string]*fake.Backend{"fake": {}}, Options{Backend: "fake", Mouse: true})
	click(h, 5, 3)
	if cmd := h.m.handleMouse(tea.MouseClickMsg{X: 5, Y: 3, Button: tea.MouseMiddle}); cmd == nil || h.m.focus != focusInput {
		t.Errorf("middle click: focus %v", h.m.focus)
	}
}
