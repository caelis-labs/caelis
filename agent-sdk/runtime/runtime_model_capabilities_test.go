package runtime

import "github.com/caelis-labs/caelis/agent-sdk/model"

// Runtime fixtures declare the complete fake-provider surface they implement.
// Individual capability rejection is covered with the undeclared model in
// capabilities_test.go.
func runtimeTestModelCapabilities() model.Capabilities {
	return model.Capabilities{
		ToolCalls:             true,
		StructuredOutput:      true,
		Streaming:             true,
		ParallelToolCalls:     true,
		ReasoningContinuation: true,
		HostedTools:           true,
	}
}

func (*gatedStreamingModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}

func (*steerRuntimeModel) Capabilities() model.Capabilities    { return runtimeTestModelCapabilities() }
func (*historyReplayModel) Capabilities() model.Capabilities   { return runtimeTestModelCapabilities() }
func (*toolLoopRuntimeModel) Capabilities() model.Capabilities { return runtimeTestModelCapabilities() }
func (*planLoopRuntimeModel) Capabilities() model.Capabilities { return runtimeTestModelCapabilities() }
func (*contextProbeModel) Capabilities() model.Capabilities    { return runtimeTestModelCapabilities() }
func (*modelCheckpointProbe) Capabilities() model.Capabilities { return runtimeTestModelCapabilities() }
func (*overflowRecoveryModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (*denyWriteRuntimeModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (*writePathRuntimeModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (*denyCommandRuntimeModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (*approveEscalatedCommandRuntimeModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (*commandTaskLoopRuntimeModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (*spawnTaskLoopRuntimeModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (*spawnApprovalTaskLoopRuntimeModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (*spawnProbeTaskLoopRuntimeModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (*stepWatermarkModel) Capabilities() model.Capabilities { return runtimeTestModelCapabilities() }
func (*repeatedWatermarkModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (*retryExhaustedHighWaterModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (*compactDeadlineModel) Capabilities() model.Capabilities { return runtimeTestModelCapabilities() }
func (*toolListProbeModel) Capabilities() model.Capabilities   { return runtimeTestModelCapabilities() }
func (searchCapableGateModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
