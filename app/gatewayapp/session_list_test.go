package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
)

func TestSessionListDoesNotReadColdHistoryOrActivateRuntime(t *testing.T) {
	ctx := context.Background()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stack, err := NewLocalStack(Config{
		StoreDir: t.TempDir(), WorkspaceKey: "workspace", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	client := newWorkspaceRuntimeHTTPClient(t, stack, "local-user")
	var ids []string
	for _, id := range []string{"cold-first", "cold-second"} {
		ids = append(ids, createWorkspaceRuntimeTestSession(t, client, "create-"+id, id, "workspace", workspace))
		path, err := stack.composition.sessions.(interface {
			HistoryPath(context.Context, session.SessionRef) (string, error)
		}).HistoryPath(ctx, session.SessionRef{SessionID: id})
		if err != nil {
			t.Fatal(err)
		}
		// A directory read must succeed independently of event-log decoding.
		if err := os.WriteFile(path, []byte("invalid history must only affect explicit resume\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		listed, err := client.ListSessions(ctx, appserver.ListSessionsRequest{CWD: workspace, Limit: 200})
		if err != nil {
			t.Fatal(err)
		}
		if len(listed.Sessions) != len(ids) || len(listed.RunningSessionIDs) != 0 {
			t.Fatalf("cold directory = %#v", listed)
		}
		for _, row := range listed.Sessions {
			if !slices.Contains(ids, row.SessionID) {
				t.Fatalf("unexpected Session %q", row.SessionID)
			}
			if _, loaded := stack.sessionRuntimes.loaded(row.SessionID); loaded {
				t.Fatalf("listing activated Session %q", row.SessionID)
			}
		}
	}
	if _, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: ids[0]}); err == nil {
		t.Fatal("explicit inspection unexpectedly accepted the invalid history fixture")
	}
}
