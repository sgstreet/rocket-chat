// Package backend defines the interface every chat backend implements and
// the registry backends add themselves to.
package backend

import (
	"context"
	"iter"

	"github.com/sgstreet/rocket-chat/internal/chat"
)

// Backend is a source of chat completions.
type Backend interface {
	Name() string
	Capabilities() Capabilities
	Models(ctx context.Context) ([]ModelInfo, error)
	// Chat streams the reply to req. The sequence ends after an EventDone
	// event, or after yielding a non-nil error. Cancelling ctx stops the
	// stream and yields ctx.Err().
	Chat(ctx context.Context, req Request) iter.Seq2[Event, error]
}

// Capabilities advertises optional features so the UI can hide controls that
// do not apply.
type Capabilities struct {
	Thinking bool
	// WebSearch reports whether the backend can search the web.
	WebSearch bool
	// InlineCitations reports whether Grounding.Spans are filled in.
	InlineCitations bool
	// SearchSuggestions reports whether Grounding.Suggestions are filled in
	// and must be shown.
	SearchSuggestions bool
}

// ModelInfo describes a model a backend can use.
type ModelInfo struct {
	Name          string
	Description   string
	ContextLength int
}

// Request is one chat completion request.
type Request struct {
	Model    string
	System   string
	Messages []chat.Message
	// Search enables or disables web search for backends that support it.
	// When nil, the backend's configured default applies.
	Search *bool
	// Temperature overrides the model's default when non-nil.
	Temperature *float64
	// Think enables or disables reasoning output when non-nil.
	Think *bool
}

// EventKind identifies the type of an Event.
type EventKind int

const (
	// EventTextDelta carries more answer text in Text.
	EventTextDelta EventKind = iota + 1
	// EventThinkingDelta carries more reasoning text in Text.
	EventThinkingDelta
	// EventSearchStarted reports a web search for Query.
	EventSearchStarted
	// EventFetchStarted reports a page fetch of URL.
	EventFetchStarted
	// EventGrounding carries the sources behind the answer in Grounding.
	EventGrounding
	// EventUsage carries token counts in Usage.
	EventUsage
	// EventDone is always the last event of a successful stream.
	EventDone
)

func (k EventKind) String() string {
	switch k {
	case EventTextDelta:
		return "TextDelta"
	case EventThinkingDelta:
		return "ThinkingDelta"
	case EventSearchStarted:
		return "SearchStarted"
	case EventFetchStarted:
		return "FetchStarted"
	case EventGrounding:
		return "Grounding"
	case EventUsage:
		return "Usage"
	case EventDone:
		return "Done"
	}
	return "Unknown"
}

// Event is one item in a Chat stream.
type Event struct {
	Kind      EventKind
	Text      string
	Query     string
	URL       string
	Grounding *chat.Grounding
	Usage     *Usage
}

// Usage reports token counts for one reply.
type Usage struct {
	InputTokens  int
	OutputTokens int
	// SearchQueries is the number of web searches run, which some providers
	// bill for.
	SearchQueries int
}
