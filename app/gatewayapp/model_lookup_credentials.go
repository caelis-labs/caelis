package gatewayapp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type pinnedAPIKeyCredential struct {
	token string
}

type runtimeAPIKeySnapshot struct {
	mu          sync.RWMutex
	credentials map[string]pinnedAPIKeyCredential
}

func (s *runtimeAPIKeySnapshot) resolve(ctx context.Context, ref string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ref = strings.ToLower(strings.TrimSpace(ref))
	s.mu.RLock()
	credential, ok := s.credentials[ref]
	s.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("gatewayapp: model credential %q is outside the Runtime activation snapshot", ref)
	}
	return credential.token, nil
}

func (s *runtimeAPIKeySnapshot) beginPut(ref string, credential pinnedAPIKeyCredential) func(bool) {
	if s == nil {
		return func(bool) {}
	}
	ref = strings.ToLower(strings.TrimSpace(ref))
	s.mu.Lock()
	if s.credentials == nil {
		s.credentials = map[string]pinnedAPIKeyCredential{}
	}
	previous, existed := s.credentials[ref]
	s.credentials[ref] = credential
	var once sync.Once
	return func(committed bool) {
		once.Do(func() {
			if !committed {
				if existed {
					s.credentials[ref] = previous
				} else {
					delete(s.credentials, ref)
				}
			}
			s.mu.Unlock()
		})
	}
}

// pinAPIKeyCredentials replaces the detached lookup's Host credential resolver
// with one Runtime-owned activation snapshot containing only explicitly
// reachable references. Host catalog deletion may then retire an unreferenced
// credential without interrupting already activated work.
func (l *modelLookup) pinAPIKeyCredentials(ctx context.Context, references []string) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	refs := make(map[string]struct{}, len(references))
	for _, raw := range references {
		ref := strings.ToLower(strings.TrimSpace(raw))
		if strings.HasPrefix(ref, "apikey:") {
			refs[ref] = struct{}{}
		}
	}
	l.mu.RLock()
	resolver := l.resolveAPIKey
	provided := make(map[string]string, len(refs))
	for _, configured := range l.configs {
		ref := strings.ToLower(strings.TrimSpace(configured.CredentialRef))
		if _, ok := refs[ref]; !ok {
			continue
		}
		if token := strings.TrimSpace(configured.Token); token != "" {
			provided[ref] = token
		}
	}
	l.mu.RUnlock()

	ordered := make([]string, 0, len(refs))
	for ref := range refs {
		ordered = append(ordered, ref)
	}
	sort.Strings(ordered)
	snapshot := &runtimeAPIKeySnapshot{credentials: make(map[string]pinnedAPIKeyCredential, len(ordered))}
	for _, ref := range ordered {
		if token := provided[ref]; token != "" {
			snapshot.credentials[ref] = pinnedAPIKeyCredential{token: token}
			continue
		}
		if resolver == nil {
			return fmt.Errorf("gatewayapp: pin Runtime model credential %q: source is unavailable", ref)
		}
		token, err := resolver(ctx, ref)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return fmt.Errorf("gatewayapp: pin Runtime model credential %q: %w", ref, err)
		}
		snapshot.credentials[ref] = pinnedAPIKeyCredential{token: token}
	}

	l.mu.Lock()
	l.pinnedAPIKeys = snapshot
	l.resolveAPIKey = snapshot.resolve
	l.mu.Unlock()
	return nil
}

// beginPinAPIKeyCredential stages one model selected from the live Host catalog
// in an already activated Runtime snapshot. The returned completion retains it
// only when the matching Session CAS commits.
func (l *modelLookup) beginPinAPIKeyCredential(ctx context.Context, configured ModelConfig, source *modelLookup) (func(bool), error) {
	if l == nil {
		return nil, fmt.Errorf("gatewayapp: Session Runtime model lookup is unavailable")
	}
	configured = normalizeModelConfig(configured)
	ref := strings.ToLower(strings.TrimSpace(configured.CredentialRef))
	if !strings.HasPrefix(ref, "apikey:") {
		return func(bool) {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	token := strings.TrimSpace(configured.Token)
	if token == "" {
		if source == nil {
			return nil, fmt.Errorf("gatewayapp: Host model credential source is unavailable")
		}
		source.mu.RLock()
		resolver := source.resolveAPIKey
		source.mu.RUnlock()
		if resolver == nil {
			return nil, fmt.Errorf("gatewayapp: Host model credential %q is unavailable", ref)
		}
		var err error
		token, err = resolver(ctx, ref)
		if err != nil {
			return nil, err
		}
	}
	l.mu.RLock()
	snapshot := l.pinnedAPIKeys
	l.mu.RUnlock()
	if snapshot == nil {
		return nil, fmt.Errorf("gatewayapp: Session Runtime credential snapshot is unavailable")
	}
	return snapshot.beginPut(ref, pinnedAPIKeyCredential{token: token}), nil
}

// materializeAPIKeyCredential copies one Runtime-owned API key into a
// process-local child Session pin. It remains absent from durable configuration.
func (l *modelLookup) materializeAPIKeyCredential(ctx context.Context, configured ModelConfig) (ModelConfig, error) {
	configured = cloneSessionModelConfig(normalizeModelConfig(configured))
	ref := strings.ToLower(strings.TrimSpace(configured.CredentialRef))
	if l == nil || !strings.HasPrefix(ref, "apikey:") || strings.TrimSpace(configured.Token) != "" {
		return configured, nil
	}
	l.mu.RLock()
	resolver := l.resolveAPIKey
	l.mu.RUnlock()
	if resolver == nil {
		return ModelConfig{}, fmt.Errorf("gatewayapp: Runtime model credential %q is unavailable", ref)
	}
	token, err := resolver(contextOrBackground(ctx), ref)
	if err != nil {
		return ModelConfig{}, err
	}
	configured.Token = token
	configured.PersistToken = false
	return configured, nil
}
