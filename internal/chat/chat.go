// Package chat holds the conversation types shared by the UI, storage and
// every backend. It must not depend on any backend package.
package chat

// Role identifies who authored a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn in a conversation.
type Message struct {
	Role Role   `json:"role"`
	Text string `json:"text"`
	// Thinking is the model's reasoning output, when the model produces it.
	Thinking string `json:"thinking,omitempty"`
	// Backend is the name of the backend that produced an assistant message.
	Backend string `json:"backend,omitempty"`
	// Grounding describes the web sources behind an assistant message.
	Grounding *Grounding `json:"grounding,omitempty"`
}

// Grounding describes the web search behind an answer.
type Grounding struct {
	Sources []Source `json:"sources,omitempty"`
	// Spans link byte ranges of the answer text to sources. Only backends
	// with inline citations (Gemini) fill this in.
	Spans []Span `json:"spans,omitempty"`
	// Queries are the search queries that were run.
	Queries []string `json:"queries,omitempty"`
	// Suggestions are search suggestions the provider requires to be shown
	// alongside the answer (Gemini).
	Suggestions *Suggestions `json:"suggestions,omitempty"`
}

// Source is one web page consulted for an answer.
type Source struct {
	Title   string `json:"title,omitempty"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	// Cited reports whether the answer cites this source.
	Cited bool `json:"cited,omitempty"`
}

// Span attributes the answer bytes [Start, End) to Sources[i] for each i in
// SourceIndexes.
type Span struct {
	Start         int   `json:"start"`
	End           int   `json:"end"`
	SourceIndexes []int `json:"source_indexes"`
}

// Suggestions are provider-supplied search suggestions.
type Suggestions struct {
	// Links are the suggestions extracted for terminal display.
	Links []Link `json:"links,omitempty"`
	// HTML is the provider's original rendering.
	HTML string `json:"html,omitempty"`
}

// Link is a piece of text and the URL it points to.
type Link struct {
	Text string `json:"text"`
	URL  string `json:"url,omitempty"`
}

// GroundedBackend names the backend whose grounded answers may only be sent
// back to itself. The Gemini API terms do not allow Grounding with Google
// Search results to be mixed with other content, so they are never passed
// to another model.
const GroundedBackend = "gemini"

// ForBackend returns the history to send to the named backend. Grounded
// answers from GroundedBackend, and the user messages that asked for them,
// are left out when sending to any other backend.
func ForBackend(messages []Message, backend string) []Message {
	if backend == GroundedBackend {
		return messages
	}
	out := make([]Message, 0, len(messages))
	for i, m := range messages {
		if isRestricted(m) {
			continue
		}
		if m.Role == RoleUser && i+1 < len(messages) && isRestricted(messages[i+1]) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func isRestricted(m Message) bool {
	return m.Role == RoleAssistant && m.Backend == GroundedBackend && m.Grounding != nil
}
