package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
)

type providerCredentialTransaction struct {
	replacement *credentialstore.ReplacementTransaction
}

func (s *Stack) prepareProviderCredentials(ctx context.Context, configs []ModelConfig) ([]ModelConfig, *providerCredentialTransaction, error) {
	prepared := make([]ModelConfig, 0, len(configs))
	seenSources := map[string]credentialstore.Source{}
	for _, raw := range configs {
		configured := modelconfig.NormalizeConfig(raw)
		if configured.AuthType == providers.AuthNone {
			configured.Token = ""
			configured.PersistToken = false
			prepared = append(prepared, configured)
			continue
		}
		secret := strings.TrimSpace(configured.Token)
		if secret == "" {
			prepared = append(prepared, configured)
			continue
		}
		ref := strings.ToLower(strings.TrimSpace(configured.CredentialRef))
		if ref == modelconfig.CodexOAuthCredentialRef {
			return nil, nil, fmt.Errorf("gatewayapp: Codex OAuth model must not carry an API key")
		}
		if ref == modelconfig.GrokOAuthCredentialRef {
			return nil, nil, fmt.Errorf("gatewayapp: Grok OAuth model must not carry an API key")
		}
		if ref == "" {
			ref = credentialstore.BuildReference(configured.Provider, configured.ProviderEndpointID)
		}
		if !strings.HasPrefix(ref, "apikey:") {
			return nil, nil, fmt.Errorf("gatewayapp: provider model %q uses unsupported credential reference %q", configured.ID, ref)
		}
		source := credentialstore.Source{APIKey: secret}
		if previousSource, ok := seenSources[ref]; ok && previousSource != source {
			return nil, nil, fmt.Errorf("gatewayapp: provider endpoint %q supplied conflicting API keys", configured.ProviderEndpointID)
		}
		seenSources[ref] = source
		configured.CredentialRef = ref
		configured.Token = ""
		configured.PersistToken = false
		prepared = append(prepared, configured)
	}
	replacements := make([]credentialstore.Replacement, 0, len(seenSources))
	for ref, source := range seenSources {
		replacements = append(replacements, credentialstore.Replacement{Ref: ref, Source: source})
	}
	if len(replacements) == 0 {
		return prepared, &providerCredentialTransaction{}, nil
	}
	if s.composition.apiKeyCredentials == nil {
		return nil, nil, fmt.Errorf("gatewayapp: provider credential store is unavailable")
	}
	replacement, err := s.composition.apiKeyCredentials.BeginReplacement(ctx, replacements)
	if err != nil {
		return nil, nil, fmt.Errorf("gatewayapp: replace provider credentials: %w", err)
	}
	txn := &providerCredentialTransaction{replacement: replacement}
	return prepared, txn, nil
}

func (t *providerCredentialTransaction) commit() error {
	if t == nil || t.replacement == nil {
		return nil
	}
	return t.replacement.Commit()
}

func (t *providerCredentialTransaction) rollback() error {
	if t == nil || t.replacement == nil {
		return nil
	}
	return t.replacement.Rollback()
}
