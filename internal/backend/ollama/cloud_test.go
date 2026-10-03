package ollama

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ollama/ollama/api"

	"github.com/sgstreet/rocket-chat/internal/backend"
)

func TestCloudNames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cloud bool
	}{
		{"gpt-oss:120b-cloud", true},
		{"kimi-k3:cloud", true},
		{"kimi-k3:CLOUD", true},
		{"qwen3:0.6b", false},
		{"qwen3", false},
		{"registry.example.com:5000/m-cloud", false},
	} {
		if got := isCloudModel(tc.name); got != tc.cloud {
			t.Errorf("isCloudModel(%q) = %v", tc.name, got)
		}
	}
	for in, want := range map[string]string{
		"gpt-oss:120b":                "gpt-oss:120b-cloud",
		"kimi-k3":                     "kimi-k3:cloud",
		"registry.example.com:5000/m": "registry.example.com:5000/m:cloud",
	} {
		if got := cloudName(in); got != want {
			t.Errorf("cloudName(%q) = %q, want %q", in, got, want)
		}
	}
}

// newCloudCatalog serves an ollama.com style catalog and counts requests.
func newCloudCatalog(t *testing.T, status int, models ...string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		var list api.ListResponse
		for _, m := range models {
			list.Models = append(list.Models, api.ListModelResponse{Name: m, Model: m})
		}
		_ = json.NewEncoder(w).Encode(list)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func modelNames(models []backend.ModelInfo) []string {
	names := make([]string, len(models))
	for i, m := range models {
		names[i] = m.Name + " (" + m.Description + ")"
	}
	return names
}

func TestModelsListsCloudModels(t *testing.T) {
	fs := newFakeServer(t)
	fs.models = []api.ListModelResponse{
		{Name: "gpt-oss:120b-cloud", RemoteHost: "https://ollama.com:443"},
		{Name: "qwen3:0.6b", Details: api.ModelDetails{Family: "qwen3", ParameterSize: "0.6B"}},
	}
	catalog, hits := newCloudCatalog(t, http.StatusOK, "kimi-k3", "gpt-oss:120b")
	b := newBackend(t, Settings{Host: fs.URL})
	b.cloudURL = catalog.URL

	models, err := b.Models(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"qwen3:0.6b (qwen3 0.6B)", "gpt-oss:120b-cloud (cloud)", "kimi-k3:cloud (cloud)"}
	if got := modelNames(models); !slices.Equal(got, want) {
		t.Errorf("models = %q, want %q", got, want)
	}
	if _, err := b.Models(t.Context()); err != nil || hits.Load() != 1 {
		t.Errorf("second listing: %v, catalog fetched %d times, want once", err, hits.Load())
	}

	off := false
	b = newBackend(t, Settings{Host: fs.URL, CloudModels: &off})
	b.cloudURL = catalog.URL
	models, err = b.Models(t.Context())
	want = []string{"qwen3:0.6b (qwen3 0.6B)", "gpt-oss:120b-cloud (cloud)"}
	if got := modelNames(models); err != nil || !slices.Equal(got, want) {
		t.Errorf("cloud_models off: %q, %v; want %q", got, err, want)
	}
}

func TestModelsWhenCatalogFails(t *testing.T) {
	fs := newFakeServer(t)
	fs.models = []api.ListModelResponse{{Name: "qwen3:0.6b"}}
	catalog, hits := newCloudCatalog(t, http.StatusServiceUnavailable)
	b := newBackend(t, Settings{Host: fs.URL})
	b.cloudURL = catalog.URL

	models, err := b.Models(t.Context())
	var partial *backend.PartialList
	if !errors.As(err, &partial) || !strings.Contains(err.Error(), "cloud models not listed") {
		t.Fatalf("err = %v, want a partial list", err)
	}
	if got := modelNames(models); !slices.Equal(got, []string{"qwen3:0.6b ()"}) {
		t.Errorf("models = %q", got)
	}
	// A failure is not kept: the next listing tries again.
	_, _ = b.Models(t.Context())
	if hits.Load() != 2 {
		t.Errorf("catalog fetched %d times, want 2", hits.Load())
	}
}

func TestCloudModelErrors(t *testing.T) {
	fs := newFakeServer(t, standardChunks...)
	b := newBackend(t, Settings{Host: fs.URL})
	for model, want := range map[string]string{
		"denied:cloud": "ollama signin",
		"gone:cloud":   "update it or run `ollama pull gone:cloud`",
	} {
		var err error
		for _, e := range b.Chat(t.Context(), backend.Request{Model: model, Messages: helloReq.Messages}) {
			if e != nil {
				err = e
			}
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", model, err, want)
		}
	}
}

func TestCloseDoesNotUnloadCloudModels(t *testing.T) {
	fs := newFakeServer(t, standardChunks...)
	b := newBackend(t, Settings{Host: fs.URL})
	for _, model := range []string{"kimi-k3:cloud", "qwen3"} {
		for range b.Chat(t.Context(), backend.Request{Model: model, Messages: helloReq.Messages}) {
		}
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fs.unloaded, []string{"qwen3"}) {
		t.Errorf("unloaded %q, want only the local model", fs.unloaded)
	}
}
