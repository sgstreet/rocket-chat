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

func TestParseFollowups(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want []string
	}{
		{`["One?", "Two?", "Three?"]`, []string{"One?", "Two?", "Three?"}},
		{"```json\n[\"One?\",\n \"Two?\"]\n```", []string{"One?", "Two?"}},
		{`["a?", "b?", "c?", "d?"]`, []string{"a?", "b?", "c?"}},
		{"1. How does it work?\n2. Why  is it   so?\nThanks!", []string{"How does it work?", "Why is it so?"}},
		{"Sorry, I cannot help.", nil},
		{`["` + strings.Repeat("x", 201) + `", "ok?"]`, []string{"ok?"}},
	} {
		if got := parseFollowups(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("parseFollowups(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// followupBackend answers questions with "An answer." and suggestion
// requests with three suggestions.
func followupBackend() *fake.Backend {
	return &fake.Backend{Reply: func(req backend.Request) []backend.Event {
		text := "An answer."
		if req.System == followupSystem {
			text = `["First question?", "Second question?", "Third question?"]`
		}
		return []backend.Event{{Kind: backend.EventTextDelta, Text: text}, {Kind: backend.EventDone}}
	}}
}

func TestFollowups(t *testing.T) {
	b := followupBackend()
	h := newHarnessWith(t, map[string]*fake.Backend{"a": b}, Options{Backend: "a", Followups: true, Mouse: true,
		FollowupModels: map[string]string{"a": "small-model"}})
	h.typeAndSend("hello")

	reqs := b.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want the answer and the suggestions", len(reqs))
	}
	f := reqs[1]
	if f.Model != "small-model" || len(f.Messages) != 3 || f.Messages[0].Text != "hello" || f.Messages[1].Text != "An answer." ||
		*f.Search || *f.Think {
		t.Errorf("suggestion request = %+v", f)
	}
	view := h.view()
	if !strings.Contains(view, "Follow-ups (alt+1–3") || !strings.Contains(view, "↳ 2  Second question?") {
		t.Fatalf("no suggestions:\n%s", view)
	}

	// Alt+2 puts the second one in the input box.
	h.m.Update(tea.KeyPressMsg{Code: '2', Mod: tea.ModAlt})
	if got := h.m.input.Value(); got != "Second question?" {
		t.Errorf("alt+2: input %q", got)
	}
	// So does a click on its line, even from the conversation.
	h.m.input.Reset()
	h.m.setFocus(focusTranscript)
	row := rowOf(t, h, "↳ 3")
	h.m.Update(tea.MouseClickMsg{X: 4, Y: row, Button: tea.MouseLeft})
	if got := h.m.input.Value(); got != "Third question?" || h.m.focus != focusInput {
		t.Errorf("click: input %q, focus %v", got, h.m.focus)
	}

	// Asking something else replaces them.
	h.m.input.Reset()
	h.typeAndSend("next")
	if h.m.followFor == nil || h.m.followFor.msg.Text != "An answer." || strings.Count(h.view(), "Follow-ups (") != 1 {
		t.Errorf("suggestions after the next reply:\n%s", h.view())
	}

	// /followups off stops asking.
	h.typeAndSend("/followups off")
	before := len(b.Requests())
	h.typeAndSend("third")
	if n := len(b.Requests()) - before; n != 1 || strings.Contains(h.view(), "Follow-ups (") {
		t.Errorf("with follow-ups off: %d requests", n)
	}
}

func TestFollowupsQuietOnFailure(t *testing.T) {
	b := &fake.Backend{Reply: func(req backend.Request) []backend.Event {
		if req.System == followupSystem {
			return []backend.Event{{Kind: backend.EventTextDelta, Text: "I'd rather not."}, {Kind: backend.EventDone}}
		}
		return []backend.Event{{Kind: backend.EventTextDelta, Text: "An answer."}, {Kind: backend.EventDone}}
	}}
	h := newHarnessWith(t, map[string]*fake.Backend{"a": b}, Options{Backend: "a", Followups: true})
	h.typeAndSend("hello")
	if len(h.m.followups) != 0 || strings.Contains(h.view(), "Follow-ups") {
		t.Error("unusable suggestions shown")
	}

	failing := &fake.Backend{Script: []backend.Event{}, Err: errors.New("down")}
	h = newHarnessWith(t, map[string]*fake.Backend{"a": failing}, Options{Backend: "a", Followups: true})
	h.typeAndSend("hello")
	if len(failing.Requests()) != 1 {
		t.Errorf("suggestions asked for after a failed reply: %d requests", len(failing.Requests()))
	}
}

func TestStaleFollowupsIgnored(t *testing.T) {
	h := newHarnessWith(t, map[string]*fake.Backend{"a": followupBackend()}, Options{Backend: "a"})
	h.typeAndSend("hello")
	old := h.m.followGen
	h.m.clearFollowups() // as a new question would
	h.m.gotFollowups(followupsMsg{gen: old, reply: h.m.current(), followups: []string{"Stale?"}})
	if len(h.m.followups) != 0 {
		t.Error("stale suggestions shown")
	}
}
