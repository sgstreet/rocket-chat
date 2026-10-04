// Package ollama implements a backend for a local or remote Ollama server.
package ollama

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
	modeltypes "github.com/ollama/ollama/types/model"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/config"
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
	backend.RegisterKey(Name, keyInfo)
}

// Settings is the backends.ollama config section.
type Settings struct {
	// Host is the server URL. Defaults to $OLLAMA_HOST, then
	// http://127.0.0.1:11434.
	Host string `json:"host"`
	// Model is used when the request does not name one.
	Model string `json:"model"`
	// Temperature overrides the model's default when set.
	Temperature *float64 `json:"temperature"`
	// NumCtx sets the context window size in tokens when non-zero.
	NumCtx int `json:"num_ctx"`
	// KeepAlive is how long the model stays loaded after a request.
	KeepAlive *config.Duration `json:"keep_alive"`
	// UnloadOnExit unloads the models rocket-chat used when it exits, so
	// their memory is freed straight away rather than after keep_alive.
	// Default true. Models another running copy of rocket-chat has used
	// stay loaded, and a server rocket-chat started is stopped instead
	// when no other copy uses it.
	UnloadOnExit *bool `json:"unload_on_exit"`
	// UnloadOnSwitch unloads a model when the chat moves to another model
	// or backend, unless another running copy of rocket-chat uses it.
	// Default true.
	UnloadOnSwitch *bool `json:"unload_on_switch"`
	// Think enables or disables reasoning for models that support it. When
	// unset, the model's default applies.
	Think *bool `json:"think"`
	// APIKey is the ollama.com API key web search uses. OLLAMA_API_KEY, when
	// set, takes precedence.
	APIKey string `json:"api_key"`
	// Search configures Ollama web search.
	Search SearchSettings `json:"search"`
	// Serve configures starting a local server when none is running.
	Serve ServeSettings `json:"serve"`
	// CloudModels adds the ollama.com cloud models to the model list.
	// Default true. Using one needs `ollama signin` on the server.
	CloudModels *bool `json:"cloud_models"`
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

	// serveMu guards the local server state.
	serveMu sync.Mutex
	// ready is set once the server has answered.
	ready bool
	// share is this backend's use of a local server, which it may have
	// started; nil for a remote host.
	share *share
	// used holds the models chatted with, which Close unloads.
	used []string

	// cloudURL lists the cloud models; "" leaves them out.
	cloudURL string
	// cloudMu guards cloudCatalog, the cloud models once fetched.
	cloudMu      sync.Mutex
	cloudCatalog []string
}

func (s Settings) unloadOnExit() bool { return s.UnloadOnExit == nil || *s.UnloadOnExit }

func (s Settings) unloadOnSwitch() bool { return s.UnloadOnSwitch == nil || *s.UnloadOnSwitch }

// unloadTimeout bounds each unload request on the way out.
const unloadTimeout = 5 * time.Second

// ensureServer makes sure the server answers before a request, starting a
// local one when it is not running and auto-start is on. It returns a
// notice for the user when it started one.
func (b *Backend) ensureServer(ctx context.Context) (string, error) {
	b.serveMu.Lock()
	defer b.serveMu.Unlock()
	if b.ready {
		return "", nil
	}
	err := b.ping(ctx)
	switch {
	case err == nil:
		b.ready = true
		if b.share != nil {
			b.share.join()
		}
		return "", nil
	case !isUnreachable(err) || !b.settings.Serve.autoStart() || b.share == nil:
		// The request itself reports the problem.
		return "", nil
	}
	started, err := b.share.start(ctx, b.settings.Serve, b.ping)
	if err != nil {
		return "", err
	}
	b.ready = true
	if !started {
		return "", nil
	}
	return fmt.Sprintf("Started a local Ollama server at %s; it stops when the last rocket-chat using it exits.", b.host), nil
}

// ping checks that the server answers.
func (b *Backend) ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return b.client.Heartbeat(ctx)
}

// Close stops a local server rocket-chat started once no other copy of
// rocket-chat uses it. Otherwise the server is left running, and the models
// this backend used are unloaded from it unless another copy has used them
// or unload_on_exit is off.
func (b *Backend) Close() error {
	b.serveMu.Lock()
	defer b.serveMu.Unlock()
	used := b.used
	b.used = nil
	b.ready = false
	var others []string
	if b.share != nil {
		var stopped bool
		if stopped, others = b.share.leave(); stopped {
			return nil
		}
	}
	if !b.settings.unloadOnExit() {
		return nil
	}
	var errs []error
	for _, model := range used {
		if slices.Contains(others, model) || isCloudModel(model) {
			continue
		}
		if _, err := b.unload(context.Background(), model); err != nil {
			errs = append(errs, fmt.Errorf("unloading %s: %w", model, err))
		}
	}
	return errors.Join(errs...)
}

// ReleaseModel unloads model, which the chat has moved away from, when
// this backend chatted with it, it runs locally, no other running copy of
// rocket-chat has used it, and unload_on_switch is on.
func (b *Backend) ReleaseModel(ctx context.Context, model string) (bool, error) {
	if !b.settings.unloadOnSwitch() || isCloudModel(model) {
		return false, nil
	}
	b.serveMu.Lock()
	i := slices.Index(b.used, model)
	if i < 0 {
		b.serveMu.Unlock()
		return false, nil // never loaded by this chat
	}
	b.used = slices.Delete(b.used, i, i+1)
	var others []string
	if b.share != nil {
		others = b.share.forget(model)
	}
	b.serveMu.Unlock()
	if slices.Contains(others, model) {
		return false, nil
	}
	return b.unload(ctx, model)
}

// unload asks the server to unload model now, reporting whether it did. A
// server that has gone away or no longer has the model has nothing to
// unload.
func (b *Backend) unload(ctx context.Context, model string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, unloadTimeout)
	defer cancel()
	err := b.client.Generate(ctx, &api.GenerateRequest{
		Model:     model,
		KeepAlive: &api.Duration{Duration: 0},
	}, func(api.GenerateResponse) error { return nil })
	var status api.StatusError
	if isUnreachable(err) || (errors.As(err, &status) && status.StatusCode == http.StatusNotFound) {
		return false, nil
	}
	return err == nil, err
}

// markUsed records that model was chatted with, for Close to unload.
func (b *Backend) markUsed(model string) {
	b.serveMu.Lock()
	defer b.serveMu.Unlock()
	if !slices.Contains(b.used, model) {
		b.used = append(b.used, model)
	}
	if b.share != nil {
		b.share.use(model)
	}
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
	b := &Backend{
		client:   api.NewClient(host, httpClient),
		http:     httpClient,
		host:     host,
		settings: s,
		now:      time.Now,
		cloudURL: defaultCloudURL,
	}
	if isLocal(host) {
		b.share = newShare(host)
	}
	return b, nil
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
	if _, err := b.ensureServer(ctx); err != nil {
		return nil, err
	}
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
		info := backend.ModelInfo{
			Name:          m.Name,
			Description:   strings.Join(desc, " "),
			ContextLength: m.Details.ContextLength,
		}
		if m.RemoteHost != "" || isCloudModel(m.Name) {
			info.Description = cloudDescription
		}
		models = append(models, info)
	}
	return b.withCloudModels(ctx, models)
}

// errStopped ends a streaming callback when the consumer stops iterating.
var errStopped = errors.New("stopped")

func (b *Backend) Chat(ctx context.Context, req backend.Request) iter.Seq2[backend.Event, error] {
	return func(yield func(backend.Event, error) bool) {
		notice, err := b.ensureServer(ctx)
		if err != nil {
			yield(backend.Event{}, err)
			return
		}
		if notice != "" && !yield(backend.Event{Kind: backend.EventNotice, Text: notice}, nil) {
			return
		}
		model := cmp.Or(req.Model, b.settings.Model)
		if model == "" {
			yield(backend.Event{}, b.noModelError(ctx))
			return
		}
		b.markUsed(model)

		stopped := false
		emit := func(ev backend.Event) error {
			if !yield(ev, nil) {
				stopped = true
				return errStopped
			}
			return nil
		}
		err = b.chat(ctx, model, req, emit)
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
			r.usage = backend.Usage{InputTokens: resp.PromptEvalCount, OutputTokens: resp.EvalCount, ContextTokens: resp.PromptEvalCount}
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
	searcher, err := newSearcher(s.Mode, s.APIURL, b.settings.APIKey, b.client, b.http)
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
		if n == 0 {
			// Later rounds add search results the next request leaves out.
			usage.ContextTokens = r.usage.ContextTokens
		}
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
		cr.KeepAlive = &api.Duration{Duration: time.Duration(*b.settings.KeepAlive)}
	}
	return cr
}

// explain turns client errors into messages that say what to do next.
func (b *Backend) explain(ctx context.Context, err error, model string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var status api.StatusError
	if auth, ok := errors.AsType[api.AuthorizationError](err); ok && isCloudModel(model) {
		hint := "run `ollama signin`"
		if auth.SigninURL != "" {
			hint += " or sign in at " + auth.SigninURL
		}
		return fmt.Errorf("cloud model %q needs the Ollama server signed in to ollama.com: %s (%w)", model, hint, err)
	}
	if errors.As(err, &status) && isCloudModel(model) {
		switch status.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("cloud model %q needs the Ollama server signed in to ollama.com: run `ollama signin` (%w)", model, err)
		case http.StatusNotFound:
			return fmt.Errorf("cloud model %q not found on %s; check the name with /model, "+
				"or, if this Ollama is too old to run cloud models without pulling them, "+
				"update it or run `ollama pull %s` (%w)", model, b.host, model, err)
		}
	}
	if errors.As(err, &status) && status.StatusCode == http.StatusNotFound && model != "" {
		return fmt.Errorf("model %q not found on %s; download it with `ollama pull %s`", model, b.host, model)
	}
	if isUnreachable(err) {
		// The server went away; check again, and start one if allowed,
		// before the next request.
		b.serveMu.Lock()
		b.ready = false
		b.serveMu.Unlock()
		hint := "is `ollama serve` running?"
		if isLocal(b.host) && !b.settings.Serve.autoStart() {
			hint += " rocket-chat can start it: set backends.ollama.serve.auto_start to true"
		}
		return fmt.Errorf("cannot reach Ollama at %s (%s): %w", b.host, hint, err)
	}
	return err
}

func (b *Backend) noModelError(ctx context.Context) error {
	models, err := b.Models(ctx)
	if _, partial := errors.AsType[*backend.PartialList](err); err != nil && !partial {
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
