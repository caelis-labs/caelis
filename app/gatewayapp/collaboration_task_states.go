package gatewayapp

import (
	"context"
	"slices"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
)

func (b *collaborationBackend) taskStates(ctx context.Context, ref session.SessionRef, taskIDs []string) ([]task.StateSnapshot, error) {
	if reader, ok := b.tasks.(task.StateReader); ok {
		return reader.ReadTaskStates(ctx, ref, taskIDs)
	}
	// Custom task stores keep their existing contract. The Host's file-backed
	// store implements the state-only capability; no second state owner exists.
	entries, err := b.tasks.ListSession(ctx, ref)
	if err != nil {
		return nil, err
	}
	out := make([]task.StateSnapshot, 0, len(entries))
	for _, entry := range entries {
		if len(taskIDs) > 0 && !slices.Contains(taskIDs, entry.TaskID) {
			continue
		}
		steering, _ := entry.Metadata["supports_steering"].(bool)
		out = append(out, task.StateSnapshot{TaskID: entry.TaskID, Revision: entry.Revision, State: entry.State, SupportsSteering: steering})
	}
	return out, nil
}
