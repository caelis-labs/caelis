package gatewayapp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

// retiredAPIKeyCredentialRefs derives reference-count retirement from canonical
// provider-backed ModelProfile reachability before and after a catalog mutation.
func retiredAPIKeyCredentialRefs(before, after AppConfig) []string {
	beforeCounts := providerProfileAPIKeyCredentialRefCounts(before)
	afterCounts := providerProfileAPIKeyCredentialRefCounts(after)
	refs := make([]string, 0, len(beforeCounts))
	for ref, count := range beforeCounts {
		if count > 0 && afterCounts[ref] == 0 {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	return refs
}

func providerProfileAPIKeyCredentialRefCounts(doc AppConfig) map[string]int {
	models := make(map[string]modelconfig.Config, len(doc.Models.Configs))
	for _, raw := range doc.Models.Configs {
		configured := modelconfig.NormalizeConfig(raw)
		if configured.ID != "" {
			models[configured.ID] = configured
		}
	}
	endpoints := make(map[string]modelconfig.ProviderEndpointConfig, len(doc.Models.ProviderEndpoints))
	for _, raw := range doc.Models.ProviderEndpoints {
		endpoint := modelconfig.NormalizeProviderEndpoint(raw)
		if endpoint.ID != "" {
			endpoints[endpoint.ID] = endpoint
		}
	}

	profiles := modelprofile.NormalizeConfiguration(doc.ModelProfiles)
	counts := make(map[string]int, len(profiles.Profiles))
	for _, profile := range profiles.Profiles {
		if profile.Backend.Provider == nil {
			continue
		}
		configured, ok := models[profile.Backend.Provider.ModelConfigID]
		if !ok {
			continue
		}
		endpoint, ok := endpoints[configured.ProviderEndpointID]
		if !ok {
			continue
		}
		ref := strings.ToLower(strings.TrimSpace(endpoint.CredentialRef))
		if strings.HasPrefix(ref, "apikey:") {
			counts[ref]++
		}
	}
	return counts
}

func providerProfileAPIKeyCredentialRefs(doc AppConfig) []string {
	counts := providerProfileAPIKeyCredentialRefCounts(doc)
	refs := make([]string, 0, len(counts))
	for ref, count := range counts {
		if count > 0 {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	return refs
}

// recoverProviderCredentialRetirements reconciles exact-byte receipts left by
// a process that stopped between credential deletion and the AppConfig CAS. The
// canonical snapshot is loaded while each receipt's reference locks are held so
// a replacement cannot be mistaken for the source being recovered.
func recoverProviderCredentialRetirements(ctx context.Context, store *appConfigStore, credentials *credentialstore.Store) error {
	if store == nil || credentials == nil {
		return nil
	}
	ctx = contextOrBackground(ctx)
	// Force any legacy migration to finish before recovery takes reference locks;
	// legacy migration owns the opposite config-lock-to-credential-lock order.
	if _, err := store.LoadContext(ctx); err != nil {
		return fmt.Errorf("gatewayapp: load model configuration before credential retirement recovery: %w", err)
	}
	return credentials.RecoverRetirements(ctx, func(callbackCtx context.Context) (map[string]struct{}, error) {
		doc, err := store.LoadContext(callbackCtx)
		if err != nil {
			return nil, fmt.Errorf("gatewayapp: load canonical model configuration for credential retirement recovery: %w", err)
		}
		counts := providerProfileAPIKeyCredentialRefCounts(doc)
		reachable := make(map[string]struct{}, len(counts))
		for ref, count := range counts {
			if count > 0 {
				reachable[ref] = struct{}{}
			}
		}
		return reachable, nil
	})
}
