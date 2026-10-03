package ollama

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ollama/ollama/api"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/backend/backendtest"
	"github.com/sgstreet/rocket-chat/internal/chat"
	"github.com/sgstreet/rocket-chat/internal/config"
)

// fakeServer imitates the parts of the Ollama API the backend uses.
type fakeServer struct {
	*httptest.Server
	chunks []api.ChatResponse
	// rounds, when set, scripts successive /api/chat calls; the last entry
	// repeats.
	rounds [][]api.ChatResponse
	delay  time.Duration
	models []api.ListModelResponse
	// capabilities maps a model to what /api/show reports. Models not
	// listed report completion and tools.
	capabilities map[string][]string
	// searchStatus, when non-zero, is returned by the web search routes.
	searchStatus int
	searchHits   int

	mu       sync.Mutex
	requests []api.ChatRequest
}

func newFakeServer(t *testing.T, chunks ...api.ChatResponse) *fakeServer {
	t.Helper()
	fs := &fakeServer{chunks: chunks}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/chat", fs.chat)
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(api.ListResponse{Models: fs.models})
	})
	mux.HandleFunc("POST /api/show", fs.show)
	mux.HandleFunc("POST /api/experimental/web_search", fs.webSearch)
	mux.HandleFunc("POST /api/experimental/web_fetch", fs.webFetch)
	fs.Server = httptest.NewServer(mux)
	t.Cleanup(fs.Close)
	return fs
}

func (fs *fakeServer) chat(w http.ResponseWriter, r *http.Request) {
	var req api.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	fs.mu.Lock()
	fs.requests = append(fs.requests, req)
	fs.mu.Unlock()

	if req.Model == "missing" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model 'missing' not found"}`))
		return
	}
	chunks := fs.chunks
	if fs.rounds != nil {
		fs.mu.Lock()
		i := min(len(fs.requests), len(fs.rounds)) - 1
		fs.mu.Unlock()
		chunks = fs.rounds[i]
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	for _, c := range chunks {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(fs.delay):
		}
		_ = enc.Encode(c)
		w.(http.Flusher).Flush()
	}
}

func (fs *fakeServer) show(w http.ResponseWriter, r *http.Request) {
	var req api.ShowRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	caps, ok := fs.capabilities[req.Model]
	if !ok {
		caps = []string{"completion", "tools"}
	}
	resp := map[string]any{"capabilities": caps}
	_ = json.NewEncoder(w).Encode(resp)
}

func (fs *fakeServer) webSearch(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	fs.searchHits++
	fs.mu.Unlock()
	if fs.searchStatus != 0 {
		w.WriteHeader(fs.searchStatus)
		_, _ = w.Write([]byte(`{"error":"search failed"}`))
		return
	}
	var req api.WebSearchRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	_ = json.NewEncoder(w).Encode(searchResults(req.Query))
}

func (fs *fakeServer) webFetch(w http.ResponseWriter, r *http.Request) {
	var req api.WebFetchRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	_ = json.NewEncoder(w).Encode(api.WebFetchResponse{Title: "Fetched " + req.URL, Content: "Full page text."})
}

func searchResults(query string) api.WebSearchResponse {
	return api.WebSearchResponse{Results: []api.WebSearchResult{
		{Title: "First for " + query, URL: "https://one.example", Content: "one"},
		{Title: "Second for " + query, URL: "https://two.example", Content: "two"},
	}}
}

func (fs *fakeServer) chatRequests() []api.ChatRequest {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]api.ChatRequest(nil), fs.requests...)
}

func (fs *fakeServer) lastRequest(t *testing.T) api.ChatRequest {
	t.Helper()
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(fs.requests) == 0 {
		t.Fatal("no chat requests received")
	}
	return fs.requests[len(fs.requests)-1]
}

func newBackend(t *testing.T, s Settings) *Backend {
	t.Helper()
	b, err := New(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var standardChunks = []api.ChatResponse{
	{Message: api.Message{Role: "assistant", Thinking: "hmm"}},
	{Message: api.Message{Role: "assistant", Content: "Hello"}},
	{Message: api.Message{Role: "assistant", Content: " world"}},
	{Done: true, DoneReason: "stop", Metrics: api.Metrics{PromptEvalCount: 12, EvalCount: 3}},
}

var helloReq = backend.Request{
	Model:    "qwen3",
	Messages: []chat.Message{{Role: chat.RoleUser, Text: "hi"}},
}

func TestContract(t *testing.T) {
	fs := newFakeServer(t, standardChunks...)
	fs.delay = 5 * time.Millisecond
	backendtest.Run(t, func(t *testing.T) backend.Backend {
		fs.models = []api.ListModelResponse{{Name: "qwen3"}}
		return newBackend(t, Settings{Host: fs.URL})
	}, helloReq)
}

func TestChatStream(t *testing.T) {
	fs := newFakeServer(t, standardChunks...)
	b := newBackend(t, Settings{Host: fs.URL})

	var events []backend.Event
	for ev, err := range b.Chat(t.Context(), helloReq) {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
	want := []backend.Event{
		{Kind: backend.EventThinkingDelta, Text: "hmm"},
		{Kind: backend.EventTextDelta, Text: "Hello"},
		{Kind: backend.EventTextDelta, Text: " world"},
		{Kind: backend.EventUsage, Usage: &backend.Usage{InputTokens: 12, OutputTokens: 3}},
		{Kind: backend.EventDone},
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events %+v, want %d", len(events), events, len(want))
	}
	for i := range want {
		got, w := events[i], want[i]
		if got.Kind != w.Kind || got.Text != w.Text {
			t.Errorf("event %d = %v %q, want %v %q", i, got.Kind, got.Text, w.Kind, w.Text)
		}
		if w.Usage != nil && (got.Usage == nil || *got.Usage != *w.Usage) {
			t.Errorf("usage = %+v, want %+v", got.Usage, w.Usage)
		}
	}
}

func TestChatRequest(t *testing.T) {
	fs := newFakeServer(t, standardChunks...)
	temp, numCtx, keep, think := 0.2, 32768, config.Duration(10*time.Minute), false
	b := newBackend(t, Settings{
		Host:        strings.TrimPrefix(fs.URL, "http://"), // scheme is optional
		Model:       "settings-model",
		Temperature: &temp,
		NumCtx:      numCtx,
		KeepAlive:   &keep,
		Think:       &think,
	})
	reqTemp := 0.9
	req := backend.Request{
		System:      "be brief",
		Temperature: &reqTemp,
		Messages: []chat.Message{
			{Role: chat.RoleUser, Text: "q1"},
			{Role: chat.RoleAssistant, Text: "a1"},
			{Role: chat.RoleUser, Text: "q2"},
		},
	}
	for _, err := range b.Chat(t.Context(), req) {
		if err != nil {
			t.Fatal(err)
		}
	}

	got := fs.lastRequest(t)
	if got.Model != "settings-model" {
		t.Errorf("model = %q, want settings default", got.Model)
	}
	var roles []string
	for _, m := range got.Messages {
		roles = append(roles, m.Role+":"+m.Content)
	}
	if want := "system:be brief,user:q1,assistant:a1,user:q2"; strings.Join(roles, ",") != want {
		t.Errorf("messages = %v, want %s", roles, want)
	}
	if got.Options["temperature"] != 0.9 {
		t.Errorf("temperature = %v, want request override 0.9", got.Options["temperature"])
	}
	if got.Options["num_ctx"] != float64(numCtx) {
		t.Errorf("num_ctx = %v", got.Options["num_ctx"])
	}
	if got.KeepAlive == nil || got.KeepAlive.Duration != time.Duration(keep) {
		t.Errorf("keep_alive = %v", got.KeepAlive)
	}
	if got.Think == nil || got.Think.Value != false {
		t.Errorf("think = %v, want false", got.Think)
	}
}

func TestThinkOmittedByDefault(t *testing.T) {
	// Sending think to a model without thinking support is an error, so it
	// must only be sent when configured.
	fs := newFakeServer(t, standardChunks...)
	b := newBackend(t, Settings{Host: fs.URL})
	for range b.Chat(t.Context(), helloReq) {
	}
	if got := fs.lastRequest(t); got.Think != nil || len(got.Options) != 0 {
		t.Errorf("think = %v, options = %v; want both unset", got.Think, got.Options)
	}
}

func chatErr(t *testing.T, b *Backend, req backend.Request) error {
	t.Helper()
	for ev, err := range b.Chat(t.Context(), req) {
		if err != nil {
			return err
		}
		if ev.Kind == backend.EventDone {
			t.Fatal("got Done, want an error")
		}
	}
	t.Fatal("stream ended without an error")
	return nil
}

func TestModelNotFound(t *testing.T) {
	fs := newFakeServer(t)
	b := newBackend(t, Settings{Host: fs.URL})
	err := chatErr(t, b, backend.Request{Model: "missing", Messages: helloReq.Messages})
	if !strings.Contains(err.Error(), "ollama pull missing") {
		t.Errorf("error = %v, want pull hint", err)
	}
}

func TestServerDown(t *testing.T) {
	fs := newFakeServer(t)
	url := fs.URL
	fs.Close()
	off := false
	b := newBackend(t, Settings{Host: url, Serve: ServeSettings{AutoStart: &off}})
	err := chatErr(t, b, helloReq)
	if !strings.Contains(err.Error(), "is `ollama serve` running?") || !strings.Contains(err.Error(), "serve.auto_start") {
		t.Errorf("error = %v, want server and auto_start hints", err)
	}
}

func TestNoModelSelected(t *testing.T) {
	fs := newFakeServer(t)
	b := newBackend(t, Settings{Host: fs.URL})
	req := backend.Request{Messages: helloReq.Messages}

	if err := chatErr(t, b, req); !strings.Contains(err.Error(), "no models installed") {
		t.Errorf("error = %v, want no models installed", err)
	}
	fs.models = []api.ListModelResponse{{Name: "qwen3:4b"}, {Name: "llama3.2"}}
	if err := chatErr(t, b, req); !strings.Contains(err.Error(), "installed: qwen3:4b, llama3.2") {
		t.Errorf("error = %v, want installed list", err)
	}
}

func TestModels(t *testing.T) {
	fs := newFakeServer(t)
	fs.models = []api.ListModelResponse{{
		Name:    "qwen3:4b",
		Details: api.ModelDetails{Family: "qwen3", ParameterSize: "4.0B", QuantizationLevel: "Q4_K_M", ContextLength: 40960},
	}}
	models, err := newBackend(t, Settings{Host: fs.URL}).Models(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := backend.ModelInfo{Name: "qwen3:4b", Description: "qwen3 4.0B Q4_K_M", ContextLength: 40960}
	if len(models) != 1 || models[0] != want {
		t.Errorf("models = %+v, want [%+v]", models, want)
	}
}

func TestRegistered(t *testing.T) {
	b, err := backend.Open(Name, func(v any) error {
		v.(*Settings).Host = "http://example.invalid:1234"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := b.(*Backend).host.String(); got != "http://example.invalid:1234" {
		t.Errorf("host = %q", got)
	}
}

// TestLive talks to a real Ollama server. Run with RC_LIVE=1 and
// RC_OLLAMA_MODEL set to an installed model.
func TestLive(t *testing.T) {
	model := os.Getenv("RC_OLLAMA_MODEL")
	if os.Getenv("RC_LIVE") != "1" || model == "" {
		t.Skip("set RC_LIVE=1 and RC_OLLAMA_MODEL to run")
	}
	b := newBackend(t, Settings{})
	var text strings.Builder
	for ev, err := range b.Chat(t.Context(), backend.Request{
		Model:    model,
		Messages: []chat.Message{{Role: chat.RoleUser, Text: "Reply with the single word: pong"}},
	}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == backend.EventTextDelta {
			text.WriteString(ev.Text)
		}
	}
	if !strings.Contains(strings.ToLower(text.String()), "pong") {
		t.Errorf("reply = %q", text.String())
	}
}
