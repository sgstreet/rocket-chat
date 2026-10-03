package render

import (
	"testing"

	"github.com/sgstreet/rocket-chat/internal/chat"
)

func TestCite(t *testing.T) {
	sources := []chat.Source{{URL: "a"}, {URL: "b"}, {URL: "c"}}
	tests := []struct {
		name  string
		text  string
		spans []chat.Span
		want  string
	}{
		{"no spans", "plain", nil, "plain"},
		{
			name:  "two spans, merged and sorted markers",
			text:  "Spain won. Final was 2-1.",
			spans: []chat.Span{{Start: 0, End: 10, SourceIndexes: []int{2, 0}}, {Start: 11, End: 25, SourceIndexes: []int{1}}, {Start: 0, End: 10, SourceIndexes: []int{0}}},
			want:  "Spain won.[1][3] Final was 2-1.[2]",
		},
		{
			name:  "multi-byte text keeps byte offsets",
			text:  "España ganó. Sí.",
			spans: []chat.Span{{Start: 0, End: 14, SourceIndexes: []int{0}}, {Start: 15, End: 19, SourceIndexes: []int{1}}},
			want:  "España ganó.[1] Sí.[2]",
		},
		{
			name:  "bad spans are skipped",
			text:  "héllo",
			spans: []chat.Span{{End: 2, SourceIndexes: []int{0}}, {End: 99, SourceIndexes: []int{0}}, {End: 6, SourceIndexes: []int{7}}},
			want:  "héllo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Cite(tt.text, &chat.Grounding{Sources: sources, Spans: tt.spans}); got != tt.want {
				t.Errorf("Cite() = %q, want %q", got, tt.want)
			}
		})
	}
}
