package errorcode_test

import (
	"context"
	"fmt"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestCodeOfAcrossSupportedSDKContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		code errorcode.Code
	}{
		{name: "session not found", err: session.ErrSessionNotFound, code: errorcode.NotFound},
		{name: "session revision", err: &session.RevisionConflictError{}, code: errorcode.Conflict},
		{name: "run conflict", err: &agent.RunConflictError{}, code: errorcode.Conflict},
		{name: "session committed", err: &session.CommittedError{}, code: errorcode.UnknownOutcome},
		{name: "model overflow", err: &model.ContextOverflowError{}, code: errorcode.ResourceExhausted},
		{name: "model capability", err: &model.CapabilityError{}, code: errorcode.Unsupported},
		{name: "sandbox capability", err: &sandbox.CapabilityError{}, code: errorcode.Unsupported},
		{name: "policy decision", err: &policy.DecisionError{}, code: errorcode.FailedPrecondition},
		{name: "task revision", err: &task.RevisionConflictError{}, code: errorcode.Conflict},
		{name: "tool error", err: tool.NewError(tool.ErrorCodePermissionDenied, "denied"), code: errorcode.PermissionDenied},
		{name: "cancelled", err: context.Canceled, code: errorcode.Cancelled},
		{name: "wrapped", err: fmt.Errorf("adapter: %w", errorcode.New(errorcode.Unavailable, "offline")), code: errorcode.Unavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := errorcode.CodeOf(tt.err); got != tt.code {
				t.Fatalf("CodeOf(%T) = %q, want %q", tt.err, got, tt.code)
			}
		})
	}
}

func TestUncodedMessageDoesNotBecomeControlFlow(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("permission denied and overloaded")
	if got := errorcode.CodeOf(err); got != errorcode.Unknown {
		t.Fatalf("CodeOf(message-only error) = %q, want unknown", got)
	}
}
