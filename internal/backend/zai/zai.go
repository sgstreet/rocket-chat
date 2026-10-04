// Package zai implements a backend for the Z.ai API, which serves the GLM
// models.
package zai

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/chat"
)

// Name is the name the backend is registered under.
const Name = "zai"

// DefaultModel is used when neither the request nor the config names one.
const DefaultModel = "glm-5.3"

// The API endpoints. Pay-as-you-go keys use the general one; GLM Coding
// Plan subscriptions use the coding one.
const (
	GeneralURL = "https://api.z.ai/api/paas/v4"
	CodingURL  = "https://api.z.ai/api/coding/paas/v4"
)

// generalURL and codingURL are the endpoints used; tests change them.
var (
	generalURL = GeneralURL
	codingURL  = CodingURL
)

// Endpoint settings.
const (
	EndpointAuto    = "auto"
	EndpointGeneral = "general"
	EndpointCoding  = "coding"
)

// codeNoBalance is Z.ai's error code for a key with no balance or resource
// package on the endpoint used.
const codeNoBalance = "1113"

var keyInfo = backend.KeyInfo{
	Description: "Z.ai API key",
	URL:         "https://z.ai/manage-apikey/apikey-list",
	Env:         []string{"ZAI_API_KEY"},
	Required:    true,
}

func init() {
	backend.Register(Name, func(decode func(any) error) (backend.Backend, error) {
		var s Settings
		if err := decode(&s); err != nil {
			return nil, err
		}
		return New(s, nil)
	})
	backend.RegisterKey(Name, keyInfo)
}

// Settings is the backends.zai config section.
type Settings struct {
	// APIKey is the Z.ai API key. ZAI_API_KEY, when set, takes precedence.
	APIKey string `json:"api_key"`
	// Model is used when the request does not name one (default
	// DefaultModel).
	Model string `json:"model"`
	// Endpoint is EndpointAuto (default), EndpointGeneral, EndpointCoding,
	// or a base URL. Auto uses the general endpoint and moves to the coding
	// one when the key has no balance there, as with a GLM Coding Plan.
	Endpoint string `json:"endpoint"`
	// Temperature overrides the model's default when set.
	Temperature *float64 `json:"temperature"`
	// Think turns the model's reasoning on or off. When unset, the model's
	// default applies.
	Think *bool `json:"think"`
	// Search configures Z.ai web search.
	Search SearchSettings `json:"search"`
}

// SearchSettings is the backends.zai.search config section.
type SearchSettings struct {
	// Enabled turns web search on when the request does not say. Off by
	// default because searches count against the plan.
	Enabled bool `json:"enabled"`
	// Engine is the search engine (default "search-prime").
	Engine string `json:"engine"`
	// Count is the number of results, 1-50 (default 5).
	Count int `json:"count"`
	// Recency limits results by age: oneDay, oneWeek, oneMonth, oneYear or
	// noLimit (default).
	Recency string `json:"recency"`
}

// Backend talks to the Z.ai API.
type Backend struct {
	http     *http.Client
	key      string
	settings Settings

	// mu guards url, which auto mode may move to the coding endpoint.
	mu   sync.Mutex
	url  string
	auto bool
}

// New creates a backend. A nil httpClient uses http.DefaultClient.
func New(s Settings, httpClient *http.Client) (*Backend, error) {
	key, _ := keyInfo.Resolve(s.APIKey)
	if key == "" {
		return nil, fmt.Errorf("%w: save one with `rocket-chat --set-key zai` (or /key zai in the chat), "+
			"or set ZAI_API_KEY; create a key at %s", backend.ErrNoAPIKey, keyInfo.URL)
	}
	b := &Backend{http: cmp.Or(httpClient, http.DefaultClient), key: key, settings: s}
	switch e := strings.TrimSpace(s.Endpoint); e {
	case "", EndpointAuto:
		b.url, b.auto = generalURL, true
	case EndpointGeneral:
		b.url = generalURL
	case EndpointCoding:
		b.url = codingURL
	default:
		if !strings.Contains(e, "://") {
			return nil, fmt.Errorf("invalid endpoint %q: use %q, %q, %q or a URL", e, EndpointAuto, EndpointGeneral, EndpointCoding)
		}
		b.url = strings.TrimRight(e, "/")
	}
	return b, nil
}

func (b *Backend) Name() string { return Name }

func (b *Backend) Capabilities() backend.Capabilities {
	return backend.Capabilities{
		DefaultModel:    cmp.Or(b.settings.Model, DefaultModel),
		Thinking:        true,
		WebSearch:       true,
		SearchByDefault: b.settings.Search.Enabled,
	}
}

func (b *Backend) baseURL() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.url
}

// useCoding moves an auto endpoint from the general to the coding one,
// reporting whether it did.
func (b *Backend) useCoding() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.auto || b.url != generalURL {
		return false
	}
	b.url = codingURL
	return true
}

func (b *Backend) Models(ctx context.Context) ([]backend.ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL()+"/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.do(req)
	if err != nil {
		return nil, b.explain(ctx, err, "")
	}
	defer func() { _ = resp.Body.Close() }()
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("reading the Z.ai model list: %w", err)
	}
	models := make([]backend.ModelInfo, 0, len(list.Data))
	for _, m := range list.Data {
		models = append(models, backend.ModelInfo{Name: m.ID})
	}
	return models, nil
}

func (b *Backend) Chat(ctx context.Context, req backend.Request) iter.Seq2[backend.Event, error] {
	return func(yield func(backend.Event, error) bool) {
		model := cmp.Or(req.Model, b.settings.Model, DefaultModel)
		body, err := json.Marshal(b.chatRequest(model, req))
		if err != nil {
			yield(backend.Event{}, err)
			return
		}
		resp, err := b.post(ctx, body)
		if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr.Code == codeNoBalance && b.useCoding() {
			// A GLM Coding Plan key: try the coding endpoint.
			if resp, err = b.post(ctx, body); err == nil {
				notice := "Using the Z.ai GLM Coding Plan endpoint; set backends.zai.endpoint to \"coding\" to skip this check."
				if !yield(backend.Event{Kind: backend.EventNotice, Text: notice}, nil) {
					_ = resp.Body.Close()
					return
				}
			}
		}
		if err != nil {
			yield(backend.Event{}, b.explain(ctx, err, model))
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if err := b.stream(ctx, resp.Body, yield); err != nil {
			yield(backend.Event{}, b.explain(ctx, err, model))
		}
	}
}

func (b *Backend) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL()+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	return b.do(req)
}

// do sends req with the API key and turns error responses into *APIError.
func (b *Backend) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+b.key)
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return nil, parseError(resp.StatusCode, data)
}

// APIError is an error the Z.ai API reported.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	msg := cmp.Or(e.Message, http.StatusText(e.Status))
	if e.Code != "" {
		return fmt.Sprintf("%s (code %s)", msg, e.Code)
	}
	return msg
}

func parseError(status int, data []byte) error {
	var body struct {
		Error *struct {
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
		} `json:"error"`
	}
	e := &APIError{Status: status}
	if json.Unmarshal(data, &body) == nil && body.Error != nil {
		e.Message = body.Error.Message
		e.Code = strings.Trim(string(body.Error.Code), `"`)
	} else if s := strings.TrimSpace(string(data)); s != "" && len(s) < 300 {
		e.Message = s
	}
	return e
}

// explain turns errors into messages that say what to do next.
func (b *Backend) explain(ctx context.Context, err error, model string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok {
		return fmt.Errorf("zai: %w", err)
	}
	switch {
	case apiErr.Status == http.StatusUnauthorized:
		return fmt.Errorf("the API key was rejected by Z.ai: %w; check it, or save a new one with /key zai "+
			"(`rocket-chat --set-key zai`); keys are at %s", err, keyInfo.URL)
	case apiErr.Code == codeNoBalance:
		hint := "top up the account, or, with a GLM Coding Plan, set backends.zai.endpoint to \"coding\""
		if b.baseURL() == codingURL {
			hint = "check that the GLM Coding Plan is active, or use a pay-as-you-go key with backends.zai.endpoint set to \"general\""
		}
		return fmt.Errorf("zai: %w; %s", err, hint)
	case apiErr.Code == "1211" && model != "":
		return fmt.Errorf("model %q is not available on Z.ai; /model lists the models (%w)", model, err)
	case apiErr.Status == http.StatusTooManyRequests:
		return fmt.Errorf("limit reached on Z.ai: %w; wait a moment and try again", err)
	}
	return fmt.Errorf("zai: %w", err)
}

// chatRequest is the body of a chat completion request.
type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	Stream      bool      `json:"stream"`
	Temperature *float64  `json:"temperature,omitempty"`
	Thinking    *thinking `json:"thinking,omitempty"`
	Tools       []tool    `json:"tools,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type thinking struct {
	Type string `json:"type"`
}

type tool struct {
	Type      string     `json:"type"`
	WebSearch *webSearch `json:"web_search,omitempty"`
}

type webSearch struct {
	Enable        bool   `json:"enable"`
	SearchResult  bool   `json:"search_result"`
	SearchEngine  string `json:"search_engine,omitempty"`
	Count         int    `json:"count,omitempty"`
	RecencyFilter string `json:"search_recency_filter,omitempty"`
	SearchPrompt  string `json:"search_prompt,omitempty"`
}

// searchPrompt asks the model to cite results by number, so the sources it
// used can be told from the ones it only saw.
const searchPrompt = "Use the web search results below to answer. After each fact taken from a result, " +
	"cite it as [n], where n is the number in the result's ref_n label.\n\n{search_result}"

func (b *Backend) chatRequest(model string, req backend.Request) chatRequest {
	cr := chatRequest{Model: model, Stream: true, Temperature: cmp.Or(req.Temperature, b.settings.Temperature)}
	if req.System != "" {
		cr.Messages = append(cr.Messages, message{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		cr.Messages = append(cr.Messages, message{Role: string(m.Role), Content: m.Text})
	}
	if think := cmp.Or(req.Think, b.settings.Think); think != nil {
		cr.Thinking = &thinking{Type: "disabled"}
		if *think {
			cr.Thinking.Type = "enabled"
		}
	}
	search := b.settings.Search.Enabled
	if req.Search != nil {
		search = *req.Search
	}
	if search {
		s := b.settings.Search
		count := s.Count
		if count <= 0 {
			count = 5
		}
		cr.Tools = []tool{{Type: "web_search", WebSearch: &webSearch{
			Enable:        true,
			SearchResult:  true,
			SearchEngine:  cmp.Or(s.Engine, "search-prime"),
			Count:         min(count, 50),
			RecencyFilter: s.Recency,
			SearchPrompt:  searchPrompt,
		}}}
	}
	return cr
}

// chunk is one server-sent event of a streamed reply.
type chunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	WebSearch []searchResult `json:"web_search"`
	Error     *struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
	} `json:"error"`
}

type searchResult struct {
	Refer   string `json:"refer"`
	Title   string `json:"title"`
	Link    string `json:"link"`
	Content string `json:"content"`
}

// stream reads the server-sent events of a reply and yields its events.
func (b *Backend) stream(ctx context.Context, body io.Reader, yield func(backend.Event, error) bool) error {
	var (
		answer  strings.Builder
		results []searchResult
		usage   *backend.Usage
	)
	r := bufio.NewReader(body)
read:
	for {
		line, readErr := r.ReadString('\n')
		if data, ok := strings.CutPrefix(strings.TrimSpace(line), "data:"); ok {
			data = strings.TrimSpace(data)
			if data == "[DONE]" {
				break
			}
			var c chunk
			if err := json.Unmarshal([]byte(data), &c); err != nil {
				return fmt.Errorf("reading the reply: %w", err)
			}
			if c.Error != nil {
				return &APIError{Code: strings.Trim(string(c.Error.Code), `"`), Message: c.Error.Message}
			}
			if len(c.WebSearch) > 0 {
				results = c.WebSearch
			}
			if c.Usage != nil {
				usage = &backend.Usage{InputTokens: c.Usage.PromptTokens, OutputTokens: c.Usage.CompletionTokens}
			}
			for _, ch := range c.Choices {
				if t := ch.Delta.ReasoningContent; t != "" && !yield(backend.Event{Kind: backend.EventThinkingDelta, Text: t}, nil) {
					return nil
				}
				if t := ch.Delta.Content; t != "" {
					answer.WriteString(t)
					if !yield(backend.Event{Kind: backend.EventTextDelta, Text: t}, nil) {
						return nil
					}
				}
			}
		}
		switch {
		case errors.Is(readErr, io.EOF):
			break read // the stream ended without [DONE]
		case readErr != nil && ctx.Err() != nil:
			return ctx.Err()
		case readErr != nil:
			return readErr
		}
	}
	if g := grounding(results, answer.String()); g != nil && !yield(backend.Event{Kind: backend.EventGrounding, Grounding: g}, nil) {
		return nil
	}
	if usage != nil && !yield(backend.Event{Kind: backend.EventUsage, Usage: usage}, nil) {
		return nil
	}
	yield(backend.Event{Kind: backend.EventDone}, nil)
	return nil
}

var referNumber = regexp.MustCompile(`(\d+)$`)

// snippetRunes is how much of a search result is kept as its snippet.
const snippetRunes = 300

// grounding returns the search results as sources in the order of their
// numbers, marking the ones the answer cites.
func grounding(results []searchResult, answer string) *chat.Grounding {
	if len(results) == 0 {
		return nil
	}
	type numbered struct {
		n   int
		src chat.Source
	}
	list := make([]numbered, 0, len(results))
	for i, r := range results {
		n := i + 1
		if m := referNumber.FindString(r.Refer); m != "" {
			n, _ = strconv.Atoi(m)
		}
		snippet := []rune(strings.TrimSpace(r.Content))
		if len(snippet) > snippetRunes {
			snippet = append(snippet[:snippetRunes], '…')
		}
		list = append(list, numbered{n, chat.Source{Title: strings.TrimSpace(r.Title), URL: r.Link, Snippet: string(snippet)}})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].n < list[j].n })
	cited := map[int]bool{}
	for _, n := range backend.CitedNumbers(answer) {
		cited[n] = true
	}
	g := &chat.Grounding{}
	for _, s := range list {
		s.src.Cited = cited[s.n]
		g.Sources = append(g.Sources, s.src)
	}
	return g
}
