package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/control/appserver"
)

// createWorkerSession grants one deterministic address, then uses the ordinary
// Session creation/activation path. Worker configuration is entirely Host-owned.
func (b *controlCommandBackend) createWorkerSession(ctx context.Context, p appserver.Principal, req appserver.CreateWorkerRequest) (appserver.CommandResult, error) {
	scope, err := appserver.ApplicationScope(p)
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	w, err := b.composition.authorities.applications.PutWorker(ctx, scope, req.OperationID)
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	created, err := b.ExecuteControlCommand(ctx, p, appserver.ActionSessionCreate, appserver.CreateSessionRequest{
		WriteBase: req.WriteBase, PreferredSessionID: w.SessionID, CWD: req.CWD, Title: req.Title,
	})
	if err != nil || req.Model == "" {
		return created, err
	}
	result, err := b.ExecuteControlCommand(ctx, p, appserver.ActionSessionModel, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{OperationID: req.OperationID, SessionID: created.SessionID, ExpectedRevision: &created.Revision},
		Model:     req.Model, ReasoningEffort: req.ReasoningEffort, FastMode: req.FastMode,
	})
	if err != nil {
		// Creation already happened. Preserve its address; a failed configuration
		// cannot be represented as no effect or invite a new creation retry.
		created.Outcome = appserver.OutcomeUnknown
		return created, appserver.NewOutcomeError(appserver.OutcomeUnknown, err)
	}
	return result, nil
}
