package acputil

import (
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
)

func TestToolCallNamePreservesGenericExecuteKind(t *testing.T) {
	t.Parallel()

	kind := acpsdk.ToolKindExecute
	got := ToolCallName(acpsdk.ToolCallUpdate{
		Kind:     &kind,
		Title:    stringPtr("Run command"),
		RawInput: map[string]any{"command": "pwd"},
	})

	if got != "execute" {
		t.Fatalf("ToolCallName() = %q, want generic execute kind", got)
	}
}

func TestToolCallNamePreservesGenericKindOverCommandShapedInput(t *testing.T) {
	t.Parallel()

	kind := acpsdk.ToolKindRead
	got := ToolCallName(acpsdk.ToolCallUpdate{
		Kind:     &kind,
		Title:    stringPtr("Read command config"),
		RawInput: map[string]any{"cmd": "show running-config"},
	})

	if got != "read" {
		t.Fatalf("ToolCallName() = %q, want generic read kind", got)
	}
}

func TestToolCallNameDoesNotReturnUnknownForMissingName(t *testing.T) {
	t.Parallel()

	got := ToolCallName(acpsdk.ToolCallUpdate{
		RawInput: map[string]any{"reason": "needs approval"},
	})

	if got != "" {
		t.Fatalf("ToolCallName() = %q, want empty name for unknown tool", got)
	}
}

func stringPtr(value string) *string {
	return &value
}
