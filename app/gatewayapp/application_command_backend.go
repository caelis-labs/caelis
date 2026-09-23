package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/application"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

// applicationSourceContextKey carries Control-admitted provenance only between
// application prompt dispatch and the native Turn resolver. It is not metadata
// supplied by a model, tool, or serialized request body.
type applicationSourceContextKey struct{}

func (b *controlCommandBackend) createApplicationSession(ctx context.Context, principal appserver.Principal, req appserver.CreateApplicationSessionRequest) (appserver.CommandResult, error) {
	store := b.composition.authorities.applications
	scope, err := appserver.ApplicationScope(principal)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	if err := store.CheckActive(ctx, scope); err != nil {
		return appserver.CommandResult{}, err
	}
	if err := application.ValidateProfile(req.Profile); err != nil {
		return appserver.CommandResult{}, err
	}
	if err := validateApplicationExecutionPlatform(req.Profile.Execution); err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	seenTools := make(map[string]bool, len(req.Profile.Tools))
	for _, def := range req.Profile.Tools {
		name := strings.ToLower(def.Name)
		if seenTools[name] {
			return appserver.CommandResult{}, errorcode.New(errorcode.InvalidArgument, "gatewayapp: application callback names must be distinct")
		}
		seenTools[name] = true
		switch name {
		case "runcommand", "task", "readresource", "publishartifact":
			return appserver.CommandResult{}, errorcode.New(errorcode.InvalidArgument, "gatewayapp: application callback collides with a native execution tool")
		}
	}
	// The permanent application anchor, not the expiring shared ledger digest,
	// is the immutable creation identity recovered after an uncertain dispatch.
	operation, err := store.GetOperation(ctx, scope, req.OperationID)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	id := application.SessionID(scope, req.OperationID)
	ref := session.SessionRef{SessionID: id}
	binding := application.Binding{Scope: scope, SessionID: id, Profile: req.Profile, CreationDigest: operation.Digest}
	if existing, err := b.composition.sessions.Session(ctx, ref); err == nil {
		if existing.UserID != scope.PrincipalID || !sessionvisibility.IsApplicationSession(existing) || existing.Metadata[application.StateKey] != scope.ApplicationID || existing.Metadata["application_creation_digest"] != operation.Digest {
			return appserver.CommandResult{}, appserver.NewOutcomeError(appserver.OutcomeConflicted, appserver.ErrOperationConflict)
		}
		if err := store.PutBinding(ctx, binding); err != nil {
			return sessionCommandResult(existing), appserver.NewOutcomeError(appserver.OutcomeUnknown, err)
		}
		return sessionCommandResult(existing), nil
	} else if !errors.Is(err, session.ErrSessionNotFound) {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	// Fail before Session admission when the explicitly pinned model cannot be
	// resolved. Never replace it with the Host's ordinary default profile.
	if _, err := b.composition.lookup.ResolveConfig(req.Profile.Model); err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	cwd, err := createApplicationWorkspace(b.composition.authorities.storeDir, id)
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	workspace, err := canonicalWorkspaceRef(session.WorkspaceRef{Key: id, CWD: cwd}, session.WorkspaceRef{})
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	active, err := b.composition.sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: b.composition.authorities.appName, UserID: scope.PrincipalID,
		PreferredSessionID: id, Workspace: workspace, Controller: initialKernelControllerBinding("application"),
		Metadata: map[string]any{
			sessionvisibility.MetadataSystemManagedAgent: sessionvisibility.SystemManagedAgentApplication,
			application.StateKey:                         scope.ApplicationID, "application_creation_digest": operation.Digest,
		},
	})
	if err != nil {
		return sessionCommandResult(active), classifyControlBackendError(err)
	}
	if err := store.PutBinding(ctx, binding); err != nil {
		return sessionCommandResult(active), appserver.NewOutcomeError(appserver.OutcomeUnknown, fmt.Errorf("gatewayapp: Session created but application binding failed: %w", err))
	}
	return sessionCommandResult(active), nil
}

func createApplicationWorkspace(storeDir, sessionID string) (string, error) {
	base := filepath.Join(storeDir, "applications")
	for _, path := range []string{base, filepath.Join(base, sessionID)} {
		if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("gatewayapp: application workspace path is not a secure directory: %s: %w", path, err)
		}
	}
	return filepath.Join(base, sessionID), nil
}

func (b *controlCommandBackend) admitApplicationPrompt(ctx context.Context, principal appserver.Principal, req appserver.ApplicationPromptRequest) (context.Context, appserver.PromptRequest, error) {
	store := b.composition.authorities.applications
	scope, err := appserver.ApplicationScope(principal)
	if err != nil {
		return ctx, appserver.PromptRequest{}, err
	}
	if err := store.CheckActive(ctx, scope); err != nil {
		return ctx, appserver.PromptRequest{}, err
	}
	binding, err := store.GetBinding(ctx, scope, req.SessionID)
	if err != nil {
		return ctx, appserver.PromptRequest{}, err
	}
	if binding.Archived {
		return ctx, appserver.PromptRequest{}, appserver.ErrSessionClosed
	}
	source := application.Source{Kind: strings.TrimSpace(req.SourceKind), OperationID: req.OperationID}
	if err := application.ValidateSource(source); err != nil {
		return ctx, appserver.PromptRequest{}, errorcode.Wrap(errorcode.InvalidArgument, "gatewayapp: invalid application prompt source", err)
	}
	// The concrete application source is not caller-selected SDK metadata. It
	// remains bound to this accepted operation throughout model tool dispatch.
	return context.WithValue(ctx, applicationSourceContextKey{}, source), req.PromptRequest, nil
}
