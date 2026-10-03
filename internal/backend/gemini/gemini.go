// Package gemini implements a backend for the Gemini API, grounded with
// Google Search.
package gemini

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/sgstreet/rocket-chat/internal/backend"
	"github.com/sgstreet/rocket-chat/internal/chat"
	"github.com/sgstreet/rocket-chat/internal/config"
)

// Name is the name the backend is registered under.
const Name = "gemini"

// DefaultModel is used when neither the request nor the config names one.
const DefaultModel = "gemini-flash-latest"

// API key environment variables, in order of preference.
var apiKeyEnv = []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}

func init() {
	backend.Register(Name, func(decode func(any) error) (backend.Backend, error) {
		var s Settings
		if err := decode(&s); err != nil {
			return nil, err
		}
		return New(s, nil)
	})
}

// Settings is the backends.gemini config section. The API key comes from
// GEMINI_API_KEY or GOOGLE_API_KEY.
type Settings struct {
	// Model is used when the request does not name one (default
	// DefaultModel).
	Model string `json:"model"`
	// Temperature overrides the model's default when set.
	Temperature *float64 `json:"temperature"`
	// Think, when true, asks the model to return its reasoning.
	Think *bool `json:"think"`
	// Search configures Grounding with Google Search.
	Search SearchSettings `json:"search"`
	// BaseURL overrides the API endpoint (proxies, testing).
	BaseURL string `json:"base_url"`
}

// SearchSettings is the backends.gemini.search config section.
type SearchSettings struct {
	// Enabled turns Google Search grounding on when the request does not
	// say. Default true.
	Enabled *bool `json:"enabled"`
	// Since limits results to pages from this long ago until now, for
	// example 168h for the last week. Zero means no limit.
	Since config.Duration `json:"since"`
}

// Backend talks to the Gemini API.
type Backend struct {
	client   *genai.Client
	settings Settings
	now      func() time.Time
}

// New creates a backend. A nil httpClient uses the SDK default.
func New(s Settings, httpClient *http.Client) (*Backend, error) {
	var key string
	for _, env := range apiKeyEnv {
		if key = os.Getenv(env); key != "" {
			break
		}
	}
	if key == "" {
		return nil, errors.New("set GEMINI_API_KEY to a Gemini API key (create one at https://aistudio.google.com/apikey)")
	}
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:      key,
		Backend:     genai.BackendGeminiAPI,
		HTTPClient:  httpClient,
		HTTPOptions: genai.HTTPOptions{BaseURL: s.BaseURL},
	})
	if err != nil {
		return nil, err
	}
	return &Backend{client: client, settings: s, now: time.Now}, nil
}

func (b *Backend) Name() string { return Name }

func (b *Backend) Capabilities() backend.Capabilities {
	return backend.Capabilities{
		DefaultModel:      cmp.Or(b.settings.Model, DefaultModel),
		Thinking:          true,
		WebSearch:         true,
		SearchByDefault:   b.searchByDefault(),
		InlineCitations:   true,
		SearchSuggestions: true,
	}
}

func (b *Backend) searchByDefault() bool {
	return b.settings.Search.Enabled == nil || *b.settings.Search.Enabled
}

func (b *Backend) Models(ctx context.Context) ([]backend.ModelInfo, error) {
	var models []backend.ModelInfo
	for m, err := range b.client.Models.All(ctx) {
		if err != nil {
			return nil, explain(ctx, err, "", false)
		}
		if !slices.Contains(m.SupportedActions, "generateContent") {
			continue
		}
		models = append(models, backend.ModelInfo{
			Name:          strings.TrimPrefix(m.Name, "models/"),
			Description:   m.DisplayName,
			ContextLength: int(m.InputTokenLimit),
		})
	}
	return models, nil
}

func (b *Backend) Chat(ctx context.Context, req backend.Request) iter.Seq2[backend.Event, error] {
	return func(yield func(backend.Event, error) bool) {
		model := cmp.Or(req.Model, b.settings.Model, DefaultModel)
		if err := ctx.Err(); err != nil {
			yield(backend.Event{}, err)
			return
		}

		var (
			answer   strings.Builder
			metadata *genai.GroundingMetadata
			usage    backend.Usage
		)
		cfg := b.config(req)
		searching := len(cfg.Tools) > 0
		for resp, err := range b.client.Models.GenerateContentStream(ctx, model, contents(req.Messages), cfg) {
			if err != nil {
				yield(backend.Event{}, explain(ctx, err, model, searching))
				return
			}
			if u := resp.UsageMetadata; u != nil {
				usage.InputTokens = int(u.PromptTokenCount + u.ToolUsePromptTokenCount)
				usage.OutputTokens = int(u.CandidatesTokenCount + u.ThoughtsTokenCount)
			}
			if len(resp.Candidates) == 0 {
				continue
			}
			c := resp.Candidates[0]
			if c.GroundingMetadata != nil {
				metadata = c.GroundingMetadata
			}
			if c.Content == nil {
				continue
			}
			for _, p := range c.Content.Parts {
				if p.Text == "" {
					continue
				}
				ev := backend.Event{Kind: backend.EventTextDelta, Text: p.Text}
				if p.Thought {
					ev.Kind = backend.EventThinkingDelta
				} else {
					answer.WriteString(p.Text)
				}
				if !yield(ev, nil) {
					return
				}
			}
		}

		// Gemini reports its searches only with the final metadata, so there
		// are no SearchStarted events; the queries are in the grounding.
		if g := grounding(answer.String(), metadata); g != nil {
			usage.SearchQueries = len(g.Queries)
			if !yield(backend.Event{Kind: backend.EventGrounding, Grounding: g}, nil) {
				return
			}
		}
		if !yield(backend.Event{Kind: backend.EventUsage, Usage: &usage}, nil) {
			return
		}
		yield(backend.Event{Kind: backend.EventDone}, nil)
	}
}

func contents(messages []chat.Message) []*genai.Content {
	out := make([]*genai.Content, 0, len(messages))
	for _, m := range messages {
		role := genai.RoleUser
		if m.Role == chat.RoleAssistant {
			role = genai.RoleModel
		}
		out = append(out, genai.NewContentFromText(m.Text, genai.Role(role)))
	}
	return out
}

func (b *Backend) config(req backend.Request) *genai.GenerateContentConfig {
	cfg := &genai.GenerateContentConfig{}
	if req.System != "" {
		cfg.SystemInstruction = genai.NewContentFromText(req.System, genai.RoleUser)
	}
	if t := cmp.Or(req.Temperature, b.settings.Temperature); t != nil {
		cfg.Temperature = genai.Ptr(float32(*t))
	}
	if think := cmp.Or(req.Think, b.settings.Think); think != nil && *think {
		cfg.ThinkingConfig = &genai.ThinkingConfig{IncludeThoughts: true}
	}

	search := b.searchByDefault()
	if req.Search != nil {
		search = *req.Search
	}
	if search {
		gs := &genai.GoogleSearch{}
		if since := time.Duration(b.settings.Search.Since); since > 0 {
			now := b.now().UTC()
			gs.TimeRangeFilter = &genai.Interval{StartTime: now.Add(-since), EndTime: now}
		}
		cfg.Tools = []*genai.Tool{{GoogleSearch: gs}}
	}
	return cfg
}

// explain turns API errors into messages that say what to do next.
func explain(ctx context.Context, err error, model string, searching bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var apiErr genai.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	// The SDK's error text includes a large details dump; keep the
	// server's one-line message.
	msg := apiErr.Message
	lower := strings.ToLower(msg)
	switch {
	case apiErr.Code == http.StatusNotFound && model != "":
		return fmt.Errorf("gemini model %q not found; see --list-models (%s)", model, msg)
	case strings.Contains(lower, "api key"), apiErr.Code == http.StatusUnauthorized, apiErr.Code == http.StatusForbidden:
		return fmt.Errorf("gemini rejected the API key; check GEMINI_API_KEY (%s)", msg)
	case apiErr.Code == http.StatusTooManyRequests && searching:
		return fmt.Errorf("gemini quota reached for a request grounded with Google Search; the key's plan may not "+
			"include grounding (check billing at https://aistudio.google.com), or try --search=false (%s)", msg)
	case apiErr.Code == http.StatusTooManyRequests:
		return fmt.Errorf("gemini quota or rate limit reached; try again later (%s)", msg)
	}
	return fmt.Errorf("gemini error %d: %s", apiErr.Code, msg)
}
