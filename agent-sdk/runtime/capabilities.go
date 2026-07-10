package runtime

import (
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func validateRunRequestAgentSpec(req agent.RunRequest) error {
	spec := cloneAgentSpec(req.AgentSpec)
	spec.Request = req.Request.WithDefaults(spec.Request)
	return validateAgentSpecCapabilities(
		spec.Model,
		spec.Tools,
		spec.Request.OutputSpec(),
		spec.Request.StreamEnabled(false),
		spec.RequiredModelCapabilities,
	)
}

func validateAgentSpecCapabilities(specModel model.LLM, tools []tool.Tool, requestOutput *model.OutputSpec, stream bool, declared model.Capabilities) error {
	required := declared
	if stream {
		required.Streaming = true
	}
	if requestOutput != nil && requestOutput.Mode != "" && requestOutput.Mode != model.OutputModeText {
		required.StructuredOutput = true
	}
	if len(tools) > 0 {
		required.ToolCalls = true
	}
	for _, item := range tools {
		if item != nil && item.Definition().Capabilities.ParallelSafe {
			required.ParallelToolCalls = true
			break
		}
	}
	actual, _ := model.CapabilitiesOf(specModel)
	name := ""
	if specModel != nil {
		name = specModel.Name()
	}
	return model.ValidateCapabilities(name, actual, required)
}
