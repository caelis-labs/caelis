package gatewayapp

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/workspacetrust"
)

func (s *controlCommandBackend) executeWorkspaceTrustCommand(
	ctx context.Context,
	action appserver.Action,
	req appserver.WorkspaceTrustRequest,
) (appserver.CommandResult, error) {
	if action != appserver.ActionWorkspaceTrust {
		return appserver.CommandResult{}, configurationRejectedError(
			errorcode.New(errorcode.InvalidArgument, "gatewayapp: invalid workspace trust command"),
		)
	}
	result, err := s.persistWorkspaceTrust(ctx, req)
	return result, classifyConfigurationPreEffectError(err)
}

func (s *controlCommandBackend) persistWorkspaceTrust(
	ctx context.Context,
	req appserver.WorkspaceTrustRequest,
) (appserver.CommandResult, error) {
	if s == nil || s.composition == nil || s.composition.authorities.store == nil {
		return appserver.CommandResult{}, errors.New("gatewayapp: workspace trust configuration is unavailable")
	}
	workspace, err := canonicalWorkspaceRef(
		session.WorkspaceRef{Key: req.WorkspaceKey, CWD: req.CWD},
		session.WorkspaceRef{},
	)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	if registry := s.runtimeRegistry(); registry != nil {
		if err := registry.validateWorkspaceIdentity(workspace); err != nil {
			return appserver.CommandResult{}, err
		}
	}
	expected := expectedConfigurationRevision(req.ExpectedRevision)
	ctx = contextOrBackground(ctx)
	if err := ctx.Err(); err != nil {
		return appserver.CommandResult{}, err
	}
	doc, err := s.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	if doc.ConfigurationRevision != expected {
		return configurationCommandResult(doc.ConfigurationRevision), &configstore.ConfigurationRevisionConflict{
			Expected: expected,
			Actual:   doc.ConfigurationRevision,
		}
	}
	if workspacetrust.Lookup(doc.WorkspaceTrust, workspace.CWD) == req.TrustLevel {
		return configurationCommandResult(doc.ConfigurationRevision), nil
	}
	next, err := workspacetrust.Set(doc.WorkspaceTrust, workspace.CWD, req.TrustLevel)
	if err != nil {
		return configurationCommandResult(doc.ConfigurationRevision), errorcode.Wrap(
			errorcode.InvalidArgument,
			"gatewayapp: invalid workspace trust decision",
			err,
		)
	}
	doc.WorkspaceTrust = next
	saved, persistErr := s.composition.authorities.store.CompareAndSave(ctx, expected, doc)
	revision := configurationErrorRevision(persistErr, saved.ConfigurationRevision)
	result := configurationCommandResult(revision)
	if persistErr != nil && configstore.WriteCommitted(persistErr) {
		result.Detail = "Workspace trust preference was saved, but durability could not be fully confirmed."
		return result, nil
	}
	return result, persistErr
}
