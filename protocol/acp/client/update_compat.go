package client

import "github.com/caelis-labs/caelis/protocol/acp/metautil"

// NormalizeInboundUpdate consumes maintained provider compatibility metadata
// before an ACP update branches into canonical and native projections. Standard
// ACP fields remain authoritative; compatibility aliases only supply missing
// display metadata and never flow past this boundary.
func NormalizeInboundUpdate(update Update) Update {
	switch typed := update.(type) {
	case ContentChunk:
		typed.Meta = metautil.NormalizeTerminalOutput(typed.Meta)
		return typed
	case ToolCall:
		typed.Meta = metautil.NormalizeTerminalOutput(typed.Meta)
		return typed
	case ToolCallUpdate:
		typed.Meta = metautil.NormalizeTerminalOutput(typed.Meta)
		return typed
	default:
		return update
	}
}
