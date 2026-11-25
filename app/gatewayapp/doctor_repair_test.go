package gatewayapp

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/internal/hostownership"
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
	if _, err := RepairDoctorIssue(ctx, storeDir, plans[0].Code, nil); err == nil {
		t.Fatal("repaired an unowned Store")
	}
	ownership, err := hostownership.Acquire(ctx, storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer ownership.Close()
	report, err := RepairDoctorIssue(ctx, storeDir, plans[0].Code, ownership)
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

func TestInterruptedDoctorRepairIsolatesOnlyPendingSessions(t *testing.T) {
	ctx := t.Context()
	storeDir := t.TempDir()
	store := doctorSessionStore(storeDir)
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"legacy-a", "legacy-b"} {
		cwd, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		active, err := store.StartSession(ctx, session.StartSessionRequest{
			AppName: "caelis", UserID: "local-user", PreferredSessionID: id,
			Workspace: session.WorkspaceRef{Key: workspace, CWD: cwd},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := sessionfile.NewTaskStore(store).Upsert(ctx, &task.Entry{
			TaskID: id, Session: active.SessionRef, Kind: task.KindCommand, State: task.StateCompleted,
		}); err != nil {
			t.Fatal(err)
		}
	}
	ownership, err := hostownership.Acquire(ctx, storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer ownership.Close()
	db, err := sql.Open("sqlite", filepath.Join(storeDir, "sessions", ".sessions.index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER stop_task_rekey BEFORE UPDATE OF workspace_key ON tasks
		BEGIN SELECT RAISE(ABORT, 'interrupted Task rekey'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := RepairDoctorIssue(ctx, storeDir, DoctorWorkspaceIdentityConflict, ownership); err == nil {
		t.Fatal("expected interrupted doctor repair")
	}
	moved, err := store.Session(ctx, session.SessionRef{SessionID: "legacy-a"})
	if err != nil || moved.WorkspaceKey != moved.CWD {
		t.Fatalf("Session key after interrupted repair = %q, %v", moved.WorkspaceKey, err)
	}
	tasks := sessionfile.NewTaskStore(store)
	stale, err := tasks.Get(ctx, "legacy-a")
	if err != nil || stale.Session.WorkspaceKey != workspace {
		t.Fatalf("Task after interrupted repair = %#v, %v", stale, err)
	}
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: storeDir, HostOwnership: ownership, WorkspaceCWD: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.WaitApprovalRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	fresh, err := startGatewayAppTestSession(ctx, stack, "fresh")
	if err != nil || fresh.WorkspaceKey != workspace {
		t.Fatalf("fresh Session = %#v, %v", fresh, err)
	}
	for _, id := range []string{"legacy-a", "legacy-b"} {
		for _, use := range []runtimeUseKind{runtimeUseObservation, runtimeUseActivation} {
			if _, _, _, err := stack.sessionRuntimes.acquireControlRuntime(ctx, id, use); !errorcode.Is(err, errorcode.FailedPrecondition) {
				t.Fatalf("pending Session %s, runtime use %v: %v", id, use, err)
			}
		}
	}
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER stop_task_rekey`); err != nil {
		t.Fatal(err)
	}
	report, err := RepairDoctorIssue(ctx, storeDir, DoctorWorkspaceIdentityConflict, ownership)
	if err != nil || report.RepairedTasks != 2 {
		t.Fatalf("resumed doctor = %#v, %v", report, err)
	}
	if entries, err := tasks.ListSession(ctx, moved.SessionRef); err != nil || len(entries) != 1 || entries[0].TaskID != "legacy-a" {
		t.Fatalf("repaired Task scope = %#v, %v", entries, err)
	}
}
