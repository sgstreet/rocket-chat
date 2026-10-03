package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"

	"github.com/sgstreet/rocket-chat/internal/render"
)

// codeBlocks returns the contents of the fenced code blocks in Markdown
// text. An unclosed block runs to the end of the text.
func codeBlocks(text string) []string {
	var (
		blocks []string
		cur    []string
		fence  string
	)
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case fence == "" && (strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")):
			fence, cur = trimmed[:3], nil
		case fence != "" && strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]) == "":
			blocks = append(blocks, strings.Join(cur, "\n"))
			fence = ""
		case fence != "":
			cur = append(cur, line)
		}
	}
	if fence != "" {
		blocks = append(blocks, strings.Join(cur, "\n"))
	}
	return blocks
}

// copyReply copies the last reply, or one of its code blocks, to the
// clipboard.
func (m *model) copyReply(arg string) tea.Cmd {
	text, what, ok := m.copyText(arg)
	if !ok {
		return nil
	}
	// OSC 52 reaches the local clipboard even over SSH; the system
	// clipboard is a best-effort extra for terminals without OSC 52.
	_ = clipboard.WriteAll(text)
	lines, unit := strings.Count(text, "\n")+1, "lines"
	if lines == 1 {
		unit = "line"
	}
	m.notice("Copied %s (%d %s).", what, lines, unit)
	return tea.SetClipboard(text)
}

// copyText picks what /copy copies: "" the whole last reply, "code" its
// last code block, a number that code block. It reports problems as
// errors in the transcript.
func (m *model) copyText(arg string) (text, what string, ok bool) {
	var last *entry
	for i := len(m.entries) - 1; i >= 0 && last == nil; i-- {
		if e := m.entries[i]; e.kind == entryAssistant && e.done && e.msg.Text != "" {
			last = e
		}
	}
	if last == nil {
		m.errorf("No reply to copy yet.")
		return "", "", false
	}
	if arg == "" {
		return render.Cite(last.msg.Text, last.msg.Grounding), "the last reply", true
	}
	blocks := codeBlocks(last.msg.Text)
	n := len(blocks)
	if arg != "code" {
		var err error
		if n, err = strconv.Atoi(arg); err != nil {
			m.errorf("Usage: /copy [code|number]")
			return "", "", false
		}
	}
	if n < 1 || n > len(blocks) {
		m.errorf("The last reply has %d code blocks.", len(blocks))
		return "", "", false
	}
	return blocks[n-1], "code block " + strconv.Itoa(n), true
}
