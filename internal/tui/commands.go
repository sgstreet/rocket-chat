package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/chat"
)

const helpText = `Commands:
  /backend [name]        show backends, or switch to one
  /model [name|number]   list models, or choose one (by name or list number)
  /system [text|clear]   show, set or clear the system prompt
  /search on|off|default turn web search on or off for this chat
  /thinking              show or hide the model's reasoning (also ctrl+t)
  /retry                 ask the last question again
  /new                   start a new conversation
  /sessions              list saved chats
  /resume [number|id]    continue a saved chat (default: the most recent)
  /export [file]         save this chat as Markdown
  /copy [code|number]    copy the last reply, its last code block, or code block N
  /quit                  leave (also ctrl+c, or ctrl+d on an empty line)

Keys: enter sends · alt+enter or ctrl+j adds a line · esc stops an answer · pgup/pgdn and shift+up/down scroll`

// modelsMsg carries the result of listing models for /model.
type modelsMsg struct {
	backend string
	models  []backend.ModelInfo
	err     error
}

// command runs a slash command.
func (m *model) command(line string) tea.Cmd {
	name, arg, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	arg = strings.TrimSpace(arg)
	if m.streaming && !slices.Contains([]string{"help", "thinking", "quit", "exit", "copy"}, name) {
		m.notice("Still answering; press Esc to stop it before /%s.", name)
		return nil
	}

	switch name {
	case "help", "?":
		m.notice(helpText)
	case "quit", "exit", "q":
		m.quitting = true
		return tea.Quit
	case "new", "clear":
		m.entries, m.session = nil, nil
		m.notice("New conversation with %s.", m.backendName)
	case "sessions":
		m.listSessions()
	case "resume":
		m.resume(arg)
	case "export":
		m.export(arg)
	case "copy":
		return m.copyReply(arg)
	case "thinking":
		m.showThinking = !m.showThinking
		m.refresh()
	case "backend":
		m.switchBackend(arg)
	case "model":
		return m.chooseModel(arg)
	case "system":
		m.setSystem(arg)
	case "search":
		m.setSearch(arg)
	case "retry":
		return m.retry()
	default:
		m.errorf("Unknown command /%s. Type /help for the list.", name)
	}
	return nil
}

func (m *model) switchBackend(name string) {
	if name == "" {
		var b strings.Builder
		b.WriteString("Backends:")
		for _, n := range m.opts.Backends {
			marker := "  "
			if n == m.backendName {
				marker = "* "
			}
			b.WriteString("\n  " + marker + n)
		}
		m.notice("%s", b.String())
		return
	}
	if name == m.backendName {
		m.notice("Already using %s.", name)
		return
	}
	b, err := m.opts.Open(name)
	if err != nil {
		m.errorf("Cannot switch to %s: %v", name, err)
		return
	}
	m.b, m.backendName, m.modelName, m.models = b, name, "", nil
	model := m.effectiveModel()
	if model == "" {
		model = "default model"
	}
	msg := fmt.Sprintf("Switched to %s (%s). The conversation so far is kept.", name, model)
	if name != chat.GroundedBackend && hasGrounded(m.entries) {
		msg += " Answers grounded with Google Search are not sent to other backends, so those turns are left out."
	}
	m.notice("%s", msg)
}

func hasGrounded(entries []*entry) bool {
	return slices.ContainsFunc(entries, func(e *entry) bool {
		return e.kind == entryAssistant && e.msg.Backend == chat.GroundedBackend && e.msg.Grounding != nil
	})
}

func (m *model) chooseModel(arg string) tea.Cmd {
	if arg == "" {
		b, name := m.b, m.backendName
		m.notice("Listing %s models…", name)
		return func() tea.Msg {
			models, err := b.Models(m.ctx)
			return modelsMsg{backend: name, models: models, err: err}
		}
	}
	if n, err := strconv.Atoi(arg); err == nil {
		if n < 1 || n > len(m.models) {
			m.errorf("No model number %d; run /model to see the list.", n)
			return nil
		}
		arg = m.models[n-1].Name
	}
	m.modelName = arg
	m.notice("Using model %s.", arg)
	return nil
}

func (m *model) showModels(msg modelsMsg) {
	if msg.backend != m.backendName {
		return
	}
	if msg.err != nil {
		m.errorf("Cannot list models: %v", msg.err)
		return
	}
	if len(msg.models) == 0 {
		m.notice("%s has no models available.", msg.backend)
		return
	}
	m.models = msg.models
	var b strings.Builder
	fmt.Fprintf(&b, "%s models (choose with /model <number> or /model <name>):", msg.backend)
	for i, md := range msg.models {
		marker := " "
		if md.Name == m.effectiveModel() {
			marker = "*"
		}
		fmt.Fprintf(&b, "\n %s %2d. %s", marker, i+1, md.Name)
		if md.Description != "" {
			b.WriteString("  " + md.Description)
		}
	}
	m.notice("%s", b.String())
}

func (m *model) setSystem(arg string) {
	switch arg {
	case "":
		if m.system == "" {
			m.notice("No system prompt. Set one with /system <text>.")
		} else {
			m.notice("System prompt: %s", m.system)
		}
	case "clear", "off":
		m.system = ""
		m.notice("System prompt cleared.")
	default:
		m.system = arg
		m.notice("System prompt set.")
	}
}

func (m *model) setSearch(arg string) {
	if !m.b.Capabilities().WebSearch {
		m.errorf("%s cannot search the web.", m.backendName)
		return
	}
	switch arg {
	case "on":
		on := true
		m.search = &on
	case "off":
		off := false
		m.search = &off
	case "default":
		m.search = nil
	case "":
	default:
		m.errorf("Usage: /search on|off|default")
		return
	}
	state := "off"
	if m.searchOn() {
		state = "on"
	}
	m.notice("Web search is %s.", state)
}

// retry removes the last reply and asks again.
func (m *model) retry() tea.Cmd {
	last := -1
	for i, e := range m.entries {
		if e.kind == entryUser {
			last = i
		}
	}
	if last < 0 {
		m.errorf("Nothing to retry.")
		return nil
	}
	m.entries = m.entries[:last+1]
	return m.ask()
}
