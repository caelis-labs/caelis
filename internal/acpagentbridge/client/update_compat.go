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
		typed.Meta = normalizeInboundTerminalOutput(typed.Meta)
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
	if mapString(provider, "namespace") != xAIToolNamespace {
		return meta, standardKind
	}

	if strings.TrimSpace(standardKind) == "" {
		if providerKind, ok := grokStandardToolKind(provider); ok {
			return meta, providerKind
		}
	}
	if mapString(provider, "kind") != "list" || !providerToolReadOnly(provider) {
		return meta, standardKind
	}

	switch strings.ToLower(strings.TrimSpace(standardKind)) {
	case "":
		standardKind = schema.ToolKindRead
	case schema.ToolKindRead, schema.ToolKindOther:
	default:
		return meta, standardKind
	}
	meta = metautil.WithSection(meta, metautil.Display, map[string]any{
		metautil.DisplayExplorationVerb: "List",
	})
	return meta, standardKind
}

func grokStandardToolKind(provider map[string]any) (string, bool) {
	readOnly, hasReadOnly := provider["read_only"].(bool)
	if !hasReadOnly {
		return "", false
	}
	switch kind := mapString(provider, "kind"); kind {
	case schema.ToolKindRead, schema.ToolKindSearch:
		return kind, readOnly
	case schema.ToolKindEdit, schema.ToolKindExecute:
		return kind, !readOnly
	default:
		return "", false
	}
}

func normalizeInboundToolMeta(meta map[string]any) map[string]any {
	meta = normalizeInboundTerminalOutput(meta)
	return metautil.WithoutSectionKeys(meta, metautil.Display, metautil.DisplayExplorationVerb)
}

// normalizeInboundTerminalOutput consumes the maintained codex-acp alias at
// the external-Agent ingress boundary. Provider-specific keys must not flow
// into canonical projection, replay, or Surface deduplication paths.
func normalizeInboundTerminalOutput(meta map[string]any) map[string]any {
	out := metautil.CloneMap(meta)
	if output, ok := metautil.TerminalOutput(out); ok {
		delete(out, metautil.TerminalOutputDeltaKey)
		return metautil.WithTerminalOutput(out, output.TerminalID, output.Data)
	}
	output, ok := inboundTerminalOutputAlias(out)
	delete(out, metautil.TerminalOutputDeltaKey)
	if !ok {
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return metautil.WithTerminalOutput(out, output.TerminalID, output.Data)
}

func inboundTerminalOutputAlias(meta map[string]any) (metautil.TerminalOutputMeta, bool) {
	values, _ := meta[metautil.TerminalOutputDeltaKey].(map[string]any)
	terminalID, _ := values["terminal_id"].(string)
	data, _ := values["data"].(string)
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" || data == "" {
		return metautil.TerminalOutputMeta{}, false
	}
	return metautil.TerminalOutputMeta{TerminalID: terminalID, Data: data}, true
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
