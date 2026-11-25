package toolbinding

import (
	"context"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/plan"
)

type planResultsKey struct{}

// PlanResults carries successful Plan updates from concurrent tool calls to
// the owning Turn, keyed by the same occurrence as the durable tool result.
type PlanResults struct {
	mu      sync.Mutex
	updates map[string]*plan.Args
}

// WithPlanResults scopes updates to one Runtime attempt.
func WithPlanResults(ctx context.Context) (context.Context, *PlanResults) {
	results := new(PlanResults)
	return context.WithValue(ctx, planResultsKey{}, results), results
}

// RecordPlanResult associates a processed update with its tool occurrence.
func RecordPlanResult(ctx context.Context, event *session.EventTool, update *plan.Args) {
	results, _ := ctx.Value(planResultsKey{}).(*PlanResults)
	if results == nil || update == nil || event.Execution.ItemID == "" {
		return
	}
	results.mu.Lock()
	defer results.mu.Unlock()
	if results.updates == nil {
		results.updates = make(map[string]*plan.Args)
	}
	results.updates[event.Execution.ItemID] = update
}

// Take consumes the update associated with a successful tool result, if any.
func (r *PlanResults) Take(event *session.Event) *plan.Args {
	if event.Type != session.EventTypeToolResult || event.Tool == nil ||
		event.Tool.Name != plan.ToolName || event.Tool.Outcome != session.ToolOutcomeSucceeded {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id := event.Tool.Execution.ItemID
	update := r.updates[id]
	delete(r.updates, id)
	return update
}
