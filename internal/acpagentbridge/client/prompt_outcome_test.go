package client

import (
	"context"
	"errors"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
)

func TestPromptOutcomeUsesStandardRPCSemantics(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		unknown bool
	}{
		{"success", nil, false},
		{"invalid params", acpsdk.NewInvalidParams(nil), false},
		{"unsupported", acpsdk.NewMethodNotFound("session/prompt"), false},
		{"authentication", acpsdk.NewAuthRequired(nil), false},
		{"overload", acpsdk.NewServerOverloaded(nil), false},
		{"internal", acpsdk.NewInternalError(nil), true},
		{"request cancelled is not turn cancelled", acpsdk.NewRequestCancelled(nil), true},
		{"unknown peer error", &acpsdk.RequestError{Code: -32123, Message: "rejected"}, true},
		{"connection closed", acpsdk.ErrConnectionClosed, true},
		{"cancelled observation", context.Canceled, true},
		{"unclassified", errors.New("failed"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := PromptOutcomeUnknown(test.err); got != test.unknown {
				t.Fatalf("PromptOutcomeUnknown(%v) = %v", test.err, got)
			}
		})
	}
}
