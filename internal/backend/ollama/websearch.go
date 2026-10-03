package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/ollama/ollama/api"
)

// Web search modes.
const (
	// SearchAuto uses SearchDirect when OLLAMA_API_KEY is set, otherwise
	// SearchLocal.
	SearchAuto = "auto"
	// SearchDirect calls the ollama.com web search API with OLLAMA_API_KEY.
	SearchDirect = "direct"
	// SearchLocal goes through the local Ollama server, which authenticates
	// with the account from `ollama signin`.
	SearchLocal = "local"
)

// EnvAPIKey holds the ollama.com API key for SearchDirect.
const EnvAPIKey = "OLLAMA_API_KEY"

const defaultWebAPI = "https://ollama.com"

// SearchSettings is the backends.ollama.search config section.
type SearchSettings struct {
	// Enabled turns web search on when the request does not say. Off by
	// default because queries leave the machine.
	Enabled bool `yaml:"enabled"`
	// Mode is SearchAuto (default), SearchDirect or SearchLocal.
	Mode string `yaml:"mode"`
	// APIURL is the base URL of the web search API used by SearchDirect
	// (default https://ollama.com).
	APIURL string `yaml:"api_url"`
	// MaxResults is the number of results per search, 1-10 (default 5).
	MaxResults int `yaml:"max_results"`
	// MaxRounds limits how many times the model may call tools in one
	// reply (default 5).
	MaxRounds int `yaml:"max_rounds"`
	// MaxFetchChars truncates fetched pages (default 8000).
	MaxFetchChars int `yaml:"max_fetch_chars"`
	// NumCtx is the minimum context window while searching (default 32768);
	// search results are long.
	NumCtx int `yaml:"num_ctx"`
}

func (s SearchSettings) withDefaults() SearchSettings {
	if s.Mode == "" {
		s.Mode = SearchAuto
	}
	if s.APIURL == "" {
		s.APIURL = defaultWebAPI
	}
	if s.MaxResults <= 0 {
		s.MaxResults = 5
	}
	s.MaxResults = min(s.MaxResults, 10)
	if s.MaxRounds <= 0 {
		s.MaxRounds = 5
	}
	if s.MaxFetchChars <= 0 {
		s.MaxFetchChars = 8000
	}
	if s.NumCtx <= 0 {
		s.NumCtx = 32768
	}
	return s
}

// errSearchAuth is returned when web search has no usable credentials.
var errSearchAuth = errors.New("ollama web search needs an ollama.com account: set " + EnvAPIKey + " or run `ollama signin`")

// webSearcher runs web_search and web_fetch tool calls.
type webSearcher interface {
	search(ctx context.Context, query string, maxResults int) ([]api.WebSearchResult, error)
	fetch(ctx context.Context, url string) (*api.WebFetchResponse, error)
}

func newSearcher(mode, webAPI string, client *api.Client, httpClient *http.Client) (webSearcher, error) {
	key := os.Getenv(EnvAPIKey)
	switch mode {
	case SearchAuto:
		if key != "" {
			return directSearcher{base: webAPI, apiKey: key, http: httpClient}, nil
		}
		return localSearcher{client: client}, nil
	case SearchDirect:
		if key == "" {
			return nil, fmt.Errorf("search mode %q needs %s", SearchDirect, EnvAPIKey)
		}
		return directSearcher{base: webAPI, apiKey: key, http: httpClient}, nil
	case SearchLocal:
		return localSearcher{client: client}, nil
	}
	return nil, fmt.Errorf("unknown search mode %q (want %s, %s or %s)", mode, SearchAuto, SearchDirect, SearchLocal)
}

// directSearcher calls the ollama.com web search API.
type directSearcher struct {
	base   string
	apiKey string
	http   *http.Client
}

func (d directSearcher) search(ctx context.Context, query string, maxResults int) ([]api.WebSearchResult, error) {
	var resp api.WebSearchResponse
	err := d.post(ctx, "/api/web_search", api.WebSearchRequest{Query: query, MaxResults: maxResults}, &resp)
	return resp.Results, err
}

func (d directSearcher) fetch(ctx context.Context, url string) (*api.WebFetchResponse, error) {
	var resp api.WebFetchResponse
	if err := d.post(ctx, "/api/web_fetch", api.WebFetchRequest{URL: url}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (d directSearcher) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(d.base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.apiKey)
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if err := statusError(resp.StatusCode, resp.Status, data); err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func statusError(code int, status string, body []byte) error {
	switch {
	case code < http.StatusBadRequest:
		return nil
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return errSearchAuth
	case code == http.StatusTooManyRequests:
		return errors.New("ollama web search is rate limited; try again later")
	}
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return fmt.Errorf("web search: %s: %s", status, e.Error)
	}
	return fmt.Errorf("web search: %s", status)
}

// localSearcher goes through the local Ollama server's experimental web
// search routes.
type localSearcher struct {
	client *api.Client
}

func (l localSearcher) search(ctx context.Context, query string, maxResults int) ([]api.WebSearchResult, error) {
	resp, err := l.client.WebSearchExperimental(ctx, &api.WebSearchRequest{Query: query, MaxResults: maxResults})
	if err != nil {
		return nil, localError(err)
	}
	return resp.Results, nil
}

func (l localSearcher) fetch(ctx context.Context, url string) (*api.WebFetchResponse, error) {
	resp, err := l.client.WebFetchExperimental(ctx, &api.WebFetchRequest{URL: url})
	if err != nil {
		return nil, localError(err)
	}
	return resp, nil
}

func localError(err error) error {
	var authErr api.AuthorizationError
	var status api.StatusError
	switch {
	case errors.As(err, &authErr):
		return errSearchAuth
	case errors.As(err, &status):
		return statusError(status.StatusCode, status.Status, []byte(`{"error":`+jsonString(status.ErrorMessage)+`}`))
	}
	return err
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
