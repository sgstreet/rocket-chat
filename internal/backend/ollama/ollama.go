// Package ollama implements a backend for a local or remote Ollama server.
package ollama

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
	modeltypes "github.com/ollama/ollama/types/model"

	"github.com/sgstreet/rocket-chat/internal/backend"
)

// Name is the name the backend is registered under.
const Name = "ollama"

func init() {
	backend.Register(Name, func(decode func(any) error) (backend.Backend, error) {
		var s Settings
		if err := decode(&s); err != nil {
			return nil, err
		}
		return New(s, nil)
	})
}

// Settings is the backends.ollama config section.
type Settings struct {
	// Host is the server URL. Defaults to $OLLAMA_HOST, then
	// http://127.0.0.1:11434.
	Host string `yaml:"host"`
	// Model is used when the request does not name one.
	Model string `yaml:"model"`
	// Temperature overrides the model's default when set.
	Temperature *float64 `yaml:"temperature"`
	// NumCtx sets the context window size in tokens when non-zero.
	NumCtx int `yaml:"num_ctx"`
	// KeepAlive is how long the model stays loaded after a request.
	KeepAlive *time.Duration `yaml:"keep_alive"`
	// Think enables or disables reasoning for models that support it. When
	// unset, the model's default applies.
	Think *bool `yaml:"think"`
	// Search configures Ollama web search.
	Search SearchSettings `yaml:"search"`
}

// Backend talks to an Ollama server.
type Backend struct {
	client   *api.Client
	http     *http.Client
	host     *url.URL
	settings Settings
	now      func() time.Time
	// toolSupport caches whether each model supports tool calling.
	toolSupport sync.Map
}

// New creates a backend. A nil httpClient uses http.DefaultClient.
func New(s Settings, httpClient *http.Client) (*Backend, error) {
	host := envconfig.Host()
	if s.Host != "" {
		raw := s.Host
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid host %q: %w", s.Host, err)
		}
		host = u
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Backend{
		client:   api.NewClient(host, httpClient),
		http:     httpClient,
		host:     host,
		settings: s,
		now:      time.Now,
	}, nil
}

func (b *Backend) Name() string { return Name }

func (b *Backend) Capabilities() backend.Capabilities {
	return backend.Capabilities{
		DefaultModel:    b.settings.Model,
		Thinking:        true,
		WebSearch:       true,
		SearchByDefault: b.settings.Search.Enabled,
	}
}

func (b *Backend) Models(ctx context.Context) ([]backend.ModelInfo, error) {
	resp, err := b.client.List(ctx)
	if err != nil {
		return nil, b.explain(ctx, err, "")
	}
	models := make([]backend.ModelInfo, 0, len(resp.Models))
	for _, m := range resp.Models {
		var desc []string
		for _, s := range []string{m.Details.Family, m.Details.ParameterSize, m.Details.QuantizationLevel} {
			if s != "" {
				desc = append(desc, s)
			}
		}
		models = append(models, backend.ModelInfo{
			Name:          m.Name,
			Description:   strings.Join(desc, " "),
			ContextLength: m.Details.ContextLength,
		})
	}
	return models, nil
}

// errStopped ends a streaming callback when the consumer stops iterating.
var errStopped = errors.New("stopped")

func (b *Backend) Chat(ctx context.Context, req backend.Request) iter.Seq2[backend.Event, error] {
	return func(yield func(backend.Event, error) bool) {
		model := cmp.Or(req.Model, b.settings.Model)
		if model == "" {
			yield(backend.Event{}, b.noModelError(ctx))
			return
		}

		stopped := false
		emit := func(ev backend.Event) error {
			if !yield(ev, nil) {
				stopped = true
				return errStopped
			}
			return nil
		}
		err := b.chat(ctx, model, req, emit)
		if err != nil && !stopped {
			yield(backend.Event{}, b.explain(ctx, err, model))
		}
	}
}

// emitFunc sends an event to the consumer. It returns errStopped when the
// consumer has stopped iterating.
type emitFunc func(backend.Event) error

func (b *Backend) chat(ctx context.Context, model string, req backend.Request, emit emitFunc) error {
	cr := b.chatRequest(model, req)
	search := b.settings.Search.Enabled
	if req.Search != nil {
		search = *req.Search
	}
	if search {
		return b.chatWithSearch(ctx, model, cr, emit)
	}
	r, err := b.streamRound(ctx, cr, emit)
	if err != nil {
		return err
	}
	if err := emit(backend.Event{Kind: backend.EventUsage, Usage: &r.usage}); err != nil {
		return err
	}
	return emit(backend.Event{Kind: backend.EventDone})
}

// round is the outcome of one /api/chat call.
type round struct {
	content   string
	thinking  string
	toolCalls []api.ToolCall
	usage     backend.Usage
}

// streamRound runs one /api/chat call, emitting thinking and text deltas.
func (b *Backend) streamRound(ctx context.Context, cr *api.ChatRequest, emit emitFunc) (round, error) {
	var (
		r                 round
		content, thinking strings.Builder
	)
	err := b.client.Chat(ctx, cr, func(resp api.ChatResponse) error {
		if t := resp.Message.Thinking; t != "" {
			thinking.WriteString(t)
			if err := emit(backend.Event{Kind: backend.EventThinkingDelta, Text: t}); err != nil {
				return err
			}
		}
		if c := resp.Message.Content; c != "" {
			content.WriteString(c)
			if err := emit(backend.Event{Kind: backend.EventTextDelta, Text: c}); err != nil {
				return err
			}
		}
		r.toolCalls = append(r.toolCalls, resp.Message.ToolCalls...)
		if resp.Done {
			r.usage = backend.Usage{InputTokens: resp.PromptEvalCount, OutputTokens: resp.EvalCount}
		}
		return nil
	})
	r.content, r.thinking = content.String(), thinking.String()
	return r, err
}

// chatWithSearch runs the tool-calling loop: the model may call web_search
// and web_fetch up to MaxRounds times before it must answer.
func (b *Backend) chatWithSearch(ctx context.Context, model string, cr *api.ChatRequest, emit emitFunc) error {
	s := b.settings.Search.withDefaults()
	ok, err := b.supportsTools(ctx, model)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("model %q does not support tool calling, which web search needs; "+
			"choose a model with tools support or turn search off (--search=false)", model)
	}
	searcher, err := newSearcher(s.Mode, s.APIURL, b.client, b.http)
	if err != nil {
		return err
	}
	turn := newSearchTurn(s, searcher)

	prompt := searchPrompt(b.now())
	if len(cr.Messages) > 0 && cr.Messages[0].Role == "system" {
		cr.Messages[0].Content += "\n\n" + prompt
	} else {
		cr.Messages = append([]api.Message{{Role: "system", Content: prompt}}, cr.Messages...)
	}
	if b.settings.NumCtx < s.NumCtx {
		cr.Options["num_ctx"] = s.NumCtx
	}

	var (
		usage  backend.Usage
		answer strings.Builder
	)
	for n := 0; ; n++ {
		cr.Tools = nil
		if n < s.MaxRounds {
			cr.Tools = webTools
		}
		// Separate text from earlier rounds from this round's text.
		roundEmit := emit
		if answer.Len() > 0 && !strings.HasSuffix(answer.String(), "\n") {
			first := true
			roundEmit = func(ev backend.Event) error {
				if ev.Kind == backend.EventTextDelta && first {
					first = false
					answer.WriteString("\n\n")
					if err := emit(backend.Event{Kind: backend.EventTextDelta, Text: "\n\n"}); err != nil {
						return err
					}
				}
				return emit(ev)
			}
		}
		r, err := b.streamRound(ctx, cr, roundEmit)
		if err != nil {
			return err
		}
		answer.WriteString(r.content)
		usage.InputTokens += r.usage.InputTokens
		usage.OutputTokens += r.usage.OutputTokens
		if len(r.toolCalls) == 0 || cr.Tools == nil {
			break
		}

		cr.Messages = append(cr.Messages, api.Message{
			Role: "assistant", Content: r.content, Thinking: r.thinking, ToolCalls: r.toolCalls,
		})
		for _, call := range r.toolCalls {
			fn := call.Function
			switch fn.Name {
			case toolWebSearch:
				err = emit(backend.Event{Kind: backend.EventSearchStarted, Query: stringArg(fn.Arguments, "query")})
			case toolWebFetch:
				err = emit(backend.Event{Kind: backend.EventFetchStarted, URL: stringArg(fn.Arguments, "url")})
			}
			if err != nil {
				return err
			}
			text, err := turn.call(ctx, fn.Name, fn.Arguments)
			if err != nil {
				return err
			}
			cr.Messages = append(cr.Messages, api.Message{
				Role: "tool", ToolName: fn.Name, ToolCallID: call.ID, Content: text,
			})
		}
	}

	usage.SearchQueries = len(turn.queries)
	if g := turn.grounding(answer.String()); g != nil {
		if err := emit(backend.Event{Kind: backend.EventGrounding, Grounding: g}); err != nil {
			return err
		}
	}
	if err := emit(backend.Event{Kind: backend.EventUsage, Usage: &usage}); err != nil {
		return err
	}
	return emit(backend.Event{Kind: backend.EventDone})
}

func stringArg(args api.ToolCallFunctionArguments, key string) string {
	v, _ := args.Get(key)
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// supportsTools reports whether model advertises the tools capability.
func (b *Backend) supportsTools(ctx context.Context, model string) (bool, error) {
	if v, ok := b.toolSupport.Load(model); ok {
		return v.(bool), nil
	}
	resp, err := b.client.Show(ctx, &api.ShowRequest{Model: model})
	if err != nil {
		return false, err
	}
	ok := slices.Contains(resp.Capabilities, modeltypes.CapabilityTools)
	b.toolSupport.Store(model, ok)
	return ok, nil
}

func (b *Backend) chatRequest(model string, req backend.Request) *api.ChatRequest {
	messages := make([]api.Message, 0, len(req.Messages)+1)
	if req.System != "" {
		messages = append(messages, api.Message{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		messages = append(messages, api.Message{Role: string(m.Role), Content: m.Text})
	}

	options := map[string]any{}
	if t := cmp.Or(req.Temperature, b.settings.Temperature); t != nil {
		options["temperature"] = *t
	}
	if b.settings.NumCtx > 0 {
		options["num_ctx"] = b.settings.NumCtx
	}

	cr := &api.ChatRequest{Model: model, Messages: messages, Options: options}
	if think := cmp.Or(req.Think, b.settings.Think); think != nil {
		cr.Think = &api.ThinkValue{Value: *think}
	}
	if b.settings.KeepAlive != nil {
		cr.KeepAlive = &api.Duration{Duration: *b.settings.KeepAlive}
	}
	return cr
}

// explain turns client errors into messages that say what to do next.
func (b *Backend) explain(ctx context.Context, err error, model string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var status api.StatusError
	if errors.As(err, &status) && status.StatusCode == http.StatusNotFound && model != "" {
		return fmt.Errorf("model %q not found on %s; download it with `ollama pull %s`", model, b.host, model)
	}
	var opErr *net.OpError
	if errors.Is(err, syscall.ECONNREFUSED) || (errors.As(err, &opErr) && opErr.Op == "dial") {
		return fmt.Errorf("cannot reach Ollama at %s (is `ollama serve` running?): %w", b.host, err)
	}
	return err
}

func (b *Backend) noModelError(ctx context.Context) error {
	models, err := b.Models(ctx)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		return errors.New("no models installed; download one with `ollama pull <model>`")
	}
	names := make([]string, len(models))
	for i, m := range models {
		names[i] = m.Name
	}
	return fmt.Errorf("no model selected; pass -m or set backends.ollama.model (installed: %s)", strings.Join(names, ", "))
}
