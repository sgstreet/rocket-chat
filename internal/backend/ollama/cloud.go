package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ollama/ollama/api"

	"github.com/sgstreet/rocket-chat/internal/backend"
)

// Ollama runs cloud models on ollama.com. A local server passes a request
// for "<model>:cloud" (or the older "<model>-cloud" form) on to ollama.com
// once it is signed in with `ollama signin`, without pulling anything.

// defaultCloudURL is where the catalog of cloud models is listed.
const defaultCloudURL = "https://ollama.com"

// cloudTimeout bounds fetching the cloud catalog.
const cloudTimeout = 5 * time.Second

// cloudDescription marks cloud models in model lists.
const cloudDescription = "cloud"

// isCloudModel reports whether name runs on ollama.com.
func isCloudModel(name string) bool {
	i := strings.LastIndex(name, ":")
	if i < 0 || strings.Contains(name[i:], "/") {
		return false
	}
	tag := strings.ToLower(name[i+1:])
	return tag == "cloud" || strings.HasSuffix(tag, "-cloud")
}

// cloudName returns the name a local server knows a cloud catalog model
// by, the one `ollama pull <model>:cloud` gives it: "gpt-oss:120b" becomes
// "gpt-oss:120b-cloud", and "kimi-k3" becomes "kimi-k3:cloud".
func cloudName(model string) string {
	if i := strings.LastIndex(model, ":"); i > strings.LastIndex(model, "/") {
		return model + "-cloud"
	}
	return model + ":cloud"
}

// cloudModels returns the ollama.com catalog as local server names. A
// fetched catalog is kept for the life of the backend.
func (b *Backend) cloudModels(ctx context.Context) ([]string, error) {
	b.cloudMu.Lock()
	defer b.cloudMu.Unlock()
	if b.cloudCatalog != nil {
		return b.cloudCatalog, nil
	}
	ctx, cancel := context.WithTimeout(ctx, cloudTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(b.cloudURL, "/")+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	var list api.ListResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	names := []string{}
	for _, m := range list.Models {
		name := strings.TrimSpace(m.Model)
		if name == "" {
			name = strings.TrimSpace(m.Name)
		}
		if name != "" {
			names = append(names, cloudName(name))
		}
	}
	b.cloudCatalog = names
	return names, nil
}

// withCloudModels lists the local models first, then the cloud ones: those
// already pulled, then the rest of the catalog. When the catalog cannot be
// fetched, the local list comes back with a *backend.PartialList error.
func (b *Backend) withCloudModels(ctx context.Context, listed []backend.ModelInfo) ([]backend.ModelInfo, error) {
	var local, cloud []backend.ModelInfo
	have := map[string]bool{}
	for _, m := range listed {
		have[m.Name] = true
		if m.Description == cloudDescription {
			cloud = append(cloud, m)
		} else {
			local = append(local, m)
		}
	}
	models := append(local, cloud...)
	if !b.listCloud() {
		return models, nil
	}
	catalog, err := b.cloudModels(ctx)
	if err != nil {
		return models, &backend.PartialList{Err: fmt.Errorf("cloud models not listed: %s/api/tags: %w", b.cloudURL, err)}
	}
	for _, name := range catalog {
		if !have[name] {
			have[name] = true
			models = append(models, backend.ModelInfo{Name: name, Description: cloudDescription})
		}
	}
	return models, nil
}

// listCloud reports whether Models includes the cloud catalog: when
// cloud_models is on and the server is not ollama.com itself.
func (b *Backend) listCloud() bool {
	on := b.settings.CloudModels == nil || *b.settings.CloudModels
	return on && b.cloudURL != "" && b.host.Hostname() != "ollama.com"
}
