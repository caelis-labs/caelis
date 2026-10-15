package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
)

// createSessionWithModel shares the ordinary Session creation/configuration
// owners and one admitted command. It never grants or enrolls another client.
func (b *controlCommandBackend) createSessionWithModel(ctx context.Context, p appserver.Principal, req appserver.CreateSessionRequest) (appserver.CommandResult, error) {
	model, effort, fast := req.Model, req.ReasoningEffort, req.FastMode
	req.Model, req.ReasoningEffort, req.FastMode = "", "", false
	created, err := b.ExecuteControlCommand(ctx, p, appserver.ActionSessionCreate, req)
	if err != nil || model == "" {
		return created, err
	}
	active, err := b.composition.sessions.Session(ctx, session.SessionRef{SessionID: created.SessionID})
	var result appserver.CommandResult
	if err == nil {
		result, err = b.ExecuteControlCommand(ctx, p, appserver.ActionSessionModel, appserver.SessionModelRequest{
			WriteBase: appserver.WriteBase{OperationID: req.OperationID, SessionID: active.SessionID,
				ExpectedRevision: &active.Revision, ExpectedControllerEpoch: active.Controller.EpochID},
			Model: model, ReasoningEffort: effort, FastMode: fast,
		})
	}
	if err != nil {
		// Creation already happened. Preserve its address; a failed configuration
		// cannot be represented as no effect or invite a new creation retry.
		created.Outcome = appserver.OutcomeUnknown
		return created, appserver.NewOutcomeError(appserver.OutcomeUnknown, err)
	}
	return result, nil
}
