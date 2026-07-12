package eventstream

import (
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestResolveRelationDeliveryPrefersTypedPointersOverLegacyMetadata(t *testing.T) {
	t.Parallel()

	env := Envelope{
		ParentTool: &ParentToolRelation{ToolCallID: "typed-parent", ToolName: "Spawn"},
		Delivery:   &Delivery{},
		Meta:       legacyRelationDeliveryMetaForTest("legacy-parent", "TASK", true, true),
	}
	resolved := ResolveRelationDelivery(env)
	if resolved.ParentTool == nil || resolved.ParentTool.ToolCallID != "typed-parent" || resolved.ParentTool.ToolName != "Spawn" {
		t.Fatalf("parent relation = %#v, want typed Spawn parent", resolved.ParentTool)
	}
	if resolved.Delivery == nil || resolved.Delivery.Transient || resolved.Delivery.HasParentToolMirror || resolved.Delivery.IsParentToolMirror {
		t.Fatalf("delivery = %#v, want typed zero delivery without legacy fallback", resolved.Delivery)
	}
	resolved.ParentTool.ToolCallID = "mutated"
	resolved.Delivery.Transient = true
	if env.ParentTool.ToolCallID != "typed-parent" || env.Delivery.Transient {
		t.Fatalf("resolved values mutated source envelope: %#v", env)
	}
}

func TestResolveRelationDeliveryFallsBackToLegacyMetadataInUpdate(t *testing.T) {
	t.Parallel()

	env := Envelope{
		Kind: KindSessionUpdate,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "child-tool-1",
			Meta:          legacyRelationDeliveryMetaForTest("spawn-call-1", "SPAWN", true, true),
		},
	}
	resolved := ResolveRelationDelivery(env)
	if resolved.ParentTool == nil || resolved.ParentTool.ToolCallID != "spawn-call-1" || resolved.ParentTool.ToolName != "SPAWN" {
		t.Fatalf("parent relation = %#v, want legacy Spawn parent", resolved.ParentTool)
	}
	if resolved.Delivery == nil || !resolved.Delivery.Transient || !resolved.Delivery.HasParentToolMirror || resolved.Delivery.IsParentToolMirror {
		t.Fatalf("delivery = %#v, want legacy transient mirror delivery", resolved.Delivery)
	}
}

func TestResolveRelationDeliveryFallsBackPerMissingTypedPointer(t *testing.T) {
	t.Parallel()

	legacyMeta := legacyRelationDeliveryMetaForTest("legacy-parent", "TASK", true, true)
	tests := []struct {
		name             string
		env              Envelope
		wantParentCallID string
		wantParentTool   string
		wantDelivery     Delivery
	}{
		{
			name: "typed relation with legacy delivery",
			env: Envelope{
				ParentTool: &ParentToolRelation{ToolCallID: "typed-parent", ToolName: "Spawn"},
				Meta:       legacyMeta,
			},
			wantParentCallID: "typed-parent",
			wantParentTool:   "Spawn",
			wantDelivery:     Delivery{Transient: true, HasParentToolMirror: true},
		},
		{
			name: "typed delivery with legacy relation",
			env: Envelope{
				Delivery: &Delivery{},
				Meta:     legacyMeta,
			},
			wantParentCallID: "legacy-parent",
			wantParentTool:   "TASK",
			wantDelivery:     Delivery{},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			resolved := ResolveRelationDelivery(tt.env)
			if resolved.ParentTool == nil || resolved.ParentTool.ToolCallID != tt.wantParentCallID || resolved.ParentTool.ToolName != tt.wantParentTool {
				t.Fatalf("parent relation = %#v, want %s/%s", resolved.ParentTool, tt.wantParentTool, tt.wantParentCallID)
			}
			if resolved.Delivery == nil || *resolved.Delivery != tt.wantDelivery {
				t.Fatalf("delivery = %#v, want %#v", resolved.Delivery, tt.wantDelivery)
			}
		})
	}
}

func legacyRelationDeliveryMetaForTest(parentCallID string, parentTool string, transient bool, mirrored bool) map[string]any {
	stream := map[string]any{
		"parent_call_id": parentCallID,
		"parent_tool":    parentTool,
	}
	if mirrored {
		stream["mirrored_to_parent_tool"] = true
	}
	caelis := map[string]any{
		"runtime": map[string]any{"stream": stream},
	}
	if transient {
		caelis["transient"] = true
	}
	return map[string]any{"caelis": caelis}
}
