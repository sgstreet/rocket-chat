package tui

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"
)

// textPos is a position in the transcript: a line of the rendered content
// and a screen column.
type textPos struct{ line, col int }

func (p textPos) before(q textPos) bool {
	return p.line < q.line || (p.line == q.line && p.col < q.col)
}

// selection is text being or having been selected with the mouse.
type selection struct {
	anchor, head textPos
	// dragging is set while the button is held; moved once the mouse has
	// left the cell it was pressed on, which makes it a selection rather
	// than a click.
	dragging, moved bool
}

// span returns the selection's start and end (inclusive), in order.
func (s *selection) span() (textPos, textPos) {
	if s.head.before(s.anchor) {
		return s.head, s.anchor
	}
	return s.anchor, s.head
}

// contentPos converts a screen position in the transcript to a content
// position, clamping rows outside the transcript to its edges.
func (m *model) contentPos(x, y int) textPos {
	y = min(max(y, 0), m.viewport.Height()-1)
	return textPos{line: m.viewport.YOffset() + y, col: max(x, 0)}
}

// handleMouse handles clicks, drags and releases. The wheel is handled by
// the viewport.
func (m *model) handleMouse(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		m.flash = ""
		m.sel = nil
		switch {
		case msg.Button == tea.MouseMiddle:
			if m.keyFor == "" {
				return tea.Batch(m.setFocus(focusInput), m.paste())
			}
			return m.paste()
		case msg.Button != tea.MouseLeft:
			return nil
		case msg.Y < m.viewport.Height():
			// Focus moves to the conversation once this turns out to be
			// a drag, or a click that is not on a link.
			p := m.contentPos(msg.X, msg.Y)
			m.sel = &selection{anchor: p, head: p, dragging: true}
		case m.keyFor == "" && msg.Y >= m.inputTop() && msg.Y < m.inputTop()+inputHeight:
			cmd := m.setFocus(focusInput)
			m.placeCursor(msg.Y-m.inputTop(), msg.X)
			return cmd
		}

	case tea.MouseMotionMsg:
		if m.sel == nil || !m.sel.dragging {
			return nil
		}
		// Dragging past the top or bottom scrolls the transcript.
		switch {
		case msg.Y < 0 || (msg.Y == 0 && m.viewport.YOffset() > 0):
			m.viewport.ScrollUp(1)
		case msg.Y >= m.viewport.Height():
			m.viewport.ScrollDown(1)
		}
		p := m.contentPos(msg.X, msg.Y)
		if p != m.sel.head {
			m.sel.head, m.sel.moved = p, true
			if m.keyFor == "" {
				return m.setFocus(focusTranscript)
			}
		}

	case tea.MouseReleaseMsg:
		if m.sel == nil || !m.sel.dragging {
			return nil
		}
		m.sel.dragging = false
		if !m.sel.moved {
			// A click: open the link under the pointer, leaving the focus
			// where it is, or else focus the conversation.
			p := m.sel.anchor
			m.sel = nil
			if p.line < len(m.lines) {
				if link := linkAt(m.lines[p.line], p.col); link != "" {
					return m.openLink(link)
				}
			}
			if m.keyFor == "" {
				return m.setFocus(focusTranscript)
			}
			return nil
		}
		text := m.selectedText()
		if text == "" {
			m.sel = nil
			return nil
		}
		_ = clipboard.WriteAll(text) // best effort; OSC 52 below is the main route
		m.flash = fmt.Sprintf("copied %d characters", utf8.RuneCountInString(text))
		return tea.SetClipboard(text)
	}
	return nil
}

// selectedText returns the selected transcript text without styling, with
// trailing spaces trimmed from each line.
func (m *model) selectedText() string {
	if m.sel == nil {
		return ""
	}
	start, end := m.sel.span()
	var out []string
	for i := start.line; i <= end.line && i < len(m.lines); i++ {
		from, to := 0, ansi.StringWidth(m.lines[i])
		if i == start.line {
			from = start.col
		}
		if i == end.line {
			to = min(to, end.col+1)
		}
		if from >= to {
			out = append(out, "")
			continue
		}
		out = append(out, strings.TrimRight(ansi.Strip(ansi.Cut(m.lines[i], from, to)), " "))
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// viewportView renders the transcript with the selection highlighted.
func (m *model) viewportView() string {
	view := m.viewport.View()
	if m.sel == nil || !m.sel.moved {
		return view
	}
	start, end := m.sel.span()
	rows := strings.Split(view, "\n")
	for r, row := range rows {
		line := m.viewport.YOffset() + r
		if line < start.line || line > end.line {
			continue
		}
		width := ansi.StringWidth(row)
		from, to := 0, width
		if line == start.line {
			from = start.col
		}
		if line == end.line {
			to = min(width, end.col+1)
		}
		if from >= to {
			continue
		}
		rows[r] = ansi.Cut(row, 0, from) + selectedStyle.Render(ansi.Strip(ansi.Cut(row, from, to))) + ansi.Cut(row, to, width)
	}
	return strings.Join(rows, "\n")
}

var selectedStyle = lipgloss.NewStyle().Reverse(true)

// plainURL finds URLs that are not hyperlinks, such as in typed messages.
var plainURL = regexp.MustCompile(`(?:https?://|mailto:)[^\s<>"'` + "`" + `]+`)

// linkAt returns the link at screen column col of a rendered line: the
// target of an OSC 8 hyperlink there, or a URL written out in the text.
func linkAt(line string, col int) string {
	pos, link := 0, ""
	for i := 0; i < len(line); {
		if line[i] == 0x1b && i+1 < len(line) {
			if url, ok := osc8(line, i); ok {
				link = url
			}
			i = seqEnd(line, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		w := ansi.StringWidth(string(r))
		if link != "" && col >= pos && col < pos+w {
			return link
		}
		pos += w
		i += size
	}

	plain := ansi.Strip(line)
	for _, loc := range plainURL.FindAllStringIndex(plain, -1) {
		u := strings.TrimRight(plain[loc[0]:loc[1]], ".,;:!?)]}")
		start := ansi.StringWidth(plain[:loc[0]])
		if col >= start && col < start+ansi.StringWidth(u) {
			return u
		}
	}
	return ""
}

// oscEnd returns where the OSC body starting at i ends and where the text
// after its terminator starts.
func oscEnd(s string, i int) (end, next int) {
	for j := i; j < len(s); j++ {
		switch {
		case s[j] == '\a':
			return j, j + 1
		case s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\':
			return j, j + 2
		}
	}
	return len(s), len(s)
}

// openLink opens a clicked link in the browser. Only web and mail links
// are opened, since replies come from a model. Over SSH a browser would
// open on the wrong machine, so the link is copied instead.
func (m *model) openLink(link string) tea.Cmd {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "mailto") {
		m.flash = "not opening " + link
		return nil
	}
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		_ = clipboard.WriteAll(link)
		m.flash = "copied link (over SSH): " + link
		return tea.SetClipboard(link)
	}
	open := m.opts.OpenURL
	if open == nil {
		open = openBrowser
	}
	m.flash = "opening " + link
	return func() tea.Msg {
		if err := open(link); err != nil {
			return linkFailedMsg{link: link, err: err}
		}
		return nil
	}
}

// linkFailedMsg reports that a link could not be opened.
type linkFailedMsg struct {
	link string
	err  error
}

// openBrowser opens url with the system's handler.
func openBrowser(link string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", link)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", link)
	default:
		path, err := exec.LookPath("xdg-open")
		if err != nil {
			return errors.New("xdg-open not found")
		}
		cmd = exec.Command(path, link)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// paste inserts the clipboard: the system clipboard when there is one,
// otherwise the terminal's, through OSC 52 (which some terminals refuse).
func (m *model) paste() tea.Cmd {
	return func() tea.Msg {
		if s, err := clipboard.ReadAll(); err == nil && s != "" {
			return tea.PasteMsg{Content: s}
		}
		return tea.ReadClipboard()
	}
}
