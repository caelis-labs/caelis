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
	// Persist resolved paths, not mutable /var or symlink aliases. The
	// operation anchor still identifies the original authenticated request.
	if req.Profile.Workspace.CWD != "" {
		resolved, err := filepath.EvalSymlinks(req.Profile.Workspace.CWD)
		if err != nil {
			return appserver.CommandResult{}, classifyControlPreDispatchError(err)
		}
		req.Profile.Workspace.CWD = resolved
	}
	for i := range req.Profile.Workspace.Access {
		resolved, err := filepath.EvalSymlinks(req.Profile.Workspace.Access[i].Path)
		if err != nil {
			return appserver.CommandResult{}, classifyControlPreDispatchError(err)
		}
		req.Profile.Workspace.Access[i].Path = resolved
	}
	if err := validateApplicationExecutionPlatform(req.Profile.Execution); err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
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
	if err := b.composition.validateApplicationProfile(ctx, req.Profile); err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	workspace, err := applicationSessionWorkspace(req.Profile.Workspace, b.composition.authorities.storeDir, id)
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

// applicationSessionWorkspace pins the authenticated application's working
// directory in the canonical Session. Profile workspace selection does not load
// CWD instructions or any ambient skill, MCP or Memory configuration.
func applicationSessionWorkspace(profile application.Workspace, storeDir, sessionID string) (session.WorkspaceRef, error) {
	cwd := profile.CWD
	if cwd == "" {
		var err error
		cwd, err = createApplicationWorkspace(storeDir, sessionID)
		if err != nil {
			return session.WorkspaceRef{}, err
		}
	}
	workspace, err := canonicalWorkspaceRef(session.WorkspaceRef{Key: sessionID, CWD: cwd}, session.WorkspaceRef{})
	if err != nil {
		return session.WorkspaceRef{}, err
	}
	if _, _, err := applicationWorkspaceAccess(profile.Access, workspace.CWD); err != nil {
		return session.WorkspaceRef{}, err
	}
	return workspace, nil
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
	if source.Kind == "authorized_background" {
		source, err = store.AdmitBackgroundSource(ctx, scope, req.SessionID, req.GrantID, req.OperationID)
		if err != nil {
			return ctx, appserver.PromptRequest{}, err
		}
	}
	if err := application.ValidateSource(source); err != nil {
		return ctx, appserver.PromptRequest{}, errorcode.Wrap(errorcode.InvalidArgument, "gatewayapp: invalid application prompt source", err)
	}
	// The concrete application source is not caller-selected SDK metadata. It
	// remains bound to this accepted operation throughout model tool dispatch.
	return context.WithValue(ctx, applicationSourceContextKey{}, source), req.PromptRequest, nil
}
