package tui

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/chat"
)

const helpText = `Commands:
  /backend [name]        show backends, or switch to one; the choice is remembered
  /model [name|number]   list models, or choose one (by name or list number); the choice is
                         remembered for this backend · /model default goes back to its default
  /role                  show the prompt in use and list the roles
  /role <name>|off       switch to a role, or use no prompt
  /role custom <text>    use your own prompt for this chat
  /role show [name]      print a role's full prompt (default: the one in use)
  /search on|off|default turn web search on or off for this chat
  /key [backend [clear]] show API keys, or save one (typed hidden) or remove it
  /markdown on|off       render replies as Markdown, or show the model's text as it is
  /lines [n]             show or set how many lines the input box shows (1-20)
  /context               show how much of the model's context window the chat takes
  /thinking              show or hide the model's reasoning (also ctrl+t)
  /retry                 ask the last question again
  /new                   start a new conversation
  /sessions              list saved chats
  /resume [number|id]    continue a saved chat (default: the most recent)
  /export [file]         save this chat as Markdown
  /copy [code|number]    copy the last reply, its last code block, or code block N
  /quit                  leave (also ctrl+c, or ctrl+d on an empty line)

Keys: enter sends · alt+enter or ctrl+j adds a line · esc stops an answer
      up/down (or ctrl+p/ctrl+n) recall earlier inputs, which can be edited before sending
      tab completes commands and their arguments; press it again to cycle, shift+tab goes back
      pgup/pgdn, shift+up/down and the mouse wheel scroll · ctrl+t shows reasoning
Mouse: click a link to open it · /copy copies a reply or code block
      click the conversation to scroll it with the keys (up/down, pgup/pgdn, home/end);
      click the input box, press esc or start typing to go back to it
      your terminal's own selection and paste work with shift held (option on macOS)`

// modelsMsg carries the result of listing models for /model.
type modelsMsg struct {
	backend string
	models  []backend.ModelInfo
	err     error
	// forCompletion marks a list fetched for Tab completion, which is
	// stored without being shown.
	forCompletion bool
}

// command runs a slash command.
func (m *model) command(line string) (out tea.Cmd) {
	// A command that moves the chat to another model or backend lets the
	// backend free the model it leaves.
	prevBackend, prevName, prevModel := m.b, m.backendName, m.effectiveModel()
	defer func() {
		if m.b != prevBackend || m.effectiveModel() != prevModel {
			out = tea.Batch(out, m.releaseModel(prevBackend, prevName, prevModel))
		}
	}()

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
	case "role", "roles":
		m.setRole(arg)
	case "system":
		m.errorf("/system is now part of /role: /role shows the prompt in use, /role custom <text> sets your own, /role off clears it.")
	case "search":
		m.setSearch(arg)
	case "key", "keys":
		return m.keyCommand(arg)
	case "markdown":
		m.setMarkdown(arg)
	case "lines":
		m.setLines(arg)
	case "context":
		return m.fetchWindow(true)
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
	m.b, m.backendName, m.modelName, m.models = b, name, m.lastModels[name], nil
	if m.opts.RememberBackend != nil {
		if err := m.opts.RememberBackend(name); err != nil {
			m.errorf("The backend will not be remembered: %v", err)
		}
	}
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
	if arg == "default" {
		m.modelName = ""
		delete(m.lastModels, m.backendName)
		if def := m.effectiveModel(); def != "" {
			m.notice("Using %s's default model, %s.", m.backendName, def)
		} else {
			m.notice("Using %s's default model.", m.backendName)
		}
	} else {
		m.modelName = arg
		m.lastModels[m.backendName] = arg
		m.notice("Using model %s.", arg)
	}
	if m.opts.RememberModel != nil {
		if err := m.opts.RememberModel(m.backendName, m.modelName); err != nil {
			m.errorf("The model will not be remembered: %v", err)
		}
	}
	return nil
}

// releasedMsg reports the result of releasing a model the chat left.
type releasedMsg struct {
	backend, model string
	released       bool
	err            error
}

// releaseModel lets b free model, when it holds anything for it.
func (m *model) releaseModel(b backend.Backend, name, model string) tea.Cmd {
	r, ok := b.(backend.ModelReleaser)
	if !ok || model == "" {
		return nil
	}
	return func() tea.Msg {
		released, err := r.ReleaseModel(m.ctx, model)
		return releasedMsg{backend: name, model: model, released: released, err: err}
	}
}

func (m *model) showReleased(msg releasedMsg) {
	switch {
	case msg.err != nil:
		m.notice("Could not unload %s from %s: %v", msg.model, msg.backend, msg.err)
	case msg.released:
		m.notice("Unloaded %s from %s.", msg.model, msg.backend)
	}
}

func (m *model) showModels(msg modelsMsg) {
	if msg.backend != m.backendName {
		return
	}
	partial, isPartial := errors.AsType[*backend.PartialList](msg.err)
	if msg.err != nil && !isPartial {
		m.errorf("Cannot list models: %v", msg.err)
		return
	}
	if len(msg.models) == 0 && isPartial {
		m.errorf("%s has no models available; %v", msg.backend, partial)
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
	if isPartial {
		fmt.Fprintf(&b, "\n(%v)", partial)
	}
	m.notice("%s", b.String())
}

// writeRoles writes header and the role library, marking the current role.
func (m *model) writeRoles(b *strings.Builder, header string) {
	b.WriteString(header)
	for _, r := range m.roles.List() {
		marker := " "
		if r.ID == m.role {
			marker = "*"
		}
		fmt.Fprintf(b, "\n %s %-12s %s", marker, r.ID, r.Name)
		if r.Description != "" {
			b.WriteString(" — " + r.Description)
		}
	}
}

// setRole runs /role. A role is a named system prompt; "custom" is the
// user's own text, and "off" means no system prompt.
func (m *model) setRole(arg string) {
	cmd, rest, _ := strings.Cut(arg, " ")
	rest = strings.TrimSpace(rest)
	switch cmd {
	case "":
		var b strings.Builder
		m.writeCurrent(&b)
		b.WriteString("\n\n")
		m.writeRoles(&b, "Roles (/role <name> to switch, /role custom <text> for your own prompt, /role off for none):")
		m.notice("%s", b.String())
		return
	case "off", "none", "clear":
		m.useRole("", "")
		m.notice("No role; no system prompt.")
		return
	case "show":
		m.showRole(rest)
		return
	case "custom":
		if rest == "" {
			m.errorf("Usage: /role custom <prompt text>")
			return
		}
		m.useRole("", rest)
		m.notice("Using your own prompt for this chat. /role shows it.")
		return
	}
	r, ok := m.roles.Find(arg)
	if !ok {
		m.errorf("No role %q. Available: %s; or /role custom <text>.", arg, strings.Join(m.roles.Names(), ", "))
		return
	}
	m.useRole(r.ID, r.Prompt)
	msg := "Role: " + r.Name + "."
	if !m.searchSet {
		m.search = r.Search
		if r.Search != nil && m.b.Capabilities().WebSearch {
			state := "off"
			if *r.Search {
				state = "on"
			}
			msg += " Web search is " + state + "."
		}
	}
	m.notice("%s", msg)
}

// useRole sets the system prompt: a role's (id set) or custom text (id
// ""). A role's search setting stops applying unless the user chose one.
func (m *model) useRole(id, prompt string) {
	m.role, m.system = id, prompt
	if !m.searchSet {
		m.search = nil
	}
	if m.opts.RememberRole != nil {
		if err := m.opts.RememberRole(id, prompt); err != nil {
			m.errorf("The role will not be remembered: %v", err)
		}
	}
}

// writeCurrent describes the prompt in use, with its full text.
func (m *model) writeCurrent(b *strings.Builder) {
	switch r, ok := m.roles.Find(m.role); {
	case m.role != "" && ok:
		fmt.Fprintf(b, "Role: %s (%s)\n\n%s", r.Name, r.ID, m.system)
	case m.system != "":
		b.WriteString("Role: custom\n\n" + m.system)
	default:
		b.WriteString("No role: no system prompt.")
	}
}

// showRole prints a role's full prompt; name "" means the current role.
func (m *model) showRole(name string) {
	if name == "" {
		if m.role == "" {
			var b strings.Builder
			m.writeCurrent(&b)
			m.notice("%s", b.String())
			return
		}
		name = m.role
	}
	r, ok := m.roles.Find(name)
	if !ok {
		m.errorf("No role %q. Available: %s.", name, strings.Join(m.roles.Names(), ", "))
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s)", r.Name, r.ID)
	if r.Description != "" {
		b.WriteString(" — " + r.Description)
	}
	if r.Search != nil {
		state := "off"
		if *r.Search {
			state = "on"
		}
		b.WriteString("\nTurns web search " + state + ".")
	}
	b.WriteString("\n\n" + r.Prompt)
	m.notice("%s", b.String())
}

func (m *model) setLines(arg string) {
	if arg == "" {
		m.notice("The input box shows %s. Change it with /lines <n> (1-%d), or ui.input_lines in the config.", lines(m.inputLines), MaxInputLines)
		return
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 || n > MaxInputLines {
		m.errorf("Use /lines <n> with n from 1 to %d.", MaxInputLines)
		return
	}
	m.inputLines = n
	m.layout()
	msg := fmt.Sprintf("The input box shows %s.", lines(n))
	if h := m.inputHeight(); h < n {
		msg += fmt.Sprintf(" The window has room for %s; it grows when the window does.", lines(h))
	}
	m.notice("%s", msg)
}

func lines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

func (m *model) setMarkdown(arg string) {
	switch arg {
	case "on", "off":
		m.markdown = arg == "on"
		m.invalidate() // re-render the replies already shown
		m.refresh()
		if m.markdown {
			m.notice("Replies are rendered as Markdown.")
		} else {
			m.notice("Replies are shown as the model wrote them, without Markdown rendering.")
		}
	case "":
		state := "off"
		if m.markdown {
			state = "on"
		}
		m.notice("Markdown rendering is %s. Use /markdown on or /markdown off.", state)
	default:
		m.errorf("Use /markdown on or /markdown off.")
	}
}

func (m *model) setSearch(arg string) {
	if !m.b.Capabilities().WebSearch {
		m.errorf("%s cannot search the web.", m.backendName)
		return
	}
	switch arg {
	case "on", "off":
		m.searchSet = true
	case "default":
		m.searchSet = false
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
