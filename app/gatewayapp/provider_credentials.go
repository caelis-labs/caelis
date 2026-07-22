package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
)

type providerCredentialPrevious struct {
	ref     string
	source  credentialstore.Source
	existed bool
}

type providerCredentialTransaction struct {
	store     *credentialstore.Store
	previous  []providerCredentialPrevious
	committed bool
}

func (s *Stack) prepareProviderCredentials(configs []ModelConfig) ([]ModelConfig, *providerCredentialTransaction, error) {
	prepared := make([]ModelConfig, 0, len(configs))
	txn := &providerCredentialTransaction{store: s.apiKeyCredentials}
	seenSources := map[string]credentialstore.Source{}
	seenPrevious := map[string]struct{}{}
	for _, raw := range configs {
		explicitSecret := strings.TrimSpace(raw.Token)
		environment := strings.TrimSpace(raw.TokenEnv)
		configured := modelconfig.NormalizeConfig(raw)
		if configured.AuthType == providers.AuthNone {
			configured.Token = ""
			configured.TokenEnv = ""
			configured.PersistToken = false
			prepared = append(prepared, configured)
			continue
		}
		secret := strings.TrimSpace(configured.Token)
		if environment != "" && explicitSecret == "" {
			secret = ""
		}
		if secret == "" && environment == "" {
			prepared = append(prepared, configured)
			continue
		}
		ref := strings.ToLower(strings.TrimSpace(configured.CredentialRef))
		if ref == modelconfig.CodexOAuthCredentialRef {
			return nil, txn, fmt.Errorf("gatewayapp: Codex OAuth model must not carry an API key")
		}
		if ref == "" {
			ref = credentialstore.BuildReference(configured.Provider, configured.ProviderEndpointID)
		}
		if !strings.HasPrefix(ref, "apikey:") {
			return nil, txn, fmt.Errorf("gatewayapp: provider model %q uses unsupported credential reference %q", configured.ID, ref)
		}
		if txn.store == nil {
			return nil, txn, fmt.Errorf("gatewayapp: provider credential store is unavailable")
		}
		source := credentialstore.Source{APIKey: secret, Environment: environment}
		if secret != "" {
			source.Environment = ""
		}
		if previousSource, ok := seenSources[ref]; ok && previousSource != source {
			return nil, txn, fmt.Errorf("gatewayapp: provider endpoint %q supplied conflicting API keys", configured.ProviderEndpointID)
		}
		seenSources[ref] = source
		if _, ok := seenPrevious[ref]; !ok {
			previous, err := txn.store.LookupSource(context.Background(), ref)
			switch {
			case err == nil:
				txn.previous = append(txn.previous, providerCredentialPrevious{ref: ref, source: previous, existed: true})
			case errors.Is(err, os.ErrNotExist):
				txn.previous = append(txn.previous, providerCredentialPrevious{ref: ref})
			default:
				return nil, txn, fmt.Errorf("gatewayapp: read previous provider credential %q: %w", ref, err)
			}
			seenPrevious[ref] = struct{}{}
			if err := putProviderCredentialSource(txn.store, ref, source); err != nil {
				return nil, txn, err
			}
		}
		configured.CredentialRef = ref
		configured.Token = ""
		configured.TokenEnv = ""
		configured.PersistToken = false
		prepared = append(prepared, configured)
	}
	return prepared, txn, nil
}

func (t *providerCredentialTransaction) commit() {
	if t != nil {
		t.committed = true
	}
}

func (t *providerCredentialTransaction) rollback() error {
	if t == nil || t.committed || t.store == nil {
		return nil
	}
	var errs []error
	for i := len(t.previous) - 1; i >= 0; i-- {
		previous := t.previous[i]
		if previous.existed {
			errs = append(errs, putProviderCredentialSource(t.store, previous.ref, previous.source))
		} else {
			errs = append(errs, t.store.Delete(context.Background(), previous.ref))
		}
	}
	return errors.Join(errs...)
}

func putProviderCredentialSource(store *credentialstore.Store, ref string, source credentialstore.Source) error {
	if source.Environment != "" {
		return store.PutEnvironment(context.Background(), ref, source.Environment)
	}
	return store.Put(context.Background(), ref, source.APIKey)
}
