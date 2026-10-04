package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/backend/fake"
	"github.com/sgstreet/rocket-chat/internal/chat"
	"github.com/sgstreet/rocket-chat/internal/store"
)

// chatTurns asks n questions, "q1" to "qn"; the echo backend answers each
// with its question.
func chatTurns(h *harness, n int) {
	for i := 1; i <= n; i++ {
		h.typeAndSend(fmt.Sprintf("q%d about things", i))
	}
}

func TestCompact(t *testing.T) {
	b := &fake.Backend{}
	h := newHarnessWith(t, map[string]*fake.Backend{"a": b}, Options{Backend: "a", System: "Be brief."})

	h.typeAndSend("/compact")
	if !strings.Contains(h.last(entryNotice).text, "Nothing to compact yet") {
		t.Errorf("short chat: %q", h.last(entryNotice).text)
	}

	chatTurns(h, 4) // 8 messages
	h.typeAndSend("/compact keep the names")
	reqs := b.Requests()
	sum := reqs[len(reqs)-1]
	// The first two exchanges are summarized; the last two are kept.
	if len(sum.Messages) != 5 || sum.Messages[0].Text != "q1 about things" || sum.Messages[3].Text != "q2 about things" ||
		!strings.Contains(sum.Messages[4].Text, "Pay particular attention to: keep the names") ||
		sum.System != compactSystem || sum.Search == nil || *sum.Search {
		t.Fatalf("summary request = %+v", sum)
	}
	if h.m.compacted != 4 || h.m.summary == "" || !strings.Contains(h.last(entryNotice).text, "Compacted 4 messages") {
		t.Fatalf("compacted %d, summary %q, notice %q", h.m.compacted, h.m.summary, h.last(entryNotice).text)
	}
	if n := strings.Count(h.view(), "· compacted"); n == 0 {
		t.Error("compacted messages are not marked")
	}

	// The next question sends the summary and the kept messages only.
	h.typeAndSend("q5 about things")
	reqs = b.Requests()
	next := reqs[len(reqs)-1]
	if !strings.HasPrefix(next.System, "Be brief.\n\n"+summaryIntro) || len(next.Messages) != 5 || next.Messages[0].Text != "q3 about things" {
		t.Errorf("next request: system %q, %d messages, first %q", next.System, len(next.Messages), next.Messages[0].Text)
	}

	// /context shows the summary.
	h.typeAndSend("/context")
	if got := h.last(entryNotice).text; !strings.Contains(got, "in place of 4 compacted messages") {
		t.Errorf("/context:\n%s", got)
	}

	// A second compaction folds the first summary in.
	h.typeAndSend("/compact")
	sum = b.Requests()[len(b.Requests())-1]
	if !strings.Contains(sum.System, "Summary of the conversation before these messages") || sum.Messages[0].Text != "q3 about things" {
		t.Errorf("second summary request: %+v", sum)
	}
	if h.m.compacted != 6 {
		t.Errorf("compacted %d after the second compaction", h.m.compacted)
	}

	// /new starts over.
	h.typeAndSend("/new")
	if h.m.summary != "" || h.m.compacted != 0 {
		t.Error("/new kept the summary")
	}
}

func TestCompactStopAndErrors(t *testing.T) {
	slow := &fake.Backend{}
	h := newHarnessWith(t, map[string]*fake.Backend{"a": slow}, Options{Backend: "a"})
	chatTurns(h, 3)

	// Esc while the summary is being written stops it.
	slow.Delay = time.Hour
	h.m.input.SetValue("/compact")
	h.m.Update(press("enter"))
	if !h.m.compacting || !strings.Contains(h.view(), "compacting… esc to stop") {
		t.Fatal("not compacting")
	}
	h.m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.m.compacting || h.m.summary != "" || !strings.Contains(h.last(entryNotice).text, "Compaction stopped") {
		t.Errorf("after esc: compacting %v, summary %q", h.m.compacting, h.m.summary)
	}

	// A failed summary changes nothing.
	failing := &fake.Backend{Script: []backend.Event{}, Err: fmt.Errorf("model overloaded")}
	h2 := newHarnessWith(t, map[string]*fake.Backend{"a": {}, "f": failing}, Options{Backend: "a"})
	chatTurns(h2, 3)
	h2.typeAndSend("/backend f")
	h2.typeAndSend("/compact")
	if h2.m.summary != "" || !strings.Contains(h2.last(entryError).text, "Cannot compact: model overloaded") {
		t.Errorf("error: summary %q", h2.m.summary)
	}
}

func TestAutoCompact(t *testing.T) {
	b := &fake.Backend{Window: 40}
	h := newHarnessWith(t, map[string]*fake.Backend{"a": b}, Options{Backend: "a", AutoCompact: 0.5, CompactKeep: 2})
	h.typeAndSend("first question about things")  // ~7 tokens each way
	h.typeAndSend("second question about things") // window now known
	before := len(b.Requests())
	h.typeAndSend("third question about things")
	reqs := b.Requests()[before:]
	if len(reqs) != 2 || reqs[0].System != compactSystem || reqs[1].Messages[len(reqs[1].Messages)-1].Text != "third question about things" {
		t.Fatalf("requests after the threshold: %+v", reqs)
	}
	// The kept messages start at a question: q2, its reply, and q3.
	if h.m.compacted != 2 || len(reqs[1].Messages) != 3 {
		t.Errorf("compacted %d; question sent with %d messages", h.m.compacted, len(reqs[1].Messages))
	}
}

func TestCompactGroundedSummaryStaysWithGemini(t *testing.T) {
	grounded := &fake.Backend{Script: []backend.Event{
		{Kind: backend.EventTextDelta, Text: "Grounded answer."},
		{Kind: backend.EventGrounding, Grounding: &chat.Grounding{Sources: []chat.Source{{URL: "https://example.com"}}}},
		{Kind: backend.EventDone},
	}}
	other := &fake.Backend{}
	h := newHarnessWith(t, map[string]*fake.Backend{chat.GroundedBackend: grounded, "other": other}, Options{Backend: chat.GroundedBackend})
	chatTurns(h, 3)
	h.typeAndSend("/compact")
	if !h.m.summaryRestricted {
		t.Fatal("a Gemini summary of grounded answers is not restricted")
	}
	h.typeAndSend("/backend other")
	if strings.Contains(h.m.requestSystem(), summaryIntro) {
		t.Error("the restricted summary would be sent to another backend")
	}
	h.typeAndSend("/context")
	if !strings.Contains(h.last(entryNotice).text, "summary        not sent") {
		t.Errorf("/context:\n%s", h.last(entryNotice).text)
	}
}

func TestCompactionIsSavedAndExported(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := newHarnessWith(t, map[string]*fake.Backend{"a": {}}, Options{Backend: "a", Store: st})
	chatTurns(h, 3)
	h.typeAndSend("/compact")
	sess, err := st.Load(h.m.session.ID)
	if err != nil || sess.Summary == "" || sess.Compacted != 2 || len(sess.Messages) != 6 {
		t.Fatalf("saved: %v, summary %q, compacted %d, %d messages", err, sess.Summary, sess.Compacted, len(sess.Messages))
	}

	h2 := newHarnessWith(t, map[string]*fake.Backend{"a": {}}, Options{Backend: "a", Store: st, Resume: sess})
	if h2.m.compacted != 2 || len(h2.m.requestMessages()) != 4 || !strings.Contains(h2.view(), "· compacted") {
		t.Errorf("resumed: compacted %d, %d messages sent", h2.m.compacted, len(h2.m.requestMessages()))
	}

	path := filepath.Join(t.TempDir(), "chat.md")
	h2.typeAndSend("/export " + path)
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "q1 about things") || !strings.Contains(string(data), "The first 2 messages were compacted") {
		t.Errorf("export:\n%s", data)
	}
}
