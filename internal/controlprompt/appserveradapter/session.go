package appserveradapter

import (
	"context"
	"errors"

	"github.com/google/uuid"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

// Compact routes manual Session compaction through the same typed Control
// command and Session Runtime selection used by every other AppServer client.
func (a *SessionClientAdapter) Compact(ctx context.Context) (controlprompt.CompactResult, error) {
	if a == nil || a.sessionClient == nil {
		return controlprompt.CompactResult{}, errors.New("app/gatewayapp/controladapter: Session client is unavailable")
	}
	state, err := a.currentClientSessionState(ctx)
	if err != nil {
		return controlprompt.CompactResult{}, err
	}
	revision := state.Revision
	result, err := a.sessionClient.CompactSession(ctx, appserver.CompactSessionRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "compact-" + uuid.NewString(),
			SessionID:               state.SessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: state.Controller.EpochID,
		},
	})
	if err != nil {
		return controlprompt.CompactResult{}, err
	}
	// Keep revision-based feedback for older Hosts and retained receipts until
	// every supported successful CompactSession receipt includes a checkpoint ID.
	return controlprompt.CompactResult{
		SessionID: state.SessionID, CheckpointID: result.CheckpointID,
		Compacted: result.CheckpointID != "" || result.Revision > revision,
	}, nil
}
