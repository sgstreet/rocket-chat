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

// Compaction replaces the older part of the conversation with a summary
// the model writes, so a long chat fits its context window again. The
// transcript keeps every message; only what is sent changes: the summary
// is added to the system prompt, followed by the messages after the
// compacted ones.

// DefaultCompactKeep is how many of the latest messages compaction keeps
// word for word.
const DefaultCompactKeep = 4

// summaryIntro introduces the summary in the system prompt.
const summaryIntro = "The earlier part of this conversation was summarized to save space:\n\n"

const compactSystem = "You summarize conversations so they can be continued without the full text."

const compactAsk = "Summarize the conversation so far, so that it can continue from your summary alone. " +
	"Keep the facts, decisions, names, numbers, code and open questions later answers may need, " +
	"and leave out pleasantries. Write compact notes, not a reply to the user."

// compactedMsg carries the summary of the messages before upTo.
type compactedMsg struct {
	gen        int
	upTo       int
	count      int
	summary    string
	restricted bool
	err        error
	// auto marks compaction started before sending a question, which is
	// sent once it ends.
	auto bool
}

// summarySent reports whether the summary goes to the backend in use.
func (m *model) summarySent() bool {
	return m.summary != "" && (!m.summaryRestricted || m.backendName == chat.GroundedBackend)
}

// requestSystem is the system prompt sent: the role's, plus the summary.
func (m *model) requestSystem() string {
	if !m.summarySent() {
		return m.system
	}
	if m.system == "" {
		return summaryIntro + m.summary
	}
	return m.system + "\n\n" + summaryIntro + m.summary
}

// requestMessages are the messages sent: those after the compacted ones,
// without what the backend may not receive.
func (m *model) requestMessages() []chat.Message {
	h := m.history()
	return chat.ForBackend(h[min(m.compacted, len(h)):], m.backendName)
}

func (m *model) compactKeep() int {
	if m.opts.CompactKeep > 0 {
		return m.opts.CompactKeep
	}
	return DefaultCompactKeep
}

// needsCompaction reports whether automatic compaction is on and the
// conversation has reached its share of the window.
func (m *model) needsCompaction() bool {
	if m.opts.AutoCompact <= 0 || m.noAutoCompact {
		return false
	}
	w := m.window()
	return w > 0 && float64(m.contextUse().tokens) >= m.opts.AutoCompact*float64(w)
}

// compact asks the model to summarize the messages before the last few,
// with focus added to the request. auto marks compaction before sending
// the question just typed, which is sent afterwards whatever happens.
func (m *model) compact(focus string, auto bool) tea.Cmd {
	h := m.history()
	split := len(h) - m.compactKeep()
	// The kept messages start with a question.
	for split > m.compacted && h[split].Role != chat.RoleUser {
		split--
	}
	if split <= m.compacted {
		if !auto {
			m.notice("Nothing to compact yet: compaction keeps the last %d messages as they are.", m.compactKeep())
			return nil
		}
		return m.ask()
	}
	part := h[m.compacted:split]

	msgs := chat.ForBackend(part, m.backendName)
	ask := compactAsk
	if focus = strings.TrimSpace(focus); focus != "" {
		ask += " Pay particular attention to: " + focus
	}
	msgs = append(msgs, chat.Message{Role: chat.RoleUser, Text: ask})
	system := compactSystem
	if m.summarySent() {
		system += "\n\nSummary of the conversation before these messages, to fold into yours:\n\n" + m.summary
	}
	off := false
	req := backend.Request{Model: m.modelName, System: system, Messages: msgs, Search: &off}
	// Summarizing grounded Gemini answers with Gemini gives a summary that
	// may only go back to Gemini.
	restricted := (m.summaryRestricted && m.summarySent()) ||
		(m.backendName == chat.GroundedBackend && len(chat.ForBackend(part, "")) != len(part))

	m.clearFollowups()
	ctx, cancel := context.WithCancel(m.ctx)
	m.gen++
	m.cancel = cancel
	m.streaming, m.compacting = true, true
	m.started = time.Now()
	m.notice("Compacting %d messages…", len(part))
	gen, b, count := m.gen, m.b, len(part)
	return tea.Batch(m.spinner.Tick, func() tea.Msg {
		var text strings.Builder
		var err error
		for ev, e := range b.Chat(ctx, req) {
			if e != nil {
				err = e
				break
			}
			if ev.Kind == backend.EventTextDelta {
				text.WriteString(ev.Text)
			}
		}
		return compactedMsg{gen: gen, upTo: split, count: count, summary: strings.TrimSpace(text.String()),
			restricted: restricted, err: err, auto: auto}
	})
}

// compacted applies a finished compaction.
func (m *model) gotCompaction(msg compactedMsg) tea.Cmd {
	if msg.gen != m.gen || !m.compacting {
		return nil
	}
	m.endCompaction()
	switch {
	case msg.err != nil:
		m.errorf("Cannot compact: %v", msg.err)
	case msg.summary == "":
		m.errorf("The model returned an empty summary; nothing was compacted.")
	default:
		m.summary, m.summaryRestricted, m.compacted = msg.summary, msg.restricted, msg.upTo
		m.markCompacted()
		m.notice("Compacted %d messages into a summary of ~%s tokens. They stay here, marked compacted; "+
			"the model now sees the summary in their place.", msg.count, thousands(estimateTokens(msg.summary)))
		m.save()
	}
	if msg.auto {
		return m.ask()
	}
	return nil
}

func (m *model) endCompaction() {
	m.streaming, m.compacting = false, false
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

// stopCompaction cancels compaction in progress.
func (m *model) stopCompaction() {
	m.gen++ // ignore the result
	m.endCompaction()
	// The question typed before automatic compaction is not answered;
	// /retry sends it without compacting first.
	m.noAutoCompact = true
	m.notice("Compaction stopped; nothing was compacted. /retry sends your last message as it is.")
}

// markCompacted flags the entries the summary replaces, and starts
// measuring the context afresh after them.
func (m *model) markCompacted() {
	n := 0
	for _, e := range m.entries {
		if !inHistory(e) {
			continue
		}
		if want := n < m.compacted; e.compacted != want {
			e.compacted, e.cache = want, ""
		}
		n++
	}
	m.measureFrom = len(m.entries)
	m.refresh()
}

// resetCompaction forgets the summary, as for a new chat.
func (m *model) resetCompaction() {
	m.summary, m.summaryRestricted, m.compacted, m.measureFrom = "", false, 0, 0
}

// summaryMarkdown is the summary section of an export, or "".
func (m *model) summaryMarkdown() string {
	if m.summary == "" {
		return ""
	}
	return fmt.Sprintf("\n---\n\n*The first %d messages were compacted; the model saw this summary in their place:*\n\n%s\n",
		m.compacted, m.summary)
}
