// Package systemagent owns model bindings for Control-managed system Agents.
package systemagent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelcatalog"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

// ID identifies one fixed Control-managed system Agent.
type ID string

const (
	// Guardian reviews approval requests.
	Guardian ID = "guardian"
	// Reviewer runs the fixed workspace review scene.
	Reviewer ID = "reviewer"
)

// Definition describes one configurable system Agent.
type Definition struct {
	ID          ID
	Name        string
	Description string
}

var definitions = []Definition{
	{ID: Guardian, Name: "Guardian", Description: "Reviews tool approval requests and safety policy."},
	{ID: Reviewer, Name: "Reviewer", Description: "Reviews current workspace changes through the fixed review scene."},
}

// Definitions returns the fixed system Agents in presentation order.
func Definitions() []Definition {
	return append([]Definition(nil), definitions...)
}

// Binding maps one system Agent to a model-backed Agent in the Control roster.
// A missing binding inherits the product's current default behavior. The
// optional ReasoningEffort overrides the configured model effort for that
// system Agent only.
type Binding struct {
	ID              ID     `json:"id,omitempty"`
	AgentID         string `json:"agent_id,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// Configuration is the persisted system-Agent model configuration.
type Configuration struct {
	Bindings []Binding `json:"bindings,omitempty"`
}

// AgentStatus is one effective system-Agent binding.
type AgentStatus struct {
	Definition Definition
	Binding    Binding
	Agent      controlagents.Agent
}

// TargetStatus is one model-backed roster Agent eligible for a system Agent.
type TargetStatus struct {
	Agent controlagents.Agent
	Model modelconfig.Config
}

// Status is the complete system-Agent configuration view.
type Status struct {
	Agents  []AgentStatus
	Targets []TargetStatus
}

// BindRequest binds one fixed system Agent to a model-backed roster Agent.
type BindRequest struct {
	ID              ID
	AgentID         string
	ReasoningEffort string
}

// Service is the narrow Control-owned system-Agent configuration capability.
type Service interface {
	SystemAgentStatus(context.Context) (Status, error)
	BindSystemAgent(context.Context, BindRequest) (Status, error)
	ResetSystemAgent(context.Context, ID) (Status, error)
}

// NormalizeID canonicalizes one system-Agent identifier.
func NormalizeID(id ID) ID {
	return ID(strings.ToLower(strings.TrimSpace(string(id))))
}

// NormalizeConfiguration returns a detached deterministic configuration.
func NormalizeConfiguration(in Configuration) Configuration {
	out := Configuration{}
	seen := make(map[ID]struct{}, len(in.Bindings))
	for _, raw := range in.Bindings {
		binding := normalizeBinding(raw)
		if binding.ID == "" || binding.AgentID == "" {
			continue
		}
		if _, ok := seen[binding.ID]; ok {
			continue
		}
		seen[binding.ID] = struct{}{}
		out.Bindings = append(out.Bindings, binding)
	}
	sort.Slice(out.Bindings, func(i, j int) bool {
		return definitionOrder(out.Bindings[i].ID) < definitionOrder(out.Bindings[j].ID)
	})
	return out
}

// ListBindings returns every fixed system Agent in presentation order.
func ListBindings(in Configuration) []Binding {
	configured := make(map[ID]Binding, len(in.Bindings))
	for _, binding := range NormalizeConfiguration(in).Bindings {
		configured[binding.ID] = binding
	}
	out := make([]Binding, 0, len(definitions))
	for _, definition := range definitions {
		binding := configured[definition.ID]
		binding.ID = definition.ID
		out = append(out, binding)
	}
	return out
}

// LookupBinding returns one fixed system-Agent binding.
func LookupBinding(in Configuration, id ID) (Binding, bool) {
	id = NormalizeID(id)
	if !knownID(id) {
		return Binding{}, false
	}
	for _, binding := range ListBindings(in) {
		if binding.ID == id {
			return binding, true
		}
	}
	return Binding{}, false
}

// ValidateConfiguration validates fixed identities and model-backed roster
// references. External ACP Agents cannot back an in-process system scene.
func ValidateConfiguration(in Configuration, roster controlagents.Configuration, models []modelconfig.Config) error {
	modelByID := configuredModelsByID(models)
	seen := make(map[ID]struct{}, len(in.Bindings))
	for _, raw := range in.Bindings {
		binding := normalizeBinding(raw)
		if !knownID(binding.ID) {
			return fmt.Errorf("control/systemagent: unknown system Agent %q", strings.TrimSpace(string(raw.ID)))
		}
		if _, exists := seen[binding.ID]; exists {
			return fmt.Errorf("control/systemagent: duplicate binding for %q", binding.ID)
		}
		seen[binding.ID] = struct{}{}
		if binding.AgentID == "" {
			return fmt.Errorf("control/systemagent: binding for %q requires an Agent ID", binding.ID)
		}
		agent, ok := controlagents.LookupAgent(roster, binding.AgentID)
		if !ok {
			return fmt.Errorf("control/systemagent: %q references unknown Agent %q", binding.ID, binding.AgentID)
		}
		modelID := strings.ToLower(strings.TrimSpace(agent.Backing.ModelAlias))
		if modelID == "" {
			return fmt.Errorf("control/systemagent: %q requires a model-backed Agent", binding.ID)
		}
		configured, ok := modelByID[modelID]
		if !ok {
			return fmt.Errorf("control/systemagent: %q references unavailable model %q", binding.ID, agent.Backing.ModelAlias)
		}
		if !modelconfig.SupportsReasoningEffort(configured, binding.ReasoningEffort) {
			return fmt.Errorf(
				"control/systemagent: reasoning effort %q is not supported by model-backed Agent %q",
				binding.ReasoningEffort,
				agent.ID,
			)
		}
	}
	return nil
}

// Bind binds one fixed system Agent to an existing model-backed roster Agent.
func Bind(current Configuration, req BindRequest, roster controlagents.Configuration, models []modelconfig.Config) (Configuration, error) {
	id := NormalizeID(req.ID)
	if !knownID(id) {
		return Configuration{}, fmt.Errorf("control/systemagent: unknown system Agent %q", id)
	}
	next := Configuration{}
	for _, binding := range NormalizeConfiguration(current).Bindings {
		if binding.ID != id {
			next.Bindings = append(next.Bindings, binding)
		}
	}
	next.Bindings = append(next.Bindings, normalizeBinding(Binding{
		ID:              id,
		AgentID:         req.AgentID,
		ReasoningEffort: req.ReasoningEffort,
	}))
	if err := ValidateConfiguration(next, roster, models); err != nil {
		return Configuration{}, err
	}
	return NormalizeConfiguration(next), nil
}

// Reset restores one system Agent to its implicit default model behavior.
func Reset(current Configuration, id ID) (Configuration, error) {
	id = NormalizeID(id)
	if !knownID(id) {
		return Configuration{}, fmt.Errorf("control/systemagent: unknown system Agent %q", id)
	}
	next := Configuration{}
	for _, binding := range NormalizeConfiguration(current).Bindings {
		if binding.ID != id {
			next.Bindings = append(next.Bindings, binding)
		}
	}
	return NormalizeConfiguration(next), nil
}

// ResetAgentBindings removes every system-Agent binding to one roster Agent.
func ResetAgentBindings(current Configuration, agentID string) Configuration {
	agentID = controlagents.NormalizeName(agentID)
	next := Configuration{}
	for _, binding := range NormalizeConfiguration(current).Bindings {
		if binding.AgentID != agentID {
			next.Bindings = append(next.Bindings, binding)
		}
	}
	return NormalizeConfiguration(next)
}

// ResolveModel returns the configured model for one bound system Agent.
func ResolveModel(configuration Configuration, id ID, roster controlagents.Configuration, models []modelconfig.Config) (modelconfig.Config, bool, error) {
	binding, ok := LookupBinding(configuration, id)
	if !ok {
		return modelconfig.Config{}, false, fmt.Errorf("control/systemagent: unknown system Agent %q", NormalizeID(id))
	}
	if binding.AgentID == "" {
		return modelconfig.Config{}, false, nil
	}
	agent, ok := controlagents.LookupAgent(roster, binding.AgentID)
	if !ok {
		return modelconfig.Config{}, false, fmt.Errorf("control/systemagent: %q references unknown Agent %q", binding.ID, binding.AgentID)
	}
	configured, ok := configuredModelsByID(models)[strings.ToLower(strings.TrimSpace(agent.Backing.ModelAlias))]
	if !ok {
		return modelconfig.Config{}, false, fmt.Errorf("control/systemagent: %q references unavailable model %q", binding.ID, agent.Backing.ModelAlias)
	}
	if binding.ReasoningEffort != "" {
		configured.ReasoningEffort = binding.ReasoningEffort
	}
	return configured, true, nil
}

func normalizeBinding(binding Binding) Binding {
	binding.ID = NormalizeID(binding.ID)
	binding.AgentID = controlagents.NormalizeName(binding.AgentID)
	binding.ReasoningEffort = modelcatalog.NormalizeReasoningEffort(binding.ReasoningEffort)
	return binding
}

func knownID(id ID) bool {
	for _, definition := range definitions {
		if definition.ID == id {
			return true
		}
	}
	return false
}

func definitionOrder(id ID) int {
	for i, definition := range definitions {
		if definition.ID == id {
			return i
		}
	}
	return len(definitions)
}

func configuredModelsByID(models []modelconfig.Config) map[string]modelconfig.Config {
	out := make(map[string]modelconfig.Config, len(models))
	for _, raw := range models {
		configured := modelconfig.NormalizeConfig(raw)
		if configured.ID != "" {
			out[strings.ToLower(strings.TrimSpace(configured.ID))] = configured
		}
	}
	return out
}
