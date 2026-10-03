package render

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sgstreet/rocket-chat/internal/chat"
)

// Cite returns text with [n] markers after each span in g.Spans, numbered
// like the Sources list. The text itself is not otherwise changed.
func Cite(text string, g *chat.Grounding) string {
	if g == nil || len(g.Spans) == 0 {
		return text
	}
	marks := map[int][]int{}
	for _, sp := range g.Spans {
		end := sp.End
		if end <= 0 || end > len(text) || (end < len(text) && !utf8.RuneStart(text[end])) {
			continue
		}
		for _, i := range sp.SourceIndexes {
			if i >= 0 && i < len(g.Sources) && !slices.Contains(marks[end], i) {
				marks[end] = append(marks[end], i)
			}
		}
	}
	// Insert from the end so earlier offsets stay valid.
	for _, end := range slices.Backward(slices.Sorted(maps.Keys(marks))) {
		idx := marks[end]
		if len(idx) == 0 {
			continue
		}
		slices.Sort(idx)
		var m strings.Builder
		for _, i := range idx {
			fmt.Fprintf(&m, "[%d]", i+1)
		}
		text = text[:end] + m.String() + text[end:]
	}
	return text
}
