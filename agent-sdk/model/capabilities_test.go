package model_test

import (
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestValidateCapabilitiesIsConservativeAndDeterministic(t *testing.T) {
	t.Parallel()

	err := model.ValidateCapabilities("third-party", model.Capabilities{}, model.Capabilities{
		Streaming:        true,
		StructuredOutput: true,
		ToolCalls:        true,
	})
	var capabilityErr *model.CapabilityError
	if !errors.As(err, &capabilityErr) {
		t.Fatalf("error = %v, want *model.CapabilityError", err)
	}
	if got, want := capabilityErr.Capability, model.CapabilityToolCalls; got != want {
		t.Fatalf("capability = %q, want deterministic first %q", got, want)
	}
}

func TestMergeCapabilitiesUnionsRequirements(t *testing.T) {
	t.Parallel()

	got := model.MergeCapabilities(
		model.Capabilities{Streaming: true, HostedTools: true},
		model.Capabilities{StructuredOutput: true, Streaming: true},
	)
	want := model.Capabilities{Streaming: true, StructuredOutput: true, HostedTools: true}
	if got != want {
		t.Fatalf("MergeCapabilities() = %+v, want %+v", got, want)
	}
}
