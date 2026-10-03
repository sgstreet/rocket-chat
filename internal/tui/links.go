package tui

import (
	"strings"
	"unicode/utf8"
)

const (
	underlineOn  = "\x1b[4m"
	underlineOff = "\x1b[24m"
)

// underlineLinks underlines every link in rendered text: OSC 8 hyperlinks
// (Markdown links, Gemini sources) and URLs written out as plain text, such
// as in typed messages. Plain URLs are also made hyperlinks, so terminals
// can open them when they handle the mouse themselves.
func underlineLinks(s string) string {
	if !strings.Contains(s, "://") && !strings.Contains(s, "mailto:") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = underlineLine(l)
	}
	return strings.Join(lines, "\n")
}

// urlSpan is a plain URL on a line: printable runes [from, to).
type urlSpan struct {
	from, to int
	url      string
}

func underlineLine(line string) string {
	if !strings.Contains(line, "://") && !strings.Contains(line, "mailto:") {
		return line
	}
	spans := plainURLSpans(line)

	var b strings.Builder
	b.Grow(len(line) + 16)
	inLink := false  // inside an OSC 8 hyperlink
	inPlain := false // inside a plain URL we made a hyperlink
	k, next := 0, 0  // printable rune index; next span in spans
	for i := 0; i < len(line); {
		if line[i] == 0x1b && i+1 < len(line) {
			j := seqEnd(line, i)
			seq := line[i:j]
			if url, ok := osc8(line, i); ok {
				if url == "" && inLink {
					b.WriteString(underlineOff)
				}
				b.WriteString(seq)
				inLink = url != ""
				if inLink {
					b.WriteString(underlineOn)
				}
			} else {
				b.WriteString(seq)
				// A style change inside a link may have reset the underline.
				if (inLink || inPlain) && line[i+1] == '[' && seq[len(seq)-1] == 'm' {
					b.WriteString(underlineOn)
				}
			}
			i = j
			continue
		}
		if next < len(spans) && k == spans[next].from {
			b.WriteString("\x1b]8;;" + spans[next].url + "\x1b\\" + underlineOn)
			inPlain = true
		}
		_, size := utf8.DecodeRuneInString(line[i:])
		b.WriteString(line[i : i+size])
		i += size
		k++
		if inPlain && k == spans[next].to {
			b.WriteString(underlineOff + "\x1b]8;;\x1b\\")
			inPlain = false
			next++
		}
	}
	if inPlain {
		b.WriteString(underlineOff + "\x1b]8;;\x1b\\")
	}
	return b.String()
}

// plainURLSpans finds URLs in the line's text that are not already inside
// an OSC 8 hyperlink.
func plainURLSpans(line string) []urlSpan {
	var (
		text   strings.Builder
		runeAt []int // byte offset in text of each printable rune
		linked []bool
		inLink bool
	)
	for i := 0; i < len(line); {
		if line[i] == 0x1b && i+1 < len(line) {
			if url, ok := osc8(line, i); ok {
				inLink = url != ""
			}
			i = seqEnd(line, i)
			continue
		}
		_, size := utf8.DecodeRuneInString(line[i:])
		runeAt = append(runeAt, text.Len())
		linked = append(linked, inLink)
		text.WriteString(line[i : i+size])
		i += size
	}
	plain := text.String()
	runeIndex := func(off int) int {
		for k, o := range runeAt {
			if o >= off {
				return k
			}
		}
		return len(runeAt)
	}
	var spans []urlSpan
	for _, loc := range plainURL.FindAllStringIndex(plain, -1) {
		url := strings.TrimRight(plain[loc[0]:loc[1]], ".,;:!?)]}")
		from, to := runeIndex(loc[0]), runeIndex(loc[0]+len(url))
		alreadyLinked := false
		for k := from; k < to; k++ {
			alreadyLinked = alreadyLinked || linked[k]
		}
		if !alreadyLinked && to > from {
			spans = append(spans, urlSpan{from: from, to: to, url: url})
		}
	}
	return spans
}

// seqEnd returns the index just after the escape sequence starting at i.
func seqEnd(s string, i int) int {
	switch s[i+1] {
	case ']':
		_, next := oscEnd(s, i+2)
		return next
	case '[':
		j := i + 2
		for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
			j++
		}
		return min(j+1, len(s))
	}
	return min(i+2, len(s))
}

// osc8 reports whether the sequence at i is an OSC 8 hyperlink and its URL
// ("" ends a link).
func osc8(s string, i int) (string, bool) {
	if s[i+1] != ']' {
		return "", false
	}
	end, _ := oscEnd(s, i+2)
	rest, ok := strings.CutPrefix(s[i+2:end], "8;")
	if !ok {
		return "", false
	}
	_, url, _ := strings.Cut(rest, ";")
	return url, true
}
