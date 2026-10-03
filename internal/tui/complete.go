package tui

import (
	"slices"
	"strings"
)

// commands are the slash commands Tab completes, in the order /help lists
// them.
var commands = []string{
	"backend", "model", "role", "system", "search", "key", "mouse", "thinking",
	"retry", "new", "sessions", "resume", "export", "copy", "help", "quit",
}

// completion is the state of Tab completion between key presses: the text
// before the word being completed, the candidates, and which one is shown.
type completion struct {
	prefix string
	cands  []string
	i      int
	// shown is the input as completion left it; any other input starts
	// over.
	shown string
}

// complete handles Tab (dir 1) and Shift+Tab (dir -1) in a slash command.
// It reports false when the input is not a one-line slash command.
func (m *model) complete(dir int) bool {
	text := m.input.Value()
	if !strings.HasPrefix(text, "/") || strings.Contains(text, "\n") {
		return false
	}
	c := m.compl
	if c == nil || c.shown != text || len(c.cands) < 2 {
		c = m.newCompletion(text)
		if c == nil {
			m.complHint = "no completions"
			return true
		}
		m.compl = c
		if len(c.cands) == 1 {
			m.setCompletion(c.prefix + c.cands[0] + " ")
			m.compl, m.complHint = nil, ""
			return true
		}
		// First Tab: extend to what all candidates share, if that adds
		// anything; otherwise start cycling.
		word := strings.TrimPrefix(text, c.prefix)
		if common := commonPrefix(c.cands); len(common) > len(word) {
			c.i = -1
			m.setCompletion(c.prefix + common)
			m.complHint = strings.Join(c.cands, "  ")
			return true
		}
		c.i = -1
	}
	if c.i < 0 && dir < 0 {
		c.i = 0 // so Shift+Tab starts from the last candidate
	}
	c.i = (c.i + dir + len(c.cands)) % len(c.cands)
	m.setCompletion(c.prefix + c.cands[c.i])
	m.complHint = hintWithCurrent(c.cands, c.i)
	return true
}

func (m *model) setCompletion(text string) {
	m.input.SetValue(text)
	m.input.CursorEnd()
	if m.compl != nil {
		m.compl.shown = text
	}
}

// newCompletion finds the candidates for the last word of text, or nil.
func (m *model) newCompletion(text string) *completion {
	body := strings.TrimPrefix(text, "/")
	fields := strings.Fields(body)
	if strings.HasSuffix(body, " ") || len(fields) == 0 {
		fields = append(fields, "")
	}
	word := fields[len(fields)-1]
	prefix := text[:len(text)-len(word)]

	var options []string
	if len(fields) == 1 {
		options = commands
	} else {
		options = m.argOptions(fields[0], fields[1:len(fields)-1])
	}
	var cands []string
	for _, o := range options {
		if strings.HasPrefix(strings.ToLower(o), strings.ToLower(word)) && !slices.Contains(cands, o) {
			cands = append(cands, o)
		}
	}
	if len(cands) == 0 {
		return nil
	}
	return &completion{prefix: prefix, cands: cands}
}

// argOptions lists the values a command's next argument can take, given
// the arguments already typed.
func (m *model) argOptions(cmd string, before []string) []string {
	switch {
	case len(before) == 0:
		switch cmd {
		case "backend":
			return m.opts.Backends
		case "model":
			names := make([]string, len(m.models))
			for i, md := range m.models {
				names[i] = md.Name
			}
			return names
		case "role", "roles":
			return append([]string{"show", "off"}, m.roles.Names()...)
		case "system":
			return []string{"clear"}
		case "search":
			return []string{"on", "off", "default"}
		case "key", "keys":
			if m.opts.Keys != nil {
				return m.opts.Keys.Names()
			}
		case "mouse":
			return []string{"on", "off"}
		case "copy":
			return []string{"code"}
		}
	case len(before) == 1:
		switch {
		case (cmd == "role" || cmd == "roles") && before[0] == "show":
			return m.roles.Names()
		case cmd == "key" || cmd == "keys":
			return []string{"clear"}
		}
	}
	return nil
}

func commonPrefix(words []string) string {
	p := words[0]
	for _, w := range words[1:] {
		for !strings.HasPrefix(w, p) {
			p = p[:len(p)-1]
		}
	}
	return p
}

// hintWithCurrent lists the candidates with the shown one in brackets.
func hintWithCurrent(cands []string, cur int) string {
	parts := make([]string, len(cands))
	for i, c := range cands {
		if i == cur {
			c = "[" + c + "]"
		}
		parts[i] = c
	}
	return strings.Join(parts, "  ")
}
