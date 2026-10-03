package render

import (
	"regexp"
	"strings"

	"charm.land/glamour/v2"
)

// Terminal renders Markdown for a terminal: styled text wrapped to width,
// with code highlighting and OSC 8 links. style is a glamour standard
// style, "dark" or "light".
func Terminal(text string, width int, style string) (string, error) {
	r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(width))
	if err != nil {
		return "", err
	}
	out, err := r.Render(text)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.Trim(out, "\n"), "\n")
	for i, l := range lines {
		lines[i] = trimPadding(l)
	}
	return strings.Join(lines, "\n"), nil
}

// trailingPad matches the spaces glamour pads lines with, and the styles
// between them.
var trailingPad = regexp.MustCompile(`(?:[ \t]|\x1b\[[0-9;]*m)+$`)

// trimPadding removes trailing spaces from a styled line, ending it with a
// style reset if it removed one.
func trimPadding(line string) string {
	pad := trailingPad.FindString(line)
	if pad == "" {
		return line
	}
	trimmed := line[:len(line)-len(pad)]
	if strings.Contains(pad, "\x1b[") {
		trimmed += "\x1b[0m"
	}
	return trimmed
}
