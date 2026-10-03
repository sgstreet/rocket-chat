package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/chat"
	"github.com/sgstreet/rocket-chat/internal/render"
)

// oneShot streams a single reply to plain-text output. The answer and its
// sources go to out; progress, reasoning and usage go to errOut.
type oneShot struct {
	out, errOut io.Writer
	// backendName is the name the backend was selected by.
	backendName string
	// progress shows search and fetch lines.
	progress bool
	// thinking shows the model's reasoning.
	thinking bool
	// verbose shows usage and timing after the reply.
	verbose bool
	// buffer holds the answer back until the end so inline citation
	// markers can be inserted; used when stdout is not a terminal.
	buffer bool
}

func (o oneShot) run(ctx context.Context, b backend.Backend, req backend.Request) error {
	start := time.Now()
	var (
		grounding  *chat.Grounding
		usage      *backend.Usage
		outLineEnd = true // out is at the start of a line
		errLineEnd = true // errOut is at the start of a line
		buffered   strings.Builder
	)
	writeOut := func(s string) {
		fmt.Fprint(o.out, s)
		outLineEnd = strings.HasSuffix(s, "\n")
	}
	endErrLine := func() {
		if !errLineEnd {
			fmt.Fprintln(o.errOut)
			errLineEnd = true
		}
	}
	finish := func() {
		endErrLine()
		if buffered.Len() > 0 {
			writeOut(render.Cite(buffered.String(), grounding))
		}
		if !outLineEnd {
			writeOut("\n")
		}
	}

	for ev, err := range b.Chat(ctx, req) {
		if err != nil {
			finish()
			return err
		}
		switch ev.Kind {
		case backend.EventTextDelta:
			endErrLine()
			switch {
			case ev.Text == "":
			case o.buffer:
				buffered.WriteString(ev.Text)
			default:
				writeOut(ev.Text)
			}
		case backend.EventThinkingDelta:
			if o.thinking && ev.Text != "" {
				fmt.Fprint(o.errOut, ev.Text)
				errLineEnd = strings.HasSuffix(ev.Text, "\n")
			}
		case backend.EventSearchStarted:
			if o.progress {
				endErrLine()
				fmt.Fprintf(o.errOut, "Searching: %s\n", ev.Query)
			}
		case backend.EventNotice:
			if o.progress {
				endErrLine()
				fmt.Fprintln(o.errOut, ev.Text)
			}
		case backend.EventFetchStarted:
			if o.progress {
				endErrLine()
				fmt.Fprintf(o.errOut, "Reading: %s\n", ev.URL)
			}
		case backend.EventGrounding:
			grounding = ev.Grounding
		case backend.EventUsage:
			usage = ev.Usage
		}
	}
	finish()

	if s := render.Sources(grounding); s != "" {
		writeOut("\n" + s)
	}
	if o.verbose {
		fmt.Fprintln(o.errOut, stats(o.backendName, cmp.Or(req.Model, b.Capabilities().DefaultModel), usage, time.Since(start)))
	}
	return nil
}

func stats(backendName, model string, u *backend.Usage, elapsed time.Duration) string {
	label := backendName
	if model != "" {
		label += "/" + model
	}
	parts := []string{label}
	if u != nil {
		parts = append(parts, fmt.Sprintf("%d in / %d out tokens", u.InputTokens, u.OutputTokens))
		if u.SearchQueries > 0 {
			parts = append(parts, fmt.Sprintf("%d searches", u.SearchQueries))
		}
	}
	parts = append(parts, elapsed.Round(time.Millisecond).String())
	return "[" + strings.Join(parts, " · ") + "]"
}
