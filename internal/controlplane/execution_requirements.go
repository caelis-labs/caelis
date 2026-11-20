package controlplane

import (
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// ExecutionValidator checks the final tool set and model capabilities before
// Control hands a local invocation to Runtime.
type ExecutionValidator struct{}

// ValidateExecutionRequest checks the assembled request, not advertised tools
// or options that may be replaced before execution.
func (ExecutionValidator) ValidateExecutionRequest(req agent.RunRequest) error {
	options := req.Request.WithDefaults(req.AgentSpec.Request)
	output := options.OutputSpec()
	if err := model.ValidateOutputSpec(output); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(req.AgentSpec.Tools))
	for index, configuredTool := range req.AgentSpec.Tools {
		if configuredTool == nil {
			return fmt.Errorf("controlplane: configured tool %d is nil", index)
		}
		name := strings.TrimSpace(configuredTool.Definition().Name)
		if name == "" {
			return fmt.Errorf("controlplane: configured tool %d has no name", index)
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("controlplane: configured tool name %q is duplicated", name)
		}
		seen[key] = struct{}{}
	}
	// Share model requirement derivation with Runtime so Control preflight and
	// Runtime capability gates cannot drift.
	required := model.DeriveRequiredCapabilities(req.AgentSpec.RequiredModelCapabilities, options.StreamEnabled(false), output, len(req.AgentSpec.Tools))
	actual, _ := model.CapabilitiesOf(req.AgentSpec.Model)
	modelName := ""
	if req.AgentSpec.Model != nil {
		modelName = req.AgentSpec.Model.Name()
	}
	return model.ValidateCapabilities(modelName, actual, required)
}
