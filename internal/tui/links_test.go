package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/sgstreet/rocket-chat/internal/backend/fake"
)

// underlined returns the visible text of s that is shown underlined, by
// tracking SGR 4 / 24 / 0 as a terminal would.
func underlined(s string) string {
	var out strings.Builder
	on := false
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) {
			j := seqEnd(s, i)
			if s[i+1] == '[' && s[j-1] == 'm' {
				for _, p := range strings.Split(s[i+2:j-1], ";") {
					switch p {
					case "4":
						on = true
					case "24", "", "0":
						on = false
					}
				}
			}
			i = j
			continue
		}
		if on {
			out.WriteByte(s[i])
		} else if out.Len() > 0 && !strings.HasSuffix(out.String(), "|") {
			out.WriteByte('|')
		}
		i++
	}
	return strings.TrimSuffix(out.String(), "|")
}

func TestUnderlineLinks(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"no links", "\x1b[1mplain\x1b[m text", ""},
		{
			"OSC 8 link with styles inside",
			"See \x1b]8;id=1;https://go.dev/\a\x1b[38;5;35;1mthe Go\x1b[m \x1b[1msite\x1b]8;;\a now",
			"the Go site",
		},
		{
			"ST-terminated link",
			"\x1b]8;;https://x.io\x1b\\uefa.com ↗\x1b]8;;\x1b\\ and more",
			"uefa.com ↗",
		},
		{
			"plain URLs, trailing punctuation left out",
			"open https://a.example/x?y=1, then (https://b.example).",
			"https://a.example/x?y=1|https://b.example",
		},
		{
			"plain URL with a style reset inside",
			"\x1b[38;5;252mvisit https://c.example/\x1b[m\x1b[38;5;252mpath ok\x1b[m",
			"https://c.example/path",
		},
		{"mailto", "write to mailto:me@example.com.", "mailto:me@example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := underlineLinks(tt.in)
			if u := underlined(got); u != tt.want {
				t.Errorf("underlined %q, want %q\nout: %q", u, tt.want, got)
			}
			if ansi.Strip(got) != ansi.Strip(tt.in) {
				t.Errorf("text changed: %q", ansi.Strip(got))
			}
		})
	}
}

func TestPlainURLsBecomeHyperlinks(t *testing.T) {
	out := underlineLinks("go to https://a.example/x, now")
	if !strings.Contains(out, "\x1b]8;;https://a.example/x\x1b\\") || !strings.Contains(out, "\x1b]8;;\x1b\\") {
		t.Errorf("no hyperlink: %q", out)
	}
	// An existing hyperlink is not wrapped a second time.
	in := "\x1b]8;;https://go.dev\x1b\\https://go.dev\x1b]8;;\x1b\\"
	if out := underlineLinks(in); strings.Count(out, "\x1b]8;;https://go.dev") != 1 {
		t.Errorf("wrapped twice: %q", out)
	}
	// linkAt still finds links in underlined text.
	if got := linkAt(underlineLinks("x https://a.example/x y"), 4); got != "https://a.example/x" {
		t.Errorf("linkAt = %q", got)
	}
}

func TestTranscriptLinksUnderlined(t *testing.T) {
	h := newHarness(t, map[string]*fake.Backend{"fake": {}}, "fake")
	h.typeAndSend("Read [the docs](https://go.dev/doc/) or https://example.com/a")
	var reply, typed string
	for _, l := range h.m.lines {
		switch plain := ansi.Strip(l); {
		case strings.Contains(plain, "Read the docs"):
			reply = l
		case strings.Contains(plain, "Read [the docs]"):
			typed = l
		}
	}
	if u := underlined(reply); !strings.Contains(u, "the docs") || !strings.Contains(u, "https://example.com/a") {
		t.Errorf("reply underlined %q in %q", u, reply)
	}
	if u := underlined(typed); !strings.Contains(u, "https://go.dev/doc/") || !strings.Contains(u, "https://example.com/a") {
		t.Errorf("typed message underlined %q in %q", u, typed)
	}
}
