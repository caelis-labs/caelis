package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	controlsystemagent "github.com/caelis-labs/caelis/control/systemagent"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

// SystemAgentService owns model bindings for fixed Control-managed Agents.
type SystemAgentService struct {
	stack *Stack
}

// SystemAgents returns the Control-owned system-Agent configuration service.
func (s *Stack) SystemAgents() SystemAgentService {
	return SystemAgentService{stack: s}
}

// SystemAgentStatus returns fixed system Agents and eligible model targets.
func (s SystemAgentService) SystemAgentStatus(ctx context.Context) (controlsystemagent.Status, error) {
	if s.stack == nil || s.stack.store == nil {
		return controlsystemagent.Status{}, fmt.Errorf("gatewayapp: system Agent configuration is unavailable")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return controlsystemagent.Status{}, err
		}
	}
	doc, err := s.stack.store.Load()
	if err != nil {
		return controlsystemagent.Status{}, err
	}
	return systemAgentStatusFromConfig(doc.SystemAgents, doc.AgentRoster, doc.Models.Configs), nil
}

// BindSystemAgent binds one fixed system Agent to a model-backed roster Agent.
func (s SystemAgentService) BindSystemAgent(ctx context.Context, req controlsystemagent.BindRequest) (controlsystemagent.Status, error) {
	return s.mutate(ctx, "bind system Agent", func(doc AppConfig) (controlsystemagent.Configuration, error) {
		return controlsystemagent.Bind(doc.SystemAgents, req, doc.AgentRoster, doc.Models.Configs)
	})
}

// ResetSystemAgent restores one fixed system Agent to its default model path.
func (s SystemAgentService) ResetSystemAgent(ctx context.Context, id controlsystemagent.ID) (controlsystemagent.Status, error) {
	return s.mutate(ctx, "reset system Agent", func(doc AppConfig) (controlsystemagent.Configuration, error) {
		return controlsystemagent.Reset(doc.SystemAgents, id)
	})
}

func (s SystemAgentService) mutate(
	ctx context.Context,
	action string,
	update func(AppConfig) (controlsystemagent.Configuration, error),
) (controlsystemagent.Status, error) {
	if s.stack == nil || s.stack.store == nil {
		return controlsystemagent.Status{}, fmt.Errorf("gatewayapp: system Agent configuration is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return controlsystemagent.Status{}, err
	}
	s.stack.reconfigureMu.Lock()
	defer s.stack.reconfigureMu.Unlock()
	s.stack.agentRosterMu.Lock()
	defer s.stack.agentRosterMu.Unlock()
	if err := s.stack.rejectReconfigureWhileActive(action); err != nil {
		return controlsystemagent.Status{}, err
	}
	doc, err := s.stack.store.Load()
	if err != nil {
		return controlsystemagent.Status{}, err
	}
	previous := doc
	next, err := update(doc)
	if err != nil {
		return controlsystemagent.Status{}, err
	}
	doc.SystemAgents = controlsystemagent.NormalizeConfiguration(next)
	if err := s.stack.store.Save(doc); err != nil {
		return controlsystemagent.Status{}, err
	}
	// Reviewer is a fixed hidden ACP scene whose concrete model configuration
	// lives in the shared Agent registry. The updater preserves live manager and
	// runner instances; the active-turn gate above prevents replacement during
	// an invocation.
	if err := s.stack.refreshConfiguredAgentsFromStore(); err != nil {
		rollbackErr := s.stack.store.Save(previous)
		refreshErr := s.stack.refreshConfiguredAgentsFromStore()
		return controlsystemagent.Status{}, errors.Join(err, rollbackErr, refreshErr)
	}
	return systemAgentStatusFromConfig(doc.SystemAgents, doc.AgentRoster, doc.Models.Configs), nil
}

func systemAgentStatusFromConfig(
	configuration controlsystemagent.Configuration,
	roster controlagents.Configuration,
	models []modelconfig.Config,
) controlsystemagent.Status {
	definitions := make(map[controlsystemagent.ID]controlsystemagent.Definition)
	for _, definition := range controlsystemagent.Definitions() {
		definitions[definition.ID] = definition
	}
	status := controlsystemagent.Status{}
	for _, binding := range controlsystemagent.ListBindings(configuration) {
		item := controlsystemagent.AgentStatus{Definition: definitions[binding.ID], Binding: binding}
		if binding.AgentID != "" {
			item.Agent, _ = controlagents.LookupAgent(roster, binding.AgentID)
		}
		status.Agents = append(status.Agents, item)
	}
	modelByID := make(map[string]modelconfig.Config, len(models))
	for _, raw := range models {
		configured := modelconfig.NormalizeConfig(raw)
		if configured.ID != "" {
			modelByID[strings.ToLower(strings.TrimSpace(configured.ID))] = configured
		}
	}
	for _, agent := range controlagents.ListAgents(roster) {
		model, ok := modelByID[strings.ToLower(strings.TrimSpace(agent.Backing.ModelAlias))]
		if !ok {
			continue
		}
		status.Targets = append(status.Targets, controlsystemagent.TargetStatus{Agent: agent, Model: model})
	}
	return status
}

func (s *Stack) resolveSystemAgentModel(
	ctx context.Context,
	id controlsystemagent.ID,
	contextWindow int,
) (kernelimpl.ModelResolution, bool, error) {
	if s == nil || s.store == nil {
		return kernelimpl.ModelResolution{}, false, nil
	}
	if s.lookup == nil {
		return kernelimpl.ModelResolution{}, false, fmt.Errorf("gatewayapp: resolve system Agent model: model lookup unavailable")
	}
	doc, err := s.store.Load()
	if err != nil {
		return kernelimpl.ModelResolution{}, false, err
	}
	configured, bound, err := controlsystemagent.ResolveModel(doc.SystemAgents, id, doc.AgentRoster, doc.Models.Configs)
	if err != nil || !bound {
		return kernelimpl.ModelResolution{}, false, err
	}
	hydrated, err := s.lookup.ResolveConfig(configured.ID)
	if err != nil {
		return kernelimpl.ModelResolution{}, false, err
	}
	if configured.ReasoningEffort != "" {
		hydrated.ReasoningEffort = configured.ReasoningEffort
	}
	resolved, err := s.lookup.ResolveModelConfig(ctx, hydrated, contextWindow)
	if err != nil {
		return kernelimpl.ModelResolution{}, false, err
	}
	return resolved, true, nil
}

// systemAgentReasoningModel applies a Control-selected system-Agent effort at
// the final model boundary. System scenes may have their own fallback effort,
// so this wrapper deliberately wins over request metadata when a binding
// supplies an explicit or model-default effort.
type systemAgentReasoningModel struct {
	inner  model.LLM
	effort string
}

func withSystemAgentReasoningEffort(resolved kernelimpl.ModelResolution) model.LLM {
	effort := strings.TrimSpace(resolved.ReasoningEffort)
	if resolved.Model == nil || effort == "" {
		return resolved.Model
	}
	return &systemAgentReasoningModel{inner: resolved.Model, effort: effort}
}

func (m *systemAgentReasoningModel) Name() string {
	if m == nil || m.inner == nil {
		return ""
	}
	return m.inner.Name()
}

func (m *systemAgentReasoningModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if m == nil || m.inner == nil {
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(nil, fmt.Errorf("gatewayapp: system Agent model is unavailable"))
		}
	}
	if req == nil {
		return m.inner.Generate(ctx, nil)
	}
	cloned := *req
	cloned.Reasoning.Effort = m.effort
	return m.inner.Generate(ctx, &cloned)
}

func (m *systemAgentReasoningModel) Capabilities() model.Capabilities {
	if m == nil || m.inner == nil {
		return model.Capabilities{}
	}
	capabilities, _ := model.CapabilitiesOf(m.inner)
	return capabilities
}

func (m *systemAgentReasoningModel) ProviderName() string {
	if m == nil || m.inner == nil {
		return ""
	}
	provider, _ := m.inner.(interface{ ProviderName() string })
	if provider == nil {
		return ""
	}
	return strings.TrimSpace(provider.ProviderName())
}

func (m *systemAgentReasoningModel) ContextWindowTokens() int {
	if m == nil || m.inner == nil {
		return 0
	}
	provider, _ := m.inner.(interface{ ContextWindowTokens() int })
	if provider == nil {
		return 0
	}
	return provider.ContextWindowTokens()
}

var _ controlsystemagent.Service = SystemAgentService{}
