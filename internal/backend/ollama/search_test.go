package ollama

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ollama/ollama/api"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/backend/backendtest"
	"github.com/sgstreet/rocket-chat/internal/chat"
)

func toolCall(name, key, value string) api.ChatResponse {
	args := api.NewToolCallFunctionArguments()
	args.Set(key, value)
	return api.ChatResponse{Message: api.Message{
		Role:      "assistant",
		ToolCalls: []api.ToolCall{{ID: "call-" + name, Function: api.ToolCallFunction{Name: name, Arguments: args}}},
	}}
}

func text(s string) api.ChatResponse {
	return api.ChatResponse{Message: api.Message{Role: "assistant", Content: s}}
}

func done(in, out int) api.ChatResponse {
	return api.ChatResponse{Done: true, Metrics: api.Metrics{PromptEvalCount: in, EvalCount: out}}
}

var on = true

func searchBackend(t *testing.T, fs *fakeServer) *Backend {
	t.Helper()
	t.Setenv(EnvAPIKey, "")
	b := newBackend(t, Settings{Host: fs.URL, Model: "qwen3", Search: SearchSettings{Enabled: true}})
	b.now = func() time.Time { return time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC) }
	return b
}

type collected struct {
	text      string
	kinds     []backend.EventKind
	queries   []string
	urls      []string
	grounding *chat.Grounding
	usage     *backend.Usage
	err       error
}

func collect(t *testing.T, b *Backend, req backend.Request) collected {
	t.Helper()
	var c collected
	var sb strings.Builder
	for ev, err := range b.Chat(t.Context(), req) {
		if err != nil {
			c.err = err
			break
		}
		c.kinds = append(c.kinds, ev.Kind)
		switch ev.Kind {
		case backend.EventTextDelta:
			sb.WriteString(ev.Text)
		case backend.EventSearchStarted:
			c.queries = append(c.queries, ev.Query)
		case backend.EventFetchStarted:
			c.urls = append(c.urls, ev.URL)
		case backend.EventGrounding:
			c.grounding = ev.Grounding
		case backend.EventUsage:
			c.usage = ev.Usage
		}
	}
	c.text = sb.String()
	return c
}

func TestSearchLoop(t *testing.T) {
	fs := newFakeServer(t)
	fs.rounds = [][]api.ChatResponse{
		{text("Let me check."), toolCall(toolWebSearch, "query", "euro 2024 winner"), done(100, 10)},
		{toolCall(toolWebFetch, "url", "https://two.example"), done(300, 5)},
		{text("Spain won Euro 2024 [2]."), done(500, 8)},
	}
	b := searchBackend(t, fs)
	c := collect(t, b, backend.Request{System: "be brief", Messages: helloReq.Messages})
	if c.err != nil {
		t.Fatal(c.err)
	}

	if want := "Let me check.\n\nSpain won Euro 2024 [2]."; c.text != want {
		t.Errorf("text = %q, want %q", c.text, want)
	}
	if !slices.Equal(c.queries, []string{"euro 2024 winner"}) || !slices.Equal(c.urls, []string{"https://two.example"}) {
		t.Errorf("queries %v urls %v", c.queries, c.urls)
	}
	if c.kinds[len(c.kinds)-1] != backend.EventDone {
		t.Errorf("last event %v, want Done", c.kinds[len(c.kinds)-1])
	}
	if c.usage == nil || *c.usage != (backend.Usage{InputTokens: 900, OutputTokens: 23, SearchQueries: 1}) {
		t.Errorf("usage = %+v", c.usage)
	}

	g := c.grounding
	if g == nil {
		t.Fatal("no grounding")
	}
	wantSources := []chat.Source{
		{Title: "First for euro 2024 winner", URL: "https://one.example", Snippet: "one"},
		{Title: "Second for euro 2024 winner", URL: "https://two.example", Snippet: "two", Cited: true},
	}
	if !slices.Equal(g.Sources, wantSources) {
		t.Errorf("sources = %+v\nwant %+v", g.Sources, wantSources)
	}
	if !slices.Equal(g.Queries, []string{"euro 2024 winner"}) {
		t.Errorf("grounding queries = %v", g.Queries)
	}

	reqs := fs.chatRequests()
	if len(reqs) != 3 {
		t.Fatalf("%d chat calls, want 3", len(reqs))
	}
	first := reqs[0]
	if len(first.Tools) != 2 {
		t.Errorf("first call has %d tools, want 2", len(first.Tools))
	}
	if sys := first.Messages[0]; sys.Role != "system" || !strings.HasPrefix(sys.Content, "be brief\n\n") ||
		!strings.Contains(sys.Content, "2026-10-03") || strings.Contains(sys.Content, "[1]") {
		t.Errorf("system message = %+v", sys)
	}
	if first.Options["num_ctx"] != float64(32768) {
		t.Errorf("num_ctx = %v, want 32768 while searching", first.Options["num_ctx"])
	}

	// The second call carries the assistant tool call and the numbered
	// results; the fetched page reuses number [2].
	msgs := reqs[2].Messages
	var tool []api.Message
	for _, m := range msgs {
		if m.Role == "tool" {
			tool = append(tool, m)
		}
	}
	if len(tool) != 2 {
		t.Fatalf("tool messages = %+v", tool)
	}
	if tool[0].ToolName != toolWebSearch || tool[0].ToolCallID != "call-web_search" ||
		!strings.Contains(tool[0].Content, "[1] First for euro 2024 winner\nURL: https://one.example") ||
		!strings.Contains(tool[0].Content, "[2] Second") || !strings.HasSuffix(tool[0].Content, citeHint) {
		t.Errorf("search tool message = %+v", tool[0])
	}
	if tool[1].ToolName != toolWebFetch || !strings.HasPrefix(tool[1].Content, "[2] Fetched https://two.example") ||
		!strings.HasSuffix(tool[1].Content, citeHint) {
		t.Errorf("fetch tool message = %+v", tool[1])
	}
}

func TestSearchMaxRounds(t *testing.T) {
	fs := newFakeServer(t)
	fs.rounds = [][]api.ChatResponse{
		{toolCall(toolWebSearch, "query", "again"), done(1, 1)},
	}
	b := searchBackend(t, fs)
	b.settings.Search.MaxRounds = 2
	if c := collect(t, b, helloReq); c.err != nil {
		t.Fatal(c.err)
	}
	reqs := fs.chatRequests()
	if len(reqs) != 3 {
		t.Fatalf("%d chat calls, want 3 (2 rounds with tools + final)", len(reqs))
	}
	if len(reqs[1].Tools) == 0 || len(reqs[2].Tools) != 0 {
		t.Errorf("tools per call: %d, %d, %d; want tools removed on the last", len(reqs[0].Tools), len(reqs[1].Tools), len(reqs[2].Tools))
	}
}

func TestSearchAuthErrorEndsReply(t *testing.T) {
	fs := newFakeServer(t)
	fs.searchStatus = http.StatusUnauthorized
	fs.rounds = [][]api.ChatResponse{{toolCall(toolWebSearch, "query", "q"), done(1, 1)}}
	c := collect(t, searchBackend(t, fs), helloReq)
	if c.err == nil || !strings.Contains(c.err.Error(), "ollama signin") {
		t.Errorf("err = %v, want sign-in hint", c.err)
	}
}

func TestSearchToolErrorGoesToModel(t *testing.T) {
	fs := newFakeServer(t)
	fs.searchStatus = http.StatusInternalServerError
	fs.rounds = [][]api.ChatResponse{
		{toolCall(toolWebSearch, "query", "q"), done(1, 1)},
		{text("I could not search."), done(1, 1)},
	}
	c := collect(t, searchBackend(t, fs), helloReq)
	if c.err != nil {
		t.Fatal(c.err)
	}
	last := fs.lastRequest(t).Messages
	if m := last[len(last)-1]; m.Role != "tool" || !strings.Contains(m.Content, "The web_search tool failed") {
		t.Errorf("tool message = %+v", m)
	}
	if c.text != "I could not search." {
		t.Errorf("text = %q", c.text)
	}
}

func TestSearchDirectUsesAPIKey(t *testing.T) {
	var gotAuth, gotPath string
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewEncoder(w).Encode(searchResults("q"))
	}))
	t.Cleanup(web.Close)

	fs := newFakeServer(t)
	fs.rounds = [][]api.ChatResponse{
		{toolCall(toolWebSearch, "query", "q"), done(1, 1)},
		{text("ok [1]"), done(1, 1)},
	}
	b := searchBackend(t, fs)
	t.Setenv(EnvAPIKey, "secret")
	b.settings.Search.APIURL = web.URL
	if c := collect(t, b, helloReq); c.err != nil {
		t.Fatal(c.err)
	}
	if gotAuth != "Bearer secret" || gotPath != "/api/web_search" {
		t.Errorf("auth %q path %q", gotAuth, gotPath)
	}
	if fs.searchHits != 0 {
		t.Errorf("local route used %d times, want 0", fs.searchHits)
	}
}

func TestSearchRequiresTools(t *testing.T) {
	fs := newFakeServer(t, standardChunks...)
	fs.capabilities = map[string][]string{"qwen3": {"completion"}}
	c := collect(t, searchBackend(t, fs), helloReq)
	if c.err == nil || !strings.Contains(c.err.Error(), "does not support tool calling") {
		t.Errorf("err = %v", c.err)
	}
}

func TestSearchRequestOverride(t *testing.T) {
	fs := newFakeServer(t, standardChunks...)
	b := searchBackend(t, fs)
	off := false
	req := helloReq
	req.Search = &off
	if c := collect(t, b, req); c.err != nil {
		t.Fatal(c.err)
	}
	if got := fs.lastRequest(t); len(got.Tools) != 0 {
		t.Errorf("tools sent with --search=false")
	}

	b.settings.Search.Enabled = false
	req.Search = &on
	if c := collect(t, b, req); c.err != nil {
		t.Fatal(c.err)
	}
	if got := fs.lastRequest(t); len(got.Tools) != 2 {
		t.Errorf("tools not sent with --search")
	}
}

func TestSearchContract(t *testing.T) {
	fs := newFakeServer(t)
	fs.delay = 5 * time.Millisecond
	fs.rounds = [][]api.ChatResponse{
		{toolCall(toolWebSearch, "query", "q"), done(1, 1)},
		{text("a [1]"), text(" b"), done(1, 1)},
	}
	backendtest.Run(t, func(t *testing.T) backend.Backend {
		return searchBackend(t, fs)
	}, helloReq)
}

func TestNewSearcher(t *testing.T) {
	t.Setenv(EnvAPIKey, "")
	if s, err := newSearcher(SearchAuto, "", nil, nil); err != nil {
		t.Fatal(err)
	} else if _, ok := s.(localSearcher); !ok {
		t.Errorf("auto without key = %T, want localSearcher", s)
	}
	if _, err := newSearcher(SearchDirect, "", nil, nil); err == nil {
		t.Error("direct without key: want error")
	}
	if _, err := newSearcher("bogus", "", nil, nil); err == nil {
		t.Error("unknown mode: want error")
	}
	t.Setenv(EnvAPIKey, "k")
	if s, _ := newSearcher(SearchAuto, "", nil, nil); s == nil {
		t.Fatal("nil searcher")
	} else if _, ok := s.(directSearcher); !ok {
		t.Errorf("auto with key = %T, want directSearcher", s)
	}
}

func TestGroundingCitations(t *testing.T) {
	turn := newSearchTurn(SearchSettings{}, nil)
	for _, u := range []string{"https://a", "https://b", "https://c", "https://d"} {
		turn.source(chat.Source{URL: u})
	}
	g := turn.grounding("x [1, 3] y [9] z [4]")
	var cited []bool
	for _, s := range g.Sources {
		cited = append(cited, s.Cited)
	}
	if want := []bool{true, false, true, true}; !slices.Equal(cited, want) {
		t.Errorf("cited = %v, want %v", cited, want)
	}
	if newSearchTurn(SearchSettings{}, nil).grounding("[1]") != nil {
		t.Error("grounding with no sources or queries should be nil")
	}
}

func TestTruncateUTF8(t *testing.T) {
	if got := truncateUTF8("héllo", 2); got != "h" {
		t.Errorf("got %q, want %q", got, "h")
	}
	if got := truncateUTF8("abc", 5); got != "abc" {
		t.Errorf("got %q", got)
	}
}
