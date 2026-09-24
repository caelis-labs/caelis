package gatewayapp

import (
	"context"
	"errors"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/control/appserver"
)

func (s *runtimeComposition) startBackgroundParticipant(ctx context.Context, active session.Session, req appserver.StartParticipantRequest, target placement.Placement) (appserver.CommandResult, error) {
	ctx = session.ContextWithControlMutation(ctx, session.ControlMutationPurposeParticipant)
	snapshot, err := s.engine.StartSubagentWithOptions(ctx, active.SessionRef, req.Handle, req.Input, req.Source, runtime.StartSubagentOptions{
		SpawnID: req.OperationID, Handle: req.Label, ContentParts: req.ContentParts,
		ReturnOnStart: true,
		Target:        delegation.Target{Selector: req.Handle, Placement: target},
	})
	if err != nil {
		return sessionCommandResult(active), classifyControlBackendError(err)
	}
	updated, err := s.sessions.Session(ctx, active.SessionRef)
	if err != nil {
		return appserver.CommandResult{Outcome: appserver.OutcomeCommitted, SessionID: active.SessionID,
			Resource: &appserver.CommandResource{Kind: appserver.CommandResourceParticipantTask, Ref: snapshot.Ref.TaskID}}, err
	}
	out := sessionCommandResult(updated)
	out.Resource = &appserver.CommandResource{Kind: appserver.CommandResourceParticipantTask, Ref: snapshot.Ref.TaskID}
	for _, binding := range updated.Participants {
		if binding.DelegationID == snapshot.Ref.TaskID {
			out.ParticipantID = binding.ID
			break
		}
	}
	return out, nil
}

type backgroundChildApprovalRequester struct{ composition *runtimeComposition }

func (r backgroundChildApprovalRequester) RequestApproval(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
	s := r.composition
	if s == nil || s.currentGateway() == nil || strings.TrimSpace(req.SessionRef.SessionID) == "" {
		return agent.ApprovalResponse{}, errors.New("child approval service is unavailable")
	}
	observer, release := s.controlTurnObserver(req.SessionRef, "")
	defer release()
	return s.currentGateway().RequestChildApproval(ctx, req, observer)
}
