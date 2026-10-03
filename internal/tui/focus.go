package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// focusArea is where key presses go.
type focusArea int

const (
	// focusInput sends keys to the input box (the default).
	focusInput focusArea = iota
	// focusTranscript makes the arrow and page keys scroll the
	// conversation. Clicking it gives it focus.
	focusTranscript
)

// setFocus moves the keyboard focus. Focusing the input box returns its
// cursor blink command.
func (m *model) setFocus(f focusArea) tea.Cmd {
	m.focus = f
	if f == focusTranscript {
		m.input.Blur()
		return nil
	}
	return m.input.Focus()
}

// transcriptKey handles a key while the conversation has focus. Keys it
// does not handle go on as usual; typing text moves the focus back to the
// input box, where the text lands.
func (m *model) transcriptKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "up":
		m.viewport.ScrollUp(1)
	case "down":
		m.viewport.ScrollDown(1)
	case "pgup":
		m.viewport.PageUp()
	case "pgdown":
		m.viewport.PageDown()
	case "home":
		m.viewport.GotoTop()
	case "end":
		m.viewport.GotoBottom()
	case "esc":
		if m.streaming {
			return nil, false // stops the answer
		}
		return m.setFocus(focusInput), true
	case "enter", "tab":
		return m.setFocus(focusInput), true
	case "ctrl+v":
		return tea.Batch(m.setFocus(focusInput), m.paste()), true
	case "ctrl+c", "ctrl+d", "ctrl+t", "shift+up", "shift+down":
		return nil, false
	default:
		if msg.Text != "" && msg.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
			// Typing goes to the input box: focus it and let the key
			// through.
			_ = m.setFocus(focusInput)
			return nil, false
		}
	}
	return nil, true
}

// inputTop is the screen row where the input box starts: below the
// transcript, the separator and the status bar.
func (m *model) inputTop() int { return m.viewport.Height() + 2 }

// placeCursor moves the input cursor to the clicked screen position: row
// is relative to the top of the input box, x is the screen column.
func (m *model) placeCursor(row, x int) {
	cur := func() int {
		c := m.input // a copy, to ask where the real cursor would be
		c.SetVirtualCursor(false)
		if p := c.Cursor(); p != nil {
			return p.Y
		}
		return row
	}
	// Move by screen rows, stopping if the cursor cannot go further.
	for y := cur(); y != row; {
		if y > row {
			m.input.CursorUp()
		} else {
			m.input.CursorDown()
		}
		next := cur()
		if next == y {
			break
		}
		y = next
	}
	li := m.input.LineInfo()
	col := max(0, x-lipgloss.Width(m.input.Prompt))
	if li.RowOffset < li.Height-1 {
		// Not the last row of a wrapped line: stay on this row.
		col = min(col, max(0, li.Width-1))
	}
	m.input.SetCursorColumn(li.StartColumn + col)
}
