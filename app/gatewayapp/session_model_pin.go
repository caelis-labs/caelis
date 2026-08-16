package gatewayapp

import (
	"context"
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
	mu      sync.RWMutex
	entries map[string]sessionModelPinEntry
}

type sessionModelPinEntry struct {
	config ModelConfig
	refs   int
}

func newSessionModelPinRegistry() *sessionModelPinRegistry {
	return &sessionModelPinRegistry{entries: map[string]sessionModelPinEntry{}}
}

func (r *sessionModelPinRegistry) retain(sessionID string, raw ModelConfig) (func(), error) {
	if r == nil {
		return nil, errors.New("gatewayapp: Session model pin registry is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	config := cloneSessionModelConfig(normalizeModelConfig(raw))
	if sessionID == "" || strings.TrimSpace(config.ID) == "" {
		return nil, errors.New("gatewayapp: child Session and pinned model are required")
	}
	r.mu.Lock()
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

func (r *sessionModelPinRegistry) config(sessionID string) (ModelConfig, bool) {
	if r == nil {
		return ModelConfig{}, false
	}
	r.mu.RLock()
	entry, ok := r.entries[strings.TrimSpace(sessionID)]
	r.mu.RUnlock()
	if !ok {
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
	pinned := cloneSessionModelConfig(*config.PinnedModel)
	if err := s.retainSpawnedSessionModelPin(sessionID, pinned); err != nil {
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

func (s *runtimeComposition) retainSpawnedSessionModelPin(sessionID string, config ModelConfig) error {
	if s == nil || s.sessionModelPins == nil {
		return errors.New("gatewayapp: Session model pin registry is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	s.spawnedSessionPinsMu.Lock()
	defer s.spawnedSessionPinsMu.Unlock()
	if _, exists := s.spawnedSessionPinReleases[sessionID]; exists {
		pinned, ok := s.sessionModelPins.config(sessionID)
		if !ok || !samePinnedSessionModel(pinned, config) {
			return fmt.Errorf("gatewayapp: child Session %q pinned model changed", sessionID)
		}
		return nil
	}
	release, err := s.sessionModelPins.retain(sessionID, config)
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
	if s == nil || s.Sessions == nil {
		return errors.New("gatewayapp: Sessions service is unavailable")
	}
	ref := session.SessionRef{SessionID: strings.TrimSpace(sessionID)}
	effort := strings.TrimSpace(pinned.ReasoningEffort)
	var conflictErr error
	for attempt := 0; attempt < sessionModelRecoveryMaxAttempts; attempt++ {
		active, err := s.Sessions.Session(ctx, ref)
		if err != nil {
			return err
		}
		state, err := s.Sessions.SnapshotState(ctx, active.SessionRef)
		if err != nil {
			return err
		}
		if strings.EqualFold(kernel.CurrentModelAlias(state), pinned.ID) &&
			strings.EqualFold(kernel.CurrentReasoningEffort(state), effort) {
			return nil
		}
		expectedRevision := active.Revision
		_, err = s.Sessions.UpdateState(ctx, session.UpdateStateRequest{
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
