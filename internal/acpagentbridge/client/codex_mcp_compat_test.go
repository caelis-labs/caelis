package client

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
)

func TestCodexMCPDisplayRequiresExactProvenanceAndPreservesStandardFields(t *testing.T) {
	for _, tc := range []struct{ name, server, tool, kind, existing, want string }{
		{"send", "caelis-collaboration", "SendMessage", "other", "", "SendMessage"},
		{"other server", "other", "SendMessage", "other", "", ""},
		{"unknown tool", "caelis-collaboration", "Unknown", "other", "", ""},
		{"standard kind", "caelis-collaboration", "SendMessage", "execute", "", ""},
		{"exact name", "caelis-collaboration", "SendMessage", "other", "RunCommand", "RunCommand"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := map[string]any{"codex/mcp_tool": map[string]any{"server": tc.server, "name": tc.tool}}
			if tc.existing != "" {
				meta = acpmeta.WithToolName(meta, tc.existing)
			}
			input := map[string]any{"to": "parent", "message": "Actual input"}
			update := ToolCall{Kind: tc.kind, Title: "Misleading / Title", RawInput: input, Meta: meta}
			got := NormalizeInboundUpdate(update).(ToolCall)
			if acpmeta.ToolName(got.Meta) != tc.want || got.Kind != tc.kind || !reflect.DeepEqual(got.RawInput, input) || got.Title != update.Title {
				t.Fatalf("normalized = %#v", got)
			}
			if acpmeta.ToolName(meta) != tc.existing {
				t.Fatal("mutated input metadata")
			}
		})
	}
	got := NormalizeInboundUpdate(ToolCall{Title: "caelis-collaboration / SendMessage", Kind: "other"}).(ToolCall)
	if acpmeta.ToolName(got.Meta) != "" {
		t.Fatal("title supplied identity")
	}
	patch := NormalizeInboundUpdate(ToolCallUpdate{Meta: map[string]any{"codex/mcp_tool": map[string]any{"server": "caelis-collaboration", "name": "SendMessage"}}}).(ToolCallUpdate)
	if acpmeta.ToolName(patch.Meta) != "" {
		t.Fatal("sparse hint overrode an unknown prior standard kind")
	}
}
