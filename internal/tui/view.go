package tui

import (
	"fmt"
	"strings"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"

	"github.com/sgstreet/rocket-chat/internal/render"
)

type styles struct {
	user, assistant, dim, err, status, statusKey, thinking, cursor lipgloss.Style
}

var st styles

func (m *model) setStyles() {
	dim := lipgloss.Color("245")
	accent := lipgloss.Color("39")
	if !m.dark {
		dim, accent = lipgloss.Color("242"), lipgloss.Color("25")
	}
	st = styles{
		user:      lipgloss.NewStyle().Bold(true).Foreground(accent),
		assistant: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("170")),
		dim:       lipgloss.NewStyle().Foreground(dim),
		err:       lipgloss.NewStyle().Foreground(lipgloss.Color("203")),
		status:    lipgloss.NewStyle().Foreground(dim),
		statusKey: lipgloss.NewStyle().Foreground(accent),
		thinking:  lipgloss.NewStyle().Foreground(dim).Italic(true),
		cursor:    lipgloss.NewStyle().Foreground(accent),
	}
	m.md.style = "dark"
	if !m.dark {
		m.md.style = "light"
	}
	m.md.r = nil
	for _, e := range m.entries {
		e.cache = ""
	}
}

// markdown renders Markdown for one width and style, recreating the
// renderer when either changes.
type markdown struct {
	r     *glamour.TermRenderer
	width int
	style string
}

func (md *markdown) render(text string, width int) string {
	if md.r == nil || md.width != width {
		r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(md.style), glamour.WithWordWrap(width))
		if err != nil {
			return wrap(text, width)
		}
		md.r, md.width = r, width
	}
	out, err := md.r.Render(text)
	if err != nil {
		return wrap(text, width)
	}
	return strings.Trim(out, "\n")
}

func wrap(text string, width int) string {
	return lipgloss.NewStyle().Width(width).Render(text)
}

// transcript renders every entry.
func (m *model) transcript() string {
	width := max(20, m.width-2)
	parts := make([]string, 0, len(m.entries))
	for _, e := range m.entries {
		parts = append(parts, m.renderEntry(e, width))
	}
	return strings.Join(parts, "\n\n")
}

func (m *model) renderEntry(e *entry, width int) string {
	if e.done && e.cache != "" && e.cacheWidth == width && e.cacheThink == m.showThinking {
		return e.cache
	}
	var out string
	switch e.kind {
	case entryUser:
		out = st.user.Render("You") + "\n" + wrap(e.msg.Text, width)
	case entryNotice:
		out = st.dim.Render(wrap(e.text, width))
	case entryError:
		out = st.err.Render(wrap(e.text, width))
	case entryAssistant:
		out = m.renderReply(e, width)
	}
	if e.done || e.kind != entryAssistant {
		e.cache, e.cacheWidth, e.cacheThink = out, width, m.showThinking
	}
	return out
}

func (m *model) renderReply(e *entry, width int) string {
	var b strings.Builder
	label := e.msg.Backend
	if e.msg.Model != "" {
		label += "/" + e.msg.Model
	}
	b.WriteString(st.assistant.Render(label))

	if t := strings.TrimSpace(e.msg.Thinking); t != "" {
		b.WriteString("\n")
		if m.showThinking {
			b.WriteString(st.thinking.Render(wrap(t, width)))
		} else {
			b.WriteString(st.dim.Render(fmt.Sprintf("▸ thinking (%d words, ctrl+t to show)", len(strings.Fields(t)))))
		}
	}
	for _, a := range e.activity {
		b.WriteString("\n" + st.dim.Render(a))
	}

	if text := e.msg.Text; text != "" {
		b.WriteString("\n")
		if e.done {
			b.WriteString(m.md.render(render.Cite(text, e.msg.Grounding), width))
		} else {
			b.WriteString(wrap(text, width) + st.cursor.Render("▍"))
		}
	} else if !e.done {
		b.WriteString("\n" + st.dim.Render(m.spinner.View()+" waiting for the model…"))
	}

	if s := strings.TrimRight(render.SourcesLinked(e.msg.Grounding, hyperlink), "\n"); s != "" && e.done {
		b.WriteString("\n\n" + st.dim.Render(s))
	}

	switch {
	case e.err != nil:
		b.WriteString("\n" + st.err.Render(wrap("Error: "+e.err.Error(), width)))
	case e.interrupted:
		b.WriteString("\n" + st.dim.Render("(stopped)"))
	}
	if e.done && e.err == nil {
		if s := replyStats(e); s != "" {
			b.WriteString("\n" + st.dim.Render(s))
		}
	}
	return b.String()
}

// hyperlink shows text as a clickable terminal link (OSC 8). Grounding URLs
// are often wider than the window, where the transcript would cut them.
func hyperlink(text, url string) string {
	return lipgloss.NewStyle().Hyperlink(url).Render(text + " ↗")
}

func replyStats(e *entry) string {
	var parts []string
	if u := e.usage; u != nil {
		parts = append(parts, fmt.Sprintf("%d in / %d out tokens", u.InputTokens, u.OutputTokens))
		if u.SearchQueries > 0 {
			parts = append(parts, fmt.Sprintf("%d searches", u.SearchQueries))
		}
	}
	if e.elapsed > 0 {
		parts = append(parts, fmt.Sprintf("%.1fs", e.elapsed.Seconds()))
	}
	return strings.Join(parts, " · ")
}

func (m *model) statusLine() string {
	modelName := m.effectiveModel()
	if modelName == "" {
		modelName = "default model"
	}
	parts := []string{st.statusKey.Render(m.backendName), modelName}
	if m.b.Capabilities().WebSearch {
		web := "web: off"
		if m.searchOn() {
			web = "web: on"
		}
		parts = append(parts, web)
	}
	switch r, ok := m.roles.Find(m.role); {
	case m.role != "" && ok:
		parts = append(parts, "role: "+r.Name)
	case m.system != "":
		parts = append(parts, "custom system prompt")
	}
	if m.streaming {
		parts = append(parts, fmt.Sprintf("%s answering… esc to stop", m.spinner.View()))
	}
	line := " " + strings.Join(parts, st.status.Render(" · "))
	return st.status.Width(m.width).MaxWidth(m.width).Render(line)
}

func (m *model) helpLine() string {
	help := " enter send · alt+enter newline · pgup/pgdn scroll · ctrl+t thinking · /help · ctrl+c quit"
	return st.dim.MaxWidth(m.width).Render(help)
}
