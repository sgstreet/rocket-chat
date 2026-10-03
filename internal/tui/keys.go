package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// keyCommand runs /key: list the keys, start hidden entry of one, or
// remove one.
func (m *model) keyCommand(arg string) tea.Cmd {
	keys := m.opts.Keys
	if keys == nil {
		m.errorf("API keys cannot be changed here.")
		return nil
	}
	name, rest, _ := strings.Cut(arg, " ")
	rest = strings.TrimSpace(rest)
	if name == "" {
		var b strings.Builder
		b.WriteString("API keys (save with /key <backend>, remove with /key <backend> clear):")
		for _, n := range keys.Names() {
			fmt.Fprintf(&b, "\n  %-8s %s", n, keys.Status(n))
		}
		m.notice("%s", b.String())
		return nil
	}
	if !slices.Contains(keys.Names(), name) {
		m.errorf("%s takes no API key. Backends with keys: %s.", name, strings.Join(keys.Names(), ", "))
		return nil
	}
	switch rest {
	case "":
		m.keyFor = name
		m.keyInput.Reset()
		m.keyInput.Prompt = name + " API key: "
		m.keyInput.Placeholder = "paste the key and press Enter (Esc cancels)"
		m.layout()
		m.input.Blur()
		m.notice("Paste the %s API key and press Enter; it is hidden as you type. Esc cancels.", name)
		return m.keyInput.Focus()
	default:
		if isKeyClear(rest) {
			m.saveKey(name, "")
			return nil
		}
		// Never echo or keep a key typed on the command line.
		m.errorf("Type /key %s on its own, then paste the key at the hidden prompt.", name)
	}
	return nil
}

// handleKeyEntry handles keys while an API key is being typed.
func (m *model) handleKeyEntry(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+v":
		return m.paste()
	case "esc", "ctrl+c":
		m.endKeyEntry()
		m.notice("No key saved.")
		return nil
	case "enter":
		name, key := m.keyFor, strings.TrimSpace(m.keyInput.Value())
		m.endKeyEntry()
		if key == "" {
			m.notice("No key saved.")
			return nil
		}
		m.saveKey(name, key)
		return nil
	}
	var cmd tea.Cmd
	m.keyInput, cmd = m.keyInput.Update(msg)
	return cmd
}

func (m *model) endKeyEntry() {
	m.keyFor = ""
	m.keyInput.Reset()
	m.keyInput.Blur()
	m.input.Focus()
}

// saveKey saves or removes a key and reopens the current backend if the
// key is its own, so the change applies straight away.
func (m *model) saveKey(name, key string) {
	keys := m.opts.Keys
	if err := keys.Set(name, key); err != nil {
		m.errorf("Key not saved: %v", err)
		return
	}
	msg := fmt.Sprintf("Saved the %s API key.", name)
	if key == "" {
		msg = fmt.Sprintf("Removed the saved %s API key.", name)
	}
	if env := keys.Overridden(name); env != "" {
		msg += fmt.Sprintf(" Note: $%s is set and is used instead of the saved key.", env)
	}
	if name == m.backendName {
		b, err := m.opts.Open(name)
		if err != nil {
			m.notice("%s", msg)
			m.errorf("%s: %v", name, err)
			return
		}
		m.b = b
	} else if key != "" {
		msg += fmt.Sprintf(" Switch to it with /backend %s.", name)
	}
	m.notice("%s", msg)
}
