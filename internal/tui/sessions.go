package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sgstreet/rocket-chat/internal/chat"
	"github.com/sgstreet/rocket-chat/internal/render"
	"github.com/sgstreet/rocket-chat/internal/store"
)

// save writes the chat to the store, creating the session on first use.
func (m *model) save() {
	if m.opts.Store == nil {
		return
	}
	history := m.history()
	if len(history) == 0 {
		return
	}
	now := time.Now()
	if m.session == nil {
		m.session = &store.Session{ID: store.NewID(now), Created: now}
	}
	m.session.Updated = now
	m.session.Backend = m.backendName
	m.session.Model = m.modelName
	m.session.System = m.system
	m.session.Messages = history
	if err := m.opts.Store.Save(m.session); err != nil {
		if !m.saveFailed {
			m.errorf("Cannot save this chat: %v", err)
		}
		m.saveFailed = true
		return
	}
	m.saveFailed = false
}

// restore replaces the chat with a saved session.
func (m *model) restore(sess *store.Session) {
	m.entries = nil
	if sess.Backend != "" && sess.Backend != m.backendName {
		if b, err := m.opts.Open(sess.Backend); err != nil {
			m.errorf("Cannot open %s, staying on %s: %v", sess.Backend, m.backendName, err)
		} else {
			m.b, m.backendName = b, sess.Backend
		}
	}
	m.modelName, m.system, m.models = sess.Model, sess.System, nil
	for _, msg := range sess.Messages {
		kind := entryUser
		if msg.Role == chat.RoleAssistant {
			kind = entryAssistant
		}
		m.entries = append(m.entries, &entry{kind: kind, msg: msg, done: true})
	}
	m.session = sess
	m.notice("Resumed %q (%d messages, last updated %s).", sess.Title, len(sess.Messages), sess.Updated.Local().Format("2006-01-02 15:04"))
}

func (m *model) listSessions() {
	if m.opts.Store == nil {
		m.errorf("Saving sessions is turned off (sessions.save in the config).")
		return
	}
	list, err := m.opts.Store.List()
	if err != nil {
		m.errorf("Cannot list sessions: %v", err)
		return
	}
	if len(list) == 0 {
		m.notice("No saved sessions yet.")
		return
	}
	list = list[:min(len(list), 20)]
	m.sessions = list
	var b strings.Builder
	b.WriteString("Saved sessions (resume with /resume <number>):")
	for i, s := range list {
		marker := " "
		if m.session != nil && s.ID == m.session.ID {
			marker = "*"
		}
		fmt.Fprintf(&b, "\n %s %2d. %s  %-8s %s (%d messages)", marker, i+1,
			s.Updated.Local().Format("2006-01-02 15:04"), s.Backend, s.Title, s.Messages)
	}
	m.notice("%s", b.String())
}

func (m *model) resume(arg string) {
	if m.opts.Store == nil {
		m.errorf("Saving sessions is turned off (sessions.save in the config).")
		return
	}
	id := arg
	switch n, err := strconv.Atoi(arg); {
	case arg == "":
		id = "last"
	case err == nil:
		if n < 1 || n > len(m.sessions) {
			m.errorf("No session number %d; run /sessions to see the list.", n)
			return
		}
		id = m.sessions[n-1].ID
	}
	sess, err := m.opts.Store.Load(id)
	if err != nil {
		m.errorf("Cannot resume: %v", err)
		return
	}
	m.save()
	m.restore(sess)
}

func (m *model) export(path string) {
	history := m.history()
	if len(history) == 0 {
		m.errorf("Nothing to export yet.")
		return
	}
	if path == "" {
		id := store.NewID(time.Now())
		if m.session != nil {
			id = m.session.ID
		}
		path = "rocket-chat-" + id + ".md"
	}
	doc := render.Markdown(store.Title(history), history)
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		m.errorf("Cannot export: %v", err)
		return
	}
	m.notice("Exported %d messages to %s.", len(history), path)
}
