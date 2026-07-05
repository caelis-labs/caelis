package kernel

import (
	agent "github.com/caelis-labs/caelis/agent-sdk"
)

type defaultRequestPolicy struct{}

func (defaultRequestPolicy) ResolveTurnRequest(req BeginTurnRequest) agent.ModelRequestOptions {
	stream := ClassifySurface(req.Surface) != SurfaceClassBatch
	return agent.ModelRequestOptions{Stream: boolPtr(stream)}
}

func boolPtr(v bool) *bool { return &v }
