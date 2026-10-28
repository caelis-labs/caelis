package runtime

import (
	"fmt"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func validateRunRequestAgentSpec(req agent.RunRequest) error {
	spec := cloneAgentSpec(req.AgentSpec)
	spec.Request = req.Request.WithDefaults(spec.Request)
	if spec.ResolveModelRequest != nil {
		// The complete model and tool capability check occurs on each resolved
		// snapshot, before its provider attempt can be admitted.
		return model.ValidateOutputSpec(spec.Request.OutputSpec())
	}
	return validateAgentSpecCapabilities(
		spec.Model,
		spec.Tools,
		spec.Request.OutputSpec(),
		spec.Request.StreamEnabled(false),
		spec.RequiredModelCapabilities,
	)
}

func validateAgentSpecCapabilities(specModel model.LLM, tools []tool.Tool, requestOutput *model.OutputSpec, stream bool, declared model.Capabilities) error {
	if err := model.ValidateOutputSpec(requestOutput); err != nil {
		return err
	}
	required := model.DeriveRequiredCapabilities(declared, stream, requestOutput, len(tools))
	actual, _ := model.CapabilitiesOf(specModel)
	name := ""
	if specModel != nil {
		name = specModel.Name()
	}
	return model.ValidateCapabilities(name, actual, required)
}

// CapabilityError identifies a required Runtime capability missing from an embedding.
// It is distinct from a stale snapshot: resolving the same source again cannot supply the capability.
type CapabilityError struct {
	Capability string
}

func (e *CapabilityError) Error() string {
	return fmt.Sprintf("agent-sdk/runtime: deferred tool source requires %s capability", e.Capability)
}
