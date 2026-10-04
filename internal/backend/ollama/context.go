package ollama

import (
	"cmp"
	"context"
	"strings"

	"github.com/ollama/ollama/api"
)

// ContextWindow returns the context the server runs model with: num_ctx
// when it is set, otherwise what the server reports for the loaded model.
// A local model that is not loaded yet reports 0, since the server picks
// its context when it loads it. A cloud model reports its full context.
func (b *Backend) ContextWindow(ctx context.Context, model string) (int, error) {
	model = cmp.Or(model, b.settings.Model)
	if model == "" {
		return 0, nil
	}
	if isCloudModel(model) {
		return b.modelContextLength(ctx, model)
	}
	if b.settings.NumCtx > 0 {
		return b.settings.NumCtx, nil
	}
	running, err := b.client.ListRunning(ctx)
	if err != nil {
		return 0, b.explain(ctx, err, "")
	}
	for _, m := range running.Models {
		if m.Name == model || m.Model == model {
			return m.ContextLength, nil
		}
	}
	return 0, nil
}

// modelContextLength reads the model's trained context length from
// /api/show.
func (b *Backend) modelContextLength(ctx context.Context, model string) (int, error) {
	resp, err := b.client.Show(ctx, &api.ShowRequest{Model: model})
	if err != nil {
		return 0, b.explain(ctx, err, model)
	}
	for k, v := range resp.ModelInfo {
		if !strings.HasSuffix(k, ".context_length") {
			continue
		}
		if n, ok := v.(float64); ok {
			return int(n), nil
		}
	}
	return 0, nil
}
