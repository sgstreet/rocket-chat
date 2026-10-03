package render

import (
	"testing"

	"github.com/sgstreet/rocket-chat/internal/chat"
)

func TestSources(t *testing.T) {
	tests := []struct {
		name string
		g    *chat.Grounding
		want string
	}{
		{name: "nil", g: nil, want: ""},
		{name: "empty", g: &chat.Grounding{}, want: ""},
		{
			name: "none marked cited lists all",
			g: &chat.Grounding{
				Sources: []chat.Source{
					{Title: "Go 1.26 release notes", URL: "https://go.dev/doc/go1.26"},
					{URL: "https://example.com/x"},
				},
				Queries: []string{"go 1.26 release"},
			},
			want: `Sources:
  [1] Go 1.26 release notes
      https://go.dev/doc/go1.26
  [2] https://example.com/x
Searched: "go 1.26 release"
`,
		},
		{
			name: "cited and consulted keep numbering",
			g: &chat.Grounding{Sources: []chat.Source{
				{Title: "A", URL: "https://a.example"},
				{Title: "B", URL: "https://b.example", Cited: true},
			}},
			want: `Sources:
  [2] B
      https://b.example
Also consulted:
  [1] A
      https://a.example
`,
		},
		{
			name: "suggestions",
			g: &chat.Grounding{Suggestions: &chat.Suggestions{Links: []chat.Link{
				{Text: "euro 2024 final"},
				{Text: "euro 2024 winner", URL: "https://www.google.com/search?q=euro+2024+winner&client=app"},
			}}},
			want: `Google Search suggestions:
  euro 2024 final  <https://www.google.com/search?q=euro+2024+final>
  euro 2024 winner  <https://www.google.com/search?q=euro+2024+winner&client=app>
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Sources(tt.g); got != tt.want {
				t.Errorf("Sources() =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestSourcesLinked(t *testing.T) {
	g := &chat.Grounding{
		Sources:     []chat.Source{{Title: "uefa.com", URL: "https://r/1", Cited: true}, {URL: "https://r/2"}},
		Suggestions: &chat.Suggestions{Links: []chat.Link{{Text: "euro 2024", URL: "https://g/s"}}},
	}
	link := func(text, url string) string { return "<" + text + "|" + url + ">" }
	want := `Sources:
  [1] <uefa.com|https://r/1>
Also consulted:
  [2] <https://r/2|https://r/2>
Google Search suggestions:
  <euro 2024|https://g/s>
`
	if got := SourcesLinked(g, link); got != want {
		t.Errorf("SourcesLinked() =\n%s\nwant\n%s", got, want)
	}
}
