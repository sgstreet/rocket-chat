// Package fake provides a backend that replays scripted events or echoes the
// last user message. It is used by tests and for developing the UI without a
// real model.
package fake

import (
	"context"
	"iter"
	"strings"
	"sync"
	"time"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/chat"
)

// Name is the name the fake backend is registered under.
const Name = "fake"

func init() {
	backend.Register(Name, func(decode func(any) error) (backend.Backend, error) {
		var s Settings
		if err := decode(&s); err != nil {
			return nil, err
		}
		return &Backend{Delay: s.Delay}, nil
	})
}

// Settings is the fake backend's config section.
type Settings struct {
	// Delay is the pause before each event, to imitate a slow model.
	Delay time.Duration `yaml:"delay"`
}

// Backend is a fake chat backend. The zero value echoes the last user
// message one word at a time.
type Backend struct {
	// Script, when non-nil, is replayed instead of echoing. It should end
	// with an EventDone event.
	Script []backend.Event
	// Err, when non-nil, is yielded after Script.
	Err error
	// Delay is the pause before each event.
	Delay time.Duration
	// Caps is returned by Capabilities.
	Caps backend.Capabilities
	// ModelList is returned by Models. When nil, a single "echo" model is
	// returned.
	ModelList []backend.ModelInfo

	mu       sync.Mutex
	requests []backend.Request
}

func (b *Backend) Name() string { return Name }

func (b *Backend) Capabilities() backend.Capabilities { return b.Caps }

func (b *Backend) Models(ctx context.Context) ([]backend.ModelInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.ModelList != nil {
		return b.ModelList, nil
	}
	return []backend.ModelInfo{{Name: "echo", Description: "Repeats the last user message"}}, nil
}

// Requests returns the requests Chat has received so far.
func (b *Backend) Requests() []backend.Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]backend.Request(nil), b.requests...)
}

func (b *Backend) Chat(ctx context.Context, req backend.Request) iter.Seq2[backend.Event, error] {
	b.mu.Lock()
	b.requests = append(b.requests, req)
	b.mu.Unlock()

	events, finalErr := b.Script, b.Err
	if events == nil && finalErr == nil {
		events = echo(req)
	}

	return func(yield func(backend.Event, error) bool) {
		for _, ev := range events {
			if err := b.wait(ctx); err != nil {
				yield(backend.Event{}, err)
				return
			}
			if !yield(ev, nil) {
				return
			}
		}
		if finalErr != nil {
			yield(backend.Event{}, finalErr)
		}
	}
}

// wait pauses for Delay and reports whether ctx was cancelled.
func (b *Backend) wait(ctx context.Context) error {
	if b.Delay <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(b.Delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func echo(req backend.Request) []backend.Event {
	var last string
	for _, m := range req.Messages {
		if m.Role == chat.RoleUser {
			last = m.Text
		}
	}
	words := strings.Fields(last)
	events := make([]backend.Event, 0, len(words)+2)
	for i, w := range words {
		if i > 0 {
			w = " " + w
		}
		events = append(events, backend.Event{Kind: backend.EventTextDelta, Text: w})
	}
	events = append(events,
		backend.Event{Kind: backend.EventUsage, Usage: &backend.Usage{InputTokens: len(words), OutputTokens: len(words)}},
		backend.Event{Kind: backend.EventDone},
	)
	return events
}
