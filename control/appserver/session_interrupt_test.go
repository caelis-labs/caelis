package appserver

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

func TestSessionInterruptRequiresObservedCASBeforeDispatch(t *testing.T) {
	backend := &recordingCommandBackend{}
	service, err := NewCommandService(CommandServiceConfig{Authorizer: allowAuthorizer{}, Operations: NewMemoryOperationStore(), Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.InterruptSession(t.Context(), Principal{ID: "owner"}, SessionInterruptRequest{WriteBase: WriteBase{SessionID: "s", OperationID: "interrupt-1", ExpectedControllerEpoch: "e1"}})
	if errorcode.CodeOf(err) != errorcode.InvalidArgument || result.Outcome != OutcomeRejected || backend.calls != 0 {
		t.Fatalf("missing revision reached backend: result=%#v err=%v calls=%d", result, err, backend.calls)
	}
}

func TestSessionInterruptAllowsNativeSessionWithoutControllerEpoch(t *testing.T) {
	revision := uint64(7)
	backend := &recordingCommandBackend{}
	service, err := NewCommandService(CommandServiceConfig{Authorizer: allowAuthorizer{}, Operations: NewMemoryOperationStore(), Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.InterruptSession(t.Context(), Principal{ID: "owner"}, SessionInterruptRequest{WriteBase: WriteBase{SessionID: "native", OperationID: "interrupt-native", ExpectedRevision: &revision}})
	if err != nil || result.Outcome != OutcomeCommitted || backend.calls != 1 {
		t.Fatalf("native Session interrupt = %#v, %v calls=%d", result, err, backend.calls)
	}
}
