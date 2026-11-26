package runtime

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// repairToolPairs records interrupted calls under the exclusive Turn admitted
// by the Session service. Repair never dispatches a tool.
func (r *Runtime) repairToolPairs(ctx context.Context, ref session.SessionRef) error {
	for {
		err := r.repairToolPairsAtCut(ctx, ref)
		if !errors.Is(err, session.ErrRevisionConflict) {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}
func (r *Runtime) repairToolPairsAtCut(ctx context.Context, ref session.SessionRef) error {
	loaded, err := r.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		return err
	}
	pairs, err := session.PairToolFacts(loaded.Events)
	if err != nil {
		return err
	}
	var through uint64
	for _, event := range loaded.Events {
		if data, ok := session.CheckpointDataFromEvent(event); ok {
			through = max(through, data.SummarizedThroughSeq)
		}
	}
	var fixes []*session.Event
	for _, pair := range pairs {
		if pair.Result != nil || pair.Call.Seq > 0 && pair.Call.Seq <= through {
			continue
		}
		call := pair.Call
		event := session.CloneEvent(call)
		event.Message = nil
		event.MessageID = ""
		event.Invocation = nil
		event.ID = "interrupted:" + call.Tool.Execution.TurnID + ":" + call.Tool.Execution.ItemID
		event.IdempotencyKey = event.ID
		event.Seq = 0
		event.Time = r.now()
		event.Type = session.EventTypeToolResult
		payload := map[string]any{"code": "execution_interrupted", "message": "Execution was interrupted and the outcome is unconfirmed; the operation may have already taken effect; do not retry blindly."}
		result := tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: mustRepairJSON(payload)}}}
		event.Tool.Result = &result
		event.Tool.Outcome = "unknown_outcome"
		event.Tool.ErrorCode = "execution_interrupted"
		event.Tool.Status = "failed"
		event.Tool.Output = payload
		if session.IsMainInvocationVisibleEvent(call) {
			message := model.Message{Role: model.RoleTool, Parts: []model.Part{{Kind: model.PartKindToolResult, ToolResult: &model.ToolResultPart{ToolUseID: call.Tool.ID, Name: call.Tool.Name, Content: tool.ModelParts(result.Content), IsError: true}}}}
			event.Message = &message
		}
		fixes = append(fixes, event)
	}
	if len(fixes) == 0 {
		return nil
	}
	_, err = r.sessions.AppendEvents(ctx, session.AppendEventsRequest{SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(ctx), ExpectedRevision: &loaded.Session.Revision, Events: fixes})
	if err != nil {
		for _, fact := range fixes {
			confirmed, readErr := r.reconcileEventAppend(ctx, ref, fact)
			if confirmed == nil {
				return errors.Join(err, readErr)
			}
		}
	}
	return nil
}

func mustRepairJSON(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }

func (r *Runtime) appendToolCallBatch(ctx context.Context, ref session.SessionRef, events []*session.Event) ([]*session.Event, error) {
	persisted, err := r.sessions.AppendEvents(ctx, session.AppendEventsRequest{SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(ctx), Events: events})
	if err == nil {
		return persisted, nil
	}
	confirmed := make([]*session.Event, 0, len(events))
	for _, event := range events {
		found, readErr := r.reconcileEventAppend(ctx, ref, event)
		if found == nil {
			return nil, errors.Join(err, readErr)
		}
		confirmed = append(confirmed, found)
	}
	return confirmed, nil
}
