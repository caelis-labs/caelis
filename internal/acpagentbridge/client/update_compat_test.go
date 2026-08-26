package client

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestNormalizeInboundUpdateExtendsGrokListWithoutForgingToolIdentity(t *testing.T) {
	t.Parallel()

	input := map[string]any{"variant": "ListDir", "target_directory": "docs"}
	meta := grokListMeta(true)

	normalized, ok := NormalizeInboundUpdate(ToolCall{
		SessionUpdate: schema.UpdateToolCall,
		ToolCallID:    "list-1",
		Title:         "List `docs`",
		RawInput:      input,
		Meta:          meta,
	}).(ToolCall)
	if !ok {
		t.Fatalf("NormalizeInboundUpdate() type = %T, want ToolCall", normalized)
	}
	if normalized.Kind != schema.ToolKindRead {
		t.Fatalf("normalized kind = %q, want compatibility read kind", normalized.Kind)
	}
	if normalized.Title != "List `docs`" {
		t.Fatalf("normalized title = %q, want standard title preserved", normalized.Title)
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

func TestNormalizeInboundUpdateRestoresGrokStandardKindWithoutForgingToolIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider map[string]any
		wantKind string
	}{
		{name: "read", provider: grokToolMeta("read_file", schema.ToolKindRead, "Read", true), wantKind: schema.ToolKindRead},
		{name: "search", provider: grokToolMeta("grep", schema.ToolKindSearch, "Search", true), wantKind: schema.ToolKindSearch},
		{name: "edit", provider: grokToolMeta("search_replace", schema.ToolKindEdit, "Edit", false), wantKind: schema.ToolKindEdit},
		{name: "execute", provider: grokToolMeta("run_terminal_command", schema.ToolKindExecute, "Run Command", false), wantKind: schema.ToolKindExecute},
		{name: "wrong namespace", provider: map[string]any{
			"namespace": "other", "kind": schema.ToolKindExecute, "read_only": false,
		}},
		{name: "missing read only", provider: map[string]any{
			"namespace": xAIToolNamespace, "kind": schema.ToolKindExecute,
		}},
		{name: "execute marked read only", provider: grokToolMeta("run_terminal_command", schema.ToolKindExecute, "Run Command", true)},
		{name: "read marked mutating", provider: grokToolMeta("read_file", schema.ToolKindRead, "Read", false)},
		{name: "unknown provider kind", provider: grokToolMeta("todo_write", "plan", "Plan", false)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := map[string]any{xAIToolMetaKey: tt.provider}
			normalized := NormalizeInboundUpdate(ToolCall{
				SessionUpdate: schema.UpdateToolCall,
				ToolCallID:    "grok-tool-1",
				Title:         "provider title",
				Status:        schema.ToolStatusInProgress,
				Meta:          meta,
			}).(ToolCall)
			if normalized.Kind != tt.wantKind {
				t.Fatalf("normalized kind = %q, want %q", normalized.Kind, tt.wantKind)
			}
			if got := metautil.String(normalized.Meta, metautil.Root, metautil.Runtime, metautil.RuntimeTool, metautil.RuntimeToolName); got != "" {
				t.Fatalf("runtime exact tool name = %q, want no identity forged from provider metadata", got)
			}
			if !reflect.DeepEqual(normalized.Meta[xAIToolMetaKey], tt.provider) {
				t.Fatalf("provider metadata = %#v, want preserved %#v", normalized.Meta[xAIToolMetaKey], tt.provider)
			}
		})
	}

	providerMeta := map[string]any{xAIToolMetaKey: grokToolMeta("run_terminal_command", schema.ToolKindExecute, "Run Command", false)}
	for _, explicitKind := range []string{schema.ToolKindOther, schema.ToolKindSearch} {
		explicit := NormalizeInboundUpdate(ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "specific-1",
			Kind:          explicitKind,
			Meta:          providerMeta,
		}).(ToolCall)
		if explicit.Kind != explicitKind {
			t.Fatalf("explicit standard kind = %q, want %q to win over provider execute", explicit.Kind, explicitKind)
		}
	}

	explicitOther := schema.ToolKindOther
	update := NormalizeInboundUpdate(ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    "specific-update-1",
		Kind:          &explicitOther,
		Meta:          providerMeta,
	}).(ToolCallUpdate)
	if update.Kind == nil || *update.Kind != schema.ToolKindOther {
		t.Fatalf("explicit update kind = %v, want other to win over provider execute", update.Kind)
	}
}

func TestNormalizeInboundUpdateGrokListCompatibilityRefinesGenericOtherWithoutOverridingSpecificKinds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		kind     string
		meta     map[string]any
		wantKind string
		wantVerb string
	}{
		{name: "missing standard kind", meta: grokListMeta(true), wantKind: schema.ToolKindRead, wantVerb: "List"},
		{name: "generic other keeps wire category and refines display", kind: schema.ToolKindOther, meta: grokListMeta(true), wantKind: schema.ToolKindOther, wantVerb: "List"},
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
		{name: "explicit generic kind wins over standard provider kind", kind: schema.ToolKindOther, meta: map[string]any{xAIToolMetaKey: map[string]any{
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
	kind := schema.ToolKindRead
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
		t.Fatalf("normalized kinds = %v then %v, want explicit read preserved", first.Kind, second.Kind)
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

func TestNormalizeInboundUpdateGrokListIsIdempotentForExplicitOtherUpdate(t *testing.T) {
	t.Parallel()

	kind := schema.ToolKindOther
	update := ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    "list-other-1",
		Kind:          &kind,
		RawInput:      map[string]any{"variant": "ListDir", "target_directory": "docs"},
		Meta:          grokListMeta(true),
	}

	first := NormalizeInboundUpdate(update).(ToolCallUpdate)
	second := NormalizeInboundUpdate(first).(ToolCallUpdate)
	if first.Kind == nil || *first.Kind != schema.ToolKindOther || second.Kind == nil || *second.Kind != schema.ToolKindOther {
		t.Fatalf("normalized kinds = %v then %v, want explicit other preserved", first.Kind, second.Kind)
	}
	if explorationVerb(first.Meta) != "List" || explorationVerb(second.Meta) != "List" {
		t.Fatalf("normalized verbs = %q then %q, want List", explorationVerb(first.Meta), explorationVerb(second.Meta))
	}
	if !reflect.DeepEqual(second.Meta[xAIToolMetaKey], update.Meta[xAIToolMetaKey]) {
		t.Fatalf("second normalization changed provider evidence: %#v", second.Meta[xAIToolMetaKey])
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
	provider := grokToolMeta("list_dir", "list", "List Files", readOnly)
	provider["input"] = map[string]any{"directory": "docs"}
	return map[string]any{xAIToolMetaKey: provider}
}

func grokToolMeta(name, kind, label string, readOnly bool) map[string]any {
	return map[string]any{
		"version":   1,
		"name":      name,
		"kind":      kind,
		"namespace": xAIToolNamespace,
		"label":     label,
		"read_only": readOnly,
	}
}

func explorationVerb(meta map[string]any) string {
	return metautil.String(meta, metautil.Root, metautil.Display, metautil.DisplayExplorationVerb)
}
