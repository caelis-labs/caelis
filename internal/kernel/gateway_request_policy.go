package kernel

import (
	"github.com/caelis-labs/caelis/ports/agent"
)

type defaultRequestPolicy struct{}

func (defaultRequestPolicy) ResolveTurnRequest(req BeginTurnRequest) agent.ModelRequestOptions {
	stream := ClassifySurface(req.Surface) != SurfaceClassBatch
	return agent.ModelRequestOptions{Stream: boolPtr(stream)}
}

func boolPtr(v bool) *bool { return &v }
