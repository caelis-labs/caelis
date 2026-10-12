package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/control/modelconfig"
)

// ConnectionModels reads the same endpoint-scoped catalog used by connection
// assembly. Retained credentials are resolved privately and never returned to
// a presentation client or saved as new connection input.
func (s ModelService) ConnectionModels(ctx context.Context, req modelconfig.ConnectRequest) ([]modelconfig.SelectableModel, error) {
	template, ok := modelconfig.LookupProvider(req.Provider)
	if !ok || template.Provider != "magpie" || req.APIKey != "" || s.composition == nil {
		return modelconfig.SelectableModels(ctx, req)
	}
	doc, err := s.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	lookup, err := newModelLookupFromDocument(doc, 0)
	if err != nil {
		return nil, err
	}
	ref, reusable := s.composition.reusableProviderCredentialRef(ctx, lookup, doc.ModelProfiles, req.Provider, req.EndpointID, req.BaseURL)
	if reusable {
		source, err := s.composition.authorities.apiKeyCredentials.LookupSource(ctx, ref)
		if err != nil {
			return nil, err
		}
		req.APIKey = source.APIKey
	}
	return modelconfig.SelectableModels(ctx, req)
}
