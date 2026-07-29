package gatewayapp

import (
	"context"
	"testing"

	model "github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
)

type grokProviderUsageReader struct {
	calls int
}

func (r *grokProviderUsageReader) SubscriptionUsage(context.Context) (providerusage.Snapshot, error) {
	r.calls++
	return providerusage.Snapshot{Provider: "xai"}, nil
}

func TestModelServiceProviderUsageSupportsGrokOAuthCredential(t *testing.T) {
	t.Parallel()

	lookup, err := newModelLookup(nil, 500000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lookup.Upsert(ModelConfig{
		Provider:      "xai",
		API:           model.APIXAIResponses,
		Model:         "grok-4.5",
		BaseURL:       modelconfig.GrokOAuthBaseURL,
		CredentialRef: modelconfig.GrokOAuthCredentialRef,
		AuthType:      model.AuthOAuthToken,
	}); err != nil {
		t.Fatal(err)
	}
	reader := &grokProviderUsageReader{}
	service := ModelService{stack: &Stack{
		lookup: lookup,
		providerUsage: providerusage.NewRegistry(map[string]providerusage.Reader{
			"xai": reader,
		}),
	}}

	snapshot, found, err := service.ProviderUsage(context.Background(), lookup.DefaultAlias())
	if err != nil {
		t.Fatal(err)
	}
	if !found || snapshot.Provider != "xai" || reader.calls != 1 {
		t.Fatalf("ProviderUsage() = snapshot:%#v found:%v calls:%d", snapshot, found, reader.calls)
	}
}
