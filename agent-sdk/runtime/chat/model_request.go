package chat

import (
	agent "github.com/caelis-labs/caelis/agent-sdk"
)

func (a *Agent) refreshModelRequest(ctx agent.Context) error {
	if a.resolveModelRequest == nil {
		return nil
	}
	// The previous response's tools have finished before the next step begins.
	if a.releaseModelRequest != nil {
		a.releaseModelRequest()
		a.releaseModelRequest = nil
	}
	snapshot, err := a.resolveModelRequest(ctx)
	if err != nil {
		return err
	}
	snapshot = agent.CloneModelRequestSnapshot(snapshot)
	next, err := NewWithTools(a.name, snapshot.Model, snapshot.Tools, "")
	if err != nil {
		if snapshot.Release != nil {
			snapshot.Release()
		}
		return err
	}
	next.deferredTools = snapshot.DeferredTools
	next.instructions, next.reasoning, next.request = snapshot.Instructions, snapshot.Reasoning, snapshot.Request
	next.admitModelRequest = snapshot.Admit
	next.releaseModelRequest = snapshot.Release
	next.resolveModelRequest = a.resolveModelRequest
	next.toolResultArtifacts = a.toolResultArtifacts
	next.discoveries = a.discoveries
	*a = *next
	return nil
}
