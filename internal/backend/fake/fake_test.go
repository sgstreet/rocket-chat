package fake_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/backend/backendtest"
	"github.com/sgstreet/rocket-chat/internal/backend/fake"
	"github.com/sgstreet/rocket-chat/internal/chat"
)

var helloReq = backend.Request{Messages: []chat.Message{{Role: chat.RoleUser, Text: "hello there world"}}}

func TestContract(t *testing.T) {
	backendtest.Run(t, func(*testing.T) backend.Backend { return &fake.Backend{} }, helloReq)
}

func TestContractWithDelay(t *testing.T) {
	backendtest.Run(t, func(*testing.T) backend.Backend {
		return &fake.Backend{Delay: time.Millisecond}
	}, helloReq)
}

func TestEcho(t *testing.T) {
	b := &fake.Backend{}
	var text strings.Builder
	for ev, err := range b.Chat(t.Context(), helloReq) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == backend.EventTextDelta {
			text.WriteString(ev.Text)
		}
	}
	if got, want := text.String(), "hello there world"; got != want {
		t.Errorf("echo = %q, want %q", got, want)
	}
	if got := b.Requests(); len(got) != 1 {
		t.Errorf("recorded %d requests, want 1", len(got))
	}
}

func TestScriptAndError(t *testing.T) {
	boom := errors.New("boom")
	b := &fake.Backend{
		Script: []backend.Event{{Kind: backend.EventSearchStarted, Query: "q"}},
		Err:    boom,
	}
	var kinds []backend.EventKind
	var gotErr error
	for ev, err := range b.Chat(t.Context(), helloReq) {
		if err != nil {
			gotErr = err
			break
		}
		kinds = append(kinds, ev.Kind)
	}
	if !slices.Equal(kinds, []backend.EventKind{backend.EventSearchStarted}) {
		t.Errorf("kinds = %v", kinds)
	}
	if !errors.Is(gotErr, boom) {
		t.Errorf("error = %v, want %v", gotErr, boom)
	}
}

func TestRegistered(t *testing.T) {
	if !slices.Contains(backend.Names(), fake.Name) {
		t.Fatalf("Names() = %v, missing %q", backend.Names(), fake.Name)
	}
	b, err := backend.Open(fake.Name, func(v any) error {
		v.(*fake.Settings).Delay = 5 * time.Millisecond
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := b.(*fake.Backend).Delay; got != 5*time.Millisecond {
		t.Errorf("Delay = %v, want 5ms", got)
	}
}
