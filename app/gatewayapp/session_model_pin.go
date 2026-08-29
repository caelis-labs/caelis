package gatewayapp

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/kernel"
)

type sessionModelPinRegistry struct {
	mu            sync.RWMutex
	resolveAPIKey func(context.Context, string) (string, error)
	configured    map[string]ModelConfig
	entries       map[string]sessionModelPinEntry
}

type sessionModelPinEntry struct {
	config ModelConfig
	refs   int
}

func newSessionModelPinRegistry(
	resolveAPIKey func(context.Context, string) (string, error),
	configs ...ModelConfig,
) *sessionModelPinRegistry {
	r := &sessionModelPinRegistry{
		resolveAPIKey: resolveAPIKey,
		configured:    map[string]ModelConfig{},
		entries:       map[string]sessionModelPinEntry{},
	}
	r.syncConfiguredModels(configs)
	return r
}

// syncConfiguredModels replaces the canonical, secret-free provider set and
// revokes pins whose provider generation has been removed or changed. API-key
// material is checked only when the corresponding pin is retained or read, so
// an unrelated broken credential cannot block Host startup or configuration.
func (r *sessionModelPinRegistry) syncConfiguredModels(configs []ModelConfig) {
	if r == nil {
		return
	}
	configured := make(map[string]ModelConfig, len(configs))
	for _, raw := range configs {
		config := configuredModelGeneration(raw)
		modelID := strings.ToLower(strings.TrimSpace(config.ID))
		if modelID != "" {
			configured[modelID] = config
		}
	}
	r.mu.Lock()
	r.configured = configured
	for sessionID, entry := range r.entries {
		canonical, ok := configured[strings.ToLower(strings.TrimSpace(entry.config.ID))]
		if !ok || !sameConfiguredModelGeneration(entry.config, canonical) {
			delete(r.entries, sessionID)
		}
	}
	r.mu.Unlock()
}

func (r *sessionModelPinRegistry) retain(ctx context.Context, sessionID string, raw ModelConfig) (func(), error) {
	if r == nil {
		return nil, errors.New("gatewayapp: Session model pin registry is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	config := cloneSessionModelConfig(normalizeModelConfig(raw))
	if sessionID == "" || strings.TrimSpace(config.ID) == "" {
		return nil, errors.New("gatewayapp: child Session and pinned model are required")
	}
	r.mu.RLock()
	canonical, ok := r.configured[strings.ToLower(strings.TrimSpace(config.ID))]
	resolveAPIKey := r.resolveAPIKey
	r.mu.RUnlock()
	if !configuredSessionModelPinMatches(ctx, config, canonical, ok, resolveAPIKey) {
		return nil, fmt.Errorf("gatewayapp: pinned model %q is not configured with the current provider profile", config.ID)
	}

	r.mu.Lock()
	canonical, ok = r.configured[strings.ToLower(strings.TrimSpace(config.ID))]
	if !ok || !sameConfiguredModelGeneration(config, canonical) {
		r.mu.Unlock()
		return nil, fmt.Errorf("gatewayapp: pinned model %q is not configured with the current provider profile", config.ID)
	}
	entry, exists := r.entries[sessionID]
	if exists && !samePinnedSessionModel(entry.config, config) {
		r.mu.Unlock()
		return nil, fmt.Errorf("gatewayapp: child Session %q already has a different pinned model", sessionID)
	}
	entry.config = config
	entry.refs++
	r.entries[sessionID] = entry
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			entry, ok := r.entries[sessionID]
			if ok && entry.refs > 1 {
				entry.refs--
				r.entries[sessionID] = entry
			} else {
				delete(r.entries, sessionID)
			}
			r.mu.Unlock()
		})
	}, nil
}

func sameConfiguredModelGeneration(left, right ModelConfig) bool {
	return reflect.DeepEqual(configuredModelGeneration(left), configuredModelGeneration(right))
}

func configuredModelGeneration(raw ModelConfig) ModelConfig {
	config := cloneSessionModelConfig(raw)
	// ReasoningEffort is a Session selection copied into a child pin, not part
	// of the provider execution generation. Clear both effort fields before
	// normalization so the selected effort cannot be promoted into the default.
	config.ReasoningEffort = ""
	config.DefaultReasoningEffort = ""
	config = normalizeModelConfig(config)
	config.HTTPClient = nil
	config.CredentialPath = ""
	config.Token = ""
	config.PersistToken = false
	return config
}

func configuredSessionModelPinMatches(
	ctx context.Context,
	config ModelConfig,
	canonical ModelConfig,
	configured bool,
	resolveAPIKey func(context.Context, string) (string, error),
) bool {
	if !configured || !sameConfiguredModelGeneration(config, canonical) {
		return false
	}
	ref := strings.ToLower(strings.TrimSpace(config.CredentialRef))
	if !strings.HasPrefix(ref, "apikey:") {
		return true
	}
	if resolveAPIKey == nil || config.Token == "" {
		return false
	}
	current, err := resolveAPIKey(contextOrBackground(ctx), ref)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(config.Token), []byte(current)) == 1
}

func samePinnedSessionModel(left, right ModelConfig) bool {
	left = cloneSessionModelConfig(normalizeModelConfig(left))
	right = cloneSessionModelConfig(normalizeModelConfig(right))
	if left.HTTPClient != right.HTTPClient {
		return false
	}
	left.HTTPClient = nil
	right.HTTPClient = nil
	return reflect.DeepEqual(left, right)
}

func (r *sessionModelPinRegistry) config(ctx context.Context, sessionID string) (ModelConfig, bool) {
	if r == nil {
		return ModelConfig{}, false
	}
	sessionID = strings.TrimSpace(sessionID)
	r.mu.RLock()
	entry, ok := r.entries[sessionID]
	if !ok {
		r.mu.RUnlock()
		return ModelConfig{}, false
	}
	canonical, configured := r.configured[strings.ToLower(strings.TrimSpace(entry.config.ID))]
	resolveAPIKey := r.resolveAPIKey
	r.mu.RUnlock()
	if !configuredSessionModelPinMatches(ctx, entry.config, canonical, configured, resolveAPIKey) {
		r.mu.Lock()
		if current, exists := r.entries[sessionID]; exists && samePinnedSessionModel(current.config, entry.config) {
			delete(r.entries, sessionID)
		}
		r.mu.Unlock()
		return ModelConfig{}, false
	}
	return cloneSessionModelConfig(entry.config), true
}

func (s *runtimeComposition) prepareSpawnedACPSession(
	ctx context.Context,
	_ tasksubagent.SpawnContext,
	sessionID string,
	config assembly.AgentConfig,
) (controlagents.SessionOptions, error) {
	options := controlagents.NormalizeSessionOptions(config.SessionOptions)
	if config.PinnedModel == nil {
		return options, nil
	}
	if s == nil || s.lookup == nil {
		return controlagents.SessionOptions{}, errors.New("gatewayapp: Runtime model lookup is unavailable")
	}
	pinned, err := s.lookup.materializeAPIKeyCredential(ctx, *config.PinnedModel)
	if err != nil {
		return controlagents.SessionOptions{}, fmt.Errorf("gatewayapp: pin child Session model credential: %w", err)
	}
	if err := s.retainSpawnedSessionModelPin(ctx, sessionID, pinned); err != nil {
		return controlagents.SessionOptions{}, err
	}
	if err := s.selectPinnedSpawnedSessionModel(ctx, sessionID, pinned); err != nil {
		s.releaseSpawnedSessionModelPin(sessionID)
		return controlagents.SessionOptions{}, err
	}
	// The pinned provider configuration is process-local Control state. It must
	// never cross the ACP boundary as a model/config value. Generic ACP
	// configuration still applies presentation mode and other safe defaults.
	options.ModelID = ""
	if options.ReasoningEffortConfigID != "" {
		delete(options.ConfigValues, options.ReasoningEffortConfigID)
	}
	delete(options.ConfigValues, acpConfigReasoningID)
	options.ReasoningEffortConfigID = ""
	return controlagents.NormalizeSessionOptions(options), nil
}

func (s *runtimeComposition) retainSpawnedSessionModelPin(ctx context.Context, sessionID string, config ModelConfig) error {
	if s == nil || s.authorities.sessionModelPins == nil {
		return errors.New("gatewayapp: Session model pin registry is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	s.spawnedSessionPinsMu.Lock()
	defer s.spawnedSessionPinsMu.Unlock()
	if _, exists := s.spawnedSessionPinReleases[sessionID]; exists {
		pinned, ok := s.authorities.sessionModelPins.config(ctx, sessionID)
		if !ok || !samePinnedSessionModel(pinned, config) {
			return fmt.Errorf("gatewayapp: child Session %q pinned model changed", sessionID)
		}
		return nil
	}
	release, err := s.authorities.sessionModelPins.retain(ctx, sessionID, config)
	if err != nil {
		return err
	}
	if s.spawnedSessionPinReleases == nil {
		s.spawnedSessionPinReleases = map[string]func(){}
	}
	s.spawnedSessionPinReleases[sessionID] = release
	return nil
}

func (s *runtimeComposition) releaseSpawnedSessionModelPin(sessionID string) {
	if s == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	s.spawnedSessionPinsMu.Lock()
	release := s.spawnedSessionPinReleases[sessionID]
	delete(s.spawnedSessionPinReleases, sessionID)
	s.spawnedSessionPinsMu.Unlock()
	if release != nil {
		release()
	}
}

func (s *runtimeComposition) selectPinnedSpawnedSessionModel(ctx context.Context, sessionID string, pinned ModelConfig) error {
	if s == nil || s.sessions == nil {
		return errors.New("gatewayapp: Sessions service is unavailable")
	}
	ref := session.SessionRef{SessionID: strings.TrimSpace(sessionID)}
	effort := strings.TrimSpace(pinned.ReasoningEffort)
	var conflictErr error
	for attempt := 0; attempt < sessionModelRecoveryMaxAttempts; attempt++ {
		active, err := s.sessions.Session(ctx, ref)
		if err != nil {
			return err
		}
		state, err := s.sessions.SnapshotState(ctx, active.SessionRef)
		if err != nil {
			return err
		}
		if strings.EqualFold(kernel.CurrentModelAlias(state), pinned.ID) &&
			strings.EqualFold(kernel.CurrentReasoningEffort(state), effort) {
			return nil
		}
		expectedRevision := active.Revision
		_, err = s.sessions.UpdateState(ctx, session.UpdateStateRequest{
			SessionRef:       active.SessionRef,
			ExpectedRevision: &expectedRevision,
			MutationGuard:    session.ControlMutationGuard(session.ControlMutationPurposeConfiguration),
			Update: func(current map[string]any) (map[string]any, error) {
				next := session.CloneState(current)
				if next == nil {
					next = map[string]any{}
				}
				next[kernel.StateCurrentModelAlias] = pinned.ID
				if effort == "" {
					delete(next, kernel.StateCurrentReasoningEffort)
				} else {
					next[kernel.StateCurrentReasoningEffort] = effort
				}
				return next, nil
			},
		})
		if err == nil {
			return nil
		}
		if !errors.Is(err, session.ErrRevisionConflict) {
			return err
		}
		conflictErr = err
	}
	return conflictErr
}

func (s *runtimeComposition) releaseSpawnedSessionModelPins() {
	if s == nil {
		return
	}
	s.spawnedSessionPinsMu.Lock()
	releases := s.spawnedSessionPinReleases
	s.spawnedSessionPinReleases = nil
	s.spawnedSessionPinsMu.Unlock()
	for _, release := range releases {
		if release != nil {
			release()
		}
	}
}
