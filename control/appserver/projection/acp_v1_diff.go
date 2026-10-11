package projection

import (
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// ToolContentForACPv1 projects structured diffs as standard text because v1
// cannot represent affected-file operations or a rendered patch. Whole-file v1
// diffs retain their original meaning. Canonical and public Control DTOs retain
// all structured facts; this conversion is only for the stable ACP boundary.
func ToolContentForACPv1(in []eventstream.ToolCallContent) []eventstream.ToolCallContent {
	if in == nil {
		return nil
	}
	out := make([]eventstream.ToolCallContent, len(in))
	copy(out, in)
	for i, item := range in {
		if item.Type == "diff" && item.Changes != nil {
			out[i] = eventstream.ToolCallContent{Type: "content", Content: eventstream.TextContent{Type: "text", Text: session.StructuredDiffText(item.Changes, item.Patch)}, Meta: item.Meta}
		}
	}
	return out
}
