package client

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
	"github.com/caelis-labs/caelis/internal/jsonvalue"
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
		typed.Meta = normalizeInboundContentMeta(typed.Meta)
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

// normalizeInboundContentMeta removes local Agent-input identity before the
// external update branches into native live output and canonical history.
func normalizeInboundContentMeta(meta map[string]any) map[string]any {
	out := normalizeInboundTerminalOutput(meta)
	caelis, ok := out["caelis"].(map[string]any)
	if !ok {
		return out
	}
	delete(caelis, session.AgentCommunicationMetaKey)
	if len(caelis) == 0 {
		delete(out, "caelis")
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
		standardKind = toolKindRead
	case toolKindRead, toolKindOther:
	default:
		return meta, standardKind
	}
	meta = withDisplayExplorationVerb(meta, "List")
	return meta, standardKind
}

func grokStandardToolKind(provider map[string]any) (string, bool) {
	readOnly, hasReadOnly := provider["read_only"].(bool)
	if !hasReadOnly {
		return "", false
	}
	switch kind := mapString(provider, "kind"); kind {
	case toolKindRead, toolKindSearch:
		return kind, readOnly
	case toolKindEdit, toolKindExecute:
		return kind, !readOnly
	default:
		return "", false
	}
}

func normalizeInboundToolMeta(meta map[string]any) map[string]any {
	meta = normalizeInboundTerminalOutput(meta)
	return withoutDisplayExplorationVerb(meta)
}

// normalizeInboundTerminalOutput consumes the maintained codex-acp alias at
// the external-Agent ingress boundary. Provider-specific keys must not flow
// into canonical projection, replay, or Surface deduplication paths.
func normalizeInboundTerminalOutput(meta map[string]any) map[string]any {
	out := jsonvalue.CloneMap(meta)
	if output, ok := acpmeta.ReadTerminalOutput(out); ok {
		delete(out, acpmeta.TerminalOutputDeltaKey)
		return acpmeta.WithTerminalOutput(out, output.TerminalID, output.Data)
	}
	output, ok := inboundTerminalOutputAlias(out)
	delete(out, acpmeta.TerminalOutputDeltaKey)
	if !ok {
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return acpmeta.WithTerminalOutput(out, output.TerminalID, output.Data)
}

func inboundTerminalOutputAlias(meta map[string]any) (acpmeta.TerminalOutput, bool) {
	values, _ := meta[acpmeta.TerminalOutputDeltaKey].(map[string]any)
	terminalID, _ := values["terminal_id"].(string)
	data, _ := values["data"].(string)
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" || data == "" {
		return acpmeta.TerminalOutput{}, false
	}
	return acpmeta.TerminalOutput{TerminalID: terminalID, Data: data}, true
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
