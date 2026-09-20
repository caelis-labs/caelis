package client

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
)

func TestCodexMCPDisplayRequiresExactProvenanceAndPreservesStandardFields(t *testing.T) {
	for _, tc := range []struct{ name, server, tool, kind, existing, want string }{
		{"send", "caelis-collaboration", "SendMessage", "other", "", "SendMessage"},
		{"read", "caelis-collaboration", "ReadMessages", "other", "", "ReadMessages"},
		{"other server", "other", "SendMessage", "other", "", ""},
		{"read other server", "other", "ReadMessages", "other", "", ""},
		{"unknown tool", "caelis-collaboration", "Unknown", "other", "", ""},
		{"similar read tool", "caelis-collaboration", "ReadMessagesExtra", "other", "", ""},
		{"standard kind", "caelis-collaboration", "SendMessage", "execute", "", ""},
		{"read standard kind", "caelis-collaboration", "ReadMessages", "execute", "", ""},
		{"exact name", "caelis-collaboration", "SendMessage", "other", "RunCommand", "RunCommand"},
		{"read exact name", "caelis-collaboration", "ReadMessages", "other", "RunCommand", "RunCommand"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := map[string]any{"codex/mcp_tool": map[string]any{"server": tc.server, "name": tc.tool}}
			if tc.existing != "" {
				meta = acpmeta.WithToolName(meta, tc.existing)
			}
			input := map[string]any{"to": "parent", "message": "Actual input"}
			content := []ToolCallContent{{Type: "content", Content: map[string]any{"type": "text", "text": "Actual content"}}}
			update := ToolCall{Kind: tc.kind, Title: "Misleading / Title", RawInput: input, Content: content, Meta: meta}
			got := NormalizeInboundUpdate(update).(ToolCall)
			if acpmeta.ToolName(got.Meta) != tc.want || got.Kind != tc.kind || !reflect.DeepEqual(got.RawInput, input) || !reflect.DeepEqual(got.Content, content) || got.Title != update.Title {
				t.Fatalf("normalized = %#v", got)
			}
			if acpmeta.ToolName(meta) != tc.existing {
				t.Fatal("mutated input metadata")
			}
		})
	}
	for _, title := range []string{
		"caelis-collaboration / SendMessage",
		"caelis-collaboration / ReadMessages",
	} {
		got := NormalizeInboundUpdate(ToolCall{Title: title, Kind: "other"}).(ToolCall)
		if acpmeta.ToolName(got.Meta) != "" {
			t.Fatalf("title supplied identity for %q", title)
		}
	}
	for _, name := range []string{"SendMessage", "ReadMessages"} {
		patch := NormalizeInboundUpdate(ToolCallUpdate{Meta: map[string]any{"codex/mcp_tool": map[string]any{"server": "caelis-collaboration", "name": name}}}).(ToolCallUpdate)
		if acpmeta.ToolName(patch.Meta) != "" {
			t.Fatalf("sparse hint overrode an unknown prior standard kind for %q", name)
		}
	}
}

func TestCodexMCPDisplayNormalizesReadMessagesSparseCompletion(t *testing.T) {
	kind := toolKindOther
	status := "completed"
	input := map[string]any{"limit": float64(12), "cursor": "cursor-1"}
	content := []ToolCallContent{{Type: "content", Content: map[string]any{"type": "text", "text": "completed messages"}}}
	meta := map[string]any{"codex/mcp_tool": map[string]any{"server": "caelis-collaboration", "name": "ReadMessages"}}

	got := NormalizeInboundUpdate(ToolCallUpdate{
		ToolCallID: "read-1",
		Kind:       &kind,
		Status:     &status,
		RawInput:   input,
		Content:    content,
		Meta:       meta,
	}).(ToolCallUpdate)
	if got.Kind == nil || *got.Kind != kind || got.Status == nil || *got.Status != status {
		t.Fatalf("normalized sparse fields = %#v", got)
	}
	if acpmeta.ToolName(got.Meta) != "ReadMessages" || !reflect.DeepEqual(got.RawInput, input) || !reflect.DeepEqual(got.Content, content) {
		t.Fatalf("normalized sparse completion = %#v", got)
	}
	if acpmeta.ToolName(meta) != "" {
		t.Fatal("mutated sparse completion metadata")
	}
}
