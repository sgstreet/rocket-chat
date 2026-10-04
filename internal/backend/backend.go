// Package backend defines the interface every chat backend implements and
// the registry backends add themselves to.
package backend

import (
	"context"
	"io"
	"iter"

	"github.com/sgstreet/rocket-chat/internal/chat"
)

// Backend is a source of chat completions.
type Backend interface {
	Name() string
	Capabilities() Capabilities
	// Models lists the models the backend can use. When only part of the
	// list could be fetched, it returns that part with a *PartialList
	// error.
	Models(ctx context.Context) ([]ModelInfo, error)
	// Chat streams the reply to req. The sequence ends after an EventDone
	// event, or after yielding a non-nil error. Cancelling ctx stops the
	// stream and yields ctx.Err().
	Chat(ctx context.Context, req Request) iter.Seq2[Event, error]
}

// Capabilities advertises optional features so the UI can hide controls that
// do not apply.
type Capabilities struct {
	// DefaultModel is the model used when the request names none, or ""
	// when the backend has no default.
	DefaultModel string
	Thinking     bool
	// WebSearch reports whether the backend can search the web.
	WebSearch bool
	// SearchByDefault reports whether web search is on when the request
	// leaves Search nil.
	SearchByDefault bool
	// InlineCitations reports whether Grounding.Spans are filled in.
	InlineCitations bool
	// SearchSuggestions reports whether Grounding.Suggestions are filled in
	// and must be shown.
	SearchSuggestions bool
}

// ModelReleaser is implemented by backends that hold resources for a
// model, such as a local server keeping it in memory.
type ModelReleaser interface {
	// ReleaseModel frees what the backend holds for model, which the chat
	// has stopped using. It reports whether anything was freed.
	ReleaseModel(ctx context.Context, model string) (bool, error)
}

// ContextWindower is implemented by backends that can tell how much
// conversation a model takes.
type ContextWindower interface {
	// ContextWindow returns the context window of model, or the backend's
	// default model when model is "", in tokens; 0 means not known.
	ContextWindow(ctx context.Context, model string) (int, error)
}

// PartialList is the error Models returns, along with the models it did
// list, when some could not be listed.
type PartialList struct {
	Err error
}

func (e *PartialList) Error() string { return e.Err.Error() }
func (e *PartialList) Unwrap() error { return e.Err }

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
	// EventNotice carries a message for the user in Text, such as a local
	// server being started.
	EventNotice
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
	case EventNotice:
		return "Notice"
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

// Close releases what a backend holds, such as a server it started, when
// the backend implements io.Closer.
func Close(b Backend) error {
	if c, ok := b.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// Usage reports token counts for one reply.
type Usage struct {
	InputTokens  int
	OutputTokens int
	// ContextTokens is the size of the conversation the model read for
	// this reply (system prompt and messages, without search results), as
	// the model counts it; 0 when not known.
	ContextTokens int
	// SearchQueries is the number of web searches run, which some providers
	// bill for.
	SearchQueries int
}
