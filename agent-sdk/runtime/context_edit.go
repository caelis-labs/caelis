package runtime

import (
	"context"
	"fmt"
	"reflect"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/requestsource"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (r *Runtime) contextEditSource(ctx context.Context, ref session.SessionRef, observed []*session.Event) (hook.ContextSource, error) {
	loaded, err := r.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref})
	if err != nil {
		return hook.ContextSource{}, err
	}
	if !contextSourceEventsEqual(mainInvocationEvents(loaded.Events), observed) {
		return hook.ContextSource{}, agent.ErrModelRequestSnapshotStale
	}
	stamp, err := session.CheckpointSourceStamp(loaded)
	return hook.ContextSource{Stamp: stamp, ThroughID: lastEventID(observed), ThroughSeq: session.LastEventSeq(observed)}, err
}

func (r *Runtime) commitContextEdit(ctx context.Context, ref session.SessionRef, turnID, runID, logical, name string, edit hook.ContextEdit, observed []*session.Event, validate checkpointValidator) ([]*session.Event, error) {
	if edit.Source.Stamp == "" || edit.Source.ThroughID == "" || edit.Source.ThroughSeq == 0 || logical == "" || name == "" {
		return nil, fmt.Errorf("context edit requires exact source and logical identity")
	}
	if (len(edit.Append) > 0) == (edit.ReplacePrefix != nil) {
		return nil, fmt.Errorf("context edit requires exactly one append or replacement")
	}
	loaded, err := r.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref})
	if err != nil {
		return nil, err
	}
	stamp, err := session.CheckpointSourceStamp(loaded)
	if err != nil {
		return nil, err
	}
	if stamp != edit.Source.Stamp || !contextSourceEventsEqual(mainInvocationEvents(loaded.Events), observed) {
		return nil, agent.ErrModelRequestSnapshotStale
	}
	var prefix []*session.Event
	found := false
	for _, e := range observed {
		if e.Seq > edit.Source.ThroughSeq {
			break
		}
		prefix = append(prefix, e)
		if e.Seq == edit.Source.ThroughSeq && e.ID == edit.Source.ThroughID {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("context edit coverage does not name the observed prefix")
	}
	pairs, err := session.PairToolFacts(prefix)
	if err != nil {
		return nil, err
	}
	for _, p := range pairs {
		if p.Result == nil {
			return nil, fmt.Errorf("context edit source splits a tool group")
		}
	}
	if err := session.ValidateContextMessages(edit.ReplacePrefix); err != nil {
		return nil, err
	}
	if edit.ReplacePrefix != nil {
		var sourceGroups [][]model.ToolCall
		for _, e := range prefix {
			if e.Message != nil {
				if calls := e.Message.ToolCalls(); len(calls) > 0 {
					sourceGroups = append(sourceGroups, calls)
				}
			}
		}
		for _, m := range edit.ReplacePrefix {
			calls := m.ToolCalls()
			if len(calls) == 0 {
				continue
			}
			match := -1
			for i, original := range sourceGroups {
				if reflect.DeepEqual(calls, original) {
					match = i
					break
				}
			}
			if match < 0 {
				return nil, fmt.Errorf("context replacement changed or invented an executed tool group")
			}
			sourceGroups = append(sourceGroups[:match], sourceGroups[match+1:]...)
		}
	}
	id := fmt.Sprintf("hook-edit:%s:%s:%s", runID, logical, name)
	var event *session.Event
	if edit.ReplacePrefix != nil {
		_, old, _ := compact.LatestCompactEvent(observed)
		data := session.CheckpointData{ContractVersion: 2, Purpose: session.CheckpointReplacement, SourceStamp: stamp, ReplacementMessages: edit.ReplacePrefix, Revision: old.Revision + 1, SourceEventCount: len(prefix), SummarizedThroughID: edit.Source.ThroughID, SummarizedThroughSeq: edit.Source.ThroughSeq, Generator: "hook/" + name, Trigger: "model_prepare"}
		message := model.NewTextMessage(model.RoleUser, "Context updated by "+name)
		event = &session.Event{Type: session.EventTypeCompact, Visibility: session.VisibilityCanonical, Actor: session.ActorRef{Kind: session.ActorKindSystem, Name: "hook/" + name}, Message: &message, Meta: map[string]any{session.CheckpointMetadataKey: session.CheckpointDataValue(data)}}
	} else {
		event, err = hookContextEvent(edit.Append, "model_prepare/"+name)
		if err != nil {
			return nil, err
		}
		event.ContextEdit = &session.ContextEditProvenance{Version: 1, SourceStamp: stamp, SourceThroughID: edit.Source.ThroughID, SourceThroughSeq: edit.Source.ThroughSeq, Hook: name, LogicalRequestID: logical}
	}
	event.ID = id
	event.IdempotencyKey = id
	if validate != nil {
		ctx = context.WithValue(ctx, checkpointValidationKey{}, validate)
	}
	persisted, err := r.persistContextArtifactUsing(ctx, &loaded.Session, turnID, event, func(current session.LoadedSession) bool {
		currentStamp, err := session.CheckpointSourceStamp(current)
		return err == nil && currentStamp == stamp
	}, nil)
	if err != nil {
		return nil, err
	}
	return []*session.Event{persisted}, nil
}

func contextSourceEventsEqual(a, b []*session.Event) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	x, err := requestsource.Fingerprint(a)
	if err != nil {
		return false
	}
	y, err := requestsource.Fingerprint(b)
	return err == nil && x == y
}
