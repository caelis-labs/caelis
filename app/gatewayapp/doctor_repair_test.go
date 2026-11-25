package gatewayapp

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func TestDoctorRepairsConflictingDurableWorkspaceIdentities(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(storeDir, "sessions")})
	workspaceA := filepath.Join(t.TempDir(), "a", "work")
	workspaceB := filepath.Join(t.TempDir(), "b", "work")
	for index, workspace := range []string{workspaceA, workspaceB} {
		if _, err := store.StartSession(ctx, session.StartSessionRequest{
			AppName: "caelis", UserID: "local-user", PreferredSessionID: "legacy-" + string(rune('a'+index)),
			Workspace: session.WorkspaceRef{Key: "work", CWD: workspace},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "local-user", PreferredSessionID: "explicit",
		Workspace: session.WorkspaceRef{Key: "kept-alias", CWD: workspaceA},
	}); err != nil {
		t.Fatal(err)
	}

	// A replacement key occupied by another directory extends the repair.
	workspaceC := t.TempDir()
	if _, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "local-user", PreferredSessionID: "displaced",
		Workspace: session.WorkspaceRef{Key: workspaceA, CWD: workspaceC},
	}); err != nil {
		t.Fatal(err)
	}

	config := Config{StoreDir: storeDir, WorkspaceCWD: t.TempDir()}
	stack, err := newGatewayAppTestStack(t, config)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := startGatewayAppTestSession(ctx, stack, "fresh")
	if err != nil || fresh.WorkspaceKey != fresh.CWD {
		t.Fatalf("unrelated Session address = %q, %q, %v", fresh.WorkspaceKey, fresh.CWD, err)
	}
	if err := stack.sessionRuntimes.validateWorkspaceIdentity(session.WorkspaceRef{Key: "work", CWD: workspaceA}); !errorcode.Is(err, errorcode.FailedPrecondition) {
		t.Fatalf("ambiguous workspace: %v", err)
	}
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}

	plans, err := InspectDoctorRepairs(ctx, storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].Code != DoctorWorkspaceIdentityConflict || plans[0].ConflictingWorkspaceKeys != 1 || plans[0].AffectedSessions != 3 {
		t.Fatalf("doctor plans = %#v", plans)
	}
	report, err := RepairDoctorIssue(ctx, storeDir, plans[0].Code)
	if err != nil {
		t.Fatal(err)
	}
	if report.RepairedSessions != 3 || report.AffectedSessions != 3 {
		t.Fatalf("doctor report = %#v", report)
	}
	for id, key := range map[string]string{"legacy-a": workspaceA, "legacy-b": workspaceB, "displaced": workspaceC} {
		if _, err := store.Session(ctx, session.SessionRef{SessionID: id, WorkspaceKey: key}); err != nil {
			t.Fatalf("repaired Session %q: %v", id, err)
		}
	}
	explicit, err := store.Session(ctx, session.SessionRef{SessionID: "explicit", WorkspaceKey: "kept-alias"})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.WorkspaceKey != "kept-alias" {
		t.Fatalf("non-conflicting explicit alias changed to %q", explicit.WorkspaceKey)
	}
	if _, err := store.Session(ctx, session.SessionRef{SessionID: "legacy-a", WorkspaceKey: "work"}); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("legacy workspace ref error = %v, want not found", err)
	}
}
