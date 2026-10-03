package gemini

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/backend/backendtest"
	"github.com/sgstreet/rocket-chat/internal/chat"
	"github.com/sgstreet/rocket-chat/internal/config"
	"github.com/sgstreet/rocket-chat/internal/render"
)

// groundedStream is a streamGenerateContent reply in the Gemini API wire
// format: a thought, two text chunks, then grounding metadata. The en dash
// in "2–1" makes byte and character offsets differ.
var groundedStream = []string{
	`{"candidates":[{"content":{"role":"model","parts":[{"text":"Looking up the final.","thought":true}]}}]}`,
	`{"candidates":[{"content":{"role":"model","parts":[{"text":"Spain won Euro 2024."}]}}]}`,
	`{"candidates":[{"content":{"role":"model","parts":[{"text":" They beat England 2–1 in Berlin."}]},"finishReason":"STOP",
	  "groundingMetadata":{
	    "webSearchQueries":["euro 2024 winner","euro 2024 final score"],
	    "searchEntryPoint":{"renderedContent":"<style>.chip{}</style><div class=\"carousel\"><a class=\"chip\" href=\"https://www.google.com/search?q=euro+2024+winner&amp;client=app\">euro 2024 winner</a><a class=\"chip\" href=\"https://www.google.com/search?q=euro+2024+final\">euro 2024 <b>final</b></a></div>"},
	    "groundingChunks":[
	      {"web":{"uri":"https://vertexaisearch.example/redirect/1","title":"uefa.com"}},
	      {"web":{"uri":"https://vertexaisearch.example/redirect/2","title":"wikipedia.org"}},
	      {"web":{"uri":"https://vertexaisearch.example/redirect/3","title":"unused.example"}}
	    ],
	    "groundingSupports":[
	      {"segment":{"endIndex":20,"text":"Spain won Euro 2024."},"groundingChunkIndices":[0,1]},
	      {"segment":{"startIndex":21,"endIndex":55,"text":"They beat England 2–1 in Berlin."},"groundingChunkIndices":[0]}
	    ]}}],
	  "usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":20,"thoughtsTokenCount":5,"toolUsePromptTokenCount":300}}`,
}

// fakeAPI imitates the Gemini API endpoints the backend uses.
type fakeAPI struct {
	*httptest.Server
	chunks []string
	delay  time.Duration
	// status, when non-zero, fails generate requests with this code and
	// message.
	status  int
	message string

	mu     sync.Mutex
	bodies []map[string]any
	paths  []string
}

func newFakeAPI(t *testing.T, chunks ...string) *fakeAPI {
	t.Helper()
	f := &fakeAPI{chunks: chunks}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models"):
		_, _ = io.WriteString(w, `{"models":[
		  {"name":"models/gemini-3.8-flash","displayName":"Gemini 3.8 Flash","inputTokenLimit":1048576,"supportedGenerationMethods":["generateContent","countTokens"]},
		  {"name":"models/text-embedding-004","displayName":"Embedding","supportedGenerationMethods":["embedContent"]}]}`)
	case strings.HasSuffix(r.URL.Path, ":streamGenerateContent"):
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = fmt.Fprintf(w, `{"error":{"code":%d,"message":%q,"status":"ERR"}}`, f.status, f.message)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range f.chunks {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(f.delay):
			}
			_, _ = fmt.Fprintf(w, "data: %s\r\n\r\n", strings.Join(strings.Fields(c), " "))
			w.(http.Flusher).Flush()
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeAPI) last(t *testing.T) (string, map[string]any) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		t.Fatal("no generate requests")
	}
	return f.paths[len(f.paths)-1], f.bodies[len(f.bodies)-1]
}

func newBackend(t *testing.T, f *fakeAPI, s Settings) *Backend {
	t.Helper()
	t.Setenv("GEMINI_API_KEY", "test-key")
	s.BaseURL = f.URL
	b, err := New(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	b.now = func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }
	return b
}

var question = backend.Request{Messages: []chat.Message{{Role: chat.RoleUser, Text: "Who won Euro 2024?"}}}

func TestChatGrounded(t *testing.T) {
	f := newFakeAPI(t, groundedStream...)
	b := newBackend(t, f, Settings{})

	var (
		answer, thinking strings.Builder
		g                *chat.Grounding
		usage            *backend.Usage
		kinds            []backend.EventKind
	)
	for ev, err := range b.Chat(t.Context(), question) {
		if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, ev.Kind)
		switch ev.Kind {
		case backend.EventTextDelta:
			answer.WriteString(ev.Text)
		case backend.EventThinkingDelta:
			thinking.WriteString(ev.Text)
		case backend.EventGrounding:
			g = ev.Grounding
		case backend.EventUsage:
			usage = ev.Usage
		}
	}

	if kinds[len(kinds)-1] != backend.EventDone {
		t.Errorf("last event %v", kinds[len(kinds)-1])
	}
	if got, want := answer.String(), "Spain won Euro 2024. They beat England 2–1 in Berlin."; got != want {
		t.Errorf("answer = %q, want %q", got, want)
	}
	if thinking.String() != "Looking up the final." {
		t.Errorf("thinking = %q", thinking.String())
	}
	if usage == nil || *usage != (backend.Usage{InputTokens: 312, OutputTokens: 25, SearchQueries: 2}) {
		t.Errorf("usage = %+v", usage)
	}

	if g == nil {
		t.Fatal("no grounding")
	}
	wantSources := []chat.Source{
		{Title: "uefa.com", URL: "https://vertexaisearch.example/redirect/1", Cited: true},
		{Title: "wikipedia.org", URL: "https://vertexaisearch.example/redirect/2", Cited: true},
		{Title: "unused.example", URL: "https://vertexaisearch.example/redirect/3"},
	}
	if !slices.Equal(g.Sources, wantSources) {
		t.Errorf("sources = %+v", g.Sources)
	}
	if !slices.Equal(g.Queries, []string{"euro 2024 winner", "euro 2024 final score"}) {
		t.Errorf("queries = %v", g.Queries)
	}
	wantLinks := []chat.Link{
		{Text: "euro 2024 winner", URL: "https://www.google.com/search?q=euro+2024+winner&client=app"},
		{Text: "euro 2024 final", URL: "https://www.google.com/search?q=euro+2024+final"},
	}
	if g.Suggestions == nil || !slices.Equal(g.Suggestions.Links, wantLinks) || g.Suggestions.HTML == "" {
		t.Errorf("suggestions = %+v", g.Suggestions)
	}

	// The second segment's offsets count the en dash as 3 bytes; citation
	// markers must land after the right text.
	if got, want := render.Cite(answer.String(), g), "Spain won Euro 2024.[1][2] They beat England 2–1 in Berlin.[1]"; got != want {
		t.Errorf("cited = %q, want %q", got, want)
	}
}

func TestRequest(t *testing.T) {
	f := newFakeAPI(t, groundedStream...)
	temp, think := 0.3, true
	b := newBackend(t, f, Settings{
		Model:       "gemini-3.8-flash",
		Temperature: &temp,
		Think:       &think,
		Search:      SearchSettings{Since: config.Duration(7 * 24 * time.Hour)},
	})
	req := backend.Request{
		System: "be brief",
		Messages: []chat.Message{
			{Role: chat.RoleUser, Text: "q1"},
			{Role: chat.RoleAssistant, Text: "a1"},
			{Role: chat.RoleUser, Text: "q2"},
		},
	}
	for range b.Chat(t.Context(), req) {
	}
	path, body := f.last(t)
	if !strings.HasSuffix(path, "/models/gemini-3.8-flash:streamGenerateContent") {
		t.Errorf("path = %s", path)
	}

	js, _ := json.Marshal(body)
	got := string(js)
	for _, want := range []string{
		`"contents":[{"parts":[{"text":"q1"}],"role":"user"},{"parts":[{"text":"a1"}],"role":"model"},{"parts":[{"text":"q2"}],"role":"user"}]`,
		`"systemInstruction":{"parts":[{"text":"be brief"}]`,
		`"temperature":0.3`,
		`"thinkingConfig":{"includeThoughts":true}`,
		`"googleSearch":{"timeRangeFilter":{"endTime":"2026-10-03T12:00:00Z","startTime":"2026-09-26T12:00:00Z"}}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("request body missing %s\nbody: %s", want, got)
		}
	}
}

func TestSearchToggle(t *testing.T) {
	f := newFakeAPI(t, groundedStream...)
	off, on := false, true

	hasSearch := func() bool {
		_, body := f.last(t)
		js, _ := json.Marshal(body)
		return strings.Contains(string(js), "googleSearch")
	}

	b := newBackend(t, f, Settings{})
	for range b.Chat(t.Context(), question) {
	}
	if !hasSearch() {
		t.Error("search should be on by default")
	}

	req := question
	req.Search = &off
	for range b.Chat(t.Context(), req) {
	}
	if hasSearch() {
		t.Error("--search=false still sent googleSearch")
	}

	b = newBackend(t, f, Settings{Search: SearchSettings{Enabled: &off}})
	for range b.Chat(t.Context(), question) {
	}
	if hasSearch() {
		t.Error("search.enabled=false still sent googleSearch")
	}
	req.Search = &on
	for range b.Chat(t.Context(), req) {
	}
	if !hasSearch() {
		t.Error("--search did not override search.enabled=false")
	}
}

func TestErrors(t *testing.T) {
	off := false
	tests := []struct {
		status  int
		message string
		search  *bool
		want    string
	}{
		{400, "API key not valid. Please pass a valid API key.", nil, "check GEMINI_API_KEY"},
		{404, "models/nope is not found", nil, `gemini model "nope" not found; see --list-models`},
		{429, "Resource has been exhausted", nil, "may not include grounding"},
		{429, "Resource has been exhausted", &off, "quota or rate limit reached; try again later"},
	}
	for _, tt := range tests {
		f := newFakeAPI(t)
		f.status, f.message = tt.status, tt.message
		b := newBackend(t, f, Settings{Model: "nope"})
		req := question
		req.Search = tt.search
		var gotErr error
		for _, err := range b.Chat(t.Context(), req) {
			if err != nil {
				gotErr = err
				break
			}
		}
		if gotErr == nil || !strings.Contains(gotErr.Error(), tt.want) {
			t.Errorf("status %d: err = %v, want %q", tt.status, gotErr, tt.want)
		}
	}
}

func TestMissingAPIKey(t *testing.T) {
	for _, env := range apiKeyEnv {
		t.Setenv(env, "")
	}
	if _, err := New(Settings{}, nil); err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Errorf("err = %v", err)
	}
	t.Setenv("GOOGLE_API_KEY", "k")
	if _, err := New(Settings{}, nil); err != nil {
		t.Errorf("GOOGLE_API_KEY not accepted: %v", err)
	}
}

func TestModels(t *testing.T) {
	f := newFakeAPI(t)
	models, err := newBackend(t, f, Settings{}).Models(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []backend.ModelInfo{{Name: "gemini-3.8-flash", Description: "Gemini 3.8 Flash", ContextLength: 1048576}}
	if !slices.Equal(models, want) {
		t.Errorf("models = %+v", models)
	}
}

func TestContract(t *testing.T) {
	f := newFakeAPI(t, groundedStream...)
	f.delay = 5 * time.Millisecond
	backendtest.Run(t, func(t *testing.T) backend.Backend {
		return newBackend(t, f, Settings{})
	}, question)
}

func TestLocate(t *testing.T) {
	answer := "One. Two. Three."
	tests := []struct {
		name       string
		start, end int32
		text       string
		want       [2]int
		ok         bool
	}{
		{"offsets match", 5, 9, "Two.", [2]int{5, 9}, true},
		{"offsets from another part, text found", 0, 4, "Two.", [2]int{5, 9}, true},
		{"no text, offsets in range", 0, 4, "", [2]int{0, 4}, true},
		{"text missing", 0, 4, "Four.", [2]int{}, false},
		{"no text, offsets out of range", 10, 99, "", [2]int{}, false},
	}
	for _, tt := range tests {
		s, e, ok := locate(answer, &genai.Segment{StartIndex: tt.start, EndIndex: tt.end, Text: tt.text})
		if ok != tt.ok || (ok && [2]int{s, e} != tt.want) {
			t.Errorf("%s: got %d,%d,%v want %v,%v", tt.name, s, e, ok, tt.want, tt.ok)
		}
	}
}

func TestGroundingNil(t *testing.T) {
	if grounding("x", nil) != nil {
		t.Error("nil metadata should give nil grounding")
	}
	if grounding("x", &genai.GroundingMetadata{}) != nil {
		t.Error("empty metadata should give nil grounding")
	}
}

// TestLive calls the real Gemini API. Run with RC_LIVE=1 and GEMINI_API_KEY.
func TestLive(t *testing.T) {
	if os.Getenv("RC_LIVE") != "1" || os.Getenv("GEMINI_API_KEY") == "" {
		t.Skip("set RC_LIVE=1 and GEMINI_API_KEY to run")
	}
	b, err := New(Settings{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var g *chat.Grounding
	var answer strings.Builder
	for ev, err := range b.Chat(t.Context(), backend.Request{Messages: []chat.Message{{Role: chat.RoleUser, Text: "Who won the most recent FIFA World Cup? One sentence."}}}) {
		if err != nil {
			t.Fatal(err)
		}
		switch ev.Kind {
		case backend.EventTextDelta:
			answer.WriteString(ev.Text)
		case backend.EventGrounding:
			g = ev.Grounding
		}
	}
	t.Logf("answer: %s\n%s", render.Cite(answer.String(), g), render.Sources(g))
	if g == nil || len(g.Sources) == 0 {
		t.Error("expected grounded sources")
	}
}
