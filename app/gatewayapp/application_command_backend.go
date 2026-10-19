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
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/internal/workspaceidentity"
)

// applicationSourceContextKey carries Control-admitted provenance only between
// application prompt dispatch and the native Turn resolver. It is not metadata
// supplied by a model, tool, or serialized request body.
type applicationSourceContextKey struct{}

func (b *controlCommandBackend) createProfileSession(ctx context.Context, principal appserver.Principal, req appserver.CreateSessionRequest) (appserver.CommandResult, error) {
	store := b.composition.authorities.sessionProfiles
	profile := *req.Profile
	profile.Workspace.Access = append([]sessionprofile.WorkspaceAccess(nil), profile.Workspace.Access...)
	scope, err := appserver.SessionScope(principal)
	if err != nil {
		return appserver.CommandResult{}, err
	}
	if err := store.CheckActive(ctx, scope); err != nil {
		return appserver.CommandResult{}, err
	}
	if err := sessionprofile.ValidateProfile(profile); err != nil {
		return appserver.CommandResult{}, err
	}
	// Persist resolved paths, not mutable /var or symlink aliases. The
	// operation anchor still identifies the original authenticated request.
	if profile.Workspace.CWD != "" {
		resolved, err := workspaceidentity.CanonicalDirectory(profile.Workspace.CWD)
		if err != nil {
			return appserver.CommandResult{}, classifyControlPreDispatchError(err)
		}
		profile.Workspace.CWD = resolved
	}
	for i := range profile.Workspace.Access {
		resolved, err := workspaceidentity.CanonicalDirectory(profile.Workspace.Access[i].Path)
		if err != nil {
			return appserver.CommandResult{}, classifyControlPreDispatchError(err)
		}
		profile.Workspace.Access[i].Path = resolved
	}
	if err := validateApplicationExecutionPlatform(profile.Execution); err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	// Bind the explicit profile to the immutable admitted command identity.
	intent, admitted := appserver.OperationIntentFromContext(ctx)
	if !admitted || intent.OperationID != req.OperationID {
		return appserver.CommandResult{}, appserver.ErrUnauthorized
	}
	digest := intent.Digest
	id := sessionprofile.SessionID(scope, req.OperationID)
	ref := session.SessionRef{SessionID: id}
	binding := sessionprofile.Binding{Scope: scope, SessionID: id, Profile: profile, CreationDigest: digest}
	if existing, err := b.composition.sessions.Session(ctx, ref); err == nil {
		if existing.UserID != scope.PrincipalID || !sessionvisibility.IsApplicationSession(existing) || existing.Metadata["application_creation_digest"] != digest {
			return appserver.CommandResult{}, appserver.NewOutcomeError(appserver.OutcomeConflicted, appserver.ErrOperationConflict)
		}
		original, err := store.GetBinding(ctx, scope, id)
		if err != nil || original.CreationDigest != digest || original.CreationRevision == nil {
			return sessionCommandResult(existing), appserver.NewOutcomeError(appserver.OutcomeUnknown, errors.New("creation has no complete immutable profile evidence"))
		}
		return appserver.CommandResult{Outcome: appserver.OutcomeCommitted, SessionID: id, Revision: *original.CreationRevision}, nil
	} else if !errors.Is(err, session.ErrSessionNotFound) {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	// Fail before Session admission when the explicitly pinned model cannot be
	// resolved. Never replace it with the Host's ordinary default profile.
	if err := b.composition.validateApplicationProfile(ctx, profile); err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	workspace, err := applicationSessionWorkspace(profile.Workspace, b.composition.authorities.storeDir, id)
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	active, err := b.composition.sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: b.composition.authorities.appName, UserID: scope.PrincipalID,
		PreferredSessionID: id, Title: req.Title, Workspace: workspace, Controller: initialKernelControllerBinding("application"),
		ExecutionConfig: profile.ExecutionConfig,
		Metadata: map[string]any{
			sessionvisibility.MetadataSystemManagedAgent: sessionvisibility.SystemManagedAgentApplication,
			"application_creation_digest":                digest,
		},
	})
	if err != nil {
		return sessionCommandResult(active), classifyControlBackendError(err)
	}
	revision := active.Revision
	binding.CreationRevision = &revision
	if err := store.PutBinding(ctx, binding); err != nil {
		return sessionCommandResult(active), appserver.NewOutcomeError(appserver.OutcomeUnknown, fmt.Errorf("gatewayapp: Session created but application binding failed: %w", err))
	}
	return sessionCommandResult(active), nil
}

// applicationSessionWorkspace pins the authenticated application's working
// directory in the canonical Session. Profile workspace selection does not load
// CWD instructions or any ambient skill or MCP configuration.
func applicationSessionWorkspace(profile sessionprofile.Workspace, storeDir, sessionID string) (session.WorkspaceRef, error) {
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

func (b *controlCommandBackend) admitProfilePrompt(ctx context.Context, principal appserver.Principal, req appserver.PromptRequest) (context.Context, appserver.PromptRequest, error) {
	store := b.composition.authorities.sessionProfiles
	scope, err := appserver.SessionScope(principal)
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
	source := sessionprofile.Source{Kind: strings.TrimSpace(req.SourceKind), OperationID: req.OperationID}
	if source.Kind == "authorized_background" {
		grantSession := req.SessionID
		if binding.Child != nil {
			grantSession = binding.Child.RootSessionID
		}
		source, err = store.AdmitBackgroundSource(ctx, scope, grantSession, req.GrantID, req.OperationID)
		if err != nil {
			return ctx, appserver.PromptRequest{}, err
		}
	}
	if binding.Child != nil {
		// A child channel inherits the parent's source; it cannot relabel
		// background work or non-authorizing evidence as a new user request.
		original := binding.Child.Source
		if source.Kind != original.Kind || source.GrantID != original.GrantID || source.AuthorizedSource != original.AuthorizedSource {
			return ctx, appserver.PromptRequest{}, appserver.ErrUnauthorized
		}
		source = original
	}
	if err := sessionprofile.ValidateSource(source); err != nil {
		return ctx, appserver.PromptRequest{}, errorcode.Wrap(errorcode.InvalidArgument, "gatewayapp: invalid application prompt source", err)
	}
	// The concrete application source is not caller-selected SDK metadata. It
	// remains bound to this accepted operation throughout model tool dispatch.
	return context.WithValue(ctx, applicationSourceContextKey{}, source), req, nil
}

// recoverProfileCreation observes immutable assembly evidence and canonical
// Session identity. It never creates a Session, repairs a binding, or activates
// execution, and never substitutes the Session's mutable latest revision.
func (b *controlCommandBackend) recoverProfileCreation(ctx context.Context, p appserver.Principal, intent appserver.OperationIntent) (appserver.CommandResult, bool, error) {
	if b == nil || b.composition == nil || b.composition.authorities.sessionProfiles == nil {
		return appserver.CommandResult{}, false, nil
	}
	scope, err := appserver.SessionScope(p)
	if err != nil {
		return appserver.CommandResult{}, false, err
	}
	id := sessionprofile.SessionID(scope, intent.OperationID)
	binding, err := b.composition.authorities.sessionProfiles.GetBinding(ctx, scope, id)
	if errors.Is(err, sessionprofile.ErrNotFound) {
		return appserver.CommandResult{}, false, nil
	}
	if err != nil {
		return appserver.CommandResult{}, false, err
	}
	if binding.CreationDigest != intent.Digest || binding.CreationRevision == nil {
		return appserver.CommandResult{}, false, nil
	}
	active, err := b.composition.sessions.Session(ctx, session.SessionRef{SessionID: id})
	if errors.Is(err, session.ErrSessionNotFound) {
		return appserver.CommandResult{}, false, nil
	}
	if err != nil {
		return appserver.CommandResult{}, false, err
	}
	if active.UserID != p.ID || !sessionvisibility.IsApplicationSession(active) || active.Metadata["application_creation_digest"] != intent.Digest || active.Revision < *binding.CreationRevision {
		return appserver.CommandResult{}, false, appserver.ErrOperationConflict
	}
	return appserver.CommandResult{OperationID: intent.OperationID, SessionID: id, Outcome: appserver.OutcomeCommitted, Revision: *binding.CreationRevision}, true, nil
}
