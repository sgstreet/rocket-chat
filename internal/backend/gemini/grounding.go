package gemini

import (
	"html"
	"regexp"
	"strings"

	"google.golang.org/genai"

	"github.com/sgstreet/rocket-chat/internal/chat"
)

// grounding converts Gemini's grounding metadata for answer into the shared
// form. It returns nil when there is nothing to show.
func grounding(answer string, md *genai.GroundingMetadata) *chat.Grounding {
	if md == nil {
		return nil
	}
	g := &chat.Grounding{Queries: md.WebSearchQueries}

	// Gemini numbers chunks; only web chunks become sources.
	sourceOf := map[int]int{}
	for i, ch := range md.GroundingChunks {
		if ch == nil || ch.Web == nil || ch.Web.URI == "" {
			continue
		}
		sourceOf[i] = len(g.Sources)
		g.Sources = append(g.Sources, chat.Source{Title: ch.Web.Title, URL: ch.Web.URI})
	}

	for _, s := range md.GroundingSupports {
		if s == nil || s.Segment == nil {
			continue
		}
		var idx []int
		for _, ci := range s.GroundingChunkIndices {
			if si, ok := sourceOf[int(ci)]; ok {
				idx = append(idx, si)
				g.Sources[si].Cited = true
			}
		}
		start, end, ok := locate(answer, s.Segment)
		if !ok || len(idx) == 0 {
			continue
		}
		g.Spans = append(g.Spans, chat.Span{Start: start, End: end, SourceIndexes: idx})
	}

	if ep := md.SearchEntryPoint; ep != nil && ep.RenderedContent != "" {
		g.Suggestions = &chat.Suggestions{Links: suggestionLinks(ep.RenderedContent), HTML: ep.RenderedContent}
	}

	if len(g.Sources) == 0 && len(g.Queries) == 0 && g.Suggestions == nil {
		return nil
	}
	return g
}

// locate finds a supported segment in answer. Segment offsets are bytes
// within one response Part, while answer joins every streamed part, so the
// offsets are trusted only when they select the segment's own text;
// otherwise the text is searched for.
func locate(answer string, seg *genai.Segment) (start, end int, ok bool) {
	start, end = int(seg.StartIndex), int(seg.EndIndex)
	inRange := 0 <= start && start < end && end <= len(answer)
	if seg.Text == "" {
		return start, end, inRange
	}
	if inRange && answer[start:end] == seg.Text {
		return start, end, true
	}
	if i := strings.Index(answer, seg.Text); i >= 0 {
		return i, i + len(seg.Text), true
	}
	return 0, 0, false
}

var (
	chipRE = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
	hrefRE = regexp.MustCompile(`(?i)\bhref\s*=\s*"([^"]*)"`)
	tagRE  = regexp.MustCompile(`<[^>]*>`)
)

// suggestionLinks extracts the suggestion chips from Google's Search
// Suggestions HTML.
func suggestionLinks(rendered string) []chat.Link {
	var links []chat.Link
	for _, m := range chipRE.FindAllStringSubmatch(rendered, -1) {
		if !strings.Contains(m[1], "chip") {
			continue
		}
		text := strings.Join(strings.Fields(html.UnescapeString(tagRE.ReplaceAllString(m[2], ""))), " ")
		if text == "" {
			continue
		}
		var href string
		if h := hrefRE.FindStringSubmatch(m[1]); h != nil {
			href = html.UnescapeString(h[1])
		}
		links = append(links, chat.Link{Text: text, URL: href})
	}
	return links
}
