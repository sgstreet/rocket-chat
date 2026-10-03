// Package render formats chat content for display.
package render

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/sgstreet/rocket-chat/internal/chat"
)

// Sources formats grounding as plain text: the sources list, the search
// queries that were run, and any provider search suggestions. Source numbers
// match the [n] markers used in answers. It returns "" when there is nothing
// to show.
func Sources(g *chat.Grounding) string {
	if g == nil {
		return ""
	}
	var b strings.Builder

	anyCited := false
	for _, s := range g.Sources {
		anyCited = anyCited || s.Cited
	}
	writeSources(&b, "Sources", g.Sources, func(s chat.Source) bool { return !anyCited || s.Cited })
	if anyCited {
		writeSources(&b, "Also consulted", g.Sources, func(s chat.Source) bool { return !s.Cited })
	}

	if len(g.Queries) > 0 {
		quoted := make([]string, len(g.Queries))
		for i, q := range g.Queries {
			quoted[i] = strconv.Quote(q)
		}
		fmt.Fprintf(&b, "Searched: %s\n", strings.Join(quoted, ", "))
	}

	if s := g.Suggestions; s != nil && len(s.Queries) > 0 {
		b.WriteString("Google Search suggestions:\n")
		for _, q := range s.Queries {
			fmt.Fprintf(&b, "  %s  <%s>\n", q, GoogleSearchURL(q))
		}
	}
	return b.String()
}

func writeSources(b *strings.Builder, heading string, sources []chat.Source, include func(chat.Source) bool) {
	wrote := false
	for i, s := range sources {
		if !include(s) {
			continue
		}
		if !wrote {
			fmt.Fprintf(b, "%s:\n", heading)
			wrote = true
		}
		title := s.Title
		if title == "" {
			title = s.URL
		}
		fmt.Fprintf(b, "  [%d] %s\n", i+1, title)
		if s.URL != "" && s.URL != title {
			fmt.Fprintf(b, "      %s\n", s.URL)
		}
	}
}

// GoogleSearchURL returns the Google Search results URL for query.
func GoogleSearchURL(query string) string {
	return "https://www.google.com/search?q=" + url.QueryEscape(query)
}
