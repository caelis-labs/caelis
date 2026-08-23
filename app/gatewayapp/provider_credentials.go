package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
)

type providerCredentialTransaction struct {
	replacement *credentialstore.ReplacementTransaction
	retirement  *credentialstore.RetirementTransaction
}

func (s *controlCommandBackend) prepareProviderCredentials(ctx context.Context, configs []ModelConfig) ([]ModelConfig, *providerCredentialTransaction, error) {
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
	if s.composition.authorities.apiKeyCredentials == nil {
		return nil, nil, fmt.Errorf("gatewayapp: provider credential store is unavailable")
	}
	replacement, err := s.composition.authorities.apiKeyCredentials.BeginReplacement(ctx, replacements)
	if err != nil {
		return nil, nil, fmt.Errorf("gatewayapp: replace provider credentials: %w", err)
	}
	txn := &providerCredentialTransaction{replacement: replacement}
	return prepared, txn, nil
}

func (s *controlCommandBackend) prepareProviderCredentialRetirement(ctx context.Context, refs []string, baseConfigurationRevision uint64) (*providerCredentialTransaction, error) {
	if len(refs) == 0 {
		return &providerCredentialTransaction{}, nil
	}
	if s == nil || s.composition == nil || s.composition.authorities.apiKeyCredentials == nil {
		return nil, fmt.Errorf("gatewayapp: provider credential store is unavailable")
	}
	retirement, err := s.composition.authorities.apiKeyCredentials.BeginRetirement(ctx, refs, baseConfigurationRevision)
	if err != nil {
		return nil, fmt.Errorf("gatewayapp: retire provider credentials: %w", err)
	}
	return &providerCredentialTransaction{retirement: retirement}, nil
}

func (t *providerCredentialTransaction) commit() error {
	if t == nil {
		return nil
	}
	var errs []error
	if t.replacement != nil {
		errs = append(errs, t.replacement.Commit())
	}
	if t.retirement != nil {
		errs = append(errs, t.retirement.Commit())
	}
	return errors.Join(errs...)
}

func (t *providerCredentialTransaction) rollback() error {
	if t == nil {
		return nil
	}
	var errs []error
	if t.retirement != nil {
		errs = append(errs, t.retirement.Rollback())
	}
	if t.replacement != nil {
		errs = append(errs, t.replacement.Rollback())
	}
	return errors.Join(errs...)
}
