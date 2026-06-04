package acputil

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/protocol/acp/client"
)

func TestToolCallNameInfersSemanticExecuteName(t *testing.T) {
	t.Parallel()

	got := ToolCallName(client.ToolCallUpdate{
		Kind:     stringPtr("execute"),
		Title:    stringPtr("Run command"),
		RawInput: map[string]any{"command": "pwd"},
	})

	if got != "RUN_COMMAND" {
		t.Fatalf("ToolCallName() = %q, want RUN_COMMAND", got)
	}
}

func TestToolCallNamePrefersKindOverCommandShapedInput(t *testing.T) {
	t.Parallel()

	got := ToolCallName(client.ToolCallUpdate{
		Kind:     stringPtr("read"),
		Title:    stringPtr("Read command config"),
		RawInput: map[string]any{"cmd": "show running-config"},
	})

	if got != "READ" {
		t.Fatalf("ToolCallName() = %q, want READ", got)
	}
}

func TestToolCallNameDoesNotReturnUnknownForMissingName(t *testing.T) {
	t.Parallel()

	got := ToolCallName(client.ToolCallUpdate{
		RawInput: map[string]any{"reason": "needs approval"},
	})

	if got != "" {
		t.Fatalf("ToolCallName() = %q, want empty name for unknown tool", got)
	}
}

func stringPtr(value string) *string {
	return &value
}
