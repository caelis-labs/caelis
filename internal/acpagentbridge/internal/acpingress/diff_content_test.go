package acpingress

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestStructuredDiffIngressDeepCopiesDurableFacts(t *testing.T) {
	oldPath, fileType, mimeType := "/tmp/source ", "text", "text/plain"
	changes := []client.ToolDiffChange{{Operation: "move", Path: "/tmp/destination ", OldPath: &oldPath, FileType: &fileType, MimeType: &mimeType, Meta: map[string]any{"nested": map[string]any{"label": "original"}}}}
	wire := client.ToolCallUpdate{
		SessionUpdate: client.UpdateToolCallState, ToolCallID: "edit",
		Content: []client.ToolCallContent{{
			Type: "diff", Changes: &changes,
			Patch: &client.ToolDiffPatch{Format: "git_patch", Text: "raw\r\npatch\r\n"},
			Meta:  map[string]any{"nested": map[string]any{"label": "original"}},
		}},
	}
	canonical := protocolUpdateFromToolCallUpdate(wire)
	want := session.CloneEventProtocol(session.EventProtocol{Update: canonical}).Update
	oldPath, fileType, mimeType = "mutated", "binary", "changed/type"
	changes[0].Path = "mutated"
	changes[0].Meta["nested"].(map[string]any)["label"] = "mutated"
	wire.Content[0].Patch.Text = "mutated"
	wire.Content[0].Meta["nested"].(map[string]any)["label"] = "mutated"
	if !reflect.DeepEqual(canonical.Content, want.Content) {
		t.Fatalf("durable diff aliased wire facts: %#v, want %#v", canonical.Content, want.Content)
	}
	content := session.ProtocolToolCallContentOf(canonical)
	if len(content) != 1 || content[0].Changes == nil || (*content[0].Changes)[0].OldPath == nil || *(*content[0].Changes)[0].OldPath != "/tmp/source " || content[0].Patch.Text != "raw\r\npatch\r\n" {
		t.Fatalf("diff facts changed: %#v", content)
	}
}
