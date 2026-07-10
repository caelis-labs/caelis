package controlplane

import (
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

// ExecutionRequirements is the Control-owned union derived from one fully
// assembled invocation and its actual tool set.
type ExecutionRequirements struct {
	Model   model.Capabilities
	Sandbox sandbox.CapabilitySet
}

// ExecutionValidatorConfig configures actual execution services selected by
// Control for preflight validation.
type ExecutionValidatorConfig struct {
	Sandbox sandbox.DescriptorProvider
}

// ExecutionValidator derives and validates one fully assembled invocation
// before Control hands it to Runtime.
type ExecutionValidator struct {
	sandbox sandbox.DescriptorProvider
}

func NewExecutionValidator(cfg ExecutionValidatorConfig) (*ExecutionValidator, error) {
	if cfg.Sandbox == nil {
		return nil, fmt.Errorf("controlplane: sandbox descriptor provider is required")
	}
	return &ExecutionValidator{sandbox: cfg.Sandbox}, nil
}

// ValidateExecutionRequest derives requirements from the actual model,
// request, and tool instances, then validates the configured implementations.
func (v *ExecutionValidator) ValidateExecutionRequest(req agent.RunRequest) error {
	_, err := v.ExecutionRequirements(req)
	return err
}

// ExecutionRequirements returns the validated requirements for one fully
// assembled invocation.
func (v *ExecutionValidator) ExecutionRequirements(req agent.RunRequest) (ExecutionRequirements, error) {
	if v == nil || v.sandbox == nil {
		return ExecutionRequirements{}, fmt.Errorf("controlplane: execution validator is not configured")
	}
	output := req.Request.WithDefaults(req.AgentSpec.Request).OutputSpec()
	if err := model.ValidateOutputSpec(output); err != nil {
		return ExecutionRequirements{}, err
	}
	requirements := ExecutionRequirements{Model: req.AgentSpec.RequiredModelCapabilities}
	if req.Request.WithDefaults(req.AgentSpec.Request).StreamEnabled(false) {
		requirements.Model.Streaming = true
	}
	if output != nil && output.Mode != "" && output.Mode != model.OutputModeText {
		requirements.Model.StructuredOutput = true
	}
	seen := make(map[string]struct{}, len(req.AgentSpec.Tools))
	for index, configuredTool := range req.AgentSpec.Tools {
		if configuredTool == nil {
			return ExecutionRequirements{}, fmt.Errorf("controlplane: configured tool %d is nil", index)
		}
		definition := configuredTool.Definition()
		name := strings.TrimSpace(definition.Name)
		if name == "" {
			return ExecutionRequirements{}, fmt.Errorf("controlplane: configured tool %d has no name", index)
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return ExecutionRequirements{}, fmt.Errorf("controlplane: configured tool name %q is duplicated", name)
		}
		seen[key] = struct{}{}
		if definition.ExecutionRequirements != nil {
			requirements.Sandbox = mergeSandboxCapabilities(requirements.Sandbox, definition.ExecutionRequirements.Sandbox)
		}
	}
	if len(req.AgentSpec.Tools) > 0 {
		requirements.Model.ToolCalls = true
	}
	actualModel, _ := model.CapabilitiesOf(req.AgentSpec.Model)
	modelName := ""
	if req.AgentSpec.Model != nil {
		modelName = req.AgentSpec.Model.Name()
	}
	if err := model.ValidateCapabilities(modelName, actualModel, requirements.Model); err != nil {
		return ExecutionRequirements{}, err
	}
	if err := sandbox.ValidateCapabilities(v.sandbox.Describe(), requirements.Sandbox); err != nil {
		return ExecutionRequirements{}, err
	}
	return requirements, nil
}

func mergeSandboxCapabilities(left, right sandbox.CapabilitySet) sandbox.CapabilitySet {
	return sandbox.CapabilitySet{
		FileSystem:     left.FileSystem || right.FileSystem,
		CommandExec:    left.CommandExec || right.CommandExec,
		AsyncSessions:  left.AsyncSessions || right.AsyncSessions,
		TTY:            left.TTY || right.TTY,
		NetworkControl: left.NetworkControl || right.NetworkControl,
		PathPolicy:     left.PathPolicy || right.PathPolicy,
		EnvPolicy:      left.EnvPolicy || right.EnvPolicy,
	}
}
