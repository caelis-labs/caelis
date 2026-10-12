package modelconfig

import (
	"context"
	"net/http"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
)

// selectableOllamaModels discovers installed models from the selected endpoint.
func selectableOllamaModels(ctx context.Context, baseURL string, client *http.Client) ([]SelectableModel, error) {

	remote, err := providers.DiscoverModels(ctx, providers.Config{
		Provider:   "ollama",
		API:        providers.APIOllama,
		BaseURL:    baseURL,
		HTTPClient: client,
		Auth:       providers.AuthConfig{Type: providers.AuthNone},
	})
	if err != nil {
		return nil, err
	}
	out := make([]SelectableModel, 0, len(remote))
	for _, model := range remote {
		// A local model's effective context, output limit, and thinking setup are
		// runtime-configurable, so the wizard must collect advanced defaults.
		out = append(out, SelectableModel{
			Name:   model.Name,
			Detail: "local Ollama API · configurable context",
		})
	}
	return uniqueSelectableModels(out), nil
}
