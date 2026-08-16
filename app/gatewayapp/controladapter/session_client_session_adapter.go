package controladapter

import (
	"context"
	"errors"

	"github.com/google/uuid"

	appserver "github.com/caelis-labs/caelis/control/appserver"
)

// Compact routes manual Session compaction through the same typed Control
// command and Session Runtime selection used by every other AppServer client.
func (a *SessionClientAdapter) Compact(ctx context.Context) error {
	if a == nil || a.sessionClient == nil {
		return errors.New("app/gatewayapp/controladapter: Session client is unavailable")
	}
	state, err := a.currentClientSessionState(ctx)
	if err != nil {
		return err
	}
	revision := state.Revision
	_, err = a.sessionClient.CompactSession(ctx, appserver.CompactSessionRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             "compact-" + uuid.NewString(),
			SessionID:               state.SessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: state.Controller.EpochID,
		},
	})
	return err
}
