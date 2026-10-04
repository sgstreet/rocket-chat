package tui

import (
	"errors"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"
)

// handleMouse handles clicks; the wheel is handled by the viewport. A
// click on a link opens it, leaving the focus where it is; any other click
// in the conversation focuses it, and a click in the input box places the
// cursor there.
func (m *model) handleMouse(msg tea.Msg) tea.Cmd {
	click, ok := msg.(tea.MouseClickMsg)
	if !ok || click.Button != tea.MouseLeft {
		return nil
	}
	m.flash = ""
	switch {
	case click.Y < m.viewport.Height():
		if line := m.viewport.YOffset() + click.Y; line < len(m.lines) {
			if link := linkAt(m.lines[line], click.X); link != "" {
				return m.openLink(link)
			}
		}
		if m.keyFor == "" {
			return m.setFocus(focusTranscript)
		}
	case m.keyFor == "" && click.Y >= m.inputTop() && click.Y < m.inputTop()+m.inputHeight():
		cmd := m.setFocus(focusInput)
		m.placeCursor(click.Y-m.inputTop(), click.X)
		return cmd
	}
	return nil
}

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
