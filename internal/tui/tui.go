// Package tui is rocket-chat's full-screen interactive chat.
package tui

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/chat"
	"github.com/sgstreet/rocket-chat/internal/store"
)

// Options configures the chat.
type Options struct {
	// Backend is the name of the backend to start with.
	Backend string
	// Model, System and Search are the initial settings; empty or nil
	// means the backend's default.
	Model  string
	System string
	Search *bool
	// Open creates a backend by name, for /backend.
	Open func(name string) (backend.Backend, error)
	// Backends lists the names /backend can switch to.
	Backends []string
	// Store saves the chat after every reply and serves /sessions and
	// /resume. Nil turns saving off.
	Store *store.Store
	// Resume, when set, is a saved session to continue.
	Resume *store.Session
}

// Run starts the chat and blocks until the user quits or ctx ends.
func Run(ctx context.Context, opts Options, programOpts ...tea.ProgramOption) error {
	m, err := newModel(ctx, opts)
	if err != nil {
		return err
	}
	p := tea.NewProgram(m, append([]tea.ProgramOption{tea.WithContext(ctx)}, programOpts...)...)
	_, err = p.Run()
	m.stop()
	return err
}

const inputHeight = 3

type model struct {
	ctx  context.Context
	opts Options

	backendName string
	b           backend.Backend
	modelName   string
	system      string
	search      *bool

	entries      []*entry
	showThinking bool
	// models is the last list shown by /model, for selecting by number.
	models []backend.ModelInfo
	// session is the saved form of this chat; nil until the first save.
	session *store.Session
	// sessions is the last list shown by /sessions.
	sessions []store.Summary
	// saveFailed stops repeating the same save error.
	saveFailed bool

	viewport viewport.Model
	input    textarea.Model
	spinner  spinner.Model
	width    int
	height   int
	dark     bool
	md       *markdown

	// Streaming state. gen identifies the current reply; events from
	// older replies are ignored.
	streaming bool
	cancel    context.CancelFunc
	gen       int
	started   time.Time
	quitting  bool
}

func newModel(ctx context.Context, opts Options) (*model, error) {
	b, err := opts.Open(opts.Backend)
	if err != nil {
		return nil, err
	}
	in := textarea.New()
	in.Placeholder = "Ask anything. Enter sends, Alt+Enter adds a line, /help lists commands."
	in.ShowLineNumbers = false
	in.Prompt = "│ "
	in.SetHeight(inputHeight)
	in.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j"))
	in.Focus()

	m := &model{
		ctx:         ctx,
		opts:        opts,
		backendName: opts.Backend,
		b:           b,
		modelName:   opts.Model,
		system:      opts.System,
		search:      opts.Search,
		viewport:    viewport.New(),
		input:       in,
		spinner:     spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		dark:        true,
		md:          &markdown{},
	}
	m.setStyles()
	if opts.Resume != nil {
		m.restore(opts.Resume)
	} else {
		m.notice("Connected to %s. Type /help for commands.", m.backendName)
	}
	return m, nil
}

func (m *model) stop() {
	if m.cancel != nil {
		m.cancel()
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, textarea.Blink)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		m.setStyles()
		m.refresh()
		return m, nil

	case streamMsg:
		return m, m.handleStream(msg)

	case modelsMsg:
		m.showModels(msg)
		return m, nil

	case spinner.TickMsg:
		if !m.streaming {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyPressMsg:
		if cmd, handled := m.handleKey(msg); handled {
			return m, cmd
		}

	case tea.PasteMsg:
		// Let the textarea take pasted text, newlines included.
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+c":
		if m.streaming {
			m.cancelReply()
			return nil, true
		}
		m.quitting = true
		return tea.Quit, true
	case "ctrl+d":
		if m.input.Value() == "" {
			m.quitting = true
			return tea.Quit, true
		}
	case "esc":
		if m.streaming {
			m.cancelReply()
		}
		return nil, true
	case "enter":
		return m.submit(), true
	case "pgup":
		m.viewport.PageUp()
		return nil, true
	case "pgdown":
		m.viewport.PageDown()
		return nil, true
	case "shift+up":
		m.viewport.ScrollUp(1)
		return nil, true
	case "shift+down":
		m.viewport.ScrollDown(1)
		return nil, true
	case "ctrl+t":
		m.showThinking = !m.showThinking
		m.refresh()
		return nil, true
	}
	return nil, false
}

// submit handles the text in the input box.
func (m *model) submit() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return nil
	}
	if strings.HasPrefix(text, "/") {
		m.input.Reset()
		return m.command(text)
	}
	if m.streaming {
		m.notice("Still answering; press Esc to stop it first.")
		return nil
	}
	m.input.Reset()
	m.entries = append(m.entries, &entry{kind: entryUser, msg: chat.Message{Role: chat.RoleUser, Text: text}})
	return m.ask()
}

func (m *model) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	v := tea.NewView(lipgloss.JoinVertical(lipgloss.Left,
		m.viewport.View(),
		m.statusLine(),
		m.input.View(),
		m.helpLine(),
	))
	v.AltScreen = true
	v.WindowTitle = "rocket-chat"
	return v
}

// layout sizes the components to the window.
func (m *model) layout() {
	if m.width == 0 {
		return
	}
	m.input.SetWidth(m.width)
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(max(1, m.height-inputHeight-2))
	m.refresh()
}

// refresh re-renders the transcript, staying at the bottom if the user was
// already there.
func (m *model) refresh() {
	if m.width == 0 {
		return
	}
	atBottom := m.viewport.AtBottom() || m.viewport.TotalLineCount() <= m.viewport.Height()
	m.viewport.SetContent(m.transcript())
	if atBottom {
		m.viewport.GotoBottom()
	}
}

func (m *model) notice(format string, args ...any) {
	m.entries = append(m.entries, &entry{kind: entryNotice, text: sprintf(format, args...)})
	m.refresh()
}

func (m *model) errorf(format string, args ...any) {
	m.entries = append(m.entries, &entry{kind: entryError, text: sprintf(format, args...)})
	m.refresh()
}

// effectiveModel is the model the next request will use, or "" when the
// backend chooses.
func (m *model) effectiveModel() string {
	if m.modelName != "" {
		return m.modelName
	}
	return m.b.Capabilities().DefaultModel
}

// searchOn reports whether web search is on for the next request.
func (m *model) searchOn() bool {
	if m.search != nil {
		return *m.search
	}
	return m.b.Capabilities().SearchByDefault
}
