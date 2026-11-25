package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
)

func TestSessionListIncludesRecentCWDSpellings(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stack, err := newTestLocalStack(Config{
		StoreDir: t.TempDir(), WorkspaceKey: "workspace", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	client := newWorkspaceRuntimeHTTPClient(t, stack, "local-user")
	createWorkspaceRuntimeTestSession(t, client, "create-old", "older-session", "workspace", workspace)
	// The Store can contain non-canonical CWD spellings. The same directory
	// filter used by /resume must include them before applying its page limit.
	if _, err := stack.Sessions().StartSession(t.Context(), session.StartSessionRequest{
		AppName: stack.AppName(), UserID: stack.UserID(),
		Workspace: session.WorkspaceRef{
			Key: "persisted-workspace", CWD: workspace + string(filepath.Separator) + ".",
		},
		PreferredSessionID: "recent-session",
	}); err != nil {
		t.Fatal(err)
	}
	listed, err := client.ListSessions(t.Context(), appserver.ListSessionsRequest{CWD: workspace, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].SessionID != "recent-session" || listed.NextCursor == "" {
		t.Fatalf("recent directory page = %#v, want recent-session followed by older-session", listed)
	}
	if _, loaded := stack.sessionRuntimes.loaded("recent-session"); loaded {
		t.Fatal("listing activated recent-session")
	}
	// An archived workspace may no longer exist on the Host filesystem.
	offline := filepath.Join(t.TempDir(), "offline")
	if _, err := stack.Sessions().StartSession(t.Context(), session.StartSessionRequest{
		AppName: stack.AppName(), UserID: stack.UserID(), PreferredSessionID: "offline-session",
		Workspace: session.WorkspaceRef{Key: "archived", CWD: offline},
	}); err != nil {
		t.Fatal(err)
	}
	listed, err = client.ListSessions(t.Context(), appserver.ListSessionsRequest{WorkspaceKey: "archived", CWD: offline})
	if err != nil || len(listed.Sessions) != 1 || listed.Sessions[0].SessionID != "offline-session" {
		t.Fatalf("offline workspace directory = %#v, %v", listed, err)
	}
}

func TestSessionListDoesNotReadColdHistoryOrActivateRuntime(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stack, err := newTestLocalStack(Config{
		StoreDir: storeDir, WorkspaceKey: "workspace", WorkspaceCWD: workspace,
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
		documents, err := filepath.Glob(filepath.Join(storeDir, "sessions", "*", "*", "*", "*", "rollout-*-"+id+".json"))
		if err != nil || len(documents) != 1 {
			t.Fatalf("fixture document for %q = %v, %v", id, documents, err)
		}
		path := strings.TrimSuffix(documents[0], ".json") + ".events.jsonl"
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
