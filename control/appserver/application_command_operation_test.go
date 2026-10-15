package appserver

import (
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/application"
)

func TestApplicationCommandHasOnePermanentReceipt(t *testing.T) {
	store, path := applicationTestStore(t)
	principal := enrollTestApplication(t, store, "owner", "enroll", "a")
	scope, _ := ApplicationScope(principal)
	request := CreateWorkerRequest{WriteBase: WriteBase{OperationID: "create"}, CWD: t.TempDir()}
	backend := &applicationBoundaryBackend{result: CommandResult{Outcome: OutcomeCommitted, SessionID: application.WorkerSessionID(scope, request.OperationID), Revision: 17}}
	ledger := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
	commands := newTestCommandService(t, allowAuthorizer{}, ledger, backend)
	service, err := NewApplicationService(ApplicationServiceConfig{Store: store, Commands: commands, Sessions: &Client{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CreateWorker(t.Context(), principal, request)
	if err != nil || result.Outcome != OutcomeCommitted {
		t.Fatalf("create: %+v %v", result, err)
	}
	repeated, err := service.CreateWorker(t.Context(), principal, request)
	if err != nil || repeated != result || backend.effects != 1 {
		t.Fatalf("repeat: %+v %v effects=%d", repeated, err, backend.effects)
	}
	var permanent, ordinary int
	if err := ledger.db.QueryRow(`SELECT count(*) FROM app_operations WHERE connection=? AND operation=? AND result IS NOT NULL`, scope.ConnectionID, request.OperationID).Scan(&permanent); err != nil {
		t.Fatal(err)
	}
	if err := ledger.db.QueryRow(`SELECT count(*) FROM control_operations WHERE operation_id=?`, request.OperationID).Scan(&ordinary); err != nil {
		t.Fatal(err)
	}
	if permanent != 1 || ordinary != 0 {
		t.Fatalf("receipt authorities: permanent=%d ordinary=%d", permanent, ordinary)
	}
	changed := request
	changed.Title = "different"
	if _, err := service.CreateWorker(t.Context(), principal, changed); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("changed intent: %v", err)
	}
	if backend.effects != 1 {
		t.Fatalf("changed request dispatched: %d", backend.effects)
	}
}

func TestApplicationAdmittedIntentStillUsesCommandAuthorization(t *testing.T) {
	store, path := applicationTestStore(t)
	principal := enrollTestApplication(t, store, "owner", "enroll", "a")
	request := CreateWorkerRequest{WriteBase: WriteBase{OperationID: "create"}, CWD: t.TempDir()}
	backend := &applicationBoundaryBackend{}
	ledger := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
	commands := newTestCommandService(t, denyAuthorizer{}, ledger, backend)
	service, err := NewApplicationService(ApplicationServiceConfig{Store: store, Commands: commands, Sessions: &Client{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CreateWorker(t.Context(), principal, request)
	if errorcode.CodeOf(err) != errorcode.PermissionDenied || result.ErrorCode != errorcode.PermissionDenied || backend.effects != 0 {
		t.Fatalf("authorization bypassed: %+v %v effects=%d", result, err, backend.effects)
	}
	observed, err := service.Operation(t.Context(), principal, request.OperationID)
	if err != nil || observed.Result == nil || *observed.Result != result {
		t.Fatalf("rejected receipt: %+v %v", observed, err)
	}
}
