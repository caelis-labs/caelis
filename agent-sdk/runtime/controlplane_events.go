package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func participantBinding(activeSession session.Session, participantID string) (session.ParticipantBinding, bool) {
	participantID = strings.TrimSpace(participantID)
	for _, item := range activeSession.Participants {
		if strings.TrimSpace(item.ID) == participantID {
			return session.CloneParticipantBinding(item), true
		}
	}
	return session.ParticipantBinding{}, false
}

func participantBindingLabel(binding session.ParticipantBinding) string {
	return firstNonEmpty(strings.TrimSpace(binding.Label), strings.TrimSpace(binding.AgentName), strings.TrimSpace(binding.ID))
}

func participantLifecycleEvent(activeSession session.Session, binding session.ParticipantBinding, action string, now time.Time) *session.Event {
	text := strings.TrimSpace(action + " participant " + firstNonEmpty(binding.Label, binding.ID))
	return &session.Event{
		Type:       session.EventTypeParticipant,
		Visibility: session.VisibilityCanonical,
		Time:       now,
		Actor:      session.ActorRef{Kind: session.ActorKindSystem, Name: "runtime"},
		Text:       text,
		Protocol:   ptrEventProtocol(session.NewParticipantProtocol(session.ProtocolParticipant{Action: action})),
		Scope: &session.EventScope{
			Source: "control_plane",
			Controller: session.ControllerRef{
				Kind:    activeSession.Controller.Kind,
				ID:      activeSession.Controller.ControllerID,
				EpochID: activeSession.Controller.EpochID,
			},
			Participant: session.ParticipantRef{
				ID:           binding.ID,
				Kind:         binding.Kind,
				Role:         binding.Role,
				DelegationID: binding.DelegationID,
			},
			ACP: session.ACPRef{
				SessionID: strings.TrimSpace(binding.SessionID),
			},
		},
		Meta: map[string]any{
			"participant_id": binding.ID,
			"label":          binding.Label,
			"session_id":     binding.SessionID,
			"controller_ref": binding.ControllerRef,
		},
	}
}

func ptrEventProtocol(protocol session.EventProtocol) *session.EventProtocol {
	out := session.CloneEventProtocol(protocol)
	return &out
}

func isMissingACPControllerRun(err error) bool {
	return errors.Is(err, controller.ErrNotActive)
}

type controllerApprovalRequester struct {
	requester            agent.ApprovalRequester
	sessionRef           session.SessionRef
	session              session.Session
	runID                string
	turnID               string
	participantID        string
	participantKind      string
	participantSessionID string
}

func (r controllerApprovalRequester) RequestControllerApproval(ctx context.Context, req controller.ApprovalRequest) (controller.ApprovalResponse, error) {
	if r.requester == nil {
		return controller.ApprovalResponse{}, nil
	}
	options := make([]session.ProtocolApprovalOption, 0, len(req.Options))
	for _, item := range req.Options {
		options = append(options, session.ProtocolApprovalOption{
			ID:   strings.TrimSpace(item.ID),
			Name: strings.TrimSpace(item.Name),
			Kind: strings.TrimSpace(item.Kind),
		})
	}
	toolName := firstNonEmpty(req.ToolCall.Name, req.ToolCall.Title, "ACP_TOOL")
	rawInput := session.CloneState(req.ToolCall.RawInput)
	var callInput json.RawMessage
	if len(rawInput) > 0 {
		if data, marshalErr := json.Marshal(rawInput); marshalErr == nil {
			callInput = data
		}
	}
	metadata := map[string]any{
		"agent": strings.TrimSpace(req.Agent),
	}
	if strings.TrimSpace(r.participantID) != "" || strings.TrimSpace(r.participantSessionID) != "" {
		metadata["scope"] = "participant"
		metadata["scope_id"] = strings.TrimSpace(r.turnID)
		metadata["participant_id"] = strings.TrimSpace(r.participantID)
		metadata["participant_kind"] = strings.TrimSpace(r.participantKind)
		metadata["participant_session_id"] = strings.TrimSpace(r.participantSessionID)
		metadata["source"] = "acp_participant"
	}
	resp, err := r.requester.RequestApproval(ctx, agent.ApprovalRequest{
		SessionRef: session.NormalizeSessionRef(r.sessionRef),
		Session:    session.CloneSession(r.session),
		RunID:      strings.TrimSpace(r.runID),
		TurnID:     strings.TrimSpace(r.turnID),
		Tool: tool.Definition{
			Name:        toolName,
			Description: firstNonEmpty(req.ToolCall.Title, req.ToolCall.Kind, "ACP controller requested permission"),
		},
		Call: tool.Call{
			ID:    strings.TrimSpace(req.ToolCall.ID),
			Name:  toolName,
			Input: callInput,
		},
		Approval: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{
				ID:       strings.TrimSpace(req.ToolCall.ID),
				Name:     toolName,
				Kind:     strings.TrimSpace(req.ToolCall.Kind),
				Title:    strings.TrimSpace(req.ToolCall.Title),
				Status:   strings.TrimSpace(req.ToolCall.Status),
				RawInput: rawInput,
			},
			Options: options,
		},
		Metadata: metadata,
	})
	if err != nil {
		return controller.ApprovalResponse{}, err
	}
	return controller.ApprovalResponse{
		Outcome:  strings.TrimSpace(resp.Outcome),
		OptionID: strings.TrimSpace(resp.OptionID),
		Approved: resp.Approved,
	}, nil
}
