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
	"strings"
	"syscall"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"

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
}

// Backend talks to an Ollama server.
type Backend struct {
	client   *api.Client
	host     *url.URL
	settings Settings
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
	return &Backend{client: api.NewClient(host, httpClient), host: host, settings: s}, nil
}

func (b *Backend) Name() string { return Name }

func (b *Backend) Capabilities() backend.Capabilities {
	return backend.Capabilities{Thinking: true}
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
		err := b.client.Chat(ctx, b.chatRequest(model, req), func(r api.ChatResponse) error {
			if r.Message.Thinking != "" {
				if err := emit(backend.Event{Kind: backend.EventThinkingDelta, Text: r.Message.Thinking}); err != nil {
					return err
				}
			}
			if r.Message.Content != "" {
				if err := emit(backend.Event{Kind: backend.EventTextDelta, Text: r.Message.Content}); err != nil {
					return err
				}
			}
			if r.Done {
				return emit(backend.Event{Kind: backend.EventUsage, Usage: &backend.Usage{
					InputTokens:  r.PromptEvalCount,
					OutputTokens: r.EvalCount,
				}})
			}
			return nil
		})
		switch {
		case stopped:
		case err != nil:
			yield(backend.Event{}, b.explain(ctx, err, model))
		default:
			yield(backend.Event{Kind: backend.EventDone}, nil)
		}
	}
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
