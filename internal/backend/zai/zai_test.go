package zai

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/backend/backendtest"
	"github.com/sgstreet/rocket-chat/internal/chat"
)

const testKey = "test-key"

// fakeZai imitates the Z.ai API on two endpoints, /general and /coding.
type fakeZai struct {
	*httptest.Server
	// noBalance makes the general endpoint answer like a GLM Coding Plan
	// key: no balance.
	noBalance bool
	// failMidStream sends an error event after the first chunk.
	failMidStream bool
	delay         time.Duration

	mu       sync.Mutex
	requests map[string][]chatRequest
}

func newFakeZai(t *testing.T) *fakeZai {
	t.Helper()
	t.Setenv("ZAI_API_KEY", "")
	fz := &fakeZai{requests: map[string][]chatRequest{}}
	mux := http.NewServeMux()
	for _, ep := range []string{"general", "coding"} {
		mux.HandleFunc("GET /"+ep+"/models", func(w http.ResponseWriter, r *http.Request) {
			if !fz.authorized(w, r) {
				return
			}
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"glm-5.3"},{"id":"glm-5.3-flash"}]}`))
		})
		mux.HandleFunc("POST /"+ep+"/chat/completions", func(w http.ResponseWriter, r *http.Request) {
			fz.chat(ep, w, r)
		})
	}
	fz.Server = httptest.NewServer(mux)
	t.Cleanup(fz.Close)
	oldGeneral, oldCoding := generalURL, codingURL
	generalURL, codingURL = fz.URL+"/general", fz.URL+"/coding"
	t.Cleanup(func() { generalURL, codingURL = oldGeneral, oldCoding })
	return fz
}

func (fz *fakeZai) authorized(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+testKey {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"1000","message":"Authentication Failed"}}`))
		return false
	}
	return true
}

func (fz *fakeZai) chat(ep string, w http.ResponseWriter, r *http.Request) {
	if !fz.authorized(w, r) {
		return
	}
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	fz.mu.Lock()
	fz.requests[ep] = append(fz.requests[ep], req)
	fz.mu.Unlock()

	switch {
	case ep == "general" && fz.noBalance:
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"1113","message":"Insufficient balance or no resource package. Please recharge."}}`))
		return
	case req.Model == "too-long":
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"1261","message":"Prompt exceeds max length"}}`))
		return
	case req.Model == "no-such-model":
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"1211","message":"Unknown Model, please check the model code."}}`))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	send := func(v string) bool {
		select {
		case <-r.Context().Done():
			return false
		case <-time.After(fz.delay):
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", v)
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	answer := "Hello world"
	if len(req.Tools) > 0 {
		answer = "Go 1.26 is out [2]."
	}
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":` + quote(answer[:5]) + `}}]}`,
	}
	if fz.failMidStream {
		chunks = append(chunks, `{"error":{"code":"1302","message":"Rate limit reached"}}`)
	}
	chunks = append(chunks, `{"choices":[{"index":0,"delta":{"role":"assistant","content":`+quote(answer[5:])+`}}]}`)
	final := `{"choices":[{"index":0,"finish_reason":"stop","delta":{"content":""}}],"usage":{"prompt_tokens":17,"completion_tokens":15}`
	if len(req.Tools) > 0 {
		final += `,"web_search":[` +
			`{"refer":"ref_2","title":"Go 1.26","link":"https://go.dev/doc/go1.26","content":"Go 1.26 is released."},` +
			`{"refer":"ref_1","title":"Go","link":"https://go.dev","content":"The Go programming language."}]`
	}
	chunks = append(chunks, final+"}", "[DONE]")
	for _, c := range chunks {
		if !send(c) {
			return
		}
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (fz *fakeZai) sent(ep string) []chatRequest {
	fz.mu.Lock()
	defer fz.mu.Unlock()
	return append([]chatRequest(nil), fz.requests[ep]...)
}

func newBackend(t *testing.T, s Settings) *Backend {
	t.Helper()
	if s.APIKey == "" {
		s.APIKey = testKey
	}
	b, err := New(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var helloReq = backend.Request{Messages: []chat.Message{{Role: chat.RoleUser, Text: "hi"}}}

type result struct {
	text, thinking string
	notices        []string
	grounding      *chat.Grounding
	usage          *backend.Usage
	err            error
}

func collect(t *testing.T, b *Backend, req backend.Request) result {
	t.Helper()
	var r result
	for ev, err := range b.Chat(t.Context(), req) {
		if err != nil {
			r.err = err
			break
		}
		switch ev.Kind {
		case backend.EventTextDelta:
			r.text += ev.Text
		case backend.EventThinkingDelta:
			r.thinking += ev.Text
		case backend.EventNotice:
			r.notices = append(r.notices, ev.Text)
		case backend.EventGrounding:
			r.grounding = ev.Grounding
		case backend.EventUsage:
			r.usage = ev.Usage
		}
	}
	return r
}

func TestContract(t *testing.T) {
	fz := newFakeZai(t)
	fz.delay = 5 * time.Millisecond
	backendtest.Run(t, func(t *testing.T) backend.Backend {
		return newBackend(t, Settings{Endpoint: EndpointCoding})
	}, helloReq)
}

func TestChat(t *testing.T) {
	fz := newFakeZai(t)
	b := newBackend(t, Settings{Endpoint: EndpointGeneral})
	req := helloReq
	req.System = "Be brief."
	r := collect(t, b, req)
	if r.err != nil || r.text != "Hello world" || r.thinking != "hmm" {
		t.Fatalf("result = %+v", r)
	}
	if r.usage == nil || r.usage.InputTokens != 17 || r.usage.OutputTokens != 15 {
		t.Errorf("usage = %+v", r.usage)
	}
	if r.grounding != nil {
		t.Errorf("grounding without search: %+v", r.grounding)
	}
	sent := fz.sent("general")
	if len(sent) != 1 {
		t.Fatalf("requests = %d", len(sent))
	}
	got := sent[0]
	if got.Model != DefaultModel || !got.Stream || len(got.Tools) != 0 || got.Thinking != nil || got.Temperature != nil {
		t.Errorf("request = %+v", got)
	}
	if len(got.Messages) != 2 || got.Messages[0] != (message{"system", "Be brief."}) || got.Messages[1] != (message{"user", "hi"}) {
		t.Errorf("messages = %+v", got.Messages)
	}
}

func TestSettingsInRequest(t *testing.T) {
	fz := newFakeZai(t)
	temp, on, off := 0.3, true, false
	b := newBackend(t, Settings{Endpoint: EndpointCoding, Model: "glm-5.3-flash", Temperature: &temp, Think: &on})
	collect(t, b, helloReq)
	req := helloReq
	req.Model, req.Think = "glm-5", &off
	collect(t, b, req)

	sent := fz.sent("coding")
	if got := sent[0]; got.Model != "glm-5.3-flash" || got.Temperature == nil || *got.Temperature != 0.3 ||
		got.Thinking == nil || got.Thinking.Type != "enabled" {
		t.Errorf("from settings: %+v", got)
	}
	if got := sent[1]; got.Model != "glm-5" || got.Thinking == nil || got.Thinking.Type != "disabled" {
		t.Errorf("from the request: %+v", got)
	}
}

func TestSearch(t *testing.T) {
	fz := newFakeZai(t)
	b := newBackend(t, Settings{Endpoint: EndpointCoding, Search: SearchSettings{Recency: "oneWeek"}})
	if b.Capabilities().SearchByDefault {
		t.Error("search on by default")
	}
	on := true
	req := helloReq
	req.Search = &on
	r := collect(t, b, req)
	if r.err != nil {
		t.Fatal(r.err)
	}
	ws := fz.sent("coding")[0].Tools
	if len(ws) != 1 || ws[0].Type != "web_search" || ws[0].WebSearch.SearchEngine != "search-prime" ||
		ws[0].WebSearch.Count != 5 || ws[0].WebSearch.RecencyFilter != "oneWeek" ||
		!strings.Contains(ws[0].WebSearch.SearchPrompt, "{search_result}") {
		t.Errorf("tools = %+v", ws)
	}
	// Sources come in number order, and [2] marks the second as cited.
	g := r.grounding
	if g == nil || len(g.Sources) != 2 || g.Sources[0].URL != "https://go.dev" || g.Sources[0].Cited ||
		g.Sources[1].URL != "https://go.dev/doc/go1.26" || !g.Sources[1].Cited || g.Sources[1].Title != "Go 1.26" {
		t.Errorf("grounding = %+v", g)
	}
}

func TestAutoEndpointMovesToCodingPlan(t *testing.T) {
	fz := newFakeZai(t)
	fz.noBalance = true
	b := newBackend(t, Settings{})
	r := collect(t, b, helloReq)
	if r.err != nil || r.text != "Hello world" {
		t.Fatalf("result = %+v", r)
	}
	if len(r.notices) != 1 || !strings.Contains(r.notices[0], "GLM Coding Plan") {
		t.Errorf("notices = %q", r.notices)
	}
	// Later requests go straight to the coding endpoint.
	if r := collect(t, b, helloReq); r.err != nil || len(r.notices) != 0 {
		t.Errorf("second chat: %+v", r)
	}
	if g, c := len(fz.sent("general")), len(fz.sent("coding")); g != 1 || c != 2 {
		t.Errorf("requests: general %d, coding %d", g, c)
	}
	if models, err := b.Models(t.Context()); err != nil || len(models) != 2 || models[0].Name != "glm-5.3" {
		t.Errorf("models = %+v, %v", models, err)
	}
}

func TestErrors(t *testing.T) {
	fz := newFakeZai(t)
	fz.noBalance = true
	for name, tc := range map[string]struct {
		s    Settings
		req  backend.Request
		want string
	}{
		"no balance": {Settings{Endpoint: EndpointGeneral}, helloReq, `set backends.zai.endpoint to "coding"`},
		"bad key":    {Settings{Endpoint: EndpointCoding, APIKey: "wrong"}, helloReq, "API key was rejected"},
		"too long": {Settings{Endpoint: EndpointCoding},
			backend.Request{Model: "too-long", Messages: helloReq.Messages}, "/compact summarizes the older messages"},
		"model": {Settings{Endpoint: EndpointCoding},
			backend.Request{Model: "no-such-model", Messages: helloReq.Messages}, `model "no-such-model" is not available`},
	} {
		t.Run(name, func(t *testing.T) {
			r := collect(t, newBackend(t, tc.s), tc.req)
			if r.err == nil || !strings.Contains(r.err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", r.err, tc.want)
			}
		})
	}

	fz.failMidStream = true
	r := collect(t, newBackend(t, Settings{Endpoint: EndpointCoding}), helloReq)
	if r.err == nil || !strings.Contains(r.err.Error(), "limit reached") || r.text != "Hello" {
		t.Errorf("mid-stream error: %+v", r)
	}
}

func TestNew(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "")
	if _, err := New(Settings{}, nil); !errors.Is(err, backend.ErrNoAPIKey) {
		t.Errorf("no key: %v", err)
	}
	if _, err := New(Settings{APIKey: "k", Endpoint: "nowhere"}, nil); err == nil {
		t.Error("bad endpoint accepted")
	}
	b, err := New(Settings{APIKey: "k", Endpoint: "https://proxy.example/v4/"}, nil)
	if err != nil || b.baseURL() != "https://proxy.example/v4" {
		t.Errorf("custom endpoint: %v, %q", err, b.baseURL())
	}
	t.Setenv("ZAI_API_KEY", "from-env")
	if b, err := New(Settings{APIKey: "saved"}, nil); err != nil || b.key != "from-env" {
		t.Error("ZAI_API_KEY does not take precedence")
	}
}

// TestLive talks to the real Z.ai API. Run with RC_LIVE=1 and ZAI_API_KEY.
func TestLive(t *testing.T) {
	if os.Getenv("RC_LIVE") == "" || os.Getenv("ZAI_API_KEY") == "" {
		t.Skip("set RC_LIVE=1 and ZAI_API_KEY to run")
	}
	b, err := New(Settings{Model: "glm-5.3-flash"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	models, err := b.Models(t.Context())
	if err != nil || len(models) == 0 {
		t.Fatalf("models: %v, %v", models, err)
	}
	on := true
	r := collect(t, b, backend.Request{Search: &on, Messages: []chat.Message{{Role: chat.RoleUser, Text: "What is the latest Go release? One sentence."}}})
	t.Logf("notices %q\ntext %q\nsources %d usage %+v", r.notices, r.text, func() int {
		if r.grounding == nil {
			return 0
		}
		return len(r.grounding.Sources)
	}(), r.usage)
	if r.err != nil || r.text == "" || r.grounding == nil {
		t.Fatalf("live chat: %+v", r)
	}
}

func TestContextWindow(t *testing.T) {
	newFakeZai(t)
	b := newBackend(t, Settings{Endpoint: EndpointCoding})
	if n, _ := b.ContextWindow(t.Context(), ""); n != 1048576 {
		t.Errorf("default model: %d", n)
	}
	if n, _ := b.ContextWindow(t.Context(), "glm-unknown"); n != 0 {
		t.Errorf("unknown model: %d", n)
	}
	b = newBackend(t, Settings{Endpoint: EndpointCoding, ContextWindow: 65536})
	if n, _ := b.ContextWindow(t.Context(), "glm-5.3"); n != 65536 {
		t.Errorf("setting: %d", n)
	}
	// With search on, the prompt includes the results, so the context
	// size is left unknown.
	on := true
	r := collect(t, b, backend.Request{Search: &on, Messages: helloReq.Messages})
	if r.usage == nil || r.usage.ContextTokens != 0 {
		t.Errorf("search usage = %+v", r.usage)
	}
	if r := collect(t, b, helloReq); r.usage == nil || r.usage.ContextTokens != 17 {
		t.Errorf("usage = %+v", r.usage)
	}
}
