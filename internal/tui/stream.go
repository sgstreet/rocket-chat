package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/chat"
)

type entryKind int

const (
	entryUser entryKind = iota
	entryAssistant
	entryNotice
	entryError
)

// entry is one item in the transcript.
type entry struct {
	kind entryKind
	// msg is the conversation message for user and assistant entries.
	msg chat.Message
	// text is the message for notices and errors.
	text string
	// model is the model that answered, when known.
	model string
	// activity lists searches and page fetches while answering.
	activity []string
	usage    *backend.Usage
	elapsed  time.Duration
	done     bool
	// interrupted marks a reply stopped by the user.
	interrupted bool
	// err is set when the reply failed.
	err error

	// cache holds the rendered entry for a width and thinking setting.
	cache      string
	cacheWidth int
	cacheThink bool
}

// streamMsg carries one backend event to the UI.
type streamMsg struct {
	gen  int
	ev   backend.Event
	err  error
	end  bool
	next tea.Cmd
}

// ask sends the conversation to the backend and starts streaming the reply.
func (m *model) ask() tea.Cmd {
	req := backend.Request{
		Model:    m.modelName,
		System:   m.system,
		Search:   m.search,
		Messages: chat.ForBackend(m.history(), m.backendName),
	}
	reply := &entry{kind: entryAssistant, msg: chat.Message{Role: chat.RoleAssistant, Backend: m.backendName}, model: m.effectiveModel()}
	m.entries = append(m.entries, reply)

	ctx, cancel := context.WithCancel(m.ctx)
	m.gen++
	m.cancel = cancel
	m.streaming = true
	m.started = time.Now()
	m.refresh()

	gen, ch := m.gen, make(chan streamMsg, 64)
	go func() {
		defer close(ch)
		send := func(sm streamMsg) bool {
			select {
			case ch <- sm:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for ev, err := range m.b.Chat(ctx, req) {
			if !send(streamMsg{gen: gen, ev: ev, err: err}) || err != nil {
				return
			}
		}
		send(streamMsg{gen: gen, end: true})
	}()

	var wait tea.Cmd
	wait = func() tea.Msg {
		sm, ok := <-ch
		if !ok {
			return nil
		}
		sm.next = wait
		return sm
	}
	return tea.Batch(wait, m.spinner.Tick)
}

// history returns the finished conversation messages.
func (m *model) history() []chat.Message {
	var out []chat.Message
	for _, e := range m.entries {
		switch {
		case e.kind == entryUser:
			out = append(out, e.msg)
		case e.kind == entryAssistant && e.done && e.err == nil && e.msg.Text != "":
			out = append(out, e.msg)
		}
	}
	return out
}

// current returns the reply being streamed.
func (m *model) current() *entry {
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].kind == entryAssistant {
			return m.entries[i]
		}
	}
	return nil
}

func (m *model) handleStream(sm streamMsg) tea.Cmd {
	if sm.gen != m.gen || !m.streaming {
		return nil
	}
	e := m.current()
	switch {
	case sm.err != nil:
		e.err = sm.err
		m.finish(e)
		return nil
	case sm.end:
		m.finish(e)
		return nil
	}

	ev := sm.ev
	switch ev.Kind {
	case backend.EventTextDelta:
		e.msg.Text += ev.Text
	case backend.EventThinkingDelta:
		e.msg.Thinking += ev.Text
	case backend.EventSearchStarted:
		e.activity = append(e.activity, "Searching: "+ev.Query)
	case backend.EventFetchStarted:
		e.activity = append(e.activity, "Reading: "+ev.URL)
	case backend.EventGrounding:
		e.msg.Grounding = ev.Grounding
	case backend.EventUsage:
		e.usage = ev.Usage
	case backend.EventDone:
		m.finish(e)
		return nil
	}
	m.refresh()
	return sm.next
}

func (m *model) finish(e *entry) {
	e.done = true
	e.elapsed = time.Since(m.started)
	e.msg.Text = strings.TrimRight(e.msg.Text, "\n")
	m.streaming = false
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.refresh()
}

// cancelReply stops the reply being streamed and keeps what arrived.
func (m *model) cancelReply() {
	if !m.streaming {
		return
	}
	e := m.current()
	e.interrupted = true
	m.gen++ // ignore anything still in flight
	m.finish(e)
}

func sprintf(format string, args ...any) string {
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}
