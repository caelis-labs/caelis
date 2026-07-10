package acpagentbridge_test

import "github.com/caelis-labs/caelis/agent-sdk/model"

func bridgeTestModelCapabilities() model.Capabilities {
	return model.Capabilities{
		ToolCalls:             true,
		StructuredOutput:      true,
		Streaming:             true,
		ParallelToolCalls:     true,
		ReasoningContinuation: true,
		HostedTools:           true,
	}
}

func (streamingTextModel) Capabilities() model.Capabilities { return bridgeTestModelCapabilities() }
func (duplicateStreamingTextModel) Capabilities() model.Capabilities {
	return bridgeTestModelCapabilities()
}
func (staticModel) Capabilities() model.Capabilities        { return bridgeTestModelCapabilities() }
func (cancelModel) Capabilities() model.Capabilities        { return bridgeTestModelCapabilities() }
func (*toolThenTextModel) Capabilities() model.Capabilities { return bridgeTestModelCapabilities() }
func (*runCommandThenTextModel) Capabilities() model.Capabilities {
	return bridgeTestModelCapabilities()
}
func (runtimeAgentTestModel) Capabilities() model.Capabilities {
	return bridgeTestModelCapabilities()
}
