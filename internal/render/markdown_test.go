package render

import (
	"testing"

	"github.com/sgstreet/rocket-chat/internal/chat"
)

func TestMarkdown(t *testing.T) {
	got := Markdown("Euro 2024", []chat.Message{
		{Role: chat.RoleUser, Text: "Who won?\n"},
		{Role: chat.RoleAssistant, Backend: "gemini", Model: "gemini-3.8-flash", Text: "Spain won.", Grounding: &chat.Grounding{
			Sources:     []chat.Source{{Title: "uefa [official]", URL: "https://u", Cited: true}, {URL: "https://w"}},
			Spans:       []chat.Span{{Start: 0, End: 10, SourceIndexes: []int{0}}},
			Queries:     []string{"euro 2024 winner"},
			Suggestions: &chat.Suggestions{Links: []chat.Link{{Text: "euro 2024", URL: "https://g/s"}, {Text: "final"}}},
		}},
		{Role: chat.RoleUser, Text: "Thanks"},
		{Role: chat.RoleAssistant, Text: "You're welcome."},
	})
	want := `# Euro 2024

## You

Who won?

## gemini/gemini-3.8-flash

Spain won.[1]

**Sources**

1. [uefa \[official\]](https://u)
2. [https://w](https://w) (consulted)

Searched: euro 2024 winner

Google Search suggestions: [euro 2024](https://g/s) · [final](https://www.google.com/search?q=final)

## You

Thanks

## Assistant

You're welcome.
`
	if got != want {
		t.Errorf("Markdown() =\n%s\nwant\n%s", got, want)
	}
}
