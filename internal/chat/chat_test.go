package chat

import (
	"slices"
	"testing"
)

func TestForBackend(t *testing.T) {
	g := &Grounding{Queries: []string{"q"}}
	history := []Message{
		{Role: RoleUser, Text: "u1"},
		{Role: RoleAssistant, Text: "a1", Backend: "ollama"},
		{Role: RoleUser, Text: "u2"},
		{Role: RoleAssistant, Text: "a2", Backend: GroundedBackend, Grounding: g},
		{Role: RoleUser, Text: "u3"},
		{Role: RoleAssistant, Text: "a3", Backend: GroundedBackend},
		{Role: RoleUser, Text: "u4"},
	}
	texts := func(ms []Message) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Text)
		}
		return out
	}
	if got := texts(ForBackend(history, GroundedBackend)); len(got) != len(history) {
		t.Errorf("gemini history = %v, want all", got)
	}
	want := []string{"u1", "a1", "u3", "a3", "u4"}
	if got := texts(ForBackend(history, "ollama")); !slices.Equal(got, want) {
		t.Errorf("ollama history = %v, want %v", got, want)
	}
}
