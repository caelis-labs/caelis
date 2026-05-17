package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

// Connect reconfigures the model provider on the live stack. The new config
// takes effect for subsequent turns.
func (s *Stack) Connect(cfg ModelConfig) (string, error) {
	if s == nil {
		return "", fmt.Errorf("gatewayapp: stack is unavailable")
	}
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
	if err := s.rejectReconfigureWhileActive("connect model"); err != nil {
		return "", err
	}
	if s.lookup == nil {
		return "", fmt.Errorf("gatewayapp: model lookup unavailable")
	}
	gw := s.CurrentGateway()
	if gw == nil {
		return "", fmt.Errorf("gatewayapp: gateway is unavailable")
	}
	resolver := gw.Resolver()
	if resolver == nil {
		return "", fmt.Errorf("gatewayapp: resolver not available")
	}
	previousLookup := s.lookup.Snapshot()
	s.lookup.mu.RLock()
	previousContextWindow := s.lookup.contextWindow
	s.lookup.mu.RUnlock()
	s.mu.RLock()
	previousRuntime := s.runtime
	s.mu.RUnlock()
	modelID, err := s.lookup.Upsert(cfg)
	if err != nil {
		return "", fmt.Errorf("gatewayapp: invalid model config: %w", err)
	}
	cfg, _ = s.lookup.Config(modelID)
	s.mu.Lock()
	runtimeCfg := s.runtime
	runtimeCfg.Model = cfg
	s.runtime = runtimeCfg
	s.mu.Unlock()
	resolver.SetModelLookup(s.lookup, s.lookup.DefaultID())
	if err := s.saveModelConfigs(); err != nil {
		s.lookup.Restore(previousLookup, previousContextWindow)
		s.mu.Lock()
		s.runtime = previousRuntime
		s.mu.Unlock()
		resolver.SetModelLookup(s.lookup, s.lookup.DefaultID())
		return "", err
	}
	return modelID, nil
}

// UseModel persists one per-session model alias override for subsequent turns.
func (s *Stack) UseModel(ctx context.Context, ref session.SessionRef, alias string, reasoningEffort ...string) error {
	if s == nil || s.Sessions == nil {
		return fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
	if err := s.rejectReconfigureWhileActive("switch model"); err != nil {
		return err
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return fmt.Errorf("gatewayapp: model alias is required")
	}
	if s.lookup == nil {
		return fmt.Errorf("gatewayapp: model lookup unavailable")
	}
	cfg, err := s.lookup.ResolveConfig(alias)
	if err != nil {
		return err
	}
	reasoning := ""
	if len(reasoningEffort) > 0 {
		reasoning = strings.TrimSpace(reasoningEffort[0])
		if reasoning != "" {
			if !modelConfigSupportsReasoningEffort(cfg, reasoning) {
				return fmt.Errorf("gatewayapp: model %q does not support reasoning level %q", alias, reasoning)
			}
		}
	}
	if s.lookup != nil {
		previousLookup := s.lookup.Snapshot()
		s.lookup.mu.RLock()
		previousContextWindow := s.lookup.contextWindow
		s.lookup.mu.RUnlock()
		if reasoning != "" {
			cfg, err := s.lookup.ResolveConfig(alias)
			if err != nil {
				return err
			}
			cfg.ReasoningEffort = reasoning
			if _, err := s.lookup.Upsert(cfg); err != nil {
				return err
			}
		}
		s.lookup.SetDefault(cfg.ID)
		gw := s.CurrentGateway()
		if gw == nil {
			s.lookup.Restore(previousLookup, previousContextWindow)
			return fmt.Errorf("gatewayapp: gateway is unavailable")
		}
		if resolver := gw.Resolver(); resolver != nil {
			resolver.SetModelLookup(s.lookup, s.lookup.DefaultID())
		}
		if err := s.saveModelConfigs(); err != nil {
			s.lookup.Restore(previousLookup, previousContextWindow)
			if resolver := gw.Resolver(); resolver != nil {
				resolver.SetModelLookup(s.lookup, s.lookup.DefaultID())
			}
			return err
		}
	}
	return s.Sessions.UpdateState(ctx, ref, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next[kernel.StateCurrentModelAlias] = cfg.ID
		if reasoning != "" {
			next[kernel.StateCurrentReasoningEffort] = reasoning
		} else {
			delete(next, kernel.StateCurrentReasoningEffort)
		}
		return next, nil
	})
}

// DeleteModel clears one per-session model alias override when it matches the
// supplied alias. This reverts the session back to the resolver default.
func (s *Stack) DeleteModel(ctx context.Context, ref session.SessionRef, alias string) error {
	if s == nil || s.Sessions == nil {
		return fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
	if err := s.rejectReconfigureWhileActive("delete model"); err != nil {
		return err
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return fmt.Errorf("gatewayapp: model alias is required")
	}
	if s.lookup == nil {
		return fmt.Errorf("gatewayapp: model lookup unavailable")
	}
	cfg, err := s.lookup.ResolveConfig(alias)
	if err != nil {
		return err
	}
	previousLookup := s.lookup.Snapshot()
	s.lookup.mu.RLock()
	previousContextWindow := s.lookup.contextWindow
	s.lookup.mu.RUnlock()
	if err := s.lookup.Delete(alias); err != nil {
		return err
	}
	hasDefault := strings.TrimSpace(s.lookup.DefaultID()) != ""
	gw := s.CurrentGateway()
	if gw == nil {
		s.lookup.Restore(previousLookup, previousContextWindow)
		return fmt.Errorf("gatewayapp: gateway is unavailable")
	}
	if resolver := gw.Resolver(); resolver != nil {
		resolver.SetModelLookup(s.lookup, s.lookup.DefaultID())
	}
	if err := s.saveModelConfigs(); err != nil {
		s.lookup.Restore(previousLookup, previousContextWindow)
		if resolver := gw.Resolver(); resolver != nil {
			resolver.SetModelLookup(s.lookup, s.lookup.DefaultID())
		}
		return err
	}
	return s.Sessions.UpdateState(ctx, ref, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		current, _ := next[kernel.StateCurrentModelAlias].(string)
		if alias == "" || strings.EqualFold(strings.TrimSpace(current), cfg.ID) || strings.EqualFold(strings.TrimSpace(current), cfg.Alias) || !hasDefault {
			delete(next, kernel.StateCurrentModelAlias)
			delete(next, kernel.StateCurrentReasoningEffort)
		}
		return next, nil
	})
}

// ListModelAliases returns the current session override plus resolver-known
// model aliases for picker surfaces such as the TUI `/model` command.
func (s *Stack) ListModelAliases(ctx context.Context, ref session.SessionRef) ([]string, error) {
	choices, err := s.ListModelChoices(ctx, ref)
	if err != nil {
		return nil, err
	}
	aliases := make([]string, 0, len(choices))
	for _, choice := range choices {
		aliases = append(aliases, choice.Alias)
	}
	return dedupeNonEmptyStrings(aliases), nil
}

func (s *Stack) ListModelChoices(ctx context.Context, ref session.SessionRef) ([]ModelChoice, error) {
	if s == nil || s.Sessions == nil {
		return nil, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	if s.lookup == nil {
		return nil, fmt.Errorf("gatewayapp: model lookup unavailable")
	}
	choices := make([]ModelChoice, 0, len(s.lookup.ListModelChoices())+1)
	if strings.TrimSpace(ref.SessionID) != "" {
		state, err := s.Sessions.SnapshotState(ctx, ref)
		if err != nil {
			return nil, err
		}
		if modelRef := kernel.CurrentModelAlias(state); modelRef != "" {
			if cfg, ok := s.lookup.Config(modelRef); ok {
				choices = append(choices, modelChoiceFromConfig(cfg))
			}
		}
	}
	choices = append(choices, s.lookup.ListModelChoices()...)
	return dedupeModelChoices(choices), nil
}

func (s *Stack) DefaultModelAlias() string {
	if s == nil || s.lookup == nil {
		return ""
	}
	return s.lookup.DefaultAlias()
}

func (s *Stack) DefaultModelID() string {
	if s == nil || s.lookup == nil {
		return ""
	}
	return s.lookup.DefaultID()
}

func (s *Stack) ModelConfig(alias string) (ModelConfig, bool) {
	if s == nil || s.lookup == nil {
		return ModelConfig{}, false
	}
	return s.lookup.Config(alias)
}

func (s *Stack) HasModelAlias(alias string) bool {
	if s == nil || s.lookup == nil {
		return false
	}
	return s.lookup.HasAlias(alias)
}

// ListProviderModels returns configured raw model names for a provider.
func (s *Stack) ListProviderModels(provider string) []string {
	if s == nil || s.lookup == nil {
		return nil
	}
	return s.lookup.ListProviderModels(provider)
}
