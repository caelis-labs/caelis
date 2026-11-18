package appserveradapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/google/uuid"
)

// DeleteModels disconnects the selected profile IDs in one Host command. It
// returns all confirmed IDs for a committed receipt, including with a warning.
func (a *SessionClientAdapter) DeleteModels(ctx context.Context, profileIDs []string) ([]string, error) {
	profileIDs = modelconfig.DedupeNonEmptyStrings(profileIDs)
	if len(profileIDs) == 0 {
		return nil, nil
	}
	if a == nil || a.configClient == nil || a.statusClient == nil {
		return nil, errors.New("app/gatewayapp/controladapter: configuration client is unavailable")
	}
	before, err := a.addressedStatus(ctx, "", false)
	if err != nil {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: read Host configuration revision: %w", err)
	}
	result, err := a.configClient.DeleteModels(ctx, appserver.DeleteModelsRequest{
		WriteBase: appserver.WriteBase{
			OperationID:      "models-delete-" + uuid.NewString(),
			ExpectedRevision: &before.Configuration.Revision,
		},
		ProfileIDs: profileIDs,
	})
	if result.Outcome == appserver.OutcomeCommitted && err == nil && result.Detail == "" {
		return profileIDs, nil
	}
	if err == nil {
		err = fmt.Errorf("model deletion outcome is %q: %s", result.Outcome, result.Detail)
	}
	if result.Outcome != appserver.OutcomeCommitted {
		profileIDs = nil
	}
	return profileIDs, &appserver.CommandReceiptError{Receipt: result, Err: err}
}
