package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
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

func loadDurableWorkspaceIdentities(ctx context.Context, store session.Lifecycle, defaultWorkspace session.WorkspaceRef) (map[string]string, error) {
	directories := make(map[string]string)
	err := visitStoredWorkspaces(ctx, store, func(_ string, workspace session.WorkspaceRef) {
		if prior, ok := directories[workspace.Key]; ok && prior != workspace.CWD {
			// The empty value blocks use of this key until explicit repair.
			directories[workspace.Key] = ""
		} else {
			directories[workspace.Key] = workspace.CWD
		}
	})
	if err != nil {
		return nil, err
	}
	if _, known := directories[defaultWorkspace.Key]; !known {
		directories[defaultWorkspace.Key] = defaultWorkspace.CWD
	}
	return directories, nil
}
