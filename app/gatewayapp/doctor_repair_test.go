package gatewayapp

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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

	_, err := loadDurableWorkspaceIdentities(ctx, store, session.WorkspaceRef{Key: "default", CWD: t.TempDir()})
	if code, ok := StartupIssueCodeOf(err); err == nil || !ok || code != StartupIssueWorkspaceIdentityConflict || errorcode.CodeOf(err) != errorcode.FailedPrecondition {
		t.Fatalf("startup conflict = %v, code=%q ok=%t", err, code, ok)
	}
	if !strings.Contains(err.Error(), "["+string(StartupIssueWorkspaceIdentityConflict)+"]") {
		t.Fatalf("startup conflict has no public code: %v", err)
	}

	plans, err := InspectDoctorRepairs(ctx, storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].Code != StartupIssueWorkspaceIdentityConflict || plans[0].ConflictingWorkspaceKeys != 1 || plans[0].AffectedSessions != 2 {
		t.Fatalf("doctor plans = %#v", plans)
	}
	report, err := RepairDoctorIssue(ctx, storeDir, plans[0].Code)
	if err != nil {
		t.Fatal(err)
	}
	if report.RepairedSessions != 2 || report.AffectedSessions != 2 {
		t.Fatalf("doctor report = %#v", report)
	}
	if _, err := loadDurableWorkspaceIdentities(ctx, store, session.WorkspaceRef{Key: "default", CWD: t.TempDir()}); err != nil {
		t.Fatalf("startup identity rebuild after doctor repair: %v", err)
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
