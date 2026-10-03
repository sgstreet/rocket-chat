package tui

import (
	"strings"
)

// History is where typed inputs are kept between runs.
type History interface {
	Entries() []string
	Add(entry string) error
}

// inputHistory lets Up/Down browse earlier inputs. A recalled entry can be
// edited; the edit is kept while browsing (like readline) and dropped once
// something is sent, so history itself only ever gains new entries.
type inputHistory struct {
	store   History
	entries []string
	// pos is the entry being shown, len(entries) for the new draft, or -1
	// when not browsing.
	pos   int
	edits map[int]string
}

func newInputHistory(store History) *inputHistory {
	h := &inputHistory{store: store, pos: -1}
	if store != nil {
		h.entries = store.Entries()
	}
	return h
}

// prev returns the entry before the one shown, given the current input.
func (h *inputHistory) prev(current string) (string, bool) {
	if len(h.entries) == 0 || h.pos == 0 {
		return "", false
	}
	if h.pos < 0 {
		h.pos = len(h.entries)
		h.edits = map[int]string{}
	}
	h.edits[h.pos] = current
	h.pos--
	return h.at(h.pos), true
}

// next returns the entry after the one shown, ending with the draft that
// was being typed before browsing started.
func (h *inputHistory) next(current string) (string, bool) {
	if h.pos < 0 {
		return "", false
	}
	h.edits[h.pos] = current
	h.pos++
	text := h.at(h.pos)
	if h.pos == len(h.entries) {
		h.pos, h.edits = -1, nil
	}
	return text, true
}

func (h *inputHistory) at(i int) string {
	if e, ok := h.edits[i]; ok {
		return e
	}
	if i < len(h.entries) {
		return h.entries[i]
	}
	return ""
}

// add records a sent input and stops browsing. It returns the error from
// saving, if any; the entry is kept in memory either way.
func (h *inputHistory) add(text string) error {
	h.pos, h.edits = -1, nil
	if !worthKeeping(text) {
		return nil
	}
	if n := len(h.entries); n == 0 || h.entries[n-1] != text {
		h.entries = append(h.entries, text)
	}
	if h.store == nil {
		return nil
	}
	return h.store.Add(text)
}

// worthKeeping reports whether text belongs in the history. A /key command
// with anything after the backend name is refused because it may hold a
// key, so it is not kept either.
func worthKeeping(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	fields := strings.Fields(text)
	if (fields[0] == "/key" || fields[0] == "/keys") && len(fields) > 2 {
		return isKeyClear(fields[2]) && len(fields) == 3
	}
	return true
}

func isKeyClear(word string) bool {
	switch word {
	case "clear", "remove", "delete", "off":
		return true
	}
	return false
}
