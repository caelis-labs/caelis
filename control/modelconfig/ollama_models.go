package modelconfig

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
)

// ErrOllamaUnreachable reports that the Host could not dial the Ollama endpoint.
var ErrOllamaUnreachable = errorcode.New(errorcode.Unavailable, "Cannot reach Ollama. Check that Ollama is installed and running at the selected endpoint, then retry.")

// ErrOllamaAPINotFound reports that the endpoint returned 404 for the model list.
var ErrOllamaAPINotFound = errorcode.New(errorcode.NotFound, "Ollama model API not found. Check the selected endpoint and retry.")

// selectableOllamaModels discovers installed models from the selected endpoint.
func selectableOllamaModels(ctx context.Context, baseURL string, client *http.Client, clientVersion string) ([]SelectableModel, error) {
	remote, err := providers.DiscoverModels(ctx, providers.Config{
		Provider:   "ollama",
		API:        model.APIOllama,
		BaseURL:    baseURL,
		HTTPClient: client,
		UserAgent:  providerUserAgent(clientVersion),
		Auth:       providers.AuthConfig{Type: model.AuthNone},
	})
	if err != nil {
		var dial *net.OpError
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return nil, err
		case errors.As(err, &dial) && dial.Op == "dial":
			return nil, fmt.Errorf("%w: %w", ErrOllamaUnreachable, err)
		case errorcode.Is(err, errorcode.NotFound):
			return nil, fmt.Errorf("%w: %w", ErrOllamaAPINotFound, err)
		}
		return nil, err
	}
	out := make([]SelectableModel, 0, len(remote))
	for _, model := range remote {
		defaults, err := ResolveModelDefaultsForEndpoint("ollama", baseURL, model.Name)
		if err != nil {
			return nil, err
		}
		// A local model's effective context, output limit, and thinking setup are
		// runtime-configurable, so the wizard must collect advanced defaults.
		out = append(out, SelectableModel{
			Name:     model.Name,
			Detail:   "local Ollama API · configurable context",
			Defaults: defaults,
		})
	}
	return uniqueSelectableModels(out), nil
}
