// Package backendtest holds the contract tests every backend must pass.
package backendtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sgstreet/rocket-chat/internal/backend"
)

// streamTimeout bounds how long a stream may take to end after cancellation.
const streamTimeout = 5 * time.Second

// Run checks the stream contract of backend.Backend. newBackend must return a
// fresh backend whose reply to req has at least two events before EventDone.
func Run(t *testing.T, newBackend func(t *testing.T) backend.Backend, req backend.Request) {
	t.Helper()

	t.Run("Name", func(t *testing.T) {
		if newBackend(t).Name() == "" {
			t.Error("Name() is empty")
		}
	})

	t.Run("Models", func(t *testing.T) {
		if _, err := newBackend(t).Models(t.Context()); err != nil {
			t.Errorf("Models() error: %v", err)
		}
	})

	t.Run("DoneIsLast", func(t *testing.T) {
		var kinds []backend.EventKind
		for ev, err := range newBackend(t).Chat(t.Context(), req) {
			if err != nil {
				t.Fatalf("Chat() error after %v: %v", kinds, err)
			}
			if len(kinds) > 0 && kinds[len(kinds)-1] == backend.EventDone {
				t.Fatalf("event %v after Done", ev.Kind)
			}
			kinds = append(kinds, ev.Kind)
		}
		if len(kinds) == 0 || kinds[len(kinds)-1] != backend.EventDone {
			t.Fatalf("stream %v does not end with Done", kinds)
		}
	})

	t.Run("EarlyBreak", func(t *testing.T) {
		// The range-over-func runtime check panics if the backend keeps
		// yielding after the loop body stops.
		for range newBackend(t).Chat(t.Context(), req) {
			break
		}
	})

	t.Run("CancelledBeforeStart", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var gotErr error
		for ev, err := range newBackend(t).Chat(ctx, req) {
			if err != nil {
				gotErr = err
				break
			}
			if ev.Kind == backend.EventDone {
				t.Fatal("got Done from a cancelled context")
			}
		}
		if !errors.Is(gotErr, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", gotErr)
		}
	})

	t.Run("CancelMidStream", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		ended := make(chan error, 1)
		go func() {
			var streamErr error
			first := true
			for _, err := range newBackend(t).Chat(ctx, req) {
				if err != nil {
					streamErr = err
					break
				}
				if first {
					cancel()
					first = false
				}
			}
			ended <- streamErr
		}()
		select {
		case err := <-ended:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("error after cancel = %v, want context.Canceled", err)
			}
		case <-time.After(streamTimeout):
			t.Fatalf("stream did not end within %v of cancellation", streamTimeout)
		}
	})
}
