package gatewayapp

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestNilStackRuntimeProjectionPreservesUnavailableResults(t *testing.T) {
	t.Parallel()

	var stack *Stack
	if _, err := stack.Status().Doctor(context.Background(), DoctorRequest{}); err == nil || !strings.Contains(err.Error(), "stack is unavailable") {
		t.Fatalf("Doctor() error = %v, want unavailable Stack", err)
	}
	if status, found, err := stack.Agents().ControllerStatus(context.Background(), session.SessionRef{}); err != nil || found {
		t.Fatalf("ACPControllerStatus() = %#v, %v, %v, want zero, false, nil", status, found, err)
	}
	if _, err := stack.Models().ListChoices(context.Background(), session.SessionRef{}); err == nil || !strings.Contains(err.Error(), "stack is unavailable") {
		t.Fatalf("ListModelChoices() error = %v, want unavailable Stack", err)
	}
	if turns := stack.ControlKernelReads().TurnState(); turns != nil {
		t.Fatalf("ControlKernelReads().TurnState() = %#v, want nil", turns)
	}
}
