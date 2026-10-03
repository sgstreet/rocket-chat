package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTerminal(t *testing.T) {
	out, err := Terminal("# Title\n\nSome **bold** text and `code`.\n\n- one\n- two", 40, "dark")
	if err != nil {
		t.Fatal(err)
	}
	plain := ansi.Strip(out)
	for _, want := range []string{"Title", "Some bold text and", "code", "• one"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "**") || !strings.Contains(out, "\x1b[") {
		t.Errorf("not rendered:\n%q", out)
	}
	for _, line := range strings.Split(plain, "\n") {
		if ansi.StringWidth(line) > 40 || strings.HasSuffix(line, " ") {
			t.Errorf("line too wide or padded: %q", line)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "\x1b[") && !strings.HasSuffix(line, "m") {
			t.Errorf("line does not end its styles: %q", line)
		}
	}
}
