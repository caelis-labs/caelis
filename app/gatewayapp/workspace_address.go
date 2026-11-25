package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/workspaceidentity"
)

// WorkspaceReadService is the focused Host-private authority for canonical
// workspace addressing and future-Session discovery reads.
type WorkspaceReadService struct {
	composition *runtimeComposition
	registry    *sessionRuntimeRegistry
}

// WorkspaceReads returns the focused workspace read authority used by
// AppServer status and completion services.
func (s *Stack) WorkspaceReads() WorkspaceReadService {
	if s == nil {
		return WorkspaceReadService{}
	}
	return WorkspaceReadService{composition: &s.composition, registry: s.sessionRuntimes}
}

// Resolve canonicalizes one client workspace address and validates that it
// does not conflict with the Host registry's durable identity map.
func (s WorkspaceReadService) Resolve(requested session.WorkspaceRef) (session.WorkspaceRef, error) {
	if s.composition == nil || s.registry == nil {
		return session.WorkspaceRef{}, sessionRuntimeHostClosingError()
	}
	workspace, err := canonicalWorkspaceRef(requested, s.composition.workspace)
	if err != nil {
		return session.WorkspaceRef{}, err
	}
	if err := s.registry.validateWorkspaceIdentity(workspace); err != nil {
		return session.WorkspaceRef{}, err
	}
	return workspace, nil
}

func (r *sessionRuntimeRegistry) storedSessionWorkspace(active session.Session) (session.WorkspaceRef, error) {
	if _, pending := r.pendingWorkspaceRepairs[active.SessionID]; pending {
		return session.WorkspaceRef{}, errorcode.New(errorcode.FailedPrecondition, fmt.Sprintf("gatewayapp: Session %q has an unfinished workspace repair; run caelis doctor", active.SessionID))
	}
	workspace, err := canonicalSessionWorkspace(active)
	if err != nil {
		return session.WorkspaceRef{}, err
	}
	return workspace, r.validateWorkspaceIdentity(workspace)
}

func storedWorkspace(key, cwd string) (session.WorkspaceRef, bool) {
	workspace := session.WorkspaceRef{Key: strings.TrimSpace(key), CWD: filepath.Clean(strings.TrimSpace(cwd))}
	return workspace, workspace.Key != "" && filepath.IsAbs(workspace.CWD)
}

// visitStoredWorkspaces iterates addressable Sessions without resolving paths
// on disk. A moved directory still owns its durable key.
func visitStoredWorkspaces(ctx context.Context, store session.Lifecycle, visit func(string, session.WorkspaceRef)) error {
	cursor := ""
	for {
		listed, err := store.ListSessions(ctx, session.ListSessionsRequest{Cursor: cursor, Limit: 200})
		if err != nil {
			return fmt.Errorf("gatewayapp: list stored workspaces: %w", err)
		}
		for _, summary := range listed.Sessions {
			if workspace, ok := storedWorkspace(summary.WorkspaceKey, summary.CWD); ok {
				visit(summary.SessionID, workspace)
			}
		}
		next := strings.TrimSpace(listed.NextCursor)
		if next == "" {
			return nil
		}
		if next == cursor {
			return errors.New("gatewayapp: durable Session listing did not advance")
		}
		cursor = next
	}
}

type workspaceBinding struct {
	cwd     string // Empty when multiple historical directories share a key.
	claimed bool   // This Host has bound its default, created a Session, or activated one.
}

func (b workspaceBinding) accepts(workspace session.WorkspaceRef) bool {
	return b.cwd == workspace.CWD || (!b.claimed && workspace.Key == workspaceidentity.KeyForDirectory(workspace.CWD))
}

func loadDurableWorkspaceIdentities(ctx context.Context, store session.Lifecycle, defaultWorkspace session.WorkspaceRef, pendingRepairs map[string]string) (map[string]workspaceBinding, error) {
	directories := make(map[string]workspaceBinding)
	err := visitStoredWorkspaces(ctx, store, func(id string, workspace session.WorkspaceRef) {
		if key, pending := pendingRepairs[id]; pending {
			// Reserve the planned binding while Session and Task keys can differ.
			workspace.Key = key
		}
		if prior, ok := directories[workspace.Key]; ok && prior.cwd != workspace.CWD {
			directories[workspace.Key] = workspaceBinding{}
		} else {
			directories[workspace.Key] = workspaceBinding{cwd: workspace.CWD}
		}
	})
	if err != nil {
		return nil, err
	}
	if binding, known := directories[defaultWorkspace.Key]; !known || binding.accepts(defaultWorkspace) {
		directories[defaultWorkspace.Key] = workspaceBinding{cwd: defaultWorkspace.CWD, claimed: true}
	}
	return directories, nil
}
