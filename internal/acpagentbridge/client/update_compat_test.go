package client

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestNormalizeInboundUpdateConsumesTerminalOutputCompatibilityAlias(t *testing.T) {
	t.Parallel()

	meta := map[string]any{
		metautil.TerminalOutputDeltaKey: map[string]any{
			"terminal_id": "command-1",
			"data":        "provider output\n",
		},
	}
	normalized := NormalizeInboundUpdate(ContentChunk{Meta: meta}).(ContentChunk)
	output, ok := metautil.TerminalOutput(normalized.Meta)
	if !ok || output.TerminalID != "command-1" || output.Data != "provider output\n" {
		t.Fatalf("normalized terminal output = %#v, %v; want codex-acp compatibility output", output, ok)
	}
	if _, ok := normalized.Meta[metautil.TerminalOutputDeltaKey]; ok {
		t.Fatalf("NormalizeInboundUpdate() retained provider alias: %#v", normalized.Meta)
	}
	if _, ok := meta[metautil.TerminalOutputKey]; ok {
		t.Fatalf("NormalizeInboundUpdate() mutated provider metadata: %#v", meta)
	}
}

func TestNormalizeInboundUpdateDropsMalformedTerminalOutputCompatibilityAlias(t *testing.T) {
	t.Parallel()

	meta := map[string]any{
		metautil.TerminalOutputDeltaKey: map[string]any{"data": "missing terminal id\n"},
		"kept":                          true,
	}
	normalized := NormalizeInboundUpdate(ContentChunk{Meta: meta}).(ContentChunk)
	if _, ok := normalized.Meta[metautil.TerminalOutputDeltaKey]; ok {
		t.Fatalf("NormalizeInboundUpdate() retained malformed provider alias: %#v", normalized.Meta)
	}
	if _, ok := metautil.TerminalOutput(normalized.Meta); ok || normalized.Meta["kept"] != true {
		t.Fatalf("NormalizeInboundUpdate() metadata = %#v, want unrelated metadata without terminal output", normalized.Meta)
	}
	if _, ok := meta[metautil.TerminalOutputDeltaKey]; !ok {
		t.Fatalf("NormalizeInboundUpdate() mutated provider metadata: %#v", meta)
	}
}

func TestNormalizeInboundUpdateCanonicalTerminalOutputWinsOverCompatibilityAlias(t *testing.T) {
	t.Parallel()

	normalized := NormalizeInboundUpdate(ContentChunk{Meta: map[string]any{
		metautil.TerminalOutputKey: map[string]any{
			"terminal_id": "command-1", "data": "canonical output\n",
		},
		metautil.TerminalOutputDeltaKey: map[string]any{
			"terminal_id": "command-1", "data": "stale provider output\n",
		},
	}}).(ContentChunk)
	output, ok := metautil.TerminalOutput(normalized.Meta)
	if !ok || output.Data != "canonical output\n" {
		t.Fatalf("normalized terminal output = %#v, %v; want canonical output", output, ok)
	}
	if _, ok := normalized.Meta[metautil.TerminalOutputDeltaKey]; ok {
		t.Fatalf("NormalizeInboundUpdate() retained provider alias: %#v", normalized.Meta)
	}
}

func TestNormalizeInboundUpdateExtendsGrokListWithoutForgingToolIdentity(t *testing.T) {
	t.Parallel()

	input := map[string]any{"variant": "ListDir", "target_directory": "docs"}
	meta := grokListMeta(true)

	normalized, ok := NormalizeInboundUpdate(ToolCall{
		SessionUpdate: eventstream.UpdateToolCall,
		ToolCallID:    "list-1",
		Title:         "List `docs`",
		RawInput:      input,
		Meta:          meta,
	}).(ToolCall)
	if !ok {
		t.Fatalf("NormalizeInboundUpdate() type = %T, want ToolCall", normalized)
	}
	if normalized.Kind != eventstream.ToolKindRead {
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
		{name: "read", provider: grokToolMeta("read_file", eventstream.ToolKindRead, "Read", true), wantKind: eventstream.ToolKindRead},
		{name: "search", provider: grokToolMeta("grep", eventstream.ToolKindSearch, "Search", true), wantKind: eventstream.ToolKindSearch},
		{name: "edit", provider: grokToolMeta("search_replace", eventstream.ToolKindEdit, "Edit", false), wantKind: eventstream.ToolKindEdit},
		{name: "execute", provider: grokToolMeta("run_terminal_command", eventstream.ToolKindExecute, "Run Command", false), wantKind: eventstream.ToolKindExecute},
		{name: "wrong namespace", provider: map[string]any{
			"namespace": "other", "kind": eventstream.ToolKindExecute, "read_only": false,
		}},
		{name: "missing read only", provider: map[string]any{
			"namespace": xAIToolNamespace, "kind": eventstream.ToolKindExecute,
		}},
		{name: "execute marked read only", provider: grokToolMeta("run_terminal_command", eventstream.ToolKindExecute, "Run Command", true)},
		{name: "read marked mutating", provider: grokToolMeta("read_file", eventstream.ToolKindRead, "Read", false)},
		{name: "unknown provider kind", provider: grokToolMeta("todo_write", "plan", "Plan", false)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := map[string]any{xAIToolMetaKey: tt.provider}
			normalized := NormalizeInboundUpdate(ToolCall{
				SessionUpdate: eventstream.UpdateToolCall,
				ToolCallID:    "grok-tool-1",
				Title:         "provider title",
				Status:        eventstream.ToolStatusInProgress,
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

	providerMeta := map[string]any{xAIToolMetaKey: grokToolMeta("run_terminal_command", eventstream.ToolKindExecute, "Run Command", false)}
	for _, explicitKind := range []string{eventstream.ToolKindOther, eventstream.ToolKindSearch} {
		explicit := NormalizeInboundUpdate(ToolCall{
			SessionUpdate: eventstream.UpdateToolCall,
			ToolCallID:    "specific-1",
			Kind:          explicitKind,
			Meta:          providerMeta,
		}).(ToolCall)
		if explicit.Kind != explicitKind {
			t.Fatalf("explicit standard kind = %q, want %q to win over provider execute", explicit.Kind, explicitKind)
		}
	}

	explicitOther := eventstream.ToolKindOther
	update := NormalizeInboundUpdate(ToolCallUpdate{
		SessionUpdate: eventstream.UpdateToolCallInfo,
		ToolCallID:    "specific-update-1",
		Kind:          &explicitOther,
		Meta:          providerMeta,
	}).(ToolCallUpdate)
	if update.Kind == nil || *update.Kind != eventstream.ToolKindOther {
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
		{name: "missing standard kind", meta: grokListMeta(true), wantKind: eventstream.ToolKindRead, wantVerb: "List"},
		{name: "generic other keeps wire category and refines display", kind: eventstream.ToolKindOther, meta: grokListMeta(true), wantKind: eventstream.ToolKindOther, wantVerb: "List"},
		{name: "standard read keeps category and refines verb", kind: eventstream.ToolKindRead, meta: grokListMeta(true), wantKind: eventstream.ToolKindRead, wantVerb: "List"},
		{name: "standard search wins", kind: eventstream.ToolKindSearch, meta: grokListMeta(true), wantKind: eventstream.ToolKindSearch},
		{name: "standard edit wins", kind: eventstream.ToolKindEdit, meta: grokListMeta(true), wantKind: eventstream.ToolKindEdit},
		{name: "standard execute wins", kind: eventstream.ToolKindExecute, meta: grokListMeta(true), wantKind: eventstream.ToolKindExecute},
		{name: "provider says mutating", kind: eventstream.ToolKindOther, meta: grokListMeta(false), wantKind: eventstream.ToolKindOther},
		{name: "provider read only missing", kind: eventstream.ToolKindOther, meta: map[string]any{xAIToolMetaKey: map[string]any{
			"kind": "list", "namespace": xAIToolNamespace,
		}}, wantKind: eventstream.ToolKindOther},
		{name: "unrecognized provider namespace", kind: eventstream.ToolKindOther, meta: map[string]any{xAIToolMetaKey: map[string]any{
			"kind": "list", "namespace": "other", "read_only": true,
		}}, wantKind: eventstream.ToolKindOther},
		{name: "explicit generic kind wins over standard provider kind", kind: eventstream.ToolKindOther, meta: map[string]any{xAIToolMetaKey: map[string]any{
			"kind": "read", "namespace": xAIToolNamespace, "read_only": true,
		}}, wantKind: eventstream.ToolKindOther},
		{name: "provider kind is case sensitive", kind: eventstream.ToolKindOther, meta: map[string]any{xAIToolMetaKey: map[string]any{
			"kind": "LIST", "namespace": xAIToolNamespace, "read_only": true,
		}}, wantKind: eventstream.ToolKindOther},
		{name: "provider kind rejects whitespace", kind: eventstream.ToolKindOther, meta: map[string]any{xAIToolMetaKey: map[string]any{
			"kind": " list ", "namespace": xAIToolNamespace, "read_only": true,
		}}, wantKind: eventstream.ToolKindOther},
		{name: "provider namespace rejects whitespace", kind: eventstream.ToolKindOther, meta: map[string]any{xAIToolMetaKey: map[string]any{
			"kind": "list", "namespace": " grok_build ", "read_only": true,
		}}, wantKind: eventstream.ToolKindOther},
		{name: "title alone is not classification", kind: eventstream.ToolKindOther, wantKind: eventstream.ToolKindOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized := NormalizeInboundUpdate(ToolCall{
				SessionUpdate: eventstream.UpdateToolCall,
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
	kind := eventstream.ToolKindRead
	update := ToolCallUpdate{
		SessionUpdate: eventstream.UpdateToolCallInfo,
		ToolCallID:    "list-1",
		Kind:          &kind,
		RawInput:      input,
		Meta:          meta,
	}

	first := NormalizeInboundUpdate(update).(ToolCallUpdate)
	second := NormalizeInboundUpdate(first).(ToolCallUpdate)
	if first.Kind == nil || *first.Kind != eventstream.ToolKindRead || second.Kind == nil || *second.Kind != eventstream.ToolKindRead {
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

	kind := eventstream.ToolKindOther
	update := ToolCallUpdate{
		SessionUpdate: eventstream.UpdateToolCallInfo,
		ToolCallID:    "list-other-1",
		Kind:          &kind,
		RawInput:      map[string]any{"variant": "ListDir", "target_directory": "docs"},
		Meta:          grokListMeta(true),
	}

	first := NormalizeInboundUpdate(update).(ToolCallUpdate)
	second := NormalizeInboundUpdate(first).(ToolCallUpdate)
	if first.Kind == nil || *first.Kind != eventstream.ToolKindOther || second.Kind == nil || *second.Kind != eventstream.ToolKindOther {
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
		SessionUpdate: eventstream.UpdateToolCallInfo,
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
		SessionUpdate: eventstream.UpdateToolCall,
		ToolCallID:    "other-1",
		Title:         "List `docs`",
		Kind:          eventstream.ToolKindOther,
		Meta:          meta,
	}).(ToolCall)
	if normalized.Kind != eventstream.ToolKindOther {
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
