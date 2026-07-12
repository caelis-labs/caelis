package acputil

import (
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/client"
)

func TestToolCallNameInfersSemanticExecuteName(t *testing.T) {
	t.Parallel()

	got := ToolCallName(client.ToolCallUpdate{
		Kind:     stringPtr("execute"),
		Title:    stringPtr("Run command"),
		RawInput: map[string]any{"command": "pwd"},
	})

	if got != "RunCommand" {
		t.Fatalf("ToolCallName() = %q, want RunCommand", got)
	}
}

func TestToolCallNamePreservesGenericKindOverCommandShapedInput(t *testing.T) {
	t.Parallel()

	got := ToolCallName(client.ToolCallUpdate{
		Kind:     stringPtr("read"),
		Title:    stringPtr("Read command config"),
		RawInput: map[string]any{"cmd": "show running-config"},
	})

	if got != "read" {
		t.Fatalf("ToolCallName() = %q, want generic read kind", got)
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
