package eventstream

import "github.com/caelis-labs/caelis/protocol/acp/schema"

// ToolCallUpdateFromEnvelope returns the original ACP tool_call_update payload
// when env carries one.
func ToolCallUpdateFromEnvelope(env Envelope) (schema.ToolCallUpdate, bool) {
	if env.Kind != KindSessionUpdate {
		return schema.ToolCallUpdate{}, false
	}
	update, ok := CloneUpdate(env.Update).(schema.ToolCallUpdate)
	return update, ok
}
