//go:build darwin

package gatewayapp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

// TestApplicationWorkspaceWriteFailsClosedOnDarwin proves canonical creation,
// direct native construction and reactivation cannot grant workspace-write on
// a platform where native process-info reads can reveal Host credentials.
func TestApplicationWorkspaceWriteFailsClosedOnDarwin(t *testing.T) {
	storeDir := t.TempDir()
	stack, err := newGatewayAppTestStack(t, Config{
		AppName: "caelis", UserID: "owner", StoreDir: storeDir,
		WorkspaceCWD: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	credential := "app-client-" + strings.Repeat("ca", 32)
	connection, err := stack.Applications().Register(t.Context(), appserver.Principal{ID: "owner"}, application.Registration{
		OperationID: "register-fail-closed", Name: "synthetic", Credential: credential,
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := appserver.Principal{ID: connection.PrincipalID, ApplicationID: connection.ApplicationID, ConnectionID: connection.ConnectionID}
	profile := application.Profile{Version: "v1", Instructions: "synthetic", Model: "unconfigured-model", ToolsVersion: "v1", Execution: "workspace-write"}
	rejected, err := stack.Applications().Create(t.Context(), principal, appserver.CreateApplicationSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "rejected-create"}, Profile: profile,
	})
	if errorcode.CodeOf(err) != errorcode.Unsupported || !strings.Contains(err.Error(), "process-information read isolation") || rejected.Outcome != appserver.OutcomeRejected {
		t.Fatalf("workspace-write creation = %+v, %v, want rejected unsupported platform ceiling", rejected, err)
	}
	id := application.SessionID(connection.Scope, "rejected-create")
	if _, err := stack.Sessions().Session(t.Context(), session.SessionRef{SessionID: id}); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("unsupported profile allocated canonical Session: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(storeDir, "applications")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported profile allocated execution directory: %v", err)
	}
	if _, err := newIsolatedExecutionRuntime("", "", nil, application.Scope{}); errorcode.CodeOf(err) != errorcode.Unsupported {
		t.Fatalf("direct native construction = %v, want unsupported before filesystem or backend work", err)
	}
	if err := validateApplicationExecutionPlatform("tools-only"); err != nil {
		t.Fatalf("tools-only must remain available: %v", err)
	}

	// Simulate a durable Session admitted on another Host. Reopen/activation
	// must reject it without rewriting or deleting its canonical user data.
	persistedID := application.SessionID(connection.Scope, "persisted")
	cwd, err := createApplicationWorkspace(storeDir, persistedID)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := canonicalWorkspaceRef(session.WorkspaceRef{Key: persistedID, CWD: cwd}, session.WorkspaceRef{})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Sessions().StartSession(t.Context(), session.StartSessionRequest{
		AppName: stack.AppName(), UserID: connection.PrincipalID, PreferredSessionID: persistedID,
		Workspace: workspace, Controller: initialKernelControllerBinding("application"),
		Metadata: map[string]any{
			sessionvisibility.MetadataSystemManagedAgent: sessionvisibility.SystemManagedAgentApplication,
			application.StateKey:                         connection.ApplicationID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.Applications().Store().PutBinding(t.Context(), application.Binding{
		Scope: connection.Scope, SessionID: persistedID, Profile: profile, CreationDigest: "persisted-digest",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stack.sessionRuntimes.activateSession(t.Context(), persistedID); errorcode.CodeOf(err) != errorcode.Unsupported {
		t.Fatalf("persisted workspace-write activation = %v, want unsupported", err)
	}
	if _, err := stack.Sessions().Session(t.Context(), active.SessionRef); err != nil {
		t.Fatalf("rejected activation changed canonical Session: %v", err)
	}
}
