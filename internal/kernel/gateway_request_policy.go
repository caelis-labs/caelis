package kernel

import (
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
)

type defaultRequestPolicy struct{}

func (defaultRequestPolicy) ResolveTurnRequest(req BeginTurnRequest) agent.ModelRequestOptions {
	// Headless renders only the completed projection, but it still drains the
	// live turn stream. Keep its model transport streaming so providers that
	// require streaming for long-running operations behave the same as in TUI.
	normalizedSurface := strings.ToLower(strings.TrimSpace(req.Surface))
	stream := strings.HasPrefix(normalizedSurface, "headless") || ClassifySurface(req.Surface) != SurfaceClassBatch
	return agent.ModelRequestOptions{Stream: boolPtr(stream)}
}

func boolPtr(v bool) *bool { return &v }
