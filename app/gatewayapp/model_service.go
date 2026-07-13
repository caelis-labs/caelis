package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/ports/gateway"
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
	txn, err := s.beginModelConfigTransaction()
	if err != nil {
		return "", err
	}
	modelID, err := s.lookup.Upsert(cfg)
	if err != nil {
		return "", fmt.Errorf("gatewayapp: invalid model config: %w", err)
	}
	cfg, _ = s.lookup.Config(modelID)
	s.setRuntimeModel(cfg)
	txn.applyResolver()
	if err := s.saveModelConfigs(); err != nil {
		return "", txn.rollback(err)
	}
	txn.markStoreSaved()
	if err := s.refreshConfiguredAgentsFromStore(); err != nil {
		return "", txn.rollback(err)
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
	previousState, err := s.Sessions.SnapshotState(ctx, ref)
	if err != nil {
		return err
	}
	txn, err := s.beginModelConfigTransaction()
	if err != nil {
		return err
	}
	if reasoning != "" {
		cfg.ReasoningEffort = reasoning
		if _, err := s.lookup.Upsert(cfg); err != nil {
			return err
		}
	}
	s.lookup.SetDefault(cfg.ID)
	txn.applyResolver()
	if err := s.saveModelConfigs(); err != nil {
		return txn.rollback(err)
	}
	txn.markStoreSaved()
	s.setRuntimeDefaultModelFromLookup()
	if err := s.refreshConfiguredAgentsFromStore(); err != nil {
		return txn.rollback(err)
	}
	if _, err := s.updateSessionState(ctx, ref, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next[gateway.StateCurrentModelAlias] = cfg.ID
		if reasoning != "" {
			next[gateway.StateCurrentReasoningEffort] = reasoning
		} else {
			delete(next, gateway.StateCurrentReasoningEffort)
		}
		return next, nil
	}); err != nil {
		return txn.rollbackWithState(ctx, ref, previousState, err)
	}
	return nil
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
	previousState, err := s.Sessions.SnapshotState(ctx, ref)
	if err != nil {
		return err
	}
	txn, err := s.beginModelConfigTransaction()
	if err != nil {
		return err
	}
	if err := s.lookup.Delete(alias); err != nil {
		return err
	}
	hasDefault := strings.TrimSpace(s.lookup.DefaultID()) != ""
	txn.applyResolver()
	if err := s.saveModelConfigs(); err != nil {
		return txn.rollback(err)
	}
	txn.markStoreSaved()
	s.setRuntimeDefaultModelFromLookup()
	if err := s.refreshConfiguredAgentsFromStore(); err != nil {
		return txn.rollback(err)
	}
	if _, err := s.updateSessionState(ctx, ref, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		current, _ := next[gateway.StateCurrentModelAlias].(string)
		if alias == "" || strings.EqualFold(strings.TrimSpace(current), cfg.ID) || strings.EqualFold(strings.TrimSpace(current), cfg.Alias) || !hasDefault {
			delete(next, gateway.StateCurrentModelAlias)
			delete(next, gateway.StateCurrentReasoningEffort)
		}
		return next, nil
	}); err != nil {
		return txn.rollbackWithState(ctx, ref, previousState, err)
	}
	return nil
}

type modelConfigTransaction struct {
	stack                 *Stack
	resolver              *kernelimpl.AssemblyResolver
	previousLookup        persistedModelConfig
	previousContextWindow int
	previousRuntime       stackRuntimeConfig
	storeSaved            bool
}

func (s *Stack) beginModelConfigTransaction() (modelConfigTransaction, error) {
	gw := s.currentGateway()
	if gw == nil {
		return modelConfigTransaction{}, fmt.Errorf("gatewayapp: gateway is unavailable")
	}
	resolver := gw.Resolver()
	if resolver == nil {
		return modelConfigTransaction{}, fmt.Errorf("gatewayapp: resolver not available")
	}
	s.lookup.mu.RLock()
	previousContextWindow := s.lookup.contextWindow
	s.lookup.mu.RUnlock()
	s.mu.RLock()
	previousRuntime := s.runtime
	s.mu.RUnlock()
	return modelConfigTransaction{
		stack:                 s,
		resolver:              resolver,
		previousLookup:        s.lookup.Snapshot(),
		previousContextWindow: previousContextWindow,
		previousRuntime:       previousRuntime,
	}, nil
}

func (t *modelConfigTransaction) applyResolver() {
	if t == nil || t.stack == nil || t.resolver == nil {
		return
	}
	t.resolver.SetModelLookup(t.stack.lookup, t.stack.lookup.DefaultID())
}

func (t *modelConfigTransaction) markStoreSaved() {
	if t != nil {
		t.storeSaved = true
	}
}

func (t *modelConfigTransaction) rollback(cause error) error {
	if t == nil || t.stack == nil {
		return cause
	}
	t.stack.lookup.Restore(t.previousLookup, t.previousContextWindow)
	t.stack.mu.Lock()
	t.stack.runtime = t.previousRuntime
	t.stack.mu.Unlock()
	t.applyResolver()
	if t.storeSaved {
		if err := t.stack.saveModelConfigs(); err != nil {
			return errors.Join(cause, fmt.Errorf("gatewayapp: rollback model config save failed: %w", err))
		}
	}
	return cause
}

func (t *modelConfigTransaction) rollbackWithState(ctx context.Context, ref session.SessionRef, previousState map[string]any, cause error) error {
	err := t.rollback(cause)
	if t == nil || t.stack == nil || t.stack.Sessions == nil {
		return err
	}
	if _, restoreErr := t.stack.replaceSessionState(ctx, ref, previousState); restoreErr != nil {
		return errors.Join(err, fmt.Errorf("gatewayapp: rollback session model state failed: %w", restoreErr))
	}
	return err
}

func (s *Stack) setRuntimeDefaultModelFromLookup() {
	if s == nil || s.lookup == nil {
		return
	}
	cfg := ModelConfig{}
	if defaultID := s.lookup.DefaultID(); strings.TrimSpace(defaultID) != "" {
		cfg, _ = s.lookup.Config(defaultID)
	}
	s.setRuntimeModel(cfg)
}

func (s *Stack) setRuntimeModel(cfg ModelConfig) {
	if s == nil {
		return
	}
	s.mu.Lock()
	runtimeCfg := s.runtime
	runtimeCfg.Model = cfg
	s.runtime = runtimeCfg
	s.mu.Unlock()
}

func (s *Stack) refreshConfiguredAgentsFromStore() error {
	if s == nil {
		return fmt.Errorf("gatewayapp: stack is unavailable")
	}
	if s.refreshConfiguredAgentsHook != nil {
		return s.refreshConfiguredAgentsHook()
	}
	if s.store == nil {
		return nil
	}
	doc, err := s.store.Load()
	if err != nil {
		return err
	}
	return s.setConfiguredAgents(doc.Agents)
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
		if modelRef := gateway.CurrentModelAlias(state); modelRef != "" {
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
