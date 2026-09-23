package gatewayapp

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

func TestRetiredProductSessionsPreserveHistoryButNeverActivateOrdinaryRuntime(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	stack, err := newGatewayAppTestStack(t, Config{
		AppName: "caelis", UserID: "owner", StoreDir: t.TempDir(),
		WorkspaceKey: "ordinary", WorkspaceCWD: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"bot", "bot-work"} {
		t.Run(kind, func(t *testing.T) {
			created, err := stack.Sessions().StartSession(ctx, session.StartSessionRequest{
				AppName: "caelis", UserID: "owner", Workspace: session.WorkspaceRef{Key: kind, CWD: workspace},
				Metadata: map[string]any{sessionvisibility.MetadataSystemManagedAgent: kind},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := stack.sessionRuntimes.activateSession(ctx, created.SessionID); err == nil || !strings.Contains(err.Error(), "retired product Session") {
				t.Fatalf("retired Session activation error = %v", err)
			}
			if _, err := stack.Sessions().Session(ctx, created.SessionRef); err != nil {
				t.Fatalf("retired Session data lost: %v", err)
			}
		})
	}
}
