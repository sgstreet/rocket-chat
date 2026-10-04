package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/chat"
)

// Follow-ups are questions the user might ask next, suggested under the
// latest reply. A small request in the background asks the model for them
// once the reply is done; they are never sent back or saved.

const maxFollowups = 3

// followupAnswerChars is how much of the reply the suggestion request
// quotes; the start of a long answer is enough to suggest from.
const followupAnswerChars = 4000

const followupSystem = "You suggest follow-up questions for a chat."

const followupAsk = "Suggest three short follow-up questions I might ask next about your last answer, " +
	"each under 15 words, written as I would type them. " +
	`Reply with only a JSON array of strings, like ["First?", "Second?", "Third?"].`

// followupPrefix starts each suggestion line, so a click can find it.
const followupPrefix = "↳ "

// followupsMsg carries suggestions for a reply.
type followupsMsg struct {
	gen       int
	reply     *entry
	followups []string
}

// startFollowups asks for suggestions after reply, when they are on.
func (m *model) startFollowups(reply *entry) tea.Cmd {
	m.clearFollowups()
	if !m.followupsOn || reply.err != nil || reply.interrupted || strings.TrimSpace(reply.msg.Text) == "" {
		return nil
	}
	var question string
	for _, e := range m.entries {
		if e.kind == entryUser {
			question = e.msg.Text
		}
	}
	if question == "" {
		return nil
	}
	answer := reply.msg.Text
	if utf8.RuneCountInString(answer) > followupAnswerChars {
		answer = string([]rune(answer)[:followupAnswerChars]) + "…"
	}
	off := false
	req := backend.Request{
		Model:  m.modelName,
		System: followupSystem,
		Messages: []chat.Message{
			{Role: chat.RoleUser, Text: question},
			{Role: chat.RoleAssistant, Text: answer},
			{Role: chat.RoleUser, Text: followupAsk},
		},
		Search: &off,
		Think:  &off,
	}
	if model := m.opts.FollowupModels[m.backendName]; model != "" {
		req.Model = model
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.followCancel = cancel
	m.followGen++
	gen, b := m.followGen, m.b
	return func() tea.Msg {
		defer cancel()
		var text strings.Builder
		for ev, err := range b.Chat(ctx, req) {
			if err != nil {
				return nil // suggestions are optional: no error is shown
			}
			if ev.Kind == backend.EventTextDelta {
				text.WriteString(ev.Text)
			}
		}
		return followupsMsg{gen: gen, reply: reply, followups: parseFollowups(text.String())}
	}
}

// gotFollowups shows suggestions that are still current.
func (m *model) gotFollowups(msg followupsMsg) {
	if msg.gen != m.followGen || len(msg.followups) == 0 {
		return
	}
	m.followups, m.followFor = msg.followups, msg.reply
	m.refresh()
}

// clearFollowups hides the suggestions and stops a request for them.
func (m *model) clearFollowups() {
	if m.followCancel != nil {
		m.followCancel()
		m.followCancel = nil
	}
	m.followGen++
	if m.followups != nil {
		m.followups, m.followFor = nil, nil
		m.refresh()
	}
}

// useFollowup puts suggestion n (from 1) in the input box.
func (m *model) useFollowup(n int) (tea.Cmd, bool) {
	if n < 1 || n > len(m.followups) || m.keyFor != "" {
		return nil, false
	}
	m.input.SetValue(m.followups[n-1])
	m.input.CursorEnd()
	return m.setFocus(focusInput), true
}

// followupAt returns the suggestion number on a rendered transcript line,
// or 0.
func (m *model) followupAt(line string) int {
	if len(m.followups) == 0 {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(ansi.Strip(line)), followupPrefix+"%d", &n); err != nil {
		return 0
	}
	return n
}

// renderFollowups is the suggestion block under a reply.
func (m *model) renderFollowups(width int) string {
	var b strings.Builder
	b.WriteString(st.dim.Render("Follow-ups (alt+1–" + fmt.Sprint(len(m.followups)) + " or click to edit and send):"))
	for i, f := range m.followups {
		b.WriteString("\n" + st.dim.Render(wrap(fmt.Sprintf("%s%d  %s", followupPrefix, i+1, f), width)))
	}
	return b.String()
}

// parseFollowups reads the model's suggestions: a JSON array of strings,
// or failing that one per line. Anything else gives none.
func parseFollowups(text string) []string {
	var list []string
	if i, j := strings.Index(text, "["), strings.LastIndex(text, "]"); i >= 0 && j > i {
		_ = json.Unmarshal([]byte(text[i:j+1]), &list)
	}
	if list == nil {
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*•0123456789.) "))
			if strings.HasSuffix(line, "?") {
				list = append(list, line)
			}
		}
	}
	var out []string
	for _, f := range list {
		f = strings.Join(strings.Fields(f), " ")
		if f == "" || utf8.RuneCountInString(f) > 200 {
			continue
		}
		if out = append(out, f); len(out) == maxFollowups {
			break
		}
	}
	return out
}

// setFollowups handles /followups.
func (m *model) setFollowups(arg string) {
	switch arg {
	case "on", "off":
		m.followupsOn = arg == "on"
		if !m.followupsOn {
			m.clearFollowups()
			m.notice("Follow-up suggestions are off.")
		} else {
			m.notice("Follow-up suggestions are on; they appear under the next reply.")
		}
	case "":
		state := "off"
		if m.followupsOn {
			state = "on"
		}
		m.notice("Follow-up suggestions are %s. Use /followups on or /followups off.", state)
	default:
		m.errorf("Use /followups on or /followups off.")
	}
}
