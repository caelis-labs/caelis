// Package controlplane owns Caelis product orchestration around neutral Agent
// SDK controller and participant execution contracts.
package controlplane

import (
	"context"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// ContextRouter applies Caelis shared-ledger routing policy to controller and
// participant endpoints.
type ContextRouter struct {
	sessions session.Reader
}

// NewContextRouter returns the product context router for one session store.
func NewContextRouter(sessions session.Reader) (*ContextRouter, error) {
	if sessions == nil {
		return nil, fmt.Errorf("controlplane: sessions service is required")
	}
	return &ContextRouter{sessions: sessions}, nil
}

// ControllerContext returns the public context offset that one controller has
// not received. Operational Session and controller metadata are deliberately
// excluded from the model-visible transfer.
func (r *ContextRouter) ControllerContext(ctx context.Context, req controller.ControllerContextRequest) (controller.ContextRoute, error) {
	if r == nil || r.sessions == nil {
		return controller.ContextRoute{}, fmt.Errorf("controlplane: context router is unavailable")
	}
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: session.NormalizeSessionRef(req.SessionRef)})
	if err != nil {
		return controller.ContextRoute{}, err
	}
	shared := sharedContextOffsetFromEvents(events, req.SinceSeq, req.ExcludeTurnID)
	return controller.ContextRoute{Context: shared.Transfer, SyncSeq: shared.Checkpoint}, nil
}

// ParticipantContext returns the public context offset that one participant
// has not received.
func (r *ContextRouter) ParticipantContext(ctx context.Context, req controller.ParticipantContextRequest) (controller.ContextRoute, error) {
	if r == nil || r.sessions == nil {
		return controller.ContextRoute{}, fmt.Errorf("controlplane: context router is unavailable")
	}
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: session.NormalizeSessionRef(req.SessionRef)})
	if err != nil {
		return controller.ContextRoute{}, err
	}
	shared := sharedContextOffsetFromEvents(events, req.Binding.ContextSyncSeq, "")
	return controller.ContextRoute{Context: shared.Transfer, SyncSeq: shared.Checkpoint}, nil
}

// Checkpoint returns the latest complete public-dialogue boundary routed by
// this policy, optionally excluding one in-flight Turn.
func (r *ContextRouter) Checkpoint(ctx context.Context, ref session.SessionRef, excludeTurnID string) (uint64, error) {
	if r == nil || r.sessions == nil {
		return 0, fmt.Errorf("controlplane: context router is unavailable")
	}
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: session.NormalizeSessionRef(ref)})
	if err != nil {
		return 0, err
	}
	return sharedContextOffsetFromEvents(events, 0, excludeTurnID).Checkpoint, nil
}

type sharedContextOffset struct {
	Checkpoint uint64
	Transfer   agent.ContextTransfer
}

type sharedTurnBuilder struct {
	endSeq           uint64
	executor         session.ActorRef
	userMessages     []string
	assistantSummary string
}

func sharedContextOffsetFromEvents(events []*session.Event, sinceSeq uint64, excludeTurnID string) sharedContextOffset {
	excludeTurnID = strings.TrimSpace(excludeTurnID)
	latestCompactSeq := latestCompactEventSeq(events)
	out := sharedContextOffset{}
	if latestCompactSeq > 0 {
		out.Checkpoint = latestCompactSeq
		if sinceSeq < latestCompactSeq {
			for i, event := range events {
				if eventSequence(event, i) == latestCompactSeq && compact.IsCompactEvent(event) {
					out.Transfer.Summary = strings.TrimSpace(session.EventText(event))
					break
				}
			}
		}
	}

	builders := make([]*sharedTurnBuilder, 0)
	byTurnID := map[string]*sharedTurnBuilder{}
	var legacyPending *sharedTurnBuilder
	for i, event := range events {
		seq := eventSequence(event, i)
		if event == nil || latestCompactSeq > 0 && seq <= latestCompactSeq || !session.IsCanonicalHistoryEvent(event) {
			continue
		}
		turnID := ""
		if event.Scope != nil {
			turnID = strings.TrimSpace(event.Scope.TurnID)
		}
		if excludeTurnID != "" && turnID == excludeTurnID {
			continue
		}
		text := strings.TrimSpace(session.EventText(event))
		if text == "" {
			continue
		}
		switch session.EventTypeOf(event) {
		case session.EventTypeUser:
			builder := legacyPending
			if turnID != "" {
				builder = byTurnID[turnID]
			}
			if builder == nil {
				builder = &sharedTurnBuilder{}
				builders = append(builders, builder)
				if turnID != "" {
					byTurnID[turnID] = builder
				} else {
					legacyPending = builder
				}
			}
			builder.userMessages = append(builder.userMessages, text)
			if executor := eventExecutor(event); session.ActorRefHasIdentity(executor) {
				builder.executor = executor
			}
		case session.EventTypeAssistant:
			builder := byTurnID[turnID]
			if turnID == "" {
				builder = legacyPending
			}
			if builder == nil || len(builder.userMessages) == 0 {
				continue
			}
			builder.assistantSummary = text
			builder.endSeq = seq
			if executor := scopedEventExecutor(event); session.ActorRefHasIdentity(executor) {
				builder.executor = executor
			} else if !session.ActorRefHasIdentity(builder.executor) {
				builder.executor = eventExecutor(event)
			}
			if turnID != "" {
				delete(byTurnID, turnID)
			} else {
				legacyPending = nil
			}
		}
	}

	for _, builder := range builders {
		if builder == nil || len(builder.userMessages) == 0 || builder.assistantSummary == "" || builder.endSeq == 0 {
			continue
		}
		if builder.endSeq > out.Checkpoint {
			out.Checkpoint = builder.endSeq
		}
		if builder.endSeq <= sinceSeq {
			continue
		}
		executor := session.CloneActorRef(builder.executor)
		if !session.ActorRefHasIdentity(executor) {
			executor = session.ActorRef{Kind: session.ActorKindController, Name: "unknown"}
		}
		out.Transfer.Turns = append(out.Transfer.Turns, agent.ContextTurn{
			Executor:         executor,
			UserMessages:     append([]string(nil), builder.userMessages...),
			AssistantSummary: builder.assistantSummary,
		})
	}
	out.Transfer = agent.CloneContextTransfer(out.Transfer)
	return out
}

func eventExecutor(event *session.Event) session.ActorRef {
	if event == nil {
		return session.ActorRef{}
	}
	if executor := scopedEventExecutor(event); session.ActorRefHasIdentity(executor) {
		return executor
	}
	actor := session.CloneActorRef(event.Actor)
	if actor.Kind == session.ActorKindController || actor.Kind == session.ActorKindParticipant {
		return actor
	}
	if event.Scope != nil {
		if id := strings.TrimSpace(event.Scope.Participant.ID); id != "" {
			return session.ActorRef{Kind: session.ActorKindParticipant, ID: id, Name: id}
		}
		if event.Scope.Controller.Kind != "" {
			return session.ActorRef{
				Kind: session.ActorKindController,
				ID:   strings.TrimSpace(event.Scope.Controller.ID),
				Name: firstNonEmpty(strings.TrimSpace(event.Scope.Controller.ID), string(event.Scope.Controller.Kind)),
			}
		}
	}
	return session.ActorRef{}
}

func scopedEventExecutor(event *session.Event) session.ActorRef {
	if event == nil || event.Scope == nil {
		return session.ActorRef{}
	}
	return session.CloneActorRef(event.Scope.Executor)
}

func eventSequence(event *session.Event, index int) uint64 {
	if event != nil && event.Seq > 0 {
		return event.Seq
	}
	return uint64(index + 1)
}

func latestCompactEventSeq(events []*session.Event) uint64 {
	for i := len(events) - 1; i >= 0; i-- {
		if compact.IsCompactEvent(events[i]) {
			return eventSequence(events[i], i)
		}
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
