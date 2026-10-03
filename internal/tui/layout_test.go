package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/sgstreet/rocket-chat/internal/backend/fake"
)

// screen returns the view's rows without styling.
func screen(h *harness) []string {
	return strings.Split(h.view(), "\n")
}

func TestComposerLayout(t *testing.T) {
	h := newHarness(t, map[string]*fake.Backend{"fake": {}}, "fake") // 100x30
	rows := screen(h)
	if len(rows) != 30 {
		t.Fatalf("%d rows, want 30", len(rows))
	}
	vp := h.m.viewport.Height()
	if vp != 30-DefaultInputLines-composerChrome {
		t.Errorf("conversation height %d", vp)
	}
	if sep := rows[vp]; strings.Trim(sep, "─") != "" || len([]rune(sep)) != 100 {
		t.Errorf("row %d is not a full-width rule: %q", vp, sep)
	}
	if !strings.Contains(rows[vp+1], "fake · ") {
		t.Errorf("status bar not below the rule: %q", rows[vp+1])
	}
	if h.m.inputTop() != vp+2 || !strings.HasPrefix(rows[h.m.inputTop()], "│") {
		t.Errorf("input box not at row %d: %q", h.m.inputTop(), rows[h.m.inputTop()])
	}
}

func TestInputLines(t *testing.T) {
	h := newHarnessWith(t, map[string]*fake.Backend{"fake": {}}, Options{Backend: "fake", InputLines: 6})
	if h.m.input.Height() != 6 || h.m.viewport.Height() != 30-6-composerChrome {
		t.Fatalf("input %d, conversation %d", h.m.input.Height(), h.m.viewport.Height())
	}
	if rows := screen(h); len(rows) != 30 {
		t.Errorf("%d rows, want 30", len(rows))
	}

	h.typeAndSend("/lines 1")
	if h.m.input.Height() != 1 || h.m.viewport.Height() != 30-1-composerChrome || len(screen(h)) != 30 {
		t.Errorf("/lines 1: input %d, conversation %d", h.m.input.Height(), h.m.viewport.Height())
	}
	h.typeAndSend("/lines")
	if !strings.Contains(h.last(entryNotice).text, "shows 1 line.") {
		t.Errorf("/lines: %q", h.last(entryNotice).text)
	}
	for _, bad := range []string{"0", "21", "many"} {
		h.typeAndSend("/lines " + bad)
		if !strings.Contains(h.last(entryError).text, "from 1 to 20") {
			t.Errorf("/lines %s accepted", bad)
		}
	}

	// A short window keeps a line of conversation; the box grows back.
	h.typeAndSend("/lines 20")
	h.send(tea.WindowSizeMsg{Width: 100, Height: 10})
	if h.m.viewport.Height() != 1 || h.m.input.Height() != 10-composerChrome-1 || len(screen(h)) != 10 {
		t.Errorf("short window: conversation %d, input %d", h.m.viewport.Height(), h.m.input.Height())
	}
	h.send(tea.WindowSizeMsg{Width: 100, Height: 40})
	if h.m.input.Height() != 20 {
		t.Errorf("did not grow back: %d", h.m.input.Height())
	}

	// Out-of-range options fall back to the default.
	d := newHarnessWith(t, map[string]*fake.Backend{"fake": {}}, Options{Backend: "fake", InputLines: 99})
	if d.m.input.Height() != DefaultInputLines {
		t.Errorf("InputLines 99: %d", d.m.input.Height())
	}

	h.m.input.SetValue("/li")
	h.send(press("tab"))
	if h.m.input.Value() != "/lines " {
		t.Errorf("completion = %q", h.m.input.Value())
	}
}
