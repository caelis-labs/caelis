package appserveradapter

import (
	"context"
	"errors"

	"github.com/google/uuid"

	appserver "github.com/caelis-labs/caelis/control/appserver"
)

// Compact routes manual Session compaction through the same typed Control
// command and Session Runtime selection used by every other AppServer client.
// It reports whether the command committed a checkpoint; false with a nil error
// means the admitted Session had nothing new to compact.
func (a *SessionClientAdapter) Compact(ctx context.Context) (bool, error) {
	if a == nil || a.sessionClient == nil {
		return false, errors.New("app/gatewayapp/controladapter: Session client is unavailable")
	}
	state, err := a.currentClientSessionState(ctx)
	if err != nil {
		return false, err
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
		return false, err
	}
	return result.Revision > revision, nil
}
