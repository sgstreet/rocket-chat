package tui

import (
	"cmp"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/chat"
)

// contextWarn is the share of the context window above which the status
// line warns that the conversation is nearly full.
const contextWarn = 0.8

// estimateTokens guesses a text's size in tokens: about four characters
// each, as in English prose.
func estimateTokens(s string) int {
	return (utf8.RuneCountInString(s) + 3) / 4
}

// windowKey names a model on a backend.
func windowKey(backendName, model string) string { return backendName + "\x00" + model }

// windowMsg carries a context window looked up for a model.
type windowMsg struct {
	backend, model string
	tokens         int
	err            error
	// show prints /context once the window is known.
	show bool
}

// fetchWindow looks up the context window of the model in use, when the
// backend can tell.
func (m *model) fetchWindow(show bool) tea.Cmd {
	w, ok := m.b.(backend.ContextWindower)
	if !ok {
		if show {
			m.showContext(windowMsg{backend: m.backendName, model: m.effectiveModel()})
		}
		return nil
	}
	name, model := m.backendName, m.effectiveModel()
	return func() tea.Msg {
		n, err := w.ContextWindow(m.ctx, model)
		return windowMsg{backend: name, model: model, tokens: n, err: err, show: show}
	}
}

func (m *model) gotWindow(msg windowMsg) {
	if msg.err == nil {
		m.windows[windowKey(msg.backend, msg.model)] = msg.tokens
	}
	if msg.show {
		m.showContext(msg)
	}
}

// window returns the known context window of the model in use, or 0.
func (m *model) window() int {
	return m.windows[windowKey(m.backendName, m.effectiveModel())]
}

// contextUse is how much of the context the next request would take.
type contextUse struct {
	tokens int
	// measured reports whether a backend count is the base of tokens,
	// rather than an estimate from the text.
	measured bool
	system   int
	// summary is the share of the compaction summary.
	summary  int
	messages []chat.Message
	largest  int // index in messages
}

// contextUse sizes the conversation the next request would send: the
// backend's count for the last reply on this backend and model, plus
// estimates for what came after it, or estimates throughout when there is
// no count. An estimate more than half again above the count wins,
// because a backend that cut a conversation too long for its window
// (Ollama does) counts only what it kept.
func (m *model) contextUse() contextUse {
	u := contextUse{system: estimateTokens(m.system), messages: m.requestMessages(), largest: -1}
	if m.summarySent() {
		u.summary = estimateTokens(m.requestSystem()) - u.system
	}
	largest := 0
	for i, msg := range u.messages {
		if n := estimateTokens(msg.Text); n > largest {
			largest, u.largest = n, i
		}
	}

	// The last finished reply from this backend and model with a count.
	model := m.effectiveModel()
	base := -1
	for i := len(m.entries) - 1; i >= m.measureFrom; i-- {
		e := m.entries[i]
		if e.kind == entryAssistant && e.done && e.err == nil && e.usage != nil && e.usage.ContextTokens > 0 &&
			e.msg.Backend == m.backendName && e.msg.Model == model {
			base = i
			break
		}
	}
	estimate := u.system + u.summary
	for _, msg := range u.messages {
		estimate += estimateTokens(msg.Text)
	}
	u.tokens = estimate
	if base < 0 {
		return u
	}
	measured := m.entries[base].usage.ContextTokens + estimateTokens(m.entries[base].msg.Text)
	for _, e := range m.entries[base+1:] {
		if (e.kind == entryUser || e.kind == entryAssistant) && e.msg.Text != "" {
			measured += estimateTokens(e.msg.Text)
		}
	}
	// Tokenizers differ from the estimate a little either way; an estimate
	// far above the count means the backend cut the conversation.
	if 2*estimate <= 3*measured {
		u.tokens, u.measured = measured, true
	}
	return u
}

// contextStatus is the status line's warning when the conversation nearly
// fills the window, or "".
func (m *model) contextStatus() string {
	w := m.window()
	if w <= 0 {
		return ""
	}
	share := float64(m.contextUse().tokens) / float64(w)
	if share < contextWarn {
		return ""
	}
	return st.err.Render(fmt.Sprintf("context %d%%", min(int(share*100), 999)))
}

// windowHints say how to learn a backend's context window when it is not
// known.
var windowHints = map[string]string{
	"ollama": "Ollama reports it once the model is loaded, after the first reply",
	"zai":    "set backends.zai.context_window",
}

// showContext prints /context: how much of the context window the
// conversation takes.
func (m *model) showContext(msg windowMsg) {
	u := m.contextUse()
	approx := "~"
	if u.measured {
		approx = ""
	}
	model := cmp.Or(msg.model, "the default model")
	var b strings.Builder
	switch w := m.windows[windowKey(msg.backend, msg.model)]; {
	case w > 0:
		fmt.Fprintf(&b, "Context: %s%s of %s tokens (%d%%) · %s on %s", approx, thousands(u.tokens), thousands(w),
			u.tokens*100/w, model, msg.backend)
	default:
		why := "the backend does not report it"
		switch {
		case msg.err != nil:
			why = "cannot look it up: " + msg.err.Error()
		case windowHints[msg.backend] != "":
			why = windowHints[msg.backend]
		}
		fmt.Fprintf(&b, "Context: %s%s tokens · %s on %s (window not known: %s)", approx, thousands(u.tokens), model, msg.backend, why)
	}

	role := "none"
	if r, ok := m.roles.Find(m.role); ok && m.role != "" {
		role = r.Name
	} else if m.system != "" {
		role = "custom"
	}
	fmt.Fprintf(&b, "\n  system prompt  ~%s tokens (%s)", thousands(u.system), role)
	switch {
	case u.summary > 0:
		fmt.Fprintf(&b, "\n  summary        ~%s tokens, in place of %d compacted messages", thousands(u.summary), m.compacted)
	case m.summary != "":
		fmt.Fprintf(&b, "\n  summary        not sent: it summarizes Google Search results, which only go to %s", chat.GroundedBackend)
	}
	var questions, replies int
	for _, msg := range u.messages {
		if msg.Role == chat.RoleUser {
			questions++
		} else {
			replies++
		}
	}
	fmt.Fprintf(&b, "\n  conversation   %d messages (%d questions, %d replies)", len(u.messages), questions, replies)
	if u.largest >= 0 {
		big := u.messages[u.largest]
		kind := "question"
		if big.Role == chat.RoleAssistant {
			kind = "reply"
		}
		fmt.Fprintf(&b, "\n  largest        %s %d of %d, ~%s tokens", kind, u.largest+1, len(u.messages), thousands(estimateTokens(big.Text)))
	}
	if !u.measured && len(u.messages) > 0 {
		b.WriteString("\n  (estimated from the text; the count is exact after the next reply)")
	}

	if w := m.windows[windowKey(msg.backend, msg.model)]; w > 0 {
		switch {
		case u.tokens >= w && msg.backend == "ollama":
			b.WriteString("\nThe conversation no longer fits the window, so Ollama drops the start of it and the model " +
				"does not see your earlier messages. /compact summarizes them to make room, or raise backends.ollama.num_ctx if the GPU has memory for it.")
		case u.tokens >= w:
			b.WriteString("\nThe conversation no longer fits the window; the backend may refuse it or drop the start. /compact summarizes the older messages to make room.")
		case float64(u.tokens) >= contextWarn*float64(w):
			b.WriteString("\nThe conversation nearly fills the window; /compact summarizes the older messages to make room.")
			if msg.backend == "ollama" {
				b.WriteString(" Raising backends.ollama.num_ctx gives Ollama more room if the GPU has memory for it.")
			}
		}
	}
	m.notice("%s", b.String())
}

// thousands formats n with comma separators.
func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
