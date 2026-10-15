package gatewayapp_test

import (
	"context"
	"fmt"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// Tests consume typed payloads only after the common command receipt proves commit.
func configurationResult(r appserver.CommandResult, err error) (sessionprofile.Configuration, error) {
	if err != nil {
		return sessionprofile.Configuration{}, err
	}
	if r.Outcome != appserver.OutcomeCommitted || r.Configuration == nil {
		return sessionprofile.Configuration{}, fmt.Errorf("configuration receipt incomplete: %s", r.Outcome)
	}
	return *r.Configuration, nil
}
func resourceResult(r appserver.CommandResult, err error) (sessionprofile.Resource, error) {
	if err != nil {
		return sessionprofile.Resource{}, err
	}
	if r.Outcome != appserver.OutcomeCommitted || r.SessionResource == nil {
		return sessionprofile.Resource{}, fmt.Errorf("resource receipt incomplete: %s", r.Outcome)
	}
	return *r.SessionResource, nil
}
func grantResult(r appserver.CommandResult, err error) (sessionprofile.BackgroundGrant, error) {
	if err != nil {
		return sessionprofile.BackgroundGrant{}, err
	}
	if r.Outcome != appserver.OutcomeCommitted || r.BackgroundGrant == nil {
		return sessionprofile.BackgroundGrant{}, fmt.Errorf("grant receipt incomplete: %s", r.Outcome)
	}
	return *r.BackgroundGrant, nil
}
func configurationCommand(session string, r sessionprofile.UpdateConfigurationRequest) appserver.UpdateSessionConfigurationRequest {
	return appserver.UpdateSessionConfigurationRequest{WriteBase: appserver.WriteBase{OperationID: r.OperationID, SessionID: session}, ExpectedConfigurationRevision: r.ExpectedConfigurationRevision, Patch: r.Patch}
}
func backgroundGrantCommand(session string, r sessionprofile.BackgroundGrantRequest) appserver.CreateSessionBackgroundGrantRequest {
	return appserver.CreateSessionBackgroundGrantRequest{WriteBase: appserver.WriteBase{OperationID: r.OperationID, SessionID: session}, Source: r.Source, AuthorizationOperationID: r.AuthorizationOperationID}
}
func revokeGrantCommand(session, id string) appserver.RevokeSessionBackgroundGrantRequest {
	return appserver.RevokeSessionBackgroundGrantRequest{WriteBase: appserver.WriteBase{OperationID: "revoke-" + id, SessionID: session}, GrantID: id}
}

func closeProfileTestSession(ctx context.Context, c *httpclient.Client, id string) error {
	state, err := c.InspectSession(ctx, appserver.StateRequest{SessionID: id})
	if err != nil {
		return err
	}
	r, err := c.CloseSession(ctx, appserver.CloseSessionRequest{WriteBase: appserver.WriteBase{OperationID: "close-" + id, SessionID: id, ExpectedRevision: &state.Revision, ExpectedControllerEpoch: state.Controller.EpochID}})
	if err != nil {
		return err
	}
	if r.Outcome != appserver.OutcomeCommitted {
		return fmt.Errorf("Session close incomplete: %s", r.Outcome)
	}
	return nil
}
