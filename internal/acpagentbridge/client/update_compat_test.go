package client

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestNormalizeInboundUpdateExtendsGrokListWithoutForgingToolIdentity(t *testing.T) {
	t.Parallel()

	kind := schema.ToolKindOther
	input := map[string]any{"variant": "ListDir", "target_directory": "docs"}
	meta := grokListMeta(true)

	normalized, ok := NormalizeInboundUpdate(ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    "list-1",
		Title:         testStringPointer("List `docs`"),
		Kind:          &kind,
		RawInput:      input,
		Meta:          meta,
	}).(ToolCallUpdate)
	if !ok {
		t.Fatalf("NormalizeInboundUpdate() type = %T, want ToolCallUpdate", normalized)
	}
	if normalized.Kind == nil || *normalized.Kind != schema.ToolKindRead {
		t.Fatalf("normalized kind = %v, want compatibility read kind", normalized.Kind)
	}
	if normalized.Title == nil || *normalized.Title != "List `docs`" {
		t.Fatalf("normalized title = %v, want standard title preserved", normalized.Title)
	}
	if !reflect.DeepEqual(normalized.RawInput, input) {
		t.Fatalf("normalized raw input = %#v, want unchanged %#v", normalized.RawInput, input)
	}
	if !reflect.DeepEqual(normalized.Meta[xAIToolMetaKey], meta[xAIToolMetaKey]) {
		t.Fatalf("provider metadata = %#v, want preserved %#v", normalized.Meta[xAIToolMetaKey], meta[xAIToolMetaKey])
	}
	if got := explorationVerb(normalized.Meta); got != "List" {
		t.Fatalf("exploration verb = %q, want List", got)
	}
}

func TestNormalizeInboundUpdateGrokListCompatibilityDefersToSpecificStandardKinds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		kind     string
		meta     map[string]any
		wantKind string
		wantVerb string
	}{
		{name: "missing standard kind", meta: grokListMeta(true), wantKind: schema.ToolKindRead, wantVerb: "List"},
		{name: "generic standard kind", kind: schema.ToolKindOther, meta: grokListMeta(true), wantKind: schema.ToolKindRead, wantVerb: "List"},
		{name: "standard read keeps category and refines verb", kind: schema.ToolKindRead, meta: grokListMeta(true), wantKind: schema.ToolKindRead, wantVerb: "List"},
		{name: "standard search wins", kind: schema.ToolKindSearch, meta: grokListMeta(true), wantKind: schema.ToolKindSearch},
		{name: "standard edit wins", kind: schema.ToolKindEdit, meta: grokListMeta(true), wantKind: schema.ToolKindEdit},
		{name: "standard execute wins", kind: schema.ToolKindExecute, meta: grokListMeta(true), wantKind: schema.ToolKindExecute},
		{name: "provider says mutating", kind: schema.ToolKindOther, meta: grokListMeta(false), wantKind: schema.ToolKindOther},
		{name: "provider read only missing", kind: schema.ToolKindOther, meta: map[string]any{xAIToolMetaKey: map[string]any{
			"kind": "list", "namespace": xAIToolNamespace,
		}}, wantKind: schema.ToolKindOther},
		{name: "unrecognized provider namespace", kind: schema.ToolKindOther, meta: map[string]any{xAIToolMetaKey: map[string]any{
			"kind": "list", "namespace": "other", "read_only": true,
		}}, wantKind: schema.ToolKindOther},
		{name: "unrecognized provider kind", kind: schema.ToolKindOther, meta: map[string]any{xAIToolMetaKey: map[string]any{
			"kind": "read", "namespace": xAIToolNamespace, "read_only": true,
		}}, wantKind: schema.ToolKindOther},
		{name: "provider kind is case sensitive", kind: schema.ToolKindOther, meta: map[string]any{xAIToolMetaKey: map[string]any{
			"kind": "LIST", "namespace": xAIToolNamespace, "read_only": true,
		}}, wantKind: schema.ToolKindOther},
		{name: "provider kind rejects whitespace", kind: schema.ToolKindOther, meta: map[string]any{xAIToolMetaKey: map[string]any{
			"kind": " list ", "namespace": xAIToolNamespace, "read_only": true,
		}}, wantKind: schema.ToolKindOther},
		{name: "provider namespace rejects whitespace", kind: schema.ToolKindOther, meta: map[string]any{xAIToolMetaKey: map[string]any{
			"kind": "list", "namespace": " grok_build ", "read_only": true,
		}}, wantKind: schema.ToolKindOther},
		{name: "title alone is not classification", kind: schema.ToolKindOther, wantKind: schema.ToolKindOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized := NormalizeInboundUpdate(ToolCall{
				SessionUpdate: schema.UpdateToolCall,
				ToolCallID:    "list-1",
				Title:         "List `docs`",
				Kind:          tt.kind,
				RawInput:      map[string]any{"target_directory": "docs"},
				Meta:          tt.meta,
			}).(ToolCall)
			if normalized.Kind != tt.wantKind {
				t.Fatalf("normalized kind = %q, want %q", normalized.Kind, tt.wantKind)
			}
			if got := explorationVerb(normalized.Meta); got != tt.wantVerb {
				t.Fatalf("exploration verb = %q, want %q", got, tt.wantVerb)
			}
		})
	}
}

func TestNormalizeInboundUpdateGrokListIsIdempotentForExplicitUpdateKind(t *testing.T) {
	t.Parallel()

	input := map[string]any{"target_directory": "docs"}
	meta := grokListMeta(true)
	meta = metautil.WithSection(meta, metautil.Display, map[string]any{"unrelated": "kept"})
	kind := schema.ToolKindOther
	update := ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    "list-1",
		Kind:          &kind,
		RawInput:      input,
		Meta:          meta,
	}

	first := NormalizeInboundUpdate(update).(ToolCallUpdate)
	second := NormalizeInboundUpdate(first).(ToolCallUpdate)
	if first.Kind == nil || *first.Kind != schema.ToolKindRead || second.Kind == nil || *second.Kind != schema.ToolKindRead {
		t.Fatalf("normalized kinds = %v then %v, want read", first.Kind, second.Kind)
	}
	if explorationVerb(first.Meta) != "List" || explorationVerb(second.Meta) != "List" {
		t.Fatalf("normalized verbs = %q then %q, want List", explorationVerb(first.Meta), explorationVerb(second.Meta))
	}
	if got := metautil.String(second.Meta, metautil.Root, metautil.Display, "unrelated"); got != "kept" {
		t.Fatalf("unrelated display metadata = %q, want kept", got)
	}
	if !reflect.DeepEqual(second.RawInput, input) || !reflect.DeepEqual(second.Meta[xAIToolMetaKey], meta[xAIToolMetaKey]) {
		t.Fatalf("second normalization changed provider evidence: %#v", second)
	}
}

func TestNormalizeInboundUpdateSparseKindDoesNotOverridePriorStandardKind(t *testing.T) {
	t.Parallel()

	meta := metautil.WithSection(grokListMeta(true), metautil.Display, map[string]any{
		metautil.DisplayExplorationVerb: "List",
	})
	normalized := NormalizeInboundUpdate(ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    "specific-1",
		Meta:          meta,
	}).(ToolCallUpdate)
	if normalized.Kind != nil {
		t.Fatalf("normalized sparse kind = %v, want omitted kind preserved", normalized.Kind)
	}
	if got := explorationVerb(normalized.Meta); got != "" {
		t.Fatalf("exploration verb = %q, want no category hint on a sparse update", got)
	}
	if !reflect.DeepEqual(normalized.Meta[xAIToolMetaKey], meta[xAIToolMetaKey]) {
		t.Fatalf("provider metadata = %#v, want preserved %#v", normalized.Meta[xAIToolMetaKey], meta[xAIToolMetaKey])
	}
}

func TestNormalizeInboundUpdateRejectsForgedExplorationVerbWithoutStrictProviderMetadata(t *testing.T) {
	t.Parallel()

	meta := metautil.WithSection(nil, metautil.Display, map[string]any{
		metautil.DisplayExplorationVerb: "List",
	})
	normalized := NormalizeInboundUpdate(ToolCall{
		SessionUpdate: schema.UpdateToolCall,
		ToolCallID:    "other-1",
		Title:         "List `docs`",
		Kind:          schema.ToolKindOther,
		Meta:          meta,
	}).(ToolCall)
	if normalized.Kind != schema.ToolKindOther {
		t.Fatalf("normalized kind = %q, want other", normalized.Kind)
	}
	if got := explorationVerb(normalized.Meta); got != "" {
		t.Fatalf("exploration verb = %q, want untrusted hint removed", got)
	}
}

func grokListMeta(readOnly bool) map[string]any {
	return map[string]any{xAIToolMetaKey: map[string]any{
		"version":   1,
		"name":      "list_dir",
		"kind":      "list",
		"namespace": xAIToolNamespace,
		"label":     "List Files",
		"read_only": readOnly,
		"input":     map[string]any{"directory": "docs"},
	}}
}

func explorationVerb(meta map[string]any) string {
	return metautil.String(meta, metautil.Root, metautil.Display, metautil.DisplayExplorationVerb)
}

func testStringPointer(value string) *string { return &value }
