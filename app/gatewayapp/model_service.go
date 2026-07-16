package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/ports/gateway"
)

// Connect reconfigures the model provider on the live stack. The new config
// takes effect for subsequent turns.
func (s *Stack) Connect(cfg ModelConfig) (string, error) {
	modelIDs, err := s.ConnectModels([]ModelConfig{cfg})
	if err != nil {
		return "", err
	}
	if len(modelIDs) == 0 {
		return "", fmt.Errorf("gatewayapp: connect produced no model")
	}
	return modelIDs[0], nil
}

// ConnectModels atomically persists one or more provider-backed models. The
// first model remains the default and active model after the transaction.
func (s *Stack) ConnectModels(configs []ModelConfig) ([]string, error) {
	if s == nil {
		return nil, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	if len(configs) == 0 {
		return nil, fmt.Errorf("gatewayapp: at least one model config is required")
	}
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
	s.agentRosterMu.Lock()
	defer s.agentRosterMu.Unlock()
	if err := s.rejectReconfigureWhileActive("connect model"); err != nil {
		return nil, err
	}
	if s.lookup == nil {
		return nil, fmt.Errorf("gatewayapp: model lookup unavailable")
	}
	txn, err := s.beginModelConfigTransaction()
	if err != nil {
		return nil, err
	}
	modelIDs := make([]string, 0, len(configs))
	for _, cfg := range configs {
		modelID, err := s.lookup.upsert(cfg, false)
		if err != nil {
			return nil, txn.rollback(fmt.Errorf("gatewayapp: invalid model config: %w", err))
		}
		modelIDs = append(modelIDs, modelID)
	}
	activeID := modelIDs[0]
	s.lookup.SetDefault(activeID)
	activeConfig, _ := s.lookup.Config(activeID)
	s.setRuntimeModel(activeConfig)
	txn.applyResolver()
	if s.store == nil {
		return nil, txn.rollback(fmt.Errorf("gatewayapp: app config store unavailable"))
	}
	doc, err := s.store.Load()
	if err != nil {
		return nil, txn.rollback(err)
	}
	rosterSelections := make([]controlagents.ModelBackingSelection, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		model, ok := s.lookup.Config(modelID)
		if !ok {
			return nil, txn.rollback(fmt.Errorf("gatewayapp: connected model %q is unavailable", modelID))
		}
		rosterSelections = append(rosterSelections, controlagents.ModelBackingSelection{
			Alias: model.ID, Name: firstNonEmpty(model.Alias, model.Model, model.ID), Namespace: model.Provider,
		})
	}
	nextRoster, _, err := controlagents.UpsertModelBackedAgents(doc.AgentRoster, rosterSelections, rosterAgentNameAllowed)
	if err != nil {
		return nil, txn.rollback(err)
	}
	txn.captureAgentRoster(doc.AgentRoster)
	doc.Models = s.lookup.Snapshot()
	doc.AgentRoster = nextRoster
	if err := s.store.Save(doc); err != nil {
		return nil, txn.rollback(err)
	}
	txn.markStoreSaved()
	if err := s.refreshConfiguredAgentsFromStore(); err != nil {
		return nil, txn.rollback(err)
	}
	return modelIDs, nil
}

// UseModel persists one per-session model alias override for subsequent turns.
func (s *Stack) UseModel(ctx context.Context, ref session.SessionRef, alias string, reasoningEffort ...string) error {
	if s == nil || s.Sessions == nil {
		return fmt.Errorf("gatewayapp: sessions service unavailable")
	}
	s.reconfigureMu.Lock()
	defer s.reconfigureMu.Unlock()
	s.agentRosterMu.Lock()
	defer s.agentRosterMu.Unlock()
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
	s.agentRosterMu.Lock()
	defer s.agentRosterMu.Unlock()
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
	if s.store == nil {
		return fmt.Errorf("gatewayapp: app config store unavailable")
	}
	doc, err := s.store.Load()
	if err != nil {
		return err
	}
	nextRoster := controlagents.RemoveModelBackedAgent(doc.AgentRoster, cfg.ID)
	if err := s.rejectRemovedBoundACPAgents(ctx, doc.AgentRoster, nextRoster); err != nil {
		return err
	}
	if err := s.lookup.Delete(alias); err != nil {
		return err
	}
	hasDefault := strings.TrimSpace(s.lookup.DefaultID()) != ""
	txn.applyResolver()
	txn.captureAgentRoster(doc.AgentRoster)
	doc.Models = s.lookup.Snapshot()
	doc.AgentRoster = nextRoster
	if err := s.store.Save(doc); err != nil {
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
	previousAgentRoster   controlagents.Configuration
	restoreAgentRoster    bool
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

func (t *modelConfigTransaction) captureAgentRoster(roster controlagents.Configuration) {
	if t == nil {
		return
	}
	t.previousAgentRoster = controlagents.NormalizeConfiguration(roster)
	t.restoreAgentRoster = true
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
	if !t.storeSaved {
		return cause
	}
	var saveErr error
	if t.restoreAgentRoster {
		saveErr = t.stack.saveModelConfigsAndAgentRoster(t.previousAgentRoster)
	} else {
		saveErr = t.stack.saveModelConfigs()
	}
	if saveErr != nil {
		return errors.Join(cause, fmt.Errorf("gatewayapp: rollback model config save failed: %w", saveErr))
	}
	if refreshErr := t.stack.refreshConfiguredAgentsFromStore(); refreshErr != nil {
		return errors.Join(cause, fmt.Errorf("gatewayapp: rollback Agent assembly refresh failed: %w", refreshErr))
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
	return s.refreshAgentAssembly()
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
