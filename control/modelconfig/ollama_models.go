package modelconfig

import (
	"context"
	"net/http"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/control/modelcatalog"
)

// selectableOllamaModels uses endpoint semantics instead of merging unrelated
// catalogs. Local endpoints report their installed models through /api/tags;
// the direct Cloud endpoint uses the curated frontier catalog.
func selectableOllamaModels(ctx context.Context, baseURL string, client *http.Client) ([]SelectableModel, error) {
	if CatalogProviderFor("ollama", baseURL) == modelcatalog.OllamaCloudProvider {
		models := modelcatalog.ListOllamaCloudModels()
		out := make([]SelectableModel, 0, len(models))
		for _, name := range models {
			out = append(out, SelectableModel{
				Name:             name,
				Detail:           selectableModelDetail("ollama", baseURL, name),
				MetadataComplete: hasCompleteModelMetadataForEndpoint("ollama", baseURL, name),
			})
		}
		return out, nil
	}

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
