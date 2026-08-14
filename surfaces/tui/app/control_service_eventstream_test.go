package tuiapp

import (
	"errors"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestEventStreamEnvelopeErrorReasonPrefersStructuredRedaction(t *testing.T) {
	t.Parallel()

	env := eventstream.Error(&model.RetryExhaustedError{
		MaxRetries: 5,
		Cause:      errors.New("model: http status 500 body=Internal Server Error"),
	})
	if !strings.Contains(env.Error, "Internal Server Error") {
		t.Fatalf("test setup error text = %q, want raw provider detail", env.Error)
	}
	reason := eventStreamEnvelopeErrorReason(env)
	if reason != "model request failed after 5 retries" {
		t.Fatalf("eventStreamEnvelopeErrorReason() = %q, want redacted retry error", reason)
	}
	if strings.Contains(reason, "Internal Server Error") || strings.Contains(reason, "http status 500") {
		t.Fatalf("failure reason leaked provider detail: %q", reason)
	}
}
