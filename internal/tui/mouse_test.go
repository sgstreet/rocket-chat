package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/sgstreet/rocket-chat/internal/backend/fake"
)

func TestLinkAt(t *testing.T) {
	// "See the Go site and https://x.io/a." with the Go site as an OSC 8
	// link (BEL-terminated) and the bare URL as one ended by ESC \.
	line := "\x1b[38;5;252mSee \x1b[m\x1b]8;id=1;https://go.dev/doc/\a\x1b[1mthe Go site\x1b[m\x1b]8;;\a and " +
		"\x1b]8;;https://x.io/a\x1b\\https://x.io/a\x1b]8;;\x1b\\. Also www and https://plain.example/p?q=1)."
	plain := ansi.Strip(line)
	col := func(s string) int { return ansi.StringWidth(plain[:strings.Index(plain, s)]) }

	tests := []struct {
		col  int
		want string
	}{
		{col("See"), ""},
		{col("the Go"), "https://go.dev/doc/"},
		{col("site") + 3, "https://go.dev/doc/"},
		{col(" and"), ""},
		{col("https://x.io"), "https://x.io/a"},
		{col(". Also"), ""},
		{col("https://plain") + 5, "https://plain.example/p?q=1"},
		{col(")."), ""},
	}
	for _, tt := range tests {
		if got := linkAt(line, tt.col); got != tt.want {
			t.Errorf("linkAt(col %d) = %q, want %q", tt.col, got, tt.want)
		}
	}
	// Wide characters count as two columns.
	if got := linkAt("界 https://a.b", 3); got != "https://a.b" {
		t.Errorf("after a wide rune: %q", got)
	}
}

func mouseHarness(t *testing.T, opened *[]string) *harness {
	t.Helper()
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	h := newHarnessWith(t, map[string]*fake.Backend{"fake": {}}, Options{
		Backend: "fake",
		Mouse:   true,
		OpenURL: func(u string) error {
			*opened = append(*opened, u)
			if strings.Contains(u, "fail") {
				return errors.New("no browser")
			}
			return nil
		},
	})
	h.m.entries = nil
	h.m.notice("alpha beta gamma")
	h.m.notice("see https://example.com/page now")
	h.m.notice("third line here")
	return h
}

// rowOf returns the screen row showing text.
func rowOf(t *testing.T, h *harness, text string) int {
	t.Helper()
	for i, l := range h.m.lines {
		if strings.Contains(ansi.Strip(l), text) {
			return i - h.m.viewport.YOffset()
		}
	}
	t.Fatalf("%q not on screen", text)
	return 0
}

func colOf(t *testing.T, h *harness, row int, text string) int {
	t.Helper()
	plain := ansi.Strip(h.m.lines[h.m.viewport.YOffset()+row])
	return ansi.StringWidth(plain[:strings.Index(plain, text)])
}

func TestDragSelectsAndCopies(t *testing.T) {
	var opened []string
	h := mouseHarness(t, &opened)
	r1, r3 := rowOf(t, h, "alpha"), rowOf(t, h, "third")
	x1 := colOf(t, h, r1, "beta")
	x3 := colOf(t, h, r3, "line") + 3

	h.send(tea.MouseClickMsg{X: x1, Y: r1, Button: tea.MouseLeft})
	h.send(tea.MouseMotionMsg{X: x3, Y: r3, Button: tea.MouseLeft})
	if v := h.m.viewportView(); !strings.Contains(v, "\x1b[7m") {
		t.Error("selection not highlighted")
	}
	var copied string
	_, cmd := h.m.Update(tea.MouseReleaseMsg{X: x3, Y: r3, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("no clipboard command")
	}
	copied = h.m.selectedText()
	if !strings.HasPrefix(copied, "beta gamma\n") || !strings.HasSuffix(copied, "third line") || !strings.Contains(copied, "https://example.com/page") {
		t.Errorf("copied %q", copied)
	}
	if !strings.Contains(h.view(), "copied ") {
		t.Errorf("no flash:\n%s", h.view())
	}
	if len(opened) != 0 {
		t.Error("a drag opened a link")
	}

	// Dragging backwards selects the same way.
	h.send(tea.MouseClickMsg{X: x3, Y: r3, Button: tea.MouseLeft})
	h.send(tea.MouseMotionMsg{X: x1, Y: r1, Button: tea.MouseLeft})
	if got := h.m.selectedText(); got != copied {
		t.Errorf("backwards selection %q", got)
	}
	// A key press clears the selection and the flash.
	h.m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if h.m.sel != nil || h.m.flash != "" {
		t.Error("selection kept after a key")
	}
}

func TestClickOpensLink(t *testing.T) {
	var opened []string
	h := mouseHarness(t, &opened)
	r := rowOf(t, h, "https://example.com")
	x := colOf(t, h, r, "example.com")

	h.send(tea.MouseClickMsg{X: x, Y: r, Button: tea.MouseLeft})
	h.send(tea.MouseReleaseMsg{X: x, Y: r, Button: tea.MouseLeft})
	if len(opened) != 1 || opened[0] != "https://example.com/page" || !strings.Contains(h.view(), "opening https://example.com/page") {
		t.Errorf("opened %q; view:\n%s", opened, h.view())
	}

	// A click off a link does nothing.
	h.send(tea.MouseClickMsg{X: 0, Y: rowOf(t, h, "alpha"), Button: tea.MouseLeft})
	h.send(tea.MouseReleaseMsg{X: 0, Y: rowOf(t, h, "alpha"), Button: tea.MouseLeft})
	if len(opened) != 1 {
		t.Errorf("opened %q", opened)
	}
}

func TestOpenLinkRules(t *testing.T) {
	var opened []string
	h := mouseHarness(t, &opened)
	for _, bad := range []string{"file:///etc/passwd", "javascript:alert(1)", "x-man-page://ls"} {
		if cmd := h.m.openLink(bad); cmd != nil || !strings.HasPrefix(h.m.flash, "not opening") {
			t.Errorf("%s: opened", bad)
		}
	}
	h.send(nil)
	if cmd := h.m.openLink("https://fail.example"); cmd != nil {
		h.send(cmd())
	}
	if !strings.Contains(h.m.flash, "could not open the link (no browser); copied it instead") {
		t.Errorf("flash %q", h.m.flash)
	}

	t.Setenv("SSH_CONNECTION", "1.2.3.4 22 5.6.7.8 22")
	opened = nil
	if cmd := h.m.openLink("https://example.com"); cmd == nil || len(opened) != 0 || !strings.Contains(h.m.flash, "over SSH") {
		t.Errorf("over SSH: opened %q, flash %q", opened, h.m.flash)
	}
}

func TestPasteFromTerminalClipboard(t *testing.T) {
	var opened []string
	h := mouseHarness(t, &opened)
	h.m.input.SetValue("say: ")
	h.m.input.CursorEnd()
	h.m.Update(tea.ClipboardMsg{Content: "two\nlines"})
	if got := h.m.input.Value(); got != "say: two\nlines" {
		t.Errorf("input = %q", got)
	}
	// In the key prompt, pasted text goes to the hidden field.
	h.m.opts.Keys = &memKeys{saved: map[string]string{}}
	h.typeAndSend("/key a")
	h.m.Update(tea.ClipboardMsg{Content: "sk-123"})
	if h.m.keyInput.Value() != "sk-123" || strings.Contains(h.view(), "sk-123") {
		t.Errorf("key prompt = %q", h.m.keyInput.Value())
	}
	// Ctrl+V and the middle button both ask for the clipboard.
	if _, handled := h.m.handleKey(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl}); !handled {
		t.Error("ctrl+v not handled")
	}
	if cmd := h.m.handleMouse(tea.MouseClickMsg{Button: tea.MouseMiddle}); cmd == nil {
		t.Error("middle click does not paste")
	}
}
