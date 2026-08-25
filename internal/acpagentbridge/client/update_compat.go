package client

import (
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

const (
	xAIToolMetaKey   = "x.ai/tool"
	xAIToolNamespace = "grok_build"
)

// NormalizeInboundUpdate consumes maintained provider compatibility metadata
// before an update branches into canonical and native projections.
func NormalizeInboundUpdate(update Update) Update {
	switch typed := update.(type) {
	case ContentChunk:
		typed.Meta = metautil.NormalizeTerminalOutput(typed.Meta)
		return typed
	case ToolCall:
		typed.Meta, typed.Kind = normalizeInboundToolDisplay(typed.Meta, typed.Kind)
		return typed
	case ToolCallUpdate:
		if typed.Kind == nil {
			typed.Meta = normalizeInboundToolMeta(typed.Meta)
			return typed
		}
		kind := stringValue(typed.Kind)
		typed.Meta, kind = normalizeInboundToolDisplay(typed.Meta, kind)
		if kind != stringValue(typed.Kind) {
			typed.Kind = &kind
		}
		return typed
	default:
		return update
	}
}

func normalizeInboundToolDisplay(meta map[string]any, standardKind string) (map[string]any, string) {
	meta = normalizeInboundToolMeta(meta)
	provider := providerToolMeta(meta)
	if mapString(provider, "namespace") != xAIToolNamespace ||
		mapString(provider, "kind") != "list" || !providerToolReadOnly(provider) {
		return meta, standardKind
	}

	switch strings.ToLower(strings.TrimSpace(standardKind)) {
	case "", schema.ToolKindOther:
		standardKind = schema.ToolKindRead
	case schema.ToolKindRead:
	default:
		return meta, standardKind
	}
	meta = metautil.WithSection(meta, metautil.Display, map[string]any{
		metautil.DisplayExplorationVerb: "List",
	})
	return meta, standardKind
}

func normalizeInboundToolMeta(meta map[string]any) map[string]any {
	meta = metautil.NormalizeTerminalOutput(meta)
	return metautil.WithoutSectionKeys(meta, metautil.Display, metautil.DisplayExplorationVerb)
}

func providerToolMeta(meta map[string]any) map[string]any {
	provider, _ := meta[xAIToolMetaKey].(map[string]any)
	return provider
}

func mapString(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func providerToolReadOnly(provider map[string]any) bool {
	readOnly, _ := provider["read_only"].(bool)
	return readOnly
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
