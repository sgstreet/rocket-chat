package render

import (
	"fmt"
	"strings"

	"github.com/sgstreet/rocket-chat/internal/chat"
)

// Markdown renders a conversation as a Markdown document. Answers keep
// their citation markers, followed by their sources, queries and search
// suggestions.
func Markdown(title string, messages []chat.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", title)
	for _, m := range messages {
		switch m.Role {
		case chat.RoleUser:
			fmt.Fprintf(&b, "\n## You\n\n%s\n", strings.TrimSpace(m.Text))
		case chat.RoleAssistant:
			name := m.Backend
			if name == "" {
				name = "Assistant"
			}
			if m.Model != "" {
				name += "/" + m.Model
			}
			fmt.Fprintf(&b, "\n## %s\n\n%s\n", name, strings.TrimSpace(Cite(m.Text, m.Grounding)))
			writeGroundingMarkdown(&b, m.Grounding)
		}
	}
	return b.String()
}

func writeGroundingMarkdown(b *strings.Builder, g *chat.Grounding) {
	if g == nil {
		return
	}
	if len(g.Sources) > 0 {
		b.WriteString("\n**Sources**\n\n")
		for i, s := range g.Sources {
			title := s.Title
			if title == "" {
				title = s.URL
			}
			note := ""
			if !s.Cited {
				note = " (consulted)"
			}
			fmt.Fprintf(b, "%d. [%s](%s)%s\n", i+1, escapeLinkText(title), s.URL, note)
		}
	}
	if len(g.Queries) > 0 {
		fmt.Fprintf(b, "\nSearched: %s\n", strings.Join(g.Queries, "; "))
	}
	if s := g.Suggestions; s != nil && len(s.Links) > 0 {
		b.WriteString("\nGoogle Search suggestions: ")
		for i, l := range s.Links {
			if i > 0 {
				b.WriteString(" · ")
			}
			u := l.URL
			if u == "" {
				u = GoogleSearchURL(l.Text)
			}
			fmt.Fprintf(b, "[%s](%s)", escapeLinkText(l.Text), u)
		}
		b.WriteString("\n")
	}
}

func escapeLinkText(s string) string {
	return strings.NewReplacer("[", `\[`, "]", `\]`).Replace(s)
}
