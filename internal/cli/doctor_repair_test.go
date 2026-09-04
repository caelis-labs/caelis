package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/app/gatewayapp"
)

func TestRunDoctorStartupRepairsWorkspaceIdentityConflict(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(storeDir, "sessions")})
	for index, cwd := range []string{"/private/tmp/legacy-a/work", "/private/tmp/legacy-b/work"} {
		if _, err := store.StartSession(ctx, session.StartSessionRequest{
			AppName: "caelis", UserID: "local-user", PreferredSessionID: "session-" + string(rune('a'+index)),
			Workspace: session.WorkspaceRef{Key: "work", CWD: cwd},
		}); err != nil {
			t.Fatal(err)
		}
	}
	config := gatewayapp.Config{
		AppName: "caelis", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: t.TempDir(), WorkspaceCWD: t.TempDir(),
	}
	results, err := runDoctorStartupRepairs(ctx, config, productClientOptions{
		Mode: productClientModeManaged, AppName: config.AppName, UserID: config.UserID,
		StoreDir: config.StoreDir, WorkspaceKey: config.WorkspaceKey, WorkspaceCWD: config.WorkspaceCWD,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Code != string(gatewayapp.StartupIssueWorkspaceIdentityConflict) ||
		results[0].Status != "repaired" || results[0].AffectedSessions != 2 || results[0].RepairedSessions != 2 {
		t.Fatalf("doctor repair results = %#v", results)
	}
	plans, err := gatewayapp.InspectDoctorRepairs(ctx, storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 0 {
		t.Fatalf("repairs remain after doctor: %#v", plans)
	}
}
